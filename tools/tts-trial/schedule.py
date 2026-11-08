#!/usr/bin/env python3
"""Finds X and Y from the measured numbers: which overlapping passes keep playback from running dry.

    python3 harness/schedule.py MODEL

A small simulation, 10 ms a step, of a reply being spoken. Text arrives evenly over 1 or 2 s, and
sentence 1 goes as soon as it's complete. Generations share the machine as measured by `overlap.py
workers`: on the GPU they take turns, so each of N runs at 1/N of the speed; on the CPU, with threads
to spare, they barely slow each other. A pass can hold what MAX_PASS_S says: on Ming, what came back
when a pass was asked for more (the 512-position context); on Phonon, 50 tokens, about two ordinary
sentences, past which it starts again without what came before.

Strategies:
  overlap  the user's idea. Pass 1 says sentence 1 alone. Each later pass says from sentence 1 to X (then
           Y), starting as soon as its last sentence has arrived, and plays from where the last ended.
           None is shown where no X and Y can reach the end, as a pass can't hold that much
  chain    one worker: each piece starts as soon as the last is made, with what's complete by then
It tries every X and Y, and reports, for each reply length, the cheapest that never runs dry.
"""
import itertools
import json
import os
import pathlib
import sys

ROOT = pathlib.Path(os.environ.get("TTS_ROOT", "~/ringback-models-test/tts")).expanduser()
HERE = pathlib.Path(__file__).resolve().parent
DT = 0.01
REPLIES = [2, 3, 5, 8, 12, 24]
MAX_PASS_S = {"ming": 20.0, "phonon": 7.0}
OVER = [1.0, 2.0]


def measured(model):
    d = ROOT / "runs/overlap"
    w = {n: json.loads((d / f"workers-{model}-{n}.json").read_text()) for n in (1, 2, 3)}
    single = json.loads((ROOT / "runs" / model / "all/summary.json").read_text())
    return {"pass_x": w[1]["speed_x_median"], "sentence_x": single["speed_x"],
            "first_s": w[1]["first_s_median"], "scale": {n: w[n]["speed_x_median"] / w[1]["speed_x_median"] for n in w}}


def durations(model):
    rows = json.loads((ROOT / "runs/context" / model / "ctx1/passes.json").read_text())
    # Each sentence's length with the one before it, less the one before: as it'd be in a pass.
    out = [rows[0]["audio_s"]]
    for r in rows[1:]:
        out.append(max(r["audio_s"] - out[-1], 0.8))
    return out


def arrivals(chars, seconds):
    total = sum(chars)
    done, out = 0, []
    for c in chars:
        done += c
        out.append(seconds * done / total)
    return out


def overlap(D, arrive, plan, m, max_pass):
    """plan: the last sentence of each pass after the first, in order. Returns (dry, first, work)."""
    n = len(D)
    ends = [0] + plan
    if ends[-1] != n - 1:
        return None
    passes = []
    for i, last in enumerate(ends):
        said = sum(D[: last + 1]) if i else D[0]
        if said > max_pass:
            return None
        # What it must reach before its audio is new: everything already covered by the pass before.
        passes.append({"begin": arrive[last], "length": said, "done": 0.0,
                       "new_from": sum(D[: ends[i - 1] + 1]) if i else 0.0})
    return run(passes, D, m)


def run(passes, D, m):
    t, play_end, dry, work = 0.0, None, 0.0, 0.0
    current, heard = 0, 0.0  # the pass playing, and how far through its audio playback is
    first = None
    total = sum(D)
    played = 0.0
    while played < total - 1e-6 and t < 400:
        active = [p for p in passes if p["begin"] <= t and p["done"] < p["length"]]
        rate = m["pass_x"] * m["scale"].get(len(active), 1 / len(active) if active else 1)
        if len(active) > 3:
            rate = m["pass_x"] / len(active)
        for p in active:
            if p.get("first", 0) < m["first_s"]:
                p["first"] = p.get("first", 0) + DT
            else:
                p["done"] += rate * DT
            work += DT / len(active)
        p = passes[current]
        have = p["done"]
        position = p["new_from"] + (played - sum(q["length"] - q["new_from"] for q in passes[:current]))
        if have > position + DT:
            if first is None:
                first = t
            played += DT
        elif first is not None:
            dry += DT
        if played >= sum(q["length"] - q["new_from"] for q in passes[: current + 1]) - 1e-6 and current + 1 < len(passes):
            current += 1
        t += DT
    return dry, first, work


def chain(D, arrive, m, encode=0.1, chars=None, cap=250):
    """One worker: sentence 1, then what's complete, a piece at a time, each with a prompt."""
    t, dry, played_end, first, i, work = arrive[0], 0.0, None, None, 0, 0.0
    n = len(D)
    while i < n:
        t = max(t, arrive[i])
        said = [i]
        while i and said[-1] + 1 < n and arrive[said[-1] + 1] <= t and sum(chars[j] for j in said) + chars[said[-1] + 1] < cap:
            said.append(said[-1] + 1)
        length = sum(D[j] for j in said)
        start = t + (encode if i else 0) + m["first_s"]
        speed = m["sentence_x"] if len(said) == 1 else m["pass_x"]
        if first is None:
            first = start
            played_end = start
        dry += max(start - played_end, 0)
        played_end = max(played_end, start) + length
        t = start + length / speed
        work += t - start
        i = said[-1] + 1
    return dry, first, work


def main():
    model = sys.argv[1]
    m = measured(model)
    D = durations(model)
    sentences = [s["text"] for s in json.loads((HERE / "sentences.json").read_text()) if s["kind"] == "run"]
    max_pass = MAX_PASS_S[model]
    print(json.dumps({"measured": m, "max_pass_s": max_pass}, default=lambda v: round(v, 3)))
    results = []
    for n, seconds in itertools.product(REPLIES, OVER):
        d, chars = D[:n], [len(s) for s in sentences[:n]]
        arrive = arrivals(chars, seconds)
        best = {}
        for k in (1, 2):  # later passes, so two or three workers in all
            for plan in itertools.combinations(range(1, n), k):
                if plan[-1] != n - 1:
                    continue
                got = overlap(d, arrive, [p for p in plan], m, max_pass)
                if got is None:
                    continue
                dry, first, work = got
                key = k + 1
                if key not in best or (dry, work) < (best[key][1], best[key][3]):
                    best[key] = ([p + 1 for p in plan], round(dry, 2), round(first or 0, 2), round(work, 1))
        c = chain(d, arrive, m, chars=chars)
        results.append({"sentences": n, "reply_s": round(sum(d), 1), "text_over_s": seconds,
                        "two_workers": best.get(2), "three_workers": best.get(3),
                        "chain": [round(c[0], 2), round(c[1], 2), round(c[2], 1)]})
        print(json.dumps(results[-1]))
    (ROOT / "runs/overlap" / f"schedule-{model}.json").write_text(json.dumps(results, indent=0))


if __name__ == "__main__":
    main()
