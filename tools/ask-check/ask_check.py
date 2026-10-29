#!/usr/bin/env python3
"""Checks the voice server's side of what sessions ask you, permissions and questions, against
a voice server started here, with the phone's side played as in tools/phone-check.

    uv run --project voice tools/ask-check/ask_check.py [--unanswered S] [--keep DIR]

The voice server gets its own socket and phone-check's ports, so it can run beside your own
voice server, but not beside a phone check. A session's requests go to its socket as the mod
sends them, and a stand-in switchboard takes the calls they place, so nothing rings. Checks:
- requests for an unknown id, or another session's, or of the wrong kind, get 404; a request
  without a tool or questions gets 400;
- with no call, a permission places one with its subject and is "placed"; a long poll waits
  while it rings; a question then goes to that call; a withdrawn question is gone;
- the call isn't answered, so after S seconds it's forgotten and the permission's verdict is
  "none", which ends the long poll before its 25 s;
- on a call answered with aiortc, a permission is "live": the voice asks it, and your spoken
  yes (Kokoro) comes back as "allow"; a question is answered with your spoken choice;
- one answered by a tool's POST is final (a second gets 409), one withdrawn is gone, and one
  still waiting when you hang up gets "none";
- the call's transcript has the requests and the answers.
"""

import argparse
import asyncio
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import aiohttp
import numpy as np
import soxr
from aiohttp import web

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "phone-check"))
from phone_check import KOKORO, RATE, SWITCHBOARD, VOICE, YOUR_VOICE, Phone, answered, free, wait_for  # noqa: E402

SESSION = "ask-check-session"
BRIEFING = """\
I fixed the crash in the config parser: it choked on empty lines between sections. I rebased
the branch fix-parser onto main, and the whole suite passes. Nothing needs you right now."""
PERMISSION = {
    "tool": "Bash",
    "description": "Force-push the branch fix-parser",
    "input": "git push --force origin fix-parser",
    "context": "I rebased fix-parser onto main to drop a debug commit, so pushing it needs force.",
    "project": "demo",
    "subject": "Force-push fix-parser?",
}
QUESTION = {
    "questions": [{
        "question": "Where should I push the fix?",
        "header": "Branch",
        "options": [
            {"label": "main", "description": "Straight onto the main branch"},
            {"label": "New branch", "description": "A branch called fix-parser, with a pull request"},
        ],
        "multiSelect": False,
    }],
    "context": "The parser fix is ready to push.",
    "project": "demo",
    "subject": "Where to push the parser fix?",
}  # fmt: skip
YES = "Yes, go ahead."
CHOICE = "A new branch, please."


async def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--unanswered", type=float, default=10, help="seconds before an unanswered call is forgotten")
    p.add_argument("--keep", type=Path, help="keep the voice log and transcripts in this directory")
    args = p.parse_args()
    for address in (SWITCHBOARD, VOICE):
        if not free(address):
            sys.exit(f"{address} is in use: is a phone check or another ask check running?")

    tmp = Path(tempfile.mkdtemp(prefix="ringback-ask-check-"))
    sock, voice_log, project = tmp / "voice.sock", tmp / "voice.log", tmp / "demo"
    project.mkdir()
    rung: list[dict] = []  # calls the stand-in switchboard took

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
         "--socket", str(sock)],
        stdout=voice_log.open("w"), stderr=subprocess.STDOUT,
        env=os.environ | {"RINGBACK_UNANSWERED": str(args.unanswered)},
    )  # fmt: skip
    socket_side = aiohttp.ClientSession(connector=aiohttp.UnixConnector(path=str(sock)))

    async def api(method: str, path: str, body: dict | None = None) -> tuple[int, dict]:
        async with socket_side.request(method, f"http://voice{path}", json=body) as r:
            return r.status, await r.json(content_type=None)

    def of(kind: str, id: str = "", session: str = SESSION) -> str:
        return f"/sessions/{session}/{kind}s" + (f"/{id}" if id else "")

    async def waited(kind: str, id: str, wait: float) -> tuple[int, dict, float]:
        """A long poll: its status, its answer, and how long it took."""
        t = time.monotonic()
        status, body = await api("GET", of(kind, id) + f"?wait={wait:g}")
        return status, body, time.monotonic() - t

    async def answer(kind: str, id: str, within: float) -> dict:
        """The answer once it isn't pending, or the last one after within seconds."""
        end = time.monotonic() + within
        while True:
            _, body, _ = await waited(kind, id, min(25, max(0.1, end - time.monotonic())))
            if body.get("verdict", body.get("status")) != "pending" or time.monotonic() >= end:
                return body

    async def spoke_after(phone: Phone, since: float, limit: float = 45) -> bool:
        """Whether the voice spoke after since, then fell quiet for 2.5 s, within limit."""
        end = time.monotonic() + limit
        while (phone.last_audio or 0) <= since and time.monotonic() < end:
            await asyncio.sleep(0.1)
        if (phone.last_audio or 0) <= since:
            return False
        await phone.quiet_for(2.5, limit=max(0.0, end - time.monotonic()))
        return True

    fails = 0
    lines: list[str] = []

    def check(name: str, ok: bool) -> None:
        nonlocal fails
        lines.append(f"{'ok  ' if ok else 'FAIL'}  {name}")
        fails += not ok

    try:
        from mlx_audio.tts.utils import load_model

        kokoro = load_model(KOKORO)

        def speech(text: str) -> np.ndarray:
            audio = np.concatenate([np.array(r.audio) for r in kokoro.generate(text, voice=YOUR_VOICE, lang_code="a")])
            return (np.clip(soxr.resample(audio, 24000, RATE), -1, 1) * 32767).astype(np.int16)

        yes, choice = speech(YES), speech(CHOICE)
        if not await wait_for(voice_log, r"voice listening", 180):
            print(voice_log.read_text())
            sys.exit("The voice server didn't start.")

        status, _ = await api("GET", of("permission", "zzzz"))
        check("an unknown permission: 404", status == 404)
        status, _ = await api("POST", of("permission", "zzzz"), {"verdict": "allow", "said": "yes"})
        check("a verdict for an unknown permission: 404", status == 404)
        status, _ = await api("DELETE", of("question", "zzzz"))
        check("withdrawing an unknown question: 404", status == 404)
        status, _ = await api("POST", of("permission"), {"description": "no tool"})
        check("a permission without a tool: 400", status == 400)
        status, _ = await api("POST", of("question"), {"questions": []})
        check("no questions: 400", status == 400)

        # No call: a permission places one, whose briefing asks it. Two at once, as from
        # parallel tool calls, share it.
        (status, body), (status2, body2) = await asyncio.gather(
            api("POST", of("permission"), PERMISSION), api("POST", of("permission"), PERMISSION)
        )
        pid, pid2 = body.get("permission", ""), body2.get("permission", "")
        check(f"two permissions at once with no call place one: {status} {body}, {status2} {body2}",
              status == status2 == 200 and body.get("call") == body2.get("call") == "placed" and pid != pid2
              and all(re.fullmatch(r"[a-z]{4}", p) for p in (pid, pid2))
              and len(rung) == 1 and rung[0].get("subject") == PERMISSION["subject"])  # fmt: skip
        placed = time.monotonic()
        status, body, took = await waited("permission", pid, 1)
        check(f"a long poll waits while it rings ({took:.2f} s for 1): {body}",
              status == 200 and body == {"verdict": "pending", "said": None} and 0.9 <= took <= 1.5)  # fmt: skip
        status, _ = await api("GET", of("permission", pid, session="another-session"))
        check("another session's permission: 404", status == 404)
        status, _ = await api("GET", of("question", pid))
        check("a permission asked for as a question: 404", status == 404)
        status, body = await api("POST", of("question"), QUESTION)
        qid = body.get("question", "")
        check(f"a question while that call rings goes to it: {status} {body}",
              status == 200 and body.get("call") == "placed" and len(rung) == 1)  # fmt: skip
        status, body = await api("DELETE", of("question", qid))
        status_after, _ = await api("GET", of("question", qid))
        check("a withdrawn question is gone (200, then 404)", status == 200 and status_after == 404)

        # Nobody answers: once the call is forgotten, the permission has no verdict.
        status, body, took = await waited("permission", pid, 25)
        since = time.monotonic() - placed
        _, body2, _ = await waited("permission", pid2, 0)
        check(f"unanswered: verdict none {since:.1f} s after placing, before the poll's 25 s: {body}, {body2}",
              status == 200 and body == body2 == {"verdict": "none", "said": None} and took < 24
              and args.unanswered - 1 <= since <= args.unanswered + 3)  # fmt: skip

        # A call you answer, as a session's briefing places it.
        call = {"session_id": SESSION, "project": "demo", "cwd": str(project), "subject": "Parser fix", "briefing": BRIEFING}
        status, _ = await api("POST", "/calls", call)
        call_id = rung[-1]["call_id"]
        phone = Phone()
        await phone.offer(call_id, ringing=True)
        await asyncio.sleep(3)
        await answered(call_id, phone)
        check("the voice opens the call you answered", await spoke_after(phone, 0))
        calls_before = len(rung)

        t = time.monotonic()
        status, body = await api("POST", of("permission"), PERMISSION)
        pid = body.get("permission", "")
        asked = await spoke_after(phone, t)
        check(f"a permission on the call you're on is live, and the voice asks it: {status} {body}",
              status == 200 and body.get("call") == "live" and len(rung) == calls_before and asked)  # fmt: skip
        phone.mic.say(yes)
        body = await answer("permission", pid, 45)
        check(f'your spoken "{YES}" allows it: {body}',
              body.get("verdict") == "allow" and "yes" in (body.get("said") or "").lower())  # fmt: skip
        await phone.quiet_for(2.5, limit=30)

        t = time.monotonic()
        status, body = await api("POST", of("question"), QUESTION)
        qid = body.get("question", "")
        asked = await spoke_after(phone, t)
        check(f"a question on the call is live, and the voice asks it: {status} {body}",
              status == 200 and body.get("call") == "live" and asked)  # fmt: skip
        phone.mic.say(choice)
        body = await answer("question", qid, 45)
        check(f'your spoken "{CHOICE}" answers it: {body}',
              body.get("status") == "answered" and body.get("answers") == {QUESTION["questions"][0]["question"]: "New branch"})  # fmt: skip
        await phone.quiet_for(2.5, limit=30)

        status, body = await api("POST", of("permission"), PERMISSION)
        tool_pid = body.get("permission", "")
        status, body = await api("POST", of("permission", tool_pid), {"verdict": "deny", "said": "from a tool"})
        again, _ = await api("POST", of("permission", tool_pid), {"verdict": "allow", "said": "again"})
        _, polled, _ = await waited("permission", tool_pid, 0)
        check(f"a tool's verdict is final: {status} {body}, then {again}, then {polled}",
              status == 200 and body.get("verdict") == "deny" and again == 409 and polled.get("verdict") == "deny")  # fmt: skip

        status, body = await api("POST", of("permission"), PERMISSION)
        gone = body.get("permission", "")
        status, _ = await api("DELETE", of("permission", gone))
        status_after, _ = await api("GET", of("permission", gone))
        check("a permission withdrawn on the call is gone (200, then 404)", status == 200 and status_after == 404)

        status, body = await api("POST", of("permission"), PERMISSION)
        last = body.get("permission", "")
        await asyncio.sleep(1)
        hung_up = time.monotonic()
        await phone.hang_up()
        status, body, _ = await waited("permission", last, 20)
        check(f"one still waiting when you hang up: {body}, {time.monotonic() - hung_up:.1f} s after",
              status == 200 and body.get("verdict") == "none")  # fmt: skip

        transcripts = sorted(project.glob(".ringback/calls/*.md"))
        text = transcripts[-1].read_text() if transcripts else ""
        check("the transcript has the permission and its answer",
              f"Permission {pid}:" in text and f"**Answered the session:** permission {pid}: allow" in text)  # fmt: skip
        check("the transcript has the question and its answer",
              f"Question {qid}," in text and f"**Answered the session:** question {qid}:" in text)  # fmt: skip
        if text:
            print(f"--- the call's transcript\n{text}")
    finally:
        await socket_side.close()
        voice.terminate()
        voice.wait()
        await switchboard.cleanup()

    print("--- voice log")
    print("".join(line for line in voice_log.open() if re.search(r"call |voice listening|session |permission |question ", line)))
    print("\n".join(lines))
    if args.keep:
        shutil.copytree(tmp, args.keep, dirs_exist_ok=True, ignore=shutil.ignore_patterns("*.sock"))
    shutil.rmtree(tmp, ignore_errors=True)
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
