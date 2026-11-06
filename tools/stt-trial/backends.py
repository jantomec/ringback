"""How each model is run. A backend is a callable from float samples at 16 kHz to text; the
imports are inside, because each runs in its own environment (node_setup.sh)."""

import os
import struct
import subprocess
from pathlib import Path

import numpy as np
import soundfile as sf

STT = Path(os.environ.get("STT", "~/ringback-models-test/stt")).expanduser()
NODE_MODELS = Path(os.environ.get("NODE_MODELS", "~/ringback-speech/models")).expanduser()
MAX_NEW_TOKENS = 256  # a stretch on a call is seconds long, so this only stops runaway text


class Sherpa:
    """sherpa_run, the node's own settings: two threads, greedy, CUDA."""

    repo = "sherpa-onnx v2.0.1"

    def __init__(self, kind, folder, files):
        env = dict(os.environ)
        site = next((STT / "envs/sherpa/lib").glob("python*/site-packages"))
        libs = [next((STT / "sherpa").glob("sherpa-onnx-*/lib")), site / "nvidia/cu13/lib", site / "nvidia/cudnn/lib"]
        env["LD_LIBRARY_PATH"] = ":".join(map(str, libs))
        self.p = subprocess.Popen([str(STT / "bin/sherpa_run"), kind, str(folder), *files],
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE, env=env, text=False)  # fmt: skip
        assert self.p.stdout.readline().strip() == b"ready", "sherpa_run didn't start"
        self.pids = [self.p.pid]

    def __call__(self, samples):
        x = np.asarray(samples, dtype=np.float32)
        self.p.stdin.write(struct.pack("<I", len(x)) + x.tobytes())
        self.p.stdin.flush()
        return self.p.stdout.readline().decode().strip()

    def close(self):
        self.p.stdin.close()
        self.p.wait()


class Qwen3:
    def __init__(self, repo):
        import torch
        from qwen_asr import Qwen3ASRModel

        self.repo = repo
        self.m = Qwen3ASRModel.from_pretrained(repo, dtype=torch.bfloat16, device_map="cuda:0",
                                               max_new_tokens=MAX_NEW_TOKENS)  # fmt: skip

    def __call__(self, samples):
        return self.m.transcribe(audio=(samples, 16000), language="English")[0].text


class CanaryQwen:
    repo = "nvidia/canary-qwen-2.5b"

    def __init__(self, half=True):
        import torch
        from nemo.collections.speechlm2.models import SALM

        self.torch = torch
        m = SALM.from_pretrained(self.repo)
        self.m = (m.bfloat16() if half else m).eval().to("cuda")
        self.path = f"/dev/shm/stt-trial-{os.getpid()}.wav"

    def __call__(self, samples):
        sf.write(self.path, samples, 16000, subtype="FLOAT")
        with self.torch.inference_mode():
            ids = self.m.generate(
                prompts=[[{"role": "user", "content": f"Transcribe the following: {self.m.audio_locator_tag}", "audio": [self.path]}]],
                max_new_tokens=MAX_NEW_TOKENS,
            )
        return self.m.tokenizer.ids_to_text(ids[0].cpu())

    def close(self):
        Path(self.path).unlink(missing_ok=True)


class ParakeetNemo:
    """NVIDIA's own runtime for Parakeet v2, to check the baseline against: not a candidate."""

    repo = "nvidia/parakeet-tdt-0.6b-v2"

    def __init__(self):
        import nemo.collections.asr as nemo_asr

        self.m = nemo_asr.models.ASRModel.from_pretrained(self.repo).eval().to("cuda")

    def __call__(self, samples):
        return self.m.transcribe([samples], batch_size=1, verbose=False)[0].text


class Granite:
    repo = "ibm-granite/granite-speech-5.0-2b"
    punctuated = "transcribe the speech with proper punctuation and capitalization."
    english = "The speech is in English. Transcribe it exactly; do not translate. can you transcribe the speech into a written format?"

    def __init__(self, prompt=english):
        import torch
        from transformers import AutoModelForSpeechSeq2Seq, AutoProcessor

        self.torch = torch
        self.p = AutoProcessor.from_pretrained(self.repo)
        self.m = AutoModelForSpeechSeq2Seq.from_pretrained(self.repo, device_map="cuda", torch_dtype=torch.bfloat16)
        chat = [{"role": "user", "content": "<|audio|>" + prompt}]
        self.text = self.p.tokenizer.apply_chat_template(chat, tokenize=False, add_generation_prompt=True)

    def __call__(self, samples):
        wav = self.torch.from_numpy(samples).unsqueeze(0)
        x = self.p(self.text, wav, device="cuda", return_tensors="pt").to("cuda")
        with self.torch.inference_mode():
            out = self.m.generate(**x, max_new_tokens=MAX_NEW_TOKENS, do_sample=False, num_beams=1)
        new = out[0, x["input_ids"].shape[-1] :].unsqueeze(0)
        return self.p.tokenizer.batch_decode(new, add_special_tokens=False, skip_special_tokens=True)[0]


v2 = NODE_MODELS / "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16"
v2_int8 = NODE_MODELS / "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8"
transducer_fp16 = ["encoder.fp16.onnx", "decoder.fp16.onnx", "joiner.fp16.onnx"]
transducer_int8 = ["encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx"]

MODELS = {
    "parakeet-v2": lambda: Sherpa("transducer", v2, transducer_fp16),
    "parakeet-v2-nemo": ParakeetNemo,
    "parakeet-v2-int8": lambda: Sherpa("transducer", v2_int8, transducer_int8),
    "parakeet-ultra-int8": lambda: Sherpa("transducer", STT / "models/sherpa-onnx-nemo-parakeet-tdt-0.6b-ultra-int8", transducer_int8),
    "cohere-int8": lambda: Sherpa("cohere", STT / "models/sherpa-onnx-cohere-transcribe-14-lang-int8-2026-04-01", ["encoder.int8.onnx", "decoder.int8.onnx"]),
    "qwen3-1.7b": lambda: Qwen3("Qwen/Qwen3-ASR-1.7B"),
    "canary-qwen": CanaryQwen,
    "canary-qwen-float32": lambda: CanaryQwen(half=False),
    "granite-5.0-2b": Granite,  # the card's language-pinned prompt
    "granite-5.0-2b-unpinned": lambda: Granite(Granite.punctuated),
}
