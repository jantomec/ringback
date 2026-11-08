#!/usr/bin/env python3
"""Does a longer pass lose words? Counts what's missing from each `drops` pass of context_make.py.

    env/bin/python -I harness/context_drops.py MODEL...      (MODEL, or MODEL/LABEL for a --try)

A word counts as lost only if it's absent from Parakeet's transcript of the whole pass and from its
transcript of overlapping 8 s windows, heard on their own, so a slip of Parakeet's isn't a loss.
"""
import difflib
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import context_join as cj  # noqa: E402
import score  # noqa: E402

ROOT = score.ROOT


def missing(want, heard):
    a, b = score.norm(want).split(), score.norm(heard).split()
    lost = []
    for tag, i1, i2, _, _ in difflib.SequenceMatcher(a=a, b=b, autojunk=False).get_opcodes():
        if tag in ("delete", "replace"):
            lost += a[i1:i2]
    return lost


def main():
    rec = score.recogniser()
    for model in sys.argv[1:]:
        d = ROOT / "runs/context" / model
        d = d / "drops" if (d / "drops").exists() else d
        rows = json.loads((d / "passes.json").read_text())
        for r in rows:
            x, rate = cj.load(d / f"{r['name']}.wav")
            y = cj.to16(x, rate)
            whole = score.transcribe(rec, y)
            lost = missing(r["text"], whole)
            if lost:
                n, windows = 8 * cj.RATE, []
                for s in range(0, max(len(y) - n // 2, 1), n // 2):
                    windows.append(score.transcribe(rec, y[s: s + n]))
                heard = set(score.norm(" ".join(windows)).split())
                lost = [w for w in lost if w not in heard]
            r.update(lost=lost, heard=whole, words=len(score.norm(r["text"]).split()), chars_s=len(r["text"]) / r["audio_s"])
        (d / "lost.json").write_text(json.dumps(rows, indent=0))
        print(model)
        for size in sorted({r["size"] for r in rows}):
            some = [r for r in rows if r["size"] == size]
            print(f"  {size} sentences: {sum(bool(r['lost']) for r in some)} of {len(some)} passes lost words, "
                  f"{sum(len(r['lost']) for r in some)} of {sum(r['words'] for r in some)} words")
            for r in some:
                if r["lost"]:
                    print("     ", r["name"], " ".join(r["lost"])[:70])


if __name__ == "__main__":
    main()
