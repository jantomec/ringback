#!/usr/bin/env python3
"""Checks the blind set by reading the finished files, not by trusting what made them.

    env/bin/python -I harness/check_blind.py OUT

For every clip in clips.json: is it AAC, mono, 48 kHz, with nothing in its tags; how loud is it; and
does Parakeet hear the sentence clips.json says is in it? Prints totals and anything out of line by
letter. It doesn't read key.json.
"""
import json
import pathlib
import statistics
import subprocess
import sys

import numpy as np
import pyloudnorm
import soxr

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import score  # noqa: E402

out = pathlib.Path(sys.argv[1])
clips = json.loads((out / "clips.json").read_text())
rec = score.recogniser()
meter = pyloudnorm.Meter(48000)
files = sorted(p for p in out.rglob("*.m4a"))
print(len(files), "files on disk,", len(clips), "in clips.json")
assert {str(p.relative_to(out)) for p in files} == {c["file"] for c in clips}, "files and clips.json differ"

loudness, tags, bad, mismatch = {}, set(), [], []
for c in clips:
    p = out / c["file"]
    info = json.loads(subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "stream=codec_name,channels,sample_rate:stream_tags:format_tags",
         "-of", "json", str(p)], capture_output=True, text=True, check=True).stdout)
    s = info["streams"][0]
    if (s["codec_name"], s["channels"], s["sample_rate"]) != ("aac", 1, "48000"):
        bad.append((c["file"], s))
    tags.add(json.dumps({**info.get("format", {}).get("tags", {}), **s.get("tags", {})}, sort_keys=True))
    raw = subprocess.run(["ffmpeg", "-v", "error", "-i", str(p), "-f", "f32le", "-ac", "1", "-ar", "48000", "-"],
                         capture_output=True, check=True).stdout
    x = np.frombuffer(raw, "float32").astype("float64")
    y = np.tile(x, int(48000 / len(x)) + 1) if len(x) < 48000 else x
    c["lufs"] = meter.integrated_loudness(y)
    c["peak"] = float(np.abs(x).max())
    loudness.setdefault((c["id"], c["variant"]), {})[c["voice"]] = c["lufs"]
    heard = score.transcribe(rec, soxr.resample(x, 48000, 16000).astype("float32"))
    errors, words = score.wer(c.get("read_as", c["sentence"]), heard)
    c["wrong"] = errors / max(words, 1)
    if c["kind"] not in ("number", "path") and c["wrong"] > 0.34:
        mismatch.append((c["file"], c["sentence"][:40], heard[:60]))

print("not AAC, mono, 48 kHz:", bad or "none")
print("tags seen across all files:", sorted(tags))
values = [c["lufs"] for c in clips]
print(f"loudness: {min(values):.2f} to {max(values):.2f} LUFS, median {statistics.median(values):.2f}; "
      f"highest peak {max(c['peak'] for c in clips):.3f}")
spread = {k: max(v.values()) - min(v.values()) for k, v in loudness.items()}
print(f"between voices on one sentence: {statistics.median(spread.values()):.2f} LU median, "
      f"{max(spread.values()):.2f} LU at worst ({max(spread, key=spread.get)})")
for letter in sorted({c["voice"] for c in clips}):
    v = [c for c in clips if c["voice"] == letter]
    print(f"{letter}: {len(v)} clips, {sum(c['seconds'] for c in v):.0f} s, "
          f"median {statistics.median(c['lufs'] for c in v):.2f} LUFS, "
          f"{sum(c['wrong'] > 0 for c in v if c['kind'] not in ('number', 'path'))} plain clips not heard exactly")
print("clips that don't sound like their sentence:", mismatch or "none")
