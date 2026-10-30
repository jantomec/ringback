"""ringback-voice serve --listen HOST:PORT [--switchboard HOST:PORT] [--report FILE] [--socket PATH]

The voice side of a call, on the Mac whose session is calling. A session's ringback mod
gives it a briefing over a Unix socket (sessions.py, at PATH), and it asks the switchboard
to ring the phone, while Haiku thinks of what to say. The phone posts its WebRTC offer here
over Nebula (on mobile data only the phone can open a tunnel), with the call's ID: while it
rings, marked "ringing", and then it posts to /answered when you answer; or, from an older
app, once you've answered. The voice opens when you've answered and audio is connected. A
new offer for the same call replaces the earlier one, as when an offer made while ringing
didn't connect. An offer marked "back" calls back a call you didn't answer, from the phone's
call history: it's answered at once, and the voice picks up.

The call is a conversation: Parakeet v2 hears you, Haiku answers in a Claude Code session
that stays open for the call (Opus takes it over if you ask for a better model), and Kokoro
speaks the answer in growing pieces. You can interrupt it at any time; the phone sends how
easily you do (your interruption sensitivity) and how much quieter background to ignore
(the background filter). A call rung by hand (`ringback-switchboard ring --voice`) talks
about the report in FILE, which is read again for every call.
"""

import argparse
import asyncio
import ipaddress
import sys
import time
from collections.abc import Callable
from datetime import datetime
from pathlib import Path

from aiohttp import web
from loguru import logger
from pipecat.audio.turn.smart_turn.base_smart_turn import SmartTurnParams
from pipecat.audio.turn.smart_turn.local_smart_turn_v3 import LocalSmartTurnAnalyzerV3
from pipecat.audio.vad.silero import SileroVADAnalyzer
from pipecat.frames.frames import BotStartedSpeakingFrame, LLMMessagesAppendFrame, LLMRunFrame
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

from ringback_voice import hearing, models, opus, sessions
from ringback_voice.claude_code import ClaudeCodeLLMService
from ringback_voice.haiku import Haiku
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


class Offer:
    """An offer the phone made for a call, while its pipeline runs."""

    def __init__(self):
        self.answered: asyncio.Future[float] = asyncio.get_running_loop().create_future()  # when you answered
        self.replaced = asyncio.Event()  # a new offer for the same call replaces this one
        self.ended = asyncio.Event()  # its pipeline has ended


class HeldConnection(SmallWebRTCConnection):
    """The call's WebRTC connection, whose voice sends nothing until you've answered. Made
    while the phone rings, it would otherwise send silence from the start, which the phone
    queues until you answer and then plays first: the voice started about 2 s late and
    garbled on the first call that connected while ringing (Stage 3)."""

    def __init__(self, answered: asyncio.Future):
        super().__init__()
        self._answered = answered

    def replace_audio_track(self, track):
        recv = track.recv

        async def held():
            if not self._answered.done():
                await asyncio.shield(self._answered)
                # Pipecat paces its frames from when the track was made: start that clock
                # now, or the time held would go out at once.
                track._start = time.time()
            return await recv()

        track.recv = held
        super().replace_audio_track(track)


def call_task(
    transport: BaseTransport,
    report: str | None,
    say: Callable[[str], None],
    turn_wait: float = TURN_WAIT,
    sensitivity: float = hearing.SENSITIVITY,
    gate: float = hearing.GATE,
    link: sessions.Link | None = None,
    haiku: Haiku | None = None,
) -> PipelineTask:
    """The conversation, between transport's input and output. With a link, the voice can
    pass messages to the session, and the session's news reaches the call. Haiku is the
    session the call started already, if it did."""
    timeline = Timeline()
    end_of_turn = LocalSmartTurnAnalyzerV3(params=SmartTurnParams(stop_secs=turn_wait))
    vad = hearing.thresholds(sensitivity, gate)
    say(f"sensitivity {sensitivity:.2f}, background filter {gate:.2f}: {hearing.describe(vad)}")
    user, assistant = LLMContextAggregatorPair(
        LLMContext(),
        user_params=LLMUserAggregatorParams(
            vad_analyzer=SileroVADAnalyzer(params=vad),
            user_turn_strategies=UserTurnStrategies(stop=[TurnAnalyzerUserTurnStopStrategy(turn_analyzer=end_of_turn)]),
            empty_user_turn=EmptyUserTurnConfig(interrupted_prompt=NOISE),
        ),
    )

    async def tell(text: str) -> None:
        # Through the assistant's side, Pipecat holds a note until the voice has finished
        # speaking and you aren't, then runs Haiku on it.
        await assistant.queue_frame(LLMMessagesAppendFrame([{"role": "developer", "content": text}], run_llm=True))

    pipeline = Pipeline([
        transport.input(),
        ParakeetSTTService(say),
        user,
        ClaudeCodeLLMService(
            instructions(report, session=link is not None, listening=link is None or link.listening),
            timeline, say, link, haiku, tell,
        ),
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

    if link:

        async def notify(text: str) -> None:
            say(f"note from the session: {text[:80]!r}")
            await tell(text)

        link.listen(notify)
        task.add_reached_downstream_filter((BotStartedSpeakingFrame,))

        @task.event_handler("on_frame_reached_downstream")
        async def opened(task, frame):
            # Notes for the voice wait until it has started its opening.
            if isinstance(frame, BotStartedSpeakingFrame):
                link.opened()

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
    text: str | None,
    sensitivity: float,
    gate: float,
    offer: Offer,
    link: sessions.Link | None = None,
    haiku: Haiku | None = None,
) -> None:
    """A call from the phone's offer until it hangs up, or a new offer replaces this one. The
    voice opens once you've answered and audio is connected, whichever comes last."""
    answered = offer.answered
    say = lambda line: log(f"call {call}: {line}")
    since = lambda: f"{time.monotonic() - t0:.2f} s after the offer"
    transport = SmallWebRTCTransport(
        webrtc_connection=conn, params=TransportParams(audio_in_enabled=True, audio_out_enabled=True)
    )
    task = call_task(transport, text, say, sensitivity=sensitivity, gate=gate, link=link, haiku=haiku)
    connected = asyncio.Event()
    connected_at = 0.0
    started = False

    @transport.event_handler("on_client_connected")
    async def on_connected(transport, connection):
        nonlocal connected_at
        say(f"connected, {since()}")
        connected_at = time.monotonic()
        connected.set()

    @transport.event_handler("on_client_disconnected")
    async def disconnected(transport, connection):
        say("disconnected")
        await task.cancel()

    task.add_reached_downstream_filter((BotStartedSpeakingFrame,))

    @task.event_handler("on_frame_reached_downstream")
    async def speaking(task, frame):
        nonlocal started
        if not started and answered.done():
            started = True
            say(f"voice started {time.monotonic() - answered.result():.2f} s after you answered")

    async def opening():
        await asyncio.shield(answered)
        await connected.wait()
        if not offer.replaced.is_set():
            await task.queue_frames([LLMRunFrame()])

    async def replaced():
        await offer.replaced.wait()
        say("replaced by a new offer")
        await task.cancel()

    waiting = [asyncio.create_task(opening()), asyncio.create_task(replaced())]
    try:
        await PipelineRunner(handle_sigint=False).run(task)
    finally:
        for waiter in waiting:
            waiter.cancel()
    if connected.is_set() and answered.done():
        # Audio went both ways from when you answered or it connected, whichever came last.
        say(await audio_stats(conn, time.monotonic() - max(connected_at, answered.result())))
    say("finished")


async def audio_stats(conn: SmallWebRTCConnection, secs: float) -> str:
    """What the call's audio sent and received, from aiortc's stats: Opus's own rate, and the
    rate with RTP, SRTP and RTCP, but not UDP, IP or Nebula (see "Data and weak signal" in
    docs/design.md). Loss and jitter on the way to the phone are as the phone reported them."""
    try:
        stats = list((await conn.pc.getStats()).values())
    except Exception as e:  # the connection's end can't stop the call's end
        return f"no audio stats: {e!r}"
    one = lambda kind: next((s for s in stats if s.type == kind and getattr(s, "kind", "audio") == "audio"), None)
    out, back, into, phone, wire = (
        one("outbound-rtp"), one("remote-inbound-rtp"), one("inbound-rtp"), one("remote-outbound-rtp"), one("transport")
    )
    kbps = lambda count: f"{count * 8 / max(secs, 0.001) / 1000:.1f} kbps"
    ms = lambda jitter: f"{jitter / 48:.0f} ms"  # in 48 kHz samples
    line = f"audio for {secs:.1f} s:"
    if out and wire:
        line += f" sent {out.packetsSent} packets, {kbps(out.bytesSent)} of Opus, {kbps(wire.bytesSent)} with headers"
    if back:
        line += f"; the phone lost {back.packetsLost}, jitter {ms(back.jitter)}"
        if back.roundTripTime is not None:
            line += f", round trip {back.roundTripTime * 1000:.0f} ms"
    if into and wire:
        line += f"; received {into.packetsReceived} packets, lost {into.packetsLost}, jitter {ms(into.jitter)}"
        line += f", {kbps(wire.bytesReceived)} with headers"
    if phone:
        line += f"; the phone sent {phone.packetsSent} packets, {kbps(phone.bytesSent)} of Opus"
    return line


def only_address(host: str) -> None:
    """WebRTC on this side offers the phone only host, the address the phone's offer comes
    to: its Nebula address, so that path is known to work. Offered every address, WebRTC also
    checked pairs that can't connect, and audio took 2.5-2.8 s to connect (Stage 3). Left
    alone for loopback or every address, as for checks on this Mac."""
    try:
        ip = ipaddress.ip_address(host)
    except ValueError:
        return
    if ip.version == 4 and not ip.is_loopback and not ip.is_unspecified:
        import aioice.ice

        aioice.ice.get_host_addresses = lambda use_ipv4, use_ipv6: [host]


def setting(body: dict, key: str, default: float) -> float:
    """A setting from 0 to 1 sent with the offer, or default if it's missing or invalid."""
    try:
        value = float(body.get(key, default))
    except (TypeError, ValueError):
        return default
    return value if 0 <= value <= 1 else default  # also NaN


def main() -> None:
    p = argparse.ArgumentParser(prog="ringback-voice")
    sub = p.add_subparsers(dest="command", required=True)
    serve = sub.add_parser("serve", help="talk to the phone during calls")
    serve.add_argument("--listen", required=True, help="this Mac's Nebula address and port, e.g. 10.0.0.1:8788")
    serve.add_argument("--switchboard", help="the switchboard's Nebula address and port, e.g. 10.0.0.1:8787")
    serve.add_argument("--report", type=Path, help="what the voice reports on a call rung by hand, read for every call")
    serve.add_argument(
        "--socket", type=Path, default=sessions.SOCKET,
        help="the Unix socket where sessions' mods reach it (default ~/.local/state/ringback/voice.sock)",
    )  # fmt: skip
    args = p.parse_args()
    only_address(args.listen.rsplit(":", 1)[0])
    log(opus.install())

    logger.remove()
    logger.add(sys.stderr, level="WARNING")
    calls = sessions.Calls(args.switchboard, args.listen, log)
    # The latest offer for each call, while its pipeline runs.
    offered: dict[str, Offer] = {}

    def mark_answered(call_id: str) -> None:
        answered = offered[call_id].answered
        if answered.done():
            return
        log(f"call {call_id[:8]}: answered")
        call = calls.answered(call_id)
        if call and call.link and call.link.transcript:
            log(f"call {call_id[:8]}: transcript {call.link.transcript.path}")
        answered.set_result(time.monotonic())

    async def offer(request: web.Request) -> web.Response:
        body = await request.json()
        t0 = time.monotonic()
        call_id = str(body.get("call_id", ""))
        # You called a call back from the phone's call history; it's answered at once.
        back = body.get("back") is True
        # Made while the phone rings; an offer without it comes once you've answered.
        ringing = body.get("ringing") is True and not back
        call = await calls.back(call_id) if back else calls.offer(call_id)
        link = haiku = None
        if call and call.held_until:
            # Held outside your work hours, it hasn't rung, so the phone can't know its ID.
            log(f"call {call_id[:8]}: refused, held until {call.held_until}")
            return web.json_response({"error": "unknown call"}, status=403)
        if call:
            text, link = call.briefing, call.link
        elif args.report and not back:
            # A call rung by hand, to try the voice: it can't reach any session.
            try:
                text = args.report.read_text()
            except OSError as e:
                log(f"call {call_id[:8]}: no report ({e}), using the stand-in")
                text = None
        elif back:
            # Never placed here, held and not rung yet, forgotten over a day ago, or before a
            # restart.
            log(f"call {call_id[:8]}: call back refused, not a call this server remembers")
            return web.json_response({"error": "unknown call"}, status=403)
        else:
            log(f"call {call_id[:8]}: refused, not a call this server placed")
            return web.json_response({"error": "unknown call"}, status=403)
        sensitivity = setting(body, "sensitivity", hearing.SENSITIVITY)
        gate = setting(body, "background_filter", hearing.GATE)
        this = Offer()
        conn = HeldConnection(this.answered)
        await conn.initialize(sdp=body["sdp"], type=body["type"])
        if earlier := offered.get(call_id):
            # The phone gave up on its earlier offer, such as one made while it rang that
            # didn't connect. This one replaces it, and its voice stops using the call's Haiku.
            log(f"call {call_id[:8]}: a new offer replaces the earlier one")
            earlier.replaced.set()
            if call:
                try:
                    await asyncio.wait_for(earlier.ended.wait(), 5)
                except TimeoutError:
                    pass
        if call:
            if call.haiku and (call.haiku.used or (earlier and not earlier.ended.is_set())):
                # A voice has read its replies already, so this offer's voice starts its own.
                await call.haiku.close()
                call.haiku = None
            haiku = call.haiku
        offered[call_id] = this
        if not ringing:
            mark_answered(call_id)

        async def talk():
            try:
                await run_call(conn, call_id[:8], t0, text, sensitivity, gate, this, link, haiku)
            finally:
                if not this.replaced.is_set():
                    del offered[call_id]
                    # A call you haven't answered stays placed until it's forgotten, so the
                    # phone can offer again.
                    if this.answered.done():
                        await calls.end(call_id)
                this.ended.set()

        asyncio.create_task(talk())
        how = ", ringing" if ringing else ", called back" if back else ""
        log(f"call {call_id[:8]}: offer answered in {time.monotonic() - t0:.2f} s{how}")
        return web.json_response(conn.get_answer())

    async def answer(request: web.Request) -> web.Response:
        """You answered a call whose offer came while the phone rang."""
        try:
            call_id = str((await request.json()).get("call_id", ""))
        except (ValueError, AttributeError):
            return web.json_response({"error": "needs call_id"}, status=400)
        if call_id not in offered:
            log(f"call {call_id[:8]}: answered, but no offer for it")
            return web.json_response({"error": "unknown call"}, status=404)
        mark_answered(call_id)
        return web.json_response({"ok": True})

    async def serve_forever():
        async def ping(request: web.Request) -> web.Response:
            # The phone checks it can reach this Mac while it rings (AudioLink.reach).
            return web.json_response({"ok": True})

        app = web.Application()
        app.add_routes([web.post("/offer", offer), web.post("/answered", answer), web.get("/ping", ping)])
        runner = web.AppRunner(app)
        await runner.setup()
        host, port = args.listen.rsplit(":", 1)
        await web.TCPSite(runner, host, int(port)).start()
        await sessions.listen(sessions.session_side(calls), args.socket)
        log(f"voice listening on {args.listen}, and for sessions on {args.socket}")
        await asyncio.Event().wait()

    t = time.monotonic()
    log("loading Parakeet and Kokoro")
    models.load()
    log(f"models loaded in {time.monotonic() - t:.0f} s")
    try:
        asyncio.run(serve_forever())
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
