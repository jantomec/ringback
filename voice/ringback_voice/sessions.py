"""The voice server's session side: the calls that sessions' ringback mods place.

Each Mac's sessions reach their voice server over a Unix socket that only you can open (see
"Voice server's session side" in docs/design.md). For each briefing it makes a random call
ID, the call's secret, and asks the switchboard to ring the phone with it. The ID reaches
the phone only through Apple's push, and the offer that answers the call must carry it.
"""

import asyncio
import os
import secrets
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from aiohttp import ClientError, ClientSession, ClientTimeout, web

SOCKET = Path("~/.local/state/ringback/voice.sock").expanduser()
UNANSWERED = 15 * 60  # seconds a placed call waits to be answered before it's forgotten


@dataclass
class Call:
    id: str
    session: str
    project: str
    subject: str
    briefing: str
    placed: float
    answered: bool = False


class Calls:
    """The calls placed and not yet ended. The Mac stays awake while there are any."""

    def __init__(self, switchboard: str | None, voice: str, log: Callable[[str], None]):
        self._switchboard = switchboard
        self._voice = voice  # this server's address on Nebula, which the phone calls
        self._log = log
        self._calls: dict[str, Call] = {}
        self._awake: asyncio.subprocess.Process | None = None

    async def place(self, session: str, project: str, subject: str, briefing: str) -> Call:
        """Asks the switchboard to ring the phone; raises RuntimeError if it can't."""
        if not self._switchboard:
            raise RuntimeError("the voice server was started without --switchboard")
        self._forget_unanswered()
        call = Call(secrets.token_urlsafe(24), session, project, subject, briefing, time.monotonic())
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
        await self._stay_awake()
        return call

    def answer(self, call_id: str) -> Call | None:
        """The call an offer answers, if it's one this server placed."""
        self._forget_unanswered()
        call = self._calls.get(call_id)
        if call:
            call.answered = True
        return call

    async def end(self, call_id: str) -> None:
        self._calls.pop(call_id, None)
        await self._stay_awake()

    def _forget_unanswered(self) -> None:
        for call in list(self._calls.values()):
            if not call.answered and time.monotonic() - call.placed > UNANSWERED:
                self._log(f"call {call.id[:8]}: not answered, forgotten")
                del self._calls[call.id]

    async def _stay_awake(self) -> None:
        """Holds a sleep assertion while any call is placed or in progress."""
        if self._calls and not self._awake:
            self._awake = await asyncio.create_subprocess_exec("caffeinate", "-i", "-w", str(os.getpid()))
        elif not self._calls and self._awake:
            self._awake.terminate()
            await self._awake.wait()
            self._awake = None


def session_side(calls: Calls) -> web.Application:
    """The endpoints the mods call: `POST /calls` places a call with a session's briefing."""

    async def place(request: web.Request) -> web.Response:
        try:
            body = await request.json()
            session, project = str(body["session_id"]), str(body["project"])
            subject, briefing = str(body.get("subject", "")), str(body["briefing"])
        except (ValueError, KeyError):
            return web.json_response({"error": "needs session_id, project and briefing"}, status=400)
        try:
            await calls.place(session, project, subject, briefing)
        except RuntimeError as e:
            return web.json_response({"error": str(e)}, status=502)
        return web.json_response({"ok": True})

    app = web.Application()
    app.add_routes([web.post("/calls", place)])
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
