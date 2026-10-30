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

Outside your work hours (set in the app) the switchboard holds a call instead of ringing,
and says when the hours start. The call is kept here meanwhile, without Haiku, and the
switchboard is asked again then, or within REASK s, so hours you widen apply soon. A new
briefing from its session replaces the held call's, and what the session asks goes to it, a
question too: it waits for the call, and the switchboard is asked again at once, so the phone
gets a notification. From the app's waiting calls, you can send a held call's question to
the session's own dialog instead, which Remote Control (turned on with ringback) shows in the
Claude app: the call is then forgotten.

When the session needs your permission, or asks you questions, its mod holds the tool call
and asks here (an Ask). The call you're on with that session gets it as a note; otherwise a
call is placed to ask it. The mod waits for the answer the voice gives, and shows its own
prompt if you hang up without one. If you don't answer the call, what it asked waits for
the next call with that session you answer: a call back, or its next call.

Any call can be called back from the phone's call history, answered or not. Each call is
remembered for a day after it's forgotten. The call back is a new call for the same
session, with a fresh Haiku, answered at once: its voice has the earlier call's briefing,
with a lead that says whether you talked then, and the session's latest one if it wrote a
newer one since, for a later call or a held one. Work hours don't apply to it: you called. A
held call can be called from the app too, where it shows as waiting: it's released at once,
without ringing, and its voice picks up as a call back's does.

The sessions that listen (ringback on) are the phone's Contacts: the switchboard hears which
they are, with each one's project and latest briefing, whenever that changes and every
PUBLISH s. You can call one from the app at any time: a new call under the ID the phone made,
answered at once, whose voice has the session's latest briefing, if it has one, with a lead
that says you called.

What it keeps is written to a file beside its socket on every change, and read again at
start, so a restart loses nothing that matters: held calls are held again, calls are still
remembered for calling back, and what sessions asked keeps its ids, so a mod's long poll
carries on. A call that was ringing or on during the restart has ended: its session is told
so, and what it asked waits as a missed call's does.
"""

import asyncio
import dataclasses
import json
import os
import re
import secrets
import string
import time
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path

from aiohttp import ClientError, ClientSession, ClientTimeout, web

from ringback_voice.haiku import PICKED_UP, Haiku
from ringback_voice.instructions import called_back, called_contact, called_now, instructions
from ringback_voice.transcript import Transcript

SOCKET = Path("~/.local/state/ringback/voice.sock").expanduser()
# What the voice server keeps across a restart, in the folder of its socket, so a check with a
# socket of its own has its own.
STATE = "voice-state.json"
# Seconds a placed call waits to be answered before the session is told it wasn't and the
# call is forgotten. The phone rings for 30 s, and its push can take about 16 s to arrive
# after it leaves Wi-Fi. RINGBACK_UNANSWERED overrides it, for checks.
UNANSWERED = float(os.environ.get("RINGBACK_UNANSWERED", "90"))
# Seconds a call held outside your work hours waits at most before the switchboard is asked
# again, so hours you widen apply within this long. If it can't be reached, it's asked again
# after RETRY s. RINGBACK_REASK overrides it, for checks.
REASK = float(os.environ.get("RINGBACK_REASK", "600"))
RETRY = min(60.0, REASK)
# Seconds a held call sleeps at most before it looks at the clock again. asyncio's clock
# stops while the Mac sleeps, so the wait is on the wall clock, looked at this often.
TICK = 30.0
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
# RINGBACK_LISTENING overrides it, for checks.
LISTENING = float(os.environ.get("RINGBACK_LISTENING", "30"))
# Seconds between telling the switchboard which sessions listen, for the phone's Contacts,
# when nothing changed; the switchboard drops a Mac's sessions it hasn't heard of in 90 s. A
# change goes at once, or CHANGED s after a try that failed. RINGBACK_PUBLISH overrides it.
PUBLISH = float(os.environ.get("RINGBACK_PUBLISH", "30"))
CHANGED = 5.0


class Link:
    """What a call's voice can do with its session: pass it messages, hear its news, and
    answer what it asks."""

    def __init__(self, send: Callable[[str], None], transcript: Transcript | None = None):
        self._send = send
        self.transcript = transcript
        # Its transcript's path in the project, read from the state file after a restart.
        self.transcript_path: str | None = None
        # False on a call back to a session that isn't listening any more: the voice can't
        # pass it anything or answer what it asked.
        self.listening = True
        # Set by the call's voice: tells it a note from the session, when you've both stopped
        # talking.
        self.notify: Callable[[str], Awaitable[None]] | None = None
        self.asks: dict[str, Ask] = {}  # what the session asks you on this call, until answered
        self._briefed: list[Ask] = []  # what the call's briefing asks, placed to ask it
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
        if briefed:
            self._briefed.append(ask)
        else:
            self.tell(ask.note(), ask)

    def rebriefed(self) -> None:
        """The call has a new briefing, which doesn't mention what the old one asked: the
        asks still waiting for it go to the voice as notes instead."""
        briefed, self._briefed = self._briefed, []
        # Notes about what no longer waits are old news by now.
        self._notes = [(a, t) for a, t in self._notes if a is None or self.asks.get(a.id) is a]
        for ask in briefed:
            if self.asks.get(ask.id) is ask:
                self.tell(ask.note(), ask)

    @property
    def briefed(self) -> tuple[str, ...]:
        """What the call's briefing asks, by name ("permission abcd")."""
        return tuple(ask.name for ask in self._briefed)

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

    @property
    def has_question(self) -> bool:
        """Whether the session asks questions on this call and waits for the answer."""
        return any(ask.kind == "question" for ask in self.asks.values())


@dataclass
class Ask:
    """A permission or questions a session asks you. Its mod holds the tool call meanwhile,
    and shows its own prompt if the call brings no answer."""

    id: str  # short and safe to speak; the voice answers with it
    kind: str  # "permission" or "question"
    session: str
    request: dict  # as the mod sent it, checked
    # allow, deny or answered; none if you hung up without one; terminal if you sent the held
    # call's question to the Claude app, so the session asks in its own dialog or prompt
    outcome: str | None = None
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
    placed: float  # when the phone started ringing (time.monotonic())
    answered_at: float | None = None  # when you answered (time.monotonic())
    link: Link | None = field(default=None, repr=False)  # to its session, made when it's placed
    # Haiku, writing the opening while the phone rings, for the call's voice; closed when the
    # call ends or is forgotten.
    haiku: Haiku | None = field(default=None, repr=False)
    deadline: asyncio.Task | None = field(default=None, repr=False)  # forgets it if it isn't answered
    # Outside your work hours: when the switchboard said the call can ring (ISO 8601, with an
    # offset), until it rings.
    held_until: str | None = None
    holding: asyncio.Task | None = field(default=None, repr=False)  # asks the switchboard again
    # Set while held, to ask the switchboard again at once: a question joined the call.
    reask: asyncio.Event = field(default_factory=asyncio.Event, repr=False)
    # When it was placed, or its briefing replaced while it was held, by the clock (time.time()),
    # for how long ago.
    at: float = field(default_factory=time.time)
    # Once it's forgotten and remembered: what its briefing asks, by name ("permission abcd").
    asked: tuple[str, ...] = ()
    # On a call you made: the call you called, as it was placed. A call back calls back an
    # earlier call; with `now`, you called a held call from the app while it waited; with
    # `contact`, you called the session from the app's Contacts, and it's the session's latest
    # briefing, as the call will be remembered.
    back: "Call | None" = field(default=None, repr=False)
    now: bool = False
    contact: bool = False
    # Once it's forgotten and remembered: when you last talked on it, a call back of it
    # included (time.time()); None if you never answered it.
    talked: float | None = None


class Calls:
    """The calls placed and not yet ended, and what waits for each session. The Mac stays
    awake while there are calls, held ones too, so a call held overnight can ring in the
    morning. With a state file, what it keeps is written there on every change, and read
    again by restore()."""

    def __init__(self, switchboard: str | None, voice: str, log: Callable[[str], None], state: Path | None = None):
        self._switchboard = switchboard
        self._voice = voice  # this server's address on Nebula, which the phone calls
        self._log = log
        self._state = state
        self._written: str | None = None  # what the state file holds
        self._calls: dict[str, Call] = {}
        self._waiting: dict[str, list[dict]] = {}  # messages for each session
        self._asks: dict[str, Ask] = {}  # what sessions ask you, by its id
        # What each session asked on calls you didn't answer, by id, until you answer one of
        # its calls or it's withdrawn or answered elsewhere.
        self._pool: dict[str, dict[str, Ask]] = {}
        self._past: dict[str, tuple[Call, float]] = {}  # forgotten calls, and when, for a day
        self._seen: dict[str, float] = {}  # when each session's mod last collected (time.monotonic())
        # Since when each session listens (time.time()), and its project and folder, as its mod
        # says them when it collects: the phone's Contacts.
        self._since: dict[str, float] = {}
        self._named: dict[str, tuple[str, str]] = {}
        self._keeping: set[asyncio.Task] = set()  # each waits for an answer, then forgets it
        self._asking = asyncio.Lock()
        self._awake: asyncio.subprocess.Process | None = None
        self._closing: set[asyncio.Task] = set()  # Haikus of forgotten calls, while they close

    async def place(
        self, session: str, project: str, cwd: str, subject: str, briefing: str, ask: "Ask | None" = None
    ) -> Call:
        """Asks the switchboard to ring the phone; raises RuntimeError if it can't. Outside
        your work hours the switchboard holds the call (its held_until), and a new briefing
        for a session whose call is held goes to that call instead of placing another. A call
        placed to ask something has it in its briefing (`ask`), so the switchboard knows from
        the start whether it has a question."""
        if not self._switchboard:
            raise RuntimeError("the voice server was started without --switchboard")
        if held := next((c for c in self._calls.values() if c.session == session and c.held_until), None):
            held.project, held.cwd, held.subject, held.briefing = project, cwd, subject, briefing
            held.at = time.time()
            held.link.rebriefed()
            self._log(f"call {held.id[:8]}: still held until {held.held_until}, with a new briefing: {subject}")
            self._save()
            return held
        call = Call(secrets.token_urlsafe(24), session, project, cwd, subject, briefing, time.monotonic())
        call.link = self._link(call)
        if ask:
            call.link.ask(ask, briefed=True)
        held_until = await self._ask_switchboard(call)
        self._calls[call.id] = call
        if held_until:
            call.held_until = held_until
            self._log(f"call {call.id[:8]}: held until {held_until} for {project} ({session[:8]}): {subject}")
            call.holding = asyncio.create_task(self._hold(call))
            self._save()
        else:
            self._log(f"call {call.id[:8]}: placed for {project} ({session[:8]}): {subject}")
            await self._ring(call)
        await self._stay_awake()
        return call

    def _link(self, call: Call) -> Link:
        """A call's link to its session. The transcript joins it when you answer."""
        link = Link(lambda text: self.send(call.session, text, link.transcript.relative if link.transcript else None))
        return link

    async def _ask_switchboard(self, call: Call) -> str | None:
        """Asks the switchboard to ring the phone for a call: None if it rings, or when it
        can ring if it holds the call outside your work hours. Raises RuntimeError if it can't
        be reached or refuses. It hears whether the call has a question, so it can tell the
        phone of a held one; a switchboard from before ignores that. It hears the session's ID
        too, which the push takes to the phone, so the app can call the session from Contacts once
        the voice server has forgotten the call."""
        body = {
            "project": call.project, "subject": call.subject, "voice": f"http://{self._voice}", "call_id": call.id,
            "question": bool(call.link and call.link.has_question), "session_id": call.session,
        }  # fmt: skip
        try:
            async with ClientSession(timeout=ClientTimeout(total=10)) as http:
                async with http.post(f"http://{self._switchboard}/call", json=body) as response:
                    if response.status != 200:
                        raise RuntimeError(f"the switchboard answered {response.status}")
                    try:
                        answer = await response.json(content_type=None)
                    except ValueError:
                        answer = None  # a switchboard from before work hours answers only 200
        except (ClientError, TimeoutError) as e:
            raise RuntimeError(f"can't reach the switchboard at {self._switchboard}: {e or type(e).__name__}") from e
        held_until = answer.get("held_until") if isinstance(answer, dict) else None
        return held_until if isinstance(held_until, str) and held_until else None

    async def _ring(self, call: Call) -> None:
        """The phone rings: Haiku thinks of what to say meanwhile, so the voice can start as
        soon as you answer, and the call is forgotten if you don't within UNANSWERED s."""
        call.placed = time.monotonic()
        say = lambda line: self._log(f"call {call.id[:8]}: {line}")
        haiku = Haiku(instructions(call.briefing, session=True), say, tools=True)
        try:
            await haiku.start()
            call.haiku = haiku
        except OSError as e:
            self._log(f"call {call.id[:8]}: Haiku didn't start ({e}); the call starts its own")
            await haiku.close()
        call.deadline = asyncio.create_task(self._deadline(call))
        self._save()

    async def _hold(self, call: Call) -> None:
        """Asks the switchboard again about a held call when it said the call can ring, or
        after REASK s if that's sooner, until it rings; RETRY s after a try that fails; and at
        once when it has news for the switchboard (`reask`). The wait is on the wall clock,
        which goes on while the Mac sleeps."""
        due = wake(call.held_until)
        while True:
            while (left := due - time.time()) > 0 and not call.reask.is_set():
                try:
                    await asyncio.wait_for(call.reask.wait(), min(left, TICK))
                except TimeoutError:
                    pass
            call.reask.clear()
            try:
                held_until = await self._ask_switchboard(call)
            except RuntimeError as e:
                self._log(f"call {call.id[:8]}: still held, {e}; asking again in {RETRY:.0f} s")
                due = time.time() + RETRY
                continue
            if not held_until:
                break
            if held_until != call.held_until:
                self._log(f"call {call.id[:8]}: now held until {held_until}")
            call.held_until = held_until
            self._save()
            due = wake(held_until)
        call.held_until = None
        self._log(f"call {call.id[:8]}: placed for {call.project} ({call.session[:8]}), held until now: {call.subject}")
        await self._ring(call)

    def offer(self, call_id: str) -> Call | None:
        """The call an offer is for, if it's one this server placed. The offer can come while
        the phone still rings, and a call you haven't answered stays placed until it's
        forgotten, so the phone can offer again. A held one is refused (__main__.py), unless
        the offer calls it now."""
        return self._calls.get(call_id)

    async def back(self, call_id: str) -> Call | None:
        """The call you make by calling call_id back from the phone, which it answers at once:
        a call this server placed or you made, answered or not, remembered for a day after it
        was forgotten, or a call you didn't answer that's still placed. None if there's no such
        call. A call back already on is given as it is, so a new offer replaces the earlier
        one, as for any call."""
        self._expire()
        if call := self._calls.get(call_id):
            if call.held_until:
                return None  # held outside your work hours, it hasn't rung, so the phone can't know its ID
            if call.back or call.answered_at is not None:
                return call
            # Still placed: the call back takes its place, so it's never told it wasn't
            # answered, and what it asked waits for the call back.
            self._calls.pop(call.id)
            if call.deadline:
                call.deadline.cancel()
            self._close_haiku(call)
            missed = remembered(call)
            pool = self._pool.setdefault(call.session, {})
            pool.update(call.link.asks)
            call.link.asks.clear()
        elif past := self._past.pop(call_id, None):
            missed = past[0]
        else:
            return None
        listening = self.listening(missed.session)
        # The session's latest briefing, if it wrote one after this call's.
        later = self._latest(missed.session)
        since = (later.briefing, time.time() - later.at) if later and later.at > missed.at else None
        # What the earlier call's briefing asks and no longer waits for an answer; a session
        # that isn't listening waits for none.
        waiting = {ask.name for ask in self._pool.get(missed.session, {}).values()} if listening else set()
        settled = [name for name in missed.asked if name not in waiting]
        # You talked on it, or on a call back of it: the lead says so, rather than that you
        # didn't answer.
        talked = time.time() - missed.talked if missed.talked is not None else None
        briefing = called_back(missed.briefing, time.time() - missed.at, since, settled, talked)
        call = dataclasses.replace(missed, briefing=briefing, placed=time.monotonic(), back=missed, contact=False)
        call.link = self._link(call)
        call.link.listening = listening
        self._calls[call.id] = call
        how = ("the session listens" if listening else "the session isn't listening") + (
            f", answered {talked / 60:.0f} min ago" if talked is not None else ""
        ) + (", with a newer briefing since" if since else "")
        self._log(f"call {call.id[:8]}: called back about {call.subject!r}, {how}")
        await self._pick_up(call)
        return call

    async def now(self, call_id: str) -> Call | None:
        """The call you make by calling a waiting call from the phone, which it answers at
        once: a call held outside your work hours is released without ringing, and isn't asked
        about at the switchboard any more. Its voice has the briefing with a lead that says you
        called. A call that's no longer held but still placed (it rang meanwhile) is given as it
        is, for an ordinary offer. None if there's no such call."""
        held = self._calls.get(call_id)
        if not held or not held.held_until:
            return held
        if held.holding:
            held.holding.cancel()
        # The held call's link goes with it: what it asks, and what its briefing asks.
        call = dataclasses.replace(
            held, briefing=called_now(held.briefing), placed=time.monotonic(), held_until=None, holding=None,
            back=remembered(held), now=True,
        )  # fmt: skip
        self._calls[call.id] = call
        self._log(f"call {call.id[:8]}: called now from the app, held until {held.held_until}: {call.subject!r}")
        await self._pick_up(call)
        return call

    async def contact(self, session: str, call_id: str) -> Call | None:
        """The call you make by calling a session from the phone's Contacts, which it answers
        at once, under the ID the phone made for it. Its voice has the session's latest
        briefing, if it has one, with a lead that says you called; it talks with the session as
        an answered call does. None if the session isn't listening: the phone lists only
        sessions that do, but its list can be a little old. A contact call already on is given
        as it is, so a new offer replaces the earlier one."""
        self._expire()
        if call := self._calls.get(call_id):
            return call if call.contact and call.session == session else None
        if call_id in self._past or not self.listening(session):
            return None
        latest = self._latest(session)
        project, cwd = self._named.get(session, ("", ""))
        if latest:
            project, cwd = project or latest.project, cwd or latest.cwd
        # What the call will be remembered as, for a call back: the latest briefing, as of now.
        plain = Call(
            call_id, session, project or "Claude", cwd, latest.subject if latest else "",
            latest.briefing if latest else "", time.monotonic(), at=latest.at if latest else time.time(),
            asked=latest.asked if latest else (), contact=True,
        )  # fmt: skip
        heard = time.time() - latest.talked if latest and latest.talked is not None else None
        briefing = called_contact(latest.briefing if latest else None, time.time() - plain.at, heard)
        call = dataclasses.replace(plain, briefing=briefing, back=plain)
        call.link = self._link(call)
        self._calls[call.id] = call
        about = f"its latest briefing {latest.subject!r}" if latest else "no briefing yet"
        self._log(f"call {call.id[:8]}: called from Contacts, {call.project} ({session[:8]}), with {about}")
        await self._pick_up(call)
        return call

    def _latest(self, session: str) -> Call | None:
        """The call with a session's latest briefing: placed, held or remembered. A call you made
        counts as the call it calls; a call from Contacts has no briefing of its own."""
        mine = [c.back or c for c in self._calls.values() if c.session == session]
        mine += [c for c, _ in self._past.values() if c.session == session]
        return max((c for c in mine if not c.contact), key=lambda c: c.at, default=None)

    def contacts(self) -> list[dict]:
        """The sessions that listen now, as the phone's Contacts list them: each one's ID,
        project, since when it listens here, and the subject of its latest briefing and when
        the session wrote it, if it has one. A session whose mod didn't say its project (a mod
        from before Contacts) goes by its latest call's, or isn't listed."""
        listed = []
        for session in self._seen:
            if not self.listening(session):
                continue
            latest = self._latest(session)
            project = self._named.get(session, ("", ""))[0] or (latest.project if latest else "")
            if not project:
                continue
            entry = {"session_id": session, "project": project, "since": stamp(self._since.get(session, time.time()))}
            if latest:
                entry |= {"subject": latest.subject, "briefed_at": stamp(latest.at)}
            listed.append(entry)
        return sorted(listed, key=lambda e: (e["project"].lower(), e["since"]))

    async def publish(self) -> None:
        """Tells the switchboard which of this Mac's sessions listen, for the phone's Contacts:
        when that changes, and every PUBLISH s, so the switchboard can drop a Mac's sessions
        once it stops hearing from it. Runs as long as the server does. A switchboard from
        before Contacts answers 404, which is logged once."""
        sent: list[dict] | None = None
        tried = again = float("-inf")
        failing = False
        while True:
            listed = self.contacts()
            now = time.monotonic()
            if (listed != sent and now >= again) or now - tried >= PUBLISH:
                tried = now
                try:
                    async with ClientSession(timeout=ClientTimeout(total=10)) as http:
                        body = {"voice": f"http://{self._voice}", "sessions": listed}
                        async with http.post(f"http://{self._switchboard}/sessions", json=body) as response:
                            if response.status != 200:
                                raise RuntimeError(f"the switchboard answered {response.status}")
                    if listed != sent:
                        names = ", ".join(f"{e['project']} ({e['session_id'][:8]})" for e in listed) or "none"
                        self._log(f"sessions listening, told the switchboard: {names}")
                    sent, failing = listed, False
                except (ClientError, TimeoutError, RuntimeError) as e:
                    again = now + CHANGED
                    if not failing:
                        self._log(f"can't tell the switchboard which sessions listen: {e or type(e).__name__}")
                    failing = True
            await asyncio.sleep(1)

    async def to_claude_app(self, call_id: str) -> str | None:
        """Sends a held call's question to the session's own dialog, which Remote Control
        shows in the Claude app, as you asked from the app's waiting calls. What the call asks
        is settled as `terminal`: the mod lets the dialog show, and a permission's prompt, which
        shows anyway, stays. The call is forgotten: it never rings, isn't asked about at the
        switchboard any more, and can't be called back. Its session is told. Returns why it
        can't, if it can't: the call doesn't wait here ("not waiting", as when it rang
        meanwhile), its session isn't listening ("not listening"), or it has no question ("no
        question")."""
        call = self._calls.get(call_id)
        if not call or not call.held_until:
            return "not waiting"
        if not self.listening(call.session):
            return "not listening"
        if not call.link.has_question:
            return "no question"
        self._calls.pop(call.id)
        if call.holding:
            call.holding.cancel()
        asked = list(call.link.asks.values())
        call.link.asks.clear()
        for ask in asked:
            ask.settle("terminal")
        names = ", ".join(ask.name for ask in asked)
        self._log(f"call {call.id[:8]}: {names} sent to the Claude app from the phone, and the call forgotten")
        about = f'The call about "{call.subject}"' if call.subject else "The held call"
        self.send(
            call.session,
            f"{about} won't ring: the developer chose on the phone to answer its question in the Claude app "
            "instead, with Remote Control.",
            kind="outcome",
        )
        await self._stay_awake()
        return None

    def listening(self, session: str) -> bool:
        """Whether a session's mod collected what waits for it within LISTENING s: ringback is
        on in it, and it hasn't ended."""
        return time.monotonic() - self._seen.get(session, float("-inf")) < LISTENING

    async def _pick_up(self, call: Call) -> None:
        """A call you made, answered at once: Haiku writes the opening of a call you made, and
        it's forgotten if its audio doesn't come within UNANSWERED s."""
        say = lambda line: self._log(f"call {call.id[:8]}: {line}")
        listening = call.link.listening
        briefing = instructions(call.briefing, session=True, listening=listening)
        haiku = Haiku(briefing, say, tools=listening, opening=PICKED_UP)
        try:
            await haiku.start()
            call.haiku = haiku
        except OSError as e:
            self._log(f"call {call.id[:8]}: Haiku didn't start ({e}); the call starts its own")
            await haiku.close()
        call.deadline = asyncio.create_task(self._deadline(call))
        self._save()
        await self._stay_awake()

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
            self._save()
        return call

    async def end(self, call_id: str) -> None:
        if call := self._calls.get(call_id):
            await self._forget(call)

    def send(self, session: str, text: str, transcript: str | None = None, kind: str = "message") -> None:
        """Keeps a message for a session until its mod collects it: what the voice passed on
        (`message`), or how a call went (`outcome`), which the session reads as a note."""
        self._waiting.setdefault(session, []).append({"kind": kind, "text": text, "transcript": transcript})
        self._log(f"{kind} for session {session[:8]}: {text!r}")
        self._save()

    def collect(self, session: str, project: str | None = None, cwd: str | None = None) -> list[dict]:
        """What waits for a session, which its mod collects while ringback is on in it: so it
        listens. The mod says its project and folder, for the phone's Contacts."""
        if not self.listening(session):
            self._since[session] = time.time()
        self._seen[session] = time.monotonic()
        if project:
            self._named[session] = (project, cwd or "")
        if messages := self._waiting.pop(session, []):
            self._save()
        return messages

    async def news(self, session: str, text: str) -> bool:
        """Tells the call you're on with a session its news; False if there's no such call."""
        for call in self._calls.values():
            # Not while the phone rings: the voice can tell you only once you've answered.
            if call.session == session and call.answered_at is not None and call.link and call.link.notify:
                await call.link.news(text)
                return True
        return False

    async def ask(self, session: str, kind: str, request: dict) -> tuple[Ask, str, str | None]:
        """Puts what a session asks to the call you're on with it ("live"), or to its call
        that's ringing, for once you answer ("placed"), or held outside your work hours
        ("held"); otherwise places a call to ask it ("placed" or "held"). Returns it, how, and
        when a held call can ring. Raises RuntimeError if no call can be placed. A question
        that joins a held call has it ask the switchboard again at once, which tells the phone."""
        # One at a time: parallel tool calls can ask at once, and should share one call.
        async with self._asking:
            ask = Ask(new_id(self._asks), kind, session, request)
            # Not a call back whose voice can't answer the session.
            ours = [c for c in self._calls.values() if c.session == session and c.link.listening]
            if call := ([c for c in ours if c.answered_at is not None] or ours or [None])[-1]:
                call.link.ask(ask)
                if call.held_until and kind == "question":
                    call.reask.set()
            else:
                r = request
                call = await self.place(session, r["project"], r["cwd"], r["subject"], ask.briefing(), ask)
            how = "live" if call.answered_at is not None else "held" if call.held_until else "placed"
            self._asks[ask.id] = ask
        self._log(f"call {call.id[:8]}: {ask.name} for session {session[:8]}, {how}: {ask.details()[:100]!r}")
        self._keep_until_read(ask)
        self._save()
        return ask, how, call.held_until

    def _keep_until_read(self, ask: Ask) -> None:
        keeping = asyncio.create_task(self._keep(ask))
        self._keeping.add(keeping)
        keeping.add_done_callback(self._keeping.discard)

    def held_until(self, ask: Ask) -> str | None:
        """When the call that asks it can ring, while that call is held outside your work
        hours."""
        return next((c.held_until for c in self._calls.values() if c.held_until and ask.id in c.link.asks), None)

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
        self._save()
        return True

    def withdraw(self, ask: Ask) -> None:
        """The session's mod no longer waits for an answer, so a call's voice no longer asks."""
        self._asks.pop(ask.id, None)
        self._pool.get(ask.session, {}).pop(ask.id, None)
        for call in self._calls.values():
            if call.held_until and ask.kind == "question" and ask.id in call.link.asks:
                call.reask.set()  # so the app soon stops showing the question
            call.link.drop(ask, "it withdrew the question" if ask.kind == "question" else "it withdrew the request")
        ask.settle("withdrawn")  # wakes those waiting for it, who find it gone
        self._save()

    async def _keep(self, ask: Ask) -> None:
        """Logs the answer, and forgets it a while after. An answer the voice gives on a call
        is saved here, once it's given."""
        if not ask.settled.is_set():
            await ask.settled.wait()
            self._log(f"{ask.answered()} (session {ask.session[:8]})")
            self._save()
        await asyncio.sleep(KEEP)
        if self._asks.get(ask.id) is ask:
            del self._asks[ask.id]
            self._save()

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
        for task in (call.deadline, call.holding):
            if task and task is not asyncio.current_task():
                task.cancel()
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
        # What's remembered is the call as it was placed, also after a call back, with when you
        # last talked on it.
        self._past[call.id] = (talked(call), time.time())
        self._expire()
        self._save()
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

    def _save(self) -> None:
        """Writes what it keeps to the state file, if it changed: atomically, so a restart
        finds either the old state or the new one. Only you can read it: it holds the calls'
        IDs, which are their secrets."""
        if not self._state:
            return
        text = json.dumps(self._snapshot(), ensure_ascii=False)
        if text == self._written:
            return
        new = self._state.with_name(self._state.name + ".new")
        try:
            fd = os.open(new, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "w") as f:
                f.write(text)
                f.flush()
                os.fsync(f.fileno())
            os.replace(new, self._state)
            self._written = text
        except OSError as e:
            self._log(f"couldn't write {self._state}: {e}")

    def _snapshot(self) -> dict:
        """What it keeps, as JSON: each Ask once, which calls, the pool and the asks a mod can
        read refer to by its place in "asks"."""
        asks: list[Ask] = []
        places: dict[int, int] = {}

        def ref(ask: Ask) -> int:
            if id(ask) not in places:
                places[id(ask)] = len(asks)
                asks.append(ask)
            return places[id(ask)]

        calls = [saved(call, ref) for call in self._calls.values()]
        return {
            "calls": calls,
            "past": [saved(call, ref) | {"forgotten": at} for call, at in self._past.values()],
            "pool": {session: [ref(a) for a in pool.values()] for session, pool in self._pool.items() if pool},
            "kept": [ref(a) for a in self._asks.values()],
            "waiting": {session: messages for session, messages in self._waiting.items() if messages},
            "asks": [
                {"id": a.id, "kind": a.kind, "session": a.session, "request": a.request, "outcome": a.outcome,
                 "said": a.said, "answers": a.answers}
                for a in asks
            ],  # fmt: skip
        }

    async def restore(self) -> None:
        """Reads what was kept before a restart. Held calls are held again, and ask the
        switchboard when they're due. A call that was ringing or on has ended: its session is
        told so, what it asked waits for the session's next call you answer, as a missed
        call's does, and it can be called back. A state file it can't read is set aside."""
        if not self._state:
            return
        try:
            data = json.loads(self._state.read_text())
            asks = [
                Ask(a["id"], a["kind"], a["session"], a["request"], a["outcome"], a["said"], a["answers"])
                for a in data["asks"]
            ]
            for ask in asks:
                if ask.outcome:
                    ask.settled.set()
            calls = [self._loaded(c, asks) for c in data["calls"]]
            past = {c["id"]: (self._loaded(c, asks), c["forgotten"]) for c in data["past"]}
            pool = {session: {asks[i].id: asks[i] for i in refs} for session, refs in data["pool"].items()}
            kept = {asks[i].id: asks[i] for i in data["kept"]}
            waiting = {session: list(messages) for session, messages in data["waiting"].items()}
        except FileNotFoundError:
            return
        except (OSError, ValueError, KeyError, IndexError, TypeError, AttributeError) as e:
            aside = self._state.with_name(self._state.name + ".unreadable")
            self._log(f"can't read {self._state} ({type(e).__name__}: {e}); set aside as {aside.name}, starting afresh")
            self._state.replace(aside)
            return
        self._past, self._pool, self._asks, self._waiting = past, pool, kept, waiting
        for call in calls:
            if call.held_until:
                self._calls[call.id] = call
                self._log(f"call {call.id[:8]}: still held until {call.held_until} after a restart: {call.subject}")
                call.holding = asyncio.create_task(self._hold(call))
                continue
            # It was ringing or on: it ended when the voice server stopped.
            if call.link.listening:
                self._pool.setdefault(call.session, {}).update(call.link.asks)
            self._log(f"call {call.id[:8]}: ended by the restart: {call.subject}")
            self._waiting.setdefault(call.session, []).append(
                {"kind": "outcome", "text": restarted(call), "transcript": call.link.transcript_path}
            )
            self._past[call.id] = (talked(call), time.time())
        for ask in self._asks.values():
            self._keep_until_read(ask)
        self._expire()
        self._log(
            f"restored {sum(1 for c in self._calls.values() if c.held_until)} held calls, {len(self._past)} to call "
            f"back, {len(self._asks)} asks, {sum(len(m) for m in self._waiting.values())} messages for sessions"
        )
        self._save()
        await self._stay_awake()

    def _loaded(self, data: dict, asks: list[Ask]) -> Call:
        """A call as saved() wrote it, with its link to its session if it had one."""
        call = Call(
            data["id"], data["session"], data["project"], data["cwd"], data["subject"], data["briefing"],
            time.monotonic(), at=data["at"], held_until=data["held_until"], asked=tuple(data["asked"]),
            back=self._loaded(data["back"], asks) if data["back"] else None, now=data.get("now", False),
            answered_at=time.monotonic() if data["answered"] else None, contact=data.get("contact", False),
            talked=data.get("talked"),
        )  # fmt: skip
        if link := data["link"]:
            call.link = self._link(call)
            call.link.listening = link["listening"]
            call.link.asks = {asks[i].id: asks[i] for i in link["asks"]}
            call.link._briefed = [asks[i] for i in link["briefed"]]
            call.link._notes = [(None if i is None else asks[i], text) for i, text in link["notes"]]
            call.link.transcript_path = link["transcript"]
        return call


def saved(call: Call, ref: Callable[[Ask], int]) -> dict:
    """A call as the state file keeps it: what it was placed with, its link's asks and notes
    by their place in the file's asks, and its transcript, if you answered it."""
    link = call.link
    return {
        "id": call.id, "session": call.session, "project": call.project, "cwd": call.cwd, "subject": call.subject,
        "briefing": call.briefing, "at": call.at, "held_until": call.held_until, "asked": list(call.asked),
        "answered": call.answered_at is not None, "now": call.now, "contact": call.contact, "talked": call.talked,
        "back": saved(call.back, ref) if call.back else None,
        "link": link and {
            "listening": link.listening,
            "asks": [ref(a) for a in link.asks.values()],
            "briefed": [ref(a) for a in link._briefed],
            "notes": [[None if a is None else ref(a), text] for a, text in link._notes],
            "transcript": link.transcript.relative if link.transcript else link.transcript_path,
        },
    }  # fmt: skip


def restarted(call: Call) -> str:
    """The outcome of a call that was ringing or on when the voice server restarted."""
    ended = "ended when the voice server restarted"
    path = call.link.transcript_path
    transcript = f" Its transcript: {path}" if path else ""
    if call.contact:
        return f"The developer called the session from the app's Contacts, and the call {ended}.{transcript}"
    if call.back:
        what = f'"{call.subject}"' if call.subject else "an earlier call"
        how = f"called about {what} from the app while it waited" if call.now else f"called back about {what}"
        return f"The developer {how}, and the call {ended}.{transcript}"
    about = f'The call about "{call.subject}"' if call.subject else "The call"
    if call.answered_at is not None:
        return f"{about} was answered, and {ended}.{transcript}"
    return f"{about} {ended}, before it was answered."


def talked(call: Call) -> Call:
    """What's remembered of a call that has ended: the call as it was placed, also after a call
    back, and when you last talked on it, if you answered."""
    kept = call.back or remembered(call)
    return dataclasses.replace(kept, talked=time.time()) if call.answered_at is not None else kept


def remembered(call: Call) -> Call:
    """What's remembered of a call once it's forgotten, so you can call it back: the call as
    it was placed, with what its briefing asks, and nothing running."""
    return dataclasses.replace(
        call, answered_at=None, link=None, haiku=None, deadline=None, held_until=None, holding=None,
        asked=call.link.briefed if call.link else call.asked,
    )  # fmt: skip


def outcome(call: Call, transcript: str | None) -> str:
    """How a call went, for its session to read. If it wasn't answered, the voice server
    can't tell why; the switchboard's log can."""
    about = f'The call about "{call.subject}"' if call.subject else "The call"
    if call.contact:
        if call.answered_at is None:
            return "The developer called the session from the app's Contacts, but the call didn't connect."
        lasted = duration(time.monotonic() - call.answered_at)
        text = f"The developer called the session from the app's Contacts and it lasted {lasted}."
        return f"{text} Its transcript: {transcript}" if transcript else text
    if call.back:
        what = f'"{call.subject}"' if call.subject else "an earlier call"
        # From the app's call history, or a held call from the app's waiting calls.
        how = f"called about {what} from the app while it waited" if call.now else f"called back about {what}"
        if call.answered_at is None:
            return f"The developer {how}, but the call didn't connect."
        text = f"The developer {how} and it lasted {duration(time.monotonic() - call.answered_at)}."
        return f"{text} Its transcript: {transcript}" if transcript else text
    if call.answered_at is None:
        took = duration(time.monotonic() - call.placed)
        return f"{about} wasn't answered within {took}: declined, missed, or its audio didn't connect."
    text = f"{about} was answered and lasted {duration(time.monotonic() - call.answered_at)}."
    return f"{text} Its transcript: {transcript}" if transcript else text


def wake(held_until: str) -> float:
    """When a held call asks the switchboard again (time.time()): when it said the call can
    ring, or in REASK s if that's sooner. Never within a second, so a Mac whose clock is a
    little ahead of the switchboard's doesn't ask again and again."""
    try:
        at = datetime.fromisoformat(held_until).timestamp()
    except ValueError:
        at = float("inf")
    now = time.time()
    return max(min(at, now + REASK), now + 1)


def stamp(at: float) -> str:
    """A time.time() as ISO 8601, with this Mac's offset from UTC."""
    return datetime.fromtimestamp(at).astimezone().isoformat(timespec="seconds")


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
    - `POST /calls` places a call with a session's briefing. Outside your work hours the
      answer has `held_until`, when the call can ring (ISO 8601, with an offset);
    - `GET /sessions/{id}/messages` collects what waits for a session: each message has a
      `kind`, `message` for what the voice passed on and `outcome` for how a call went, its
      `text`, and the call's `transcript` if there is one. `?project=…&cwd=…` say the
      session's project and folder, for the phone's Contacts;
    - `POST /sessions/{id}/messages` leaves one, as the voice does (for tools);
    - `POST /sessions/{id}/news` brings a session's answer: `{"live": true}` if the voice
      told the call you're on, false if the session should call you;
    - `POST /sessions/{id}/permissions` asks your permission for a tool call, and
      `POST /sessions/{id}/questions` asks you questions, each with their options. Either is
      put to the call you're on with the session (`"call": "live"`) or to one placed to ask
      it (`"placed"`), or held outside your work hours (`"held"`, with `held_until`), and
      answered with its id; 502 if no call can be placed;
    - `GET .../{ask}?wait=S` waits up to S seconds (at most 25) for the answer: a permission's
      `verdict` (allow, deny, none, terminal or pending), or the questions' `status`
      (answered, none, terminal or pending) and `answers`, with your `said`, and
      `"held": true` while its call is held. None: you hung up without answering it.
      Terminal: you sent the held call's question to the Claude app, so the session asks in
      its own dialog. A call you didn't answer leaves it pending, for the next call with the
      session you answer;
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
            call = await calls.place(
                body["session_id"], body["project"], str(body.get("cwd", "")), str(body.get("subject", "")), body["briefing"]
            )
        except RuntimeError as e:
            return web.json_response({"error": str(e)}, status=502)
        return web.json_response({"ok": True} | ({"held_until": call.held_until} if call.held_until else {}))

    async def collect(request: web.Request) -> web.Response:
        q = request.query
        return web.json_response({"messages": calls.collect(request.match_info["id"], q.get("project"), q.get("cwd"))})

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
                asked, how, held_until = await calls.ask(request.match_info["id"], kind, checked)
            except RuntimeError as e:
                return web.json_response({"error": str(e)}, status=502)
            return web.json_response({kind: asked.id, "call": how} | ({"held_until": held_until} if held_until else {}))

        def asked(request: web.Request) -> Ask:
            if found := calls.asked(request.match_info["id"], kind, request.match_info["ask"]):
                return found
            raise web.HTTPNotFound(text='{"error": "unknown"}', content_type="application/json")

        def answer(a: Ask) -> dict:
            held = {"held": True} if calls.held_until(a) else {}
            if kind == "permission":
                return {"verdict": a.outcome or "pending", "said": a.said} | held
            return {"status": a.outcome or "pending", "answers": a.answers, "said": a.said} | held

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
