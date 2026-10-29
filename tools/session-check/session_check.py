#!/usr/bin/env python3
"""Plays a session's calls on this Mac, up to where the phone would ring.

    uv run --project voice tools/session-check/session_check.py [--model MODEL] [--unanswered S] [--keep DIR] [WORK]
    uv run --project voice tools/session-check/session_check.py --asks [--model MODEL] [--keep DIR]

A Claude Code session (`claude -p`) loads the ringback mod from mod/, runs /ringback and
then WORK. When WORK's turn ends, the mod has the session write its call briefing and gives
it to a voice server started here, which asks a switchboard started here to ring. That
switchboard knows only a made-up phone and has no APNs key, so nothing rings. Then a
message is left for the session, as the voice leaves what you say: the session answers it,
and since no call is live, it calls again. Neither call is answered, so after S seconds the
session is told so, in notes that start no turn. Then it's asked at the prompt what it was
told about its last call. Prints the session's turns, both servers' logs and the checks.

With --asks, the session instead asks your permission: in default mode it runs a command
that needs it, which is allowed as the voice would allow it, then another, which is
refused. Each must reach the voice server and take effect in the session. Then a second
session, in auto mode, runs a routine command, which must ask nothing. The session's own
questions (AskUserQuestion) can't be tried here: `claude -p` doesn't offer that tool.

Stop your own voice server first: both use the same socket.
"""

import argparse
import asyncio
import json
import os
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
QUIET = 10  # seconds watched after the outcome notes, for a turn or call they'd set off
QUESTION = "What were you told about how your last ringback call went? Quote it word for word."
UNANSWERED = "wasn't answered within"  # what an unanswered call's outcome says
ASKED = r"(permission|question) ([a-z]{4}) for session"  # the voice server's log of what a session asks
ALLOWED, REFUSED = "allowed.txt", "refused.txt"


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


async def until(ready, seconds: float) -> bool:
    end = time.monotonic() + seconds
    while not ready():
        if time.monotonic() > end:
            return False
        await asyncio.sleep(0.2)
    return True


def calls_in(path: Path) -> int:
    return len(re.findall(CALL, path.read_text(errors="replace")))


class Session:
    """A Claude Code session with the mod, fed prompts as stream-json."""

    def __init__(self, process: asyncio.subprocess.Process, log: Path):
        self.process = process
        self.turns: list[str] = []
        self.id = asyncio.get_running_loop().create_future()
        self._log = log.open("wb")
        self._reader = asyncio.create_task(self._read())

    @classmethod
    async def start(cls, args, project: Path, mode: str, log: Path) -> "Session":
        process = await asyncio.create_subprocess_exec(
            "claude", "-p", "--plugin-dir", str(REPO / "mod"), "--model", args.model,
            "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
            "--setting-sources", "", "--tools", "Bash", "--permission-mode", mode,
            cwd=project, stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.STDOUT, limit=1 << 22,
        )  # fmt: skip
        return cls(process, log)

    async def _read(self):
        while line := await self.process.stdout.readline():
            self._log.write(line)
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if not self.id.done() and event.get("session_id"):
                self.id.set_result(event["session_id"])
            if event.get("type") == "result":
                self.turns.append(event.get("result") or "")

    async def prompt(self, text: str) -> int:
        """Sends a prompt; returns how many turns had ended before it."""
        message = {"type": "user", "message": {"role": "user", "content": text}}
        self.process.stdin.write((json.dumps(message) + "\n").encode())
        await self.process.stdin.drain()
        return len(self.turns)

    async def close(self):
        self.process.stdin.close()
        try:
            await asyncio.wait_for(self.process.wait(), 30)
        except TimeoutError:
            self.process.kill()
        await self._reader


async def asks(args, project: Path, voice_log: Path, switchboard_log: Path, tmp: Path) -> int:
    """Permissions, through a real session and the voice server."""
    socket_side = aiohttp.ClientSession(connector=aiohttp.UnixConnector(path=str(SOCKET)))
    results: list[tuple[str, bool]] = []

    def check(name: str, ok: bool) -> None:
        results.append((name, ok))

    async def answer(session: Session, nth: int, body: dict) -> tuple[str | None, int | None]:
        """Answers the nth thing the session asked, as the voice would."""
        if not (asked := await wait_for(voice_log, ASKED, WAIT, nth)):
            return None, None
        kind, id = asked.groups()
        url = f"http://voice/sessions/{await session.id}/{kind}s/{id}"
        async with socket_side.post(url, json=body) as r:
            return kind, r.status

    try:
        # In default mode, as acceptEdits would let a command that writes a file run unasked.
        session = await Session.start(args, project, "default", tmp / "session.jsonl")
        await session.prompt("/ringback")
        await until(lambda: session.turns, WAIT)  # its answer is a turn's result too

        # A command that needs your permission, which you allow on the call.
        before = await session.prompt(f"Run exactly this with the Bash tool, nothing else: date +%s > {ALLOWED}")
        kind, status = await answer(session, 1, {"verdict": "allow", "said": "Yes, go ahead."})
        check("the permission reached the voice server, and the voice's yes was taken (200)", (kind, status) == ("permission", 200))
        check(f"allowed, the command ran ({ALLOWED} exists)", await until(lambda: (project / ALLOWED).exists(), WAIT))
        await until(lambda: len(session.turns) > before, WAIT)

        # Another, which you refuse.
        before = await session.prompt(f"Run exactly this with the Bash tool, nothing else: date +%s > {REFUSED}")
        kind, status = await answer(session, 2, {"verdict": "deny", "said": "No, don't."})
        check("the second permission reached the voice server, and the voice's no was taken (200)", (kind, status) == ("permission", 200))
        ended = await until(lambda: len(session.turns) > before, WAIT)
        check(f"refused, the command didn't run ({REFUSED} doesn't exist)", ended and not (project / REFUSED).exists())
        # The session's own words on it, not a briefing the mod asked for after a turn.
        said = next((t for t in session.turns[before:] if t.strip() and not t.startswith("Subject:")), "")
        print(f"      the session, refused: {said.strip()[:200]!r}")

        await asyncio.sleep(1)
        await session.close()

        # Auto mode settles a routine command itself, so nothing should be asked.
        asked_before = len(re.findall(ASKED, voice_log.read_text(errors="replace")))
        auto = await Session.start(args, project, "auto", tmp / "auto.jsonl")
        await auto.prompt("/ringback")
        await until(lambda: auto.turns, WAIT)
        before = await auto.prompt("Run exactly this with the Bash tool, nothing else: python3 -c 'print(6*7)'")
        ended = await until(lambda: len(auto.turns) > before, WAIT)
        ran = ended and any("42" in t for t in auto.turns[before:])
        asked_after = len(re.findall(ASKED, voice_log.read_text(errors="replace")))
        check("in auto mode, a routine command ran and asked you nothing", ran and asked_after == asked_before)
        await auto.close()
    finally:
        await socket_side.close()

    print("--- voice log (what the sessions asked, and the answers)")
    print("".join(line for line in voice_log.open() if re.search(r"permission|question|placed for", line)))
    fails = 0
    for name, ok in results:
        print(f"{'ok  ' if ok else 'FAIL'}  {name}")
        fails += not ok
    return 1 if fails else 0


async def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("work", nargs="?", default="Say in one sentence that the session check works.")
    p.add_argument("--model", default="haiku", help="the session's model (default haiku)")
    p.add_argument("--unanswered", type=int, default=10, help="seconds each call waits to be answered (default 10)")
    p.add_argument("--keep", type=Path, help="keep the logs in this directory")
    p.add_argument("--asks", action="store_true", help="check permissions instead")
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
            env={**os.environ, "RINGBACK_UNANSWERED": str(args.unanswered)},
            stdout=voice_log.open("w"), stderr=subprocess.STDOUT,
        ))  # fmt: skip
        if not await wait_for(voice_log, r"voice listening", 120):
            print(voice_log.read_text())
            sys.exit("The voice server didn't start.")

        async with aiohttp.ClientSession() as http:
            async with http.post(f"http://{VOICE}/offer", json={"call_id": "made-up", "sdp": "", "type": "offer"}) as r:
                refused = r.status
        if args.asks:
            return await asks(args, project, voice_log, switchboard_log, tmp)

        t0 = time.monotonic()
        session = await asyncio.create_subprocess_exec(
            "claude", "-p", "--plugin-dir", str(REPO / "mod"), "--model", args.model,
            "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
            "--setting-sources", "", "--tools", "",
            cwd=project, stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.STDOUT, limit=1 << 22,
        )  # fmt: skip
        stream = (tmp / "session.jsonl").open("wb")  # everything the session printed
        turns: list[str] = []
        outcomes: list[str] = []  # the calls' outcomes, as the mod showed them to you
        session_id = asyncio.get_running_loop().create_future()

        async def read():
            while line := await session.stdout.readline():
                stream.write(line)
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                if not session_id.done() and event.get("session_id"):
                    session_id.set_result(event["session_id"])
                if event.get("type") == "result":
                    turns.append(event.get("result") or "")
                text = event.get("text") or ""
                if event.get("subtype") == "ui_log" and event.get("plugin") == "ringback" and text.startswith("The call"):
                    outcomes.append(text)

        reader = asyncio.create_task(read())

        async def prompt(text: str) -> None:
            message = {"type": "user", "message": {"role": "user", "content": text}}
            session.stdin.write((json.dumps(message) + "\n").encode())
            await session.stdin.drain()

        await prompt("/ringback")
        await prompt(args.work)
        placed = await wait_for(switchboard_log, CALL, WAIT)
        took = time.monotonic() - t0
        again = left = told = quiet = reply = None
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
        if again:
            # Neither call is answered. Once each one's time is up, the session is told so
            # in a note, which mustn't start a turn: with ringback on, that would call again.
            told = await until(lambda: len(outcomes) >= 2, args.unanswered + 30)
            turns_before, calls_before = len(turns), calls_in(switchboard_log)
            await asyncio.sleep(QUIET)
            quiet = len(turns) == turns_before and calls_in(switchboard_log) == calls_before
            # Asked at the prompt, as when you're back, the session should know.
            asked = len(turns)
            await prompt(QUESTION)
            if await until(lambda: len(turns) > asked, WAIT):
                reply = turns[asked]
            await wait_for(switchboard_log, CALL, WAIT, nth=3)  # it then calls again
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
        if args.asks:
            if args.keep:
                shutil.copytree(tmp, args.keep, dirs_exist_ok=True)
            shutil.rmtree(tmp, ignore_errors=True)

    print("--- the session's turns")
    for i, text in enumerate(turns, 1):
        print(f"[{i}] {text.strip()}\n")
    print("--- switchboard log\n" + switchboard_log.read_text())
    print("--- voice log (session side)")
    print("".join(line for line in voice_log.open() if re.search(r"call |outcome for|voice listening", line)))

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
            print(f"      outcomes shown: {outcomes}")
            check(
                f"each call's outcome, not answered, was shown in the session within {args.unanswered + 30} s",
                bool(told) and all(UNANSWERED in o for o in outcomes[:2]),
            )
            check(f"the outcomes started no turn and no call in the next {QUIET} s", bool(quiet))
            if reply is not None:
                print(f"      asked {QUESTION!r}, it said: {reply.strip()!r}")
            check(
                "asked at the prompt, it quoted that its last call wasn't answered",
                reply is not None and UNANSWERED in reply.replace("\u2019", "'"),
            )
    if args.keep:
        shutil.copytree(tmp, args.keep, dirs_exist_ok=True)
    shutil.rmtree(tmp, ignore_errors=True)
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
