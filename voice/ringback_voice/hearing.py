"""What the voice listens to: when your speech counts as speech.

The app's interruption sensitivity sets the thresholds of voice-activity detection (see
"Interruptions" in docs/design.md).
"""

from pipecat.audio.vad.vad_analyzer import VADParams

SENSITIVITY = 0.5  # the app's default

# Voice-activity thresholds at the app's lowest, middle and highest sensitivity, with
# straight lines in between. The middle is Pipecat's defaults, which the calls so far used.
# The ends are untested: lower needs clearer, longer and louder speech, for noisy places.
LOWEST = VADParams(confidence=0.85, start_secs=0.35, min_volume=0.7)
MIDDLE = VADParams()  # confidence 0.7, start_secs 0.2, min_volume 0.6 (-50 LUFS)
HIGHEST = VADParams(confidence=0.55, start_secs=0.1, min_volume=0.5)


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
