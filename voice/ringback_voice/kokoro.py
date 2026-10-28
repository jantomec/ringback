"""Speech with Kokoro-82M through MLX, the `af_heart` voice.

Pipecat's own Kokoro service runs kokoro-onnx, about 3× slower on a Mac (see
tools/tts-speed). Each piece of a reply (see speech.py) is synthesized whole, in under a
second, and sent on as one block of audio.
"""

import time
from collections.abc import AsyncGenerator, Callable

import numpy as np
from pipecat.frames.frames import Frame, TTSAudioRawFrame
from pipecat.services.settings import TTSSettings
from pipecat.services.tts_service import TTSService
from pipecat.transcriptions.language import Language

from ringback_voice import models
from ringback_voice.speech import Timeline


class KokoroTTSService(TTSService):
    def __init__(self, timeline: Timeline, log: Callable[[str], None], **kwargs):
        super().__init__(
            push_start_frame=True,
            push_stop_frames=True,
            sample_rate=models.SPEAKING_RATE,
            settings=TTSSettings(model=models.KOKORO, voice=models.VOICE, language=Language.EN),
            **kwargs,
        )
        self._timeline = timeline
        self._log = log

    def can_generate_metrics(self) -> bool:
        return True

    async def run_tts(self, text: str, context_id: str) -> AsyncGenerator[Frame | None, None]:
        await self.start_tts_usage_metrics(text)
        reply = self._timeline.reply
        t = time.monotonic()
        samples = await models.run(models.synthesize, text)
        await self.stop_ttfb_metrics()
        if self._timeline.reply != reply:  # interrupted meanwhile
            return
        secs = len(samples) / models.SPEAKING_RATE
        self._timeline.add(text, secs)
        self._log(f"voice ({secs:.1f} s, made in {time.monotonic() - t:.2f} s): {text!r}")
        audio = (np.clip(samples, -1, 1) * 32767).astype(np.int16).tobytes()
        yield TTSAudioRawFrame(
            audio=audio, sample_rate=models.SPEAKING_RATE, num_channels=1, context_id=context_id
        )
