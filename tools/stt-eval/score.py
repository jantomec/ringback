#!/usr/bin/env python3
"""Score every model's transcripts against the reference text.

    uv run --with jiwer==4.0.0 --with whisper-normalizer==0.1.15 tools/stt-eval/score.py DATA_DIR

The reference for a recording is DATA_DIR/ref/<recording>.txt if it exists
(lines starting with # are skipped, a leading "* " is dropped), otherwise
tools/stt-eval/sentences.txt for scripted recordings (names without "free"). Each recording's utterances are joined and compared with the whole
reference, so where the recording was split doesn't matter. Both sides go
through Whisper's English normalizer (case, punctuation, numbers, fillers).
Word-by-word alignments go to DATA_DIR/report/.
"""

import json
import statistics
import sys
from pathlib import Path

import jiwer
from whisper_normalizer.english import EnglishTextNormalizer

SENTENCES = Path(__file__).with_name("sentences.txt")


def reference(data, recording):
    own = data / "ref" / f"{recording}.txt"
    if own.exists():
        lines = [l.removeprefix("* ") for l in own.read_text().splitlines() if not l.startswith("#")]
        return "\n".join(lines)
    return None if "free" in recording else SENTENCES.read_text()


def main():
    data = Path(sys.argv[1]).expanduser()
    normalize = EnglishTextNormalizer()
    report = data / "report"
    report.mkdir(exist_ok=True)

    models = sorted(p.name for p in (data / "hyp").iterdir() if p.is_dir())
    recordings = sorted({f.stem for m in models for f in (data / "hyp" / m).glob("*.json")})
    scored = [r for r in recordings if reference(data, r) is not None]

    print(f"{'model':16}" + "".join(f"{r:>18}" for r in scored) + f"{'all scored':>12}{'median ms':>11}")
    for m in models:
        cells, totals, secs = [], [0, 0], []
        for r in recordings:
            path = data / "hyp" / m / f"{r}.json"
            if not path.exists():
                cells.append(f"{'-':>18}") if r in scored else None
                continue
            utts = json.loads(path.read_text())["utterances"]
            secs += [u["secs"] for u in utts]
            if r not in scored:
                continue
            ref = normalize(reference(data, r))
            hyp = normalize(" ".join(u["text"] for u in utts))
            out = jiwer.process_words(ref, hyp)
            errors = out.substitutions + out.deletions + out.insertions
            words = out.hits + out.substitutions + out.deletions
            totals[0] += errors
            totals[1] += words
            cells.append(f"{100 * out.wer:9.1f}% ({errors:>2}/{words})")
            (report / f"{m}--{r}.txt").write_text(jiwer.visualize_alignment(out, show_measures=True))
        overall = f"{100 * totals[0] / totals[1]:11.1f}%" if totals[1] else f"{'-':>12}"
        print(f"{m:16}" + "".join(cells) + overall + f"{1000 * statistics.median(secs):11.0f}")


if __name__ == "__main__":
    main()
