#!/usr/bin/env python3
"""Checks that the voice server loses nothing that matters when it restarts: it's killed
twice, once with SIGTERM and once with SIGKILL, and started again each time, with a held
call, calls to call back, what missed calls asked and messages for sessions kept in its state
file (voice-state.json, beside its socket).

    uv run --project voice tools/restart-check/restart_check.py [--unanswered S] [--reask S] [--keep DIR]

The voice server gets its own socket, state file and temporary folder, and phone-check's
ports 18797 and 18798, so it runs beside your own voice server, but not beside a phone check
or an ask check. A stand-in switchboard holds the calls of the project "held" for an hour, as
outside your work hours, and takes the others, so nothing rings. The phone's side is
phone-check's: aiortc, and your words spoken by Kokoro. Checks:
- before the first restart: a permission places a held call; a briefing places a call nobody
  answers, and a permission another; a call is answered, and a permission put to it. A long
  poll waits for the unanswered call's permission;
- killed with SIGTERM: the long poll fails, as a mod's would. Started again: the held call is
  still held, a long poll says so, its offer is refused, and it asks the switchboard again
  with the same call ID. The unanswered call's permission is still pending under its id. The
  answered call has ended: its session is told it ended when the voice server restarted, with
  its transcript, and its permission still waits, as a missed call's does;
- a message is left for a session, and the voice server killed with SIGKILL at once. Started
  again: the held call is still held and asks the switchboard again; the message and the
  missed calls' outcomes wait for their sessions; a missed call is called back and the voice
  opens with what it was about; the call placed to ask a permission is called back, the voice
  asks it, and your spoken yes allows it; the answered call that ended is called back, and
  the permission it was asked waits for it: the voice asks it, and your yes allows it;
- the state file can be read only by you, and the killed servers' Haiku processes ended.
"""

import argparse
import asyncio
import os
import re
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timedelta
from pathlib import Path

import aiohttp
import numpy as np
import soxr
from aiohttp import web

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "phone-check"))
from phone_check import (  # noqa: E402
    KOKORO, RATE, SWITCHBOARD, VOICE, YOUR_VOICE, Phone, answered, free, kept, wait_for,
)  # fmt: skip

HELD, MISSED, POOL, LIVE = "restart-held", "restart-missed", "restart-pool", "restart-live"
MISSED_SUBJECT = "Parser fix ready to push"
MISSED_BRIEFING = """\
I fixed the crash in the config parser. It choked on empty lines between sections. The fix
is two lines, with a new test, and the whole suite passes. One thing needs you: shall I
push it to a branch and open a pull request?"""
LIVE_SUBJECT = "Docs site rebuilt"
LIVE_BRIEFING = """\
I rebuilt the documentation site and fixed three broken links in the setup guide. It's all
committed. Nothing needs the developer."""
PERMISSION = {
    "tool": "Bash",
    "description": "Delete the build folder",
    "input": "rm -rf build",
    "context": "I'm cleaning up before the release build.",
    "subject": "Delete the build folder?",
}
YES = "Yes, go ahead."
LEFT = "The developer says: also update the changelog."


async def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--unanswered", type=float, default=8, help="seconds before an unanswered call is forgotten")
    p.add_argument("--reask", type=float, default=4, help="seconds before a held call asks the switchboard again")
    p.add_argument("--keep", type=Path, help="keep the voice logs, state file and transcripts in this directory")
    args = p.parse_args()
    for address in (SWITCHBOARD, VOICE):
        if not free(address):
            sys.exit(f"{address} is in use: is a phone check, an ask check or another restart check running?")

    tmp = Path(tempfile.mkdtemp(prefix="ringback-restart-check-"))
    sock, state, project = tmp / "voice.sock", tmp / "voice-state.json", tmp / "demo"
    project.mkdir()
    # The voice server's own temporary folder, so its Haiku processes and their sessions'
    # folders tell from those of other voice servers. Short: a socket's path has at most 104
    # characters.
    own = Path(tempfile.mkdtemp(prefix="rb"))
    rung: list[tuple[float, dict]] = []  # what the stand-in switchboard took, and when (time.time())

    async def take_call(request: web.Request) -> web.Response:
        body = await request.json()
        rung.append((time.time(), body))
        if body.get("project") == "held":
            held_until = (datetime.now().astimezone() + timedelta(hours=1)).isoformat(timespec="seconds")
            return web.json_response({"ok": True, "held_until": held_until})
        return web.json_response({"ok": True})

    switchboard = web.AppRunner(web.Application())
    switchboard.app.add_routes([web.post("/call", take_call)])
    await switchboard.setup()
    host, port = SWITCHBOARD.rsplit(":", 1)
    await web.TCPSite(switchboard, host, int(port)).start()

    starts = 0
    voice: subprocess.Popen | None = None

    async def start_voice() -> float:
        """Starts the voice server, and returns when it listens (time.time())."""
        nonlocal voice, starts
        starts += 1
        log = tmp / f"voice-{starts}.log"
        voice = subprocess.Popen(
            [sys.executable, "-m", "ringback_voice", "serve", "--listen", VOICE, "--switchboard", SWITCHBOARD,
             "--socket", str(sock)],
            stdout=log.open("w"), stderr=subprocess.STDOUT,
            env=os.environ | {"RINGBACK_UNANSWERED": str(args.unanswered), "RINGBACK_REASK": str(args.reask),
                              "TMPDIR": str(own)},
        )  # fmt: skip
        if not await wait_for(log, r"voice listening", 180):
            print(log.read_text())
            sys.exit("The voice server didn't start.")
        return time.time()

    def voice_log() -> Path:
        return tmp / f"voice-{starts}.log"

    def leftovers() -> int:
        """Processes of the voice servers started here: Haikus and their tools, by the
        temporary folder their command lines name."""
        found = subprocess.run(["pgrep", "-f", str(own)], capture_output=True)
        return len(found.stdout.split())

    left: list[str] = []  # Haiku session folders each killed server left in ~/.claude/projects

    async def kill(how: signal.Signals) -> float:
        """Kills the voice server, and returns how long its Haiku processes outlived it."""
        voice.send_signal(how)
        voice.wait()
        t = time.monotonic()
        while leftovers() and time.monotonic() - t < 10:
            await asyncio.sleep(0.1)
        outlived = time.monotonic() - t
        await asyncio.sleep(1)
        left.append(f"{how.name}: {len(kept(own) - sessions_before)}")
        return outlived

    def socket_side() -> aiohttp.ClientSession:
        return aiohttp.ClientSession(connector=aiohttp.UnixConnector(path=str(sock)))

    async def api(method: str, path: str, body: dict | None = None) -> tuple[int, dict]:
        async with socket_side() as http, http.request(method, f"http://voice{path}", json=body) as r:
            return r.status, await r.json(content_type=None)

    def permission(project_name: str) -> dict:
        return PERMISSION | {"project": project_name, "cwd": str(project)}

    async def placed_for(path: str, body: dict) -> tuple[dict, str]:
        """Asks or places through the socket; the answer, and the call ID the switchboard got."""
        status, answer = await api("POST", path, body)
        assert status == 200, (status, answer)
        return answer, rung[-1][1]["call_id"]

    async def asked_again(call_id: str, since: float, within: float) -> float | None:
        """Seconds after since when the switchboard was asked about call_id, or None."""
        end = time.time() + within
        while time.time() < end:
            if at := next((t for t, body in rung if t > since and body["call_id"] == call_id), None):
                return at - since
            await asyncio.sleep(0.05)
        return None

    async def messages(session: str) -> list[dict]:
        _, body = await api("GET", f"/sessions/{session}/messages")
        return body.get("messages", [])

    async def spoke_after(phone: Phone, since: float, limit: float = 45) -> bool:
        """Whether the voice spoke after since, then fell quiet for 2.5 s, within limit."""
        end = time.monotonic() + limit
        while (phone.last_audio or 0) <= since and time.monotonic() < end:
            await asyncio.sleep(0.1)
        if (phone.last_audio or 0) <= since:
            return False
        await phone.quiet_for(2.5, limit=max(0.0, end - time.monotonic()))
        return True

    async def verdict(session: str, id: str, within: float) -> dict:
        """A permission's answer once it isn't pending, or the last one after within seconds."""
        end = time.monotonic() + within
        while True:
            _, body = await api("GET", f"/sessions/{session}/permissions/{id}?wait=5")
            if body.get("verdict") != "pending" or time.monotonic() >= end:
                return body

    async def call_back(session: str, call_id: str) -> tuple[Phone, int, str]:
        """Calls call_id back, as the app does from its call history, and listens to the
        voice's opening: the phone, the offer's status, and the opening as the transcript has it.
        The session's mod collects first, so the session listens, as it does once a second."""
        await messages(session)
        before = set(project.glob(".ringback/calls/*.md"))
        phone = Phone()
        status = await phone.offer(call_id, ringing=False, back=True)
        await spoke_after(phone, 0)
        transcript = next(iter(set(project.glob(".ringback/calls/*.md")) - before), None)
        found = transcript and await wait_for(transcript, r"\*\*Voice:\*\* (.+)", 30)
        return phone, status, found[1] if found else ""

    fails = 0
    lines: list[str] = []
    openings: list[str] = []

    def check(name: str, ok: bool) -> None:
        nonlocal fails
        lines.append(f"{'ok  ' if ok else 'FAIL'}  {name}")
        fails += not ok

    sessions_before = kept(own)
    try:
        from mlx_audio.tts.utils import load_model

        kokoro = load_model(KOKORO)
        audio = np.concatenate([np.array(r.audio) for r in kokoro.generate(YES, voice=YOUR_VOICE, lang_code="a")])
        yes = (np.clip(soxr.resample(audio, 24000, RATE), -1, 1) * 32767).astype(np.int16)
        await start_voice()

        # Before the first restart.
        held, held_id = await placed_for(f"/sessions/{HELD}/permissions", permission("held"))
        held_pid = held.get("permission", "")
        check(f"a permission places a held call: {held}", held.get("call") == "held")
        _, missed_id = await placed_for(
            "/calls", {"session_id": MISSED, "project": "demo", "cwd": str(project), "subject": MISSED_SUBJECT,
                       "briefing": MISSED_BRIEFING},
        )  # fmt: skip
        asked, pool_id = await placed_for(f"/sessions/{POOL}/permissions", permission("demo"))
        pool_pid = asked.get("permission", "")
        _, live_id = await placed_for(
            "/calls", {"session_id": LIVE, "project": "demo", "cwd": str(project), "subject": LIVE_SUBJECT,
                       "briefing": LIVE_BRIEFING},
        )  # fmt: skip
        live = Phone()
        await live.offer(live_id, ringing=True)
        await asyncio.sleep(1)
        await answered(live_id, live)
        opened = await spoke_after(live, 0)
        t = time.monotonic()
        status, body = await api("POST", f"/sessions/{LIVE}/permissions", permission("demo"))
        live_pid = body.get("permission", "")
        asks_it = await spoke_after(live, t)
        check(f"a call is answered, and a permission put to it: {body}",
              opened and body.get("call") == "live" and asks_it)  # fmt: skip
        for call_id in (missed_id, pool_id):
            forgotten = rf"call {re.escape(call_id[:8])}: not answered, forgotten"
            await wait_for(voice_log(), forgotten, args.unanswered + 20)
        status, body = await api("GET", f"/sessions/{POOL}/permissions/{pool_pid}?wait=0")
        check(f"the unanswered call's permission waits: {body}", body.get("verdict") == "pending")

        # Killed with SIGTERM, while a mod's long poll waits for that permission.
        poll = asyncio.create_task(api("GET", f"/sessions/{POOL}/permissions/{pool_pid}?wait=25"))
        await asyncio.sleep(1)
        outlived = await kill(signal.SIGTERM)
        try:
            polled = await poll
        except (aiohttp.ClientError, OSError) as e:
            polled = type(e).__name__
        check(f"killed with SIGTERM, the long poll fails as a mod's would: {polled}", isinstance(polled, str))
        check(f"the killed server's Haiku processes ended ({outlived:.1f} s after it)", not leftovers())
        await live.hang_up()
        mode = stat.S_IMODE(state.stat().st_mode) if state.exists() else 0
        check(f"its state file can be read only by you: {oct(mode)}", mode == 0o600)

        listening = await start_voice()
        restored = await wait_for(voice_log(), r"restored .*", 1)
        lines.append(f"      {restored[0] if restored else 'nothing restored'}")
        status, body = await api("GET", f"/sessions/{HELD}/permissions/{held_pid}?wait=0")
        check(f"SIGTERM: the held call's permission still waits, held: {body}",
              status == 200 and body.get("verdict") == "pending" and body.get("held") is True)  # fmt: skip
        phone = Phone()
        refused = await phone.offer(held_id, ringing=True)
        await phone.hang_up()
        check(f"SIGTERM: an offer for the held call is still refused: {refused}", refused == 403)
        after = await asked_again(held_id, listening - 1, args.reask + 3)
        check(f"SIGTERM: the held call asks the switchboard again with its ID, {after or 0:.2f} s after the start",
              after is not None)  # fmt: skip
        status, body = await api("GET", f"/sessions/{POOL}/permissions/{pool_pid}?wait=1")
        check(f"SIGTERM: a long poll for the unanswered call's permission carries on: {status} {body}",
              status == 200 and body.get("verdict") == "pending")  # fmt: skip
        told = [m for m in await messages(LIVE) if m.get("kind") == "outcome"]
        text = told[0]["text"] if told else ""
        check(f"SIGTERM: the answered call's session is told it ended with the restart: {told}",
              len(told) == 1 and text.startswith(f'The call about "{LIVE_SUBJECT}" was answered, and ended when the '
                                                 "voice server restarted. Its transcript: .ringback/calls/")
              and (project / told[0]["transcript"]).exists())  # fmt: skip
        status, body = await api("GET", f"/sessions/{LIVE}/permissions/{live_pid}?wait=0")
        check(f"SIGTERM: the permission put to it still waits, as a missed call's does: {body}",
              body.get("verdict") == "pending")  # fmt: skip

        # A message left, and the voice server killed with SIGKILL at once.
        status, _ = await api("POST", f"/sessions/{MISSED}/messages", {"text": LEFT})
        outlived = await kill(signal.SIGKILL)
        check(f"killed with SIGKILL right after a message was left ({status})", status == 200)

        listening = await start_voice()
        restored = await wait_for(voice_log(), r"restored .*", 1)
        lines.append(f"      {restored[0] if restored else 'nothing restored'}")
        status, body = await api("GET", f"/sessions/{HELD}/permissions/{held_pid}?wait=0")
        after = await asked_again(held_id, listening - 1, args.reask + 3)
        check(f"SIGKILL: the held call is still held, and asks the switchboard again with its ID: {body}",
              body.get("held") is True and after is not None)  # fmt: skip
        waiting = await messages(MISSED)
        check(f"SIGKILL: the message left and the missed call's outcome wait for its session: {waiting}",
              [m.get("kind") for m in waiting] == ["outcome", "message"] and waiting[1]["text"] == LEFT
              and "wasn't answered" in waiting[0]["text"])  # fmt: skip
        waiting = await messages(POOL)
        check(f"SIGKILL: so does the outcome of the call placed to ask: {[m['text'] for m in waiting]}",
              len(waiting) == 1 and "wasn't answered" in waiting[0]["text"])  # fmt: skip

        phone, status, opening = await call_back(MISSED, missed_id)
        openings.append(f"missed call: {opening}")
        check(f"SIGKILL: the missed call is called back ({status}), and the voice opens with what it was about",
              status == 200 and re.search(r"parser|empty line|push", opening, re.I) is not None)  # fmt: skip
        await phone.hang_up()

        phone, status, opening = await call_back(POOL, pool_id)
        openings.append(f"the call placed to ask: {opening}")
        phone.mic.say(yes)
        body = await verdict(POOL, pool_pid, 45)
        check(f'SIGKILL: the call placed to ask is called back ({status}), the voice asks it, and "{YES}" allows it: '
              f"{body}", status == 200 and body.get("verdict") == "allow")  # fmt: skip
        await phone.quiet_for(2.5, limit=30)
        await phone.hang_up()

        phone, status, opening = await call_back(LIVE, live_id)
        openings.append(f"the call that ended: {opening}")
        # The permission comes as a note after the opening, and Haiku's second reply asks it.
        await wait_for(voice_log(), rf"call {re.escape(live_id[:8])}: Haiku: first words after", 45, nth=2)
        await phone.quiet_for(2.5, limit=45)
        phone.mic.say(yes)
        body = await verdict(LIVE, live_pid, 45)
        check(f'SIGKILL: the call that ended is called back ({status}), the permission it was asked goes to it, and '
              f'"{YES}" allows it: {body}', status == 200 and body.get("verdict") == "allow")  # fmt: skip
        await phone.quiet_for(2.5, limit=30)
        await phone.hang_up()
    finally:
        if voice and voice.poll() is None:
            # The last call's Haiku closes and deletes its folder first.
            end = time.monotonic() + 5
            while leftovers() and time.monotonic() < end:
                await asyncio.sleep(0.1)
            voice.terminate()
            voice.wait()
        await switchboard.cleanup()

    # A Haiku whose voice server was killed mid-call leaves its session's folder (Risks in
    # docs/design.md); they're counted after each kill, then removed.
    await asyncio.sleep(1)
    folders = sorted(p.name for p in kept(own) - sessions_before)
    lines.append(f"      Haiku session folders left in ~/.claude/projects after each kill, in all: {', '.join(left)}; "
                 f"at the end: {len(folders)}, removed")  # fmt: skip
    home = Path(os.environ.get("CLAUDE_CONFIG_DIR") or "~/.claude").expanduser()
    for name in folders:
        shutil.rmtree(home / "projects" / name, ignore_errors=True)
    print("--- voice logs")
    for n in range(1, starts + 1):
        print(f"start {n}:")
        log = tmp / f"voice-{n}.log"
        print("".join(line for line in log.open() if re.search(r"call |voice listening|restored|session ", line)))
    print("what the voice said first on each call back:")
    print("\n".join(f"  {o}" for o in openings))
    print("\n".join(lines))
    if args.keep:
        shutil.copytree(tmp, args.keep, dirs_exist_ok=True, ignore=shutil.ignore_patterns("*.sock"))
    shutil.rmtree(tmp, ignore_errors=True)
    shutil.rmtree(own, ignore_errors=True)
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
