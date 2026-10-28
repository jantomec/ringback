"""Speaking a reply in growing pieces, and keeping track of what you've heard of it.

The first sentence of a reply is spoken on its own, so the voice starts fast. One second
before the current piece finishes playing, everything Haiku has finished since, up to the
last finished sentence, becomes the next piece (see "Speaking a reply" in
docs/design.md).
"""

import asyncio
import time
from collections.abc import Callable

from pipecat.frames.frames import (
    AggregatedTextFrame,
    InterruptionFrame,
    LLMFullResponseEndFrame,
    LLMFullResponseStartFrame,
    LLMTextFrame,
)
from pipecat.processors.frame_processor import FrameDirection, FrameProcessor
from pipecat.utils.string import match_endofsentence

LEAD = 1.0  # seconds: the next piece is handed over this long before the current one ends


class Timeline:
    """The pieces of the current reply against the clock.

    A piece starts playing when the one before it ends, or at once if nothing is playing.
    That's when its audio leaves for the phone, which plays it a few tens of ms later.
    """

    def __init__(self):
        self._pieces: list[tuple[str, float, float]] = []  # text, start, seconds
        self._replying = False
        self.reply = 0  # counts replies and interruptions, so stale work can tell
        self.added = asyncio.Event()  # set when a piece is added
        self.cut_off: str | None = None  # what you'd heard of the reply you interrupted

    def start_reply(self) -> None:
        self._pieces.clear()
        self._replying = True
        self.reply += 1

    def add(self, text: str, secs: float) -> None:
        self._pieces.append((text, max(time.monotonic(), self.end), secs))
        self.added.set()

    @property
    def count(self) -> int:
        return len(self._pieces)

    @property
    def end(self) -> float:
        if not self._pieces:
            return 0.0
        _, start, secs = self._pieces[-1]
        return start + secs

    def interrupt(self, unfinished: bool) -> str | None:
        """Notes and returns what you'd heard of an unfinished reply, whose remaining audio
        is dropped. Returns None if there was no reply to interrupt."""
        heard = None
        if self._replying and unfinished:
            heard = self.cut_off = self.heard(time.monotonic())
        self._replying = False
        self._pieces.clear()
        self.reply += 1
        return heard

    def heard(self, now: float) -> str:
        words = []
        for text, start, secs in self._pieces:
            if now >= start + secs:
                words.append(text)
            elif now > start:
                # Assumes the words are spread evenly over the piece's audio, and leaves
                # out the word that was being spoken.
                part = text[: int(len(text) * (now - start) / secs)]
                words.append(part.rsplit(" ", 1)[0] if " " in part else "")
        return " ".join(w.strip() for w in words if w.strip())


def finished_sentences(text: str) -> int:
    """The length of text up to the end of its last finished sentence, or 0."""
    end = 0
    while n := match_endofsentence(text[end:]):
        end += n
    return end


class Pieces(FrameProcessor):
    """Sits between Haiku and Kokoro, and hands Haiku's reply over in pieces."""

    def __init__(self, timeline: Timeline, log: Callable[[str], None]):
        super().__init__()
        self._timeline = timeline
        self._log = log
        self._waiter: asyncio.Task | None = None
        self._reset()

    def _reset(self):
        self._text = ""  # reply not handed over yet
        self._handed = 0  # pieces handed over in this reply
        self._ended = False  # the whole reply has arrived
        self._due = False  # the current piece is about to end

    async def cleanup(self):
        await super().cleanup()
        await self._stop_waiting()

    async def _stop_waiting(self):
        if self._waiter:
            await self.cancel_task(self._waiter)
            self._waiter = None

    async def process_frame(self, frame, direction: FrameDirection):
        await super().process_frame(frame, direction)
        if isinstance(frame, LLMFullResponseStartFrame):
            await self._stop_waiting()
            self._reset()
            self._timeline.start_reply()
            await self.push_frame(frame, direction)
        elif isinstance(frame, LLMTextFrame):
            self._text += frame.text
            await self._hand_over()
        elif isinstance(frame, LLMFullResponseEndFrame):
            self._ended = True
            await self._hand_over()
            await self.push_frame(frame, direction)
        elif isinstance(frame, InterruptionFrame):
            # Each of your turns starts with one, also when the voice had finished.
            unfinished = not self._ended or bool(self._text) or time.monotonic() < self._timeline.end
            await self._stop_waiting()
            self._reset()
            heard = self._timeline.interrupt(unfinished)
            if heard is not None:
                self._log(f"interrupted; you'd heard {heard[-60:]!r}")
            await self.push_frame(frame, direction)
        else:
            await self.push_frame(frame, direction)

    async def _hand_over(self):
        if self._ended:
            # Once the reply is complete, the rest is the next piece whenever it's handed
            # over, so it goes now.
            n = len(self._text)
        elif self._handed == 0:
            n = match_endofsentence(self._text)
        elif self._due:
            n = finished_sentences(self._text)
        else:
            return
        piece, self._text = self._text[:n].strip(), self._text[n:]
        if not piece:
            return
        self._handed += 1
        self._due = False
        await self.push_frame(AggregatedTextFrame(piece, text_type="sentence"))
        await self._stop_waiting()
        self._waiter = self.create_task(self._wait_for_lead(self._handed))

    async def _wait_for_lead(self, pieces: int):
        # A piece's length is known once Kokoro has synthesized it and added it.
        while self._timeline.count < pieces:
            self._timeline.added.clear()
            await self._timeline.added.wait()
        await asyncio.sleep(self._timeline.end - LEAD - time.monotonic())
        self._due = True
        self._waiter = None
        await self._hand_over()
