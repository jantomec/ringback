#!/usr/bin/env python3
"""Split recordings into utterances at pauses, like the live pipeline splits turns.

    uv run --with numpy tools/stt-eval/segment.py DATA_DIR

Reads DATA_DIR/wav/*.wav (16 kHz mono) and writes DATA_DIR/segments/<name>.json, a
list of [start, end] times in seconds. Speech is anything louder than the noise
floor by THRESHOLD_DB; a pause of MIN_GAP seconds or more ends an utterance.
"""

import json
import sys
import wave
from pathlib import Path

import numpy as np

FRAME, HOP = 0.030, 0.010  # seconds
THRESHOLD_DB = 12  # above the noise floor
MIN_GAP = 0.6  # seconds of quiet that end an utterance
MIN_SPEECH = 0.15  # shorter blips are dropped
PAD = 0.2  # seconds kept around each utterance


def read_wav(path):
    with wave.open(str(path)) as w:
        assert w.getnchannels() == 1 and w.getsampwidth() == 2, f"{path}: need 16-bit mono"
        return np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16).astype(np.float32) / 32768, w.getframerate()


def segments(audio, rate):
    frame, hop = int(FRAME * rate), int(HOP * rate)
    n = 1 + max(0, len(audio) - frame) // hop
    rms = np.array([np.sqrt(np.mean(audio[i * hop : i * hop + frame] ** 2)) for i in range(n)])
    db = 20 * np.log10(rms + 1e-10)
    speech = db > np.percentile(db, 10) + THRESHOLD_DB

    spans, start, quiet = [], None, 0
    for i, s in enumerate(speech):
        if s:
            start = i if start is None else start
            quiet = 0
        elif start is not None:
            quiet += 1
            if quiet * HOP >= MIN_GAP:
                spans.append((start, i - quiet + 1))
                start, quiet = None, 0
    if start is not None:
        spans.append((start, n - quiet))

    duration = len(audio) / rate
    return [
        [round(max(0, a * HOP - PAD), 3), round(min(duration, b * HOP + FRAME + PAD), 3)]
        for a, b in spans
        if (b - a) * HOP >= MIN_SPEECH
    ]


def main():
    data = Path(sys.argv[1]).expanduser()
    (data / "segments").mkdir(exist_ok=True)
    for path in sorted((data / "wav").glob("*.wav")):
        audio, rate = read_wav(path)
        segs = segments(audio, rate)
        (data / "segments" / f"{path.stem}.json").write_text(json.dumps(segs))
        lengths = [b - a for a, b in segs]
        print(f"{path.stem}: {len(segs)} utterances, {min(lengths):.1f}-{max(lengths):.1f} s long")


if __name__ == "__main__":
    main()
