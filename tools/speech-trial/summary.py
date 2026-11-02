"""Summarises a live log of the on-device speech trial (the app's SpeechTrial, in debug builds):
whether audio and results kept coming in the background and with the phone locked, how soon
the final words came once asked for, the first words after you started, the app's memory and
the battery. See README.md.

"Locked" is iOS's data protection: from the lock until Face ID or the passcode unlocks it,
which a glance at the lock screen does while the screen stays locked. "Background" is the app
not on screen, from the ticks every 5 s: the screen locked, or another app shown.

    python3 tools/speech-trial/summary.py LIVE.jsonl
"""

import json
import sys


def stats(values: list[float]) -> str:
    if not values:
        return "none"
    s = sorted(values)
    p90 = s[min(len(s) - 1, int(0.9 * len(s)))]
    return f"{len(s)}: median {s[len(s) // 2]:.2f} s, 90% {p90:.2f} s, max {s[-1]:.2f} s"


def main() -> None:
    events = [json.loads(line) for line in open(sys.argv[1]) if line.strip()]
    start = next(e for e in events if e["kind"] == "start")
    stop = next((e for e in events if e["kind"] == "stop"), events[-1])
    print(f"formats: input {start['input_format']}; analyzer {start['analyzer_format']}")
    print(f"model ready in {start['prepare_s']:.2f} s; call {stop['t'] / 60:.1f} min")

    # Locked spans, from the lock events; the call starts unlocked.
    spans, locked_at = [], None
    for e in events:
        if e["kind"] == "locked" and locked_at is None:
            locked_at = e["t"]
        elif e["kind"] == "unlocked" and locked_at is not None:
            spans.append((locked_at, e["t"]))
            locked_at = None
    if locked_at is not None:
        spans.append((locked_at, stop["t"]))

    def locked(t: float) -> bool:
        return any(a <= t <= b for a, b in spans)

    ticks = [e for e in events if e["kind"] == "tick"]

    def background(t: float) -> bool:
        """The app was in the background at t: the nearest tick says so."""
        tick = min(ticks, key=lambda e: abs(e["t"] - t), default={})
        return tick.get("background", False)

    finals = [e for e in events if e["kind"] == "final"]
    for name, inside in (("locked", locked), ("in the background", background)):
        spanned = [(a, b) for a, b in zip(ticks, ticks[1:]) if inside(b["t"])]
        audio = sum(b["audio_s"] - a["audio_s"] for a, b in spanned)
        wall = sum(b["t"] - a["t"] for a, b in spanned)
        print(f"{name}: {wall / 60:.1f} min between ticks, with {audio / max(wall, 1e-9) * 100:.0f}% of the audio arriving; "
              f"final results {sum(inside(e['t']) for e in finals)} of {len(finals)}")  # fmt: skip
    errors = [e for e in events if e["kind"] == "error"]
    if errors:
        print("errors:", "; ".join(f"{e['t']:.0f} s: {e['text']}" for e in errors))

    latencies = [e["latency_s"] for e in events if "latency_s" in e]
    print("final words after your last loud audio (with the 0.6 s the trial waits):", stats(latencies))
    # After the trial asked for them (finalize), at the end of speech: what a call's own end of
    # turn would wait for.
    asked = []
    for i, e in enumerate(events):
        if e["kind"] == "speech_end":
            later = next((f for f in events[i + 1:] if f["kind"] in ("final", "speech_start")), None)
            if later and later["kind"] == "final":
                asked.append(later["t"] - e["t"])
    print("final words after the trial asked for them:", stats(asked))
    bursts = sorted({round(e["t"], 1) for e in events if e["kind"] == "volatile"})
    gaps = [b - a for a, b in zip(bursts, bursts[1:]) if b - a > 0.5]
    print("tentative words came in bursts, every", stats(gaps))
    print("first words after you started:", stats([e["first_words_s"] for e in events if "first_words_s" in e]))
    ends = sum(e["kind"] == "speech_end" for e in events)
    print(f"stops of speech found {ends}, timed {len(latencies)}")

    footprints = [e["footprint_mb"] for e in events if "footprint_mb" in e]
    available = [e["available_mb"] for e in events if "available_mb" in e]
    print(f"app's memory {min(footprints):.0f}–{max(footprints):.0f} MB; available to it at least {min(available):.0f} MB")
    b0, b1, minutes = stop.get("battery_start", -1), stop.get("battery_end", -1), stop["t"] / 60
    if b0 >= 0 and b1 >= 0 and minutes > 0:
        print(f"battery {b0 * 100:.0f}% → {b1 * 100:.0f}% in {minutes:.1f} min ({(b0 - b1) * 100 / minutes * 60:.0f}% an hour)")
    print()
    for e in finals:
        print(f"{e['t']:7.1f} s {'locked ' if locked(e['t']) else '       '}{e.get('latency_s', 0) or 0:5.2f} s  {e['text']}")


if __name__ == "__main__":
    main()
