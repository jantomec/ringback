"""Haiku in a Claude Code session that stays open for the whole call, on the Claude
subscription.

Pipecat has no Claude Code service. The session (haiku.py) keeps the conversation itself, so
each turn sends only what's new: your words, and a note when you cut the voice off. Replies
come back as they're written (stream-json), and the first words arrive about 1.2 s after
your turn (see tools/reply-speed). A call that a session placed brings its session already
started, with the opening written while the phone rang.

On a call for a session, Haiku has three tools (call_tools.py): pass_to_session, and
answer_permission and answer_question for what the session asks you. A message it passes, or
an answer it gives, is held until you've heard the voice say so, and dropped if you cut the
voice off before that (see "Acting on the call" in docs/design.md).
"""

import asyncio
import time
from collections.abc import Callable
from dataclasses import fields

from pipecat.frames.frames import LLMContextFrame, LLMFullResponseEndFrame, LLMFullResponseStartFrame
from pipecat.processors.frame_processor import FrameDirection
from pipecat.services.llm_service import LLMService
from pipecat.services.settings import LLMSettings
from pipecat.utils.string import match_endofsentence

from ringback_voice.haiku import Haiku
from ringback_voice.sessions import Link
from ringback_voice.speech import Timeline

HELD = (
    "Held until the developer has heard you say you're passing it on, then passed to the session; "
    "if they cut you off before that, it's dropped and you'll be told. The session's answer comes "
    'later, in a note "[The session: ...]". Until then you don\'t know what it will say or do. '
    "You've told the developer already: end your reply now, unless there's something else to say."
)
ANSWER_HELD = (
    "Held until the developer has heard you say what you'll answer, then given to the session; if they "
    "cut you off before that, it's dropped and you'll be told. You've told the developer already: end "
    "your reply now, unless there's something else to say."
)
# Pipecat expects every setting to be given. The session sets its own, so only the model is.
SETTINGS = LLMSettings(
    **{f.name: None for f in fields(LLMSettings) if f.name != "extra"}
    | {"model": "haiku", "filter_incomplete_user_turns": False, "use_provider_thinking_default": False}
)


class ClaudeCodeLLMService(LLMService):
    def __init__(
        self,
        instructions: str,
        timeline: Timeline,
        log: Callable[[str], None],
        link: Link | None = None,
        haiku: Haiku | None = None,
        **kwargs,
    ):
        super().__init__(settings=SETTINGS, **kwargs)
        self._instructions = instructions
        self._haiku = haiku  # its session, if the call started it already
        self._own_haiku = False  # started here, so it's closed here
        self._timeline = timeline
        self._log = log
        self._link = link  # to the calling session, if there is one
        self._said = ""  # your words in the last turn sent
        self._turn = 0  # turns sent to Haiku
        self._held: list[dict] = []  # messages and answers for the session, until you've heard them announced
        self._unsaid = ""  # reply text after a tool call, which isn't spoken
        self._quiet = False  # this reply passed a message after saying so: the rest isn't spoken
        self._heard_words: int | None = None  # of the last reply, if you cut it off
        self._seen = 0  # messages of the Pipecat context already sent
        self._unfinished = 0  # turns sent whose reply hasn't fully arrived
        self._reply = ""  # the last reply, as Haiku wrote it
        self._written = False  # the last reply is in the call's transcript
        self._opening: float | None = None  # when the opening was asked for, until it's spoken
        self._cut = False  # you interrupted the last reply while it was arriving

    async def start(self, frame):
        await super().start(frame)
        if not self._haiku:
            # The call starts when the phone's offer arrives, before its audio connects.
            self._haiku = Haiku(self._instructions, self._log, tools=self._link is not None)
            self._own_haiku = True
            await self._haiku.start()
        if self._link:
            self.create_task(self._pass_heard())
        # Haiku was asked for the opening when it started, and writes it meanwhile. It waits
        # in the queue until it can be spoken.
        self._turn = self._unfinished = 1
        self._opening = self._haiku.opening

    async def stop(self, frame):
        await super().stop(frame)
        await self._close()

    async def cancel(self, frame):
        await super().cancel(frame)
        await self._close()

    async def _close(self):
        if self._link:
            # You hung up: what you'd heard announced still goes.
            heard = len(self._timeline.heard(time.monotonic()).split())
            self._pass(lambda h: self._needed(h) <= heard, dropped="you hung up first")
            self._write_reply()
            self._reply = ""
        if self._haiku and self._own_haiku:
            await self._haiku.close()
        self._haiku = None

    async def process_frame(self, frame, direction: FrameDirection):
        await super().process_frame(frame, direction)
        if isinstance(frame, LLMContextFrame):
            await self._answer(frame.context)
        else:
            await self.push_frame(frame, direction)

    async def _answer(self, context):
        self._haiku.used = True
        words = self._new_words(context)
        if self._opening is not None and not words:
            t, self._opening = self._opening, None
        else:
            self._opening = None
            # A reply you interrupted can still be arriving. It's waited for, because the
            # session takes one turn at a time, and its text completes the last reply.
            while self._unfinished and await self._next_text() is not None:
                pass
            self._write_reply()
            note = self._cut_off_note()
            # What the last reply announced goes to the session if you heard it announced.
            heard = self._heard_words
            note += self._pass(
                lambda h: heard is None or self._needed(h) <= heard, dropped="you cut the voice off first"
            )
            if note:
                self._log(f"told Haiku: {note.strip()}")
            if self._link and self._link.transcript:
                transcript = self._link.transcript
                for line in note.splitlines():
                    transcript.add("Note to the voice", line.strip("[]"))
                transcript.add("Developer", self._said)
            t = await self._send(note + (words or "[Carry on.]"))
        await self.push_frame(LLMFullResponseStartFrame())
        await self.start_processing_metrics()
        await self.start_ttfb_metrics()
        try:
            first = True
            while self._unfinished and (text := await self._next_text()) is not None:
                if text and first:
                    first = False
                    await self.stop_ttfb_metrics()
                    # When they came, which for an opening written while the phone rang
                    # was before it's spoken.
                    came = self._haiku.first_words or time.monotonic()
                    self._log(f"Haiku: first words after {came - t:.2f} s")
                if text:
                    await self._push_llm_text(text)
            # Written as soon as it's complete, so it comes before what it passed on.
            self._write_reply()
            if not self._reply.split():
                # Nothing was said, so there's nothing to hear first.
                self._pass(lambda h: h["turn"] == self._turn)
        except asyncio.CancelledError:
            self._cut = True
            raise
        finally:
            await self.stop_processing_metrics()
            await self.push_frame(LLMFullResponseEndFrame())

    async def _send(self, message: str) -> float:
        """Sends a turn to the session and returns when it was sent."""
        self._reply = ""
        self._written = False
        self._cut = False
        self._quiet = False
        self._turn += 1
        if self._link:
            # From your first words on, Haiku's tool calls are yours to pass on (see haiku.py).
            self._haiku.take_tools(self._tool)
        t = await self._haiku.send(message)
        self._unfinished += 1
        return t

    async def _next_text(self) -> str | None:
        """The next piece of reply text ("" for other events), or None if the session ended."""
        event = await self._haiku.events.get()
        if event is None:
            self._unfinished = 0
            await self.push_error("The Claude Code session ended")
            return None
        if event.get("type") == "result":
            self._unfinished -= 1
            if self._unsaid:
                self._log(f"not spoken, after passing a message: {self._unsaid.strip()!r}")
                self._unsaid = ""
            if event.get("is_error"):
                self._log(f"Haiku: error {event.get('result')!r}")
            return ""
        stream = event.get("event", {}) if event.get("type") == "stream_event" else {}
        if stream.get("type") == "content_block_start":
            block = stream.get("content_block", {}).get("type")
            if block == "tool_use" and self._reply.split():
                # Haiku has said it's passing something on. Told so or not, it tends to
                # say it again after the tool, so the rest of this reply isn't spoken.
                self._quiet = True
            elif block == "text" and not self._quiet and self._reply and not self._reply[-1].isspace():
                # Text after a tool call starts a new block; keep a space between them.
                self._reply += " "
                return " "
        delta = stream.get("delta", {})
        if delta.get("type") == "text_delta":
            if self._quiet:
                self._unsaid += delta["text"]
                return ""
            self._reply += delta["text"]
            return delta["text"]
        return ""

    def _write_reply(self) -> None:
        if self._link and self._link.transcript and not self._written:
            self._link.transcript.add("Voice", self._reply)
            self._written = True

    def _tool(self, name: str, arguments: dict) -> str:
        if name == "answer_permission":
            return self._answer_permission(arguments)
        if name == "answer_question":
            return self._answer_question(arguments)
        message = arguments.get("message")
        if name != "pass_to_session" or not isinstance(message, str) or not message.strip():
            return "Nothing passed: pass_to_session needs a message."
        message = message.strip()
        self._hold("message", message, lambda: self._link.send(message))
        self._log(f"Haiku → session, held until heard: {message!r}")
        return HELD

    def _answer_permission(self, arguments: dict) -> str:
        ask = self._link.asks.get(arguments.get("permission"))
        if not ask or ask.kind != "permission":
            return f"Nothing answered: no permission {arguments.get('permission')!r} waits. {self._waiting()}"
        if not isinstance(allow := arguments.get("allow"), bool):
            return "Nothing answered: answer_permission needs allow, true or false."
        verdict, said = "allow" if allow else "deny", self._words(arguments)
        text = f"{ask.name}: {verdict} ({said!r})"
        self._hold("answer", text, lambda: self._link.answer(ask.id, verdict, said))
        self._log(f"Haiku answers the session, held until heard: {text}")
        return ANSWER_HELD

    def _answer_question(self, arguments: dict) -> str:
        ask = self._link.asks.get(arguments.get("question"))
        if not ask or ask.kind != "question":
            return f"Nothing answered: no question {arguments.get('question')!r} waits. {self._waiting()}"
        answers, missing = ask.match(arguments.get("answers"))
        if missing:
            return f"Nothing answered: answer_question needs an answer to each question. Missing: {missing}"
        said = self._words(arguments)
        text = f"{ask.name}: {answers} ({said!r})"
        self._hold("answer", text, lambda: self._link.answer(ask.id, "answered", said, answers))
        self._log(f"Haiku answers the session, held until heard: {text}")
        return ANSWER_HELD

    def _words(self, arguments: dict) -> str:
        """Your words that Haiku gave with its answer, or else your last turn as recognised."""
        said = arguments.get("said")
        return said.strip() if isinstance(said, str) and said.strip() else self._said.strip()

    def _waiting(self) -> str:
        waiting = ", ".join(ask.name for ask in self._link.asks.values())
        return f"Waiting for an answer: {waiting}." if waiting else "Nothing waits for an answer."

    def _hold(self, kind: str, text: str, give: Callable[[], bool | None]) -> None:
        """Holds a message or an answer for the session until you've heard the reply so far."""
        # The words of the reply so far are what you must hear before it goes; None if Haiku
        # said nothing first.
        words = len(self._reply.split()) or None
        self._held.append({"turn": self._turn, "words": words, "kind": kind, "text": text, "give": give})

    def _needed(self, held: dict) -> int:
        """The words of the reply you must hear before something held goes: those before
        Haiku gave it, or if it said nothing first, the sentence that follows. Once it
        answered your yes before saying anything, and the answer went when you'd heard
        "Okay,"."""
        if held["words"] is not None:
            return held["words"]
        end = match_endofsentence(self._reply)
        return max(1, len(self._reply[: end or len(self._reply)].split()))

    async def _pass_heard(self):
        """Passes a message or an answer on as soon as you've heard the reply up to where
        Haiku gave it."""
        while True:
            await asyncio.sleep(0.1)
            if self._held and not self._cut:
                heard = len(self._timeline.heard(time.monotonic()).split())
                self._pass(lambda h: h["turn"] == self._turn and self._needed(h) <= heard)

    def _pass(self, goes: Callable[[dict], bool], dropped: str | None = None) -> str:
        """Passes on the held messages and answers that go; drops the rest if dropped gives
        why. Returns a note for Haiku about those dropped."""
        note = ""
        for held in list(self._held):
            message = held["kind"] == "message"
            text = repr(held["text"]) if message else held["text"]
            if goes(held):
                self._held.remove(held)
                if held["give"]() is False:
                    self._log(f"not given, the session no longer waits for it: {text}")
                else:
                    self._log(f"{'passed to' if message else 'answered'} the session: {text}")
            elif dropped:
                self._held.remove(held)
                self._log(f"not {'passed' if message else 'answered'}, {dropped}: {text}")
                if self._link.transcript:
                    what = "Not passed to the session" if message else "Not answered"
                    self._link.transcript.add(f"{what}, {dropped}", held["text"])
                if message:
                    note += (
                        f'[Not passed to the session, because the developer cut you off before hearing you say '
                        f'so: "{held["text"]}". Pass it again only if they still want it.]\n'
                    )
                else:
                    note += (
                        f"[Your answer wasn't given, because the developer cut you off before hearing you say "
                        f"it: {held['text']}. The session still waits. Read what they said before together "
                        "with what they've just said: answer again only if together it's a clear answer, "
                        "otherwise ask.]\n"
                    )
        return note

    def _new_words(self, context) -> str:
        """What the Pipecat context gained since the last turn: your words, and notes."""
        messages = context.get_messages()
        new, self._seen = messages[self._seen :], len(messages)
        parts, said = [], []
        for m in new:
            if not isinstance(m, dict):
                continue
            content = m.get("content")
            text = content if isinstance(content, str) else " ".join(
                p.get("text", "") for p in content or [] if isinstance(p, dict)
            )  # fmt: skip
            if m.get("role") == "user":
                parts.append(text)
                said.append(text)
            elif m.get("role") in ("developer", "system"):
                parts.append(f"[{text}]")
        self._said = "\n".join(said)
        return "\n".join(parts)

    def _cut_off_note(self) -> str:
        heard, self._timeline.cut_off = self._timeline.cut_off, None
        if heard is None and self._cut:
            # Cut off so early that none of it reached the timeline, so none was heard.
            heard = ""
        if heard is None or heard.split() == self._reply.split():
            self._heard_words = None
            return ""
        self._heard_words = len(heard.split())
        if not heard:
            return "[You were interrupted before the developer heard any of your last reply.]\n"
        tail = " ".join(heard.split()[-12:])
        return f'[You were interrupted. The developer heard your last reply up to: "…{tail}"]\n'
