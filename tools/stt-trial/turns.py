#!/usr/bin/env python3
"""Writing down a whole turn at once: how long Parakeet v2 takes, and how many words it gets
wrong, on your recordings after the call's Opus (prepare.py's path), with the speech node's
settings (sherpa_run). Today the node writes down each stretch of speech on its own, cut at
0.2 s pauses; a whole turn would be written down again at each pause, in the 0.5 s between
Silero's stop and your words going to the model (docs/design.md, "Whose turn it is").

    gpu.sh $STT/envs/harness/bin/python turns.py DATA_DIR RUNS_DIR OUT_DIR [--voice]

- speed: spans of the free speech, 2 to 300 s, as a whole turn would be heard (with 0.5 s of
  silence after). Each is written down once, as every turn on a call is a length not seen
  before, then twice more.
- --voice: the same again while Qwen3 speaks without a break through the node's audio.cpp
  server, the worst case: on a call the voice is mostly cut off while you speak.
- words: each recording cut into turns of about 10, 30 and 60 s at the pauses between its
  utterances (up to 1 s before each, not reaching into the one before, and 0.2 s after),
  each written down in one pass and scored as tally.py scores the call set, beside the call
  and opus sets' own scores from RUNS_DIR/parakeet-v2.

Writes OUT_DIR/turns.json, times and error counts with no words, and OUT_DIR/turns-text.json
with the words: keep that out of the repo.
"""

import argparse
import base64
import json
import os
import socket
import statistics
import subprocess
import threading
import time
import urllib.request
from pathlib import Path

import numpy as np

import tally  # its scoring, and stt-eval's references
from backends import MODELS
from prepare import RATE, Opus, read

SPEED_SECS = [2, 5, 10, 20, 30, 45, 60, 90, 120, 180, 300]
SAMPLES = 3  # spans of each length up to 60 s, from different places, each a little longer
AGAIN = 2
TURN_SECS = [10, 30, 60]
PAD = RATE // 2  # silence after, as the node's trailingPad
INSTALL = Path(os.environ.get("NODE_INSTALL", "~/ringback-speech")).expanduser()
VOICE_TEXT = (
    "I looked through the parser change this morning and it holds up well. The old version read the whole file "
    "before it checked the header, so a broken file took a long time to fail. Now it checks the header first and "
    "stops at once. I also added a test with a file that ends halfway through a line, and another with an empty "
    "file. Both pass. The last thing left is the release note, which I can write after lunch if you want it today."
)


def as_float(pcm):
    return np.concatenate([pcm, np.zeros(PAD, np.int16)]).astype(np.float32) / 32768


def timed(model, audio):
    t = time.perf_counter()
    text = model(audio)
    return time.perf_counter() - t, text


def speed(model, free):
    rows = []
    for secs in SPEED_SECS:
        n = SAMPLES if secs <= 60 else 1
        for k in range(n):
            length = min(secs + 0.13 * k, len(free) / RATE - 0.5)
            start = int((len(free) / RATE - length) * k / max(n, 2) * RATE) if n > 1 else 0
            audio = as_float(free[start : start + int(length * RATE)])
            first, text = timed(model, audio)
            again = [timed(model, audio)[0] for _ in range(AGAIN)]
            rows.append({"audio_secs": round(len(audio) / RATE, 2), "first_ms": round(1000 * first),
                         "again_ms": [round(1000 * a) for a in again], "words": len(text.split())})  # fmt: skip
            print(f"  {rows[-1]['audio_secs']:6.1f} s: first {rows[-1]['first_ms']} ms, again {rows[-1]['again_ms']} ms", flush=True)
    return rows


def turns(segs, target):
    """Consecutive utterances grouped until each turn spans target seconds, (first, last)."""
    out, i = [], 0
    while i < len(segs):
        j = i
        while j + 1 < len(segs) and segs[j][1] - segs[i][0] < target:
            j += 1
        out.append((i, j))
        i = j + 1
    return out


def turn_audio(pcm, segs, i, j):
    start, end = segs[i][0], segs[j][1]
    before = segs[i - 1][1] if i > 0 else 0.0
    after = segs[j + 1][0] if j + 1 < len(segs) else len(pcm) / RATE
    a, b = max(start - 1.0, (before + start) / 2, 0.0), min(end + 0.2, (end + after) / 2)
    return as_float(pcm[int(a * RATE) : int(b * RATE)])


def score(text, ref):
    _, errors, words = tally.align(tally.normalize(ref), tally.normalize(text))
    return errors, words


def kinds(per_recording):
    out = {}
    for kind in ("free", "scripted"):
        recs = [r for r in per_recording if ("free" in r) == (kind == "free")]
        out[kind] = {"errors": sum(per_recording[r][0] for r in recs), "words": sum(per_recording[r][1] for r in recs)}
    return out


class Voice(threading.Thread):
    """Qwen3 speaking without a break through audio.cpp's server, started as the node starts it."""

    def __init__(self, out):
        super().__init__(daemon=True)
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        conf = out / "audiocpp.json"
        conf.write_text(json.dumps({"host": "127.0.0.1", "port": port, "backend": "cuda", "device": 0, "threads": 4, "lazy_load": False,
                                    "models": [{"id": "m", "family": "qwen3_tts", "path": str(INSTALL / "models/qwen3-tts-12hz-1.7b-base-q8_0.gguf"),
                                                "task": "tts", "mode": "streaming"}]}))  # fmt: skip
        env = dict(os.environ, LD_LIBRARY_PATH=str(INSTALL / "audiocpp/lib"))
        self.p = subprocess.Popen([str(INSTALL / "audiocpp/audiocpp_server"), "--config", str(conf)], cwd=INSTALL / "audiocpp",
                                  env=env, stdout=open(out / "audiocpp.log", "w"), stderr=subprocess.STDOUT)  # fmt: skip
        self.base = f"http://127.0.0.1:{port}"
        deadline = time.time() + 90
        while True:
            try:
                if urllib.request.urlopen(self.base + "/health", timeout=1).status == 200:
                    break
            except OSError:
                pass
            assert self.p.poll() is None and time.time() < deadline, "audio.cpp's server didn't start"
            time.sleep(0.2)
        self.body = json.dumps({"model": "m", "input": VOICE_TEXT, "stream": True, "stream_format": "sse", "response_format": "pcm",
                                "options": {"language": "en"}, "voice_ref": str(INSTALL / "voices/hadley.wav"),
                                "reference_text": (INSTALL / "voices/hadley.txt").read_text().strip()}).encode()  # fmt: skip
        self.samples, self.takes, self.stop = 0, 0, threading.Event()
        self.speaking = threading.Event()

    def run(self):
        while not self.stop.is_set():
            req = urllib.request.Request(self.base + "/v1/audio/speech", data=self.body,
                                         headers={"Content-Type": "application/json", "Accept": "text/event-stream"})  # fmt: skip
            with urllib.request.urlopen(req) as r:
                for line in r:
                    if self.stop.is_set():
                        break
                    if line.startswith(b"data: [DONE]"):
                        self.takes += 1
                        break
                    if b'"speech.audio.delta"' in line:
                        self.samples += len(base64.b64decode(json.loads(line[5:])["audio"])) // 2
                        self.speaking.set()

    def close(self):
        self.stop.set()
        self.join(timeout=30)
        self.p.kill()
        self.p.wait()


def main():
    a = argparse.ArgumentParser()
    a.add_argument("data", type=Path)
    a.add_argument("runs", type=Path)
    a.add_argument("out", type=Path)
    a.add_argument("--voice", action="store_true")
    args = a.parse_args()
    data, runs, out = args.data.expanduser(), args.runs.expanduser(), args.out.expanduser()
    out.mkdir(parents=True, exist_ok=True)
    started = time.perf_counter()

    opus = Opus()
    recordings = {p.stem: opus.through(read(p))[0] for p in sorted((data / "wav").glob("*.wav"))}
    segments = {r: json.loads((data / "segments" / f"{r}.json").read_text()) for r in recordings}
    refs = {r: tally.reference(data, r) for r in recordings}
    free = recordings[next(r for r in recordings if "free" in r)]

    model = MODELS["parakeet-v2"]()
    warm = as_float(free[RATE : 4 * RATE])
    for _ in range(3):
        model(warm)
    result = {"speed": {}, "words": {}, "turns": {}}

    print("speed, alone", flush=True)
    result["speed"]["alone"] = speed(model, free)
    if args.voice:
        voice = Voice(out)
        voice.start()
        assert voice.speaking.wait(30), "the voice made no audio"
        t, before = time.perf_counter(), voice.samples
        print("speed, with Qwen3 speaking", flush=True)
        result["speed"]["with_voice"] = speed(model, free)
        made = (voice.samples - before) / 24000
        result["voice"] = {"audio_secs": round(made, 1), "over_secs": round(time.perf_counter() - t, 1), "takes": voice.takes}
        voice.close()
        print(f"  the voice made {made:.0f} s of audio meanwhile", flush=True)

    for name in ("call", "opus"):
        rows, clips = tally.load(runs, "parakeet-v2", name), json.loads((data / "clips" / f"{name}.json").read_text())
        result["words"][name] = kinds({r: score(tally.recording_text(rows, clips, r), refs[r]) for r in recordings})
    texts = {}
    for target in TURN_SECS:
        per, texts[target], spans = {}, {}, []
        for r, pcm in recordings.items():
            parts = []
            for i, j in turns(segments[r], target):
                audio = turn_audio(pcm, segments[r], i, j)
                spans.append(len(audio) / RATE)
                parts.append(model(audio).strip())
            texts[target][r] = " ".join(parts)
            per[r] = score(texts[target][r], refs[r])
        result["words"][f"turns-{target}"] = kinds(per)
        result["turns"][target] = {"count": len(spans), "median_secs": round(statistics.median(spans), 1), "longest_secs": round(max(spans), 1)}
    model.close()
    result["wall_secs"] = round(time.perf_counter() - started, 1)
    (out / "turns.json").write_text(json.dumps(result, indent=1))
    (out / "turns-text.json").write_text(json.dumps(texts, indent=1))
    for name, w in result["words"].items():
        print(f"{name:10} free {w['free']['errors']}/{w['free']['words']}  scripted {w['scripted']['errors']}/{w['scripted']['words']}", flush=True)
    print(json.dumps(result["turns"]), flush=True)
    os._exit(0)


if __name__ == "__main__":
    main()
