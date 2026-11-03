"""A call through ringback's real parts: the hub and the agent on this Mac, the speech node
on its machine, and the stand-in phone (`tools/stand-in-phone`). The session's side is played
through the agent's socket, as a mod does. See README.md.

    uv run --project tools tools/line-check/line_check.py --listen ADDR --node HOST
        [--node-dir DIR] [--ring S] [--keep DIR]
"""

import argparse
import asyncio
import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import aiohttp
import numpy as np
import soxr

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "stand-in-phone"))
import stand_in_phone  # noqa: E402
from stand_in_phone import BRIEFING, KOKORO, RATE, REPLY, SUBJECT, YOUR_VOICE, Phone, wait_for  # noqa: E402

SESSION = "line-check-session"
ROOT = Path(__file__).resolve().parents[2]

lines: list[str] = []
fails = 0


def check(name: str, passed: bool) -> None:
    global fails
    lines.append(f"{'ok  ' if passed else 'FAIL'}  {name}")
    fails += not passed


def build() -> tuple[Path, Path]:
    """The hub and the agent, built from this checkout."""
    hub, agent = ROOT / "hub/build/ringback-hub", ROOT / "agent/build/ringback-agent"
    subprocess.run(["go", "build", "-o", str(hub), "."], cwd=ROOT / "hub", check=True)
    subprocess.run(["go", "build", "-o", str(agent), "."], cwd=ROOT / "agent", check=True)
    return hub, agent


async def main() -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--listen", required=True, help="where the hub listens: an address the speech node reaches this Mac at, and a free port")
    p.add_argument("--node", required=True, help="the speech node's ssh host")
    p.add_argument("--node-dir", default="~/ringback-speech", help="its install directory (default ~/ringback-speech)")
    p.add_argument("--ring", type=float, default=6.0, help="seconds from placing the call to answering it (default 6)")
    p.add_argument("--keep", type=Path, help="keep the logs and the transcript in this directory")
    args = p.parse_args()
    if not stand_in_phone.free(args.listen):
        sys.exit(f"{args.listen} is in use")
    hub_bin, agent_bin = build()
    tmp = Path(tempfile.mkdtemp(prefix="ringback-line-check-"))
    tokens = {"agent": secrets.token_hex(16), "speech": secrets.token_hex(16)}
    (tmp / "hub/.config/ringback").mkdir(parents=True)
    (tmp / "hub/.config/ringback/hub.toml").write_text(
        f'listen = "{args.listen}"\n[tokens]\nagent = "{tokens["agent"]}"\nspeech = "{tokens["speech"]}"\n'
    )
    (tmp / "agent.toml").write_text(f'hub = "{args.listen}"\ntoken = "{tokens["agent"]}"\nname = "line-check"\n')
    project = tmp / "demo"
    project.mkdir()
    sock, hub_log, agent_log, node_log = tmp / "voice.sock", tmp / "hub.log", tmp / "agent.log", tmp / "node.log"
    stand_in_phone.HUB = args.listen

    hub = subprocess.Popen([str(hub_bin), "serve"], env=os.environ | {"HOME": str(tmp / "hub")},
                           stdout=hub_log.open("w"), stderr=subprocess.STDOUT)  # fmt: skip
    node_pid = None
    agent = None
    try:
        if not await wait_for(hub_log, r"hub listening", 10):
            sys.exit(f"The hub didn't start:\n{hub_log.read_text()}")
        t = time.monotonic()
        started = subprocess.run(
            ["ssh", args.node, f"cd {args.node_dir} && . ./env.sh && (nohup bin/ringback-speech --hub ws://{args.listen}/speech"
             f" --token {tokens['speech']} serve > line-check.log 2>&1 < /dev/null & echo $!)"],
            capture_output=True, text=True, timeout=30,
        )  # fmt: skip
        node_pid = started.stdout.strip()
        node_up = await wait_for(hub_log, r"speech .*: connected", 120)
        check(f"the speech node linked to the hub ({time.monotonic() - t:.1f} s)", node_up is not None)
        if not node_up:
            return 1

        agent = subprocess.Popen([str(agent_bin), "serve", "--socket", str(sock), "--config", str(tmp / "agent.toml")],
                                 stdout=agent_log.open("w"), stderr=subprocess.STDOUT)  # fmt: skip
        agent_up = await wait_for(hub_log, r"agent line-check: connected", 20) and await wait_for(agent_log, r"voice listening", 20)
        check("the agent connected to the hub", bool(agent_up))

        from mlx_audio.tts.utils import load_model

        kokoro = load_model(KOKORO)
        speech = np.concatenate([np.array(r.audio) for r in kokoro.generate(REPLY, voice=YOUR_VOICE, lang_code="a")])
        reply = (np.clip(soxr.resample(speech, 24000, RATE), -1, 1) * 32767).astype(np.int16)

        socket_side = aiohttp.ClientSession(connector=aiohttp.UnixConnector(path=str(sock)))
        http = aiohttp.ClientSession()

        async def for_the_session() -> list[str]:
            async with socket_side.get(f"http://voice/sessions/{SESSION}/messages") as r:
                return [m["text"] for m in (await r.json())["messages"] if m.get("kind", "message") == "message"]

        # Placed as a session's mod places it; its ID from the agent's state file, as the hub
        # gives it only to the phone, in its push.
        body = {"session_id": SESSION, "project": "demo", "cwd": str(project), "subject": SUBJECT, "briefing": BRIEFING}
        placed = time.monotonic()
        async with socket_side.post("http://voice/calls", json=body) as r:
            check(f"the call is placed through the hub: {r.status}", r.status == 200)
        call_id = None
        while call_id is None and time.monotonic() - placed < 5:
            state = tmp / "voice-state.json"
            if state.exists():
                calls = [c for c in json.loads(state.read_text()).get("calls", []) if c["session"] == SESSION]
                call_id = calls[-1]["id"] if calls else None
            await asyncio.sleep(0.1)
        if call_id is None:
            check("the call's ID is in the agent's state file", False)
            return 1

        # The phone makes its offer while it rings, and you answer RING s after the call was placed.
        phone = Phone()
        status = await phone.offer(call_id, ringing=True)
        check(f"the offer made while it rings is answered: {status}", status == 200)
        await asyncio.sleep(max(0.0, placed + args.ring - time.monotonic()))
        early = phone.first_audio
        answered_at = time.monotonic()
        phone.mic.on.set()  # the phone's microphone sends once you've answered, as the app's
        async with http.post(f"http://{args.listen}/answered", json={"call_id": call_id}) as r:
            answered = r.status
        silence = await phone.heard_after(answered_at)
        check(f"silent while it rings, then the voice is heard {silence and round(silence, 2)} s after you answer",
              early is None and answered == 200 and silence is not None and silence < 3)  # fmt: skip
        await phone.quiet_for(2.5, limit=60)
        opening = (phone.last_audio or 0) - (phone.first_audio or 0)

        # Your reply, spoken by another Kokoro voice into the phone.
        said_at = time.monotonic()
        phone.mic.say(reply)
        stopped = said_at + len(reply) / RATE
        await asyncio.sleep(len(reply) / RATE)
        end = time.monotonic() + 20
        answer_at = None
        while answer_at is None and time.monotonic() < end:
            answer_at = next((t for t, loud in phone.heard if loud and t > stopped), None)
            await asyncio.sleep(0.02)
        heard = re.findall(r'you \(([\d.]+) s, heard in ([\d.]+) s\): "(.*)"', hub_log.read_text())
        words = heard[-1][2] if heard else ""
        check(f"the speech node heard your reply: {words!r}", re.search(r"push", words, re.I) is not None
              and re.search(r"pull request", words, re.I) is not None)  # fmt: skip
        check(f"the voice answered {answer_at and round(answer_at - stopped, 2)} s after you stopped", answer_at is not None)
        messages: list[str] = []
        end = time.monotonic() + 45
        while not messages and time.monotonic() < end:
            await asyncio.sleep(0.5)
            messages = await for_the_session()
        check(f"your reply reached the session: {messages}",
              len(messages) == 1 and re.search(r"push|pull request", messages[0], re.I) is not None)  # fmt: skip
        await phone.quiet_for(2.0, limit=30)

        hung_up = time.monotonic()
        talked = hung_up - answered_at
        await phone.hang_up()
        ended = await wait_for(agent_log, r"finished: you talked ([\d.]+) s", 40)
        check(f"hung up, the call ended {time.monotonic() - hung_up:.1f} s later, lasting {ended and ended[1]} s (on the phone {talked:.1f} s)",
              ended is not None and abs(float(ended[1]) - talked) < 3)  # fmt: skip
        gone_by = time.monotonic() + 5
        while subprocess.run(["pgrep", "-P", str(agent.pid), "-f", "input-format stream-json"], capture_output=True).stdout and time.monotonic() < gone_by:
            await asyncio.sleep(0.1)
        left = subprocess.run(["pgrep", "-P", str(agent.pid), "-f", "input-format stream-json"], capture_output=True).stdout.split()
        check(f"no Haiku left: {len(left)}", not left)
        transcripts = list((project / ".ringback/calls").glob("*"))
        text = transcripts[0].read_text() if transcripts else ""
        check(f"one transcript, with your words: {[t.name for t in transcripts]}",
              len(transcripts) == 1 and re.search(r"push", text, re.I) is not None)  # fmt: skip
        await socket_side.close()
        await http.close()

        first_words = re.findall(r"first words after ([\d.]+) s", agent_log.read_text())
        timing = [
            f"placed → answered: {args.ring:.1f} s; answered → voice heard: {silence or 0:.2f} s; opening {opening:.1f} s",
            f"Haiku's first words after: {', '.join(first_words)} s (the opening, then your reply)",
            f"your reply: {len(reply) / RATE:.1f} s; heard by the node: {heard[-1][0] if heard else '?'} s of speech, recognised in {heard[-1][1] if heard else '?'} s",
            f"you stopped → the voice answered: {answer_at and round(answer_at - stopped, 2)} s",
        ]
    finally:
        if agent:
            agent.terminate()
            agent.wait()
        hub.terminate()
        hub.wait()
        if node_pid:
            subprocess.run(["ssh", args.node, f"kill {node_pid}; sleep 1; tail -n 60 {args.node_dir}/line-check.log; rm -f {args.node_dir}/line-check.log"],
                           stdout=node_log.open("w"), stderr=subprocess.STDOUT, timeout=30)  # fmt: skip
    # The node loads its models when the hub says a call is coming (`warm`, as the call is
    # placed), and has them before you answer.
    loaded = re.search(r"models loaded, ([\d.]+) s after `(\w+)`", node_log.read_text()) if node_log.exists() else None
    check(f"the node loaded its models when the call was placed: {loaded and loaded[0]}",
          loaded is not None and loaded[2] == "warm" and float(loaded[1]) < args.ring)  # fmt: skip
    if loaded and "timing" in locals():
        timing.insert(1, f"placed → the node's models loaded: {loaded[1]} s (warmed as the call was placed)")
    for name, path in (("hub", hub_log), ("agent", agent_log), ("speech node", node_log)):
        print(f"--- {name}\n{path.read_text()}")
    print("\n".join(lines))
    print("\n".join(timing) if "timing" in locals() else "")
    if args.keep:
        shutil.copytree(tmp, args.keep, dirs_exist_ok=True, ignore=shutil.ignore_patterns("*.sock", "hub"))
    shutil.rmtree(tmp, ignore_errors=True)
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
