#!/usr/bin/env python3
"""How much later the voice hears you start speaking through VoxLock.

    uv run --project voice tools/voxlock-delay/voxlock_delay.py SAMPLE CLIP... [--sensitivity S]

Plays each CLIP (a 16 kHz mono WAV file, optionally cut to a span: "file.wav:12-30") in
20 ms frames, as a call delivers them, into Pipecat's voice-activity detection with the
voice's thresholds: once directly, and once through the voice's VoxLock filter
(hearing.your_voice) given the voice sample SAMPLE, a CLIP of at least 3 s. Each time
detection starts hearing speech, which is when the voice stops if it's talking, is matched
with the nearest start in the other run. This measures the delay from VoxLock's 80 ms
chunks, its 24 ms lag and any change in what detection hears, on the recording's own
clock. On a call, the time VoxLock takes to compute each chunk comes on top; it's timed
here too.
"""

import argparse
import asyncio
import re
import statistics
import time
import wave

import numpy as np
from pipecat.audio.vad.silero import SileroVADAnalyzer
from pipecat.audio.vad.vad_analyzer import VADState

from ringback_voice import hearing, models

FRAME = 320  # samples: calls deliver 20 ms frames
MATCH = 1.0  # seconds: starts this close in the two runs are the same start


def clip(spec: str) -> np.ndarray:
    m = re.fullmatch(r"(.+\.wav)(?::([\d.]+)-([\d.]+))?", spec)
    with wave.open(m[1]) as w:
        assert w.getframerate() == models.HEARING_RATE and w.getnchannels() == 1, f"{spec}: need 16 kHz mono"
        audio = np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16)
    if m[2]:
        audio = audio[int(float(m[2]) * models.HEARING_RATE) : int(float(m[3]) * models.HEARING_RATE)]
    return audio


async def starts(audio: np.ndarray, sensitivity: float, sample: np.ndarray | None) -> tuple[list[float], list[float]]:
    """When detection starts hearing speech (s), directly or through VoxLock if there's a
    sample, and the seconds VoxLock took for each chunk."""
    vad = SileroVADAnalyzer(params=hearing.thresholds(sensitivity))
    vad.set_sample_rate(models.HEARING_RATE)  # as the pipeline does when it starts
    voxlock = None
    if sample is not None:
        voxlock = hearing.your_voice(sample)
        await voxlock.start(models.HEARING_RATE)
    found, computing, state = [], [], VADState.QUIET
    for i in range(0, len(audio) - FRAME + 1, FRAME):
        now = (i + FRAME) / models.HEARING_RATE  # when this frame has fully arrived
        chunk = audio[i : i + FRAME].tobytes()
        if voxlock:
            t = time.perf_counter()
            chunk = await voxlock.filter(chunk)
            if chunk:
                computing.append(time.perf_counter() - t)
        if not chunk:
            continue
        # Like Pipecat's VAD controller: speech starts on going from quiet to speaking.
        new = await vad.analyze_audio(chunk)
        if new == VADState.SPEAKING and state == VADState.QUIET:
            found.append(now)
        if new in (VADState.QUIET, VADState.SPEAKING):
            state = new
    if voxlock:
        await voxlock.stop()
    return found, computing


async def run(args):
    hearing.load()
    sample = clip(args.sample).astype(np.float32) / 32768
    delays, missed, extra, computing = [], 0, 0, []
    for spec in args.clips:
        audio = clip(spec)
        direct, _ = await starts(audio, args.sensitivity, None)
        filtered, took = await starts(audio, args.sensitivity, sample)
        computing += took
        unmatched, lost = list(filtered), []
        for t in direct:
            near = [f for f in unmatched if abs(f - t) <= MATCH]
            if not near:
                lost.append(t)
                continue
            f = min(near, key=lambda f: abs(f - t))
            unmatched.remove(f)
            delays.append(f - t)
        missed += len(lost)
        extra += len(unmatched)
        print(f"{spec}: {len(direct)} starts directly, {len(filtered)} through VoxLock")
        if lost or unmatched:
            print(f"  only directly at {', '.join(f'{t:.1f}' for t in lost) or '-'} s; "
                  f"only through VoxLock at {', '.join(f'{t:.1f}' for t in unmatched) or '-'} s")
    ms = sorted(1000 * d for d in delays)
    print(f"\nsensitivity {args.sensitivity}: {hearing.describe(hearing.thresholds(args.sensitivity))}")
    print(f"{len(ms)} starts heard both ways; through VoxLock they came:")
    print(f"  median {statistics.median(ms):.0f} ms later, middle half {ms[len(ms) // 4]:.0f} to {ms[3 * len(ms) // 4]:.0f} ms, "
          f"range {ms[0]:.0f} to {ms[-1]:.0f} ms")
    print(f"{missed} heard only directly, {extra} only through VoxLock")
    c = sorted(1000 * t for t in computing)
    print(f"VoxLock computed each 80 ms chunk in {statistics.median(c):.0f} ms (median), {c[int(0.99 * len(c))]:.0f} ms (99th percentile)")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("sample")
    p.add_argument("clips", nargs="+")
    p.add_argument("--sensitivity", type=float, default=hearing.SENSITIVITY)
    asyncio.run(run(p.parse_args()))


if __name__ == "__main__":
    main()
