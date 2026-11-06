"""What a recogniser is given on a call: the speech node's hearing, as far as it cuts stretches.

A port of speech/internal/hearing (Silero through the volume gate, the quiet/starting/speaking/
stopping state machine, and one stretch of audio per stop), so that every model is given exactly
the audio Parakeet is given on a call. Smart Turn isn't here: it decides when a turn ends, not
what is transcribed. The Go code is the source of truth; `check_gate` in prepare.py compares this
port with the results in tools/short-answers/README.md.
"""

import math
import os

import numpy as np
import onnxruntime as ort
from scipy.signal import lfilter

RATE = 16000
FRAME = 320  # 20 ms, as the hub hands audio to the node

PRE_ROLL = RATE  # audio kept before speech starts (1 s)
TRAILING_PAD = RATE // 2  # silence after a stretch, so its last word is heard
RETRY_LEAD = 0.1  # seconds before the speech began that a second transcription keeps

# Thresholds(0.5, 0.5): the app's default sensitivity and background filter.
CONFIDENCE = 0.7
START_SECS = 0.2
STOP_SECS = 0.2
MIN_VOLUME = 0.7  # -40 LUFS

WINDOW = 512
CONTEXT = 64
VOLUME_WINDOW = RATE * 400 // 1000
RESET_SECS, RESET_CONFIDENCE, RESET_WINDOWS = 2.0, 0.1, 5

QUIET, STARTING, SPEAKING, STOPPING = 1, 2, 3, 4


def biquads():
    f0, g, q = 1681.974450955533, 3.999843853973347, 0.7071752369554196
    k = math.tan(math.pi * f0 / RATE)
    vh, vb, a0 = 10 ** (g / 20), (10 ** (g / 20)) ** 0.4996667741545416, 1 + k / q + k * k
    shelf = ([(vh + vb * k / q + k * k) / a0, 2 * (k * k - vh) / a0, (vh - vb * k / q + k * k) / a0],
             [1, 2 * (k * k - 1) / a0, (1 - k / q + k * k) / a0])  # fmt: skip
    f0, q = 38.13547087602444, 0.5003270373238773
    k = math.tan(math.pi * f0 / RATE)
    a0 = 1 + k / q + k * k
    return shelf, ([1, -2, 1], [1, 2 * (k * k - 1) / a0, (1 - k / q + k * k) / a0])


SHELF, HIGH_PASS = biquads()


def volume(samples):
    """loudness.go: one BS.1770 gating block, as 0 to 1 for -110 to -10 LUFS."""
    x = lfilter(*HIGH_PASS, lfilter(*SHELF, samples.astype(np.float64) / 32768))
    energy = float(np.mean(x * x))
    if energy <= 0:
        return 0.0
    lufs = 10 * math.log10(energy) - 0.691
    return 0.0 if lufs < -70 else min(1.0, max(0.0, (lufs + 110) / 100))


def round_even(x):
    return int(round(x))  # Python rounds halves to even, as math.RoundToEven


class Vad:
    """vad.go's analyzer."""

    def __init__(self, session):
        self.s = session
        self.start_frames = round_even(START_SECS / (WINDOW / RATE))
        self.stop_frames = round_even(STOP_SECS / (WINDOW / RATE))
        self.buf = np.zeros(0, dtype=np.int16)
        self.state, self.start_count, self.stop_count = QUIET, 0, 0
        self.model_state = np.zeros((2, 1, 128), dtype=np.float32)
        self.context = np.zeros(CONTEXT, dtype=np.float32)
        self.since_reset = self.low_windows = self.voiced_run = 0
        self.volume_buf = np.zeros(0, dtype=np.int16)

    def confidence(self, window):
        x = np.concatenate([self.context, window.astype(np.float32) / 32768])
        p, self.model_state = self.s.run(None, {
            "input": x[None, :], "state": self.model_state, "sr": np.array(RATE, dtype=np.int64)})  # fmt: skip
        self.context = x[WINDOW:]
        p = float(np.asarray(p).reshape(-1)[0])
        self.since_reset += 1
        self.low_windows = self.low_windows + 1 if p < RESET_CONFIDENCE else 0
        if self.since_reset * WINDOW >= RESET_SECS * RATE and self.state == QUIET and self.low_windows >= RESET_WINDOWS:
            self.model_state = np.zeros((2, 1, 128), dtype=np.float32)
            self.context = np.zeros(CONTEXT, dtype=np.float32)
            self.since_reset = 0
        return p

    def speech_volume(self, window, voiced):
        self.volume_buf = np.concatenate([self.volume_buf, window])[-VOLUME_WINDOW:]
        if not voiced:
            self.voiced_run = 0
            return volume(self.volume_buf)
        self.voiced_run += 1
        n = min(len(self.volume_buf), self.voiced_run * WINDOW)
        return volume(self.volume_buf[-n:])

    def analyze(self, samples):
        self.buf = np.concatenate([self.buf, samples])
        if len(self.buf) < WINDOW:
            return self.state
        while len(self.buf) >= WINDOW:
            window = self.buf[:WINDOW]
            confidence = self.confidence(window)
            voiced = np.float32(confidence) >= np.float32(CONFIDENCE)
            loud = self.speech_volume(window, voiced)
            self.buf = self.buf[WINDOW:]
            if voiced and loud >= MIN_VOLUME:
                if self.state == QUIET:
                    self.state, self.start_count = STARTING, 1
                elif self.state == STARTING:
                    self.start_count += 1
                elif self.state == STOPPING:
                    self.state, self.stop_count = SPEAKING, 0
            else:
                if self.state == STARTING:
                    self.state, self.start_count = QUIET, 0
                elif self.state == SPEAKING:
                    self.state, self.stop_count = STOPPING, 1
                elif self.state == STOPPING:
                    self.stop_count += 1
        if self.state == STARTING and self.start_count >= self.start_frames:
            self.state, self.start_count = SPEAKING, 0
        if self.state == STOPPING and self.stop_count >= self.stop_frames:
            self.state, self.stop_count = QUIET, 0
        return self.state


def session(path):
    o = ort.SessionOptions()
    o.intra_op_num_threads, o.inter_op_num_threads = 1, 1
    return ort.InferenceSession(os.fspath(path), o, providers=["CPUExecutionProvider"])


def stretches(pcm, model):
    """stream.go's Audio, started and stopped: the stretches a call would transcribe, from
    16-bit samples. Each is (audio with its trailing pad, retry_from, seconds into pcm it ended).
    A stretch that comes back with no words is written down once more from retry_from."""
    vad = Vad(model)
    said = QUIET
    segment = np.zeros(0, dtype=np.int16)
    speaking_now, segment_start, now = False, 0.0, 0.0
    out = []

    def stop():
        audio = np.concatenate([segment, np.zeros(TRAILING_PAD, dtype=np.int16)])
        start = max(0, len(audio) - TRAILING_PAD - int((now - segment_start + RETRY_LEAD) * RATE))
        out.append((audio, start, now))

    for i in range(0, len(pcm), FRAME):
        frame = pcm[i : i + FRAME]
        now += len(frame) / RATE
        segment = np.concatenate([segment, frame])
        if not speaking_now and len(segment) > PRE_ROLL:
            segment = segment[-PRE_ROLL:]
        state = vad.analyze(frame)
        if state != said and state not in (STARTING, STOPPING):
            started, stopped = state == SPEAKING, state == QUIET
            said = state
            if started:
                speaking_now, segment_start = True, now - START_SECS
            elif stopped:
                speaking_now = False
                stop()
                segment = np.zeros(0, dtype=np.int16)
    if said == SPEAKING:  # the audio ran out while speaking: Stream.Idle
        stop()
    return out
