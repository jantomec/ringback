#!/usr/bin/env python3
"""Scores what trial.py made, with no GPU: a rough check for mistakes, and how much the voice moves.

    env/bin/python harness/score.py MODEL... [--label all]

Mistakes: each clip goes through Parakeet (the speech node's model, int8, on the CPU), and the
transcript is searched for the sentence's key items (`keys` in sentences.json). It's only a rough
check: a miss may be Parakeet's own, it can't hear stress or a wrong sound in a right word, and a
hit doesn't mean it sounded right. For the sentences with no keys it gives the share of words wrong.

Over the `run` sentences, each made on its own, it measures how far median pitch, loudness and pace
move between them, and the silence around each sentence. stability.py asks whether the speaker stays
the same.

Writes ROOT/runs/MODEL/LABEL/score.json and prints one line per model.
"""
import argparse
import json
import os
import pathlib
import re

import numpy as np
import pyloudnorm
import sherpa_onnx
import soundfile
import soxr

ROOT = pathlib.Path(os.environ.get("TTS_ROOT", "~/ringback-models-test/tts")).expanduser()
HERE = pathlib.Path(__file__).resolve().parent
PARAKEET = pathlib.Path("~/ringback-speech/models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8").expanduser()
RATE = 16000


def recogniser():
    d = PARAKEET
    return sherpa_onnx.OfflineRecognizer.from_transducer(
        encoder=str(d / "encoder.int8.onnx"), decoder=str(d / "decoder.int8.onnx"),
        joiner=str(d / "joiner.int8.onnx"), tokens=str(d / "tokens.txt"),
        model_type="nemo_transducer", num_threads=8, provider="cpu")


def transcribe(rec, audio):
    # A second of silence after it, as a call gives the recogniser.
    padded = np.concatenate([np.zeros(RATE // 4, "float32"), audio, np.zeros(RATE, "float32")])
    s = rec.create_stream()
    s.accept_waveform(RATE, padded)
    rec.decode_stream(s)
    return s.result.text.strip()


def norm(s):
    """Lower case, no punctuation but the point in a number, thousands joined."""
    s = s.lower().replace("’", "'")
    s = re.sub(r"(?<=\d),(?=\d{3}\b)", "", s)
    s = re.sub(r"[^a-z0-9.:' ]+", " ", s)
    s = re.sub(r"(?<!\d)[.:]|[.:](?!\d)", " ", s)
    return " ".join(s.split())


def found(form, heard):
    """A key in a transcript: whole words, except that a number may run into its unit ("64gb")."""
    f = norm(form)
    if f[0].isdigit():
        return re.search(rf"(?<![\d.]){re.escape(f)}(?![\d])(?!\.\d)", heard) is not None
    return f" {f} " in f" {heard} " or (len(f) > 3 and f in heard)


def key_check(hyp, keys):
    heard = norm(hyp)
    return [forms[0] for forms in keys if not any(found(f, heard) for f in forms)]


def wer(ref, hyp):
    r, h = norm(ref).split(), norm(hyp).split()
    d = list(range(len(h) + 1))
    for i, rw in enumerate(r, 1):
        prev, d[0] = d[0], i
        for j, hw in enumerate(h, 1):
            prev, d[j] = d[j], min(d[j] + 1, d[j - 1] + 1, prev + (rw != hw))
    return d[len(h)], len(r)


def active(x, rate=RATE):
    """The samples within 40 dB of the loudest 20 ms: where the speech is."""
    n = int(rate * 0.02)
    frames = x[: len(x) // n * n].reshape(-1, n)
    rms = np.sqrt((frames ** 2).mean(axis=1)) + 1e-9
    on = np.flatnonzero(rms > rms.max() * 0.01)
    if len(on) == 0:
        return 0, 0
    return on[0] * n, (on[-1] + 1) * n


def pitch_track(x):
    """F0 of the voiced frames, by autocorrelation (70-400 Hz): a list of (seconds, Hz)."""
    n, hop = int(RATE * 0.04), int(RATE * 0.01)
    lo, hi = RATE // 400, RATE // 70
    track = []
    for start in range(0, len(x) - n, hop):
        f = x[start: start + n]
        f = f - f.mean()
        e = (f ** 2).sum()
        if e < 1e-5:
            continue
        c = np.correlate(f, f, "full")[n - 1:]
        lag = lo + int(np.argmax(c[lo:hi]))
        if c[lag] / e > 0.5:
            track.append((start / RATE, RATE / lag))
    return track


def pitch(x):
    """Median F0 of the voiced frames, in Hz."""
    track = pitch_track(x)
    return float(np.median([f for _, f in track])) if track else float("nan")


def final_rise(x):
    """Semitones from the voiced stretch before the last 250 ms to the last 250 ms: above zero, the
    sentence ends going up, as a yes/no question usually does. A rough stand-in for the ear."""
    track = pitch_track(x)
    if len(track) < 10:
        return float("nan")
    end = track[-1][0]
    last = [f for t, f in track if t >= end - 0.25]
    before = [f for t, f in track if end - 0.75 <= t < end - 0.25]
    if not last or not before:
        return float("nan")
    return float(12 * np.log2(np.median(last) / np.median(before)))


def lufs(x, rate=RATE):
    if len(x) < rate:  # the measure needs 400 ms blocks: repeat what's short
        x = np.tile(x, int(rate / max(len(x), 1)) + 1)
    return pyloudnorm.Meter(rate).integrated_loudness(x)


def movement(items):
    p = np.array([it["pitch"] for it in items])
    st = 12 * np.log2(p / np.nanmedian(p))
    level = np.array([it["lufs"] for it in items])
    pace = np.array([it["pace"] for it in items])
    return {
        "pitch_median_hz": float(np.nanmedian(p)),
        # Sentence to sentence, as a spread that one octave error in the estimate can't move much.
        "pitch_iqr_semitones": float(np.subtract(*np.nanpercentile(st, [75, 25]))),
        "pitch_range_semitones": float(np.nanmax(st) - np.nanmin(st)),
        "loudness_sd_lu": float(level.std()), "loudness_range_lu": float(level.max() - level.min()),
        "pace_chars_per_s": float(pace.mean()), "pace_sd_percent": float(100 * pace.std() / pace.mean()),
    }


def score(model, label, sentences, rec):
    out = ROOT / "runs" / model / label
    rows = []
    for s in sentences:
        for variant, text in (("", s["text"]), ("-spoken", s.get("spoken"))):
            f = out / f"{s['id']}{variant}.wav"
            if text is None or not f.exists():
                continue
            x, rate = soundfile.read(f, dtype="float32")
            raw = len(x) / rate
            a, b = active(x, rate)
            lead, tail = a / rate, (len(x) - b) / rate
            y = soxr.resample(x, rate, RATE) if rate != RATE else x
            hyp = transcribe(rec, y) if len(y) else ""
            row = {"id": s["id"] + variant, "kind": s["kind"], "text": text, "heard": hyp,
                   "seconds": raw, "lead_s": lead, "tail_s": tail, "peak": float(np.abs(x).max()) if len(x) else 0}
            if "keys" in s:
                row["missed"] = key_check(hyp, s["keys"])
                row["keys"] = len(s["keys"])
            else:
                row["errors"], row["words"] = wer(text, hyp)
            if s["kind"] in ("yes_no", "wh") and len(y):
                a16, b16 = active(y)
                row["final_rise_st"] = final_rise(y[a16:b16])
            if s["kind"] == "run" and len(y):
                a16, b16 = active(y)
                core = y[a16:b16]
                row["_item"] = {"pitch": pitch(core), "lufs": lufs(core),
                                "pace": len(text) / max(len(core) / RATE, 0.1)}
            rows.append(row)

    def part(pick):
        return [r for r in rows if pick(r)]

    raw_tech = part(lambda r: "keys" in r and not r["id"].endswith("-spoken"))
    spoken = part(lambda r: "keys" in r and r["id"].endswith("-spoken"))
    plain = part(lambda r: "keys" not in r)
    items = [r.pop("_item") for r in rows if "_item" in r]
    result = {
        "model": model,
        "raw_keys_missed": sum(len(r["missed"]) for r in raw_tech),
        "raw_keys_total": sum(r["keys"] for r in raw_tech),
        "spoken_keys_missed": sum(len(r["missed"]) for r in spoken),
        "spoken_keys_total": sum(r["keys"] for r in spoken),
        "plain_wer_percent": 100 * sum(r["errors"] for r in plain) / max(sum(r["words"] for r in plain), 1),
        "lead_s_median": float(np.median([r["lead_s"] for r in rows])),
        "tail_s_median": float(np.median([r["tail_s"] for r in rows])),
        "peak_max": max(r["peak"] for r in rows),
        "yes_no_final_rise_st": float(np.nanmedian([r["final_rise_st"] for r in rows if r["kind"] == "yes_no"] or [np.nan])),
        "wh_final_rise_st": float(np.nanmedian([r["final_rise_st"] for r in rows if r["kind"] == "wh"] or [np.nan])),
        "rows": rows,
    }
    if len(items) >= 3:
        result["movement"] = movement(items)
    (out / "score.json").write_text(json.dumps(result, indent=1, ensure_ascii=False))
    return result


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("models", nargs="+")
    ap.add_argument("--label", default="all")
    args = ap.parse_args()
    sentences = json.loads((HERE / "sentences.json").read_text())
    rec = recogniser()
    for m in args.models:
        r = score(m, args.label, sentences, rec)
        st = r.get("movement", {})
        print(json.dumps({k: (round(v, 3) if isinstance(v, float) else v) for k, v in {
            "model": m, "raw_missed": f"{r['raw_keys_missed']}/{r['raw_keys_total']}",
            "spoken_missed": f"{r['spoken_keys_missed']}/{r['spoken_keys_total']}",
            "plain_wer": r["plain_wer_percent"], **st}.items()}), flush=True)


if __name__ == "__main__":
    main()
