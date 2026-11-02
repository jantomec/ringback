"""Checks the Go hub's side of a call with stand-ins for everything around it: the phone
(phone-check's, an aiortc peer as the app makes it), an agent and a speech node, each a
WebSocket client speaking docs/protocol.md. It runs the hub built in hub/build, on localhost,
with its config and state in a throwaway HOME. See README.md.

    uv run --project voice tools/hub-check/hub_check.py [--keep DIR]
"""

import argparse
import asyncio
import json
import os
import secrets
import shutil
import struct
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import aiohttp
import numpy as np

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "phone-check"))
import phone_check  # noqa: E402

PORT = 18827
HUB = f"127.0.0.1:{PORT}"
TOKENS = {"agent": "agent-" + secrets.token_hex(8), "speech": "speech-" + secrets.token_hex(8)}
SPEAKING = 24000
SECS_PER_CHAR = 0.06  # the stand-in node's speech: this long per character of text
REPLY = "Hello there. This is the hub check, speaking a reply that has two sentences in it."
LONG = " ".join(["This is a long reply that goes on for a while, so you can cut it off."] * 4)

lines: list[str] = []
fails = 0


def check(name: str, passed: bool) -> None:
    global fails
    lines.append(f"{'ok  ' if passed else 'FAIL'}  {name}")
    fails += not passed


class Link:
    """A WebSocket to the hub, as an agent or a speech node opens it; it keeps every message."""

    def __init__(self, kind: str):
        self.kind = kind
        self.got: list[tuple[float, dict]] = []
        self.audio: list[tuple[float, bytes]] = []
        self.ws = None
        self.session = None
        self.handle = None  # called with each message

    async def open(self) -> dict:
        self.session = aiohttp.ClientSession()
        self.ws = await self.session.ws_connect(
            f"http://{HUB}/{self.kind}", headers={"Authorization": f"Bearer {TOKENS[self.kind]}"}
        )
        await self.ws.send_json({"type": "hello", "version": 1, "name": f"check-{self.kind}"})
        welcome = await self.ws.receive_json(timeout=5)
        asyncio.create_task(self._read())
        return welcome

    async def _read(self):
        async for message in self.ws:
            if message.type == aiohttp.WSMsgType.BINARY:
                self.audio.append((time.monotonic(), message.data))
            elif message.type == aiohttp.WSMsgType.TEXT:
                data = json.loads(message.data)
                self.got.append((time.monotonic(), data))
                if self.handle:
                    await self.handle(data)

    def of(self, kind: str) -> list[dict]:
        return [m for _, m in self.got if m.get("type") == kind]

    async def close(self):
        if self.ws:
            await self.ws.close()
        if self.session:
            await self.session.close()


async def until(ready, limit: float) -> float | None:
    t = time.monotonic()
    while not ready():
        if time.monotonic() - t > limit:
            return None
        await asyncio.sleep(0.02)
    return time.monotonic() - t


async def main() -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--keep", type=Path, help="keep the hub's log and state in this directory")
    args = p.parse_args()
    root = Path(__file__).resolve().parents[2]
    binary = Path(os.environ.get("RINGBACK_HUB", root / "hub" / "build" / "ringback-hub"))
    if not binary.exists():
        sys.exit("build the hub first: (cd hub && go build -o build/ringback-hub .)")
    home = Path(tempfile.mkdtemp(prefix="ringback-hub-check-"))
    (home / ".config/ringback").mkdir(parents=True)
    (home / ".config/ringback/hub.toml").write_text(
        f'listen = "{HUB}"\n[tokens]\nagent = "{TOKENS["agent"]}"\nspeech = "{TOKENS["speech"]}"\n'
    )
    log = home / "hub.log"
    hub = subprocess.Popen([*os.environ.get("RINGBACK_HUB_ARCH", "").split(), str(binary), "serve"], env=os.environ | {"HOME": str(home)}, stdout=log.open("w"), stderr=subprocess.STDOUT)
    phone_check.VOICE = HUB
    agent, node = Link("agent"), Link("speech")
    try:
        await until(lambda: "hub listening" in log.read_text(), 5)

        # A link without the token is refused; with it, hello is welcomed.
        async with aiohttp.ClientSession() as http:
            try:
                async with http.ws_connect(f"http://{HUB}/agent", headers={"Authorization": "Bearer nope"}):
                    refused = False
            except aiohttp.WSServerHandshakeError as e:
                refused = e.status == 401
        check("a link with a wrong token is refused (401)", refused)
        check(f"the agent's hello is welcomed: {await agent.open()}", True)

        # Before any speech node links, a call the agent places waits instead of ringing, and
        # the app's status says why. The phone registers with a notification token, so the
        # hub tries to tell it, and with no APNs key here, that push fails, which the status
        # says too.
        phone_token = "abcd" * 8
        async with aiohttp.ClientSession() as http:
            async with http.post(f"http://{HUB}/register", json={"token": phone_token, "environment": "sandbox",
                                 "device": "check phone", "notification_token": "ef01" * 8}) as r:  # fmt: skip
                registered = r.status
        early_id = "known-" + secrets.token_hex(12)

        async def place(call_id: str) -> dict:
            n = len(agent.of("reply"))
            await agent.ws.send_json({"type": "place", "id": 900 + n, "call_id": call_id, "project": "demo",
                                      "subject": "Parser fix ready", "session_id": "s1", "question": False})  # fmt: skip
            await until(lambda: len(agent.of("reply")) > n, 5)
            return agent.of("reply")[-1]

        async def waiting() -> dict:
            async with aiohttp.ClientSession() as http:
                async with http.post(f"http://{HUB}/waiting", json={"token": phone_token}) as r:
                    return await r.json()

        held = await place(early_id)
        check(f"with no speech node linked, a call waits instead of ringing: {held}",
              registered == 200 and held.get("held") is True and held.get("reason") == "no speech")  # fmt: skip
        await asyncio.sleep(0.5)
        answer = await waiting()
        listed = [c for c in answer["calls"] if c["call_id"] == early_id]
        status = answer.get("status", {})
        check(f"the app's waiting calls list it, with its reason: {listed}", len(listed) == 1 and listed[0].get("reason") == "no speech")
        check(f"and its status says calls can't be talked through: {status.get('speech')}",
              status.get("speech", {}).get("ok") is False and status["speech"]["waiting"] == 1)  # fmt: skip
        check(f"and that the last push to the phone failed: {status.get('push')}",
              status.get("push", {}).get("ok") is False and status["push"].get("problem") == "key")  # fmt: skip
        check(f"and the Macs linked: {status.get('macs')}", status.get("macs") == [{"name": "check-agent", "sessions": 0}])
        async with aiohttp.ClientSession() as http:
            async with http.post(f"http://{HUB}/register", json={"token": phone_token, "environment": "sandbox",
                                 "device": "check phone", "notification_token": "ef01" * 8}) as r:  # fmt: skip
                pass
        again = (await waiting()).get("status", {}).get("push", {})
        check(f"registering again doesn't hide a key problem: {again.get('problem')}", again.get("problem") == "key")

        # A node links: the waiting call asks again, and rings.
        check(f"the speech node's hello is welcomed: {await node.open()}", True)
        await until(lambda: agent.of("reask"), 3)
        check(f"once a node links, the agent is asked to ask again: {agent.of('reask')}",
              [m["call_ids"] for m in agent.of("reask")] == [[early_id]])  # fmt: skip
        rang = await place(early_id)
        answer = await waiting()
        check(f"asked again, it rings, and the status is fine: {rang}, {answer.get('status', {}).get('speech')}",
              rang.get("ok") is True and not rang.get("held") and not [c for c in answer["calls"] if c["call_id"] == early_id]
              and answer["status"]["speech"]["ok"] is True)  # fmt: skip

        # The stand-in agent owns calls whose ID starts with "known-", and speaks a reply once
        # you've answered and the audio connected. The stand-in node makes a tone for each
        # piece, in 100 ms chunks, and on speak_cancel drops every piece of the stream asked so far.
        replies: list[str] = [REPLY]
        asked: dict[int, int] = {}  # stream → the last piece asked
        cancelled: dict[int, int] = {}  # stream → pieces up to this one are dropped

        async def agent_handle(m):
            if m["type"] == "offer":
                ok = m["call_id"].startswith("known-")
                await agent.ws.send_json({"type": "reply", "id": m["id"], "ok": ok} | ({} if ok else {"status": 403, "error": "unknown call"}))
            elif m["type"] in ("answered", "connected"):
                kinds = {x["type"] for x in agent.of("answered") + agent.of("connected") if x["call_id"] == m["call_id"]}
                if kinds == {"answered", "connected"} and replies:
                    text = replies.pop(0)
                    for i in range(0, len(text), 9):
                        await agent.ws.send_json({"type": "say", "call_id": m["call_id"], "reply": 1, "text": text[i:i + 9]})
                        await asyncio.sleep(0.01)
                    await agent.ws.send_json({"type": "said", "call_id": m["call_id"], "reply": 1})

        async def node_handle(m):
            if m["type"] == "speak":
                stream, piece = m["stream"], m["piece"]
                asked[stream] = piece
                samples = int(len(m["text"]) * SECS_PER_CHAR * SPEAKING)
                t = np.arange(samples) / SPEAKING
                tone = (np.sin(2 * np.pi * 440 * t) * 8000).astype("<i2")
                for i in range(0, samples, SPEAKING // 10):
                    if piece <= cancelled.get(stream, 0):
                        return
                    await node.ws.send_bytes(struct.pack(">II", stream, piece) + tone[i:i + SPEAKING // 10].tobytes())
                    await asyncio.sleep(0.005)
                await node.ws.send_json({"type": "spoken", "stream": stream, "piece": piece, "secs": samples / SPEAKING, "took": 0.05})
            elif m["type"] == "speak_cancel":
                cancelled[m["stream"]] = asked.get(m["stream"], 0)

        agent.handle = agent_handle
        node.handle = lambda m: asyncio.create_task(node_handle(m)) if m["type"] == "speak" else node_handle(m)

        # A call the agent doesn't know is refused, as the voice server refused it.
        stranger = phone_check.Phone()
        status = await stranger.offer("unknown-" + secrets.token_hex(12), ringing=False)
        await stranger.hang_up()
        check(f"an offer for a call no agent owns is refused: {status}", status == 403)

        # A call answered at once: the voice is heard, your audio reaches the node.
        call_id = "known-" + secrets.token_hex(12)
        phone = phone_check.Phone()
        t0 = time.monotonic()
        status = await phone.offer(call_id, ringing=False)
        check(f"an offer the agent accepts is answered: {status}", status == 200)
        connected = await until(lambda: agent.of("connected"), 5)
        check(f"the agent hears answered and connected ({connected and round(connected, 2)} s)",
              bool(agent.of("answered")) and connected is not None)  # fmt: skip
        hear = node.of("hear")
        check(f"the node is asked to hear the call: {hear}", len(hear) == 1 and hear[0].get("sensitivity") == 0.5)
        warms = [i for i, (_, m) in enumerate(node.got) if m["type"] == "warm"]
        first_hear = next(i for i, (_, m) in enumerate(node.got) if m["type"] == "hear")
        check(f"and warmed up before it: {len(warms)} warm", bool(warms) and warms[0] < first_hear)
        heard_in = await phone.heard_after(t0)
        check(f"the phone hears the voice {heard_in and round(heard_in, 2)} s after the offer", heard_in is not None and heard_in < 3)
        await until(lambda: len(node.audio) >= 25, 3)
        sizes = {len(d) for _, d in node.audio}
        check(f"your audio reaches the node in 20 ms frames: {len(node.audio)} frames, sizes {sizes}",
              len(node.audio) >= 25 and sizes == {4 + 640})  # fmt: skip
        speaking = agent.of("speaking")
        check(f"the agent hears the voice is speaking: {len(speaking)}", len(speaking) >= 1)
        idle_in = await until(lambda: agent.of("idle"), 15)
        heard = [m["text"] for m in agent.of("heard")]
        check(f"what you've heard grows to the whole reply: {heard[-1] if heard else None!r}",
              bool(heard) and heard[-1] == REPLY and all(heard[i] in heard[i + 1] or len(heard[i]) <= len(heard[i + 1]) for i in range(len(heard) - 1)))  # fmt: skip
        check(f"then the agent hears the voice is idle ({idle_in and round(idle_in, 1)} s)", idle_in is not None)
        pieces = [m["text"] for m in node.of("speak")]
        check(f"the reply was spoken in pieces, the first sentence alone: {pieces}",
              len(pieces) >= 2 and pieces[0] == "Hello there.")  # fmt: skip

        # Your turn reaches the agent.
        stream = hear[0]["stream"]
        await node.ws.send_json({"type": "turn", "stream": stream, "text": "Tell me more.", "secs": 1.2, "took": 0.07})
        await until(lambda: agent.of("turn"), 3)
        check(f"your turn reaches the agent: {agent.of('turn')}", [m["text"] for m in agent.of("turn")] == ["Tell me more."])

        # A long reply, cut off: the voice stops, the node cancels, the agent hears what you'd heard.
        async def say(n: int, text: str, end: bool = True, step: int = 12):
            for i in range(0, len(text), step):
                await agent.ws.send_json({"type": "say", "call_id": call_id, "reply": n, "text": text[i:i + step]})
            if end:
                await agent.ws.send_json({"type": "said", "call_id": call_id, "reply": n})

        before = len(agent.of("speaking"))
        await say(2, LONG, end=False)
        await until(lambda: len(agent.of("speaking")) > before, 5)
        await asyncio.sleep(1.5)
        cut = time.monotonic()
        await node.ws.send_json({"type": "speech_started", "stream": stream})
        await until(lambda: agent.of("interrupted"), 3)
        interrupted = agent.of("interrupted")
        await asyncio.sleep(0.6)
        after = [t for t, loud in phone.heard if t > cut + 0.3 and loud]
        check(f"cut off, the agent hears what you'd heard of reply 2: {interrupted and interrupted[0]['heard'][:50]!r}",
              len(interrupted) == 1 and interrupted[0]["reply"] == 2 and LONG.startswith(interrupted[0]["heard"])
              and 0 < len(interrupted[0]["heard"]) < len(LONG))  # fmt: skip
        check(f"the node is told to stop speaking: {len(node.of('speak_cancel'))}", len(node.of("speak_cancel")) >= 1)
        check(f"and the phone hears no more of the voice: {len(after)} loud frames after", len(after) == 0)

        # What comes of the cut reply after the interruption (written before the agent heard of
        # it) is dropped.
        spoken = len(node.of("speak"))
        await say(2, " And this late slice should never be spoken.")
        await asyncio.sleep(0.8)
        check(f"late slices of the cut reply aren't spoken: {node.of('speak')[spoken:]}", len(node.of("speak")) == spoken)
        await node.ws.send_json({"type": "speech_stopped", "stream": stream})

        # You go on talking right after your turn, before its reply has come: it's cut off
        # unheard, and when it comes it's dropped; the reply to your next turn is spoken.
        await node.ws.send_json({"type": "turn", "stream": stream, "text": "Wait.", "secs": 0.4, "took": 0.05})
        await until(lambda: len(agent.of("turn")) == 2, 3)
        await node.ws.send_json({"type": "speech_started", "stream": stream})
        await until(lambda: len(agent.of("interrupted")) == 2, 3)
        second = agent.of("interrupted")[1:]
        check(f"talking on before the reply came cuts off reply 3, unheard: {second}",
              second == [{"type": "interrupted", "call_id": call_id, "reply": 3, "heard": ""}])  # fmt: skip
        await say(3, "Too late for this one. It's dropped.")
        await node.ws.send_json({"type": "speech_stopped", "stream": stream})
        await node.ws.send_json({"type": "turn", "stream": stream, "text": "Go on.", "secs": 0.6, "took": 0.05})
        idles = len(agent.of("idle"))
        await say(4, "Going on now.")
        await until(lambda: len(agent.of("idle")) > idles, 10)
        late = [m["text"] for m in node.of("speak")[spoken:]]
        check(f"only the next turn's reply is spoken: {late}", late == ["Going on now."])
        heard = agent.of("heard")
        empty = [m for m in agent.of("heard") if not m["text"]]
        check(f"the agent never hears an empty heard: {len(empty)}", not empty)
        check(f"and heard to its end, as reply 4, then idle: {heard[-1]['text']!r}, {len(agent.of('idle')) - idles} idle",
              heard[-1]["text"] == "Going on now." and heard[-1]["reply"] == 4 and len(agent.of("idle")) == idles + 1)  # fmt: skip

        # A reply with nothing to say (only a tool call): the voice is idle.
        await node.ws.send_json({"type": "turn", "stream": stream, "text": "Do it.", "secs": 0.5, "took": 0.05})
        await agent.ws.send_json({"type": "said", "call_id": call_id, "reply": 5})
        await until(lambda: len(agent.of("idle")) > idles + 1, 3)
        check(f"a reply with nothing to say leaves the voice idle: {len(agent.of('idle')) - idles - 1} idle",
              len(agent.of("idle")) == idles + 2)  # fmt: skip
        flags = [(m["text"], m["interrupted"]) for m in agent.of("turn")]
        check(f"a turn says whether you cut the voice off since the last: {flags}",
              [f for _, f in flags] == [False, True, True, False])  # fmt: skip

        # Hanging up: the agent hears how long you talked.
        talked = time.monotonic() - t0
        await phone.hang_up()
        ended_in = await until(lambda: agent.of("ended"), 40)
        ended = agent.of("ended")
        check(f"hung up, the agent hears it ended {ended_in and round(ended_in, 1)} s later, lasted {ended and ended[0]['lasted']}",
              len(ended) == 1 and ended[0]["lasted"] is not None and abs(ended[0]["lasted"] - talked) < 3)  # fmt: skip
        check(f"and the node to stop hearing: {node.of('hear_end')}", len(node.of("hear_end")) == 1)

        # The agent's link drops during a call: the hub ends the call.
        second = phone_check.Phone()
        status = await second.offer("known-" + secrets.token_hex(12), ringing=False)
        await until(lambda: len(agent.of("connected")) == 2, 5)
        await agent.close()
        gone = await until(lambda: len(node.of("hear_end")) == 2, 5)
        check(f"the agent's link drops mid-call: the hub ends the call ({gone and round(gone, 2)} s)", status == 200 and gone is not None)
        await second.hang_up()
    finally:
        await agent.close()
        await node.close()
        hub.terminate()
        hub.wait()
    print(log.read_text())
    print("\n".join(lines))
    if args.keep:
        shutil.copytree(home, args.keep, dirs_exist_ok=True)
    shutil.rmtree(home, ignore_errors=True)
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
