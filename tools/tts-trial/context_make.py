#!/usr/bin/env python3
"""Makes the run's 24 sentences each way a reply could be made, to see what context does.

    harness/gate.sh python3 harness/context_make.py MODEL [--only METHOD...]

Methods, for MODEL ming, phonon or kokoro:
  whole     all 24 as one text, in the runtime's own chunks
  whole8    the first 8 as one text
  call      as a call does: sentence 1 alone, then the rest as one piece, in the runtime's chunks.
            With the text arriving over 1-2 s, the rest is all there one second before sentence 1 ends
  ctx1      each sentence with the one before it spoken first, to be cut away
  ctx2      each sentence with the two before it
  window    six sentences a pass, the first two to be cut away: a piece as a call makes it, with context
  overlap   the user's idea over the first 8: 1 alone, 1 to 5, then 3 to 8, as far as a pass can hold
  native1   Ming only: each sentence with the last one's audio and text as its prompt, nothing regenerated
  native2   the same with the last two
  nativegrp the same, four sentences a piece, with the last sentence as the prompt
  nativespk native1 with Ming's speaker embedding on, taken from the prompt
  anchor    each sentence with sentence 1 as its prompt throughout, as Ming's own long-text code does
  anchorgrp the same, four sentences a piece
  short     a very short sentence and then ordinary ones, to see what a cut does there
  drops     passes of 1, 2, 3, 4 and 6 sentences at four places in the run, three seeds each, to see
            whether a longer pass loses words

The separately made sentences are in ROOT/runs/MODEL/all. Writes ROOT/runs/context/MODEL/METHOD/
PIECE.wav and passes.json: each pass's text, its sentences, which of them are kept, and its timing.
"""
import argparse
import array
import json
import math
import pathlib
import re
import sys
import time
import wave

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import trial  # noqa: E402

ROOT = trial.ROOT
RUN = [s["text"] for s in json.loads((trial.HERE / "sentences.json").read_text()) if s["kind"] == "run"]
SHORT = ["Okay.", "Done.", "Yes.", "Sure, go ahead.", "No, not yet.", "Got it, thanks."]
AFTER = [RUN[0], RUN[6], RUN[8]]
TRAIL_KEPT = 0.1  # seconds of a prompt's trailing silence kept
JOIN_PAUSE = 0.38  # seconds of pause at a join between takes: the median inside a take


def timed(speaker, text, extra=None):
    t0, chunks, pcm = speaker.speak(text, extra)
    audio = len(pcm) / 2 / speaker.rate
    row = {"first_s": chunks[0][0] - t0 if chunks else None, "total_s": chunks[-1][0] - t0 if chunks else None,
           "audio_s": audio, "ahead_s": trial.ahead(chunks, speaker.rate, t0), "chunks": len(chunks)}
    return row, pcm


def without_tail(pcm, rate):
    """The audio up to TRAIL_KEPT after its last sound, for use as a prompt: the model goes on from it."""
    a = array.array("h", pcm)
    n = rate // 100
    last = 0
    for i in range(0, len(a) - n, n):
        if math.sqrt(sum(v * v for v in a[i: i + n]) / n) > 100:
            last = i + n
    return a[: last + int(rate * TRAIL_KEPT)].tobytes()


def plan(method):
    """A pass: (sentence indexes said, how many at its start are context, or None for a chain)."""
    n = len(RUN)
    if method == "whole":
        return [(list(range(n)), 0)]
    if method.startswith("whole") and method != "whole":
        return [(list(range(int(method[5:]))), 0)]
    if method == "call":
        return [([0], 0), (list(range(1, n)), 0)]
    if method in ("ctx1", "ctx2"):
        k = int(method[-1])
        return [([0], 0)] + [(list(range(max(i - k, 0), i + 1)), min(i, k)) for i in range(1, n)]
    if method == "window":
        passes, i = [([0], 0)], 1
        while i < n:
            new = list(range(i, min(i + 4, n)))
            ctx = list(range(max(i - 2, 0), i))
            passes.append((ctx + new, len(ctx)))
            i += 4
        return passes
    if method == "overlap":
        return [([0], 0), ([0, 1, 2, 3, 4], 1), ([2, 3, 4, 5, 6, 7], 3)]
    raise ValueError(method)


def write(path, pcm, rate):
    trial.write_wav(path, pcm, rate)


def run_plan(speaker, method, out):
    rows = []
    for number, (said, context) in enumerate(plan(method)):
        text = " ".join(RUN[i] for i in said)
        row, pcm = timed(speaker, text)
        name = f"pass-{number:02d}"
        write(out / f"{name}.wav", pcm, speaker.rate)
        rows.append({"name": name, "said": said, "context": context, "text": text, **row})
    return rows


def run_native(speaker, method, out):
    """Ming goes on from what came before: the audio as its acoustic prompt, the words as its text."""
    k = 2 if method == "native2" else 1
    size = 4 if method.endswith("grp") else 1
    rows, spoken = [], []  # spoken: (sentence, pcm without its tail) of each piece so far
    i = 0
    number = 0
    fixed = method.startswith("anchor")
    options = {"use_speaker_embedding": "true" if fixed or method == "nativespk" else "false"}
    while i < len(RUN):
        said = [0] if i == 0 else list(range(i, min(i + size, len(RUN))))
        extra = None
        if i:
            ref = out / f"prompt-{number:02d}.wav"
            use = spoken[:1] if fixed else spoken[-k:]
            write(ref, b"".join(p for _, p in use), speaker.rate)
            extra = {"voice_ref": str(ref), "reference_text": " ".join(t for t, _ in use), "options": options}
        text = " ".join(RUN[j] for j in said)
        row, pcm = timed(speaker, text, extra)
        name = f"pass-{number:02d}"
        write(out / f"{name}.wav", pcm, speaker.rate)
        rows.append({"name": name, "said": said, "context": 0, "text": text, **row})
        spoken.append((text, without_tail(pcm, speaker.rate)))
        i += len(said)
        number += 1
    return rows


def run_drops(speaker, out, sizes=(1, 2, 3, 4, 6)):
    rows = []
    for size in sizes:
        for begin in (1, 6, 12, 18):
            said = list(range(begin, begin + size))
            for seed in (42, 7, 1895):
                speaker.seed = seed
                text = " ".join(RUN[i] for i in said)
                row, pcm = timed(speaker, text)
                name = f"pass-{size}-{begin:02d}-{seed}"
                write(out / f"{name}.wav", pcm, speaker.rate)
                rows.append({"name": name, "size": size, "seed": seed, "said": said, "text": text, **row})
    speaker.seed = trial.SEED
    return rows


def run_replies(speaker, out, seeds):
    """The replies in ROOT/replies.json, a list of strings, each as one pass. Real calls' replies are
    private, so the file stays on the node."""
    rows = []
    for i, text in enumerate(json.loads((ROOT / "replies.json").read_text())):
        size = max(1, len(re.findall(r"[.!?](?:\s|$)", text)))
        for seed in seeds:
            speaker.seed = seed
            row, pcm = timed(speaker, text)
            name = f"reply-{i:03d}-{seed}"
            write(out / f"{name}.wav", pcm, speaker.rate)
            rows.append({"name": name, "size": size, "seed": seed, "said": [i], "text": text, **row})
    speaker.seed = trial.SEED
    return rows


def last_sentence_start(pcm, rate):
    """Where the last sentence begins, in samples: after the longest pause in the last 8 s."""
    a = array.array("h", pcm)
    n = rate // 100
    begin = max(0, len(a) - 8 * rate)
    best, run, end = 0, 0, begin
    for i in range(begin, len(a) - n, n):
        if math.sqrt(sum(v * v for v in a[i: i + n]) / n) < 100:
            run += 1
            if run > best:
                best, end = run, i + n
        else:
            run = 0
    return end


def run_takes(speaker, out):
    """Sentences 1 to 5 as one take, then 6 to 8 going on from sentence 5: its audio and words as the
    prompt, and its speaker embedding. Both ends of the pause are cut to what a take leaves inside."""
    first = " ".join(RUN[:5])
    row, pcm = timed(speaker, first)
    write(out / "pass-00.wav", pcm, speaker.rate)
    spoken = without_tail(pcm, speaker.rate)
    prompt = array.array("h", spoken)[last_sentence_start(spoken, speaker.rate):].tobytes()
    write(out / "prompt.wav", prompt, speaker.rate)
    extra = {"voice_ref": str(out / "prompt.wav"), "reference_text": RUN[4],
             "options": {"zero_speaker_embedding": "false", "use_speaker_embedding": "true"}}
    second = " ".join(RUN[5:8])
    row2, pcm2 = timed(speaker, second, extra)
    write(out / "pass-01.wav", pcm2, speaker.rate)
    a = array.array("h", pcm2)
    n = speaker.rate // 100
    start = 0
    while start < len(a) - n and math.sqrt(sum(v * v for v in a[start: start + n]) / n) < 100:
        start += n
    pause = array.array("h", bytes(2 * int(speaker.rate * (JOIN_PAUSE - TRAIL_KEPT))))
    write(out / "take.wav", spoken + pause.tobytes() + a[start:].tobytes(), speaker.rate)
    return [{"name": "pass-00", "said": list(range(5)), "context": 0, "text": first, **row},
            {"name": "pass-01", "said": [5, 6, 7], "context": 0, "text": second, **row2}]


def run_batches(speaker, out):
    """The eight sentences as three takes split at sentence ends, joined by a pause as long as one
    inside a take, each end faded so nothing steps."""
    groups = [RUN[:3], RUN[3:6], RUN[6:8]]
    rows, joined = [], array.array("h")
    fade = speaker.rate // 50
    for number, group in enumerate(groups):
        text = " ".join(group)
        row, pcm = timed(speaker, text)
        name = f"pass-{number:02d}"
        write(out / f"{name}.wav", pcm, speaker.rate)
        rows.append({"name": name, "said": [RUN.index(g) for g in group], "context": 0, "text": text, **row})
        a = array.array("h", without_tail(pcm, speaker.rate))
        n = speaker.rate // 100
        start = 0
        while start < len(a) - n and math.sqrt(sum(v * v for v in a[start: start + n]) / n) < 100:
            start += n
        a = a[max(0, start - fade):]
        for i in range(fade):
            a[i] = int(a[i] * i / fade)
            a[-1 - i] = int(a[-1 - i] * i / fade)
        joined += a
        if number < len(groups) - 1:
            joined += array.array("h", bytes(2 * int(speaker.rate * (JOIN_PAUSE - TRAIL_KEPT))))
    write(out / "take.wav", joined.tobytes(), speaker.rate)
    return rows


LONG_LENGTHS = (50, 100, 150, 200, 300)  # words, rounded to whole sentences


def long_takes():
    """Each text of long_texts.json cut after whole sentences, as near each of LONG_LENGTHS as it gets."""
    takes = []
    for i, full in enumerate(json.loads((trial.HERE / "long_texts.json").read_text())):
        sentences = [s.strip() for s in re.findall(r"[^.!?]+[.!?]+", full)]
        for target in LONG_LENGTHS:
            best = None
            for n in range(1, len(sentences) + 1):
                text = " ".join(sentences[:n])
                if best is None or abs(len(text.split()) - target) < abs(len(best.split()) - target):
                    best = text
            takes.append((i, target, best))
    return takes


def slowest(chunks, rate, t0, window=10.0):
    """The slowest stretch of a take: audio seconds over wall seconds, across any window of audio."""
    marks, audio = [(t0, 0.0)], 0.0
    for t, n in chunks:
        audio += n / 2 / rate
        marks.append((t, audio))
    least, j = None, 0
    for i in range(len(marks)):
        while j < len(marks) and marks[j][1] - marks[i][1] < window:
            j += 1
        if j == len(marks):
            break
        took = marks[j][0] - marks[i][0]
        if took > 0:
            speed = (marks[j][1] - marks[i][1]) / took
            least = speed if least is None else min(least, speed)
    return least


def run_long(speaker, out, seeds):
    """The texts of long_texts.json at 50 to 300 words, each as one request: does a take keep its words?"""
    rows = []
    for i, target, text in long_takes():
        for seed in seeds:
            speaker.seed = seed
            t0, chunks, pcm = speaker.speak(text)
            name = f"long-{i}-{target:03d}-{seed}"
            write(out / f"{name}.wav", pcm, speaker.rate)
            audio = len(pcm) / 2 / speaker.rate
            rows.append({"name": name, "size": target, "seed": seed, "said": [i], "text": text,
                         "words": len(text.split()), "first_s": chunks[0][0] - t0 if chunks else None,
                         "total_s": chunks[-1][0] - t0 if chunks else None, "audio_s": audio,
                         "ahead_s": trial.ahead(chunks, speaker.rate, t0), "chunks": len(chunks),
                         "slowest_x": slowest(chunks, speaker.rate, t0)})
            print(name, f"{audio:.1f} s, first {rows[-1]['first_s']:.3f} s, slowest {rows[-1]['slowest_x'] or 0:.2f}x",
                  flush=True)
    speaker.seed = trial.SEED
    return rows


def run_short(speaker, out):
    rows = []
    for a, first in enumerate(SHORT):
        for b, rest in enumerate(AFTER):
            text = f"{first} {rest}"
            row, pcm = timed(speaker, text)
            name = f"pass-{a}{b}"
            write(out / f"{name}.wav", pcm, speaker.rate)
            rows.append({"name": name, "first": first, "rest": rest, "text": text, **row})
    return rows


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("model")
    ap.add_argument("--only", nargs="*")
    ap.add_argument("--try", dest="settings", help="drops only: JSON options to try, on 3-sentence passes")
    ap.add_argument("--label", default="drops")
    ap.add_argument("--seeds", default="42", help="replies and long only: the seeds to make each one with")
    ap.add_argument("--export", help="save this model's speaker embedding to this file, and stop")
    ap.add_argument("--save-prompt", action="store_true",
                    help="keep sentence 1 of the anchor method as refs/ming.wav, for ming-anchored, and stop")
    args = ap.parse_args()
    if args.save_prompt:
        first = ROOT / "runs/context/ming/anchor/pass-00.wav"
        with wave.open(str(first)) as w:
            pcm, rate = w.readframes(w.getnframes()), w.getframerate()
        write(ROOT / "refs/ming.wav", without_tail(pcm, rate), rate)
        (ROOT / "refs/ming.txt").write_text(RUN[0] + "\n")
        return
    cfg = dict(trial.MODELS[args.model])
    if args.settings:
        cfg["options"] = {**cfg.get("options", {}), **json.loads(args.settings)}
    log = ROOT / "runs/context" / args.model / "runtime.log"
    log.parent.mkdir(parents=True, exist_ok=True)
    speaker = trial.RUNTIMES[cfg["runtime"]](cfg, log)
    methods = ["whole", "whole8", "call"]
    if args.model != "kokoro":
        methods += ["ctx1", "ctx2", "window", "overlap", "short"]
    if args.model == "ming":
        methods += ["native1", "native2", "nativegrp", "nativespk", "anchor", "anchorgrp"]
    try:
        speaker.ready()
        for text in trial.WARM_UP:
            timed(speaker, text)
        if args.export:
            timed(speaker, trial.WARM_UP[0], {"options": {"export_speaker_embedding_file": args.export}})
            return
        for method in args.only or methods:
            out = log.parent / (args.label if method in ("drops", "replies", "long") else method)
            out.mkdir(exist_ok=True)
            start = time.time()
            if method == "short":
                rows = run_short(speaker, out)
            elif method == "replies":
                rows = run_replies(speaker, out, [int(v) for v in args.seeds.split(",")])
            elif method == "takes":
                rows = run_takes(speaker, out)
            elif method == "batches":
                rows = run_batches(speaker, out)
            elif method == "long":
                rows = run_long(speaker, out, [int(v) for v in args.seeds.split(",")])
            elif method == "drops":
                rows = run_drops(speaker, out, (3,) if args.settings else (1, 2, 3, 4, 6))
            elif method.startswith(("native", "anchor")):
                rows = run_native(speaker, method, out)
            else:
                rows = run_plan(speaker, method, out)
            (out / "passes.json").write_text(json.dumps(rows, indent=0))
            made = sum(r["audio_s"] for r in rows)
            print(f"{args.model} {method}: {len(rows)} passes, {made:.1f} s made in {time.time() - start:.1f} s", flush=True)
    finally:
        speaker.close()


if __name__ == "__main__":
    main()
