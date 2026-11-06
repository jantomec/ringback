#!/usr/bin/env python3
"""Replay a call's turns through a model: could it tell when you've finished, from your words?

    python3 tools/turn-replay/replay.py --speech JOURNAL --agent AGENT_LOG --calls DIR [DIR...] \
        --out RESULTS.jsonl [--models haiku,sonnet] [--dry-run N]

Takes each turn the speech node closed, with what you had heard by then, and asks the model
what the voice should do: REPLY, WAIT or CONTINUE. Each is asked with each hint of Smart Turn's score (--hints), and the answers are set against what you did: whether you spoke again before the voice
did. See README.md. Your transcripts and logs are private: they stay out of this repo.
"""

import argparse
import glob
import json
import os
import re
import subprocess
import sys
import tempfile
import time
from concurrent.futures import ThreadPoolExecutor

SYSTEM = """You are the voice of a Claude Code session, on a phone call with a developer. They \
speak English as a second language, through speech recognition, so some words may be wrong, \
and they often pause in the middle of a thought to find their words. They have just paused \
for about a third of a second. From their words and the conversation, decide what the voice \
does now:

REPLY: they've said what they wanted to say and are waiting for the voice to respond. A short, \
clear answer to a question they've heard in full, such as "yes", "no" or a choice, is finished, \
even if it's one word.
WAIT: they're probably in the middle of a thought and will go on, so stay silent.
CONTINUE: only if they cut the voice off, and what they said only shows they're listening, \
such as "mm-hmm", "yeah" or "okay": the voice carries on with what it was saying.

They've heard only what's shown as heard. What the voice hadn't said yet can't be what \
they're answering.

Answer with one word: REPLY, WAIT or CONTINUE."""

# What the model is told of their intonation, by name: Smart Turn's score, or only that they
# sound unfinished when the score is very low, which is when it means something.
HINTS = {
    "none": lambda p: "",
    "score": lambda p: f"A model listening to their intonation, which can be wrong, gives {p:.2f} for "
                       "how finished they sound, from 0 (unfinished) to 1 (finished).",
    "low": lambda p: "They sound unfinished." if p < LOW else "",
}
LOW = 0.3


def clock(h, m, s):
    return int(h) * 3600 + int(m) * 60 + float(s)


def read_speech(path):
    """The speech node's log: each stream's voice pieces and your turns, in order."""
    events = []
    for line in open(path):
        m = re.search(r" (\d\d):(\d\d):(\d\d\.\d+)  stream (\d+): (.*)", line)
        if not m:
            continue
        t, stream, rest = clock(*m.groups()[:3]), int(m[4]), m[5]
        you = re.match(r'you \(([\d.]+) s, heard in [\d.]+ s; Smart Turn ([\d.]+) in [\d.]+ s\): "(.*)"', rest)
        voice = re.match(r'voice piece \d+ \([^)]*\): "(.*)"', rest)
        if you:
            events.append({"t": t, "stream": stream, "kind": "you", "spoke": float(you[1]),
                           "p": float(you[2]), "text": you[3]})
        elif voice:
            events.append({"t": t, "stream": stream, "kind": "voice", "text": voice[1]})
    return events


def read_cuts(path, since):
    """The agent's log: when you cut the voice off, and the end of what you'd heard."""
    cuts = []
    for line in open(path):
        m = re.match(r"(\d\d):(\d\d):(\d\d\.\d+)  call \S+: interrupted; you'd heard (.*)", line)
        if m and clock(*m.groups()[:3]) >= since:
            heard = m[4].strip()
            cuts.append({"t": clock(*m.groups()[:3]), "kind": "cut", "heard": heard[1:-1]})
    return cuts


def read_replies(dirs):
    """Every reply as written, from the call transcripts: a cut one is only partly in the log."""
    replies = []
    for folder in dirs:
        for path in sorted(glob.glob(os.path.join(folder, "*.md"))):
            call = open(path).read().split("## The call")[-1]
            for block in re.split(r"\n\n(?=\*\*)", call):
                if block.startswith("**Voice:** "):
                    replies.append(block[len("**Voice:** "):].strip())
    return replies


def timelines(speech, cuts):
    """Each stream's events in order, with each cut placed in its stream by time."""
    streams = {}
    for e in speech:
        streams.setdefault(e["stream"], []).append(e)
    for events in streams.values():
        first, last = events[0]["t"] - 60, events[-1]["t"] + 5
        events += [dict(c) for c in cuts if first <= c["t"] <= last]
        events.sort(key=lambda e: e["t"])
    return streams


def full_reply(pieces, replies):
    start = pieces[0][:40]
    return next((r for r in replies if start in r), " ".join(pieces))


def split_heard(reply, tail):
    """What you heard of a reply cut off after tail, and what you didn't."""
    if not tail:
        return "", reply
    at = reply.rfind(tail)
    if at < 0:
        return tail, ""
    end = at + len(tail)
    return reply[:end], reply[end:].strip()


def cases(streams, replies, context=10):
    """One case for each turn, with the conversation as you'd heard it by then."""
    out = []
    for stream, events in sorted(streams.items()):
        said, pieces, cut, yous = [], [], None, [e for e in events if e["kind"] == "you"]
        for e in events:
            if e["kind"] == "voice":
                pieces.append(e["text"])
            elif e["kind"] == "cut":
                cut = e
            elif e["kind"] == "you":
                heard, unheard, reply = "", "", ""
                if pieces:
                    reply = full_reply(pieces, replies)
                    heard, unheard = (reply, "") if cut is None else split_heard(reply, cut["heard"])
                elif cut is not None:
                    heard = ""
                n = yous.index(e) + 1
                next_you = yous[n] if n < len(yous) else None
                went_on = False
                if next_you is not None:
                    began = next_you["t"] - next_you["spoke"]
                    later = [x["t"] for x in events if x["kind"] == "voice" and x["t"] > e["t"]]
                    went_on = began < (later[0] if later else 1e12)
                lines = list(said)
                if heard:
                    lines.append("Voice: " + heard + (" (cut off here)" if cut is not None else ""))
                now = ""
                if cut is not None and (pieces or heard):
                    now = f'\nThese words cut the voice off. It still had to say: "{unheard}"' if unheard else ""
                out.append({"stream": stream, "t": round(e["t"], 3), "text": e["text"], "p": e["p"], "spoke": e["spoke"],
                            "went_on": went_on, "cut": cut is not None, "heard_any": bool(heard),
                            "prompt": "The conversation so far, as the developer heard it:\n"
                                      + ("\n".join(lines[-context:]) or "(nothing yet)")
                                      + f'\n\nThe developer\'s words just now: "{e["text"]}"' + now})
                if heard:
                    said.append("Voice: " + heard + (" (cut off here)" if cut is not None else ""))
                said.append("Developer: " + e["text"])
                pieces, cut = [], None
    return out


def ask(case, model, hint):
    words = HINTS[hint](case["p"])
    system = SYSTEM + ("\n\n" + words if words else "")
    env = {k: v for k, v in os.environ.items() if k != "CLAUDE_CODE_PLUGIN_DIRS"}
    with tempfile.TemporaryDirectory() as cwd:
        began = time.monotonic()
        r = subprocess.run(["claude", "-p", "--model", model, "--output-format", "json", "--tools", "",
                            "--strict-mcp-config", "--disable-slash-commands", "--setting-sources", "",
                            "--no-session-persistence", "--system-prompt", system],
                           input=case["prompt"], cwd=cwd, env=env, text=True, capture_output=True, timeout=120)
    took = time.monotonic() - began
    try:
        text = json.loads(r.stdout).get("result", "")
    except json.JSONDecodeError:
        text = "ERROR " + (r.stderr or r.stdout)[:200]
    word = re.search(r"\b(REPLY|WAIT|CONTINUE)\b", text)
    return {**{k: v for k, v in case.items() if k != "prompt"}, "model": model, "hint": hint,
            "decision": word[1] if word else "ERROR", "raw": text[:80], "secs": round(took, 2)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--speech", required=True)
    parser.add_argument("--agent", required=True)
    parser.add_argument("--calls", nargs="+", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--models", default="haiku,sonnet")
    parser.add_argument("--hints", default="none,score", help="of: " + ", ".join(HINTS))
    parser.add_argument("--since", default="07:00:00", help="only the agent log's cuts from this time")
    parser.add_argument("--workers", type=int, default=8)
    parser.add_argument("--dry-run", type=int, default=0, help="print this many prompts and stop")
    args = parser.parse_args()

    speech = read_speech(args.speech)
    cuts = read_cuts(args.agent, clock(*args.since.split(":")))
    all_cases = cases(timelines(speech, cuts), read_replies(args.calls))
    print(f"{len(all_cases)} turns, {sum(c['went_on'] for c in all_cases)} you went on from, "
          f"{sum(c['cut'] for c in all_cases)} that cut the voice off")
    if args.dry_run:
        for c in [c for c in all_cases if c["cut"] and c["heard_any"]][:args.dry_run]:
            print("-" * 60)
            print(c["prompt"][-900:])
            print("went on:", c["went_on"], " score:", c["p"])
        return
    jobs = [(c, m, h) for m in args.models.split(",") for h in args.hints.split(",") for c in all_cases]
    began = time.monotonic()
    done = 0
    with open(args.out, "w") as out, ThreadPoolExecutor(args.workers) as pool:
        for result in pool.map(lambda job: ask(*job), jobs):
            out.write(json.dumps(result) + "\n")
            out.flush()
            done += 1
            if done % 50 == 0:
                print(f"{done}/{len(jobs)} in {time.monotonic() - began:.0f} s", flush=True)
    print(f"{len(jobs)} asked in {time.monotonic() - began:.0f} s")


if __name__ == "__main__":
    sys.exit(main())
