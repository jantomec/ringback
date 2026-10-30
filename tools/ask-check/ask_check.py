#!/usr/bin/env python3
"""Checks the voice server's side of what sessions ask you, permissions and questions, against
a voice server started here, with the phone's side played as in tools/phone-check. Then its
calls held outside your work hours.

    uv run --project voice tools/ask-check/ask_check.py [--unanswered S] [--reask S] [--keep DIR]

The voice server gets its own socket and phone-check's ports, so it can run beside your own
voice server, but not beside a phone check. A session's requests go to its socket as the mod
sends them, and a stand-in switchboard takes the calls they place, so nothing rings. Checks:
- requests for an unknown id, or another session's, or of the wrong kind, get 404; a request
  without a tool or questions gets 400;
- with no call, a permission places one with its subject and is "placed"; a long poll waits
  while it rings; a question then goes to that call; a withdrawn question is gone;
- the call isn't answered, so after S seconds it's forgotten, and both permissions stay
  "pending": a long poll waits on through it. One is withdrawn and gone;
- the next call you answer, answered with aiortc, gets the other: the voice asks it, and
  your spoken yes (Kokoro) comes back as "allow";
- on that call, a permission is "live": the voice asks it, and your spoken yes comes back as
  "allow"; a question is answered with your spoken choice;
- one answered by a tool's POST is final (a second gets 409), one withdrawn is gone, and one
  still waiting when you hang up gets "none";
- the call's transcript has the requests and the answers.

Held calls: the stand-in switchboard holds a call (`held_until`) as it does outside your work
hours, and the voice server is told to ask again within S seconds (`--reask`) instead of 10
min. Checks:
- a permission with no call places one that's held ("held", with `held_until`), starts no
  Haiku, and keeps the Mac awake; a long poll says `held`; an offer for it is refused;
- a session's new briefing goes to the held call, without placing another, and a question
  goes to it too;
- it's asked again after S seconds, with the same call ID and the new subject; then at the
  time the switchboard gave; then, after a 503, S seconds later; then it rings and Haiku
  starts;
- answered, its transcript has the new briefing, and the permission the old one asked was
  put to the voice as a note;
- another held call rings when its time comes and isn't answered: it's forgotten S
  (`--unanswered`) seconds after it rang, not after it was placed, and the Mac sleeps again.
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
from datetime import datetime, timedelta
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
# Held calls, each for a session of its own.
HELD, HELD_LATER = "ask-check-held", "ask-check-held-later"
HELD_SUBJECT = "Parser fix, and the docs"
HELD_BRIEFING = """\
Since I asked about the force-push, I also updated the parser's docs to say that empty lines
between sections are fine now. The force-push is still waiting for your answer."""


async def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--unanswered", type=float, default=10, help="seconds before an unanswered call is forgotten")
    p.add_argument("--reask", type=float, default=4, help="seconds before a held call asks the switchboard again")
    p.add_argument("--keep", type=Path, help="keep the voice log and transcripts in this directory")
    args = p.parse_args()
    for address in (SWITCHBOARD, VOICE):
        if not free(address):
            sys.exit(f"{address} is in use: is a phone check or another ask check running?")

    tmp = Path(tempfile.mkdtemp(prefix="ringback-ask-check-"))
    sock, voice_log, project, held_project = tmp / "voice.sock", tmp / "voice.log", tmp / "demo", tmp / "held"
    project.mkdir()
    held_project.mkdir()
    rung: list[dict] = []  # calls the stand-in switchboard took
    taken: list[float] = []  # when it took each (time.time())
    # How it answers the next ones: hold for so many seconds, "down" for a 503, or ring (None
    # or none left). What it said each held call can ring at, by when it took that request.
    holds: list[float | str | None] = []
    held_until: dict[float, str] = {}

    async def take_call(request: web.Request) -> web.Response:
        rung.append(await request.json())
        taken.append(now := time.time())
        hold = holds.pop(0) if holds else None
        if hold == "down":
            return web.json_response({"ok": False}, status=503)
        if hold is not None:
            # As the switchboard writes it: whole seconds, with the offset.
            held_until[now] = (datetime.now().astimezone() + timedelta(seconds=hold)).isoformat(timespec="seconds")
            return web.json_response({"ok": True, "held_until": held_until[now]})
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
        env=os.environ | {"RINGBACK_UNANSWERED": str(args.unanswered), "RINGBACK_REASK": str(args.reask)},
    )  # fmt: skip
    socket_side = aiohttp.ClientSession(connector=aiohttp.UnixConnector(path=str(sock)))

    def children(pattern: str) -> int:
        """The voice server's processes whose command line matches: Haiku sessions
        ("input-format stream-json"), or the sleep assertion ("caffeinate")."""
        found = subprocess.run(["pgrep", "-P", str(voice.pid), "-f", pattern], capture_output=True)
        return len(found.stdout.split())

    async def until(ready, limit: float) -> float | None:
        """Seconds until ready() is true, polled every 50 ms, or None after limit seconds."""
        t = time.monotonic()
        while not ready():
            if time.monotonic() - t > limit:
                return None
            await asyncio.sleep(0.05)
        return time.monotonic() - t

    async def api(method: str, path: str, body: dict | None = None) -> tuple[int, dict]:
        async with socket_side.request(method, f"http://voice{path}", json=body) as r:
            return r.status, await r.json(content_type=None)

    def of(kind: str, id: str = "", session: str = SESSION) -> str:
        return f"/sessions/{session}/{kind}s" + (f"/{id}" if id else "")

    async def waited(kind: str, id: str, wait: float, session: str = SESSION) -> tuple[int, dict, float]:
        """A long poll: its status, its answer, and how long it took."""
        t = time.monotonic()
        status, body = await api("GET", of(kind, id, session) + f"?wait={wait:g}")
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

        # Nobody answers: once the call is forgotten, both permissions still wait, for the next
        # call with the session you answer, and a long poll waits on through it.
        wait = max(1.0, placed + args.unanswered + 5 - time.monotonic())
        status, body, took = await waited("permission", pid, wait)
        forgotten = re.search(rf"call {re.escape(rung[0]['call_id'][:8])}: not answered, forgotten", voice_log.read_text())
        _, body2, _ = await waited("permission", pid2, 0)
        check(f"unanswered: forgotten, and both still pending, a poll of {wait:.1f} s waiting {took:.1f} s: {body}, {body2}",
              forgotten is not None and status == 200 and body == body2 == {"verdict": "pending", "said": None}
              and took >= wait - 0.5)  # fmt: skip
        status, _ = await api("DELETE", of("permission", pid2))
        status_after, _ = await api("GET", of("permission", pid2))
        check("one withdrawn after its call is forgotten is gone (200, then 404)", status == 200 and status_after == 404)
        pooled = pid

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

        # The permission still waiting from the call you didn't answer goes to this one, as a
        # note once the voice has opened; Haiku's second reply asks it.
        asked = await wait_for(voice_log, rf"call {re.escape(call_id[:8])}: Haiku: first words after", 45, nth=2)
        await phone.quiet_for(2.5, limit=45)
        phone.mic.say(yes)
        body = await answer("permission", pooled, 45)
        check(f'the permission from the call you didn\'t answer is asked on this one, and "{YES}" allows it: {body}',
              asked is not None and body.get("verdict") == "allow")  # fmt: skip
        await phone.quiet_for(2.5, limit=30)

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
        check("the transcript has both permissions and their answers",
              all(f"Permission {p}:" in text and f"**Answered the session:** permission {p}: allow" in text
                  for p in (pooled, pid)))  # fmt: skip
        check("the transcript has the question and its answer",
              f"Question {qid}," in text and f"**Answered the session:** question {qid}:" in text)  # fmt: skip
        if text:
            print(f"--- the call's transcript\n{text}")

        # Held calls. The live call's Haiku and sleep assertion end after its hang-up first.
        haiku = "input-format stream-json"
        await until(lambda: children(haiku) == children("caffeinate") == 0, 10)
        haikus, awake = children(haiku), children("caffeinate")
        n = len(rung)
        holds[:] = [3600, 2.5, "down", None]
        status, body = await api("POST", of("permission", session=HELD), PERMISSION)
        held_pid = body.get("permission", "")
        first = taken[n] if len(rung) > n else 0.0
        held_id = rung[n]["call_id"] if len(rung) > n else ""
        check(f"a permission with no call places a held one: {status} {body}",
              status == 200 and body.get("call") == "held" and len(rung) == n + 1
              and body.get("held_until") == held_until.get(first))  # fmt: skip
        await asyncio.sleep(1)
        check(f"a held call starts no Haiku ({children(haiku)}, {haikus} before) and keeps the Mac awake "
              f"({children('caffeinate')} caffeinate, {awake} before)",
              children(haiku) == haikus and children("caffeinate") == 1 and awake == 0)  # fmt: skip
        status, body, took = await waited("permission", held_pid, 1, HELD)
        check(f"a long poll says it's held ({took:.2f} s for 1): {body}",
              status == 200 and body == {"verdict": "pending", "said": None, "held": True} and 0.9 <= took <= 1.5)  # fmt: skip
        phone = Phone()
        refused = await phone.offer(held_id, ringing=True)
        await phone.hang_up()
        logged = await wait_for(voice_log, rf"call {re.escape(held_id[:8])}: refused, held until", 2)
        check(f"an offer for a held call is refused: {refused}", refused == 403 and logged is not None)
        rebriefed = {"session_id": HELD, "project": "demo", "cwd": str(held_project), "subject": HELD_SUBJECT,
                     "briefing": HELD_BRIEFING}  # fmt: skip
        status, body = await api("POST", "/calls", rebriefed)
        check(f"a new briefing goes to the held call, without placing another: {status} {body}",
              status == 200 and body == {"ok": True, "held_until": held_until.get(first)} and len(rung) == n + 1)  # fmt: skip
        status, body = await api("POST", of("question", session=HELD), QUESTION)
        held_qid = body.get("question", "")
        check(f"a question goes to the held call too: {status} {body}",
              status == 200 and body.get("call") == "held" and len(rung) == n + 1)  # fmt: skip

        # Asked again after --reask s; then at the time the switchboard gave; after a 503,
        # --reask s later (a minute, unless --reask is shorter); then it rings.
        await until(lambda: len(rung) >= n + 4, args.reask * 2 + 10)
        if len(rung) >= n + 4:
            t1, t2, t3, t4 = taken[n : n + 4]
            due = datetime.fromisoformat(held_until[t2]).timestamp()
            check(f"asked again {t2 - t1:.2f} s later, with the same ID and the new subject: {rung[n + 1].get('subject')!r}",
                  all(r["call_id"] == held_id for r in rung[n : n + 4]) and args.reask <= t2 - t1 <= args.reask + 1
                  and rung[n + 1].get("subject") == HELD_SUBJECT)  # fmt: skip
            check(f"then at the time the switchboard gave ({held_until[t2]}): {t3 - due:+.2f} s", -0.05 <= t3 - due <= 0.5)
            check(f"then {t4 - t3:.2f} s after a 503", args.reask <= t4 - t3 <= args.reask + 1)
        else:
            t4 = time.time()
            check(f"asked again three times: {len(rung) - n - 1}", False)
        started = await until(lambda: children(haiku) > haikus, 5)
        check(f"it rings, and Haiku starts {time.time() - t4:.2f} s after", started is not None)
        status, body, _ = await waited("permission", held_pid, 0, HELD)
        check(f"a poll no longer says held: {body}", status == 200 and "held" not in body)

        phone = Phone()
        offered = await phone.offer(held_id, ringing=True)
        await asyncio.sleep(1)
        await answered(held_id, phone)
        opened = await spoke_after(phone, 0)
        await phone.quiet_for(3, limit=45)  # the voice asks both after its opening
        await phone.hang_up()
        _, body, _ = await waited("permission", held_pid, 20, HELD)
        transcripts = sorted(held_project.glob(".ringback/calls/*.md"))
        held_text = transcripts[-1].read_text() if transcripts else ""
        check(f"answered, the voice opens: {offered}", offered == 200 and opened)
        check("its transcript has the new briefing, not the old one",
              HELD_BRIEFING.split("\n")[0] in held_text and "has stopped because it needs" not in held_text)  # fmt: skip
        check("the permission the old briefing asked went to the voice as a note",
              f"**The session asks:** Permission {held_pid}:" in held_text)  # fmt: skip
        check("so did the question", f"**The session asks:** Question {held_qid}," in held_text)
        check(f"hung up, the permission has no verdict: {body}", body.get("verdict") == "none")
        if held_text:
            print(f"--- the held call's transcript\n{held_text}")

        # Another session's call, held 3 s, rings and isn't answered.
        n = len(rung)
        holds[:] = [3, None]
        later = {"session_id": HELD_LATER, "project": "demo", "cwd": str(held_project), "subject": "Docs updated",
                 "briefing": BRIEFING}  # fmt: skip
        status, body = await api("POST", "/calls", later)
        check(f"a briefing with no call places a held one: {status} {body}", status == 200 and "held_until" in body)
        await until(lambda: len(rung) >= n + 2, 10)
        placed = taken[n] if len(rung) > n else time.time()
        rang = taken[n + 1] if len(rung) >= n + 2 else time.time()
        outcome, end = None, time.time() + args.unanswered + 15
        while outcome is None and time.time() < end:
            _, body = await api("GET", f"/sessions/{HELD_LATER}/messages")
            outcome = next((m["text"] for m in body.get("messages", []) if m.get("kind") == "outcome"), None)
            await asyncio.sleep(0 if outcome else 0.1)
        forgotten = time.time()
        within = re.search(r"wasn't answered within (\d+) s", outcome or "")
        check(f"it rang {rang - placed:.2f} s after it was placed, and was forgotten {forgotten - rang:.2f} s after it "
              f"rang: {outcome!r}",
              within is not None and int(within[1]) <= args.unanswered + 1
              and args.unanswered - 0.5 <= forgotten - rang <= args.unanswered + 2)  # fmt: skip
        gone = await until(lambda: children(haiku) == children("caffeinate") == 0, 10)
        check(f"with no call left, no Haiku runs and the Mac can sleep again ({gone} s)", gone is not None)
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
