#!/usr/bin/env python3
"""Prints the trial's results, in two tables that keep the test blind:

    env/bin/python -I harness/summary.py KEY.json MODEL...

Speed and memory by real name, with no letters. Mistakes and steadiness by letter, with no names:
anything a listener could hear would otherwise give away which voice is which. Only key.json joins
the two, and this script never prints that join.
"""
import argparse
import json
import os
import pathlib
import statistics

ROOT = pathlib.Path(os.environ.get("TTS_ROOT", "~/ringback-models-test/tts")).expanduser()


def load(model, label):
    p = ROOT / "runs" / model / label / "summary.json"
    return json.loads(p.read_text()) if p.exists() else None


def score_of(model):
    return json.loads((ROOT / "runs" / model / "all" / "score.json").read_text())


def timing(model):
    rows = []
    for label in ("all", "repeat"):
        p = ROOT / "runs" / model / label / "timing.jsonl"
        if p.exists():
            rows += [json.loads(line) for line in p.read_text().splitlines()]
    return [r for r in rows if r["audio_s"] > 0]


def speed_table(models):
    print("| Model | Speed | Slowest sentence | First audio, median / worst | First speech, median / worst "
          "| Gap-free start, worst | GPU peak | Host peak | Ready | CPU cores | With 3 GB held |")
    print("|---|---|---|---|---|---|---|---|---|---|---|")
    for m in models:
        a, b = load(m, "all"), load(m, "ballast")
        again = load(m, "repeat") or a
        rows = timing(m)
        total = sum(r["audio_s"] for r in rows) / sum(r["total_s"] for r in rows)
        long_enough = [r["audio_s"] / r["total_s"] for r in rows if r["audio_s"] >= 1.5]
        first = [r["first_s"] for r in rows]
        gap = [r["ahead_s"] for r in rows]
        if b and not b.get("error"):
            fit = f"{b['speed_x']:.2f}×, {b['gpu_peak_total_mib']} MiB in all"
        else:
            fit = f"failed: {(b or {}).get('error', 'not run')}"[:60]
        # First speech: first audio plus the silence the model starts with, which is what you wait for.
        lead = {r["id"]: r["lead_s"] for r in score_of(m)["rows"]}
        speech = [r["first_s"] + lead.get(r["id"], 0) for r in rows]
        print(f"| {m} | {total:.2f}× | {min(long_enough):.2f}× | {statistics.median(first) * 1000:.0f} / "
              f"{max(first) * 1000:.0f} ms | {statistics.median(speech) * 1000:.0f} / {max(speech) * 1000:.0f} ms | "
              f"{max(gap) * 1000:.0f} ms | {max(a['gpu_peak_model_mib'], again['gpu_peak_model_mib'])} MiB | "
              f"{max(a['host_peak_mib'], again['host_peak_mib'])} MiB | {a['ready_s']:.1f} s | "
              f"{a.get('cores_used', 0):.1f} | {fit} |")
    print(f"\n{sum(len(timing(m)) for m in models)} sentences timed over two passes each.")


def blind_table(key):
    stable = json.loads((ROOT / "stability-titanet.json").read_text())
    print("\n| Voice | Raw keys missed | Spoken keys missed | Plain words wrong | Steadiness "
          "| Pitch | Pitch IQR / range | Loudness SD / range | Pace SD | Yes/no end | Wh- end |")
    print("|---|---|---|---|---|---|---|---|---|---|---|")
    for letter, model in key.items():
        r = score_of(model)
        st = r["movement"]
        print(f"| {letter} | {r['raw_keys_missed']}/{r['raw_keys_total']} | "
              f"{r['spoken_keys_missed']}/{r['spoken_keys_total']} | {r['plain_wer_percent']:.1f}% | "
              f"{stable['models'][model]['steadiness']:.2f} | {st['pitch_median_hz']:.0f} Hz | "
              f"{st['pitch_iqr_semitones']:.1f} / {st['pitch_range_semitones']:.1f} st | "
              f"{st['loudness_sd_lu']:.1f} / {st['loudness_range_lu']:.1f} LU | {st['pace_sd_percent']:.0f}% | "
              f"{r['yes_no_final_rise_st']:+.1f} st | {r['wh_final_rise_st']:+.1f} st |")
    print(f"\nSteadiness controls: Kokoro voices {min(stable['control_steady'].values()):.2f}-"
          f"{max(stable['control_steady'].values()):.2f}, a voice changing at every sentence "
          f"{stable['control_changing_voice_mean']:.2f} (95th percentile {stable['control_changing_voice_p95']:.2f}).")
    print("\nWhat Parakeet heard where a key was missed:\n")
    for letter, model in key.items():
        for row in score_of(model)["rows"]:
            if row.get("missed"):
                print(f"- {letter} `{row['id']}`: \"{row['heard']}\" (missed {', '.join(row['missed'])})")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("key")
    ap.add_argument("models", nargs="+")
    ap.add_argument("--raw", help="also write the per-sentence numbers here, timing by name, the rest by letter")
    args = ap.parse_args()
    key = json.loads(pathlib.Path(args.key).read_text())
    speed_table(args.models)
    blind_table(key)
    if args.raw:
        raw = pathlib.Path(args.raw)
        raw.mkdir(parents=True, exist_ok=True)
        for m in args.models:
            rows = [{k: r[k] for k in ("id", "first_s", "total_s", "audio_s", "ahead_s", "chunks")}
                    for r in timing(m)]
            (raw / f"timing-{m}.json").write_text(json.dumps(rows, indent=0))
        (raw / "scores-by-letter.json").write_text(json.dumps({
            letter: {k: v for k, v in score_of(model).items() if k != "model"}
            for letter, model in key.items()}, indent=0, ensure_ascii=False))


main()
