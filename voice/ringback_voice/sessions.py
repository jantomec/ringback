"""The voice server's session side: the calls that sessions' ringback mods place, and what
passes between a call and its session.

Each Mac's sessions reach their voice server over a Unix socket that only you can open (see
"Voice server's session side" in docs/design.md). For each briefing it makes a random call
ID, the call's secret, and asks the switchboard to ring the phone with it. The ID reaches
the phone only through Apple's push, and the phone's offer must carry it. Haiku starts on
the call's opening as soon as the switchboard takes the call, so it's written while the
phone rings. The phone can make its offer while it rings, and says when you've answered.

During the call, what the voice passes to the session waits here until the session's mod
collects it. When the session has finished with it, the mod brings its answer back as
news: the voice tells you if you're still on the call, and otherwise the session calls
again.

The session also learns how each call went: when it ends, or when it isn't answered in time,
its outcome waits here for the mod too, as a note the session reads rather than a prompt.

When the session needs your permission, or asks you questions, its mod holds the tool call
and asks here (an Ask). The call you're on with that session gets it as a note; otherwise a
call is placed to ask it. The mod waits for the answer the voice gives, and shows its own
prompt if you hang up without one. If you don't answer the call, what it asked waits for
the next call with that session you answer: a call back, or its next call.

A call you didn't answer can be called back from the phone's call history. Each call is
remembered for a day after it's forgotten, in memory only, so a restart forgets them. The
call back is a new call for the same session, with a fresh Haiku, answered at once: its
voice has the missed call's briefing, and the session's latest one if it called again
since.
"""

import asyncio
import dataclasses
import os
import re
import secrets
import string
import time
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field
from pathlib import Path

from aiohttp import ClientError, ClientSession, ClientTimeout, web

from ringback_voice.haiku import PICKED_UP, Haiku
from ringback_voice.instructions import called_back, instructions
from ringback_voice.transcript import Transcript

SOCKET = Path("~/.local/state/ringback/voice.sock").expanduser()
# Seconds a placed call waits to be answered before the session is told it wasn't and the
# call is forgotten. The phone rings for 30 s, and its push can take about 16 s to arrive
# after it leaves Wi-Fi. RINGBACK_UNANSWERED overrides it, for checks.
UNANSWERED = float(os.environ.get("RINGBACK_UNANSWERED", "90"))
# What a session asks is kept this long after it's answered, so its mod can still read the
# answer, then forgotten.
KEEP = 600
# The longest tool input and context the voice gets: the mod sends at most about this much.
INPUT = 2000
CONTEXT = 1500
# Seconds a mod's request for an answer waits for one at most.
LONGEST_WAIT = 25
# Seconds a forgotten call is remembered, so you can call it back: a day.
REMEMBER = 24 * 3600
# A session whose mod hasn't collected its messages for this many seconds isn't listening:
# ringback is off in it, or it has ended. While it's on, the mod collects once a second.
LISTENING = 30


class Link:
    """What a call's voice can do with its session: pass it messages, hear its news, and
    answer what it asks."""

    def __init__(self, send: Callable[[str], None], transcript: Transcript | None = None):
        self._send = send
        self.transcript = transcript
        # False on a call back to a session that isn't listening any more: the voice can't
        # pass it anything or answer what it asked.
        self.listening = True
        # Set by the call's voice: tells it a note from the session, when you've both stopped
        # talking.
        self.notify: Callable[[str], Awaitable[None]] | None = None
        self.asks: dict[str, Ask] = {}  # what the session asks you on this call, until answered
        # Notes kept until the voice has started talking: one appended before the opening is
        # spoken would make Haiku answer it instead.
        self._notes: list[tuple[Ask | None, str]] = []
        self._open = False
        self._telling: set[asyncio.Task] = set()

    def send(self, text: str) -> None:
        self._send(text)
        if self.transcript:
            self.transcript.add("Passed to the session", text)

    async def news(self, text: str) -> None:
        if self.transcript:
            self.transcript.add("The session's answer", text)
        self.tell(f"The session: {text}")

    def listen(self, notify: Callable[[str], Awaitable[None]]) -> None:
        """The call's voice, from an offer on: notes wait until it has started talking."""
        self.notify = notify
        self._open = False

    def opened(self) -> None:
        """The voice has started talking: the notes kept for it go."""
        if not self._open:
            self._open = True
            notes, self._notes = self._notes, []
            for ask, text in notes:
                self.tell(text, ask)

    def tell(self, text: str, ask: "Ask | None" = None) -> None:
        """Tells the voice a note, or keeps it until the voice has started talking."""
        if not (self._open and self.notify):
            self._notes.append((ask, text))
            return
        if ask and self.transcript:
            self.transcript.add("The session asks", ask.details())
        telling = asyncio.create_task(self.notify(text))
        self._telling.add(telling)
        telling.add_done_callback(self._telling.discard)

    def ask(self, ask: "Ask", briefed: bool = False) -> None:
        """Puts what the session asks to this call's voice: in a note, unless the call was
        placed to ask it, so its briefing has it."""
        self.asks[ask.id] = ask
        if not briefed:
            self.tell(ask.note(), ask)

    def answer(self, id: str, outcome: str, said: str, answers: dict[str, str] | None = None) -> bool:
        """Gives the session your answer, once you've heard the voice say it; False if it no
        longer waits for one."""
        ask = self.asks.pop(id, None)
        if not ask or not ask.settle(outcome, said, answers):
            return False
        if self.transcript:
            self.transcript.add("Answered the session", ask.answered())
        return True

    def drop(self, ask: "Ask", why: str) -> None:
        """Takes back what the session asked, which no longer waits for this call: withdrawn,
        or answered elsewhere. The voice is told, if it was told of it."""
        if self.asks.pop(ask.id, None) is None:
            return
        kept = [(a, t) for a, t in self._notes if a is not ask]
        if len(kept) == len(self._notes):
            self.tell(
                f"The session no longer needs an answer to {ask.name}: {why}. Don't put it to the developer "
                "any more; if you were asking it, say so briefly."
            )
        self._notes = kept
        if self.transcript:
            self.transcript.add("No longer asked", f"{ask.name}: {why}")


@dataclass
class Ask:
    """A permission or questions a session asks you. Its mod holds the tool call meanwhile,
    and shows its own prompt if the call brings no answer."""

    id: str  # short and safe to speak; the voice answers with it
    kind: str  # "permission" or "question"
    session: str
    request: dict  # as the mod sent it, checked
    outcome: str | None = None  # allow, deny or answered; none if you hung up without one
    said: str | None = None  # your words, as the voice heard them
    answers: dict[str, str] | None = None  # each question's answer
    settled: asyncio.Event = field(default_factory=asyncio.Event, repr=False)

    @property
    def name(self) -> str:
        return f"{self.kind} {self.id}"

    def settle(self, outcome: str, said: str | None = None, answers: dict[str, str] | None = None) -> bool:
        if self.outcome:
            return False
        self.outcome, self.said, self.answers = outcome, said, answers
        self.settled.set()
        return True

    def details(self) -> str:
        """What it asks, for the voice and the transcript."""
        r = self.request
        if self.kind == "permission":
            text = f"Permission {self.id}: the session wants to use its tool {r['tool']}."
            if r["description"].strip():
                text += f" {r['description'].strip()}"
            return text + (f"\nIts input:\n{r['input'].strip()}" if r["input"].strip() else "")
        lines = [f"Question {self.id}, with {len(r['questions'])} question(s) for the developer:"]
        for n, q in enumerate(r["questions"], 1):
            line = f"{n}. {q['question'].strip()}"
            if q["header"]:
                line += f" ({q['header'].strip()})"
            if q["options"]:
                options = "; ".join(
                    f'"{o["label"]}"' + (f": {o['description']}" if o["description"] else "") for o in q["options"]
                )
                line += f" Options: {options}."
                line += " They can choose more than one." if q["multiSelect"] else ""
            lines.append(line)
        return "\n".join(lines)

    def context(self) -> str:
        context = self.request["context"].strip()
        return f"\nWhat the session was doing, in its own last words:\n{context}" if context else ""

    def note(self) -> str:
        """The note that puts it to a call's voice."""
        how = (
            "answer_permission, once they've clearly said yes or no"
            if self.kind == "permission"
            else "answer_question, once they've clearly chosen or answered"
        )
        return (
            f"The session asks, and waits for the answer.\n{self.details()}{self.context()}\n"
            f"Put it to the developer, then answer with {how}."
        )

    def briefing(self) -> str:
        """The briefing of a call placed to ask it."""
        what = "needs the developer's permission" if self.kind == "permission" else "has questions for the developer"
        return (
            f"The session has stopped because it {what}, and this call is about that. Open the call by "
            f"asking it.\n\n{self.details()}\n{self.context()}"
        )

    def answered(self) -> str:
        """The answer, for the transcript and the log."""
        said = f' (they said "{self.said}")' if self.said else ""
        if self.kind == "permission":
            return f"{self.name}: {self.outcome}{said}"
        answers = "; ".join(f'{q} "{a}"' for q, a in (self.answers or {}).items())
        return f"{self.name}: {answers or self.outcome}{said}"

    def match(self, given: object) -> tuple[dict[str, str], list[str]]:
        """The voice's answers as {question: answer}, with an option's label where the answer
        is one (labels joined by ", " where several can be chosen), and the questions left
        unanswered. Answers are matched to the questions by their text, or else in order."""
        pairs = [
            (str(a.get("question", "")), str(a.get("answer", "")).strip())
            for a in (given if isinstance(given, list) else [])
            if isinstance(a, dict)
        ]
        questions = self.request["questions"]
        answers, missing = {}, []
        for n, q in enumerate(questions):
            answer = next((a for k, a in pairs if plain(k) in (plain(q["question"]), plain(q["header"]))), None)
            if answer is None and len(pairs) == len(questions):
                answer = pairs[n][1]
            if not answer:
                missing.append(q["question"])
                continue
            labels = {plain(o["label"]): o["label"] for o in q["options"]}
            chosen = [plain(part) for part in answer.split(",")] if q["multiSelect"] else [plain(answer)]
            if all(c in labels for c in chosen):
                answer = ", ".join(labels[c] for c in chosen)
            answers[q["question"]] = answer
        return answers, missing


def plain(text: str) -> str:
    """Text without case or punctuation, to compare what Haiku wrote with what was asked."""
    return " ".join(re.findall(r"[a-z0-9]+", text.lower()))


@dataclass
class Call:
    id: str
    session: str
    project: str
    cwd: str  # the session's working directory, where the transcript goes
    subject: str
    briefing: str
    placed: float
    answered_at: float | None = None  # when you answered (time.monotonic())
    link: Link | None = field(default=None, repr=False)  # to its session, made when it's placed
    # Haiku, writing the opening while the phone rings, for the call's voice; closed when the
    # call ends or is forgotten.
    haiku: Haiku | None = field(default=None, repr=False)
    deadline: asyncio.Task | None = field(default=None, repr=False)  # forgets it if it isn't answered
    at: float = field(default_factory=time.time)  # when it was placed, by the clock, for how long ago
    asked: tuple[str, ...] = ()  # what the session asks in its briefing, by name ("permission abcd")
    back: "Call | None" = field(default=None, repr=False)  # on a call back: the call it calls back


class Calls:
    """The calls placed and not yet ended, and what waits for each session. The Mac stays
    awake while there are calls."""

    def __init__(self, switchboard: str | None, voice: str, log: Callable[[str], None]):
        self._switchboard = switchboard
        self._voice = voice  # this server's address on Nebula, which the phone calls
        self._log = log
        self._calls: dict[str, Call] = {}
        self._waiting: dict[str, list[dict]] = {}  # messages for each session
        self._asks: dict[str, Ask] = {}  # what sessions ask you, by its id
        # What each session asked on calls you didn't answer, by id, until you answer one of
        # its calls or it's withdrawn or answered elsewhere.
        self._pool: dict[str, dict[str, Ask]] = {}
        self._past: dict[str, tuple[Call, float]] = {}  # forgotten calls, and when, for a day
        self._seen: dict[str, float] = {}  # when each session's mod last collected (time.monotonic())
        self._keeping: set[asyncio.Task] = set()  # each waits for an answer, then forgets it
        self._asking = asyncio.Lock()
        self._awake: asyncio.subprocess.Process | None = None
        self._closing: set[asyncio.Task] = set()  # Haikus of forgotten calls, while they close

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
        # The transcript joins the link when you answer.
        link = Link(lambda text: self.send(session, text, link.transcript.relative if link.transcript else None))
        call.link = link
        self._calls[call.id] = call
        self._log(f"call {call.id[:8]}: placed for {project} ({session[:8]}): {subject}")
        # Haiku thinks of what to say while the phone rings, so the voice can start as soon
        # as you answer.
        say = lambda line: self._log(f"call {call.id[:8]}: {line}")
        haiku = Haiku(instructions(briefing, session=True), say, tools=True)
        try:
            await haiku.start()
            call.haiku = haiku
        except OSError as e:
            self._log(f"call {call.id[:8]}: Haiku didn't start ({e}); the call starts its own")
            await haiku.close()
        call.deadline = asyncio.create_task(self._deadline(call))
        await self._stay_awake()
        return call

    def offer(self, call_id: str) -> Call | None:
        """The call an offer is for, if it's one this server placed. The offer can come while
        the phone still rings, and a call you haven't answered stays placed until it's
        forgotten, so the phone can offer again."""
        return self._calls.get(call_id)

    async def back(self, call_id: str) -> Call | None:
        """The call you make by calling call_id back from the phone, which it answers at once:
        a call this server placed and you didn't answer, remembered for a day after it was
        forgotten, or still placed. None if there's no such call. A call back already on is
        given as it is, so a new offer replaces the earlier one, as for any call."""
        self._expire()
        if call := self._calls.get(call_id):
            if call.back or call.answered_at is not None:
                return call
            # Still placed: the call back takes its place, so it's never told it wasn't
            # answered, and what it asked waits for the call back.
            self._calls.pop(call.id)
            if call.deadline:
                call.deadline.cancel()
            self._close_haiku(call)
            pool = self._pool.setdefault(call.session, {})
            pool.update(call.link.asks)
            call.link.asks.clear()
            missed = dataclasses.replace(call, answered_at=None, link=None, haiku=None, deadline=None)
        elif past := self._past.pop(call_id, None):
            missed = past[0]
        else:
            return None
        listening = time.monotonic() - self._seen.get(missed.session, float("-inf")) < LISTENING
        # The session's latest call, if it placed one after this.
        later = max(
            [c for c in self._calls.values() if c.session == missed.session and not c.back]
            + [c for c, _ in self._past.values() if c.session == missed.session],
            key=lambda c: c.at, default=None,
        )  # fmt: skip
        since = (later.briefing, time.time() - later.at) if later and later.at > missed.at else None
        # What the missed call's briefing asks and no longer waits for an answer; a session
        # that isn't listening waits for none.
        waiting = {ask.name for ask in self._pool.get(missed.session, {}).values()} if listening else set()
        settled = [name for name in missed.asked if name not in waiting]
        briefing = called_back(missed.briefing, time.time() - missed.at, since, settled)
        call = dataclasses.replace(missed, briefing=briefing, placed=time.monotonic(), back=missed)
        link = Link(lambda text: self.send(call.session, text, link.transcript.relative if link.transcript else None))
        link.listening = listening
        call.link = link
        self._calls[call.id] = call
        how = ("the session listens" if listening else "the session isn't listening") + (
            ", and called again since" if since else ""
        )
        self._log(f"call {call.id[:8]}: called back about {call.subject!r}, {how}")
        say = lambda line: self._log(f"call {call.id[:8]}: {line}")
        haiku = Haiku(instructions(briefing, session=True, listening=listening), say, tools=listening, opening=PICKED_UP)
        try:
            await haiku.start()
            call.haiku = haiku
        except OSError as e:
            self._log(f"call {call.id[:8]}: Haiku didn't start ({e}); the call starts its own")
            await haiku.close()
        call.deadline = asyncio.create_task(self._deadline(call))
        await self._stay_awake()
        return call

    def answered(self, call_id: str) -> Call | None:
        """Marks an offered call you've answered, once, and starts its transcript, so a call
        you don't answer leaves none. What its session asked on calls you didn't answer goes
        to this one."""
        call = self._calls.get(call_id)
        if call and call.answered_at is None:
            call.answered_at = time.monotonic()
            if call.cwd and call.link:
                try:
                    call.link.transcript = Transcript(Path(call.cwd), call.subject, call.briefing)
                except OSError as e:
                    self._log(f"call {call_id[:8]}: no transcript ({e})")
            if call.link and call.link.listening:
                for ask in self._pool.pop(call.session, {}).values():
                    # In a note, unless the briefing asks it: a call back's can.
                    call.link.ask(ask, briefed=ask.name in call.asked)
                    self._log(f"call {call_id[:8]}: {ask.name}, asked on a call you didn't answer, goes to this one")
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
        self._seen[session] = time.monotonic()  # it's listening
        return self._waiting.pop(session, [])

    async def news(self, session: str, text: str) -> bool:
        """Tells the call you're on with a session its news; False if there's no such call."""
        for call in self._calls.values():
            # Not while the phone rings: the voice can tell you only once you've answered.
            if call.session == session and call.answered_at is not None and call.link and call.link.notify:
                await call.link.news(text)
                return True
        return False

    async def ask(self, session: str, kind: str, request: dict) -> tuple[Ask, str]:
        """Puts what a session asks to the call you're on with it ("live"), or to its call
        that's ringing, for once you answer ("placed"); otherwise places a call to ask it
        ("placed"). Raises RuntimeError if no call can be placed."""
        # One at a time: parallel tool calls can ask at once, and should share one call.
        async with self._asking:
            ask = Ask(new_id(self._asks), kind, session, request)
            # Not a call back whose voice can't answer the session.
            ours = [c for c in self._calls.values() if c.session == session and c.link.listening]
            if call := ([c for c in ours if c.answered_at is not None] or ours or [None])[-1]:
                call.link.ask(ask)
                how = "live" if call.answered_at is not None else "placed"
            else:
                r = request
                call = await self.place(session, r["project"], r["cwd"], r["subject"], ask.briefing())
                call.link.ask(ask, briefed=True)
                call.asked = (ask.name,)
                how = "placed"
            self._asks[ask.id] = ask
        self._log(f"call {call.id[:8]}: {ask.name} for session {session[:8]}, {how}: {ask.details()[:100]!r}")
        keeping = asyncio.create_task(self._keep(ask))
        self._keeping.add(keeping)
        keeping.add_done_callback(self._keeping.discard)
        return ask, how

    def asked(self, session: str, kind: str, id: str) -> Ask | None:
        """What a session asked, by its id, until it's withdrawn or forgotten."""
        ask = self._asks.get(id)
        return ask if ask and ask.session == session and ask.kind == kind else None

    def settle(self, ask: Ask, outcome: str, said: str, answers: dict[str, str] | None = None) -> bool:
        """Records an answer given outside a call (by a tool); False if it has one already."""
        if not ask.settle(outcome, said, answers):
            return False
        self._pool.get(ask.session, {}).pop(ask.id, None)
        for call in self._calls.values():
            call.link.drop(ask, f"it was answered outside the call, {ask.answered()}")
        return True

    def withdraw(self, ask: Ask) -> None:
        """The session's mod no longer waits for an answer, so a call's voice no longer asks."""
        self._asks.pop(ask.id, None)
        self._pool.get(ask.session, {}).pop(ask.id, None)
        for call in self._calls.values():
            call.link.drop(ask, "it withdrew the question" if ask.kind == "question" else "it withdrew the request")
        ask.settle("withdrawn")  # wakes those waiting for it, who find it gone

    async def _keep(self, ask: Ask) -> None:
        """Logs the answer, and forgets it a while after."""
        await ask.settled.wait()
        self._log(f"{ask.answered()} (session {ask.session[:8]})")
        await asyncio.sleep(KEEP)
        if self._asks.get(ask.id) is ask:
            del self._asks[ask.id]

    async def _deadline(self, call: Call) -> None:
        """Forgets a call that isn't answered in time, which tells its session."""
        await asyncio.sleep(UNANSWERED)
        if call.answered_at is None and self._calls.get(call.id) is call:
            self._log(f"call {call.id[:8]}: not answered, forgotten")
            await self._forget(call)

    async def _forget(self, call: Call) -> None:
        """Drops a call, answered and ended or never answered: the one place a call goes. Its
        session is told how it went, the Haiku that wrote its opening is closed, and it's
        remembered for a day, so you can call it back."""
        self._calls.pop(call.id, None)
        if call.deadline and call.deadline is not asyncio.current_task():
            call.deadline.cancel()
        self._close_haiku(call)
        if call.answered_at is None and call.link.listening:
            # You didn't answer: what it was asking waits for the next call with its session
            # you answer, a call back or its next call, while the session's mod waits.
            self._pool.setdefault(call.session, {}).update(call.link.asks)
        else:
            # You hung up without answering it: the session's mod asks you itself.
            for ask in list(call.link.asks.values()):
                ask.settle("none")
        call.link.asks.clear()
        transcript = call.link.transcript.relative if call.link.transcript else None
        self.send(call.session, outcome(call, transcript), transcript, kind="outcome")
        # What's remembered is the call as it was placed, also after a call back.
        self._past[call.id] = (
            call.back or dataclasses.replace(call, answered_at=None, link=None, haiku=None, deadline=None),
            time.time(),
        )
        self._expire()
        await self._stay_awake()

    def _close_haiku(self, call: Call) -> None:
        if call.haiku:
            closing = asyncio.create_task(call.haiku.close())
            self._closing.add(closing)
            closing.add_done_callback(self._closing.discard)
            call.haiku = None

    def _expire(self) -> None:
        """Forgets the calls forgotten more than a day ago."""
        now = time.time()
        self._past = {id: (call, at) for id, (call, at) in self._past.items() if now - at < REMEMBER}

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
    if call.back:
        what = f'"{call.subject}"' if call.subject else "an earlier call"
        if call.answered_at is None:
            return f"The developer called back about {what}, but the call didn't connect."
        text = f"The developer called back about {what} and it lasted {duration(time.monotonic() - call.answered_at)}."
        return f"{text} Its transcript: {transcript}" if transcript else text
    if call.answered_at is None:
        took = duration(time.monotonic() - call.placed)
        return f"{about} wasn't answered within {took}: declined, missed, or its audio didn't connect."
    text = f"{about} was answered and lasted {duration(time.monotonic() - call.answered_at)}."
    return f"{text} Its transcript: {transcript}" if transcript else text


def new_id(taken: dict) -> str:
    """Four random lowercase letters, not taken yet: short, and safe for Haiku to write."""
    while (id := "".join(secrets.choice(string.ascii_lowercase) for _ in range(4))) in taken:
        pass
    return id


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
      told the call you're on, false if the session should call you;
    - `POST /sessions/{id}/permissions` asks your permission for a tool call, and
      `POST /sessions/{id}/questions` asks you questions, each with their options. Either is
      put to the call you're on with the session (`"call": "live"`) or to one placed to ask
      it (`"placed"`), and answered with its id; 502 if no call can be placed;
    - `GET .../{ask}?wait=S` waits up to S seconds (at most 25) for the answer: a permission's
      `verdict` (allow, deny, none or pending), or the questions' `status` (answered, none or
      pending) and `answers`, with your `said`. None: you hung up without answering it. A
      call you didn't answer leaves it pending, for the next call with the session you answer;
    - `POST .../{ask}` records an answer, as the voice does (for tools);
    - `DELETE .../{ask}` withdraws it: the session no longer waits for an answer.
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

    def asks(kind: str) -> list[web.RouteDef]:
        """The endpoints for what sessions ask: permissions, or questions."""

        async def ask(request: web.Request) -> web.Response:
            try:
                body = await request.json()
                checked = permission(body) if kind == "permission" else questions(body)
            except ValueError:
                needs = "tool" if kind == "permission" else "questions, each with a question and options with labels"
                return web.json_response({"error": f"needs {needs}"}, status=400)
            try:
                asked, how = await calls.ask(request.match_info["id"], kind, checked)
            except RuntimeError as e:
                return web.json_response({"error": str(e)}, status=502)
            return web.json_response({kind: asked.id, "call": how})

        def asked(request: web.Request) -> Ask:
            if found := calls.asked(request.match_info["id"], kind, request.match_info["ask"]):
                return found
            raise web.HTTPNotFound(text='{"error": "unknown"}', content_type="application/json")

        def answer(a: Ask) -> dict:
            if kind == "permission":
                return {"verdict": a.outcome or "pending", "said": a.said}
            return {"status": a.outcome or "pending", "answers": a.answers, "said": a.said}

        async def wait(request: web.Request) -> web.Response:
            a = asked(request)
            try:
                seconds = float(request.query.get("wait", 0))
            except ValueError:
                seconds = 0
            seconds = min(seconds, LONGEST_WAIT) if seconds > 0 else 0  # also NaN
            try:
                await asyncio.wait_for(a.settled.wait(), seconds)
            except TimeoutError:
                pass
            asked(request)  # 404 if it was withdrawn meanwhile
            return web.json_response(answer(a))

        async def record(request: web.Request) -> web.Response:
            a = asked(request)
            try:
                body = await request.json()
                if not isinstance(body, dict):
                    raise ValueError
                said = str(body.get("said") or "")
                if kind == "permission":
                    outcome, answers = body.get("verdict"), None
                    if outcome not in ("allow", "deny"):
                        raise ValueError
                else:
                    outcome, answers = "answered", body.get("answers")
                    if not isinstance(answers, dict) or not all(isinstance(v, str) for v in answers.values()):
                        raise ValueError
            except ValueError:
                needs = "verdict, allow or deny" if kind == "permission" else "answers, {question: answer}"
                return web.json_response({"error": f"needs {needs}"}, status=400)
            if not calls.settle(a, outcome, said, answers):
                return web.json_response({"error": f"answered already: {a.outcome}"}, status=409)
            return web.json_response(answer(a))

        async def withdraw(request: web.Request) -> web.Response:
            calls.withdraw(asked(request))
            return web.json_response({"ok": True})

        path = f"/sessions/{{id}}/{kind}s"
        return [
            web.post(path, ask),
            web.get(path + "/{ask}", wait),
            web.post(path + "/{ask}", record),
            web.delete(path + "/{ask}", withdraw),
        ]

    app = web.Application()
    app.add_routes([
        web.post("/calls", place),
        web.get("/sessions/{id}/messages", collect),
        web.post("/sessions/{id}/messages", leave),
        web.post("/sessions/{id}/news", news),
        *asks("permission"),
        *asks("question"),
    ])  # fmt: skip
    return app


def permission(body: object) -> dict:
    """A permission request as a mod sends it, checked, with its input and context cut to
    what the voice gets; raises ValueError if it names no tool."""
    if not isinstance(body, dict) or not isinstance(body.get("tool"), str) or not body["tool"].strip():
        raise ValueError
    keys = ("tool", "description", "input", "context", "project", "cwd", "subject")
    checked = {k: str(body.get(k) or "") for k in keys}
    return checked | {"input": cut(checked["input"], INPUT), "context": cut(checked["context"], CONTEXT)}


def questions(body: object) -> dict:
    """Questions as a mod sends them (those of Claude Code's AskUserQuestion), checked; raises
    ValueError unless there are 1 to 4, each with its text and options with labels."""
    asked = body.get("questions") if isinstance(body, dict) else None
    if not isinstance(asked, list) or not 1 <= len(asked) <= 4:
        raise ValueError
    checked = []
    for q in asked:
        if not isinstance(q, dict) or not isinstance(q.get("question"), str) or not q["question"].strip():
            raise ValueError
        options = q.get("options") or []
        if not isinstance(options, list):
            raise ValueError
        if not all(isinstance(o, dict) and isinstance(o.get("label"), str) for o in options):
            raise ValueError
        checked.append({
            "question": q["question"],
            "header": str(q.get("header") or ""),
            "options": [{"label": o["label"], "description": str(o.get("description") or "")} for o in options],
            "multiSelect": q.get("multiSelect") is True,
        })  # fmt: skip
    rest = {k: str(body.get(k) or "") for k in ("project", "cwd", "subject")}
    return rest | {"questions": checked, "context": cut(str(body.get("context") or ""), CONTEXT)}


def cut(text: str, most: int) -> str:
    return text if len(text) <= most else text[: most - 1] + "…"


async def listen(app: web.Application, path: Path = SOCKET) -> web.AppRunner:
    """Serves app on a Unix socket at path, which only you can open."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.unlink(missing_ok=True)
    runner = web.AppRunner(app)
    await runner.setup()
    old = os.umask(0o177)  # the socket is created readable and writable by you alone
    try:
        await web.UnixSite(runner, str(path)).start()
    finally:
        os.umask(old)
    return runner
