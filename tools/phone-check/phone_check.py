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

    uv run --project voice tools/phone-check/phone_check.py --back [--unanswered S] [--keep DIR]

calls back calls you didn't answer instead, as the app does from its call history: an offer
marked "back", answered at once. A stand-in for the session's mod collects its messages once
a second, so the session listens. Checks that a call back is refused for a call the server
doesn't know; that the voice of a call back opens with what the missed call was about, and
when the session has called again since, with what changed; that a session that isn't
listening gets a voice that passes nothing on; that a permission asked on a call you didn't
answer is answered on its call back; and that a call back while the missed call is still
placed leaves no "wasn't answered" note. Prints the openings, and the time from each offer
to the voice's first audio.

    uv run --project voice tools/phone-check/phone_check.py --signal [--loss 0,5,15] [--runs N] [--keep DIR]

says known sentences instead, on calls rung by hand whose line loses some of the phone's
packets (each at random, with the chances given in percent), with the phone's Opus set up as
Google's WebRTC sets it up from the voice server's answer. Compares what the voice server
heard with what was said, and prints the words heard wrong, how soon after each sentence its
words were heard, and the data each way.

--port P gives the stand-in switchboard port P and the voice server P + 1 (18797 and 18798 by
default), so two checks can run at once.
"""

import argparse
import ast
import asyncio
import fractions
import json
import os
import random
import re
import secrets
import shutil
import socket
import statistics
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timedelta
from pathlib import Path

import aiohttp
import av
import numpy as np
import soxr
from aiohttp import web
from aiortc import MediaStreamTrack, RTCConfiguration, RTCPeerConnection, RTCSessionDescription
from aiortc.codecs.opus import OpusEncoder
from aiortc.mediastreams import MediaStreamError
from aiortc.rtp import RtpPacket, is_rtcp

PORT = 18797  # the stand-in switchboard's port, in this script; the voice server gets the next
SWITCHBOARD = f"127.0.0.1:{PORT}"
VOICE = f"127.0.0.1:{PORT + 1}"
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
        self.on_answer = None  # called with the voice server's answer before it's applied
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

    async def offer(self, call_id: str, ringing: bool, back: bool = False) -> int:
        """Offers the call's audio: while it rings, once you've answered, or calling it back."""
        if not ringing:
            self.mic.on.set()
        await self.pc.setLocalDescription(await self.pc.createOffer())
        body = {"sdp": self.pc.localDescription.sdp, "type": "offer", "call_id": call_id}
        if ringing:
            body["ringing"] = True
        if back:
            body["back"] = True
        async with aiohttp.ClientSession() as http:
            async with http.post(f"http://{VOICE}/offer", json=body) as r:
                if r.status != 200:
                    return r.status
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


def kept(under: Path | None = None) -> set[Path]:
    """The folders where Claude Code keeps the voice's sessions, which it deletes at the end
    of a call (forget in voice/ringback_voice/haiku.py). With under, only those of a voice
    server whose temporary folder (TMPDIR) is under, not of other voice servers on this Mac."""
    home = Path(os.environ.get("CLAUDE_CONFIG_DIR") or "~/.claude").expanduser()
    found = set((home / "projects").glob("*ringback-voice-*"))
    if under:
        # Claude Code names the folder for the session's real path, with every character but
        # letters and digits made "-".
        prefix = re.sub(r"[^a-zA-Z0-9]", "-", os.path.realpath(under))
        found = {p for p in found if p.name.startswith(prefix)}
    return found


async def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--runs", type=int, default=3, help="calls of each kind (default 3)")
    p.add_argument("--ring", type=float, default=6.0, help="seconds from placing a call to answering it (default 6)")
    p.add_argument("--keep", type=Path, help="keep the voice log and transcripts in this directory")
    p.add_argument("--back", action="store_true", help="call back calls you didn't answer instead")
    p.add_argument("--unanswered", type=float, default=8, help="with --back: seconds before an unanswered call is forgotten")
    p.add_argument("--signal", action="store_true", help="say known sentences through a line that loses packets instead")
    p.add_argument("--loss", default="0,5,15", help="with --signal: percentages of the phone's packets lost (default 0,5,15)")
    p.add_argument("--port", type=int, default=PORT, help=f"the stand-in switchboard's port; the voice server's is the next (default {PORT})")
    args = p.parse_args()
    global SWITCHBOARD, VOICE
    SWITCHBOARD, VOICE = f"127.0.0.1:{args.port}", f"127.0.0.1:{args.port + 1}"
    for address in (SWITCHBOARD, VOICE):
        if not free(address):
            sys.exit(f"{address} is in use: is another phone check running?")
    if args.back:
        return await call_backs(args)
    if args.signal:
        return await weak_signal(args)

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


# Call backs (--back): the session's later call, a session that isn't listening, a permission,
# and a call called back before it's forgotten.
LATER_SUBJECT = "Pull request 42 opened"
LATER = """\
Since the parser fix, the developer said yes in the terminal, so I pushed it to the branch
fix-parser and opened pull request 42. Its checks pass. Nothing needs the developer now,
apart from reviewing the pull request when they have time."""
GONE_SESSION = "phone-check-gone"  # no mod collects for it: it isn't listening
GONE_SUBJECT = "Docs site rebuilt"
GONE = """\
I rebuilt the documentation site and fixed three broken links in the setup guide. It's all
committed. Nothing needs the developer."""
ASK_AGAIN = "Please ask the session to rebuild the docs once more."
PERMISSION = {
    "tool": "Bash",
    "description": "Delete the build folder",
    "input": "rm -rf build",
    "context": "I'm cleaning up before the release build.",
    "project": "demo",
    "subject": "Delete the build folder?",
}
YES = "Yes, go ahead."
# A voice that introduces itself, or says it's calling, on a call you made.
INTRODUCES = re.compile(r"this is claude|claude here|i'm claude|i'm calling|i am calling|calling (you )?(about|from)", re.I)
WINDOW_SUBJECT = "Release notes drafted"
WINDOW = """\
I drafted the release notes for version 2.4: the parser fix and the new config warnings.
They're in the notes folder, not committed yet. Should I commit them?"""


async def call_backs(args) -> int:
    """Calls back calls you didn't answer, as the app does from its call history."""
    tmp = Path(tempfile.mkdtemp(prefix="ringback-phone-check-"))
    sock, voice_log, project = tmp / "voice.sock", tmp / "voice.log", tmp / "demo"
    project.mkdir()
    # The voice server's own temporary folder, so its sessions' folders tell from those of
    # other voice servers on this Mac. Short: Haiku's tools socket goes in a folder inside it,
    # and a socket's path can have at most 104 characters.
    own = Path(tempfile.mkdtemp(prefix="rb"))
    # With a report, an offer for a call the server didn't place is a call rung by hand; a
    # call back for one must still be refused.
    (tmp / "report.txt").write_text(BRIEFING)
    rung: list[dict] = []

    async def take_call(request: web.Request) -> web.Response:
        rung.append(await request.json())
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
        env=os.environ | {"RINGBACK_UNANSWERED": str(args.unanswered), "TMPDIR": str(own)},
    )  # fmt: skip
    socket_side = aiohttp.ClientSession(connector=aiohttp.UnixConnector(path=str(sock)))
    got: list[dict] = []  # what the stand-in mod collected for SESSION

    async def mod():
        """The session's mod, while ringback is on: collects its messages once a second."""
        while True:
            try:
                async with socket_side.get(f"http://voice/sessions/{SESSION}/messages") as r:
                    got.extend((await r.json())["messages"])
            except (aiohttp.ClientError, ValueError):
                pass
            await asyncio.sleep(1)

    async def place(session: str, subject: str, briefing: str) -> str:
        body = {"session_id": session, "project": "demo", "cwd": str(project), "subject": subject, "briefing": briefing}
        async with socket_side.post("http://voice/calls", json=body) as r:
            assert r.status == 200, await r.text()
        return rung[-1]["call_id"]

    async def forgotten(call_id: str) -> bool:
        return await wait_for(voice_log, rf"call {re.escape(call_id[:8])}: not answered, forgotten", args.unanswered + 10) is not None

    def outcomes(subject: str) -> list[str]:
        return [m["text"] for m in got if m.get("kind") == "outcome" and f'"{subject}"' in m["text"]]

    def haikus() -> int:
        found = subprocess.run(["pgrep", "-P", str(voice.pid), "-f", "input-format stream-json"], capture_output=True)
        return len(found.stdout.split())

    fails = 0
    lines: list[str] = []
    timings: list[float] = []  # from each call back's offer to the voice's first audio
    openings: list[str] = []  # what the voice said first on each call back
    replies: list[str] = []
    during = 0  # the most of its sessions' folders seen during a call back, so the last check finds them

    def check(name: str, ok: bool) -> None:
        nonlocal fails
        lines.append(f"{'ok  ' if ok else 'FAIL'}  {name}")
        fails += not ok

    async def call_back(call_id: str, name: str) -> tuple[Phone, Path | None, str]:
        """Calls call_id back, and listens to the voice's opening: the phone, the call back's
        transcript, and the opening as the voice said it."""
        nonlocal during
        before = set(project.glob(".ringback/calls/*.md"))
        phone = Phone()
        t = time.monotonic()
        status = await phone.offer(call_id, ringing=False, back=True)
        heard = await phone.heard_after(t)
        during = max(during, len(kept(own)))
        await phone.quiet_for(2.5, limit=60)
        transcript = next(iter(set(project.glob(".ringback/calls/*.md")) - before), None)
        found = transcript and await wait_for(transcript, r"\*\*Voice:\*\* (.+)", 30)
        opening = found[1] if found else ""
        if heard is not None:
            timings.append(heard)
        openings.append(f"{name}: {opening}")
        check(f"{name}: the call back is answered (200), voice heard {heard or 0:.2f} s after the offer",
              status == 200 and heard is not None)  # fmt: skip
        return phone, transcript, opening

    collecting = None
    try:
        from mlx_audio.tts.utils import load_model

        kokoro = load_model(KOKORO)

        def speech(text: str) -> np.ndarray:
            audio = np.concatenate([np.array(r.audio) for r in kokoro.generate(text, voice=YOUR_VOICE, lang_code="a")])
            return (np.clip(soxr.resample(audio, 24000, RATE), -1, 1) * 32767).astype(np.int16)

        ask_again, yes = speech(ASK_AGAIN), speech(YES)
        if not await wait_for(voice_log, r"voice listening", 180):
            print(voice_log.read_text())
            sys.exit("The voice server didn't start.")
        collecting = asyncio.create_task(mod())

        status = await Phone().offer("never-placed-call", ringing=False, back=True)
        check(f"a call back for a call the server doesn't know is refused, though it takes calls rung by hand: {status}",
              status == 403)  # fmt: skip

        # A call you don't answer, called back once it's forgotten.
        missed = await place(SESSION, SUBJECT, BRIEFING)
        check("a call you don't answer is forgotten", await forgotten(missed))
        await asyncio.sleep(1.5)
        check(f"its session was told it wasn't answered: {outcomes(SUBJECT)}",
              len(outcomes(SUBJECT)) == 1 and "wasn't answered" in outcomes(SUBJECT)[0])  # fmt: skip
        phone, _, opening = await call_back(missed, "missed")
        check("missed: the opening is about the missed call", re.search(r"parser|empty line|push", opening, re.I) is not None)
        await phone.hang_up()
        await asyncio.sleep(2.5)
        told = outcomes(SUBJECT)[1:]
        check(f"missed: its session was told you called back: {told}",
              len(told) == 1 and told[0].startswith(f'The developer called back about "{SUBJECT}" and it lasted'))  # fmt: skip

        # The session called again since: calling the missed call back again, the voice
        # opens with what changed.
        later = await place(SESSION, LATER_SUBJECT, LATER)
        check("the session's later call isn't answered either", await forgotten(later))
        phone, _, opening = await call_back(missed, "moved on")
        first = " ".join(opening.split()[:30])
        since = rf"call {re.escape(missed[:8])}: called back .*with a newer briefing since"
        check("moved on: a call called back before can be called back again, with the later briefing, "
              "and its opening starts with what changed",
              re.search(since, voice_log.read_text()) is not None
              and re.search(r"pull request|42|pushed", first, re.I) is not None)  # fmt: skip
        await phone.hang_up()

        # A session that isn't listening any more: no mod collects for it.
        gone = await place(GONE_SESSION, GONE_SUBJECT, GONE)
        check("a call to a session that isn't listening is forgotten", await forgotten(gone))
        phone, transcript, opening = await call_back(gone, "not listening")
        check("not listening: the voice server says so, and the opening is about its call",
              re.search(rf"call {re.escape(gone[:8])}: called back .*the session isn't listening", voice_log.read_text())
              is not None and re.search(r"doc|link", opening, re.I) is not None)  # fmt: skip
        phone.mic.say(ask_again)
        replied = transcript and await wait_for(transcript, r"\*\*Voice:\*\* (.+)", 45, nth=2)
        await phone.quiet_for(2.5, limit=30)
        await phone.hang_up()
        await asyncio.sleep(1)
        async with socket_side.get(f"http://voice/sessions/{GONE_SESSION}/messages") as r:
            left = [m["text"] for m in (await r.json())["messages"] if m.get("kind") == "message"]
        reply = replied[1] if replied else ""
        replies.append(f"not listening, asked to pass something on: {reply}")
        check(f'not listening: asked "{ASK_AGAIN}", the voice passed nothing on, and said it can\'t: {left}',
              not left and re.search(r"can't|cannot|can not|isn't listening|not listening|terminal", reply, re.I) is not None)  # fmt: skip

        # A permission asked on a call you don't answer waits, and its call back answers it.
        async with socket_side.post(f"http://voice/sessions/{SESSION}/permissions", json=PERMISSION | {"cwd": str(project)}) as r:
            asked = await r.json()
        pid, asking = asked.get("permission", ""), rung[-1]["call_id"]
        check(f"a permission with no call places one: {asked}", asked.get("call") == "placed")
        ok = await forgotten(asking)
        async with socket_side.get(f"http://voice/sessions/{SESSION}/permissions/{pid}?wait=0") as r:
            waiting = await r.json()
        check(f"its call isn't answered and is forgotten, and the permission still waits: {waiting}",
              ok and waiting.get("verdict") == "pending")  # fmt: skip
        phone, _, opening = await call_back(asking, "permission")
        check("permission: the opening asks it", re.search(r"delet|build|remov", opening, re.I) is not None)
        phone.mic.say(yes)
        verdict, end = {}, time.monotonic() + 45
        while verdict.get("verdict") in (None, "pending") and time.monotonic() < end:
            async with socket_side.get(f"http://voice/sessions/{SESSION}/permissions/{pid}?wait=5") as r:
                verdict = await r.json()
        check(f'permission: "{YES}" on the call back allows it: {verdict}', verdict.get("verdict") == "allow")
        await phone.quiet_for(2.5, limit=30)
        await phone.hang_up()

        # Called back before it's forgotten: you decline, then call it back at once, and the
        # call back outlasts the missed call's time.
        placed = time.monotonic()
        window = await place(SESSION, WINDOW_SUBJECT, WINDOW)
        ringing = Phone()
        await ringing.offer(window, ringing=True)
        await asyncio.sleep(1)
        await ringing.hang_up()
        await asyncio.sleep(0.5)
        phone, _, _ = await call_back(window, "inside the window")
        await asyncio.sleep(max(0.0, placed + args.unanswered + 2 - time.monotonic()))
        await phone.hang_up()
        await asyncio.sleep(3)
        told = outcomes(WINDOW_SUBJECT)
        check(f"inside the window: after the missed call's time, its session was told only that you called back: {told}",
              len(told) == 1 and told[0].startswith(f'The developer called back about "{WINDOW_SUBJECT}"'))  # fmt: skip

        # You called, so the voice picks up: it doesn't introduce itself or say it's calling.
        calling = [o for o in openings if INTRODUCES.search(o)]
        check(f"no opening introduces the voice or says it's calling: {calling}", not calling)

        end = time.monotonic() + 5
        while haikus() and time.monotonic() < end:
            await asyncio.sleep(0.1)
        # A session's folder is deleted just after its process has ended.
        await asyncio.sleep(1)
        left = sorted(p.name for p in kept(own))
        check(f"no Haiku left, and none of its sessions' folders in ~/.claude/projects: {left} "
              f"(up to {during} during a call back)",
              not haikus() and not left and during > 0)  # fmt: skip
    finally:
        if collecting:
            collecting.cancel()
        await socket_side.close()
        voice.terminate()
        voice.wait()
        await switchboard.cleanup()

    print("--- voice log")
    print("".join(line for line in voice_log.open() if re.search(r"call |voice listening|models loaded|session ", line)))
    print("\n".join(lines))
    print("\nwhat the voice said first on each call back:")
    print("\n".join(f"  {o}" for o in openings + replies))
    if timings:
        spread = f"median {statistics.median(timings):.2f}, {min(timings):.2f}–{max(timings):.2f}"
        print(f"\nfrom the offer to the voice's first audio (s): {spread}: {', '.join(f'{v:.2f}' for v in timings)}")
    if args.keep:
        shutil.copytree(tmp, args.keep, dirs_exist_ok=True, ignore=shutil.ignore_patterns("*.sock"))
    shutil.rmtree(tmp, ignore_errors=True)
    shutil.rmtree(own, ignore_errors=True)
    return 1 if fails else 0


# Weak signal (--signal): known sentences, said on calls whose line loses some of the phone's
# packets.
SENTENCES = Path(__file__).parents[1] / "stt-eval" / "sentences.txt"
# Left out: Haiku could take it for asking for a better model, and hand the call to Opus.
NOT_SAID = "Use the Haiku model for the voice and keep Opus for the session."
GAP = 2.5  # seconds of silence after each sentence, past the end of your turn (1.5 s)
# Bytes each packet gains outside SRTP through Nebula over IPv4: UDP and IP inside Nebula (28),
# Nebula's header (16) and tag (16), and UDP and IP outside (28).
NEBULA = 88
PHONE_OPUS: dict = {}  # how the stand-in phone's encoder runs, set from each answer


class PhoneOpus(OpusEncoder):
    """The phone's Opus encoder, as Google's WebRTC sets it up from the Mac's answer (`phone_opus`):
    mono, and with forward error correction if the answer has useinbandfec=1, set for the loss
    the Mac reports, as WebRTC sets it (here, the loss the check makes). aiortc's own encoder
    sends 96 kbps stereo."""

    def __init__(self):
        self.codec = av.CodecContext.create("libopus", "w")
        self.codec.bit_rate = PHONE_OPUS.get("bitrate", 32000)
        self.codec.format = "s16"
        self.codec.layout = "mono"
        self.codec.sample_rate = RATE
        self.codec.time_base = fractions.Fraction(1, RATE)
        options = {"application": "voip"}
        if PHONE_OPUS.get("cutoff"):
            options["cutoff"] = str(PHONE_OPUS["cutoff"])
        if PHONE_OPUS.get("fec") and PHONE_OPUS.get("loss"):
            options |= {"fec": "1", "packet_loss": str(PHONE_OPUS["loss"])}
        self.codec.options = options
        self.resampler = av.AudioResampler(format="s16", layout="mono", rate=RATE, frame_size=int(RATE * TICK))
        self.first_packet_pts = None


def phone_opus(answer: str, loss: float) -> dict:
    """How Google's WebRTC sets up the phone's Opus encoder from the answer's Opus parameters:
    the bitrate is maxaveragebitrate, or else 32 kbps, or 20 kbps if maxplaybackrate is 16 kHz
    or less; the band is what maxplaybackrate allows; FEC is on with useinbandfec=1."""
    rtpmap = re.search(r"a=rtpmap:(\d+) opus/48000/2", answer, re.I)
    line = rtpmap and re.search(rf"a=fmtp:{rtpmap[1]} (\S+)", answer)
    fmtp = dict(p.split("=", 1) for p in line[1].split(";") if "=" in p) if line else {}
    playback = int(fmtp.get("maxplaybackrate", 48000))
    default = 12000 if playback <= 8000 else 20000 if playback <= 16000 else 32000
    cutoff = next((c for rate, c in ((8000, 4000), (12000, 6000), (16000, 8000), (24000, 12000)) if playback <= rate), 0)
    return {
        "bitrate": int(fmtp.get("maxaveragebitrate", default)), "cutoff": cutoff,
        "fec": fmtp.get("useinbandfec") == "1", "loss": round(loss),
    }  # fmt: skip


def words(text: str) -> list[str]:
    return re.findall(r"[a-z0-9']+", text.lower().replace("-", " "))


def wrong(said: list[str], heard: list[str]) -> int:
    """Words heard wrong, missed or added: the edit distance in words."""
    row = list(range(len(heard) + 1))
    for i, w in enumerate(said, 1):
        diagonal, row[0] = row[0], i
        for j, h in enumerate(heard, 1):
            diagonal, row[j] = row[j], min(row[j] + 1, row[j - 1] + 1, diagonal + (w != h))
    return row[-1]


def rates(packets: list[tuple[float, int]], frames: list[tuple[float, bool]]) -> tuple[float, float, float]:
    """kbps of Opus over the call, and on average in the seconds where at least 3 in 4 frames
    had speech, and where none had. packets are when each went or came and its bytes of Opus;
    frames when each frame went or came and whether it had speech."""
    start, end = packets[0][0], packets[-1][0]
    total = sum(b for _, b in packets) * 8 / max(end - start, 0.001) / 1000
    speaking, silent = [], []
    for second in np.arange(start, end - 1, 1.0):
        inside = [s for t, s in frames if second <= t < second + 1]
        kbps = sum(b for t, b in packets if second <= t < second + 1) * 8 / 1000
        if inside and sum(inside) >= 0.75 * len(inside):
            speaking.append(kbps)
        elif inside and not any(inside):
            silent.append(kbps)
    mean = lambda values: statistics.mean(values) if values else float("nan")
    return total, mean(speaking), mean(silent)


def spoken(kokoro, text: str) -> np.ndarray:
    audio = np.concatenate([np.array(r.audio) for r in kokoro.generate(text, voice=YOUR_VOICE, lang_code="a")])
    return (np.clip(soxr.resample(audio, 24000, RATE), -1, 1) * 32767).astype(np.int16)


async def weak_signal(args) -> int:
    """Says known sentences on calls rung by hand whose line loses some of the phone's packets,
    and compares what the voice server heard with what was said."""
    import aiortc.codecs

    aiortc.codecs.OpusEncoder = PhoneOpus
    losses = [float(x) for x in args.loss.split(",")]
    said = [line for line in SENTENCES.read_text().splitlines() if line.strip() and line != NOT_SAID]
    reference = [w for line in said for w in words(line)]
    tmp = Path(tempfile.mkdtemp(prefix="ringback-phone-check-"))
    voice_log = tmp / "voice.log"
    (tmp / "report.txt").write_text(BRIEFING)
    voice = subprocess.Popen(
        [sys.executable, "-m", "ringback_voice", "serve", "--listen", VOICE, "--socket", str(tmp / "voice.sock"),
         "--report", str(tmp / "report.txt")],
        stdout=voice_log.open("w"), stderr=subprocess.STDOUT,
    )  # fmt: skip
    results: list[dict] = []

    async def lossy_call(run: int, loss: float, clips: list[np.ndarray]) -> dict:
        # The same packets are lost for the same run and loss, counted from the call's first.
        dice = random.Random(f"{run} {loss}")
        sent: list[tuple[float, int]] = []  # when each RTP packet went, and its bytes of Opus
        received: list[tuple[float, int]] = []
        headers: list[int] = []  # bytes each packet the phone sent gained from RTP and SRTP
        dropped = 0
        phone = Phone()

        def on_answer(sdp: str) -> None:
            PHONE_OPUS.clear()
            PHONE_OPUS.update(phone_opus(sdp, loss))
            transport = phone.pc.getSenders()[0].transport
            send = transport._send_rtp
            receiver = phone.pc.getReceivers()[0]
            handle = receiver._handle_rtp_packet

            async def lossy(data: bytes) -> None:
                nonlocal dropped
                if is_rtcp(data):
                    return await send(data)
                opus = len(RtpPacket.parse(data).payload)
                sent.append((time.monotonic(), opus))
                if dice.random() < loss / 100:
                    dropped += 1
                    return
                before = transport._RTCDtlsTransport__tx_bytes
                await send(data)
                headers.append(transport._RTCDtlsTransport__tx_bytes - before - opus)

            async def heard(packet, arrival_time_ms):
                received.append((time.monotonic(), len(packet.payload)))
                await handle(packet, arrival_time_ms)

            transport._send_rtp = lossy
            receiver._handle_rtp_packet = heard

        phone.on_answer = on_answer
        call_id = secrets.token_hex(8)
        t = time.monotonic()
        ok = await phone.offer(call_id, ringing=False) == 200
        ok = ok and await phone.heard_after(t) is not None
        await phone.quiet_for(2.5, limit=60)
        ends: list[datetime] = []  # when the speech in each sentence ended
        for clip in clips:
            phone.mic.say(clip)
            while len(phone.mic._pending):
                await asyncio.sleep(0.01)
            # Kokoro ends a sentence in silence, which the voice server may stop hearing it in
            # before it has all gone: count from the last sample louder than that.
            quiet = len(clip) - 1 - np.flatnonzero(np.abs(clip.astype(np.int32)) > LOUD)[-1]
            ends.append(datetime.now() - timedelta(seconds=quiet / RATE))
            await asyncio.sleep(GAP)
        await asyncio.sleep(1)
        await phone.hang_up()
        found = await wait_for(voice_log, rf"call {call_id[:8]}: finished", WAIT)
        log = voice_log.read_text()
        today = datetime.now().date()
        you = [
            (datetime.combine(today, datetime.strptime(stamp, "%H:%M:%S.%f").time()), ast.literal_eval(text))
            for stamp, text in re.findall(rf"^(\S+)  call {call_id[:8]}: you \(.*?\): (.+)$", log, re.M)
        ]
        heard = [w for _, text in you for w in words(text)]
        # From the end of the speech in each sentence to the first words heard after it, before the
        # next one's ended.
        delays = []
        for end, after in zip(ends, ends[1:] + [datetime.max]):
            first = next((stamp for stamp, _ in you if end < stamp < after), None)
            if first:
                delays.append((first - end).total_seconds())
        stats = re.search(rf"call {call_id[:8]}: (audio for .+)$", log, re.M)
        header = statistics.mean(headers) if headers else 0
        wire = lambda packets: (
            (sum(b for _, b in packets) + len(packets) * (header + NEBULA)) * 8 / (packets[-1][0] - packets[0][0]) / 1000
        )
        return {
            "run": run, "loss": loss, "ok": ok and found is not None, "dropped": dropped, "sent": len(sent),
            "wrong": wrong(reference, heard), "words": len(reference), "delays": delays,
            "up": rates(sent, phone.mic.frames), "down": rates(received, phone.heard),
            "up_pps": len(sent) / (sent[-1][0] - sent[0][0]), "down_pps": len(received) / (received[-1][0] - received[0][0]),
            "up_wire": wire(sent), "down_wire": wire(received), "header": header,
            "voice": stats[1] if stats else "no stats", "heard": " / ".join(text for _, text in you),
            "encoder": dict(PHONE_OPUS),
        }  # fmt: skip

    try:
        from mlx_audio.tts.utils import load_model

        kokoro = load_model(KOKORO)
        clips = [spoken(kokoro, line) for line in said]
        if not await wait_for(voice_log, r"voice listening", 180):
            print(voice_log.read_text())
            sys.exit("The voice server didn't start.")
        for run in range(1, args.runs + 1):
            for loss in losses:
                results.append(r := await lossy_call(run, loss, clips))
                heard = statistics.median(r["delays"] or [float("nan")])
                print(f"loss {loss:g}%, run {run}: {r['wrong']} of {r['words']} words wrong, heard {heard:.2f} s after", flush=True)
    finally:
        voice.terminate()
        voice.wait()

    print("--- voice log")
    print("".join(line for line in voice_log.open() if re.search(r"call |voice listening|models loaded", line)))
    seconds = sum(len(c) for c in clips) / RATE
    print(f"{len(said)} sentences, {len(reference)} words, {seconds:.0f} s of speech, {GAP:g} s apart")
    for r in results:
        up, down = r["up"], r["down"]
        print(f"\nloss {r['loss']:g}%, run {r['run']}{'' if r['ok'] else ' (FAILED: no voice, or the call did not end)'}: "
              f"{r['dropped']} of the phone's {r['sent']} packets lost ({100 * r['dropped'] / max(r['sent'], 1):.1f}%); "
              f"{r['wrong']} of {r['words']} words heard wrong ({100 * r['wrong'] / r['words']:.1f}%); first words heard "
              f"a median {statistics.median(r['delays'] or [float('nan')]):.2f} s after the speech in each sentence ended")
        print(f"  the phone's encoder: {r['encoder']}")
        print(f"  phone to Mac: {up[0]:.1f} kbps of Opus (speaking {up[1]:.1f}, silent {up[2]:.1f}), "
              f"{r['up_pps']:.1f} packets/s, {r['header']:.0f} bytes of RTP and SRTP each, "
              f"about {r['up_wire']:.0f} kbps through Nebula")
        print(f"  Mac to phone: {down[0]:.1f} kbps of Opus (speaking {down[1]:.1f}, silent {down[2]:.1f}), "
              f"{r['down_pps']:.1f} packets/s, about {r['down_wire']:.0f} kbps through Nebula")
        print(f"  the voice server: {r['voice']}")
        print(f"  heard: {r['heard']}")
    print("\nwords heard wrong, over all runs:")
    for loss in losses:
        mine = [r for r in results if r["loss"] == loss]
        errors, total = sum(r["wrong"] for r in mine), sum(r["words"] for r in mine)
        delays = [d for r in mine for d in r["delays"]]
        print(f"  {loss:4g}% lost: {errors} of {total} ({100 * errors / total:.1f}%), "
              f"per run {', '.join(str(r['wrong']) for r in mine)}; first words heard a median "
              f"{statistics.median(delays or [float('nan')]):.2f} s after the speech in each sentence")
    if args.keep:
        shutil.copytree(tmp, args.keep, dirs_exist_ok=True, ignore=shutil.ignore_patterns("*.sock"))
    shutil.rmtree(tmp, ignore_errors=True)
    return 0 if all(r["ok"] for r in results) else 1

if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
