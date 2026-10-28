"""ringback-voice serve --listen HOST:PORT

The voice side of a call, on the Mac whose session is calling. When you answer, the phone
posts its WebRTC offer here over Nebula (on mobile data only the phone can open a tunnel).

Stage 1b: the call greets you, then plays back what you say a second later, which shows
that audio flows both ways. Stage 2 replaces that with the conversation.
"""

import argparse
import asyncio
import sys
import time
import wave
from datetime import datetime
from pathlib import Path

from aiohttp import web
from loguru import logger
from pipecat.frames.frames import InputAudioRawFrame, OutputAudioRawFrame
from pipecat.pipeline.pipeline import Pipeline
from pipecat.pipeline.runner import PipelineRunner
from pipecat.pipeline.task import PipelineTask
from pipecat.processors.frame_processor import FrameDirection, FrameProcessor
from pipecat.transports.base_transport import TransportParams
from pipecat.transports.smallwebrtc.connection import SmallWebRTCConnection
from pipecat.transports.smallwebrtc.transport import SmallWebRTCTransport

GREETING = Path("~/.local/state/ringback/greeting.wav").expanduser()
ECHO_DELAY = 1.0  # seconds


def log(line: str) -> None:
    print(f"{datetime.now():%H:%M:%S.%f}"[:-3] + f"  {line}", flush=True)


class DelayedEcho(FrameProcessor):
    """Plays back what the phone sends, ECHO_DELAY seconds later."""

    def __init__(self, on_first_audio):
        super().__init__()
        self._on_first_audio = on_first_audio

    async def process_frame(self, frame, direction: FrameDirection):
        await super().process_frame(frame, direction)
        if isinstance(frame, InputAudioRawFrame):
            if self._on_first_audio:
                self._on_first_audio()
                self._on_first_audio = None
            echo = OutputAudioRawFrame(frame.audio, frame.sample_rate, frame.num_channels)
            asyncio.get_running_loop().call_later(ECHO_DELAY, lambda: asyncio.ensure_future(self.push_frame(echo)))
        else:
            await self.push_frame(frame, direction)


def greeting_frame() -> OutputAudioRawFrame | None:
    if not GREETING.exists():
        return None
    with wave.open(str(GREETING)) as w:
        return OutputAudioRawFrame(w.readframes(w.getnframes()), w.getframerate(), w.getnchannels())


async def run_call(conn: SmallWebRTCConnection, call: str, t0: float) -> None:
    since = lambda: f"{time.monotonic() - t0:.2f} s after the offer"
    transport = SmallWebRTCTransport(
        webrtc_connection=conn, params=TransportParams(audio_in_enabled=True, audio_out_enabled=True)
    )
    echo = DelayedEcho(on_first_audio=lambda: log(f"call {call}: first audio from the phone, {since()}"))
    task = PipelineTask(Pipeline([transport.input(), echo, transport.output()]))

    @transport.event_handler("on_client_connected")
    async def connected(transport, connection):
        log(f"call {call}: connected, {since()}")
        if frame := greeting_frame():
            await task.queue_frames([frame])

    @transport.event_handler("on_client_disconnected")
    async def disconnected(transport, connection):
        log(f"call {call}: disconnected")
        await task.cancel()

    await PipelineRunner(handle_sigint=False).run(task)
    log(f"call {call}: finished")


async def offer(request: web.Request) -> web.Response:
    body = await request.json()
    t0 = time.monotonic()
    call = str(body.get("call_id", "?"))[:8]
    conn = SmallWebRTCConnection()
    await conn.initialize(sdp=body["sdp"], type=body["type"])
    asyncio.create_task(run_call(conn, call, t0))
    log(f"call {call}: offer answered in {time.monotonic() - t0:.2f} s")
    return web.json_response(conn.get_answer())


def main() -> None:
    p = argparse.ArgumentParser(prog="ringback-voice")
    sub = p.add_subparsers(dest="command", required=True)
    serve = sub.add_parser("serve", help="take calls' audio from the phone")
    serve.add_argument("--listen", required=True, help="this Mac's Nebula address and port, e.g. 10.0.0.1:8788")
    args = p.parse_args()

    logger.remove()
    logger.add(sys.stderr, level="WARNING")
    host, port = args.listen.rsplit(":", 1)
    app = web.Application()
    app.add_routes([web.post("/offer", offer)])
    log(f"voice listening on {args.listen}" + ("" if GREETING.exists() else f" (no greeting at {GREETING})"))
    web.run_app(app, host=host, port=int(port), print=None)


if __name__ == "__main__":
    main()
