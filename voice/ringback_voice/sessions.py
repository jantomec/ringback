"""The voice server's session side: the calls that sessions' ringback mods place, and what
passes between a call and its session.

Each Mac's sessions reach their voice server over a Unix socket that only you can open (see
"Voice server's session side" in docs/design.md). For each briefing it makes a random call
ID, the call's secret, and asks the switchboard to ring the phone with it. The ID reaches
the phone only through Apple's push, and the offer that answers the call must carry it.

During the call, what the voice passes to the session waits here until the session's mod
collects it. When the session has finished with it, the mod brings its answer back as
news: the voice tells you if you're still on the call, and otherwise the session calls
again.

The session also learns how each call went: when it ends, or when it isn't answered in time,
its outcome waits here for the mod too, as a note the session reads rather than a prompt.
"""

import asyncio
import os
import secrets
import time
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field
from pathlib import Path

from aiohttp import ClientError, ClientSession, ClientTimeout, web

from ringback_voice.transcript import Transcript

SOCKET = Path("~/.local/state/ringback/voice.sock").expanduser()
# Seconds a placed call waits to be answered before the session is told it wasn't and the
# call is forgotten. The phone rings for 30 s, and its push can take about 16 s to arrive
# after it leaves Wi-Fi. RINGBACK_UNANSWERED overrides it, for checks.
UNANSWERED = float(os.environ.get("RINGBACK_UNANSWERED", "90"))


class Link:
    """What a call's voice can do with its session: pass it messages and hear its news."""

    def __init__(self, send: Callable[[str], None], transcript: Transcript | None = None):
        self._send = send
        self.transcript = transcript
        # Set by the call: tells the voice the session's news, when you've both stopped
        # talking.
        self.notify: Callable[[str], Awaitable[None]] | None = None

    def send(self, text: str) -> None:
        self._send(text)
        if self.transcript:
            self.transcript.add("Passed to the session", text)

    async def news(self, text: str) -> None:
        if self.transcript:
            self.transcript.add("The session's answer", text)
        if self.notify:
            await self.notify(text)


@dataclass
class Call:
    id: str
    session: str
    project: str
    cwd: str  # the session's working directory, where the transcript goes
    subject: str
    briefing: str
    placed: float
    answered_at: float | None = None  # time.monotonic() when it was answered
    link: Link | None = field(default=None, repr=False)
    deadline: asyncio.Task | None = field(default=None, repr=False)  # forgets it if it isn't answered


class Calls:
    """The calls placed and not yet ended, and what waits for each session. The Mac stays
    awake while there are calls."""

    def __init__(self, switchboard: str | None, voice: str, log: Callable[[str], None]):
        self._switchboard = switchboard
        self._voice = voice  # this server's address on Nebula, which the phone calls
        self._log = log
        self._calls: dict[str, Call] = {}
        self._waiting: dict[str, list[dict]] = {}  # messages for each session
        self._awake: asyncio.subprocess.Process | None = None

    async def place(self, session: str, project: str, cwd: str, subject: str, briefing: str) -> Call:
        """Asks the switchboard to ring the phone; raises RuntimeError if it can't."""
        if not self._switchboard:
            raise RuntimeError("the voice server was started without --switchboard")
        call = Call(secrets.token_urlsafe(24), session, project, cwd, subject, briefing, time.monotonic())
        body = {"project": project, "subject": subject, "voice": f"http://{self._voice}", "call_id": call.id}
        try:
            async with ClientSession(timeout=ClientTimeout(total=10)) as http:
                async with http.post(f"http://{self._switchboard}/call", json=body) as response:
                    if response.status != 200:
                        raise RuntimeError(f"the switchboard answered {response.status}")
        except (ClientError, TimeoutError) as e:
            raise RuntimeError(f"can't reach the switchboard at {self._switchboard}: {e or type(e).__name__}") from e
        self._calls[call.id] = call
        self._log(f"call {call.id[:8]}: placed for {project} ({session[:8]}): {subject}")
        call.deadline = asyncio.create_task(self._deadline(call))
        await self._stay_awake()
        return call

    def answer(self, call_id: str) -> Call | None:
        """The call an offer answers, if it's one this server placed, with its link to the
        session."""
        call = self._calls.get(call_id)
        if call:
            if call.answered_at is None:
                call.answered_at = time.monotonic()
            transcript = None
            if call.cwd:
                try:
                    transcript = Transcript(Path(call.cwd), call.subject, call.briefing)
                except OSError as e:
                    self._log(f"call {call_id[:8]}: no transcript ({e})")
            call.link = Link(
                lambda text: self.send(call.session, text, transcript.relative if transcript else None), transcript
            )
        return call

    async def end(self, call_id: str) -> None:
        if call := self._calls.get(call_id):
            await self._forget(call)

    def send(self, session: str, text: str, transcript: str | None = None, kind: str = "message") -> None:
        """Keeps a message for a session until its mod collects it: what the voice passed on
        (`message`), or how a call went (`outcome`), which the session reads as a note."""
        self._waiting.setdefault(session, []).append({"kind": kind, "text": text, "transcript": transcript})
        self._log(f"{kind} for session {session[:8]}: {text!r}")

    def collect(self, session: str) -> list[dict]:
        return self._waiting.pop(session, [])

    async def news(self, session: str, text: str) -> bool:
        """Tells the call you're on with a session its news; False if there's no such call."""
        for call in self._calls.values():
            if call.session == session and call.link and call.link.notify:
                self._log(f"call {call.id[:8]}: news from the session: {text[:80]!r}")
                await call.link.news(text)
                return True
        return False

    async def _deadline(self, call: Call) -> None:
        """Forgets a call that isn't answered in time, which tells its session."""
        await asyncio.sleep(UNANSWERED)
        if call.answered_at is None and self._calls.get(call.id) is call:
            self._log(f"call {call.id[:8]}: not answered, forgotten")
            await self._forget(call)

    async def _forget(self, call: Call) -> None:
        """Drops a call, answered and ended or never answered: the one place a call goes. Its
        session is told how it went."""
        self._calls.pop(call.id, None)
        transcript = call.link.transcript.relative if call.link and call.link.transcript else None
        self.send(call.session, outcome(call, transcript), transcript, kind="outcome")
        await self._stay_awake()

    async def _stay_awake(self) -> None:
        """Holds a sleep assertion while any call is placed or in progress."""
        if self._calls and not self._awake:
            self._awake = await asyncio.create_subprocess_exec("caffeinate", "-i", "-w", str(os.getpid()))
        elif not self._calls and self._awake:
            self._awake.terminate()
            await self._awake.wait()
            self._awake = None


def outcome(call: Call, transcript: str | None) -> str:
    """How a call went, for its session to read. If it wasn't answered, the voice server
    can't tell why; the switchboard's log can."""
    about = f'The call about "{call.subject}"' if call.subject else "The call"
    if call.answered_at is None:
        took = duration(time.monotonic() - call.placed)
        return f"{about} wasn't answered within {took}: declined, missed, or its audio didn't connect."
    text = f"{about} was answered and lasted {duration(time.monotonic() - call.answered_at)}."
    return f"{text} Its transcript: {transcript}" if transcript else text


def duration(seconds: float) -> str:
    """Seconds as written for a person: "45 s", "1 min 5 s", "2 min"."""
    minutes, seconds = divmod(round(seconds), 60)
    if not minutes:
        return f"{seconds} s"
    return f"{minutes} min {seconds} s" if seconds else f"{minutes} min"


def session_side(calls: Calls) -> web.Application:
    """The endpoints the mods call:
    - `POST /calls` places a call with a session's briefing;
    - `GET /sessions/{id}/messages` collects what waits for a session: each message has a
      `kind`, `message` for what the voice passed on and `outcome` for how a call went, its
      `text`, and the call's `transcript` if there is one;
    - `POST /sessions/{id}/messages` leaves one, as the voice does (for tools);
    - `POST /sessions/{id}/news` brings a session's answer: `{"live": true}` if the voice
      told the call you're on, false if the session should call you.
    """

    async def body_of(request: web.Request, *keys: str) -> dict:
        body = await request.json()
        if not isinstance(body, dict) or any(not isinstance(body.get(k), str) for k in keys):
            raise ValueError
        return body

    async def place(request: web.Request) -> web.Response:
        try:
            body = await body_of(request, "session_id", "project", "briefing")
        except ValueError:
            return web.json_response({"error": "needs session_id, project and briefing"}, status=400)
        try:
            await calls.place(
                body["session_id"], body["project"], str(body.get("cwd", "")), str(body.get("subject", "")), body["briefing"]
            )
        except RuntimeError as e:
            return web.json_response({"error": str(e)}, status=502)
        return web.json_response({"ok": True})

    async def collect(request: web.Request) -> web.Response:
        return web.json_response({"messages": calls.collect(request.match_info["id"])})

    async def leave(request: web.Request) -> web.Response:
        try:
            body = await body_of(request, "text")
        except ValueError:
            return web.json_response({"error": "needs text"}, status=400)
        calls.send(request.match_info["id"], body["text"], body.get("transcript"))
        return web.json_response({"ok": True})

    async def news(request: web.Request) -> web.Response:
        try:
            body = await body_of(request, "text")
        except ValueError:
            return web.json_response({"error": "needs text"}, status=400)
        return web.json_response({"live": await calls.news(request.match_info["id"], body["text"])})

    app = web.Application()
    app.add_routes([
        web.post("/calls", place),
        web.get("/sessions/{id}/messages", collect),
        web.post("/sessions/{id}/messages", leave),
        web.post("/sessions/{id}/news", news),
    ])  # fmt: skip
    return app


async def listen(app: web.Application) -> web.AppRunner:
    """Serves app on SOCKET, which only you can open."""
    SOCKET.parent.mkdir(parents=True, exist_ok=True)
    SOCKET.unlink(missing_ok=True)
    runner = web.AppRunner(app)
    await runner.setup()
    old = os.umask(0o177)  # the socket is created readable and writable by you alone
    try:
        await web.UnixSite(runner, str(SOCKET)).start()
    finally:
        os.umask(old)
    return runner
