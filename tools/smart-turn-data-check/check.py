"""How much silence follows the speech in smart-turn-data clips, and does it give the label away?

Run it on parquet shards of pipecat-ai/smart-turn-data-v3.2-{train,test}. See README.md.
"""
import argparse
import csv
import io
import sys
import time

import numpy as np
import onnxruntime as ort
import pyarrow.parquet as pq
import soundfile as sf

SR = 16000
WINDOW, CONTEXT = 512, 64  # Silero v5 at 16 kHz
PAD = 0.03  # Silero's speech_pad_ms, as OpenHRI's offsets use
META = ["id", "language", "endpoint_bool", "midfiller", "endfiller", "synthetic", "dataset"]


def silero_probs(session, x):
    state = np.zeros((2, 1, 128), dtype=np.float32)
    context = np.zeros((1, CONTEXT), dtype=np.float32)
    probs = []
    for i in range(len(x) // WINDOW):
        frame = x[i * WINDOW : (i + 1) * WINDOW][None, :]
        out, state = session.run(
            None,
            {
                "input": np.concatenate([context, frame], axis=1),
                "state": state,
                "sr": np.array(SR, dtype=np.int64),
            },
        )
        context = frame[:, -CONTEXT:]
        probs.append(float(out.reshape(-1)[0]))
    return np.array(probs)


def silero_offset(p):
    """End of the last speech segment: holds until p < 0.35, as get_speech_timestamps does."""
    above = np.where(p >= 0.5)[0]
    if len(above) == 0:
        return None
    i = above[-1]
    while i + 1 < len(p) and p[i + 1] >= 0.35:
        i += 1
    return (i + 1) * WINDOW / SR + PAD


def energy_offset(x, frame=320):
    """End of the last 20 ms frame louder than max(floor + 12 dB, -50 dBFS)."""
    n = len(x) // frame
    if n == 0:
        return None
    f = x[: n * frame].reshape(n, frame).astype(np.float64)
    db = 20 * np.log10(np.sqrt(np.mean(f * f, axis=1)) + 1e-9)
    loud = np.where(db > max(np.percentile(db, 10) + 12, -50))[0]
    return None if len(loud) == 0 else (loud[-1] + 1) * frame / SR


def measure(shards, vad_path, out_path):
    options = ort.SessionOptions()
    options.intra_op_num_threads = options.inter_op_num_threads = 1
    session = ort.InferenceSession(vad_path, options, providers=["CPUExecutionProvider"])
    rows = []
    started = time.time()
    for shard in shards:
        for batch in pq.ParquetFile(shard).iter_batches(batch_size=64, columns=META + ["audio"]):
            d = batch.to_pydict()
            for i in range(batch.num_rows):
                x, sr = sf.read(io.BytesIO(d["audio"][i]["bytes"]), dtype="float32", always_2d=False)
                if x.ndim > 1:
                    x = x.mean(axis=1)
                if sr != SR:
                    sys.exit(f"expected {SR} Hz, got {sr}")
                duration = len(x) / SR
                silero, energy = silero_offset(silero_probs(session, x)), energy_offset(x)
                rows.append(
                    {k: d[k][i] for k in META}
                    | {
                        "duration": duration,
                        "tail_silero": None if silero is None else max(0.0, duration - silero),
                        "tail_energy": None if energy is None else max(0.0, duration - energy),
                    }
                )
    if out_path:
        with open(out_path, "w", newline="") as f:
            w = csv.DictWriter(f, fieldnames=list(rows[0]))
            w.writeheader()
            w.writerows(rows)
    print(f"{len(rows)} clips, {time.time() - started:.0f} s")
    return rows


def spread(rows, key):
    v = sorted(r[key] for r in rows if r[key] is not None)
    if not v:
        return "no clips"
    return f"median {v[len(v) // 2]:.2f} s, under 0.2 s {sum(x < 0.2 for x in v) / len(v):5.1%}"


def auc(rows, key):
    """Chance that a complete clip has a longer tail than an incomplete one (0.5 = no leak)."""
    done = np.array([r[key] for r in rows if r[key] is not None and r["endpoint_bool"]])
    open_ = np.array([r[key] for r in rows if r[key] is not None and not r["endpoint_bool"]])
    if len(done) == 0 or len(open_) == 0:
        return float("nan")
    wins = (done[:, None] > open_[None, :]).mean() + 0.5 * (done[:, None] == open_[None, :]).mean()
    return float(wins)


def report(rows):
    groups = [
        ("all clips", rows),
        ("complete", [r for r in rows if r["endpoint_bool"]]),
        ("incomplete", [r for r in rows if not r["endpoint_bool"]]),
        ("synthetic", [r for r in rows if r["synthetic"]]),
        ("human", [r for r in rows if not r["synthetic"]]),
        ("English", [r for r in rows if r["language"] == "eng"]),
        ("English human", [r for r in rows if r["language"] == "eng" and not r["synthetic"]]),
    ]
    for source in sorted({r["dataset"] for r in rows}, key=lambda s: -sum(r["dataset"] == s for r in rows)):
        groups.append((source, [r for r in rows if r["dataset"] == source]))
    print(f"{'group':24} {'n':>5}  {'Silero offset':34}  {'energy offset':34}  AUC Silero  AUC energy")
    for name, group in groups:
        print(
            f"{name:24} {len(group):5}  {spread(group, 'tail_silero'):34}  {spread(group, 'tail_energy'):34}"
            f"  {auc(group, 'tail_silero'):10.3f}  {auc(group, 'tail_energy'):10.3f}"
        )


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("shards", nargs="+")
    parser.add_argument("--vad", required=True, help="silero_vad.onnx")
    parser.add_argument("--out", help="write one row per clip as CSV")
    args = parser.parse_args()
    report(measure(args.shards, args.vad, args.out))
