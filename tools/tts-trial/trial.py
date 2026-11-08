#!/usr/bin/env python3
"""Measures one text-to-speech model on the speech node, the same way for every model.

Run it through gate.sh, which holds the GPU lock and stops it if a call starts:

    harness/gate.sh python3 harness/trial.py MODEL [--sentences all|screen] [--ballast MIB] [--label L]

It starts the model's runtime once, makes three warm-up sentences that aren't counted, then makes
each sentence on its own, as a call does, and writes one WAV per sentence, timing.jsonl and
summary.json to ROOT/runs/MODEL/LABEL/. It uses only Python's standard library, so it needs no
environment of its own.

What it measures, for each sentence:
  first_s    request to first audio: the first chunk for a model that streams, the whole sentence
             for one that doesn't, which is what a caller waits for
  total_s    request to the last audio
  audio_s    seconds of audio made
  ahead_s    how long playback must wait after the request to run with no gap, given when each
             chunk came; for a stream that keeps up it's the same as first_s
and for the run: peak GPU memory (the whole card's, less what it used before starting, so Vulkan
counts too), peak host memory of the runtime, and how long it took to be ready.
"""
import argparse
import base64
import http.client
import json
import mimetypes
import os
import pathlib
import socket
import statistics
import subprocess
import sys
import threading
import time
import uuid
import wave

ROOT = pathlib.Path(os.environ.get("TTS_ROOT", "~/ringback-models-test/tts")).expanduser()
HERE = pathlib.Path(__file__).resolve().parent
LIVE_KOKORO = pathlib.Path("~/ringback-speech/models/kokoro-multi-lang-v1_0").expanduser()

def free_port():
    """A port nothing holds: a server that doesn't reuse addresses can't bind a recent one."""
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


SEED = 42
WARM_UP = [
    "Hello, the voice is ready.",
    "This sentence warms up the model and isn't counted.",
    "Nothing here is measured yet.",
]
# What a model with no built-in voice says to make itself a reference: see refs.py.
REFERENCE = ("I've gone through the changes you asked for, and everything looks good so far. There are "
             "a couple of small things I'd like to check with you, but nothing that can't wait until "
             "you have a moment.")
# A few of each kind, to see what a model can do before giving it a whole run.
SCREEN = ["short-01", "short-03", "yes_no-01", "wh-01", "number-01", "path-01", "run-01",
          "run-03", "run-07", "run-10", "run-12", "run-22"]
BALLAST_SET = SCREEN


def audiocpp(family, path, rate, **more):
    return {"runtime": "audiocpp", "family": family, "path": path, "rate": rate, **more}


# Which runtime, weights, voice and settings each model is tried with. `rate` is the sample rate of
# the audio it hands back. `ref` names a reference clip made by refs.py, from the model itself.
MODELS = {
    "kokoro": {"runtime": "kokoro", "rate": 24000},
    # Other Kokoro voices: the control for score.py, to show what a different speaker scores.
    "kokoro-bella": {"runtime": "kokoro", "rate": 24000, "sid": 2},
    "kokoro-sarah": {"runtime": "kokoro", "rate": 24000, "sid": 9},
    "kokoro-nicole": {"runtime": "kokoro", "rate": 24000, "sid": 6},
    "kokoro-alloy": {"runtime": "kokoro", "rate": 24000, "sid": 0},
    "ming": audiocpp("ming3_tts", "ming/ming-3.0-tts-0.5b-q8_0.gguf", 44100,
                     options={"template_name": "ip", "voice_id": "US_Emma", "language": "en"}),
    # Ming with one fixed clip of its own as the prompt for everything it says: see context_make.py.
    "ming-anchored": audiocpp("ming3_tts", "ming/ming-3.0-tts-0.5b-q8_0.gguf", 44100,
                              options={"template_name": "ip", "voice_id": "US_Emma", "language": "en"},
                              ref="ming"),
    # The same clip for its speaker embedding alone, with no audio prompt.
    "ming-speaker": audiocpp("ming3_tts", "ming/ming-3.0-tts-0.5b-q8_0.gguf", 44100,
                             options={"template_name": "ip", "voice_id": "US_Emma", "language": "en"},
                             ref="ming", ref_audio_only=True),
    # The built-in voice as Ming's README calls it: use_zero_spk_emb=True keeps the speaker row, empty.
    "ming-zero": audiocpp("ming3_tts", "ming/ming-3.0-tts-0.5b-q8_0.gguf", 44100,
                          options={"template_name": "ip", "voice_id": "US_Emma", "language": "en",
                                   "zero_speaker_embedding": True}),
    # The same with text cut about where Ming's own default of 24 words cuts it, not at 300 characters.
    "ming-zero-short": audiocpp("ming3_tts", "ming/ming-3.0-tts-0.5b-q8_0.gguf", 44100,
                                options={"template_name": "ip", "voice_id": "US_Emma", "language": "en",
                                         "zero_speaker_embedding": True, "text_chunk_size": 130}),
    # The empty row, with the text in one piece: no chunks, so no joins, up to Ming's 512 positions.
    "ming-raw": audiocpp("ming3_tts", "ming/ming-3.0-tts-0.5b-q8_0.gguf", 44100,
                         options={"template_name": "ip", "voice_id": "US_Emma", "language": "en",
                                  "zero_speaker_embedding": True, "raw_text": True}),
    "ming-bf16": audiocpp("ming3_tts", "ming/ming-3.0-tts-0.5b-bf16.gguf", 44100,
                          options={"template_name": "ip", "voice_id": "US_Emma", "language": "en"}),
    "audio8": audiocpp("audio8_tts", "audio8/audio8-tts-realistic-0.6b-q8_0.gguf", 44100,
                       options={"language": "en"}, ref="audio8"),
    "audio8-noref": audiocpp("audio8_tts", "audio8/audio8-tts-realistic-0.6b-q8_0.gguf", 44100,
                             options={"language": "en"}),
    "lfm3": audiocpp("lfm3_audio", "lfm3/LFM3-Audio-2B-Q8_0.gguf", 24000,
                     voice="american_english_female"),
    "qwen3": audiocpp("qwen3_tts", "qwen3/qwen3-tts-12hz-1.7b-customvoice-q8_0.gguf", 24000,
                      options={"voice_id": "Serena", "language": "en"}),
    "qwen3-aiden": audiocpp("qwen3_tts", "qwen3/qwen3-tts-12hz-1.7b-customvoice-q8_0.gguf", 24000,
                            options={"voice_id": "Aiden", "language": "en"}),
    # Qwen3's voice-clone model, with Phonon's Hadley as the voice: the one the user liked on a long stretch.
    "qwen3-hadley": audiocpp("qwen3_tts", "qwen3/qwen3-tts-12hz-1.7b-base-q8_0.gguf", 24000,
                             options={"language": "en"}, ref="hadley"),
    "voxcpm2": audiocpp("voxcpm2", "voxcpm2/voxcpm2-q8_0.gguf", 48000,
                        options={"retry_badcase": False}, ref="voxcpm2"),
    "voxcpm2-design": audiocpp("voxcpm2", "voxcpm2/voxcpm2-q8_0.gguf", 48000,
                               options={"retry_badcase": False},
                               prefix="(A warm, clear young woman, calm and friendly)"),
    "kugel": audiocpp("kugelaudio", "kugel/kugelaudio-0-open-q4_k.gguf", 24000,
                      options={"voice_id": "english_female"}),
    "breeze-audiocpp": audiocpp("breeze_tts", "breeze/audiocpp/breeze-tts-2-q8_0.gguf", 24000,
                                options={"instruct": "A warm, clear young woman, calm and friendly."}),
    "breeze": {"runtime": "breeze", "path": "breeze/port/breeze-tts-2-q8_0.gguf", "rate": 24000,
               "voice_id": "trial"},
    "breeze-q4": {"runtime": "breeze", "path": "breeze/port/breeze-tts-2-q4_k.gguf", "rate": 24000,
                  "voice_id": "trial"},
    "breeze-port-q8": {"runtime": "breeze", "path": "breeze/port/breeze-tts-2-q8_0.gguf", "rate": 24000,
                       "instruction": "A warm, clear young woman, calm and friendly."},
    "breeze-port-q4": {"runtime": "breeze", "path": "breeze/port/breeze-tts-2-q4_k.gguf", "rate": 24000,
                       "instruction": "A warm, clear young woman, calm and friendly."},
    "phonon": {"runtime": "phonon", "rate": 24000, "voice": "Hadley", "threads": 2},
    "phonon-4t": {"runtime": "phonon", "rate": 24000, "voice": "Hadley", "threads": 4},
}


class Monitor(threading.Thread):
    """Samples the card's memory and the runtime's host memory until told to stop."""

    def __init__(self):
        super().__init__(daemon=True)
        self.base = gpu_used()
        self.pid = None
        self.gpu_peak = self.base
        self.rss_peak = 0
        self.done = threading.Event()

    def run(self):
        while not self.done.is_set():
            self.gpu_peak = max(self.gpu_peak, gpu_used())
            self.rss_peak = max(self.rss_peak, peak_rss(self.pid))
            self.done.wait(0.2)

    def stop(self):
        self.done.set()
        self.join()


def gpu_used():
    out = subprocess.run(["nvidia-smi", "--query-gpu=memory.used", "--format=csv,noheader,nounits"],
                         capture_output=True, text=True).stdout
    return int(out.split()[0])


def peak_rss(pid):
    """VmHWM of a process, in MiB: its own record of its peak, so nothing is missed between samples."""
    try:
        for line in open(f"/proc/{pid}/status"):
            if line.startswith("VmHWM:"):
                return int(line.split()[1]) // 1024
    except (OSError, TypeError):
        pass
    return 0


def cpu_seconds(pid):
    """User and system time of a process so far, children that have ended included."""
    try:
        fields = open(f"/proc/{pid}/stat").read().rsplit(")", 1)[1].split()
        return sum(int(fields[i]) for i in (11, 12, 13, 14)) / os.sysconf("SC_CLK_TCK")
    except (OSError, IndexError):
        return 0.0


def write_wav(path, pcm, rate):
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(pcm)


def ahead(chunks, rate, t0):
    """The shortest wait after the request after which playback never runs out: chunks is a list
    of (arrival time, bytes of 16-bit audio)."""
    need, played = 0.0, 0.0
    for at, n in chunks:
        need = max(need, at - t0 - played)
        played += n / 2 / rate
    return need


class AudioCpp:
    """audio.cpp's server, one streaming model, read as server-sent events."""

    def __init__(self, cfg, log):
        self.cfg, self.rate = cfg, cfg["rate"]
        entry = {"id": "m", "family": cfg["family"], "path": str(ROOT / "models" / cfg["path"]),
                 "task": "tts", "mode": "streaming"}
        entry.update(cfg.get("entry", {}))
        self.port = free_port()
        conf = {"host": "127.0.0.1", "port": self.port, "backend": "cuda", "device": 0,
                "threads": cfg.get("threads", 4), "lazy_load": False, "models": [entry]}
        path = log.with_name("server.json")
        path.write_text(json.dumps(conf, indent=1))
        bin_dir = ROOT / "runtime/audiocpp/bin"
        env = dict(os.environ, LD_LIBRARY_PATH=str(ROOT / "runtime/audiocpp/cudart"))
        self.proc = subprocess.Popen([str(bin_dir / "audiocpp_server"), "--config", str(path), "--log"],
                                     env=env, cwd=bin_dir, stdout=log.open("w"), stderr=subprocess.STDOUT)
        self.pid = self.proc.pid
        self.seed = SEED
        self.body = {"model": "m", "stream": True, "stream_format": "sse", "response_format": "pcm",
                     "options": dict(cfg.get("options", {}))}
        if cfg.get("voice"):
            self.body["voice"] = cfg["voice"]
        if cfg.get("ref"):
            ref = ROOT / "refs" / cfg["ref"]
            self.body["voice_ref"] = str(ref.with_suffix(".wav"))
            if not cfg.get("ref_audio_only"):
                self.body["reference_text"] = ref.with_suffix(".txt").read_text().strip()

    def ready(self):
        wait_http(self.proc, "/health", self.port)

    def speak(self, text, extra=None):
        """`extra` adds to the request, and its `options` to the options, for one sentence."""
        body = {**self.body, "input": self.cfg.get("prefix", "") + text, "seed": self.seed}
        if extra:
            options = {**body["options"], **extra.get("options", {})}
            body = {**body, **extra, "options": options}
        c = http.client.HTTPConnection("127.0.0.1", self.port, timeout=300)
        t0 = time.time()
        c.request("POST", "/v1/audio/speech", json.dumps(body),
                  {"Content-Type": "application/json", "Accept": "text/event-stream"})
        r = c.getresponse()
        if r.status != 200:
            raise RuntimeError(f"HTTP {r.status}: {r.read()[:300]}")
        pcm, chunks = bytearray(), []
        for line in r:
            if not line.startswith(b"data:"):
                continue
            data = line[5:].strip()
            if data == b"[DONE]":
                break
            event = json.loads(data)
            if event.get("type") == "speech.audio.delta":
                b = base64.b64decode(event["audio"])
                chunks.append((time.time(), len(b)))
                pcm += b
            elif event.get("type") != "speech.audio.done":
                raise RuntimeError(json.dumps(event)[:300])
        return t0, chunks, bytes(pcm)

    def close(self):
        self.proc.terminate()
        self.proc.wait(20)


class Breeze:
    """The Breeze port's own server, which streams raw 24 kHz PCM."""

    def __init__(self, cfg, log):
        self.cfg, self.rate = cfg, cfg["rate"]
        d = next((ROOT / "runtime/breeze").glob("breeze-tts-2-*"))
        env = dict(os.environ, LD_LIBRARY_PATH=str(d))
        self.port = free_port()
        self.proc = subprocess.Popen(
            [str(d / "breeze-server"), str(ROOT / "models" / cfg["path"]), "--host", "127.0.0.1",
             "--port", str(self.port), "--ws-port", "-1", "--voices-dir", str(ROOT / "refs/breeze-voices"),
             *cfg.get("args", [])],
            env=env, cwd=d, stdout=log.open("w"), stderr=subprocess.STDOUT)
        self.pid = self.proc.pid
        self.seed = SEED

    def ready(self):
        wait_http(self.proc, "/health", self.port)

    def speak(self, text):
        fields = {"text": text, "seed": str(self.seed)}
        if self.cfg.get("voice_id"):
            fields["voice_id"] = self.cfg["voice_id"]
        else:
            fields["instruction"] = self.cfg["instruction"]
        boundary = uuid.uuid4().hex
        body = "".join(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'
                       for k, v in fields.items()) + f"--{boundary}--\r\n"
        c = http.client.HTTPConnection("127.0.0.1", self.port, timeout=300)
        t0 = time.time()
        c.request("POST", "/v1/audio/speech", body.encode(),
                  {"Content-Type": f"multipart/form-data; boundary={boundary}"})
        r = c.getresponse()
        if r.status != 200:
            raise RuntimeError(f"HTTP {r.status}: {r.read()[:300]}")
        pcm, chunks = bytearray(), []
        while True:
            b = r.read1(65536)
            if not b:
                break
            chunks.append((time.time(), len(b)))
            pcm += b
        return t0, chunks, bytes(pcm)

    def close(self):
        self.proc.terminate()
        self.proc.wait(20)


def wait_http(proc, path, port):
    for _ in range(2400):
        if proc.poll() is not None:
            raise RuntimeError(f"runtime exited with {proc.returncode}")
        try:
            c = http.client.HTTPConnection("127.0.0.1", port, timeout=2)
            c.request("GET", path)
            r = c.getresponse()
            r.read()
            if r.status == 200:
                return
        except OSError:
            pass
        time.sleep(0.05)
    raise RuntimeError("runtime didn't come up")


class LineChild:
    """A child that takes a JSON line per request and answers with one: Kokoro and Phonon."""

    def __init__(self, cmd, env, rate, log):
        self.rate = rate
        self.proc = subprocess.Popen(cmd, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=log.open("w"), text=True)
        self.pid = self.proc.pid

    def ready(self):
        line = self.proc.stdout.readline()
        if not json.loads(line or "{}").get("ready"):
            raise RuntimeError(f"child didn't load: {line!r}")

    def close(self):
        self.proc.stdin.close()
        self.proc.wait(20)


class Kokoro(LineChild):
    """Kokoro as a call makes it: kokoro_trial.go.txt, on ringback's own front end."""

    def __init__(self, cfg, log):
        d = ROOT / "runtime/kokoro"
        env = dict(os.environ, LD_LIBRARY_PATH=f"{d}/sherpa-onnx/lib:{d}/lib")
        super().__init__([str(d / "bin/kokoro-trial"), str(LIVE_KOKORO), str(d / "espeak-ng"),
                          str(cfg.get("sid", 3))], env, cfg["rate"], log)
        self.tmp = log.with_name("kokoro-tmp.wav")

    def speak(self, text, extra=None):
        t0 = time.time()
        self.proc.stdin.write(json.dumps({"id": "x", "text": text, "out": str(self.tmp)}) + "\n")
        self.proc.stdin.flush()
        r = json.loads(self.proc.stdout.readline())
        end = t0 + r["total_s"]
        with wave.open(str(self.tmp)) as w:
            pcm = w.readframes(w.getnframes())
        # The first chunk is whole when it comes, and one sentence is one chunk.
        first = t0 + r["first_s"]
        return t0, [(first, len(pcm))] if r["chunks"] == 1 else [(first, 0), (end, len(pcm))], pcm


class Phonon(LineChild):
    """Phonon on the CPU, through its own Python package, streaming."""

    def __init__(self, cfg, log):
        env = dict(os.environ, HF_HOME=str(ROOT / "cache/hf"))
        super().__init__([str(ROOT / "env/bin/python"), "-I", str(HERE / "phonon_trial.py"),
                          cfg["voice"], str(cfg["threads"])], env, cfg["rate"], log)

    def speak(self, text, extra=None):
        t0 = time.time()
        self.proc.stdin.write(json.dumps({"text": text}) + "\n")
        self.proc.stdin.flush()
        r = json.loads(self.proc.stdout.readline())
        pcm = base64.b64decode(r["pcm"])
        return t0, [(t0 + at, n) for at, n in r["chunks"]], pcm


RUNTIMES = {"audiocpp": AudioCpp, "breeze": Breeze, "kokoro": Kokoro, "phonon": Phonon}


def measure(speaker, text, rate):
    t0, chunks, pcm = speaker.speak(text)
    audio = len(pcm) / 2 / rate
    return {"first_s": chunks[0][0] - t0 if chunks else None,
            "total_s": chunks[-1][0] - t0 if chunks else None,
            "audio_s": audio, "ahead_s": ahead(chunks, rate, t0), "chunks": len(chunks)}, pcm


def spread(values):
    values = sorted(values)
    return {"median": statistics.median(values), "worst": values[-1], "best": values[0]}


def summarise(rows):
    made = [r for r in rows if r["audio_s"] > 0]
    long_enough = [r for r in made if r["audio_s"] >= 1.5]
    return {
        "sentences": len(rows),
        "empty": len(rows) - len(made),
        "audio_s": sum(r["audio_s"] for r in made),
        "wall_s": sum(r["total_s"] for r in made),
        "speed_x": sum(r["audio_s"] for r in made) / sum(r["total_s"] for r in made),
        # Over sentences of 1.5 s or more: a word or two says little about speed.
        "speed_per_sentence_x": {
            "median": statistics.median(r["audio_s"] / r["total_s"] for r in long_enough),
            "worst": min(r["audio_s"] / r["total_s"] for r in long_enough)} if long_enough else None,
        "first_audio_s": spread(r["first_s"] for r in made),
        "gap_free_start_s": spread(r["ahead_s"] for r in made),
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("model", choices=MODELS)
    ap.add_argument("--sentences", default="all", choices=["all", "screen", "ballast", "run"])
    ap.add_argument("--ballast", type=int, default=0, help="MiB of GPU memory to hold during the run")
    ap.add_argument("--label", default=None)
    ap.add_argument("--set", default=None, help="JSON overrides for the model's settings")
    ap.add_argument("--refs", type=int, default=0,
                    help="instead of a trial, make this many reference candidates, one seed each")
    args = ap.parse_args()
    cfg = dict(MODELS[args.model])
    if args.set:
        cfg.update(json.loads(args.set))
    out = ROOT / "runs" / args.model / (args.label or args.sentences)
    out.mkdir(parents=True, exist_ok=True)

    sentences = json.loads((HERE / "sentences.json").read_text())
    if args.sentences == "run":
        sentences = [s for s in sentences if s["kind"] == "run"]
    elif args.sentences != "all":
        pick = SCREEN if args.sentences == "screen" else BALLAST_SET
        sentences = [s for s in sentences if s["id"] in pick]
    jobs = [(s["id"], s["text"]) for s in sentences]
    if args.sentences == "all":
        jobs += [(s["id"] + "-spoken", s["spoken"]) for s in sentences if "spoken" in s]

    ballast = None
    before = gpu_used()
    if args.ballast:
        ballast = subprocess.Popen([sys.executable, str(HERE / "ballast.py"), str(args.ballast)],
                                   stdout=subprocess.PIPE, text=True)
        ballast.stdout.readline()
    monitor = Monitor()
    monitor.start()
    start = time.time()
    log = out / "runtime.log"
    speaker = RUNTIMES[cfg["runtime"]](cfg, log)
    monitor.pid = speaker.pid
    result = {"model": args.model, "settings": cfg, "gpu_idle_mib": before,
              "gpu_before_load_mib": monitor.base, "ballast_mib": args.ballast}
    try:
        speaker.ready()
        result["ready_s"] = time.time() - start
        if args.refs:
            out = ROOT / "refs/candidates" / args.model
            out.mkdir(parents=True, exist_ok=True)
            for seed in range(1, args.refs + 1):
                speaker.seed = seed
                _, pcm = measure(speaker, REFERENCE, speaker.rate)
                write_wav(out / f"seed-{seed}.wav", pcm, speaker.rate)
            (out / "text.txt").write_text(REFERENCE + "\n")
            return
        warm = []
        for text in WARM_UP:
            row, _ = measure(speaker, text, speaker.rate)
            warm.append(row)
        result["warm_up"] = warm
        result["loaded_mib"] = gpu_used() - monitor.base
        rows = []
        cpu_start = cpu_seconds(speaker.pid)
        with (out / "timing.jsonl").open("w") as f:
            for sid, text in jobs:
                row, pcm = measure(speaker, text, speaker.rate)
                row.update(id=sid, text=text)
                rows.append(row)
                write_wav(out / f"{sid}.wav", pcm, speaker.rate)
                f.write(json.dumps(row) + "\n")
                f.flush()
        result["cpu_s"] = cpu_seconds(speaker.pid) - cpu_start
        result.update(summarise(rows))
        result["cores_used"] = result["cpu_s"] / result["wall_s"]
    except Exception as e:
        result["error"] = f"{type(e).__name__}: {e}"
    finally:
        try:
            speaker.close()
        except Exception as e:
            speaker.proc.kill()
            result["close_error"] = str(e)
        monitor.stop()
        if ballast:
            ballast.terminate()
    result["gpu_peak_total_mib"] = monitor.gpu_peak
    result["gpu_peak_model_mib"] = monitor.gpu_peak - monitor.base
    result["host_peak_mib"] = monitor.rss_peak
    (out / "summary.json").write_text(json.dumps(result, indent=1))
    show = {k: result.get(k) for k in ("model", "error", "ready_s", "speed_x", "speed_per_sentence_x",
                                       "first_audio_s", "gap_free_start_s", "gpu_peak_model_mib",
                                       "host_peak_mib", "cores_used", "sentences", "empty")}
    print(json.dumps(show, default=lambda x: round(x, 3)))


if __name__ == "__main__":
    main()
