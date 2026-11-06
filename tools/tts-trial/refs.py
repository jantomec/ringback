#!/usr/bin/env python3
"""Picks a reference voice for a model that has no built-in one, from clips made by the model itself
(`trial.py MODEL --refs N`), so no real person's voice is cloned and no consent is needed.

    env/bin/python harness/refs.py MODEL [--name NAME]

There's no ear in this. A clip qualifies if Parakeet hears its text with at most one word wrong, and
the one with the median pitch nearest af_heart's (about 197 Hz) wins, so the voices are alike in
kind; a better voice may well be among the others. Writes ROOT/refs/NAME.wav and NAME.txt.
"""
import argparse
import json
import os
import pathlib
import shutil
import sys

import soundfile
import soxr

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import score  # noqa: E402

ROOT = pathlib.Path(os.environ.get("TTS_ROOT", "~/ringback-models-test/tts")).expanduser()
TARGET_HZ = 197.0

ap = argparse.ArgumentParser()
ap.add_argument("model")
ap.add_argument("--name")
args = ap.parse_args()
d = ROOT / "refs/candidates" / args.model
text = (d / "text.txt").read_text().strip()
rec = score.recogniser()
rows = []
for f in sorted(d.glob("seed-*.wav")):
    x, rate = soundfile.read(f, dtype="float32")
    y = soxr.resample(x, rate, 16000) if rate != 16000 else x
    a, b = score.active(y)
    errors, words = score.wer(text, score.transcribe(rec, y))
    rows.append({"seed": f.stem, "seconds": round(len(x) / rate, 2), "errors": errors,
                 "pitch_hz": round(score.pitch(y[a:b]), 1)})
print(json.dumps(rows))
good = [r for r in rows if r["errors"] <= 1 and 5 < r["seconds"] < 20]
if not good:
    sys.exit("no clip qualifies")
best = min(good, key=lambda r: abs(r["pitch_hz"] - TARGET_HZ))
name = args.name or args.model
shutil.copy(d / f"{best['seed']}.wav", ROOT / "refs" / f"{name}.wav")
(ROOT / "refs" / f"{name}.txt").write_text(text + "\n")
print("picked", best)
