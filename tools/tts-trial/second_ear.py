#!/usr/bin/env python3
"""A second ear on the context clips: Qwen3-ASR, not Parakeet, hears each clip, so a word that
Parakeet says is lost is checked by a model built differently. It also finds where each of the
eight sentences starts in each clip, for the listening page, and proves each cut by hearing the
piece: a piece must start with its sentence's first two words.

    gpu.sh ENV_PYTHON second_ear.py STT_TRIAL_DIR CLIPS_DIR

Writes CLIPS_DIR/second-ear.json. Needs ffmpeg, and backends.py from tools/stt-trial on the node.
"""

import difflib
import json
import re
import subprocess
import sys
from pathlib import Path

import numpy as np

sys.path.insert(0, sys.argv[1])
from backends import MODELS  # noqa: E402

FOLDER = Path(sys.argv[2])
RATE = 16000
HOP = 0.01  # seconds a level is measured over
QUIET_DB = -38  # under the clip's speech level, this is a pause
SHORTEST_GAP = 0.08


def words(text):
    return re.sub(r"[^a-z' ]", " ", text.lower().replace("’", "'")).split()


def read(path):
    args = ["ffmpeg", "-v", "error", "-i", str(path), "-f", "f32le", "-ac", "1", "-ar", str(RATE), "-"]
    return np.frombuffer(subprocess.run(args, capture_output=True, check=True).stdout, dtype=np.float32).copy()


def gaps(x):
    """Every pause, as (start, end) in seconds, the outer silence included."""
    hop = int(HOP * RATE)
    level = np.sqrt(np.mean(x[: len(x) // hop * hop].reshape(-1, hop) ** 2, axis=1)) + 1e-9
    db = 20 * np.log10(level)
    quiet = db < np.percentile(db, 90) + QUIET_DB
    found, start = [], None
    for i, q in enumerate(quiet):
        if q and start is None:
            start = i
        if not q and start is not None:
            if (i - start) * HOP >= SHORTEST_GAP:
                found.append((start * HOP, i * HOP))
            start = None
    if start is not None:
        found.append((start * HOP, len(quiet) * HOP))
    return found


def sentence_cuts(x, sentences):
    """Each sentence's (start, end): the pauses nearest where the sentences' lengths put the
    boundaries, the longer pause winning. Monotonic, so a comma's pause can't steal a boundary."""
    found = gaps(x)
    head = found[0][1] if found and found[0][0] == 0 else 0.0
    tail = found[-1][0] if found and found[-1][1] >= len(x) / RATE - HOP * 2 else len(x) / RATE
    inner = [g for g in found if g[0] > head and g[1] < tail]
    sizes = np.cumsum([len(s) for s in sentences])[:-1] / sum(len(s) for s in sentences)
    want = [head + (tail - head) * s for s in sizes]
    best = {}

    def choose(n, after):
        if n == len(want):
            return 0.0, []
        if (n, after) in best:
            return best[n, after]
        answer = (float("inf"), [])
        for i in range(after + 1, len(inner) - (len(want) - n - 1)):
            s, e = inner[i]
            cost = abs((s + e) / 2 - want[n]) - 2.5 * (e - s)
            rest, picked = choose(n + 1, i)
            if cost + rest < answer[0]:
                answer = (cost + rest, [i] + picked)
        best[n, after] = answer
        return answer

    picked = choose(0, -1)[1]
    edges = [inner[i] for i in picked]
    starts = [head] + [e for _, e in edges]
    ends = [s for s, _ in edges] + [tail]
    return list(zip(starts, ends))


def main():
    clips = json.load(open(FOLDER / "clips.json"))
    hear = MODELS["qwen3-1.7b"]()
    report = []
    for c in clips:
        x = read(FOLDER / c["file"])
        said = words(" ".join(c["sentences"]))
        heard = words(hear(x))
        lost = []
        for tag, i1, i2, _, j2 in difflib.SequenceMatcher(None, said, heard, autojunk=False).get_opcodes():
            if tag != "equal":
                lost.append({"tag": tag, "said": " ".join(said[i1:i2]), "heard": " ".join(heard[j2 - (j2 - _):j2]), "after": " ".join(said[max(0, i1 - 2):i1])})
        cuts = sentence_cuts(x, c["sentences"])
        bad = []
        for n, (start, end) in enumerate(cuts):
            piece = x[int(max(0, start - 0.04) * RATE) : int((end + 0.04) * RATE)]
            got = words(hear(piece))
            want = words(c["sentences"][n])
            if got[:2] != want[:2]:
                bad.append({"sentence": n + 1, "heard": " ".join(got[:6]), "want": " ".join(want[:6])})
        print(f"{c['file']:28} parakeet {c['words_heard_wrong']:>2}  qwen3 ops {len(lost):>2}  cuts wrong {len(bad)}", flush=True)
        for e in lost + bad:
            print("    ", e, flush=True)
        report.append({"file": c["file"], "seconds": round(len(x) / RATE, 2), "qwen3": lost, "cuts": [[round(a, 2), round(b, 2)] for a, b in cuts], "bad_cuts": bad})
    json.dump(report, open(FOLDER / "second-ear.json", "w"), indent=1)


main()
