#!/usr/bin/env python3
"""Is the voice the same from one separately made sentence to the next?

    env/bin/python -I harness/stability.py --models M...

Plain speaker similarity between different sentences doesn't answer it. Measured here with CAM++, a
different Kokoro voice on the same sentence is closer (0.78) than the same voice on another sentence
(0.58): on sentences of two to six seconds, an embedding hears the words more than the speaker. So
the words are taken out. Each sentence's embedding (TitaNet-L), less the same sentence's in a fixed
reference voice, leaves that voice's offset for that sentence, and a steady voice has about the same
offset every time. The score is the mean cosine between those offsets over all pairs of the 24 `run`
sentences: higher is steadier.

Two controls set the scale, and the score means nothing without them: Kokoro voices, steady by
construction; and sentences taken at random from four Kokoro voices, which is what a voice changing
at every sentence would score. The score also rises the further a voice is from the reference, so
small differences between voices mean nothing. Writes ROOT/stability-EMBEDDING.json.
"""
import argparse
import json
import os
import pathlib

import numpy as np
import sherpa_onnx
import soundfile
import soxr

ROOT = pathlib.Path(os.environ.get("TTS_ROOT", "~/ringback-models-test/tts")).expanduser()
HERE = pathlib.Path(__file__).resolve().parent
EMBEDDINGS = {"resnet34": "wespeaker_en_voxceleb_resnet34_LM.onnx", "titanet": "nemo_en_titanet_large.onnx",
              "campplus": "3dspeaker_speech_campplus_sv_en_voxceleb_16k.onnx"}
REFERENCE = "kokoro-bella"
SHUFFLE_VOICES = ["kokoro", "kokoro-sarah", "kokoro-nicole", "kokoro-alloy"]
SHUFFLES = 200


def embed(extractor, model, ids):
    out = []
    for sid in ids:
        x, rate = soundfile.read(ROOT / "runs" / model / "all" / f"{sid}.wav", dtype="float32")
        y = soxr.resample(x, rate, 16000) if rate != 16000 else x
        s = extractor.create_stream()
        s.accept_waveform(16000, y)
        s.input_finished()
        e = np.array(extractor.compute(s))
        out.append(e / np.linalg.norm(e))
    return np.array(out)


def steadiness(e, ref):
    r = e - ref
    r = r / np.linalg.norm(r, axis=1, keepdims=True)
    c = (r @ r.T)[np.triu_indices(len(r), 1)]
    return float(c.mean()), float(np.percentile(c, 10))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--models", nargs="*", default=[])
    ap.add_argument("--embedding", default="titanet", choices=EMBEDDINGS,
                    help="campplus can't tell the controls apart, and resnet34 only just")
    args = ap.parse_args()
    ids = [s["id"] for s in json.loads((HERE / "sentences.json").read_text()) if s["kind"] == "run"]
    extractor = sherpa_onnx.SpeakerEmbeddingExtractor(sherpa_onnx.SpeakerEmbeddingExtractorConfig(
        model=str(ROOT / "models/speaker" / EMBEDDINGS[args.embedding]), num_threads=8, provider="cpu"))
    ref = embed(extractor, REFERENCE, ids)
    voices = {m: embed(extractor, m, ids) for m in SHUFFLE_VOICES}
    steady = {m: steadiness(voices[m], ref)[0] for m in SHUFFLE_VOICES}
    rng = np.random.default_rng(1)
    mixed = []
    for _ in range(SHUFFLES):
        pick = rng.integers(len(SHUFFLE_VOICES), size=len(ids))
        mixed.append(steadiness(np.array([voices[SHUFFLE_VOICES[p]][i] for i, p in enumerate(pick)]), ref)[0])
    out = {"embedding": args.embedding, "reference": REFERENCE,
           "control_steady": steady, "control_changing_voice_mean": float(np.mean(mixed)),
           "control_changing_voice_p95": float(np.percentile(mixed, 95)), "models": {}}
    print(json.dumps({k: v for k, v in out.items() if k != "models"}))
    for m in args.models:
        mean, p10 = steadiness(embed(extractor, m, ids), ref)
        out["models"][m] = {"steadiness": mean, "p10": p10}
    (ROOT / f"stability-{args.embedding}.json").write_text(json.dumps(out, indent=1))


main()
