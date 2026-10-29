"""What the voice listens to: when your speech counts as speech.

Two settings from the app set the thresholds of voice-activity detection (see
"Interruptions" in docs/design.md). The interruption sensitivity sets how sure and how long
speech must be. The background filter ("Ignore Background Noise" in the app, sent as 0 when
it's off) is a level gate: it ignores speech quieter than a volume, such as people talking
farther from the microphone than you.
"""

from pipecat.audio.vad.vad_analyzer import VADParams

SENSITIVITY = 0.5  # the app's default
GATE = 0.5  # the app's default for the background filter

# Voice-activity thresholds at the app's lowest, middle and highest sensitivity, with
# straight lines in between. The middle is Pipecat's defaults, which the calls so far used.
# The ends are untested: lower needs clearer, longer speech, for noisy places.
LOWEST = VADParams(confidence=0.85, start_secs=0.35)
MIDDLE = VADParams()  # confidence 0.7, start_secs 0.2
HIGHEST = VADParams(confidence=0.55, start_secs=0.1)

# Pipecat's volume, from 0 to 1 for -110 to -10 LUFS, so 0.1 is 10 dB. With the filter off
# it's Pipecat's default, which the calls so far used. The default, 0.7, is a rough
# estimate: offline it kept out a voice 20 dB below yours, and your AirPods recordings
# measured 0.75-0.89. At its strongest, it would ignore some of your own quieter speech.
GATE_OFF = 0.6  # -50 LUFS
GATE_STRONGEST = 0.8  # -30 LUFS


def thresholds(sensitivity: float, gate: float) -> VADParams:
    """Voice-activity thresholds for a sensitivity and a background filter, each 0 to 1."""
    s = min(max(sensitivity, 0.0), 1.0)
    low, high, t = (LOWEST, MIDDLE, 2 * s) if s < 0.5 else (MIDDLE, HIGHEST, 2 * s - 1)
    at = lambda name: round(getattr(low, name) + (getattr(high, name) - getattr(low, name)) * t, 3)
    g = min(max(gate, 0.0), 1.0)
    return VADParams(
        confidence=at("confidence"),
        start_secs=at("start_secs"),
        stop_secs=MIDDLE.stop_secs,
        min_volume=round(GATE_OFF + (GATE_STRONGEST - GATE_OFF) * g, 3),
    )


def describe(params: VADParams) -> str:
    return f"confidence {params.confidence}, {params.start_secs} s of speech, volume {params.min_volume}"
