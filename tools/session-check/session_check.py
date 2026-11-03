#!/usr/bin/env python3
"""Plays a session's calls on this Mac through ringback's real agent and hub, up to where the
phone would ring.

    uv run --project tools tools/session-check/session_check.py [--model MODEL] [--unanswered S] [--keep DIR] [WORK]
    uv run --project tools tools/session-check/session_check.py --asks [--model MODEL] [--keep DIR]

A Claude Code session (`claude -p`) loads the ringback mod from mod/, runs /ringback and
then WORK. When WORK's turn ends, the mod has the session write its call briefing and gives
it to an agent (agent/) started here, which asks a hub (hub/) started here to ring. The hub
knows only a made-up phone, registered with its On-device voice on so that calls ring with no
speech node, and has no APNs key, so it logs the push it would send and nothing rings. Then a
message is left for the session, as the voice leaves what you say: the session answers it,
and since no call is live, it calls again. Neither call is answered, so after S seconds the
session is told so, in notes that start no turn. Then it's asked at the prompt what it was
told about its last call. Prints the session's turns, the hub's and the agent's logs and the
checks.

With --asks, the session instead asks your permission: in default mode it runs a command
that needs it, which is allowed as the voice would allow it, then another, which is
refused. Each must reach the agent and take effect in the session. Then a second session, in
auto mode, runs a routine command, which must ask nothing. The session's own questions
(AskUserQuestion) can't be tried here: `claude -p` doesn't offer that tool.

Everything runs in a throwaway folder under /tmp, with its own HOME for the agent and the
session, so the mod reaches this check's agent and not yours, which can keep running. Your
login keychain's folder is linked into that HOME, so Claude Code finds your login.
"""

import argparse
import asyncio
import json
import os
import re
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import aiohttp

REPO = Path(__file__).resolve().parents[2]
DEVICE = "Check phone"  # the made-up phone the hub knows
PHONE = secrets.token_hex(32)  # its made-up push token
CALL = r"(?m)call ([\w-]{8}): asked by (\S+) for (\S+?)(?:: (.*))?$"  # the hub's log of a call
WAIT = 180  # seconds for the session to place each call
MESSAGE = "The developer says: reply with the single word pineapple, and nothing else."
QUIET = 10  # seconds watched after the outcome notes, for a turn or call they'd set off
QUESTION = "What were you told about how your last ringback call went? Quote it word for word."
UNANSWERED = "wasn't answered within"  # what an unanswered call's outcome says
ASKED = r"call ([\w-]{8}): (permission|question) ([a-z]{4}) for session \S+, (\w+):"  # the agent's log of what a session asks
ALLOWED, REFUSED = "allowed.txt", "refused.txt"
OUTLIVED = 30  # seconds what the agent started may run on once it has stopped


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def without_claude(**extra: str) -> dict[str, str]:
    """This environment, as from an ordinary terminal: without what a Claude Code session that
    runs this check would pass on (tools/env-check)."""
    return {k: v for k, v in os.environ.items() if not k.startswith("CLAUDE")} | extra


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


async def outlived(tmp: Path, seconds: float = OUTLIVED) -> tuple[list[str], float]:
    """What the agent started (the calls' Haiku and their tools) and left running once it
    stopped: the commands, and how long they ran on. Whatever still runs after `seconds` is
    killed."""
    t, seen = time.monotonic(), {}
    while time.monotonic() - t < seconds:
        pids = subprocess.run(["pgrep", "-f", str(tmp)], capture_output=True, text=True).stdout.split()
        if not pids:
            break
        for pid in pids:
            command = subprocess.run(["ps", "-o", "command=", "-p", pid], capture_output=True, text=True).stdout.split()
            seen.setdefault(pid, " ".join([Path(command[0]).name, *command[1:2]]) if command else "?")
        await asyncio.sleep(0.2)
    for pid in subprocess.run(["pgrep", "-f", str(tmp)], capture_output=True, text=True).stdout.split():
        subprocess.run(["kill", pid])
    return list(seen.values()), time.monotonic() - t


class Session:
    """A Claude Code session with the mod, fed prompts as stream-json."""

    def __init__(self, process: asyncio.subprocess.Process, log: Path):
        self.process = process
        self.turns: list[str] = []
        self.outcomes: list[str] = []  # the calls' outcomes, as the mod showed them to you
        self.id = asyncio.get_running_loop().create_future()
        self._log = log.open("wb")
        self._reader = asyncio.create_task(self._read())

    @classmethod
    async def start(cls, args, project: Path, home: Path, tools: str, log: Path, mode: str | None = None) -> "Session":
        process = await asyncio.create_subprocess_exec(
            "claude", "-p", "--plugin-dir", str(REPO / "mod"), "--model", args.model,
            "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
            "--setting-sources", "", "--strict-mcp-config", *(["--permission-mode", mode] if mode else []),
            "--tools", tools,
            cwd=project, env=without_claude(HOME=str(home)), stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.STDOUT, limit=1 << 22,
        )  # fmt: skip
        return cls(process, log)

    async def _read(self):
        while line := await self.process.stdout.readline():
            self._log.write(line)
            self._log.flush()
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if not self.id.done() and event.get("session_id"):
                self.id.set_result(event["session_id"])
            if event.get("type") == "result":
                self.turns.append(event.get("result") or "")
            text = event.get("text") or ""
            if event.get("subtype") == "ui_log" and event.get("plugin") == "ringback" and text.startswith("The call"):
                self.outcomes.append(text)

    async def prompt(self, text: str) -> int:
        """Sends a prompt; returns how many turns had ended before it."""
        message = {"type": "user", "message": {"role": "user", "content": text}}
        self.process.stdin.write((json.dumps(message) + "\n").encode())
        await self.process.stdin.drain()
        return len(self.turns)

    def answer(self, before: int) -> str | None:
        """The session's answer to the prompt sent when `before` turns had ended: the first turn
        since that isn't a briefing, which the mod has the session write after a turn and
        which can end first."""
        return next((t for t in self.turns[before:] if not t.startswith("Subject:")), None)

    async def close(self):
        self.process.stdin.close()
        try:
            await asyncio.wait_for(self.process.wait(), 30)
        except TimeoutError:
            self.process.kill()
        await self._reader
        self._log.close()


async def asks(args, project: Path, home: Path, sock: Path, hub_log: Path, agent_log: Path, tmp: Path) -> int:
    """Permissions, through a real session and the agent."""
    socket_side = aiohttp.ClientSession(connector=aiohttp.UnixConnector(path=str(sock)))
    results: list[tuple[str, bool]] = []

    def check(name: str, ok: bool) -> None:
        results.append((name, ok))

    async def answer(session: Session, nth: int, body: dict) -> tuple[str | None, str | None, int | None, str | None]:
        """Answers the nth thing the session asked, as the voice would. Returns what it was,
        how it was put to you, the agent's status for the answer, and the call it was put to."""
        if not (asked := await wait_for(agent_log, ASKED, WAIT, nth)):
            return None, None, None, None
        call, kind, id, how = asked.groups()
        url = f"http://voice/sessions/{await session.id}/{kind}s/{id}"
        async with socket_side.post(url, json=body) as r:
            return kind, how, r.status, call

    def placed_for(call: str | None) -> str | None:
        """The subject the agent placed a call with, if the hub rang it."""
        placed = re.search(rf"call {re.escape(call or '')}: placed for \S+ \(\S+\): (.*)", agent_log.read_text())
        rang = re.search(rf"call {re.escape(call or '')} → {DEVICE}:", hub_log.read_text())
        return placed[1] if placed and rang else None

    t0 = time.monotonic()
    try:
        # In default mode, as acceptEdits would let a command that writes a file run unasked.
        session = await Session.start(args, project, home, "Bash", tmp / "session.jsonl", "default")
        await session.prompt("/ringback")
        await until(lambda: session.turns, WAIT)  # its answer is a turn's result too

        # A command that needs your permission, which you allow on the call.
        before = await session.prompt(f"Run exactly this with the Bash tool, nothing else: date +%s > {ALLOWED}")
        t = time.monotonic()
        kind, how, status, call = await answer(session, 1, {"verdict": "allow", "said": "Yes, go ahead."})
        check(f"the permission reached the agent, and the voice's yes was taken: {kind}, {status}", (kind, status) == ("permission", 200))
        subject = placed_for(call)
        check(f"with no call yet, the agent placed one to ask it ({how}), and the hub rang it: {subject!r}",
              how == "placed" and subject == "Permission to run a shell command")  # fmt: skip
        ran = await until(lambda: (project / ALLOWED).exists(), WAIT)
        check(f"allowed, the command ran ({ALLOWED} exists, {time.monotonic() - t:.0f} s after the prompt)", ran)
        await until(lambda: session.answer(before) is not None, WAIT)

        # Another, which you refuse.
        before = await session.prompt(f"Run exactly this with the Bash tool, nothing else: date +%s > {REFUSED}")
        kind, how, status, call = await answer(session, 2, {"verdict": "deny", "said": "No, don't."})
        check(f"the second permission reached the agent ({how}, on the call with subject {placed_for(call)!r}), and the voice's no was taken: {status}",
              (kind, status) == ("permission", 200))  # fmt: skip
        ended = await until(lambda: session.answer(before) is not None, WAIT)
        check(f"refused, the command didn't run ({REFUSED} doesn't exist once the session has answered)",
              ended and not (project / REFUSED).exists())  # fmt: skip
        # The session's own words on it, not a briefing the mod asked for after a turn.
        print(f"      the session, refused: {(session.answer(before) or '').strip()[:300]!r}")

        await asyncio.sleep(1)
        await session.close()

        # Auto mode settles a routine command itself, so nothing should be asked.
        asked_before = len(re.findall(ASKED, agent_log.read_text(errors="replace")))
        auto = await Session.start(args, project, home, "Bash", tmp / "auto.jsonl", "auto")
        await auto.prompt("/ringback")
        await until(lambda: auto.turns, WAIT)
        before = await auto.prompt("Run exactly this with the Bash tool, nothing else: python3 -c 'print(6*7)'")
        await until(lambda: auto.answer(before) is not None, WAIT)
        ran = "42" in (auto.answer(before) or "")
        asked_after = len(re.findall(ASKED, agent_log.read_text(errors="replace")))
        check("in auto mode, a routine command ran and asked you nothing", ran and asked_after == asked_before)
        await asyncio.sleep(1)
        await auto.close()
    finally:
        await socket_side.close()

    print(f"--- agent log (what the sessions asked, the answers and the calls), {time.monotonic() - t0:.0f} s")
    print("".join(line for line in agent_log.open() if re.search(r"permission|question|placed for|voice listening", line)))
    fails = 0
    for name, ok in results:
        print(f"{'ok  ' if ok else 'FAIL'}  {name}")
        fails += not ok
    return fails


async def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("work", nargs="?", default="Say in one sentence that the session check works.")
    p.add_argument("--model", default="haiku", help="the session's model (default haiku)")
    p.add_argument("--unanswered", type=int, default=10, help="seconds each call waits to be answered (default 10)")
    p.add_argument("--keep", type=Path, help="keep the logs and what the sessions printed in this directory")
    p.add_argument("--asks", action="store_true", help="check permissions instead")
    args = p.parse_args()
    started = time.monotonic()

    # Under /tmp, not the usual temporary folder: a Unix socket's path must stay short.
    tmp = Path(tempfile.mkdtemp(prefix="rbsc-", dir="/tmp"))
    port = free_port()
    hub_address = f"127.0.0.1:{port}"
    tokens = {"agent": secrets.token_hex(16), "speech": secrets.token_hex(16)}
    (tmp / "hub/.config/ringback").mkdir(parents=True)
    (tmp / "hub/.config/ringback/hub.toml").write_text(
        f'listen = "{hub_address}"  # session check\n[tokens]\nagent = "{tokens["agent"]}"\nspeech = "{tokens["speech"]}"\n'
    )
    (tmp / "agent.toml").write_text(f'hub = "{hub_address}"\ntoken = "{tokens["agent"]}"\nname = "session-check"\n')
    # The agent's and the session's HOME: the mod reaches the agent at $HOME/.local/state/ringback/voice.sock.
    home = tmp / "home"
    (home / "Library").mkdir(parents=True)
    (home / "Library/Keychains").symlink_to(Path.home() / "Library/Keychains")
    sock = home / ".local/state/ringback/voice.sock"
    project = tmp / "demo"
    project.mkdir()
    hub_log, agent_log = tmp / "hub.log", tmp / "agent.log"

    (tmp / "bin").mkdir()
    hub_bin, agent_bin = tmp / "bin/ringback-hub", tmp / "bin/ringback-agent"
    subprocess.run(["go", "build", "-o", str(hub_bin), "."], cwd=REPO / "hub", check=True)
    subprocess.run(["go", "build", "-o", str(agent_bin), "."], cwd=REPO / "agent", check=True)
    procs: list[subprocess.Popen] = []
    try:
        procs.append(subprocess.Popen(
            [hub_bin, "serve"], env=without_claude(HOME=str(tmp / "hub")),
            stdout=hub_log.open("w"), stderr=subprocess.STDOUT,
        ))  # fmt: skip
        if not await wait_for(hub_log, r"hub listening", 10):
            sys.exit(f"The hub didn't start:\n{hub_log.read_text()}")
        async with aiohttp.ClientSession() as http:
            phone = {"token": PHONE, "environment": "sandbox", "device": DEVICE, "on_device": True}
            async with http.post(f"http://{hub_address}/register", json=phone) as r:
                registered = r.status
        procs.append(subprocess.Popen(
            [agent_bin, "serve", "--socket", str(sock), "--config", str(tmp / "agent.toml")],
            env=without_claude(HOME=str(home), RINGBACK_UNANSWERED=str(args.unanswered)),
            stdout=agent_log.open("w"), stderr=subprocess.STDOUT,
        ))  # fmt: skip
        linked = await wait_for(hub_log, r"agent session-check: connected", 20) and await wait_for(agent_log, r"voice listening", 20)
        if registered != 200 or not linked:
            sys.exit(f"The phone didn't register ({registered}) or the agent didn't link.\n{hub_log.read_text()}\n{agent_log.read_text()}")

        # The phone's own speech for a call the agent didn't place: the hub asks the agent,
        # which refuses it, as it refuses an offer.
        async with aiohttp.ClientSession() as http:
            async with http.ws_connect(f"http://{hub_address}/phone-speech") as ws:
                await ws.send_json({"type": "hello", "version": 1, "call_id": "made-up-" + secrets.token_hex(12), "kind": "ring", "ringing": True})
                refused = await ws.receive_json(timeout=15)

        fails = 0
        if args.asks:
            fails = await asks(args, project, home, sock, hub_log, agent_log, tmp)
        else:
            t0 = time.monotonic()
            session = await Session.start(args, project, home, "", tmp / "session.jsonl")
            await session.prompt("/ringback")
            await session.prompt(args.work)
            placed = await wait_for(hub_log, CALL, WAIT)
            took = time.monotonic() - t0
            again = left = told = quiet = reply = None
            if placed:
                # As the voice leaves what you say on a call. No call is live, so when the
                # session has answered, it should call again.
                async with aiohttp.ClientSession(connector=aiohttp.UnixConnector(path=str(sock))) as http:
                    url = f"http://voice/sessions/{await session.id}/messages"
                    async with http.post(url, json={"text": MESSAGE}) as r:
                        left = r.status
                t1 = time.monotonic()
                again = await wait_for(hub_log, CALL, WAIT, nth=2)
                took_again = time.monotonic() - t1
            if again:
                # Neither call is answered. Once each one's time is up, the session is told so
                # in a note, which mustn't start a turn: with ringback on, that would call again.
                t2 = time.monotonic()
                told = await until(lambda: len(session.outcomes) >= 2, args.unanswered + 30)
                took_told = time.monotonic() - t2
                turns_before, calls_before = len(session.turns), calls_in(hub_log)
                await asyncio.sleep(QUIET)
                quiet = len(session.turns) == turns_before and calls_in(hub_log) == calls_before
                # Asked at the prompt, as when you're back, the session should know.
                asked = await session.prompt(QUESTION)
                if await until(lambda: session.answer(asked) is not None, WAIT):
                    reply = session.answer(asked)
                await wait_for(hub_log, CALL, WAIT, nth=3)  # it then calls again
            await asyncio.sleep(1)  # the hub's push
            await session.close()
    finally:
        for proc in reversed(procs):
            proc.terminate()
            proc.wait()
        left_over, ran_on = await outlived(tmp)
    ended = f"what the agent started had ended {ran_on:.1f} s after it stopped, within {OUTLIVED} s: {left_over}"
    if args.asks:
        print(f"{'ok  ' if ran_on < OUTLIVED else 'FAIL'}  {ended}")
        print(f"--- {time.monotonic() - started:.0f} s in all")
        keep(args, tmp)
        return 1 if fails or ran_on >= OUTLIVED else 0

    print("--- the session's turns")
    for i, text in enumerate(session.turns, 1):
        print(f"[{i}] {text.strip()}\n")
    print("--- hub log\n" + hub_log.read_text())
    print("--- agent log (session side)")
    print("".join(line for line in agent_log.open() if re.search(r"call [\w-]{8}: (placed|not answered)|outcome for|message for|voice listening", line)))

    def check(name: str, ok: bool) -> None:
        nonlocal fails
        print(f"{'ok  ' if ok else 'FAIL'}  {name}")
        fails += not ok

    check(f"the phone's own speech for a call the agent didn't place is refused: {refused}",
          refused.get("type") == "refused" and refused.get("status") == 403)  # fmt: skip
    check(f"the session placed a call within {WAIT} s", placed is not None)
    if placed:
        call, _, name, subject = placed.groups()
        subject = subject or ""
        hub = hub_log.read_text()
        print(f"      after {took:.0f} s, subject {subject!r}")
        check("the call names the project", name == project.name)
        check("the subject has 3 to 8 words", 3 <= len(subject.split()) <= 8)
        check("the agent placed that call", f"call {call}: placed for {project.name}" in agent_log.read_text())
        pushed = re.search(rf"call {re.escape(call)} → {DEVICE}: (.*)", hub)
        check(f"the hub rang it: the push it would send, with that call's ID, to the made-up phone: {pushed and pushed[1]!r}",
              pushed is not None and "no [apns.sandbox] key" in pushed[1])  # fmt: skip
        check("the message for the session was left (200)", left == 200)
        check("the session answered the message", any("pineapple" in t.lower() for t in session.turns))
        check(f"with no call live, the session called again within {WAIT} s", again is not None)
        if again:
            print(f"      after {took_again:.0f} s, subject {again.group(4)!r}")
            check("the second call has a new ID", again.group(1) != call)
            check("the hub rang it too", re.search(rf"call {re.escape(again.group(1))} → {DEVICE}:", hub) is not None)
            print(f"      outcomes shown after {took_told:.1f} s: {session.outcomes}")
            check(
                f"each call's outcome, not answered, was shown in the session within {args.unanswered + 30} s",
                bool(told) and all(UNANSWERED in o for o in session.outcomes[:2]),
            )
            check(f"the outcomes started no turn and no call in the next {QUIET} s", bool(quiet))
            if reply is not None:
                print(f"      asked {QUESTION!r}, it said: {reply.strip()!r}")
            check(
                f"asked at the prompt, it quoted that its last call {UNANSWERED} {args.unanswered} s",
                reply is not None and f"{UNANSWERED} {args.unanswered} s" in reply.replace("’", "'"),
            )
    check(ended, ran_on < OUTLIVED)
    print(f"--- {time.monotonic() - started:.0f} s in all")
    keep(args, tmp)
    return 1 if fails else 0


def keep(args, tmp: Path) -> None:
    """Keeps the logs, the hub's and the agent's state and what the sessions printed, if asked,
    and removes the rest. Not the socket, the keychain's link, or Claude Code's own files in
    the throwaway HOME."""
    if args.keep:
        shutil.copytree(tmp, args.keep, dirs_exist_ok=True, symlinks=True,
                        ignore=shutil.ignore_patterns("*.sock", "Library", ".claude", ".claude.json*", "bin"))  # fmt: skip
    shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
