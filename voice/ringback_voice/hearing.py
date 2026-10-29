"""What the voice listens to: only your voice, and only once it's clearly speech.

VoxLock removes other people's voices before voice-activity detection and Parakeet hear the
call. It's given the voice sample you recorded in the app, so it works from your first word.
Without one, it lets everything through until it has heard 3 s of speech, and learns your
voice from that. The app's interruption sensitivity sets the thresholds of voice-activity
detection (see "Interruptions" in docs/design.md).
"""

import base64

import numpy as np
from pipecat.audio.vad.vad_analyzer import VADParams
from voxlock.core import download
from voxlock.pipecat import VoxLockFilter

from ringback_voice import models

PRECISION = "int8"  # VoxLock's default; 0.3x real time on one CPU core in 80 ms chunks
SAMPLE_SECS = 3  # VoxLock uses 3 s of your voice
SENSITIVITY = 0.5  # the app's default

# Voice-activity thresholds at the app's lowest, middle and highest sensitivity, with
# straight lines in between. The middle is Pipecat's defaults, which the calls so far used.
# The ends are untested: lower needs clearer, longer and louder speech, for noisy places.
LOWEST = VADParams(confidence=0.85, start_secs=0.35, min_volume=0.7)
MIDDLE = VADParams()  # confidence 0.7, start_secs 0.2, min_volume 0.6 (-50 LUFS)
HIGHEST = VADParams(confidence=0.55, start_secs=0.1, min_volume=0.5)

_model_dir: str | None = None


def load() -> None:
    """Downloads VoxLock's model the first time (74 MB), so no call waits for it."""
    global _model_dir
    _model_dir = str(download(PRECISION))


def your_voice(sample: np.ndarray | None) -> VoxLockFilter:
    """A filter for one call that keeps only the voice in sample (float, at HEARING_RATE)."""
    return VoxLockFilter(voice_sample=sample, model_dir=_model_dir, precision=PRECISION)


def decode_sample(text: str) -> np.ndarray:
    """The app's voice sample: base64 of 16-bit mono PCM at HEARING_RATE."""
    samples = np.frombuffer(base64.b64decode(text), dtype="<i2").astype(np.float32) / 32768
    if len(samples) < SAMPLE_SECS * models.HEARING_RATE:
        raise ValueError(f"the voice sample is {len(samples) / models.HEARING_RATE:.1f} s, under {SAMPLE_SECS} s")
    return samples


def thresholds(sensitivity: float) -> VADParams:
    """Voice-activity thresholds for a sensitivity from 0 to 1."""
    s = min(max(sensitivity, 0.0), 1.0)
    low, high, t = (LOWEST, MIDDLE, 2 * s) if s < 0.5 else (MIDDLE, HIGHEST, 2 * s - 1)
    at = lambda name: round(getattr(low, name) + (getattr(high, name) - getattr(low, name)) * t, 3)
    return VADParams(
        confidence=at("confidence"), start_secs=at("start_secs"), stop_secs=MIDDLE.stop_secs, min_volume=at("min_volume")
    )


def describe(params: VADParams) -> str:
    return f"confidence {params.confidence}, {params.start_secs} s of speech, volume {params.min_volume}"
