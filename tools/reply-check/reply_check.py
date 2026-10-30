"""Feeds the voice's reply parser (ClaudeCodeLLMService in voice/ringback_voice/claude_code.py)
scripted events from Haiku's stream, and checks what's spoken around its tool calls: the
"Okay." said when Haiku reaches for a tool before saying anything, and the quiet after a
tool once it has said something. No Haiku, no audio: it takes a second or two."""

import asyncio
import sys

from ringback_voice.claude_code import ClaudeCodeLLMService


class Stub:
    """Stands in for Haiku's session: only its stream of events."""

    def __init__(self):
        self.events = asyncio.Queue()


def tool() -> dict:
    return {"type": "stream_event", "event": {"type": "content_block_start", "content_block": {"type": "tool_use"}}}


def text(t: str) -> list[dict]:
    start = {"type": "stream_event", "event": {"type": "content_block_start", "content_block": {"type": "text"}}}
    delta = {"type": "stream_event", "event": {"type": "content_block_delta", "delta": {"type": "text_delta", "text": t}}}
    return [start, delta]


async def spoken(events: list[dict]) -> tuple[str, bool, int]:
    """What the voice speaks of a reply made of events, whether the rest went quiet, and the
    words after the "Okay." that an answer it gave waits for you to hear."""
    service = ClaudeCodeLLMService("", None, lambda line: None, haiku=Stub())
    service._said = "Okay, blue."  # your words, which the reply answers
    for event in events + [{"type": "result"}]:
        service._haiku.events.put_nowait(event)
    service._unfinished = 1
    said = "".join([await service._next_text() or "" for _ in range(len(events) + 1)])
    return said, service._quiet, len(service._reply[len(service._ack) :].split())


CASES = [
    # (what Haiku sends, what's spoken, quiet after)
    ("a tool, then words: Okay first", [tool(), *text("I'll answer blue.")], "Okay. I'll answer blue.", False),
    (
        "a tool refused and tried again, then words: Okay once, the words spoken",
        [tool(), tool(), *text("I'll answer blue.")],
        "Okay. I'll answer blue.",
        False,
    ),
    ("words, a tool, words: quiet after the tool", [*text("Passing that on. "), tool(), *text("Passed.")], "Passing that on. ", True),
    (
        "a tool, words, another tool: quiet after the second",
        [tool(), *text("Telling it now. "), tool(), *text("Told it.")],
        "Okay. Telling it now. ",
        True,
    ),
    ("after the Okay, Haiku's own Okay is dropped", [tool(), *text("Okay, I'll answer blue.")], "Okay. I'll answer blue.", False),
]


async def main() -> int:
    failed = 0
    for name, events, expected, quiet in CASES:
        said, was_quiet, _ = await spoken(events)
        ok = said == expected and was_quiet == quiet
        failed += not ok
        print(f"{'ok  ' if ok else 'FAIL'}  {name}: {said!r}" + ("" if ok else f", expected {expected!r} (quiet {quiet})"))
    print(f"{len(CASES) - failed}/{len(CASES)} passed")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
