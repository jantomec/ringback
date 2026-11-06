#!/usr/bin/env python3
"""One model over the clips of prepare.py, one stretch at a time as a call does, under gpu.sh.

    gpu.sh ENV_PYTHON run_model.py MODEL DATA_DIR OUT_DIR [--sets a,b] [--ballast MIB]

Writes OUT_DIR/<set>.json as it goes and OUT_DIR/meta.json at the end: load time, peak GPU and
host memory. --ballast holds that much GPU memory in another process for the whole run, for
the test that the model fits beside something else.
"""

import argparse
import json
import os
import resource
import subprocess
import sys
import threading
import time

START = time.perf_counter()

from pathlib import Path  # noqa: E402

import numpy as np  # noqa: E402
import pynvml  # noqa: E402
import soundfile as sf  # noqa: E402

from backends import MODELS, STT  # noqa: E402

WARM_UPS = 3  # untimed transcriptions before timing starts


class Memory(threading.Thread):
    """Samples this process's and its helpers' GPU memory, and the whole GPU's, five times a second."""

    def __init__(self):
        super().__init__(daemon=True)
        pynvml.nvmlInit()
        self.gpu = pynvml.nvmlDeviceGetHandleByIndex(0)
        self.total = pynvml.nvmlDeviceGetMemoryInfo(self.gpu).total >> 20
        self.base = pynvml.nvmlDeviceGetMemoryInfo(self.gpu).used >> 20
        self.pids = {os.getpid()}
        self.peak_process = self.peak_device = 0
        self.done = threading.Event()

    def run(self):
        while not self.done.wait(0.2):
            procs = pynvml.nvmlDeviceGetComputeRunningProcesses(self.gpu)
            mine = sum((p.usedGpuMemory or 0) for p in procs if p.pid in self.pids) >> 20
            self.peak_process = max(self.peak_process, mine)
            self.peak_device = max(self.peak_device, pynvml.nvmlDeviceGetMemoryInfo(self.gpu).used >> 20)


def ballast(mib):
    p = subprocess.Popen([str(STT / "envs/qwen3/bin/python"), str(Path(__file__).with_name("ballast.py")), str(mib)],
                         stdout=subprocess.PIPE, text=True)  # fmt: skip
    assert p.stdout.readline().strip() == "holding", "the ballast didn't start"
    return p


def main():
    a = argparse.ArgumentParser()
    a.add_argument("model", choices=MODELS)
    a.add_argument("data", type=Path)
    a.add_argument("out", type=Path)
    a.add_argument("--sets", default="clean,opus,call,short,noise")
    a.add_argument("--ballast", type=int, default=0)
    args = a.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)

    mem = Memory()
    mem.start()
    held = ballast(args.ballast) if args.ballast else None
    t = time.perf_counter()
    model = MODELS[args.model]()
    load_secs = time.perf_counter() - t
    mem.pids |= set(getattr(model, "pids", []))

    def read(file):
        return sf.read(args.data / "clips" / file, dtype="float32")[0]

    sets = {s: json.loads((args.data / "clips" / f"{s}.json").read_text()) for s in args.sets.split(",")}
    first = next(iter(sets.values()))
    warm = read(max(first[:20], key=lambda c: c["secs"])["file"])
    t = time.perf_counter()
    model(warm)
    first_secs = time.perf_counter() - t
    ready_secs = time.perf_counter() - START
    for _ in range(WARM_UPS - 1):
        model(warm)

    for name, clips in sets.items():
        rows = []
        for c in clips:
            audio = read(c["file"])
            t = time.perf_counter()
            text = model(audio).strip()
            row = {"id": c["id"], "text": text, "secs": round(time.perf_counter() - t, 4), "audio_secs": c["secs"]}
            if not text and c.get("retry_from") is not None:  # as stream.go's Transcribed
                t = time.perf_counter()
                row["retry_text"] = model(audio[c["retry_from"] :]).strip()
                row["retry_secs"] = round(time.perf_counter() - t, 4)
            rows.append(row)
        (args.out / f"{name}.json").write_text(json.dumps(rows, indent=0))
        secs = sorted(r["secs"] for r in rows)
        print(f"{args.model} {name}: {len(rows)} clips, median {1000 * secs[len(secs) // 2]:.0f} ms, worst {1000 * secs[-1]:.0f} ms", flush=True)

    if hasattr(model, "close"):
        model.close()
    if held:
        held.terminate()
    mem.done.set()
    mem.join()
    rss = max(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss, resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss)
    meta = {
        "model": args.model,
        "repo": getattr(model, "repo", ""),
        "load_secs": round(load_secs, 2),  # imports and loading
        "first_secs": round(first_secs, 3),  # the first transcription
        "ready_secs": round(ready_secs, 2),  # from the process starting to the first words
        "peak_gpu_mib": mem.peak_process,
        "peak_device_mib": mem.peak_device,
        "device_before_mib": mem.base,
        "device_total_mib": mem.total,
        "peak_host_mib": rss >> 10,
        "ballast_mib": args.ballast,
        "wall_secs": round(time.perf_counter() - START, 1),
        "sets": list(sets),
        "python": sys.version.split()[0],
    }
    (args.out / "meta.json").write_text(json.dumps(meta, indent=1))
    print(json.dumps(meta), flush=True)
    os._exit(0)  # some models' threads don't stop by themselves


if __name__ == "__main__":
    main()
