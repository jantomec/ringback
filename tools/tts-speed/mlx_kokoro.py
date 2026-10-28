#!/usr/bin/env python3
"""Time Kokoro-82M through MLX (mlx-audio), the way the voice runs it.

    uv run --no-project --with mlx-audio==0.6.2 --with 'misaki[en]==0.9.4' tools/tts-speed/mlx_kokoro.py [OUT_DIR]

Each text is timed from the call to generate() to its first and last segment's audio
(MLX is lazy, so each segment is converted to numpy). With OUT_DIR, the long reply is
saved there as a WAV.
"""

import statistics
import sys
import time
import wave
from pathlib import Path

import numpy as np
from mlx_audio.tts.utils import load_model

from texts import RUNS, TEXTS

LABEL = "mlx"
OUT = Path(sys.argv[1]) if len(sys.argv) > 1 else None

t = time.perf_counter()
model = load_model("mlx-community/Kokoro-82M-bf16")
print(f"{LABEL}: load {time.perf_counter() - t:.2f} s", flush=True)

for name, text in TEXTS.items():
    firsts, totals = [], []
    for i in range(RUNS + 1):  # run 0 is the warm-up
        t0 = time.perf_counter()
        first, chunks, sr = None, [], None
        for r in model.generate(text, voice="af_heart", speed=1.0, lang_code="a"):
            chunks.append(np.array(r.audio, dtype=np.float32))
            first = first or time.perf_counter()
            sr = r.sample_rate
        last = time.perf_counter()
        if i:
            firsts.append(first - t0)
            totals.append(last - t0)
    audio = np.concatenate(chunks)
    dur = len(audio) / sr
    print(
        f"{LABEL} {name:6} audio {dur:5.2f} s | first audio median {statistics.median(firsts):.3f} s "
        f"(min {min(firsts):.3f}, max {max(firsts):.3f}) | all audio median {statistics.median(totals):.3f} s "
        f"(min {min(totals):.3f}, max {max(totals):.3f}) | {dur / statistics.median(totals):.0f}x real time "
        f"| segments {len(chunks)}",
        flush=True,
    )
    if name == "long" and OUT:
        OUT.mkdir(parents=True, exist_ok=True)
        with wave.open(str(OUT / f"{LABEL}-long.wav"), "wb") as w:
            w.setnchannels(1), w.setsampwidth(2), w.setframerate(sr)
            w.writeframes((np.clip(audio, -1, 1) * 32767).astype(np.int16).tobytes())
