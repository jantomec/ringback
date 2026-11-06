#!/usr/bin/env python3
"""Score replay.py's answers against labels made by hand, by reading the calls.

    python3 tools/turn-replay/score.py RESULTS.jsonl LABELS.json

LABELS.json maps "stream:t" to {"unfinished": bool, "short_answer": bool, "text": ...}: whether
you had more to say on that thought, from your words and what you said next, and whether the
turn was a short answer to a question. Keep it with your other private call data.
"""
import collections
import json
import sys

labels = json.load(open(sys.argv[2]))
runs = collections.defaultdict(list)
for line in open(sys.argv[1]):
    r = json.loads(line)
    label = labels[f"{r['stream']}:{r['t']}"]
    runs[(r["model"], r["hint"])].append({**r, **label})
for (model, hint), rows in sorted(runs.items(), key=str):
    unfinished = [r for r in rows if r["unfinished"]]
    finished = [r for r in rows if not r["unfinished"]]
    short = [r for r in rows if r["short_answer"]]
    waited = lambda rs: sum(r["decision"] == "WAIT" for r in rs)
    spoke = lambda rs: [r for r in rs if r["decision"] != "WAIT"]
    print(f"{model}, hint {hint}: waited on {waited(unfinished)}/{len(unfinished)} unfinished; "
          f"stalled on {waited(finished)}/{len(finished)} finished; "
          f"short answers stalled {waited(short)}/{len(short)}")
    for r in finished:
        if r["decision"] == "WAIT":
            print(f"    stalled: {r['text'][:66]!r}")
    for r in spoke(unfinished):
        print(f"    {r['decision'].lower():8} on unfinished: p={r['p']:.2f} {r['text'][:60]!r}")
