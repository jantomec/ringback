#!/usr/bin/env python3
"""Phonon on the CPU, kept warm: takes a JSON line per sentence and answers with its audio and when
each chunk came. Usage: phonon_trial.py VOICE THREADS. trial.py starts it."""
import base64
import json
import sys
import time

import numpy as np
import ptts

voice, threads = sys.argv[1], int(sys.argv[2])
ptts.set_num_threads(threads)  # it would take one per logical core
assert ptts.get_num_threads() == threads
tts = ptts.TTS(lang="en", quant="q8")
tts.synth("Hello, the voice is ready.", voice=voice, seed=42)
print(json.dumps({"ready": True, "rate": tts.sample_rate, "threads": ptts.get_num_threads()}), flush=True)
for line in sys.stdin:
    text = json.loads(line)["text"]
    t0 = time.time()
    chunks, parts = [], []
    with tts.stream(text, voice=voice, seed=42) as audio:
        for pcm in audio:
            b = (np.clip(pcm, -1, 1) * 32767).astype("<i2").tobytes()
            chunks.append((time.time() - t0, len(b)))
            parts.append(b)
    print(json.dumps({"chunks": chunks, "pcm": base64.b64encode(b"".join(parts)).decode()}), flush=True)
