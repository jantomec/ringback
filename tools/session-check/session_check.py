#!/usr/bin/env python3
"""Plays a session's calls on this Mac, up to where the phone would ring.

    uv run --project voice tools/session-check/session_check.py [--model MODEL] [--keep DIR] [WORK]

A Claude Code session (`claude -p`) loads the ringback mod from mod/, runs /ringback and
then WORK. When WORK's turn ends, the mod has the session write its call briefing and gives
it to a voice server started here, which asks a switchboard started here to ring. That
switchboard knows only a made-up phone and has no APNs key, so nothing rings. Then a
message is left for the session, as the voice leaves what you say: the session answers it,
and since no call is live, it calls again. Prints the session's turns, both servers' logs
and the checks.

Stop your own voice server first: both use the same socket.
"""

import argparse
import asyncio
import json
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import aiohttp

REPO = Path(__file__).resolve().parents[2]
SOCKET = Path("~/.local/state/ringback/voice.sock").expanduser()
SWITCHBOARD = "127.0.0.1:18787"
VOICE = "127.0.0.1:18788"
PHONE = "0" * 64  # a made-up device token
CALL = r"call ([\w-]{8}): asked by (\S+) for (\S+): ?(.*)"
WAIT = 180  # seconds for the session to place each call
MESSAGE = "The developer says: reply with the single word pineapple, and nothing else."


def socket_in_use() -> bool:
    if not SOCKET.exists():
        return False
    with socket.socket(socket.AF_UNIX) as s:
        try:
            s.connect(str(SOCKET))
            return True
        except OSError:
            return False


async def wait_for(path: Path, pattern: str, seconds: float, nth: int = 1) -> re.Match | None:
    """The nth match of pattern in the file at path, once there is one."""
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if path.exists():
            found = list(re.finditer(pattern, path.read_text(errors="replace")))
            if len(found) >= nth:
                return found[nth - 1]
        await asyncio.sleep(0.2)
    return None


async def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("work", nargs="?", default="Say in one sentence that the session check works.")
    p.add_argument("--model", default="haiku", help="the session's model (default haiku)")
    p.add_argument("--keep", type=Path, help="keep the logs in this directory")
    args = p.parse_args()
    if socket_in_use():
        sys.exit(f"A voice server is listening on {SOCKET}: stop it first.")

    tmp = Path(tempfile.mkdtemp(prefix="ringback-session-check-"))
    home = tmp / "switchboard-home"
    (home / ".config/ringback").mkdir(parents=True)
    (home / ".config/ringback/switchboard.toml").write_text(f'listen = "{SWITCHBOARD}"  # session check\n')
    (home / ".local/state/ringback").mkdir(parents=True)
    (home / ".local/state/ringback/devices.json").write_text(
        json.dumps({PHONE: {"environment": "sandbox", "device": "made-up phone"}})
    )
    project = tmp / "demo"
    project.mkdir()
    switchboard_log, voice_log = tmp / "switchboard.log", tmp / "voice.log"

    built = subprocess.run(
        ["swift", "build", "-c", "release", "--arch", "arm64", "--arch", "x86_64", "--show-bin-path"],
        cwd=REPO / "switchboard", capture_output=True, text=True, check=True,
    )  # fmt: skip
    binary = Path(built.stdout.strip()) / "ringback-switchboard"
    procs = []
    try:
        procs.append(subprocess.Popen(
            [binary, "serve"], env={"HOME": str(home), "PATH": "/usr/bin:/bin"},
            stdout=switchboard_log.open("w"), stderr=subprocess.STDOUT,
        ))  # fmt: skip
        procs.append(subprocess.Popen(
            [sys.executable, "-m", "ringback_voice", "serve", "--listen", VOICE, "--switchboard", SWITCHBOARD],
            stdout=voice_log.open("w"), stderr=subprocess.STDOUT,
        ))  # fmt: skip
        if not await wait_for(voice_log, r"voice listening", 120):
            print(voice_log.read_text())
            sys.exit("The voice server didn't start.")

        async with aiohttp.ClientSession() as http:
            async with http.post(f"http://{VOICE}/offer", json={"call_id": "made-up", "sdp": "", "type": "offer"}) as r:
                refused = r.status

        t0 = time.monotonic()
        session = await asyncio.create_subprocess_exec(
            "claude", "-p", "--plugin-dir", str(REPO / "mod"), "--model", args.model,
            "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
            "--setting-sources", "", "--tools", "",
            cwd=project, stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.STDOUT, limit=1 << 22,
        )  # fmt: skip
        turns: list[str] = []
        session_id = asyncio.get_running_loop().create_future()

        async def read():
            while line := await session.stdout.readline():
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                if not session_id.done() and event.get("session_id"):
                    session_id.set_result(event["session_id"])
                if event.get("type") == "result":
                    turns.append(event.get("result") or "")

        reader = asyncio.create_task(read())
        for text in ["/ringback", args.work]:
            message = {"type": "user", "message": {"role": "user", "content": text}}
            session.stdin.write((json.dumps(message) + "\n").encode())
        await session.stdin.drain()
        placed = await wait_for(switchboard_log, CALL, WAIT)
        took = time.monotonic() - t0
        again = left = None
        if placed:
            # As the voice leaves what you say on a call. No call is live, so when the
            # session has answered, it should call again.
            connector = aiohttp.UnixConnector(path=str(SOCKET))
            async with aiohttp.ClientSession(connector=connector) as http:
                url = f"http://voice/sessions/{await session_id}/messages"
                async with http.post(url, json={"text": MESSAGE}) as r:
                    left = r.status
            t1 = time.monotonic()
            again = await wait_for(switchboard_log, CALL, WAIT, nth=2)
            took_again = time.monotonic() - t1
        await asyncio.sleep(1)  # the switchboard's push
        session.stdin.close()
        try:
            await asyncio.wait_for(session.wait(), 30)
        except TimeoutError:
            session.kill()
        await reader
    finally:
        for proc in procs:
            proc.terminate()
            proc.wait()

    print("--- the session's turns")
    for i, text in enumerate(turns, 1):
        print(f"[{i}] {text.strip()}\n")
    print("--- switchboard log\n" + switchboard_log.read_text())
    print("--- voice log (session side)")
    print("".join(line for line in voice_log.open() if re.search(r"call |voice listening", line)))

    fails = 0

    def check(name: str, ok: bool) -> None:
        nonlocal fails
        print(f"{'ok  ' if ok else 'FAIL'}  {name}")
        fails += not ok

    check("an offer for a call the voice server didn't place is refused (403)", refused == 403)
    check(f"the session placed a call within {WAIT} s", placed is not None)
    if placed:
        call, voice, name, subject = placed.groups()
        sb = switchboard_log.read_text()
        print(f"      after {took:.0f} s, subject {subject!r}")
        check("the call names this voice server", voice == f"http://{VOICE}")
        check("the call names the project", name == project.name)
        check("the subject has 3 to 8 words", 3 <= len(subject.split()) <= 8)
        check("the voice server placed that call", f"call {call}: placed for demo" in voice_log.read_text())
        check("the push went out with that call's ID (to the made-up phone)", f"call {call} → made-up phone:" in sb)
        check("the message for the session was left (200)", left == 200)
        check("the session answered the message", any("pineapple" in t.lower() for t in turns))
        check(f"with no call live, the session called again within {WAIT} s", again is not None)
        if again:
            print(f"      after {took_again:.0f} s, subject {again.group(4)!r}")
            check("the second call has a new ID", again.group(1) != call)
    if args.keep:
        shutil.copytree(tmp, args.keep, dirs_exist_ok=True)
    shutil.rmtree(tmp, ignore_errors=True)
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
