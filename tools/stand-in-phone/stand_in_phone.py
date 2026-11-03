"""A stand-in for the iPhone app's side of a call, for the checks that need one
(`tools/hub-check`, `tools/line-check`, `tools/portal-screens`): an aiortc peer that makes
the call's offer to the hub as the app does, with all its ICE candidates in it, sends your
voice once you've answered, and notes when the hub's voice is heard.

    sys.path.insert(0, ".../tools/stand-in-phone")
    import stand_in_phone
    stand_in_phone.HUB = "127.0.0.1:8787"
    phone = stand_in_phone.Phone()
    await phone.offer(call_id, ringing=False)
"""

import asyncio
import fractions
import re
import socket
import time
from pathlib import Path

import aiohttp
import av
import numpy as np
from aiortc import MediaStreamTrack, RTCConfiguration, RTCPeerConnection, RTCSessionDescription
from aiortc.mediastreams import MediaStreamError

HUB = "127.0.0.1:8787"  # where the phone reaches the hub; each check sets its own
SUBJECT = "Parser fix ready to push"
BRIEFING = """\
I fixed the crash in the config parser. It choked on empty lines between sections. The fix
is two lines, with a new test, and the whole suite passes. One thing needs you: shall I
push it to a branch and open a pull request?"""
REPLY = "Yes, push it and open the pull request."
YOUR_VOICE = "am_michael"  # Kokoro voice for your reply
KOKORO = "mlx-community/Kokoro-82M-bf16"
RATE = 48000  # the phone's audio, as WebRTC carries it
TICK = 0.02  # seconds of audio per frame the phone sends
LOUD = 100  # int16 level above which the voice counts as speaking
WAIT = 20  # seconds for the voice to start


class Mic(MediaStreamTrack):
    """The phone's microphone: nothing until you answer, as on the phone, then silence, and
    your reply when there is one."""

    kind = "audio"

    def __init__(self):
        super().__init__()
        self.on = asyncio.Event()  # set when you answer
        self._pending = np.zeros(0, dtype=np.int16)
        self._start: float | None = None
        self._sent = 0  # samples
        self.frames: list[tuple[float, bool]] = []  # when each frame went, and whether you spoke in it

    def say(self, samples: np.ndarray) -> None:
        self._pending = np.concatenate([self._pending, samples])

    async def recv(self) -> av.AudioFrame:
        step = int(RATE * TICK)
        await self.on.wait()
        if self._start is None:
            self._start = time.monotonic()
        await asyncio.sleep(max(0.0, self._start + self._sent / RATE - time.monotonic()))
        chunk, self._pending = self._pending[:step], self._pending[step:]
        self.frames.append((time.monotonic(), len(chunk) > 0))
        chunk = np.pad(chunk, (0, step - len(chunk)))
        frame = av.AudioFrame.from_ndarray(chunk.reshape(1, -1), format="s16", layout="mono")
        frame.sample_rate, frame.pts, frame.time_base = RATE, self._sent, fractions.Fraction(1, RATE)
        self._sent += step
        return frame


class Phone:
    """The phone's side of one call, as the app does it: one offer with all ICE candidates."""

    def __init__(self):
        self.pc = RTCPeerConnection(RTCConfiguration(iceServers=[]))
        self.mic = Mic()
        self.pc.addTrack(self.mic)
        self.first_audio: float | None = None  # when the voice was first heard
        self.last_audio: float | None = None
        self.frames = 0  # audio frames received, silent ones too
        self.heard: list[tuple[float, bool]] = []  # when each frame came, and whether the voice spoke in it
        self.on_answer = None  # called with the hub's answer before it's applied
        self._listener: asyncio.Task | None = None

        @self.pc.on("track")
        def track(track):
            if track.kind == "audio":
                self._listener = asyncio.ensure_future(self._listen(track))

    async def _listen(self, track):
        while True:
            try:
                frame = await track.recv()
            except MediaStreamError:
                return
            self.frames += 1
            loud = np.abs(frame.to_ndarray().astype(np.int32)).max(initial=0) > LOUD
            self.heard.append((time.monotonic(), loud))
            if loud:
                self.last_audio = time.monotonic()
                self.first_audio = self.first_audio or self.last_audio

    async def offer(
        self, call_id: str, ringing: bool, back: bool = False, now: bool = False, contact: str | None = None
    ) -> int | tuple[int, str]:
        """Offers the call's audio: while it rings, once you've answered, calling it back,
        calling a waiting call now, or calling a session from the Sessions tab. A call to a
        session that's refused gives the status and the error."""
        if not ringing:
            self.mic.on.set()
        await self.pc.setLocalDescription(await self.pc.createOffer())
        body = {"sdp": self.pc.localDescription.sdp, "type": "offer", "call_id": call_id}
        if ringing:
            body["ringing"] = True
        if back:
            body["back"] = True
        if now:
            body["now"] = True
        if contact:
            body["contact"] = contact
        async with aiohttp.ClientSession() as http:
            async with http.post(f"http://{HUB}/offer", json=body) as r:
                if r.status != 200:
                    return (r.status, (await r.json(content_type=None) or {}).get("error", "")) if contact else r.status
                answer = await r.json()
        if self.on_answer:
            self.on_answer(answer["sdp"])
        await self.pc.setRemoteDescription(RTCSessionDescription(sdp=answer["sdp"], type=answer["type"]))
        return 200

    async def heard_after(self, t: float) -> float | None:
        """Seconds from t until the voice was first heard, or None if it wasn't within WAIT."""
        end = time.monotonic() + WAIT
        while self.first_audio is None and time.monotonic() < end:
            await asyncio.sleep(0.01)
        return None if self.first_audio is None else self.first_audio - t

    async def quiet_for(self, secs: float, limit: float) -> None:
        """Waits until the voice has been quiet for secs, at most limit seconds."""
        end = time.monotonic() + limit
        while time.monotonic() < end and time.monotonic() - (self.last_audio or 0) < secs:
            await asyncio.sleep(0.1)

    async def hang_up(self) -> None:
        await self.pc.close()
        if self._listener:
            self._listener.cancel()


async def wait_for(path: Path, pattern: str, seconds: float, nth: int = 1) -> re.Match | None:
    """The nth match of pattern in the file at path, once there is one."""
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if path.exists():
            found = list(re.finditer(pattern, path.read_text(errors="replace")))
            if len(found) >= nth:
                return found[nth - 1]
        await asyncio.sleep(0.1)
    return None


def free(address: str) -> bool:
    """Whether nothing listens at address (host:port) yet."""
    host, port = address.rsplit(":", 1)
    with socket.socket() as s:
        try:
            s.bind((host, int(port)))
            return True
        except OSError:
            return False
