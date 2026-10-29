"""ringback-voice serve --listen HOST:PORT [--report FILE]

The voice side of a call, on the Mac whose session is calling. When you answer, the phone
posts its WebRTC offer here over Nebula (on mobile data only the phone can open a tunnel).

The call is a conversation: Parakeet v2 hears you, Haiku answers in a Claude Code session
that stays open for the call, and Kokoro speaks the answer in growing pieces. You can
interrupt it at any time, and only your voice stops it: the phone sends the voice sample you
recorded in the app, and your interruption sensitivity. Until sessions call (Stage 3), the
voice talks about the report in FILE, which is read again for every call.
"""

import argparse
import asyncio
import sys
import time
from collections.abc import Callable
from datetime import datetime
from pathlib import Path

import numpy as np
from aiohttp import web
from loguru import logger
from pipecat.audio.turn.smart_turn.base_smart_turn import SmartTurnParams
from pipecat.audio.turn.smart_turn.local_smart_turn_v3 import LocalSmartTurnAnalyzerV3
from pipecat.audio.vad.silero import SileroVADAnalyzer
from pipecat.frames.frames import LLMRunFrame
from pipecat.observers.user_bot_latency_observer import UserBotLatencyObserver
from pipecat.pipeline.pipeline import Pipeline
from pipecat.pipeline.runner import PipelineRunner
from pipecat.pipeline.task import PipelineParams, PipelineTask
from pipecat.processors.aggregators.llm_context import LLMContext
from pipecat.processors.aggregators.llm_response_universal import (
    LLMContextAggregatorPair,
    LLMUserAggregatorParams,
)
from pipecat.transports.base_transport import BaseTransport, TransportParams
from pipecat.transports.smallwebrtc.connection import SmallWebRTCConnection
from pipecat.transports.smallwebrtc.transport import SmallWebRTCTransport
from pipecat.turns.empty_user_turn import EmptyUserTurnConfig
from pipecat.turns.user_stop import TurnAnalyzerUserTurnStopStrategy
from pipecat.turns.user_turn_strategies import UserTurnStrategies

from ringback_voice import hearing, models
from ringback_voice.claude_code import ClaudeCodeLLMService
from ringback_voice.instructions import instructions
from ringback_voice.kokoro import KokoroTTSService
from ringback_voice.parakeet import ParakeetSTTService
from ringback_voice.speech import Pieces, Timeline

# Sent to Haiku when something stopped the voice but no words were recognised (see
# "Interruptions" in docs/design.md).
NOISE = (
    "Something stopped you, but no words were recognised, so it was probably noise. Carry on "
    "from the start of the sentence you were in."
)
# Seconds of silence after which your turn ends even if Pipecat's end-of-turn model (Smart
# Turn) thinks you haven't finished, as it sometimes does with a short "mm-hmm". Pipecat's
# 3 s made such turns wait; 1.5 s is twice the longest pause in five minutes of free speech,
# apart from one long hesitation (see Stage 2 in docs/design.md).
TURN_WAIT = 1.5


def log(line: str) -> None:
    print(f"{datetime.now():%H:%M:%S.%f}"[:-3] + f"  {line}", flush=True)


def call_task(
    transport: BaseTransport,
    report: str | None,
    say: Callable[[str], None],
    turn_wait: float = TURN_WAIT,
    sensitivity: float = hearing.SENSITIVITY,
) -> PipelineTask:
    """The conversation, between transport's input and output. The transport's input should
    filter your voice (hearing.your_voice)."""
    timeline = Timeline()
    end_of_turn = LocalSmartTurnAnalyzerV3(params=SmartTurnParams(stop_secs=turn_wait))
    vad = hearing.thresholds(sensitivity)
    say(f"sensitivity {sensitivity:.2f}: {hearing.describe(vad)}")
    user, assistant = LLMContextAggregatorPair(
        LLMContext(),
        user_params=LLMUserAggregatorParams(
            vad_analyzer=SileroVADAnalyzer(params=vad),
            user_turn_strategies=UserTurnStrategies(stop=[TurnAnalyzerUserTurnStopStrategy(turn_analyzer=end_of_turn)]),
            empty_user_turn=EmptyUserTurnConfig(interrupted_prompt=NOISE),
        ),
    )
    pipeline = Pipeline([
        transport.input(),
        ParakeetSTTService(say),
        user,
        ClaudeCodeLLMService(instructions(report), timeline, say),
        Pieces(timeline, say),
        KokoroTTSService(timeline, say),
        transport.output(),
        assistant,
    ])  # fmt: skip
    latency = UserBotLatencyObserver()
    task = PipelineTask(
        pipeline,
        params=PipelineParams(
            audio_in_sample_rate=models.HEARING_RATE, audio_out_sample_rate=models.SPEAKING_RATE, enable_metrics=True
        ),
        observers=[latency],
    )

    @latency.event_handler("on_first_bot_speech_latency")
    async def first_words(observer, secs):
        say(f"voice started {secs:.2f} s after connecting")

    @latency.event_handler("on_latency_measured")
    async def replied(observer, secs):
        say(f"voice answered {secs:.2f} s after you stopped speaking")

    return task


async def run_call(
    conn: SmallWebRTCConnection,
    call: str,
    t0: float,
    report: Path | None,
    sample: np.ndarray | None,
    sensitivity: float,
) -> None:
    say = lambda line: log(f"call {call}: {line}")
    since = lambda: f"{time.monotonic() - t0:.2f} s after the offer"
    text = None
    if report:
        try:
            text = report.read_text()
        except OSError as e:
            say(f"no report ({e}), using the stand-in")
    say("VoxLock: " + ("your voice sample" if sample is not None else "learns your voice from your first 3 s of speech"))
    transport = SmallWebRTCTransport(
        webrtc_connection=conn,
        params=TransportParams(
            audio_in_enabled=True, audio_out_enabled=True, audio_in_filter=hearing.your_voice(sample)
        ),
    )
    task = call_task(transport, text, say, sensitivity=sensitivity)

    @transport.event_handler("on_client_connected")
    async def connected(transport, connection):
        say(f"connected, {since()}")
        await task.queue_frames([LLMRunFrame()])

    @transport.event_handler("on_client_disconnected")
    async def disconnected(transport, connection):
        say("disconnected")
        await task.cancel()

    await PipelineRunner(handle_sigint=False).run(task)
    say("finished")


def main() -> None:
    p = argparse.ArgumentParser(prog="ringback-voice")
    sub = p.add_subparsers(dest="command", required=True)
    serve = sub.add_parser("serve", help="talk to the phone during calls")
    serve.add_argument("--listen", required=True, help="this Mac's Nebula address and port, e.g. 10.0.0.1:8788")
    serve.add_argument("--report", type=Path, help="what the voice reports, read for every call (until Stage 3)")
    args = p.parse_args()

    logger.remove()
    logger.add(sys.stderr, level="WARNING")

    async def offer(request: web.Request) -> web.Response:
        body = await request.json()
        t0 = time.monotonic()
        call = str(body.get("call_id", "?"))[:8]
        sample = None
        if body.get("voice_sample"):
            try:
                sample = hearing.decode_sample(body["voice_sample"])
            except ValueError as e:
                log(f"call {call}: no voice sample ({e})")
        try:
            sensitivity = float(body.get("sensitivity", hearing.SENSITIVITY))
        except (TypeError, ValueError):
            sensitivity = hearing.SENSITIVITY
        if not 0 <= sensitivity <= 1:  # also NaN
            sensitivity = hearing.SENSITIVITY
        conn = SmallWebRTCConnection()
        await conn.initialize(sdp=body["sdp"], type=body["type"])
        asyncio.create_task(run_call(conn, call, t0, args.report, sample, sensitivity))
        log(f"call {call}: offer answered in {time.monotonic() - t0:.2f} s")
        return web.json_response(conn.get_answer())

    t = time.monotonic()
    log("loading Parakeet, Kokoro and VoxLock")
    models.load()
    hearing.load()
    host, port = args.listen.rsplit(":", 1)
    app = web.Application()
    app.add_routes([web.post("/offer", offer)])
    log(f"voice listening on {args.listen}, models loaded in {time.monotonic() - t:.0f} s")
    web.run_app(app, host=host, port=int(port), print=None)


if __name__ == "__main__":
    main()
