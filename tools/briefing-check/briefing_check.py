#!/usr/bin/env python3
"""Shows what a session says about the call itself in its briefing, for the mod at each REV.

    python3 tools/briefing-check/briefing_check.py [--runs N] [--model MODEL] [REV ...]

For each REV (a git revision, or `.` for the working tree, the default), it takes mod/ as
it is there, points its socket at a path that doesn't exist, so no call can be placed, and
runs N sessions (`claude -p`, 4 at a time): /ringback, then the session check's work. When
that turn ends, the mod asks for the briefing. Prints each briefing's subject and its
sentences that mention a call or the phone, for you to judge: saying that ringback will call
is fine, saying the call may not reach you isn't.
"""

import argparse
import json
import re
import subprocess
import sys
import tempfile
import threading
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
WORK = "Say in one sentence that the session check works."  # as in tools/session-check
SOCKET = "socketPath: `${await $.env.get('HOME')}/.local/state/ringback/voice.sock`"
ABOUT_THE_CALL = re.compile(r"\b(phone|reach\w*|ring\w*|calls?)\b", re.IGNORECASE)


def mod_at(rev: str, into: Path) -> Path:
    """mod/ at rev, with its socket pointed nowhere."""
    if rev == ".":
        subprocess.run(["cp", "-R", str(REPO / "mod"), str(into)], check=True)
    else:
        into.mkdir()
        archive = subprocess.run(["git", "-C", str(REPO), "archive", rev, "mod"], capture_output=True, check=True)
        subprocess.run(["tar", "-x", "-C", str(into), "--strip-components", "1"], input=archive.stdout, check=True)
    register = into / "hooks" / "register.ts"
    text = register.read_text()
    if SOCKET not in text:
        sys.exit(f"The mod at {rev} doesn't name its socket as this check expects.")
    register.write_text(text.replace(SOCKET, "socketPath: '/nonexistent/ringback-briefing-check.sock'"))
    return into


def briefing(mod: Path, model: str) -> str:
    cwd = Path(tempfile.mkdtemp(prefix="ringback-briefing-check-")) / "demo"
    cwd.mkdir()
    session = subprocess.Popen(
        ["claude", "-p", "--plugin-dir", str(mod), "--model", model, "--input-format", "stream-json",
         "--output-format", "stream-json", "--verbose", "--setting-sources", "", "--tools", ""],
        cwd=cwd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
    )  # fmt: skip
    for text in ["/ringback", WORK]:
        session.stdin.write(json.dumps({"type": "user", "message": {"role": "user", "content": text}}) + "\n")
    session.stdin.flush()
    timer = threading.Timer(180, session.kill)  # stdin stays open until the briefing comes
    timer.start()
    found = ""
    for line in session.stdout:
        if '"type":"result"' in line:
            result = json.loads(line).get("result") or ""
            if result.lstrip().lower().startswith("subject:"):
                found = result
                break
    timer.cancel()
    session.stdin.close()
    session.wait()
    return found


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("revs", nargs="*", default=["."], help="git revisions of the mod, . for the working tree")
    p.add_argument("--runs", type=int, default=4, help="sessions for each revision (default 4)")
    p.add_argument("--model", default="haiku", help="the session's model (default haiku)")
    args = p.parse_args()
    tmp = Path(tempfile.mkdtemp(prefix="ringback-briefing-check-"))
    mods = {rev: mod_at(rev, tmp / f"mod-{i}") for i, rev in enumerate(args.revs)}
    jobs = [rev for rev in args.revs for _ in range(args.runs)]
    with ThreadPoolExecutor(4) as pool:
        briefings = list(pool.map(lambda rev: briefing(mods[rev], args.model), jobs))

    for rev in args.revs:
        print(f"=== {rev}")
        for text in (b for r, b in zip(jobs, briefings) if r == rev):
            if not text:
                print("  (no briefing)")
                continue
            print(f"  {text.splitlines()[0]}")
            body = text.split("\n", 1)[-1]
            for s in re.split(r"(?<=[.!?])\s+", body.strip()):
                if ABOUT_THE_CALL.search(s):
                    print(f"    - {s}")
        print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
