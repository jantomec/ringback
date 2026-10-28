#!/usr/bin/env python3
"""Time Pipecat's own Kokoro service (kokoro-onnx) inside a Pipecat pipeline.

    uv run --no-project --with 'pipecat-ai[kokoro]==1.14.0' tools/tts-speed/pipecat_kokoro.py [OUT_DIR]

kokoro-onnx runs on the CPU unless ONNX_PROVIDER names another onnxruntime provider, e.g.
CoreMLExecutionProvider. Each text is queued as a TTSSpeakFrame and timed to its first and
last TTSAudioRawFrame. With OUT_DIR, the long reply is saved there as a WAV.
"""

import asyncio
import os
import statistics
import sys
import time
import wave
from pathlib import Path

from loguru import logger
from pipecat.frames.frames import EndFrame, TTSAudioRawFrame, TTSSpeakFrame, TTSStoppedFrame
from pipecat.pipeline.pipeline import Pipeline
from pipecat.pipeline.runner import PipelineRunner
from pipecat.pipeline.task import PipelineParams, PipelineTask
from pipecat.processors.frame_processor import FrameDirection, FrameProcessor
from pipecat.services.kokoro.tts import KokoroTTSService

from texts import RUNS, TEXTS

logger.remove()
logger.add(sys.stderr, level="WARNING")
PROVIDER = os.getenv("ONNX_PROVIDER", "CPUExecutionProvider")
LABEL = "onnx-" + PROVIDER.removesuffix("ExecutionProvider").lower()
OUT = Path(sys.argv[1]) if len(sys.argv) > 1 else None
SR = 24000


class Sink(FrameProcessor):
    """Notes when each reply's audio arrives."""

    def __init__(self):
        super().__init__()
        self.reset()

    def reset(self):
        self.first = self.last = None
        self.audio = bytearray()
        self.done = asyncio.Event()

    async def process_frame(self, frame, direction: FrameDirection):
        await super().process_frame(frame, direction)
        if isinstance(frame, TTSAudioRawFrame):
            now = time.perf_counter()
            self.first = self.first or now
            self.last = now
            self.audio += frame.audio
        elif isinstance(frame, TTSStoppedFrame):
            self.done.set()
        await self.push_frame(frame, direction)


async def main():
    t = time.perf_counter()
    tts = KokoroTTSService(settings=KokoroTTSService.Settings(voice="af_heart"))
    print(f"{LABEL}: load {time.perf_counter() - t:.2f} s", flush=True)
    sink = Sink()
    task = PipelineTask(Pipeline([tts, sink]), params=PipelineParams(audio_out_sample_rate=SR))
    runner = asyncio.create_task(PipelineRunner(handle_sigint=False).run(task))
    await asyncio.sleep(0.5)

    for name, text in TEXTS.items():
        firsts, totals, failed, audio = [], [], 0, b""
        for i in range(RUNS + 1):  # run 0 is the warm-up
            sink.reset()
            t0 = time.perf_counter()
            await task.queue_frame(TTSSpeakFrame(text))
            # A request that makes no audio may never send TTSStoppedFrame (seen with CoreML).
            try:
                await asyncio.wait_for(sink.done.wait(), 15)
            except TimeoutError:
                pass
            if not sink.audio:
                failed += 1
            elif i:
                audio = bytes(sink.audio)
                firsts.append(sink.first - t0)
                totals.append(sink.last - t0)
        if not totals:
            print(f"{LABEL} {name:6} no audio in any run", flush=True)
            continue
        dur = len(audio) / 2 / SR
        print(
            f"{LABEL} {name:6} audio {dur:5.2f} s | first audio median {statistics.median(firsts):.3f} s "
            f"(min {min(firsts):.3f}, max {max(firsts):.3f}) | all audio median {statistics.median(totals):.3f} s "
            f"(min {min(totals):.3f}, max {max(totals):.3f}) | {dur / statistics.median(totals):.0f}x real time"
            f" | runs with no audio {failed}",
            flush=True,
        )
        if name == "long" and OUT:
            OUT.mkdir(parents=True, exist_ok=True)
            with wave.open(str(OUT / f"{LABEL}-long.wav"), "wb") as w:
                w.setnchannels(1), w.setsampwidth(2), w.setframerate(SR)
                w.writeframes(audio)

    await task.queue_frame(EndFrame())
    await runner


asyncio.run(main())
