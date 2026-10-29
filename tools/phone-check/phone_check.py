#!/usr/bin/env python3
"""Plays the phone's side of calls against a voice server started here, to time the silence
after you answer without the phone.

    uv run --project voice tools/phone-check/phone_check.py [--runs N] [--ring S] [--keep DIR]

The voice server gets its own socket and ports, so it can't clash with yours or with other
checks. Calls are placed through its socket as a session's mod places them, and a stand-in
switchboard takes them, so nothing rings. Then aiortc plays the phone, in three ways, N
times each, each timed to the first audio that isn't silence:
- ringing: the offer goes as soon as the call is placed, marked "ringing", and /answered
  S seconds after placing; timed from /answered;
- answered: the offer goes S seconds after placing, as from an app that offers only once
  you've answered; timed from the offer;
- by hand: a call rung by hand, whose Haiku starts only when the offer comes, as every
  call's did before; timed from the offer.
As on the phone, the microphone sends nothing until you answer. It also declines a call
and offers it again, replaces a live offer with a new one, answers a call with a reply for
Haiku to pass to the session, and checks the calls' transcripts, and that no Claude Code
session folder is left. Prints the voice's log, the checks and the timings.
"""

import argparse
import asyncio
import fractions
import json
import os
import re
import secrets
import shutil
import socket
import statistics
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import aiohttp
import av
import numpy as np
import soxr
from aiohttp import web
from aiortc import MediaStreamTrack, RTCConfiguration, RTCPeerConnection, RTCSessionDescription
from aiortc.mediastreams import MediaStreamError

SWITCHBOARD = "127.0.0.1:18797"  # the stand-in, in this script
VOICE = "127.0.0.1:18798"
SESSION = "phone-check-session"
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
LOUD = 100  # int16 level above which the voice counts as speaking, as in call-check
HEARD = 2.0  # seconds of the voice heard before hanging up
WAIT = 20  # seconds for the voice to start, or a call to finish after hanging up


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

    def say(self, samples: np.ndarray) -> None:
        self._pending = np.concatenate([self._pending, samples])

    async def recv(self) -> av.AudioFrame:
        step = int(RATE * TICK)
        await self.on.wait()
        if self._start is None:
            self._start = time.monotonic()
        await asyncio.sleep(max(0.0, self._start + self._sent / RATE - time.monotonic()))
        chunk, self._pending = self._pending[:step], self._pending[step:]
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
            if np.abs(frame.to_ndarray().astype(np.int32)).max(initial=0) > LOUD:
                self.last_audio = time.monotonic()
                self.first_audio = self.first_audio or self.last_audio

    async def offer(self, call_id: str, ringing: bool) -> int:
        if not ringing:
            self.mic.on.set()
        await self.pc.setLocalDescription(await self.pc.createOffer())
        body = {"sdp": self.pc.localDescription.sdp, "type": "offer", "call_id": call_id}
        if ringing:
            body["ringing"] = True
        async with aiohttp.ClientSession() as http:
            async with http.post(f"http://{VOICE}/offer", json=body) as r:
                if r.status != 200:
                    return r.status
                answer = await r.json()
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


async def answered(call_id: str, phone: Phone | None = None) -> tuple[int, dict]:
    """Tells the voice server you've answered, as the phone does; its microphone comes on."""
    if phone:
        phone.mic.on.set()
    async with aiohttp.ClientSession() as http:
        async with http.post(f"http://{VOICE}/answered", json={"call_id": call_id}) as r:
            return r.status, await r.json()


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
    host, port = address.rsplit(":", 1)
    with socket.socket() as s:
        try:
            s.bind((host, int(port)))
            return True
        except OSError:
            return False


def kept() -> set[Path]:
    """The folders where Claude Code keeps the voice's sessions, which it deletes at the end
    of a call (forget in voice/ringback_voice/haiku.py)."""
    home = Path(os.environ.get("CLAUDE_CONFIG_DIR") or "~/.claude").expanduser()
    return set((home / "projects").glob("*ringback-voice-*"))


async def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--runs", type=int, default=3, help="calls of each kind (default 3)")
    p.add_argument("--ring", type=float, default=6.0, help="seconds from placing a call to answering it (default 6)")
    p.add_argument("--keep", type=Path, help="keep the voice log and transcripts in this directory")
    args = p.parse_args()
    for address in (SWITCHBOARD, VOICE):
        if not free(address):
            sys.exit(f"{address} is in use: is another phone check running?")

    sessions = kept()
    tmp = Path(tempfile.mkdtemp(prefix="ringback-phone-check-"))
    sock, voice_log, project = tmp / "voice.sock", tmp / "voice.log", tmp / "demo"
    project.mkdir()
    (tmp / "report.txt").write_text(BRIEFING)
    calls_to_the_switchboard: list[dict] = []

    async def take_call(request: web.Request) -> web.Response:
        calls_to_the_switchboard.append(await request.json())
        return web.json_response({"ok": True})

    switchboard = web.AppRunner(web.Application())
    switchboard.app.add_routes([web.post("/call", take_call)])
    await switchboard.setup()
    host, port = SWITCHBOARD.rsplit(":", 1)
    await web.TCPSite(switchboard, host, int(port)).start()
    voice = subprocess.Popen(
        [sys.executable, "-m", "ringback_voice", "serve", "--listen", VOICE, "--switchboard", SWITCHBOARD,
         "--socket", str(sock), "--report", str(tmp / "report.txt")],
        stdout=voice_log.open("w"), stderr=subprocess.STDOUT,
    )  # fmt: skip
    socket_side = aiohttp.ClientSession(connector=aiohttp.UnixConnector(path=str(sock)))

    def haikus() -> int:
        """Haiku sessions the voice server runs now."""
        found = subprocess.run(["pgrep", "-P", str(voice.pid), "-f", "input-format stream-json"], capture_output=True)
        return len(found.stdout.split())

    async def haikus_gone(since: float) -> float | None:
        """Seconds from since until the voice server runs no Haiku, or None if it still does
        after 5 s. A Haiku that doesn't exit within 2 s of its call's end is stopped."""
        end = time.monotonic() + 5
        while haikus() and time.monotonic() < end:
            await asyncio.sleep(0.1)
        return None if haikus() else time.monotonic() - since

    async def place() -> str:
        """Places a call as a session's mod does; its ID, as the switchboard got it."""
        body = {"session_id": SESSION, "project": "demo", "cwd": str(project), "subject": SUBJECT, "briefing": BRIEFING}
        async with socket_side.post("http://voice/calls", json=body) as r:
            assert r.status == 200, await r.text()
        return calls_to_the_switchboard[-1]["call_id"]

    async def finished(call_id: str, since: float, nth: int = 1) -> float | None:
        """Seconds from since until the call's nth pipeline ended, or None if it didn't."""
        if await wait_for(voice_log, rf"call {re.escape(call_id[:8])}: finished", WAIT, nth):
            return time.monotonic() - since
        return None

    fails = 0
    lines: list[str] = []

    def check(name: str, ok: bool) -> None:
        nonlocal fails
        lines.append(f"{'ok  ' if ok else 'FAIL'}  {name}")
        fails += not ok

    try:
        from mlx_audio.tts.utils import load_model

        kokoro = load_model(KOKORO)
        speech = np.concatenate([np.array(r.audio) for r in kokoro.generate(REPLY, voice=YOUR_VOICE, lang_code="a")])
        reply = (np.clip(soxr.resample(speech, 24000, RATE), -1, 1) * 32767).astype(np.int16)
        if not await wait_for(voice_log, r"voice listening", 180):
            print(voice_log.read_text())
            sys.exit("The voice server didn't start.")

        status, _ = await answered("never-offered-call")
        check("/answered for a call never offered: 404", status == 404)

        # Declined: the phone hangs up while it rings.
        call_id = await place()
        status, _ = await answered(call_id)
        check("/answered for a placed call before its offer: 404", status == 404)
        phone = Phone()
        check("a ringing offer is answered (200)", await phone.offer(call_id, ringing=True) == 200)
        await asyncio.sleep(3)
        check("no voice while it rings", phone.first_audio is None)
        # Silence sent while it rings would queue up on the phone and play first.
        check(f"nothing at all sent while it rings ({phone.frames} frames)", phone.frames == 0)
        hung_up = time.monotonic()
        await phone.hang_up()
        took = await finished(call_id, hung_up)
        check(f"declined: its pipeline ended after the phone hung up ({took or 0:.1f} s)", took is not None)
        await asyncio.sleep(0.5)
        check("declined: the call stays placed until it's forgotten, with its Haiku", haikus() == 1)
        check("declined: no transcript", not list(project.glob(".ringback/calls/*.md")))
        status, _ = await answered(call_id)
        check("/answered with no offer live: 404", status == 404)

        # Offered again once you answer, as the app does when its offer made while ringing
        # failed: the call's Haiku, with its opening, is still there.
        phone = Phone()
        t = time.monotonic()
        ok = await phone.offer(call_id, ringing=False) == 200
        silence = await phone.heard_after(t)
        await asyncio.sleep(HEARD)
        hung_up = time.monotonic()
        await phone.hang_up()
        ended = await finished(call_id, hung_up, nth=2)
        gone = await haikus_gone(hung_up)
        check(f"offered again after declining: voice heard after {silence or 0:.2f} s, call ended, no Haiku left",
              ok and silence is not None and ended is not None and gone is not None)  # fmt: skip
        answered_calls = 1
        closing = [gone or 0]  # seconds from hanging up until no Haiku was left

        # A new offer replaces one still live, made while the phone rang.
        placed = time.monotonic()
        call_id = await place()
        first = Phone()
        ok = await first.offer(call_id, ringing=True) == 200
        await asyncio.sleep(max(0.0, placed + args.ring - time.monotonic()))
        phone = Phone()
        t = time.monotonic()
        ok = ok and await phone.offer(call_id, ringing=False) == 200
        replaced = await wait_for(voice_log, rf"call {re.escape(call_id[:8])}: replaced by a new offer", WAIT)
        silence = await phone.heard_after(t)
        await asyncio.sleep(HEARD)
        hung_up = time.monotonic()
        await phone.hang_up()
        await first.hang_up()
        ended = await finished(call_id, hung_up, nth=2)
        gone = await haikus_gone(hung_up)
        check(f"a new offer replaced a live one: voice heard after {silence or 0:.2f} s, not on the first, "
              "call ended, no Haiku left",
              ok and replaced is not None and first.first_audio is None and silence is not None
              and ended is not None and gone is not None)  # fmt: skip
        answered_calls += 1
        closing.append(gone or 0)

        # A call rung by hand, offered while it rings.
        call_id = "handring" + secrets.token_hex(8)
        phone = Phone()
        ok = await phone.offer(call_id, ringing=True) == 200
        await asyncio.sleep(3)
        ok = ok and phone.first_audio is None
        t = time.monotonic()
        ok = ok and await answered(call_id, phone) == (200, {"ok": True})
        silence = await phone.heard_after(t)
        await phone.hang_up()
        ended = await finished(call_id, time.monotonic())
        check(f"by hand, ringing: silent until /answered, then heard after {silence or 0:.2f} s, ended",
              ok and silence is not None and ended is not None)  # fmt: skip

        timings: dict[str, list[float]] = {"ringing": [], "answered": [], "by hand": []}
        firsts: dict[str, list[float]] = {"ringing": [], "answered": [], "by hand": []}
        for run in range(1, args.runs + 1):
            for kind in timings:
                phone = Phone()
                if kind == "by hand":
                    call_id = f"hand{run:04d}" + secrets.token_hex(8)
                    t = time.monotonic()
                    ok = await phone.offer(call_id, ringing=False) == 200
                else:
                    placed = time.monotonic()
                    call_id = await place()
                    if kind == "ringing":
                        ok = await phone.offer(call_id, ringing=True) == 200
                        await asyncio.sleep(max(0.0, placed + args.ring - time.monotonic()))
                        early = phone.first_audio
                        t = time.monotonic()
                        once, again = await answered(call_id, phone), await answered(call_id)
                        ok = ok and early is None and once == again == (200, {"ok": True})
                    else:
                        await asyncio.sleep(max(0.0, placed + args.ring - time.monotonic()))
                        t = time.monotonic()
                        ok = await phone.offer(call_id, ringing=False) == 200
                    answered_calls += 1
                silence = await phone.heard_after(t)
                await asyncio.sleep(HEARD)
                hung_up = time.monotonic()
                await phone.hang_up()
                ended = await finished(call_id, hung_up)
                gone = await haikus_gone(hung_up)
                closing.append(gone or 0)
                name = f"{kind} {run}: " + ("silent before /answered, answered twice, " if kind == "ringing" else "")
                check(name + f"voice heard, call ended, no Haiku left ({silence or 0:.2f} s)",
                      ok and silence is not None and ended is not None and gone is not None)  # fmt: skip
                if silence is not None:
                    timings[kind].append(silence)
                first_words = rf"call {re.escape(call_id[:8])}: Haiku: first words after ([\d.]+) s"
                if m := re.search(first_words, voice_log.read_text()):
                    firsts[kind].append(float(m[1]))

        async def for_the_session() -> list[str]:
            """What the voice passed to the session; calls' outcomes, also queued for it, aren't."""
            async with socket_side.get(f"http://voice/sessions/{SESSION}/messages") as r:
                return [m["text"] for m in (await r.json())["messages"] if m.get("kind", "message") == "message"]

        stray = await for_the_session()
        check(f"nothing reached the session from calls where you said nothing: {stray}", not stray)

        # A reply that Haiku passes to the session, from a call offered while it rang.
        placed = time.monotonic()
        call_id = await place()
        phone = Phone()
        await phone.offer(call_id, ringing=True)
        await asyncio.sleep(max(0.0, placed + args.ring - time.monotonic()))
        await answered(call_id, phone)
        answered_calls += 1
        await phone.heard_after(time.monotonic())
        await phone.quiet_for(2.5, limit=60)
        phone.mic.say(reply)
        messages: list[str] = []
        end = time.monotonic() + 45
        while not messages and time.monotonic() < end:
            await asyncio.sleep(0.5)
            messages = await for_the_session()
        await asyncio.sleep(1)
        await phone.hang_up()
        await finished(call_id, time.monotonic())
        messages += await for_the_session()
        check(f"your reply, and only it, reached the session: {messages}",
              len(messages) == 1 and re.search(r"push|pull request", messages[0], re.I) is not None)  # fmt: skip

        transcripts = sorted(project.glob(".ringback/calls/*.md"))
        check(f"one transcript for each answered call ({len(transcripts)} of {answered_calls})",
              len(transcripts) == answered_calls)  # fmt: skip
        firsts_in = [re.search(r"^\*\*(.+?):\*\*", t.read_text(), re.M) for t in transcripts]
        check("each transcript starts with the voice's opening", all(m and m[1] == "Voice" for m in firsts_in))
        gone = await haikus_gone(time.monotonic())
        left = sorted(p.name for p in kept() - sessions)
        check(f"no Haiku left, and no Claude Code session folders in ~/.claude/projects: {left}",
              gone is not None and not left)  # fmt: skip
        if transcripts:
            print(f"--- the last transcript\n{transcripts[-1].read_text()}")
    finally:
        await socket_side.close()
        voice.terminate()
        voice.wait()
        await switchboard.cleanup()

    print("--- voice log")
    print("".join(line for line in voice_log.open() if re.search(r"call |voice listening|models loaded", line)))
    print("\n".join(lines))
    print(f"\nsilence after you answer, to the first audio (s), {args.ring:g} s from placing to answering:")
    for kind, values in timings.items():
        if values:
            spread = f"median {statistics.median(values):.2f}, {min(values):.2f}–{max(values):.2f}"
            print(f"  {kind:9} {spread}: {', '.join(f'{v:.2f}' for v in values)}")
    print("Haiku's first words after it was asked for the opening (s, from the voice log):")
    for kind, values in firsts.items():
        if values:
            print(f"  {kind:9} {', '.join(f'{v:.2f}' for v in values)}")
    if closing:
        print(f"no Haiku left {min(closing):.1f}–{max(closing):.1f} s after hanging up")
    if args.keep:
        shutil.copytree(tmp, args.keep, dirs_exist_ok=True, ignore=shutil.ignore_patterns("*.sock"))
    shutil.rmtree(tmp, ignore_errors=True)
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
