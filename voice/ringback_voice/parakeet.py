"""Speech-to-text with Parakeet v2, one utterance at a time.

Pipecat has no Parakeet service. Pipecat's voice-activity detection cuts your speech into
utterances, and each is transcribed once it ends, which takes about 40 ms (see
tools/stt-eval).
"""

import time
from collections.abc import AsyncGenerator, Callable

import numpy as np
from pipecat.frames.frames import Frame, TranscriptionFrame
from pipecat.services.settings import STTSettings
from pipecat.services.stt_service import SegmentedSTTService
from pipecat.transcriptions.language import Language
from pipecat.utils.time import time_now_iso8601

from ringback_voice import models


class ParakeetSTTService(SegmentedSTTService):
    def __init__(self, log: Callable[[str], None], **kwargs):
        super().__init__(
            sample_rate=models.HEARING_RATE,
            settings=STTSettings(model=models.PARAKEET, language=Language.EN),
            **kwargs,
        )
        self._log = log

    @property
    def wants_wav_segments(self) -> bool:
        return False

    def can_generate_metrics(self) -> bool:
        return True

    async def run_stt(self, audio: bytes) -> AsyncGenerator[Frame | None, None]:
        await self.start_processing_metrics()
        t = time.monotonic()
        samples = np.frombuffer(audio, dtype=np.int16).astype(np.float32) / 32768
        text = await models.run(models.transcribe, samples)
        await self.stop_processing_metrics()
        self._log(f"you ({len(samples) / models.HEARING_RATE:.1f} s, heard in {time.monotonic() - t:.2f} s): {text!r}")
        if text:
            yield TranscriptionFrame(text, self._user_id, time_now_iso8601())
