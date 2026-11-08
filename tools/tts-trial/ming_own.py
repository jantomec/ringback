#!/usr/bin/env python3
"""Ming through its own Python, to tell what's Ming's from what's audio.cpp's or ours. The built-in
voice as its README calls it, on the passes of context_make.py's `drops`, so context_drops.py
scores them the same way. --runs makes longer single passes from the first sentence.

    sh harness/gate.sh ming-py/bin/python -I harness/ming_own.py LABEL [--raw] [--no-slot]
                                          [--cache N] [--runs 8,12,24]
                                          [--texts FILE --seeds 42,7] [--language English] [--prep]
                                          [--speaker WAV] [--clone WAV]

Needs setup.sh's Ming checkout in runtime/ming-src, and its weights in models/ming-py. --cache
raises MAX_CACHE_LEN, Ming's cap of 512 positions on prompt and speech together, which only sets
how many steps it will take: the backbone allows 32768. Writes ROOT/runs/context/LABEL/drops/.

--texts takes a JSON list of texts in place of the passes. --prep sends the text through Ming's own
text path in one piece, --speaker conditions on a clip's speaker embedding in place of the empty row,
and --clone is Ming's cloning call, with WAV and the transcript beside it as WAV.txt. Each row says
how the pass ended: `stopped` if Ming chose to, and how many positions of the cache it used.
"""
import argparse
import json
import os
import pathlib
import sys
import time

ROOT = pathlib.Path(os.environ.get("TTS_ROOT", "~/ringback-models-test/tts")).expanduser()
sys.path.insert(0, str(ROOT / "runtime/ming-src"))
os.chdir(ROOT / "runtime/ming-src")

import numpy as np  # noqa: E402
import soundfile  # noqa: E402
import torch  # noqa: E402

import modeling_bailingmm  # noqa: E402
from ming_tts import MingTTS  # noqa: E402

HERE = pathlib.Path(__file__).resolve().parent
RUN = [s["text"] for s in json.loads((HERE / "sentences.json").read_text()) if s["kind"] == "run"]
SEEDS = (42, 7, 1895)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("label")
    ap.add_argument("--raw", action="store_true", help="one piece of text, as Ming's benchmarks were run")
    ap.add_argument("--no-slot", action="store_true", help="leave out the empty speaker row, as our first test did")
    ap.add_argument("--cache", type=int, default=512)
    ap.add_argument("--runs", help="instead of the drops passes: these lengths, from sentence 1")
    ap.add_argument("--texts", help="a JSON list of texts, in place of the passes")
    ap.add_argument("--seeds", default=",".join(str(v) for v in SEEDS))
    ap.add_argument("--language", help="language_id, as Ming's Quick Start passes it")
    ap.add_argument("--prep", action="store_true", help="Ming's text path, in one piece")
    ap.add_argument("--speaker", help="a clip's speaker embedding, in place of the empty row")
    ap.add_argument("--clone", help="Ming's cloning call, with this clip and its transcript")
    args = ap.parse_args()
    modeling_bailingmm.MAX_CACHE_LEN = args.cache
    out = ROOT / "runs/context" / args.label / "drops"
    out.mkdir(parents=True, exist_ok=True)
    tts = MingTTS(str(ROOT / "models/ming-py"), device="cuda:0", text_normalization=False)
    seen = {}
    sample, prepare = tts.model.sample, tts.model.prepare_input_embed

    def counted_sample(*a, **k):
        seen.update(steps=0, stopped=False)
        for latent, last in sample(*a, **k):
            seen["steps"] += 1
            seen["stopped"] = seen["stopped"] or bool(last)
            yield latent, last

    def counted_prepare(*a, **k):
        out = prepare(*a, **k)
        seen["prompt"] = out[1].shape[1]
        return out

    tts.model.sample, tts.model.prepare_input_embed = counted_sample, counted_prepare
    if args.texts:
        texts = json.loads(pathlib.Path(args.texts).read_text())
        grid = [(i + 1, 0, t) for i, t in enumerate(texts)]
    elif args.runs:
        grid = [(int(n), 0, " ".join(RUN[: int(n)])) for n in args.runs.split(",")]
    else:
        grid = [(size, begin, " ".join(RUN[begin: begin + size])) for size in (1, 2, 3, 4, 6) for begin in (1, 6, 12, 18)]
    call = {"task": "IP", "controls": {"ip": "US_Emma"}, "use_zero_spk_emb": not args.no_slot}
    if args.speaker:
        call = {"task": "IP", "controls": {"ip": "US_Emma"}, "prompt_wav_path": args.speaker, "use_spk_emb": True}
    if args.clone:
        call = {"task": "TTS", "prompt_wav_path": args.clone, "use_spk_emb": True,
                "prompt_text": pathlib.Path(args.clone + ".txt").read_text().strip()}
    if args.language:
        call["language_id"] = args.language
    rows = []
    for size, begin, text in grid:
        said = list(range(begin, begin + size))
        for seed in (int(v) for v in args.seeds.split(",")):
            torch.manual_seed(seed)
            np.random.seed(seed)
            t0 = time.time()
            wav = tts.synthesize(text=text, raw_text=args.raw, max_decode_steps=args.cache,
                                 max_length=100000 if args.prep else 24, **call)
            wall = time.time() - t0
            x = wav.squeeze().numpy()
            name = f"pass-{size}-{begin:02d}-{seed}"
            soundfile.write(out / f"{name}.wav", x, tts.sample_rate, subtype="PCM_16")
            rows.append({"name": name, "size": size, "seed": seed, "said": said, "text": text,
                         "audio_s": len(x) / tts.sample_rate, "wall_s": wall, **seen})
            print(name, f"{rows[-1]['audio_s']:.1f} s made in {wall:.1f} s, positions {seen.get('prompt')} + "
                        f"{seen['steps']} of {args.cache}, {'stopped' if seen['stopped'] else 'ran out'}", flush=True)
    (out / "passes.json").write_text(json.dumps(rows, indent=0))


main()
