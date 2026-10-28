"""The MLX models: Parakeet v2 hears you and Kokoro speaks. Both load once, when the server
starts, and run on one thread of their own.

Sharing that thread means they never run at the same time. Your words wait at most for the
piece of a reply that is being synthesized, which takes under a second (see
tools/tts-speed).
"""

import asyncio
from concurrent.futures import ThreadPoolExecutor

import numpy as np

PARAKEET = "mlx-community/parakeet-tdt-0.6b-v2"
KOKORO = "mlx-community/Kokoro-82M-bf16"
VOICE = "af_heart"
HEARING_RATE = 16000  # Parakeet's sample rate
SPEAKING_RATE = 24000  # Kokoro's sample rate

_thread = ThreadPoolExecutor(max_workers=1, thread_name_prefix="mlx")
_parakeet = None
_kokoro = None


async def run(fn, *args):
    """Runs fn(*args) on the models' thread."""
    return await asyncio.get_running_loop().run_in_executor(_thread, fn, *args)


def load() -> None:
    """Loads both models and runs each once, so the first call doesn't wait for that."""

    def work():
        global _parakeet, _kokoro
        from mlx_audio.stt import load as load_parakeet
        from mlx_audio.tts.utils import load_model as load_kokoro

        _parakeet = load_parakeet(PARAKEET)
        _kokoro = load_kokoro(KOKORO)
        transcribe(np.zeros(HEARING_RATE, dtype=np.float32))
        synthesize("Hello.")

    _thread.submit(work).result()


def transcribe(samples: np.ndarray) -> str:
    """Text of one utterance, given as float samples at HEARING_RATE."""
    import mlx.core as mx

    return _parakeet.generate(mx.array(samples)).text.strip()


def synthesize(text: str, voice: str = VOICE) -> np.ndarray:
    """Float samples at SPEAKING_RATE of text spoken by the voice."""
    parts = [np.array(r.audio, dtype=np.float32) for r in _kokoro.generate(text, voice=voice, lang_code="a")]
    return np.concatenate(parts) if parts else np.zeros(0, dtype=np.float32)
