#!/usr/bin/env python3
"""Makes the blind listening set from what trial.py made.

    env/bin/python harness/blind.py OUT MODEL=LABEL... [--check-only]

Every clip goes through what a call does to audio, then gets a phone-readable file:
  1. to 24 kHz mono, as the speech node hands audio to the hub (speakingRate in hub/opus.go);
  2. silence at each end evened out, so a model's padding is neither a tell nor a difference;
  3. to the same loudness, -23 LUFS, so a louder model doesn't win;
  4. Opus as the hub sets it up (hub/opus.go): 24 kbps, mono, 20 ms frames, VoIP, FEC on with 10%
     expected loss, and libopus's defaults for what the hub doesn't set (constrained VBR,
     complexity 9), then decoded at 48 kHz, as the phone does;
  5. a small trim, since Opus moves the level a little and not equally for every voice;
  6. AAC at 128 kbps, far above what's left of the signal, so it adds nothing to hear.

Sentence clips are each matched on their own. The `run` is the 24 sentences, each made separately,
in order with 350 ms between them, and is matched as a whole with one gain, so any drift in loudness
from sentence to sentence stays audible; it goes through Opus as one stream, as on a call.

Voices get letters at random. The letters go in OUT/key.json and nowhere else: this script prints
none of it, and the files carry no tags and all the same time.
"""
import argparse
import json
import os
import pathlib
import secrets
import subprocess
import sys
import tempfile

import numpy as np
import pyloudnorm
import soundfile
import soxr

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from opus_args import OPUS, RATE  # noqa: E402

ROOT = pathlib.Path(os.environ.get("TTS_ROOT", "~/ringback-models-test/tts")).expanduser()
TARGET = -23.0  # a short word has a high peak for its loudness: at -20 LUFS some reached 1.16
LEAD, TAIL = 0.10, 0.20  # seconds of quiet kept at each end of a clip
GAP = 0.35
LETTERS = "ABCDEFGHIJ"
STAMP = 1762387200  # every file gets this time, so the order they were made in says nothing


def run(*cmd):
    subprocess.run(cmd, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def load(path):
    x, rate = soundfile.read(path, dtype="float64")
    return soxr.resample(x, rate, RATE) if rate != RATE else x


def trimmed(x):
    """Cuts the ends to where the speech is, to 40 dB under its loudest 20 ms, then evens them out."""
    n = int(RATE * 0.02)
    frames = x[: len(x) // n * n].reshape(-1, n)
    rms = np.sqrt((frames ** 2).mean(axis=1)) + 1e-9
    on = np.flatnonzero(rms > rms.max() * 0.01)
    # A frame either side, so a soft start or end isn't clipped.
    start, end = max(on[0] - 1, 0) * n, min(on[-1] + 2, len(rms)) * n
    return x[start:end]


def padded(x):
    return np.concatenate([np.zeros(int(RATE * LEAD)), x, np.zeros(int(RATE * TAIL))])


def lufs(x, rate):
    """Integrated loudness; what's under a second is repeated, since it's measured in 400 ms blocks."""
    if len(x) < rate:
        x = np.tile(x, int(rate / max(len(x), 1)) + 1)
    return pyloudnorm.Meter(rate).integrated_loudness(x)


def through_opus(x, tmp):
    w, o, d = tmp / "in.wav", tmp / "out.ogg", tmp / "decoded.wav"
    soundfile.write(w, x, RATE, subtype="PCM_16")
    run("ffmpeg", "-y", "-v", "error", "-i", str(w), *OPUS, str(o))
    run("ffmpeg", "-y", "-v", "error", "-i", str(o), "-ar", "48000", "-ac", "1", "-c:a", "pcm_f32le", str(d))
    y, _ = soundfile.read(d, dtype="float64")
    return y


def finish(y, path, tmp):
    path.parent.mkdir(parents=True, exist_ok=True)
    w = tmp / "final.wav"
    soundfile.write(w, y, 48000, subtype="FLOAT")
    run("ffmpeg", "-y", "-v", "error", "-i", str(w), "-map_metadata", "-1", "-c:a", "aac", "-b:a", "128k",
        "-ac", "1", "-ar", "48000", "-fflags", "+bitexact", "-flags:a", "+bitexact",
        "-movflags", "+faststart", str(path))
    os.utime(path, (STAMP, STAMP))


def make(x, path, tmp):
    """One clip through the whole chain; returns its seconds and its highest peak on the way."""
    gain = 10 ** ((TARGET - lufs(x, RATE)) / 20)
    # Opus takes 16 bits, so nothing may go over full scale; the trim after it makes up any lost level.
    x = x * min(gain, 0.99 / np.abs(x).max())
    y = through_opus(x, tmp)
    y = y * 10 ** ((TARGET - lufs(y, 48000)) / 20)
    finish(y, path, tmp)
    return {"seconds": round(len(y) / 48000, 2), "peak": max(float(np.abs(x).max()), float(np.abs(y).max()))}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("out")
    ap.add_argument("models", nargs="+", help="MODEL=LABEL under ROOT/runs")
    args = ap.parse_args()
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    models = [m.split("=") for m in args.models]
    letters = list(LETTERS[: len(models)])
    secrets.SystemRandom().shuffle(letters)
    key = dict(sorted((letters[i], models[i][0]) for i in range(len(models))))
    (out / "key.json").write_text(json.dumps(key, indent=1) + "\n")

    sentences = json.loads((HERE / "sentences.json").read_text())
    clips, stats = [], {}
    with tempfile.TemporaryDirectory() as t:
        tmp = pathlib.Path(t)
        for letter, model in key.items():
            label = dict(models)[model]
            src = ROOT / "runs" / model / label
            peaks = []
            for s in sentences:
                if s["kind"] == "run":
                    continue
                for variant, stem in (("text", s["id"]), ("spoken", s["id"] + "-spoken")):
                    if variant == "spoken" and "spoken" not in s:
                        continue
                    file = f"{letter}/{stem}.m4a"
                    m = make(padded(trimmed(load(src / f"{s['id'] + ('-spoken' if variant == 'spoken' else '')}.wav"))),
                             out / file, tmp)
                    peaks.append(m["peak"])
                    clip = {"voice": letter, "id": s["id"], "kind": s["kind"], "variant": variant,
                            "sentence": s["text"], "file": file, "seconds": m["seconds"]}
                    if variant == "spoken":
                        clip["read_as"] = s["spoken"]
                    clips.append(clip)
            parts = []
            run_text = []
            for s in sentences:
                if s["kind"] == "run":
                    parts += [trimmed(load(src / f"{s['id']}.wav")), np.zeros(int(RATE * GAP))]
                    run_text.append(s["text"])
            file = f"{letter}/run.m4a"
            m = make(padded(np.concatenate(parts[:-1])), out / file, tmp)
            peaks.append(m["peak"])
            clips.append({"voice": letter, "id": "run", "kind": "run", "variant": "text",
                          "sentence": " ".join(run_text), "file": file, "seconds": m["seconds"]})
            stats[letter] = max(peaks)
    (out / "clips.json").write_text(json.dumps(clips, indent=1, ensure_ascii=False) + "\n")
    os.utime(out / "clips.json", (STAMP, STAMP))
    (ROOT / "blind-peaks.json").write_text(json.dumps(stats, indent=1))
    print(len(clips), "clips for", len(key), "voices; highest peak", round(max(stats.values()), 3))


if __name__ == "__main__":
    main()
