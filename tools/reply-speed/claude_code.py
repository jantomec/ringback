#!/usr/bin/env python3
"""Time the voice's replies through a Claude Code session that stays open, as the voice runs it.

    python3 tools/reply-speed/claude_code.py [ROUNDS [SETTING,SETTING]]
    python3 tools/reply-speed/claude_code.py stop [MODEL]

For each setting in SETTINGS, starts one session per round, waits as long as a phone
rings (RING_SECS), then plays the conversation in TURNS and times each turn from sending
your words to the first text of the reply. Runs claude from an empty directory, so no
project CLAUDE.md is loaded, with the voice's own instructions from agent/texts.go.

`stop` asks for a long reply, stops it once its first text has come, and times how soon the
session takes the next turn.
"""

import json
import os
import pathlib
import re
import statistics
import subprocess
import sys
import tempfile
import time

RING_SECS = 3
BASE = [
    "claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
    "--include-partial-messages", "--tools", "", "--setting-sources", "",
    "--strict-mcp-config", "--no-session-persistence", "--disable-slash-commands",
]
SETTINGS = {
    "haiku": ["--model", "haiku"],
    "sonnet": ["--model", "sonnet"],
    "opus": ["--model", "opus"],
}
# The mod, if this runs inside a session that loads it, stays out of the sessions timed.
ENV = {k: v for k, v in os.environ.items() if k != "CLAUDE_CODE_PLUGIN_DIRS"}
TEXTS = (pathlib.Path(__file__).parents[2] / "agent" / "texts.go").read_text()
RULES = "".join(re.search(name + r" = `(.*?)`", TEXTS, re.S)[1] for name in ("\\brules", "sessionRules"))
SYSTEM = RULES + """
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
        p = subprocess.Popen(BASE + flags + ["--system-prompt", SYSTEM], cwd=cwd, text=True, env=ENV,
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
                    if first is None:
                        # A turn with no words: the voice would stay silent, so say what it was.
                        print(f"  no words for {words!r}: {json.dumps(m.get('result'))[:120]} "
                              f"stop={m.get('stop_reason')} error={m.get('is_error')}", flush=True)
                    break
            firsts.append(first)
        p.stdin.close()
        p.wait()
        return firsts, thought


def stop(model):
    """Does a stop end a reply being written, and how soon can the next turn go?"""
    send = lambda p, m: (p.stdin.write(json.dumps(m) + "\n"), p.stdin.flush())
    turn = lambda words: {"type": "user", "message": {"role": "user", "content": words}}
    with tempfile.TemporaryDirectory() as cwd:
        p = subprocess.Popen(BASE + ["--model", model, "--system-prompt", "You answer at length."], cwd=cwd, text=True,
                             env=ENV, stdin=subprocess.PIPE, stdout=subprocess.PIPE, bufsize=1)
        time.sleep(RING_SECS)
        send(p, turn("Write a 600-word story about a lighthouse."))
        asked, chars, lines = None, 0, []
        for line in p.stdout:
            m = json.loads(line)
            delta = m.get("event", {}).get("delta", {}) if m["type"] == "stream_event" else {}
            if delta.get("type") == "text_delta":
                chars += len(delta["text"])
                if asked is None:
                    send(p, {"type": "control_request", "request_id": "stop-1", "request": {"subtype": "interrupt"}})
                    asked, at_stop = time.monotonic(), chars
            elif m["type"] in ("control_response", "result"):
                lines.append(f"{m['type']} {time.monotonic() - asked:.2f} s after the stop: "
                             + json.dumps(m.get("response") or {"subtype": m.get("subtype"), "error": m.get("is_error")})[:160])
                if m["type"] == "result":
                    break
        print("\n".join(lines))
        print(f"{chars} characters came, {at_stop} of them before the stop")
        t = time.monotonic()
        send(p, turn("Say one word."))
        for line in p.stdout:
            m = json.loads(line)
            if m["type"] == "stream_event" and m["event"].get("delta", {}).get("type") == "text_delta":
                print(f"the next turn's first text {time.monotonic() - t:.2f} s after it was sent")
                break
        p.stdin.close()
        p.wait()


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "stop":
        stop(sys.argv[2] if len(sys.argv) > 2 else "sonnet")
        return
    rounds = int(sys.argv[1]) if len(sys.argv) > 1 else 3
    names = sys.argv[2].split(",") if len(sys.argv) > 2 else list(SETTINGS)
    for name in names:
        flags = SETTINGS[name]
        first_turns, later_turns, thought, silent = [], [], 0, 0
        for _ in range(rounds):
            firsts, t = run(flags)
            silent += firsts.count(None)
            first_turns += [f for f in firsts[:1] if f is not None]
            later_turns += [f for f in firsts[1:] if f is not None]
            thought += t
        if silent:
            print(f"{name}: {silent} turns with no words")
        print(
            f"{name:12} first turn median {statistics.median(first_turns):.2f} s "
            f"({min(first_turns):.2f}–{max(first_turns):.2f}) | later turns median "
            f"{statistics.median(later_turns):.2f} s ({min(later_turns):.2f}–{max(later_turns):.2f}, "
            f"{len(later_turns)} turns) | thinking chunks {thought}",
            flush=True,
        )


if __name__ == "__main__":
    main()
