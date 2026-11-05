"""View check: a model's live view through the real hub, agent and renderer on this computer, with
a stand-in phone that decodes its video. See README.md.

A session's agent shows a small animated model; the phone opens its view, as the app does, and
the check measures when the first frame comes, that turning the camera changes the picture and
how soon, that it plays and goes to a frame, that closing the view stops the renderer, and that
what can't be viewed is refused, saying why.
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
from aiortc import RTCConfiguration, RTCPeerConnection, RTCSessionDescription

HUB = "127.0.0.1:18797"
TOKEN = "agent-" + secrets.token_hex(8)

# A floor, and a box that moves across it over a second, Y up, 24 frames a second.
MODEL = """#usda 1.0
(
    defaultPrim = "World"
    startTimeCode = 1
    endTimeCode = 24
    timeCodesPerSecond = 24
    framesPerSecond = 24
    upAxis = "Y"
)

def Xform "World"
{
    def Cube "Floor"
    {
        color3f[] primvars:displayColor = [(0.5, 0.5, 0.5)]
        double size = 1
        float3 xformOp:scale = (8, 0.1, 8)
        uniform token[] xformOpOrder = ["xformOp:scale"]
    }

    def Cube "Box"
    {
        color3f[] primvars:displayColor = [(0.85, 0.3, 0.3)]
        double size = 1
        double3 xformOp:translate.timeSamples = {
            1: (-3, 0.6, 0),
            24: (3, 0.6, 0),
        }
        uniform token[] xformOpOrder = ["xformOp:translate"]
    }

    def Sphere "Ball"
    {
        color3f[] primvars:displayColor = [(0.3, 0.5, 0.9)]
        double radius = 0.6
        double3 xformOp:translate = (0, 0.6, -2.5)
        uniform token[] xformOpOrder = ["xformOp:translate"]
    }
}
"""

failures = 0


def check(name: str, passed: bool) -> None:
    global failures
    print(("ok    " if passed else "FAIL  ") + name, flush=True)
    failures += 0 if passed else 1


async def until(ready, limit: float, step: float = 0.02) -> float | None:
    """Seconds until ready() is true, or None after limit."""
    start = time.monotonic()
    while time.monotonic() - start < limit:
        if ready():
            return time.monotonic() - start
        await asyncio.sleep(step)
    return None


class Phone:
    """The app's side of a model's view: video received only, and a data channel."""

    def __init__(self):
        # No STUN, as the app has none: its offer has only its own addresses.
        self.pc = RTCPeerConnection(RTCConfiguration(iceServers=[]))
        self.gathered = self.answered = 0.0
        self.pc.addTransceiver("video", direction="recvonly")
        self.channel = self.pc.createDataChannel("control")
        # As the app's: where it looks from while it moves, unordered and never resent.
        self.camera = self.pc.createDataChannel("camera", ordered=False, maxRetransmits=0)
        self.frames: list[tuple[float, np.ndarray]] = []
        self.said: list[dict] = []
        self.channel.on("message", lambda m: self.said.append(json.loads(m)))
        self.pc.on("track", lambda track: asyncio.ensure_future(self._receive(track)))

    async def _receive(self, track):
        while True:
            try:
                frame = await track.recv()
            except Exception:
                return
            self.frames.append((time.monotonic(), frame.to_ndarray(format="rgb24")))

    async def open(self, http, token: str, key: str, shown: str) -> tuple[int, dict]:
        """Offers the view; then gathered and answered say how long the phone took to gather
        its addresses, and the hub to answer."""
        start = time.monotonic()
        await self.pc.setLocalDescription(await self.pc.createOffer())
        self.gathered = time.monotonic() - start
        body = {"token": token, "key": key, "shown_id": shown, "sdp": self.pc.localDescription.sdp, "width": 640, "height": 480}
        async with http.post(f"http://{HUB}/view", json=body) as r:
            answer = await r.json()
            self.answered = time.monotonic() - start - self.gathered
            if r.status == 200:
                await self.pc.setRemoteDescription(RTCSessionDescription(answer["sdp"], "answer"))
            return r.status, answer

    def send(self, message: dict) -> None:
        self.channel.send(json.dumps(message))

    def moving(self, message: dict) -> None:
        self.camera.send(json.dumps(message))

    def latest(self) -> np.ndarray | None:
        return self.frames[-1][1] if self.frames else None

    def state(self) -> dict | None:
        states = [m for m in self.said if "playing" in m]
        return states[-1] if states else None


def differs(a: np.ndarray, b: np.ndarray) -> float:
    return float(np.abs(a.astype(int) - b.astype(int)).mean())


def psnr(a: np.ndarray, b: np.ndarray) -> float:
    """How close two pictures are, in dB: 40 or more looks the same, under 30 visibly worse."""
    error = float(np.mean((a.astype(float) - b.astype(float)) ** 2))
    return 99.0 if error == 0 else 10 * np.log10(255**2 / error)


def picture(path: Path) -> np.ndarray:
    import av

    with av.open(str(path)) as container:
        return next(container.decode(video=0)).to_ndarray(format="rgb24")


async def main() -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--keep", type=Path, help="keep the logs and state in this directory")
    args = p.parse_args()
    root = Path(__file__).resolve().parents[2]
    hub_binary = Path(os.environ.get("RINGBACK_HUB", root / "hub/build/ringback-hub"))
    agent_binary = Path(os.environ.get("RINGBACK_AGENT", root / "agent/build/ringback-agent"))
    renderer = Path(os.environ.get("RINGBACK_RENDER", root / "render/build" /
                                   ("ringback-render-apple" if sys.platform == "darwin" else "ringback-render")))  # fmt: skip
    for binary, build in [(hub_binary, "(cd hub && go build -o build/ringback-hub .)"),
                          (agent_binary, "(cd agent && go build -o build/ringback-agent .)"),
                          (renderer, "render/build.sh apple, or render/build.sh")]:  # fmt: skip
        if not binary.exists():
            sys.exit(f"build {binary.name} first: {build}")
    home = Path(tempfile.mkdtemp(prefix="ringback-view-check-"))
    # The agent's socket path has to be short.
    sockets = Path(tempfile.mkdtemp(prefix="rbvc-", dir="/tmp"))
    (home / ".config/ringback").mkdir(parents=True)
    (home / ".config/ringback/hub.toml").write_text(f'listen = "{HUB}"\n[tokens]\nagent = "{TOKEN}"\n')
    (home / "agent.toml").write_text(f'hub = "{HUB}"\ntoken = "{TOKEN}"\nrender = "{renderer}"\nname = "view-check"\n')
    model = home / "model.usda"
    model.write_text(MODEL)
    hub_log, agent_log = home / "hub.log", home / "agent.log"
    env = os.environ | {"HOME": str(home)}
    hub = subprocess.Popen([str(hub_binary), "serve"], env=env, stdout=hub_log.open("w"), stderr=subprocess.STDOUT)
    agent = None
    phone = None
    try:
        await until(lambda: "hub listening" in hub_log.read_text(), 5)
        agent = subprocess.Popen([str(agent_binary), "serve", "--socket", str(sockets / "v.sock"), "--config", str(home / "agent.toml")],
                                 env=env, stdout=agent_log.open("w"), stderr=subprocess.STDOUT)  # fmt: skip
        check("the agent links to the hub", await until(lambda: "linked to the hub" in agent_log.read_text(), 10) is not None)

        printed = subprocess.run([str(hub_binary), "pair"], env=env, capture_output=True, text=True).stdout
        code = re.search(r"code=([A-Za-z0-9_-]+)", printed).group(1)
        token = "view" * 16
        async with aiohttp.ClientSession() as http:
            async with http.post(f"http://{HUB}/pair", json={"code": code, "token": token, "environment": "sandbox", "device": "check phone"}) as r:
                key = (await r.json()).get("key", "")
        check("the phone pairs", len(key) == 64)

        socket = aiohttp.UnixConnector(path=str(sockets / "v.sock"))
        async with aiohttp.ClientSession(connector=socket) as agent_http:
            async def show(path: Path) -> dict:
                body = {"path": str(path), "project": "view-check", "cwd": str(home), "caption": "A box that moves"}
                async with agent_http.post("http://voice/sessions/view-check/show", json=body) as r:
                    return await r.json()

            started = time.monotonic()
            shown = await show(model)
            check(f"the session shows the model ({time.monotonic() - started:.2f} s, its picture rendered): {shown.get('id', shown)}",
                  len(shown.get("id", "")) == 36)  # fmt: skip
            gone = home / "gone.usda"
            gone.write_text(MODEL)
            shown_gone = await show(gone)
            gone.unlink()
            image = home / "picture.png"
            shutil.copy(home / ".local/state/ringback/shown" / shown["id"] / "poster", image)
            shown_image = await show(image)

        async with aiohttp.ClientSession() as http:
            # What can't be viewed is refused, saying why.
            status, answer = await Phone().open(http, token, key, shown_image["id"])
            check(f"an image has no view: {status}", status == 404)
            status, answer = await Phone().open(http, token, key, shown_gone["id"])
            check(f"a model gone from its computer: {status} {answer.get('error')}", status == 410 and "no longer at" in answer.get("error", ""))

            phone = Phone()
            opened = time.monotonic()
            status, answer = await phone.open(http, token, key, shown["id"])
            check(f"the phone opens the model's view: {status}", status == 200)
            came = await until(lambda: phone.frames, 20)
            first = phone.frames[0][0] - opened if came is not None else None
            check(f"the first frame comes {first:.2f} s after the phone opens it: {phone.gathered:.2f} s gathering its "
                  f"addresses, {phone.answered:.2f} s for the hub to answer, its renderer starting meanwhile"
                  if first is not None else "no frame came in 20 s", first is not None)  # fmt: skip
            ready = next((m for m in phone.said if m.get("ready")), None)
            await until(lambda: any(m.get("ready") for m in phone.said), 5)
            ready = next((m for m in phone.said if m.get("ready")), None)
            check(f"the renderer's ready reaches the phone: {ready and {k: ready[k] for k in ('frames', 'fps', 'animated', 'up')}}",
                  bool(ready) and ready["frames"] == 24 and ready["animated"] is True)  # fmt: skip
            frame = phone.latest()
            check(f"the frames are as big as asked: {frame.shape[1] if frame is not None else 0} × {frame.shape[0] if frame is not None else 0}",
                  frame is not None and frame.shape[:2] == (480, 640))  # fmt: skip

            # Settled, the home view is as sharp as the renderer's own still of it.
            await asyncio.sleep(1)
            subprocess.run([str(renderer), "still", str(model), str(home / "still.png"), "640", "480"], capture_output=True)
            still = picture(home / "still.png")
            came_first, settled = psnr(phone.frames[0][1], still), psnr(phone.latest(), still)
            check(f"settled, the view matches the renderer's still: {settled:.1f} dB (its first frame {came_first:.1f} dB)", settled >= 32)

            # Turned, the picture changes.
            before = phone.latest()
            home_view = ready["home"]
            turned = dict(home_view, yaw=home_view["yaw"] + 1.2)
            sent = time.monotonic()
            phone.moving({"camera": turned})
            took = await until(lambda: differs(phone.latest(), before) > 8, 5)
            saved = {"first": phone.frames[0][1], "settled": before, "turned": phone.latest()}
            check(f"turning the camera changes the picture in {took or 0:.3f} s (hub, agent and renderer on this computer)"
                  if took else "turning the camera changed nothing in 5 s", took is not None)  # fmt: skip
            await asyncio.sleep(1)
            settled = len(phone.frames)
            await asyncio.sleep(1)
            check(f"still, it sends nothing more: {len(phone.frames) - settled} frames in 1 s", len(phone.frames) - settled <= 2)
            # Settled after the turn, the view is as sharp as a key frame of it.
            turned_settled = phone.latest()
            phone.send({"keyframe": True})
            await asyncio.sleep(1)
            sharp = psnr(turned_settled, phone.latest())
            check(f"settled after a turn, the view matches a key frame of it: {sharp:.1f} dB", sharp >= 32)

            # It plays, and goes to a frame.
            phone.send({"play": True})
            await asyncio.sleep(0.7)
            state = phone.state()
            check(f"it plays: {state}", bool(state) and state["playing"] is True and state["frame"] > 0)
            phone.send({"frame": 23})
            went = await until(lambda: (phone.state() or {}).get("frame") == 23 and phone.state()["playing"] is False, 3)
            check(f"it goes to the last frame, paused: {phone.state()}", went is not None)

            await phone.pc.close()
            stopped = await until(lambda: "stopping, the phone closed it" in agent_log.read_text(), 40)
            check(f"closing the view stops the renderer ({stopped or 0:.1f} s)", stopped is not None)
            check(f"{len(phone.frames)} frames in all", True)
    finally:
        if phone:
            await phone.pc.close()
        for process in (agent, hub):
            if process:
                process.terminate()
                process.wait(timeout=10)
        if args.keep:
            shutil.copytree(home, args.keep, dirs_exist_ok=True)
            for name, frame in (saved if "saved" in locals() else {}).items():
                # As PPM, which anything opens; sips turns it into PNG on a Mac.
                height, width = frame.shape[:2]
                (args.keep / f"{name}.ppm").write_bytes(b"P6 %d %d 255\n" % (width, height) + frame.tobytes())
        if failures:
            print("--- hub log\n" + hub_log.read_text()[-3000:] + "\n--- agent log\n" + agent_log.read_text()[-3000:])
        shutil.rmtree(home, ignore_errors=True)
        shutil.rmtree(sockets, ignore_errors=True)
    print(f"{failures} failed" if failures else "all passed")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
