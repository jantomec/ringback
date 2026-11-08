#!/usr/bin/env python3
"""Prints the context trial's tables from what context_join.py, overlap.py and schedule.py wrote.

    env/bin/python -I harness/context_report.py
"""
import json
import os
import pathlib
import statistics

ROOT = pathlib.Path(os.environ.get("TTS_ROOT", "~/ringback-models-test/tts")).expanduser()
CONTEXT = ROOT / "runs/context"


def number(v, digits=2):
    return "-" if v is None else f"{v:.{digits}f}"


def methods(model):
    d = json.loads((CONTEXT / model / "results.json").read_text())
    print(f"\n{model}\n")
    print("| Method | Words heard wrong | Offset | Rise | Attack | Moved from separate | Pause, median / max | Stability | Pitch IQR | Joins, in speech | Join level | Click | Step at joins (LU, st) | Step at natural ends |")
    print("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
    for name, v in d["methods"].items():
        moved = (v.get("moved") or {}).get("offset")
        j, n = v["steps"]["join"], v["steps"]["natural"]
        print(f"| {name} | {v['wrong_words']} | {number(v['offset']['median'], 1)} ±{number(v['offset']['sd'], 1)} | "
              f"{number(v['rise']['median'], 1)} ±{number(v['rise']['sd'], 1)} | {number(v['attack']['median'], 1)} ±{number(v['attack']['sd'], 1)} | "
              f"{number(moved, 1)} | {number(v['gap_s_median'])} / {number(v['gap_s_max'])} | {number(v['steadiness'])} | "
              f"{number(v['pitch_iqr_st'], 1)} | {v['joins']}, {v.get('joins_in_speech', 0)} | {number(v['join_level_db_max'], 0)} dB | {number(v['click_db_max'], 0)} dB | "
              f"{number(j['loudness_lu'], 1)}, {number(j['pitch_st'], 1)} | {number(n['loudness_lu'], 1)}, {number(n['pitch_st'], 1)} |")
    if "cuts" not in d:
        return
    print("\nFinding the cut:\n")
    print("| Passes | Cuts | Pause: in the gap, right words | Word times: in the gap, right words |")
    print("|---|---|---|---|")
    for m in ("ctx1", "ctx2", "window", "overlap"):
        r = [x for x in d["cuts"] if x["method"] == m]
        print(f"| {m} | {len(r)} | {sum(x['pause_in_gap'] for x in r)}, {sum(x['pause_ok'] for x in r)} | "
              f"{sum(x['words_in_gap'] for x in r)}, {sum(x['words_ok'] for x in r)} |")
    r = d["cuts"]
    print(f"| all | {len(r)} | {sum(x['pause_in_gap'] for x in r)}, {sum(x['pause_ok'] for x in r)} | "
          f"{sum(x['words_in_gap'] for x in r)}, {sum(x['words_ok'] for x in r)} |")
    print(f"\nParakeet on the CPU to get word times: median {statistics.median(x['asr_s'] for x in r):.2f} s a pass.")
    s = d["short"]
    print(f"\nA very short first sentence, {len(s)} passes: pause {sum(x['pause_ok'] for x in s)} right, word times {sum(x['words_ok'] for x in s)} right")
    for first in sorted({x["first"] for x in s}):
        some = [x for x in s if x["first"] == first]
        print(f"- {first} pause {sum(x['pause_ok'] for x in some)}/{len(some)}, word times {sum(x['words_ok'] for x in some)}/{len(some)}; heard: {some[0]['heard'][:60]}")


def paired(model):
    """Each sentence's rise against the same sentence made alone: does context change how it begins?"""
    d = json.loads((CONTEXT / model / "results.json").read_text())["methods"]
    base = d["separate"]["starts"]
    print(f"\n{model}, rise at the start of a sentence, against the same sentence made alone:")
    for name, v in d.items():
        if name == "separate":
            continue
        pairs = [(a["rise"], b["rise"]) for a, b in zip(v["starts"], base) if a["rise"] is not None and b["rise"] is not None]
        if pairs:
            diff = [a - b for a, b in pairs]
            print(f"  {name}: median change {statistics.median(diff):+.1f} st, lower in {sum(x < 0 for x in diff)} of {len(diff)}")


def boundary_steps(model):
    """The step in pitch and loudness across each sentence end, against the same end made separately."""
    d = json.loads((CONTEXT / model / "results.json").read_text())["methods"]
    base = d["separate"]["boundary_steps"]
    print(f"\n{model}, steps across sentence ends, against the same end between sentences made separately:")
    for name, v in d.items():
        if name == "separate":
            continue
        for i, unit, label in ((1, "st", "pitch"), (0, "LU", "loudness")):
            pairs = [(abs(v["boundary_steps"][k][i]), abs(base[k][i])) for k in base
                     if k in v["boundary_steps"] and v["boundary_steps"][k][i] is not None and base[k][i] is not None]
            if pairs:
                diff = [a - b for a, b in pairs]
                print(f"  {name} {label}: {statistics.median(a for a, _ in pairs):.1f} against {statistics.median(b for _, b in pairs):.1f} {unit}, "
                      f"smaller at {sum(x < 0 for x in diff)} of {len(diff)}")


def lost(path):
    p = path / "lost.json"
    if not p.exists():
        return None
    rows = json.loads(p.read_text())
    return sum(bool(r["lost"]) for r in rows), len(rows), sum(len(r["lost"]) for r in rows), sum(r["words"] for r in rows)


def replays():
    print("\nReplays (text arriving over the seconds shown):\n")
    print("| Strategy | Sentences | Text over | First audio | Dry | Pieces | Audio | Parakeet working |")
    print("|---|---|---|---|---|---|---|---|")
    for p in sorted((ROOT / "runs/overlap").glob("replay-*.json")):
        d = json.loads(p.read_text())
        print(f"| {d['strategy']} | {d['sentences']} | {d['text_over_s']:.0f} s | {d['first_audio_s'] * 1000:.0f} ms | "
              f"{d['dry_s']:.2f} s | {d['pieces']} | {d['audio_s']:.1f} s | {'yes' if d['parakeet'] else 'no'} |")


def main():
    for model in ("kokoro", "phonon", "ming", "ming-speaker"):
        if (CONTEXT / model / "results.json").exists():
            methods(model)
    for model in ("kokoro", "phonon", "ming"):
        paired(model)
    for model in ("kokoro", "phonon", "ming", "ming-speaker"):
        boundary_steps(model)
    replays()
    print("\nWord loss by setting, 3-sentence passes (passes that lost words, of; words lost, of):\n")
    for name in sorted(p.name for p in (CONTEXT / "ming").iterdir() if (p / "lost.json").exists()):
        print(name, lost(CONTEXT / "ming" / name))
    for name in ("ming-bf16", "ming-anchored", "ming-speaker"):
        if (CONTEXT / name / "drops/lost.json").exists():
            print(name, lost(CONTEXT / name / "drops"))
    print("\nWorkers:\n")
    for model in ("ming", "phonon"):
        for n in (1, 2, 3):
            d = json.loads((ROOT / f"runs/overlap/workers-{model}-{n}.json").read_text())
            print(f"{model} {n}: {d['speed_x_median']:.2f}× each (worst {d['speed_x_worst']:.2f}×), first audio "
                  f"{d['first_s_median'] * 1000:.0f} / {d['first_s_worst'] * 1000:.0f} ms, GPU {d['gpu_peak_mib']} MiB")


if __name__ == "__main__":
    main()
