"""Short answers through the speech node's hearing: which ones start a turn and get their words.

Speaks short answers ("Yes.", "No." and longer ones) in several Kokoro voices here, sets each
to a loudness as the node measures it (the loudest 400 ms block, BS.1770), puts it after a
stretch of room noise, and has the speech node hear each file as a call does
(`ringback-speech listen`: Silero with the background filter, Smart Turn, Parakeet), with the
background filter at its default and off. See README.md.
"""

import argparse
import json
import math
import subprocess
import sys
import tempfile
from collections import Counter
from pathlib import Path

import numpy as np
import soxr

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "stand-in-phone"))
from stand_in_phone import KOKORO  # noqa: E402

RATE = 16000
WORDS = ["Yes.", "No.", "Yeah.", "Nope.", "Okay.", "Yes, go ahead.", "It's a yes."]
VOICES = ["am_michael", "am_adam", "bf_emma", "bm_george"]  # not af_heart, the call's own voice
LEVELS = [-25, -30, -35, -40, -45, -50]  # LUFS of the loudest 400 ms block (the filter is at -40)
ROOMS = {"quiet room": -75.0, "noisy room": -55.0}  # LUFS of the noise before and after
GATES = {"filter 0.5": 0.5, "filter off": 0.0}
LEAD, TAIL = 1.5, 0.5  # seconds of room noise before and after the word


def k_weighted(x: np.ndarray) -> np.ndarray:
    """BS.1770's K-weighting at 16 kHz, as speech/internal/hearing/loudness.go has it."""
    f0, g, q = 1681.974450955533, 3.999843853973347, 0.7071752369554196
    k = math.tan(math.pi * f0 / RATE)
    vh, a0 = 10 ** (g / 20), 1 + k / q + k * k
    vb = vh**0.4996667741545416
    shelf = ((vh + vb * k / q + k * k) / a0, 2 * (k * k - vh) / a0, (vh - vb * k / q + k * k) / a0,
             2 * (k * k - 1) / a0, (1 - k / q + k * k) / a0)  # fmt: skip
    f0, q = 38.13547087602444, 0.5003270373238773
    k = math.tan(math.pi * f0 / RATE)
    a0 = 1 + k / q + k * k
    high_pass = (1.0, -2.0, 1.0, 2 * (k * k - 1) / a0, (1 - k / q + k * k) / a0)
    for b0, b1, b2, a1, a2 in (shelf, high_pass):
        y = np.zeros_like(x)
        x1 = x2 = y1 = y2 = 0.0
        for i, v in enumerate(x):
            out = b0 * v + b1 * x1 + b2 * x2 - a1 * y1 - a2 * y2
            x2, x1, y2, y1 = x1, v, y1, out
            y[i] = out
        x = y
    return x


def loudest_block(x: np.ndarray) -> float:
    """The loudness of the loudest 400 ms block, in LUFS, stepping 32 ms as the node does."""
    w = k_weighted(x) ** 2
    block, step = int(0.4 * RATE), 512
    sums = np.convolve(w, np.ones(block), "valid")[::step] / block
    return 10 * math.log10(max(sums.max(), 1e-20)) - 0.691


def noise(seconds: float, lufs: float, rng: np.random.Generator) -> np.ndarray:
    """White noise at a loudness: K-weighting changes white noise's level by a fixed amount,
    so it's measured once."""
    global WHITE
    if WHITE is None:
        WHITE = loudest_block(np.random.default_rng(0).standard_normal(2 * RATE))
    return rng.standard_normal(int(seconds * RATE)) * 10 ** ((lufs - WHITE) / 20)


WHITE = None  # the loudness of white noise with a standard deviation of 1


def make_clips(out: Path) -> list[dict]:
    from mlx_audio.tts.utils import load_model

    kokoro = load_model(KOKORO)
    rng = np.random.default_rng(1)
    clips = []
    for voice in VOICES:
        for word in WORDS:
            audio = np.concatenate([np.array(r.audio) for r in kokoro.generate(word, voice=voice, lang_code="a")])
            audio = soxr.resample(audio.astype(np.float64), 24000, RATE)
            loud = np.flatnonzero(np.abs(audio) > 0.01 * np.abs(audio).max())
            audio = audio[loud[0] : loud[-1] + 1]  # the word alone, without Kokoro's own silence
            length, loudness = len(audio) / RATE, loudest_block(audio)
            for level in LEVELS:
                word_at = audio * 10 ** ((level - loudness) / 20)
                for room, floor in ROOMS.items():
                    x = np.concatenate([noise(LEAD, floor, rng), word_at, noise(TAIL, floor, rng)])
                    name = f"{voice}-{word.strip('.').replace(' ', '_').replace(',', '').replace(chr(39), '')}-{-level}-{floor:.0f}.wav"
                    pcm = np.clip(np.round(x * 32767), -32768, 32767).astype("<i2")
                    write_wav(out / name, pcm)
                    clips.append({"file": name, "voice": voice, "word": word, "secs": round(length, 2), "level": level, "room": room})
    return clips


def write_wav(path: Path, pcm: np.ndarray) -> None:
    import wave

    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(RATE)
        w.writeframes(pcm.tobytes())


def heard(events: list[dict], word: str) -> str:
    """What became of a clip: its words, a turn with none, a stretch not taken, or nothing."""
    turns = [e for e in events if e["event"] == "turn"]
    if turns:
        text = " ".join(e["text"] for e in turns).strip()
        return f"heard: {text}" if text else "turn, no words"
    if any(e["event"] == "missed" for e in events):
        return "not taken"
    return "nothing"


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--node", required=True, help="the speech node's ssh host")
    p.add_argument("--node-dir", default="~/ringback-speech", help="its install directory")
    p.add_argument("--bin", default="bin", help="the folder in it with the binary (bin, bin-next)")
    p.add_argument("--keep", type=Path, help="keep the clips and the node's answers here")
    args = p.parse_args()

    work = args.keep or Path(tempfile.mkdtemp(prefix="short-answers-"))
    clips_dir = work / "clips"
    clips_dir.mkdir(parents=True, exist_ok=True)
    clips = make_clips(clips_dir)
    remote = f"{args.node_dir}/scratch/short-answers"
    subprocess.run(["ssh", args.node, f"rm -rf {remote} && mkdir -p {remote}"], check=True)
    subprocess.run(["rsync", "-a", f"{clips_dir}/", f"{args.node}:{remote}/"], check=True)
    results: dict[tuple[str, str], list[dict]] = {}
    for gate_name, gate in GATES.items():
        out = subprocess.run(
            ["ssh", args.node, f"cd {args.node_dir} && . ./env.sh && {args.bin}/ringback-speech listen --gate {gate} {remote}/*.wav"],
            check=True, capture_output=True, text=True,
        ).stdout  # fmt: skip
        (work / f"listen-{gate}.jsonl").write_text(out)
        for line in out.splitlines():
            if line.startswith("{"):
                e = json.loads(line)
                results.setdefault((gate_name, e["file"]), []).append(e)
    subprocess.run(["ssh", args.node, f"rm -rf {remote}"], check=True)

    # One line per word and voice: what became of it at each level, room and filter.
    print(f"{len(clips)} clips: {len(WORDS)} answers × {len(VOICES)} voices × {len(LEVELS)} levels × {len(ROOMS)} rooms, "
          f"each with the filter at 0.5 and off ({args.bin})\n")  # fmt: skip
    short = {"heard": "✓", "turn, no words": "∅", "not taken": "✗", "nothing": "·"}
    for gate_name in GATES:
        for room in ROOMS:
            print(f"{gate_name}, {room}: ✓ heard (words) · ∅ a turn with no words · ✗ not taken for speech · · nothing")
            print(f"  {'answer':<16} {'voice':<11} {'secs':>5}  " + "  ".join(f"{lv:>4}" for lv in LEVELS))
            for c in clips:
                if c["room"] != room or c["level"] != LEVELS[0]:
                    continue
                row = []
                for lv in LEVELS:
                    f = next(x["file"] for x in clips if x["voice"] == c["voice"] and x["word"] == c["word"]
                             and x["level"] == lv and x["room"] == room)  # fmt: skip
                    row.append(short[heard(results.get((gate_name, f), []), c["word"]).split(":")[0]])
                print(f"  {c['word']:<16} {c['voice']:<11} {c['secs']:>5.2f}  " + "  ".join(f"{r:>4}" for r in row))
            print()
    print("Totals (heard / no words / not taken / nothing, of the clips at each level):")
    for gate_name in GATES:
        for room in ROOMS:
            cells = []
            for lv in LEVELS:
                n = Counter(heard(results.get((gate_name, c["file"]), []), c["word"]).split(":")[0]
                            for c in clips if c["level"] == lv and c["room"] == room)  # fmt: skip
                cells.append(f"{lv} LUFS {n['heard']}/{n['turn, no words']}/{n['not taken']}/{n['nothing']}")
            print(f"  {gate_name}, {room}: " + ", ".join(cells))
    got = [heard(results.get((g, c["file"]), []), c["word"]) for g in GATES for c in clips]
    words = Counter(w.split(": ", 1)[1] for w in got if w.startswith("heard"))
    print("\nWhat Parakeet wrote for the clips it heard:", dict(words.most_common()))
    if args.keep:
        print(f"\nKept in {work}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
