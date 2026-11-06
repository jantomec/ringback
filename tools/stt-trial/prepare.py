#!/usr/bin/env python3
"""Make the audio every model hears, from the recordings of tools/stt-eval and the clips of
tools/short-answers. Runs once, on any machine with libopus; see README.md.

    python prepare.py DATA_DIR SILERO_ONNX

Reads DATA_DIR/wav, segments and short-answers, and writes DATA_DIR/clips/<set>/*.wav with
DATA_DIR/clips/<set>.json:

- clean: the utterances of tools/stt-eval/segment.py, as they are;
- opus: the same, cut from the recording after it has been through the call's Opus;
- call: the recording after Opus, cut as the speech node cuts a call (gate.py): more, shorter
  stretches, 1 s of audio before each and 0.5 s of silence after it;
- short: the short-answers clips after Opus, cut the same way;
- noise: silence and noise after Opus, whole.

DATA_DIR/clips/opus-stats.json says what the Opus path really did.
"""

import ctypes
import ctypes.util
import json
import sys
from pathlib import Path

import numpy as np
import soundfile as sf
from scipy.signal import butter, lfilter

import gate

RATE = 16000
FRAME = 320  # 20 ms

# hub/opus.go: the phone is asked for mono at 24 kbps with FEC, played back at 16 kHz, and the
# hub decodes straight to 16 kHz. The hub's own encoder expects 10% loss, which is what keeps
# FEC in at this bitrate; packets aren't dropped here.
BITRATE = 24000
EXPECTED_LOSS = 10

VOIP = 2048
SET_BITRATE, SET_MAX_BANDWIDTH, SET_INBAND_FEC, SET_PACKET_LOSS_PERC = 4002, 4004, 4012, 4014
GET_LOOKAHEAD = 4027
WIDEBAND = 1103

MIN_GAP = 0.5  # seconds: a pause between utterances long enough to count as room tone
MAX_ROOM = 12  # room-tone clips from each recording


class Opus:
    """A call's Opus, through libopus itself."""

    def __init__(self):
        lib = ctypes.CDLL(ctypes.util.find_library("opus") or "libopus.so.0")
        lib.opus_encoder_create.restype = lib.opus_decoder_create.restype = ctypes.c_void_p
        self.lib = lib

    def _ctl(self, enc, request, value):
        r = self.lib.opus_encoder_ctl(ctypes.c_void_p(enc), request, ctypes.c_int(value))
        assert r == 0, f"opus ctl {request}: {r}"

    def through(self, pcm):
        """Returns the audio after Opus, in step with what went in, and the packets' stats."""
        lib = self.lib
        err = ctypes.c_int()
        enc = lib.opus_encoder_create(RATE, 1, VOIP, ctypes.byref(err))
        assert err.value == 0
        dec = lib.opus_decoder_create(RATE, 1, ctypes.byref(err))
        assert err.value == 0
        self._ctl(enc, SET_BITRATE, BITRATE)
        self._ctl(enc, SET_MAX_BANDWIDTH, WIDEBAND)
        self._ctl(enc, SET_INBAND_FEC, 1)
        self._ctl(enc, SET_PACKET_LOSS_PERC, EXPECTED_LOSS)
        look = ctypes.c_int()
        assert lib.opus_encoder_ctl(ctypes.c_void_p(enc), GET_LOOKAHEAD, ctypes.byref(look)) == 0

        pad = FRAME - len(pcm) % FRAME
        x = np.concatenate([pcm, np.zeros(pad + FRAME * 2, dtype=np.int16)])
        out = np.zeros(len(x), dtype=np.int16)
        packet = (ctypes.c_ubyte * 4000)()
        sizes, fec = [], 0
        for i in range(0, len(x), FRAME):
            n = lib.opus_encode(ctypes.c_void_p(enc), x[i : i + FRAME].ctypes.data_as(ctypes.c_void_p),
                                FRAME, packet, 4000)  # fmt: skip
            assert n > 0, f"opus_encode: {n}"
            sizes.append(n)
            fec += lib.opus_packet_has_lbrr(packet, n) == 1
            got = lib.opus_decode(ctypes.c_void_p(dec), packet, n,
                                  out[i : i + FRAME].ctypes.data_as(ctypes.c_void_p), FRAME, 0)  # fmt: skip
            assert got == FRAME, f"opus_decode: {got}"
        lib.opus_encoder_destroy(ctypes.c_void_p(enc))
        lib.opus_decoder_destroy(ctypes.c_void_p(dec))
        out = out[look.value : look.value + len(pcm)]
        return out, {
            "packets": len(sizes),
            "kbps": round(8 * sum(sizes) / (len(sizes) * 0.02) / 1000, 2),
            "with_fec": round(fec / len(sizes), 3),
            "lookahead_samples": look.value,
        }


def read(path):
    a, r = sf.read(path, dtype="int16")
    assert r == RATE and a.ndim == 1, f"{path}: need 16 kHz mono"
    return a


def snr_db(a, b):
    n = min(len(a), len(b))
    a, b = a[:n].astype(float), b[:n].astype(float)
    return round(10 * np.log10(np.sum(a * a) / max(np.sum((a - b) ** 2), 1)), 1)


def best_lag(a, b, span=160):
    """Where Opus's output lines up with what went in, in samples: 0 means in step."""
    n = min(len(a), len(b)) - span
    scores = {lag: float(np.dot(a[span : n].astype(float), b[span + lag : n + lag].astype(float)))
              for lag in range(-span, span + 1)}  # fmt: skip
    return max(scores, key=scores.get)


def write_set(data, name, rows):
    (data / "clips" / f"{name}.json").write_text(json.dumps(rows, indent=0))
    secs = [r["secs"] for r in rows]
    print(f"{name}: {len(rows)} clips, {sum(secs):.0f} s, {min(secs):.2f}-{max(secs):.2f} s long")


def save(data, name, cid, pcm):
    d = data / "clips" / name
    d.mkdir(parents=True, exist_ok=True)
    sf.write(d / f"{cid}.wav", pcm, RATE, subtype="PCM_16")
    return f"{name}/{cid}.wav"


def noise_clips(rng):
    """(name, 16-bit samples, kind) of synthetic silence and noise, 3 s long unless named."""
    def scaled(x, dbfs):
        return np.clip(x / np.sqrt(np.mean(x * x)) * 10 ** (dbfs / 20) * 32767, -32768, 32767).astype(np.int16)

    n = 3 * RATE
    for secs in (1, 3, 10):
        yield f"silence-{secs}s", np.zeros(secs * RATE, dtype=np.int16), "digital silence"
    for dbfs in (-60, -50, -40, -30):
        white = rng.standard_normal(n)
        yield f"white{dbfs}", scaled(white, dbfs), "white noise"
        yield f"pink{dbfs}", scaled(lfilter([0.049922, -0.095993, 0.050613, -0.004408],
                                            [1, -2.494956, 2.017265, -0.522189], white), dbfs), "pink noise"  # fmt: skip
        yield f"brown{dbfs}", scaled(np.cumsum(rng.standard_normal(n)) - np.linspace(0, 1, n) * np.cumsum(rng.standard_normal(n))[-1], dbfs), "brown noise"
    t = np.arange(n) / RATE
    yield "hum-50hz", scaled(sum(np.sin(2 * np.pi * 50 * k * t) / k for k in range(1, 6)), -40), "mains hum"
    for i in range(2):
        clicks = np.zeros(n)
        for at in rng.integers(0, n - 800, 14):  # typing: short decaying bursts
            clicks[at : at + 800] += rng.standard_normal(800) * np.exp(-np.arange(800) / 100)
        yield f"typing-{i}", scaled(clicks, -35), "typing"
        b, a = butter(2, [1000 / (RATE / 2), 4000 / (RATE / 2)], "band")
        breath = lfilter(b, a, rng.standard_normal(n)) * (np.sin(np.pi * t * (1 + i)) ** 2)  # slow swells
        yield f"breath-{i}", scaled(breath, -40), "breath"


def main():
    data, silero = Path(sys.argv[1]).expanduser(), gate.session(Path(sys.argv[2]).expanduser())
    (data / "clips").mkdir(exist_ok=True)
    opus = Opus()
    rng = np.random.default_rng(7)
    clean, through, call, noise, stats = [], [], [], [], {}

    for path in sorted((data / "wav").glob("*.wav")):
        rec = path.stem
        pcm = read(path)
        opus_pcm, st = opus.through(pcm)
        st["snr_db"], st["lag_samples"] = snr_db(pcm, opus_pcm), best_lag(pcm, opus_pcm)
        stats[rec] = st

        segs = json.loads((data / "segments" / f"{rec}.json").read_text())
        for i, (start, end) in enumerate(segs):
            a, b = int(start * RATE), int(end * RATE)
            row = {"recording": rec, "index": i, "start": start, "end": end, "secs": round((b - a) / RATE, 3)}
            clean.append(row | {"id": f"{rec}-{i:03}", "file": save(data, "clean", f"{rec}-{i:03}", pcm[a:b])})
            through.append(row | {"id": f"{rec}-{i:03}", "file": save(data, "opus", f"{rec}-{i:03}", opus_pcm[a:b])})

        for i, (audio, retry, ended) in enumerate(gate.stretches(opus_pcm, silero)):
            call.append({"id": f"{rec}-{i:03}", "recording": rec, "index": i, "ended": round(ended, 3), "retry_from": retry,
                         "secs": round(len(audio) / RATE, 3), "file": save(data, "call", f"{rec}-{i:03}", audio)})  # fmt: skip

        gaps = [(segs[i][1], segs[i + 1][0]) for i in range(len(segs) - 1) if segs[i + 1][0] - segs[i][1] >= MIN_GAP]
        for i, (a, b) in enumerate(gaps[:MAX_ROOM]):
            x = opus_pcm[int(a * RATE) : int(min(b, a + 3) * RATE)]
            cid = f"room-{rec}-{i:02}"
            noise.append({"id": cid, "kind": "room tone between utterances", "secs": round(len(x) / RATE, 3), "file": save(data, "noise", cid, x)})  # fmt: skip

    for cid, x, kind in noise_clips(rng):
        out, _ = opus.through(x)
        noise.append({"id": cid, "kind": kind, "secs": round(len(out) / RATE, 3), "file": save(data, "noise", cid, out)})

    short, not_taken = [], []
    src = data / "short-answers"
    for c in json.loads((src / "clips.json").read_text()):
        out, _ = opus.through(read(src / c["file"]))
        parts = gate.stretches(out, silero)
        if not parts:
            not_taken.append(c)
        stem = c["file"].removesuffix(".wav")
        for i, (audio, retry, _) in enumerate(parts):
            short.append(c | {"id": f"{stem}-{i}", "clip": stem, "part": i, "retry_from": retry, "secs": round(len(audio) / RATE, 3),
                              "file": save(data, "short", f"{stem}-{i}", audio)})  # fmt: skip

    write_set(data, "clean", clean)
    write_set(data, "opus", through)
    write_set(data, "call", call)
    write_set(data, "short", short)
    write_set(data, "noise", noise)
    (data / "clips" / "short-not-taken.json").write_text(json.dumps(not_taken, indent=0))
    (data / "clips" / "opus-stats.json").write_text(json.dumps(stats, indent=1))
    print(f"short: {len(not_taken)} clips weren't taken for speech")
    print(json.dumps(stats, indent=1))


def check_gate(clips_dir, silero_path):
    """Compares this port with tools/short-answers/README.md's results for the filter at its
    default, without Opus: 168 of 168 at -25 to -35 LUFS, 45 of 56 at -40, 0 of 112 below."""
    src, silero = Path(clips_dir).expanduser(), gate.session(Path(silero_path).expanduser())
    counts = {}
    for c in json.loads((src / "clips.json").read_text()):
        taken = bool(gate.stretches(read(src / c["file"]), silero))
        k = counts.setdefault(c["level"], [0, 0])
        k[0] += taken
        k[1] += 1
    print({level: f"{a} of {b}" for level, (a, b) in sorted(counts.items(), reverse=True)})


if __name__ == "__main__":
    if sys.argv[1] == "--check-gate":
        check_gate(sys.argv[2], sys.argv[3])
    else:
        main()
