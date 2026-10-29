#!/usr/bin/env python3
"""Checks how the voice hands a call over from Haiku to Opus, with its own Claude Code
session (voice/ringback_voice/haiku.py), without a call.

    uv run --project voice tools/handover-check/handover_check.py [--runs N] [--bare-resume]

Each run starts Haiku as a call for a session starts it, with the voice's instructions and a
briefing that holds a code name (and a build number of its own, so the prompt cache holds
none of it, as with a real call), and plays its opening and two turns: one it should pass to
the session with its tool (whose result holds a receipt word), and one that only says
something. It lists the files the session keeps on disk, then hands over to Opus as the
voice does (Haiku.hand_over: Haiku's process closed, the session resumed with
`--resume ID --model opus`) and asks Opus what only the briefing, the conversation and the
tool's result tell it. Last, it closes the session as the voice does at the end of a call,
and lists what's left of it under ~/.claude. --bare-resume resumes without giving the system
prompt again, to see whether the session's own record of it is enough.

Prints each turn's model, first words, tokens (input, cache read, cache written, output) and
the cost `claude -p` reports for it, then how many runs passed.
"""

import argparse
import asyncio
import os
import re
import secrets
import time
from pathlib import Path

from ringback_voice import haiku
from ringback_voice.claude_code import HAND_OVER
from ringback_voice.instructions import instructions

BRIEFING = """\
Subject: Release notes for 2.3 written

What's new: I wrote the release notes for version 2.3, whose code name is Pelican. They
cover the new offline mode, the faster sync, and two bug fixes in the settings screen. I
also bumped the version number in the app and the changelog.

What needs you: the release can go out once you say when. Should I tag it now, or wait for
the translations?"""
TURNS = [
    "Please tell the session the release goes out on Friday.",
    "Thanks. By the way, my favourite fruit is quince.",
]
RECEIPT = "Kestrel"  # a word: the voice says numbers its own way
ASK = (
    f"[{HAND_OVER}]\nWhat's the release's code name, what did I ask you to pass to the session, what "
    "receipt word did the call give you when you passed it on, and what's my favourite fruit?"
)
EXPECT = {"code name": "pelican", "Friday": "friday", "receipt": RECEIPT.lower(), "quince": "quince"}


def tool(name: str, arguments: dict) -> str:
    print(f"    tool call: {name} {arguments}")
    return f"Passed. The call's receipt word is {RECEIPT}." if name == "pass_to_session" else "Nothing done."


async def reply(h: haiku.Haiku, sent: float) -> dict:
    """Reads a turn's reply until its result."""
    text, first = "", None
    while (event := await h.events.get()) is not None:
        stream = event.get("event", {}) if event.get("type") == "stream_event" else {}
        if stream.get("delta", {}).get("type") == "text_delta":
            first = first or time.monotonic() - sent
            text += stream["delta"]["text"]
        if event.get("type") == "result":
            return {"text": text, "first": first, "result": event}
    return {"text": text, "first": first, "result": {}}


def show(name: str, model: str, r: dict, cost: float) -> None:
    u = r["result"].get("usage", {})
    first = f"{r['first']:.2f} s" if r["first"] else "none"
    print(
        f"  {name} ({model}): first words after {first}; input {u.get('input_tokens')}, "
        f"cache read {u.get('cache_read_input_tokens')}, cache written {u.get('cache_creation_input_tokens')}, "
        f"output {u.get('output_tokens')}; ${cost:.4f}"
    )
    print(f"    {r['text'].strip()!r}")


def kept(id: str, cwd: str) -> list[str]:
    """What Claude Code keeps of the session: the folder for cwd, and anything named for it."""
    home = Path(os.environ.get("CLAUDE_CONFIG_DIR") or "~/.claude").expanduser()
    folder = home / "projects" / re.sub(r"[^a-zA-Z0-9]", "-", os.path.realpath(cwd))
    found = {p for p in folder.rglob("*")} | {folder} if folder.exists() else set()
    found |= set(home.glob(f"*/*{id}*")) | set(home.glob(f"*/*/*{id}*"))
    return sorted(str(p.relative_to(home)) + ("/" if p.is_dir() else "") for p in found)


async def run() -> bool:
    # A briefing of its own, as each call's is, so the prompt cache holds nothing of it yet.
    briefing = f"{BRIEFING}\n\nBuild {secrets.token_hex(4)}."
    h = haiku.Haiku(instructions(briefing, session=True), lambda line: print(f"    voice: {line}"), tools=True)
    t = time.monotonic()
    await h.start()
    cwd = h._dir.name
    print(f"  session {h.id}")
    turns = [("opening", h.model, await reply(h, t))]
    h.take_tools(tool)
    for text in TURNS:
        turns.append((text[:30], h.model, await reply(h, await h.send(text))))
    running = kept(h.id, cwd)
    t = time.monotonic()
    await h.hand_over()
    switched = time.monotonic() - t
    answer = await reply(h, await h.send(ASK))
    turns.append((f"Opus's first turn ({switched:.2f} s to hand over)", h.model, answer))
    turns.append(("Thanks, that's all.", h.model, await reply(h, await h.send("Thanks, that's all."))))
    process = None
    for name, model, r in turns:
        if model != process:
            process, cost = model, 0.0
        total = r["result"].get("total_cost_usd", 0)  # for the process so far
        show(name, model, r, total - cost)
        cost = total
    print(f"  kept on disk while Haiku ran: {running or 'nothing yet'}")
    print(f"  kept on disk at the end of the call: {kept(h.id, cwd) or 'nothing'}")
    await h.close()
    left = kept(h.id, cwd)
    said = answer["text"].lower()
    knew = {what: word in said for what, word in EXPECT.items()}
    resumed = answer["result"].get("session_id") == h.id
    print(f"  Opus knew {knew}; same session: {resumed}")
    print(f"  left after closing: {left or 'nothing'}")
    return all(knew.values()) and resumed and not left


def bare_resume() -> None:
    """Resumes without --system-prompt, so only the session's record of it is there."""
    start = asyncio.create_subprocess_exec

    async def without(*command, **kwargs):
        if "--resume" in command:
            i = command.index("--system-prompt")
            command = command[:i] + command[i + 2 :]
            print("    resuming without --system-prompt")
        return await start(*command, **kwargs)

    asyncio.create_subprocess_exec = without


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--runs", type=int, default=1)
    p.add_argument("--bare-resume", action="store_true", help="resume without giving the system prompt again")
    args = p.parse_args()
    if args.bare_resume:
        bare_resume()
    passed = 0
    for n in range(args.runs):
        print(f"\nrun {n + 1}")
        passed += asyncio.run(run())
    print(f"\n{passed} of {args.runs} runs: Opus knew everything, in the same session, and nothing was left")


if __name__ == "__main__":
    main()
