"""The voice server's side of the state file, so the agent's tests can check that each reads
what the other wrote (TestStateWithPython, when RINGBACK_PYTHON names the voice server's
Python):

    python state_check.py write FILE   a held call that asks a permission, a call to call back,
                                       and a message waiting for its session
    python state_check.py read FILE    restores it, and prints the log and what it kept, as JSON
"""

import asyncio
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "voice"))

from ringback_voice import sessions  # noqa: E402


async def write(path: Path) -> None:
    calls = sessions.Calls(None, "x", lambda line: None, state=path)
    ask = sessions.Ask("abcd", "permission", "s1", sessions.permission({"tool": "Bash", "input": "{}", "project": "demo"}))
    held = sessions.Call(
        "held-call-0123456789", "s1", "demo", "/tmp/demo", "Held", "The briefing.", time.monotonic(),
        held_until="2030-01-01T08:00:00+01:00",
    )  # fmt: skip
    held.link = calls._link(held)
    held.link.ask(ask, briefed=True)
    calls._calls[held.id] = held
    calls._asks[ask.id] = ask
    past = sessions.Call("past-call-0123456789", "s2", "demo", "/tmp/demo", "Past", "Earlier.", time.monotonic(), talked=time.time())
    calls._past[past.id] = (past, time.time())
    calls._waiting["s2"] = [{"kind": "outcome", "text": "Done.", "transcript": None}]
    calls._save()


async def read(path: Path) -> None:
    lines: list[str] = []
    calls = sessions.Calls(None, "x", lines.append, state=path)
    await calls.restore()
    for call in calls._calls.values():
        if call.holding:
            call.holding.cancel()
    if calls._awake:
        calls._awake.terminate()
        await calls._awake.wait()
    held = {c.id: {"held_until": c.held_until, "asks": sorted(c.link.asks), "briefed": list(c.link.briefed)} for c in calls._calls.values()}
    print(json.dumps({"log": lines, "asks": sorted(calls._asks), "held": held, "past": sorted(calls._past)}))


if __name__ == "__main__":
    asyncio.run({"write": write, "read": read}[sys.argv[1]](Path(sys.argv[2])))
