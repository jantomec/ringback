#!/usr/bin/env python3
"""Time Haiku's replies through a Claude Code session that stays open, as the voice runs it.

    python3 tools/reply-speed/claude_code.py [ROUNDS]

For each setting in SETTINGS, starts one session per round, waits as long as a phone
rings (RING_SECS), then plays the conversation in TURNS and times each turn from sending
your words to the first text of the reply. Runs claude from an empty directory, so no
project CLAUDE.md is loaded.
"""

import json
import statistics
import subprocess
import sys
import tempfile
import time

RING_SECS = 3
BASE = [
    "claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
    "--include-partial-messages", "--model", "haiku", "--tools", "", "--setting-sources", "",
    "--strict-mcp-config", "--no-session-persistence", "--disable-slash-commands",
]
SETTINGS = {
    "default": [],
    "effort low": ["--effort", "low"],
    "thinking off": ["--settings", '{"alwaysThinkingEnabled": false}'],
}
SYSTEM = """\
You are the voice of a Claude Code session, on a phone call with the developer. Speak in
short, plain sentences, at most three per turn. Don't use lists, tables or code.

The session's report: the switchboard, which rings the phone, was rewritten from Python
to one Swift binary and moved to the always-on Mac. It now uses about 4 MB of memory
instead of 50. A test call rang the locked phone and audio worked both ways. Two issues
remain on that Mac: it doesn't restart the switchboard after a reboot, and automatic
macOS updates can leave it locked until someone logs in."""
TURNS = [
    "Hi, what's new?",
    "Nice. How much memory did it use before?",
    "And what's the problem with the updates?",
    "Okay. Should I turn the automatic updates off?",
    "Yeah, fine. What would you do first?",
    "Thanks, that's all.",
]


def run(flags):
    with tempfile.TemporaryDirectory() as cwd:
        p = subprocess.Popen(BASE + flags + ["--system-prompt", SYSTEM], cwd=cwd, text=True,
                             stdin=subprocess.PIPE, stdout=subprocess.PIPE, bufsize=1)
        time.sleep(RING_SECS)
        firsts, thought = [], 0
        for words in TURNS:
            t = time.monotonic()
            p.stdin.write(json.dumps({"type": "user", "message": {"role": "user", "content": words}}) + "\n")
            p.stdin.flush()
            first = None
            for line in p.stdout:
                m = json.loads(line)
                if m["type"] == "stream_event":
                    delta = m["event"].get("delta", {}).get("type")
                    thought += delta == "thinking_delta"
                    if delta == "text_delta" and first is None:
                        first = time.monotonic() - t
                elif m["type"] == "result":
                    break
            firsts.append(first)
        p.stdin.close()
        p.wait()
        return firsts, thought


def main():
    rounds = int(sys.argv[1]) if len(sys.argv) > 1 else 3
    for name, flags in SETTINGS.items():
        first_turns, later_turns, thought = [], [], 0
        for _ in range(rounds):
            firsts, t = run(flags)
            first_turns.append(firsts[0])
            later_turns += firsts[1:]
            thought += t
        print(
            f"{name:12} first turn median {statistics.median(first_turns):.2f} s "
            f"({min(first_turns):.2f}–{max(first_turns):.2f}) | later turns median "
            f"{statistics.median(later_turns):.2f} s ({min(later_turns):.2f}–{max(later_turns):.2f}, "
            f"{len(later_turns)} turns) | thinking chunks {thought}",
            flush=True,
        )


if __name__ == "__main__":
    main()
