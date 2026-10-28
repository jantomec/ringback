#!/usr/bin/env python3
"""Transcribe every utterance of every recording with one model, timing each.

    uv run --with mlx-audio==0.6.2 tools/stt-eval/transcribe.py MODEL DATA_DIR

MODEL is a key of MODELS. Reads DATA_DIR/wav/*.wav and DATA_DIR/segments/*.json
(from segment.py) and writes DATA_DIR/hyp/MODEL/<recording>.json. The first
utterance is transcribed once untimed, so model loading and warm-up don't count.
"""

import json
import statistics
import sys
import time
import wave
from pathlib import Path

import mlx.core as mx
import numpy as np
from mlx_audio.stt import load

MODELS = {
    "parakeet-v2": ("mlx-community/parakeet-tdt-0.6b-v2", {}),
    "parakeet-v3": ("mlx-community/parakeet-tdt-0.6b-v3", {}),
    "qwen3-asr-1.7b": ("mlx-community/Qwen3-ASR-1.7B-bf16", {"language": "English"}),
    "whisper-turbo": ("mlx-community/whisper-large-v3-turbo-asr-fp16", {"language": "en"}),
}


def read_wav(path):
    with wave.open(str(path)) as w:
        assert w.getframerate() == 16000 and w.getnchannels() == 1, f"{path}: need 16 kHz mono"
        return np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16).astype(np.float32) / 32768


def peak_memory_gb():
    get = getattr(mx, "get_peak_memory", None) or mx.metal.get_peak_memory
    return get() / 1e9


def main():
    name, data = sys.argv[1], Path(sys.argv[2]).expanduser()
    repo, kwargs = MODELS[name]
    model = load(repo)
    out = data / "hyp" / name
    out.mkdir(parents=True, exist_ok=True)

    warmed = False
    for path in sorted((data / "wav").glob("*.wav")):
        audio = read_wav(path)
        rows = []
        for start, end in json.loads((data / "segments" / f"{path.stem}.json").read_text()):
            clip = mx.array(audio[int(start * 16000) : int(end * 16000)])
            if not warmed:
                model.generate(clip, **kwargs)
                warmed = True
            t = time.perf_counter()
            text = model.generate(clip, **kwargs).text.strip()
            rows.append({"start": start, "end": end, "text": text, "secs": round(time.perf_counter() - t, 3)})
        (out / f"{path.stem}.json").write_text(json.dumps({"model": repo, "options": kwargs, "utterances": rows}, indent=1))

        speech = sum(r["end"] - r["start"] for r in rows)
        compute = sum(r["secs"] for r in rows)
        print(
            f"{name} {path.stem}: {len(rows)} utterances, {speech:.0f} s of audio in {compute:.1f} s "
            f"({speech / compute:.0f}x real time), median {1000 * statistics.median(r['secs'] for r in rows):.0f} ms "
            f"per utterance",
            flush=True,
        )
    print(f"{name}: peak memory {peak_memory_gb():.1f} GB")


if __name__ == "__main__":
    main()
