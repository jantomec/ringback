#!/usr/bin/env python3
"""Generations at the same time, and a voice beside speech recognition that's actually working.

    harness/gate.sh python3 harness/overlap.py workers MODEL N
    harness/gate.sh python3 harness/overlap.py cut MODEL N
    harness/gate.sh python3 harness/overlap.py parakeet ming
    harness/gate.sh python3 harness/overlap.py replay ming N SECONDS STRATEGY [--parakeet]

workers: N generations started together on the same five sentences, each its own process, as a
runtime only runs one job at a time. Three rounds. Gives each worker's speed and first audio.

cut: the user's overlap for real. Worker 1 makes sentence 1 alone, and worker 2 (and 3) sentences 1 to
X (Y) at the same moment. Measures when the first audio past the end of sentence 1 comes, against
when sentence 1 runs out, for an ordinary first sentence and very short ones.

parakeet: Ming's speed alone, then with Parakeet on the same GPU transcribing a 6 s stretch all the
time, and then once a second, as near a call as this gets, with Parakeet's own times alongside.

replay: a reply of N sentences played for real. Its text arrives evenly over SECONDS, sentence 1 goes
as soon as it's complete, and playback is worked out from when each chunk actually came. STRATEGY
`today` is Ming as trial.py has it, handing over the next piece one second before the one playing
ends, as the hub does. `anchor` is the same on `ming-anchored`, with its fixed prompt throughout.

Writes ROOT/runs/overlap/*.json.
"""
import argparse
import json
import os
import pathlib
import statistics
import subprocess
import sys
import threading
import time

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import trial  # noqa: E402
from context_make import RUN, timed  # noqa: E402

ROOT = trial.ROOT
OUT = ROOT / "runs/overlap"
PARAKEET = pathlib.Path("~/ringback-speech/models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16").expanduser()
SHORT_FIRST = ["Okay.", "Sure, go ahead.", RUN[6], RUN[0], RUN[2]]


def start(model, n):
    workers = []
    for i in range(n):
        cfg = dict(trial.MODELS[model])
        w = trial.RUNTIMES[cfg["runtime"]](cfg, OUT / f"{model}-{i}.log")
        w.ready()
        workers.append(w)
    for w in workers:
        for text in trial.WARM_UP:
            timed(w, text)
    return workers


def together(jobs):
    """Runs (speaker, text) jobs at the same moment. Returns each one's chunk log and its start."""
    results = [None] * len(jobs)
    gate = threading.Barrier(len(jobs))

    def go(i, speaker, text):
        gate.wait()
        results[i] = speaker.speak(text)

    threads = [threading.Thread(target=go, args=(i, *j)) for i, j in enumerate(jobs)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    return results


def workers_test(model, n):
    monitor = trial.Monitor()  # before they load, so its peak holds their weights too
    monitor.start()
    workers = start(model, n)
    text = " ".join(RUN[:5])
    rounds = []
    try:
        for _ in range(3):
            row = []
            for (t0, chunks, pcm), w in zip(together([(w, text) for w in workers]), workers):
                audio = len(pcm) / 2 / w.rate
                row.append({"first_s": chunks[0][0] - t0, "total_s": chunks[-1][0] - t0, "audio_s": audio,
                            "speed_x": audio / (chunks[-1][0] - t0)})
            rounds.append(row)
    finally:
        for w in workers:
            w.close()
        monitor.stop()
    all_rows = [r for row in rounds for r in row]
    out = {"model": model, "workers": n, "rounds": rounds,
           "speed_x_median": statistics.median(r["speed_x"] for r in all_rows),
           "speed_x_worst": min(r["speed_x"] for r in all_rows),
           "first_s_median": statistics.median(r["first_s"] for r in all_rows),
           "first_s_worst": max(r["first_s"] for r in all_rows),
           "gpu_peak_mib": monitor.gpu_peak - monitor.base}
    (OUT / f"workers-{model}-{n}.json").write_text(json.dumps(out, indent=0))
    print(json.dumps({k: v for k, v in out.items() if k != "rounds"}, default=lambda v: round(v, 3)))


def past(chunks, t0, seconds, rate):
    """When the first audio past `seconds` into a stream came, after t0."""
    made = 0.0
    for at, n in chunks:
        made += n / 2 / rate
        if made > seconds:
            return at - t0
    return None


def cut_test(model, n):
    workers = start(model, n)
    rows = []
    try:
        for first in SHORT_FIRST:
            rest = [s for s in RUN if s != first]
            passes = [first, " ".join([first] + rest[:3]), " ".join([first] + rest[:5])][:n]
            for _ in range(3):
                results = together(list(zip(workers, passes)))
                t0, chunks, pcm = results[0]
                length = len(pcm) / 2 / workers[0].rate
                began = chunks[0][0] - t0
                row = {"first": first, "sentence_s": length, "first_audio_s": began, "runs_out_s": began + length}
                for i in range(1, n):
                    ti, ci, _ = results[i]
                    # Its sentence 1 is a little longer or shorter than the one that played.
                    row[f"worker{i + 1}_past_cut_s"] = past(ci, ti, length, workers[i].rate)
                    row[f"worker{i + 1}_total_s"] = ci[-1][0] - ti
                    row[f"worker{i + 1}_audio_s"] = sum(c[1] for c in ci) / 2 / workers[i].rate
                    row[f"worker{i + 1}_margin_s"] = row["runs_out_s"] - row[f"worker{i + 1}_past_cut_s"]
                rows.append(row)
    finally:
        for w in workers:
            w.close()
    (OUT / f"cut-{model}-{n}.json").write_text(json.dumps(rows, indent=0))
    for first in SHORT_FIRST:
        some = [r for r in rows if r["first"] == first]
        print(first[:24], f"{some[0]['sentence_s']:.1f} s", {
            k: round(statistics.median(r[k] for r in some), 2) for k in some[0]
            if k.endswith(("margin_s", "past_cut_s", "total_s")) or k == "first_audio_s"}, flush=True)


def ming_pass(speaker, label):
    """Five sentences as a piece, and a run of single sentences."""
    rows = []
    text = " ".join(RUN[:5])
    for _ in range(3):
        row, _ = timed(speaker, text)
        rows.append({"kind": "piece", **row})
    for s in RUN[:8]:
        row, _ = timed(speaker, s)
        rows.append({"kind": "sentence", **row})
    def speed(kind):
        some = [r for r in rows if r["kind"] == kind]
        return sum(r["audio_s"] for r in some) / sum(r["total_s"] for r in some)
    return {"label": label, "piece_speed_x": speed("piece"), "sentence_speed_x": speed("sentence"),
            "first_s_median": statistics.median(r["first_s"] for r in rows),
            "first_s_worst": max(r["first_s"] for r in rows), "slowest_x": min(r["audio_s"] / r["total_s"] for r in rows)}


def parakeet_test():
    d = ROOT / "runtime/kokoro"
    env = dict(os.environ, LD_LIBRARY_PATH=f"{d}/sherpa-onnx/lib:{d}/lib")
    clip = OUT / "stretch.wav"
    with __import__("wave").open(str(ROOT / "runs/kokoro/all/run-03.wav")) as w:
        trial.write_wav(clip, w.readframes(w.getnframes()), w.getframerate())
    speaker = start("ming", 1)[0]
    out = {"alone": ming_pass(speaker, "alone")}
    try:
        for label, gap in (("parakeet_always", "0"), ("parakeet_each_second", "1")):
            p = subprocess.Popen([str(d / "bin/parakeet-load"), str(PARAKEET), str(clip), gap], env=env,
                                 stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
            times = []
            ready = json.loads(p.stdout.readline())

            def read():
                for line in p.stdout:
                    times.append(json.loads(line)["took_s"])

            threading.Thread(target=read, daemon=True).start()
            time.sleep(1)
            alone_times = list(times)
            out[label] = ming_pass(speaker, label)
            p.terminate()
            p.wait(10)
            busy = times[len(alone_times):]
            out[label]["parakeet_stretch_s"] = ready["seconds"]
            out[label]["parakeet_before_ming_s"] = statistics.median(alone_times) if alone_times else None
            out[label]["parakeet_median_s"] = statistics.median(busy)
            out[label]["parakeet_worst_s"] = max(busy)
            out[label]["parakeet_runs"] = len(busy)
        out["alone_again"] = ming_pass(speaker, "alone again")
    finally:
        speaker.close()
    (OUT / "parakeet-ming.json").write_text(json.dumps(out, indent=1))
    for v in out.values():
        print(json.dumps(v, default=lambda x: round(x, 3)))


PIECE_CHARS = 250  # about what a pass can hold with its prompt
LEAD = 1.0  # hub/call.go: how long before a piece ends the next is handed over


def parakeet_process():
    d = ROOT / "runtime/kokoro"
    env = dict(os.environ, LD_LIBRARY_PATH=f"{d}/sherpa-onnx/lib:{d}/lib")
    clip = OUT / "stretch.wav"
    p = subprocess.Popen([str(d / "bin/parakeet-load"), str(PARAKEET), str(clip), "0"], env=env,
                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return p


def replay(n, seconds, strategy, with_parakeet):
    sentences = RUN[:n]
    speaker = start("ming-anchored" if strategy == "anchor" else "ming", 1)[0]
    load = parakeet_process() if with_parakeet else None
    if load:
        time.sleep(4)
    rate = speaker.rate
    total = sum(len(s) for s in sentences)
    arrive, chars = [], 0
    for s in sentences:
        chars += len(s)
        arrive.append(seconds * chars / total)
    t0 = time.time()
    played_to = None  # when playback of what's made so far ends
    first_audio = None
    dry, pieces, i = 0.0, [], 0
    try:
        while i < n:
            now = time.time() - t0
            wait = arrive[i] - now
            if wait > 0:
                time.sleep(wait)
            if played_to is not None:
                wait = played_to - LEAD - (time.time() - t0)
                if wait > 0:
                    time.sleep(wait)
            now = time.time() - t0
            if i == 0:
                said = [0]
            else:
                said = [i]
                while said[-1] + 1 < n and arrive[said[-1] + 1] <= now and \
                        sum(len(sentences[j]) for j in said) + len(sentences[said[-1] + 1]) < PIECE_CHARS:
                    said.append(said[-1] + 1)
            text = " ".join(sentences[j] for j in said)
            start_at = time.time() - t0
            s0, chunks, pcm = speaker.speak(text)
            times = [(at - t0, b / 2 / rate) for at, b in chunks]
            if first_audio is None:
                first_audio = times[0][0]
            clock = max(played_to if played_to is not None else times[0][0], times[0][0])
            if played_to is not None:
                dry += max(times[0][0] - played_to, 0)
            for at, secs in times:
                if at > clock:
                    dry += at - clock
                    clock = at
                clock += secs
            played_to = clock
            pieces.append({"said": said, "asked_s": start_at, "first_s": times[0][0], "made_s": times[-1][0],
                           "audio_s": sum(t[1] for t in times), "ends_s": played_to})
            i = said[-1] + 1
    finally:
        speaker.close()
        if load:
            load.terminate()
            load.wait(10)
    row = {"sentences": n, "text_over_s": seconds, "strategy": strategy, "parakeet": with_parakeet,
           "first_audio_s": first_audio, "dry_s": dry, "pieces": len(pieces), "audio_s": sum(p["audio_s"] for p in pieces),
           "ends_s": played_to, "detail": pieces}
    tag = f"{strategy}-{n}-{seconds}{'-parakeet' if with_parakeet else ''}"
    (OUT / f"replay-{tag}.json").write_text(json.dumps(row, indent=0))
    print(json.dumps({k: v for k, v in row.items() if k != "detail"}, default=lambda v: round(v, 3)))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("test", choices=["workers", "cut", "parakeet", "replay"])
    ap.add_argument("model")
    ap.add_argument("n", type=int, nargs="?", default=1)
    ap.add_argument("seconds", type=float, nargs="?", default=1.5)
    ap.add_argument("strategy", nargs="?", default="native")
    ap.add_argument("--parakeet", action="store_true")
    args = ap.parse_args()
    OUT.mkdir(parents=True, exist_ok=True)
    if args.test == "workers":
        workers_test(args.model, args.n)
    elif args.test == "cut":
        cut_test(args.model, args.n)
    elif args.test == "replay":
        replay(args.n, args.seconds, args.strategy, args.parakeet)
    else:
        parakeet_test()


if __name__ == "__main__":
    main()
