"""Haiku in a Claude Code session that stays open for the whole call, on the Claude
subscription.

Pipecat has no Claude Code service. The session keeps the conversation itself, so each turn
sends only what's new: your words, and a note when you cut the voice off. Replies come back
as they're written (stream-json), and the first words arrive about 1.2 s after your turn
(see tools/reply-speed).
"""

import asyncio
import json
import tempfile
import time
from collections.abc import Callable
from dataclasses import fields

from pipecat.frames.frames import LLMContextFrame, LLMFullResponseEndFrame, LLMFullResponseStartFrame
from pipecat.processors.frame_processor import FrameDirection
from pipecat.services.llm_service import LLMService
from pipecat.services.settings import LLMSettings

from ringback_voice.speech import Timeline

COMMAND = [
    "claude", "-p", "--model", "haiku", "--no-session-persistence",
    "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
    "--include-partial-messages",
    # The voice only talks: no tools, no MCP servers, no slash commands, and no settings
    # files, whose hooks could start another call.
    "--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--setting-sources", "",
]  # fmt: skip
OPENING = "[The developer has answered the call.]"
# Pipecat expects every setting to be given. The session sets its own, so only the model is.
SETTINGS = LLMSettings(
    **{f.name: None for f in fields(LLMSettings) if f.name != "extra"}
    | {"model": "haiku", "filter_incomplete_user_turns": False, "use_provider_thinking_default": False}
)


class ClaudeCodeLLMService(LLMService):
    def __init__(self, instructions: str, timeline: Timeline, log: Callable[[str], None], **kwargs):
        super().__init__(settings=SETTINGS, **kwargs)
        self._instructions = instructions
        self._timeline = timeline
        self._log = log
        self._dir: tempfile.TemporaryDirectory | None = None
        self._process: asyncio.subprocess.Process | None = None
        self._reader: asyncio.Task | None = None
        self._events: asyncio.Queue[dict | None] = asyncio.Queue()
        self._seen = 0  # messages of the Pipecat context already sent
        self._unfinished = 0  # turns sent whose reply hasn't fully arrived
        self._reply = ""  # the last reply, as Haiku wrote it
        self._opening: float | None = None  # when the opening was asked for, until it's spoken
        self._cut = False  # you interrupted the last reply while it was arriving

    async def start(self, frame):
        await super().start(frame)
        # An empty working directory, so no project's CLAUDE.md is loaded.
        self._dir = tempfile.TemporaryDirectory(prefix="ringback-voice-")
        self._process = await asyncio.create_subprocess_exec(
            *COMMAND, "--system-prompt", self._instructions, cwd=self._dir.name,
            stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, limit=1 << 20,
        )  # fmt: skip
        self._reader = self.create_task(self._read())
        # The call starts when you answer, a few seconds before its audio connects. Haiku
        # writes its opening meanwhile, and it waits in the queue until it can be spoken.
        self._opening = await self._send(OPENING)

    async def stop(self, frame):
        await super().stop(frame)
        await self._close()

    async def cancel(self, frame):
        await super().cancel(frame)
        await self._close()

    async def _close(self):
        if self._process and self._process.returncode is None:
            self._process.stdin.close()
            try:
                await asyncio.wait_for(self._process.wait(), 2)
            except TimeoutError:
                self._process.kill()
        if self._reader:
            await self.cancel_task(self._reader)
            self._reader = None
        if self._dir:
            self._dir.cleanup()
            self._dir = None

    async def _read(self):
        while line := await self._process.stdout.readline():
            try:
                await self._events.put(json.loads(line))
            except ValueError:
                pass
        await self._events.put(None)

    async def process_frame(self, frame, direction: FrameDirection):
        await super().process_frame(frame, direction)
        if isinstance(frame, LLMContextFrame):
            await self._answer(frame.context)
        else:
            await self.push_frame(frame, direction)

    async def _answer(self, context):
        words = self._new_words(context)
        if self._opening is not None and not words:
            t, self._opening = self._opening, None
        else:
            self._opening = None
            # A reply you interrupted can still be arriving. It's waited for, because the
            # session takes one turn at a time, and its text completes the last reply.
            while self._unfinished and await self._next_text() is not None:
                pass
            note = self._cut_off_note()
            if note:
                self._log(f"told Haiku: {note.strip()}")
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
                    self._log(f"Haiku: first words after {time.monotonic() - t:.2f} s")
                if text:
                    await self._push_llm_text(text)
        except asyncio.CancelledError:
            self._cut = True
            raise
        finally:
            await self.stop_processing_metrics()
            await self.push_frame(LLMFullResponseEndFrame())

    async def _send(self, message: str) -> float:
        """Sends a turn to the session and returns when it was sent."""
        self._reply = ""
        self._cut = False
        self._process.stdin.write(
            (json.dumps({"type": "user", "message": {"role": "user", "content": message}}) + "\n").encode()
        )
        await self._process.stdin.drain()
        self._unfinished += 1
        return time.monotonic()

    async def _next_text(self) -> str | None:
        """The next piece of reply text ("" for other events), or None if the session ended."""
        event = await self._events.get()
        if event is None:
            self._unfinished = 0
            await self.push_error("The Claude Code session ended")
            return None
        if event.get("type") == "result":
            self._unfinished -= 1
            if event.get("is_error"):
                self._log(f"Haiku: error {event.get('result')!r}")
            return ""
        delta = event.get("event", {}).get("delta", {}) if event.get("type") == "stream_event" else {}
        if delta.get("type") == "text_delta":
            self._reply += delta["text"]
            return delta["text"]
        return ""

    def _new_words(self, context) -> str:
        """What the Pipecat context gained since the last turn: your words, and notes."""
        messages = context.get_messages()
        new, self._seen = messages[self._seen :], len(messages)
        parts = []
        for m in new:
            if not isinstance(m, dict):
                continue
            content = m.get("content")
            text = content if isinstance(content, str) else " ".join(
                p.get("text", "") for p in content or [] if isinstance(p, dict)
            )  # fmt: skip
            if m.get("role") == "user":
                parts.append(text)
            elif m.get("role") in ("developer", "system"):
                parts.append(f"[{text}]")
        return "\n".join(parts)

    def _cut_off_note(self) -> str:
        heard, self._timeline.cut_off = self._timeline.cut_off, None
        if heard is None and self._cut:
            # Cut off so early that none of it reached the timeline, so none was heard.
            heard = ""
        if heard is None or heard.split() == self._reply.split():
            return ""
        if not heard:
            return "[You were interrupted before the developer heard any of your last reply.]\n"
        tail = " ".join(heard.split()[-12:])
        return f'[You were interrupted. The developer heard your last reply up to: "…{tail}"]\n'
