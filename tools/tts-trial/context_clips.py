#!/usr/bin/env python3
"""The context trial's listening clips: the first 8 sentences of the run, each way each model made them.

    env/bin/python -I harness/context_clips.py OUT

Not blind: they're named by model and method. They go through the same path as the blind set (24 kHz,
-23 LUFS, the call's Opus, AAC), one gain a clip, and keep what each method made between its sentences.
The ends of a clip are evened out, as in the blind set. Writes OUT/MODEL-METHOD.m4a and OUT/clips.json.
"""
import json
import pathlib
import sys
import tempfile

import numpy as np
import soundfile
import soxr

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import blind  # noqa: E402
import context_join as cj  # noqa: E402
import score  # noqa: E402

ROOT = score.ROOT
METHODS = {
    "separate": "Each sentence made on its own, as the blind set's run was, and played as made",
    "whole": "The sentences as one text, in the runtime's own chunks",
    "call": "As a call does it today: sentence 1 alone, then the rest as one piece",
    "overlap": "The user's overlap: sentence 1 alone, then 1 to 5 from sentence 2 on, then 3 to 8 from sentence 6 on",
    "ctx1": "Each sentence with the one before it spoken first and cut away",
    "ctx2": "Each sentence with the two before it spoken first and cut away",
    "window": "Six sentences a pass with the first two cut away: a call's piece, with context",
    "anchor": "Each sentence with sentence 1's audio and text as its prompt, nothing regenerated",
    "anchorgrp": "Four sentences a piece with sentence 1's audio and text as the prompt",
}
SETS = {"kokoro": ["separate", "whole", "call"],
        "phonon": ["separate", "whole", "call", "overlap", "ctx1", "ctx2", "window"],
        "ming": ["separate", "whole", "call", "overlap", "ctx1", "ctx2", "window", "anchor", "anchorgrp"],
        "ming-speaker": ["separate", "whole", "call"],
        "ming-zero": ["separate", "whole", "call"],
        "ming-zero-short": ["call"]}
FIRST_EIGHT = cj.RUN[:8]
# Unbroken takes: clip name, the audio, how many sentences it says, and what it is.
TAKES = [("ming-take-5", "ming-raw/whole5/pass-00.wav", 5, "Five sentences as one take, no joins, through audio.cpp"),
         ("ming-take-8", "own-long/drops/pass-8-00-42.wav", 8, "All eight as one take, through Ming's own Python with its cap raised"),
         ("ming-two-takes", "ming-raw/takes/take.wav", 8, "Two takes, the second going on from the first's last sentence"),
         ("ming-batches", "ming-raw/batches/take.wav", 8, "Three takes split at sentence ends, with tidied joins"),
         ("qwen3-hadley-take-8", "qwen3-hadley/whole8/pass-00.wav", 8, "Qwen3 with Phonon's Hadley as its voice, all eight as one take"),
         ("qwen3-aiden-take-8", "qwen3-aiden/whole8/pass-00.wav", 8, "Qwen3's Aiden, all eight as one take"),
         ("qwen3-hadley-300", "qwen3-hadley/long/long-1-300-42.wav", 0, "Qwen3 with Hadley, a 297-word technical reply as one take"),
         ("qwen3-aiden-300", "qwen3-aiden/long/long-1-300-42.wav", 0, "Qwen3's Aiden, a 297-word technical reply as one take")]


def first_eight(rec, x, rate):
    """The audio up to the end of sentence 8, cut in the pause after it, if there's a sentence 9."""
    y = cj.to16(x, rate)
    words, _ = cj.recognise(rec, y)
    want = cj.expected_words(cj.RUN)
    if len(words) < len(cj.expected_words(FIRST_EIGHT)) + 3:
        return x, None
    truth = cj.gap_truth(cj.RUN, 7, words)
    if truth is None:
        return None, "sentence 8 wasn't found"
    near = [p for p in cj.pauses(x, rate) if p[1] >= truth[0] - 0.1 and p[0] <= truth[1] + 0.1]
    end = (near[0][0] + near[0][1]) / 2 if near else truth[1]
    return x[: int(end * rate)], None


def main():
    out = pathlib.Path(sys.argv[1])
    out.mkdir(parents=True, exist_ok=True)
    only = sys.argv[2:]  # just these models, keeping the clips already listed for the others
    rec = score.recogniser()
    listed = out / "clips.json"
    clips = [c for c in json.loads(listed.read_text()) if c["model"] not in only] if only else []
    with tempfile.TemporaryDirectory() as t:
        tmp = pathlib.Path(t)
        for model, methods in SETS.items():
            if only and model not in only:
                continue
            for method in methods:
                if method == "overlap":
                    x, rate, _ = cj.assemble(rec, model, "overlap")
                else:
                    x, rate = cj.load(ROOT / "runs/context" / model / f"{method}.wav")
                    x, problem = first_eight(rec, x, rate)
                    if x is None:
                        print(model, method, problem)
                        continue
                heard = score.transcribe(rec, cj.to16(x, rate))
                wrong, words = score.wer(" ".join(FIRST_EIGHT), heard)
                audio = soxr.resample(x, rate, blind.RATE) if rate != blind.RATE else x
                name = f"{model}-{method}.m4a"
                m = blind.make(blind.padded(blind.trimmed(audio)), out / name, tmp)
                what = METHODS[method]
                if model == "ming-speaker":
                    what += ", with a stored speaker embedding, which stops Ming losing words"
                if model.startswith("ming-zero"):
                    what += ", with the empty speaker row Ming's README calls for"
                clips.append({"model": model, "method": method, "what": what, "file": name,
                              "seconds": m["seconds"], "words_heard_wrong": wrong, "words": words,
                              "sentences": FIRST_EIGHT})
                print(name, m["seconds"], "wrong", wrong, flush=True)
        if "takes" in only:
            clips = [c for c in clips if c["model"] != "ming-takes"]
            for name, path, n, what in TAKES:
                x, rate = cj.load(ROOT / "runs/context" / path)
                said = FIRST_EIGHT[:n]
                if n:
                    wrong, words = score.wer(" ".join(said), score.transcribe(rec, cj.to16(x, rate)))
                else:
                    wrong, words = None, None  # a long take, scored by context_drops.py
                audio = soxr.resample(x, rate, blind.RATE) if rate != blind.RATE else x
                m = blind.make(blind.padded(blind.trimmed(audio)), out / f"{name}.m4a", tmp)
                clips.append({"model": "ming-takes", "method": name, "what": what, "file": f"{name}.m4a",
                              "seconds": m["seconds"], "words_heard_wrong": wrong, "words": words, "sentences": said})
                print(name, m["seconds"], "wrong", wrong, flush=True)
    (out / "clips.json").write_text(json.dumps(clips, indent=1, ensure_ascii=False) + "\n")


if __name__ == "__main__":
    main()
