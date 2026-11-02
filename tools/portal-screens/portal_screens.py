"""Screenshots the hub's portal in every state its status page's map can show, in light and
dark, on a desk (1440 px wide) and on a phone (390 px), with the calls, log, setup and sign-in
pages. Each state is a throwaway hub with its own HOME: stand-in Macs and speech nodes on the
links, a fake phone registered, calls placed and reported, signed in through headless Chrome
(its DevTools protocol). Nothing reaches Apple: the pushes that would are avoided, or fail on a
key that can't be read. See README.md.

    uv run --project voice tools/portal-screens/portal_screens.py [OUT] [--only NAME,...]
"""

import argparse
import asyncio
import base64
import hashlib
import hmac
import json
import os
import secrets
import shutil
import struct
import subprocess
import sys
import tempfile
import time
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path

import aiohttp

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "tools" / "phone-check"))
import phone_check  # noqa: E402

CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
BINARY = REPO / "hub" / "build" / "ringback-hub"
USER, PASSWORD = "demo", "a long demo password"
SIZES = {"desktop": (1440, 900), "phone": (390, 844)}


def totp(secret: str, step: int) -> str:
    """RFC 6238: SHA-1, 6 digits, 30 s steps, as the hub's passcodes."""
    key = base64.b32decode(secret + "=" * (-len(secret) % 8))
    mac = hmac.new(key, struct.pack(">Q", step), hashlib.sha1).digest()
    o = mac[-1] & 0x0F
    return f"{(struct.unpack('>I', mac[o:o + 4])[0] & 0x7FFFFFFF) % 1_000_000:06d}"


def iso(delta: timedelta = timedelta()) -> str:
    return (datetime.now(timezone.utc) + delta).strftime("%Y-%m-%dT%H:%M:%SZ")


def local_iso(delta: timedelta) -> str:
    return (datetime.now().astimezone() + delta).isoformat(timespec="seconds")


# The sessions each stand-in Mac says are listening. Generic names only: this repo is public.
def sessions(mac: str) -> list[dict]:
    h = timedelta(hours=1)
    return {
        "studio": [
            {"session_id": "1f3e5a7c-0000-4000-8000-000000000001", "project": "parser", "since": local_iso(-4 * h),
             "subject": "Which branch should the fix go to?", "briefed_at": local_iso(-h / 3)},
            {"session_id": "2a4c6e80-0000-4000-8000-000000000002", "project": "docs-site", "since": local_iso(-3 * h),
             "subject": "Docs site rebuilt, three links fixed", "briefed_at": local_iso(-h / 2)},
            {"session_id": "3b5d7f91-0000-4000-8000-000000000003", "project": "ringback", "since": local_iso(-h / 4)},
        ],
        "laptop": [
            {"session_id": "4c6e8002-0000-4000-8000-000000000004", "project": "website", "since": local_iso(-2 * h),
             "subject": "Dark mode toggle ready to review", "briefed_at": local_iso(-h / 6)},
        ],
        "a-mac-with-a-rather-long-name-indeed": [
            {"session_id": "5d7f9113-0000-4000-8000-000000000005", "project": "a-project-with-a-rather-long-name-indeed",
             "since": local_iso(-5 * h), "briefed_at": local_iso(-h),
             "subject": "The migration ran on staging; it needs a decision on the old tables before production"},
        ],
        "build-box": [],
    }[mac]


class Link:
    """A stand-in agent or speech node on its link: it accepts every request it's asked
    (offers included) and says its models are loaded when told a call is coming."""

    def __init__(self, hub, kind, name):
        self.hub, self.kind, self.name, self.ws, self.http = hub, kind, name, None, None

    async def open(self):
        self.http = aiohttp.ClientSession()
        self.ws = await self.http.ws_connect(f"{self.hub.base}/{self.kind}",
                                             headers={"Authorization": f"Bearer {self.hub.tokens[self.kind]}"})
        hello = {"type": "hello", "version": 1, "name": self.name}
        if self.kind == "speech":
            hello |= {"hearing": True, "speaking": True}
        await self.ws.send_json(hello)
        await self.ws.receive_json(timeout=5)
        asyncio.create_task(self._read())
        return self

    async def _read(self):
        async for message in self.ws:
            if message.type != aiohttp.WSMsgType.TEXT:
                continue
            m = json.loads(message.data)
            if self.kind == "agent" and "id" in m and m.get("type") != "reply":
                await self.ws.send_json({"type": "reply", "id": m["id"], "ok": True})
            if self.kind == "speech" and m.get("type") in ("warm", "hear"):
                await self.ws.send_json({"type": "models", "loaded": True})

    async def send(self, message):
        await self.ws.send_json(message)

    async def close(self):
        await self.ws.close()
        await self.http.close()


class Hub:
    """A throwaway hub: its HOME, config and state under the run's folder, on its own port."""

    def __init__(self, root: Path, name: str, port: int, push: str | None):
        self.home = root / "state" / name
        shutil.rmtree(self.home, ignore_errors=True)
        config = self.home / ".config" / "ringback"
        config.mkdir(parents=True)
        self.state = self.home / ".local" / "state" / "ringback"
        self.state.mkdir(parents=True)
        self.port, self.base = port, f"http://127.0.0.1:{port}"
        self.tokens = {"agent": "agent-" + secrets.token_hex(16), "speech": "speech-" + secrets.token_hex(16)}
        text = f'listen = "127.0.0.1:{port}"\n[tokens]\nagent = "{self.tokens["agent"]}"\nspeech = "{self.tokens["speech"]}"\n'
        if push:
            # A key Apple has never seen ("good": nothing here pushes with it), or a file that
            # isn't a key ("broken").
            key = config / "AuthKey_DEMO000000.p8"
            if push == "good":
                pem = subprocess.run("openssl ecparam -name prime256v1 -genkey -noout | openssl pkcs8 -topk8 -nocrypt",
                                     shell=True, capture_output=True, text=True, check=True).stdout
                key.write_text(pem)
            else:
                key.write_text("not a key\n")
            text += ('[apns]\nteam_id = "ABCDE12345"\nbundle_id = "com.example.ringback"\n'
                     f'[apns.sandbox]\nkey_id = "DEMO000000"\nkey_file = "~/.config/ringback/{key.name}"\n')
        (config / "hub.toml").write_text(text)
        self.env = os.environ | {"HOME": str(self.home)}
        self.process, self.links, self.callers = None, [], []
        self.secret, self.used = "", 0

    def make_account(self):
        """`ringback-hub portal-user`, as you'd answer it: the password twice, then the
        passcode for the setup key it prints."""
        p = subprocess.Popen([str(BINARY), "portal-user", USER], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE, text=True, env=self.env)
        p.stdin.write(f"{PASSWORD}\n{PASSWORD}\n")
        p.stdin.flush()
        for line in p.stdout:
            if line.strip().startswith("setup key:"):
                self.secret = line.split("setup key:")[1].split("(")[0].replace(" ", "").strip()
                break
        self.used = int(time.time() // 30)
        p.stdin.write(totp(self.secret, self.used) + "\n")
        p.stdin.close()
        rest = p.stdout.read()
        if p.wait(20) != 0 or "The portal's account is demo" not in rest:
            raise RuntimeError("portal-user: " + rest + p.stderr.read())

    def code(self) -> str:
        """A passcode the hub hasn't taken yet: now, or the next step's."""
        self.used = max(int(time.time() // 30), self.used + 1)
        return totp(self.secret, self.used)

    async def start(self):
        log = (self.home / "hub.log").open("w")
        self.process = subprocess.Popen([str(BINARY), "serve"], env=self.env, stdout=log, stderr=subprocess.STDOUT)
        async with aiohttp.ClientSession() as http:
            for _ in range(100):
                try:
                    async with http.get(f"{self.base}/ping") as r:
                        if r.status == 200:
                            return
                except aiohttp.ClientError:
                    pass
                await asyncio.sleep(0.05)
        raise RuntimeError("the hub didn't start")

    async def stop(self):
        for caller in self.callers:
            await caller.hang_up()
        for link in self.links:
            try:
                await link.close()
            except Exception:
                pass
        if self.process:
            self.process.terminate()
            self.process.wait()

    async def post(self, path, body):
        async with aiohttp.ClientSession() as http:
            async with http.post(self.base + path, json=body) as r:
                return r.status, await r.json(content_type=None)

    async def phone(self, token, name, on_device, environment="sandbox"):
        await self.post("/register", {"token": token, "environment": environment, "device": name, "on_device": on_device,
                                      "notification_token": token[::-1]})
        await self.post("/check", {"token": token})

    async def agent(self, name):
        link = await Link(self, "agent", name).open()
        await link.send({"type": "sessions", "sessions": sessions(name)})
        self.links.append(link)
        await asyncio.sleep(0.15)
        return link

    async def node(self, name, loaded):
        link = await Link(self, "speech", name).open()
        await link.send({"type": "models", "loaded": loaded})
        self.links.append(link)
        await asyncio.sleep(0.1)
        return link

    async def gone(self, link):
        self.links.remove(link)
        await link.close()
        await asyncio.sleep(0.2)

    async def live_call(self, session):
        """A call to a session, as from the app's Sessions tab: it rings no phone, so nothing
        pushes, and it's live once the stand-in agent accepts it."""
        phone_check.VOICE = f"127.0.0.1:{self.port}"
        caller = phone_check.Phone()
        call_id = str(uuid.uuid4()).upper()  # as the app makes it
        status = await caller.offer(call_id, ringing=False, contact=session)
        if status != 200:
            raise RuntimeError(f"the live call's offer: {status}")
        self.callers.append(caller)
        await self.post("/events", {"call_id": call_id, "event": "audio connected", "at": iso(), "note": "from Sessions"})
        await asyncio.sleep(0.3)

    async def place(self, agent, project, subject, question=False):
        await agent.send({"type": "place", "id": secrets.randbelow(10**6), "call_id": secrets.token_hex(16),
                          "project": project, "subject": subject, "session_id": "s-" + project, "question": question})
        await asyncio.sleep(0.3)

    def waiting(self, calls):
        """Calls waiting for want of speech, written before the hub starts, as if they'd been
        placed and their phone told already (so nothing pushes)."""
        kept = {}
        for project, subject in calls:
            kept[secrets.token_hex(16)] = {"project": project, "subject": subject, "voice": self.base, "reason": "no speech",
                                           "refreshed": iso(), "notified": iso(timedelta(minutes=-3))}
        (self.state / "waiting.json").write_text(json.dumps(kept, indent=2))

    async def events(self):
        """Calls the phone reported: today, yesterday and earlier in the week."""
        now = datetime.now(timezone.utc)

        def at(days, hour, minute, second=0):
            t = (now - timedelta(days=days)).astimezone().replace(hour=hour, minute=minute, second=second)
            return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

        a, b, c, d, e = (secrets.token_hex(16) for _ in range(5))
        for event in [
            {"call_id": e, "event": "ringing", "at": at(4, 16, 2)}, {"call_id": e, "event": "declined", "at": at(4, 16, 2, 20)},
            {"call_id": a, "event": "ringing", "at": at(1, 16, 2)}, {"call_id": a, "event": "answered", "at": at(1, 16, 2, 6)},
            {"call_id": a, "event": "audio connected", "at": at(1, 16, 2, 6), "since_answer_s": "0.00",
             "note": "offer while ringing, connected 2.8 s after ringing"},
            {"call_id": a, "event": "ended", "at": at(1, 16, 4, 41), "duration_s": "155.2"},
            {"call_id": b, "event": "ringing", "at": at(0, 8, 40)}, {"call_id": b, "event": "missed", "at": at(0, 8, 40, 45)},
            {"call_id": c, "event": "ringing", "at": at(0, 10, 12)}, {"call_id": c, "event": "answered", "at": at(0, 10, 12, 3)},
            {"call_id": c, "event": "audio failed", "at": at(0, 10, 12, 18), "error": "no audio within 15 s"},
            {"call_id": c, "event": "ended", "at": at(0, 10, 12, 20), "duration_s": "17.0"},
            {"call_id": d, "event": "ringing", "at": at(0, 12, 58)}, {"call_id": d, "event": "answered", "at": at(0, 12, 58, 4)},
            {"call_id": d, "event": "ended", "at": at(0, 13, 1, 16), "duration_s": "192.4"},
        ]:
            await self.post("/events", event)


# The states, each a function that sets up a hub, and the words its status page must show.

async def working(h):
    await h.phone("f00d" * 16, "iPhone", False)
    await h.agent("studio")
    await h.agent("laptop")
    await h.node("gpu-tower", True)
    await h.live_call(sessions("studio")[0]["session_id"])
    await h.events()


async def no_node_voice_off(h):
    await h.phone("f00d" * 16, "iPhone", False)
    await h.agent("studio")


async def no_node_voice_on(h):
    await h.phone("f00d" * 16, "iPhone", True)
    await h.agent("studio")


async def node_offline(h):
    await h.phone("f00d" * 16, "iPhone", False)
    await h.agent("studio")
    await h.gone(await h.node("gpu-tower", False))


async def mac_asleep(h):
    await h.phone("f00d" * 16, "iPhone", False)
    await h.agent("studio")
    await h.gone(await h.agent("laptop"))
    await h.node("gpu-tower", False)


async def push_failing(h):
    await h.phone("f00d" * 16, "iPhone", False)
    studio = await h.agent("studio")
    await h.node("gpu-tower", False)
    await h.place(studio, "parser", "Which branch should the fix go to?")


async def on_hold(h):
    await h.phone("f00d" * 16, "iPhone", True)
    await h.agent("studio")
    await h.node("gpu-tower", False)


async def on_hold_calls(h, studio):
    await h.place(studio, "docs-site", "Deploy the rebuilt docs now?")
    await h.place(studio, "parser", "Which branch should the fix go to?")


async def no_phone(h):
    await h.agent("studio")
    await h.node("gpu-tower", False)


async def fresh(h):
    pass


async def busy(h):
    await h.phone("f00d" * 16, "iPhone", False)
    await h.phone("beef" * 16, "iPad", True)
    await h.agent("studio")
    await h.agent("a-mac-with-a-rather-long-name-indeed")
    await h.agent("build-box")
    await h.gone(await h.agent("laptop"))
    await h.node("gpu-tower", True)
    await h.node("spare-box", False)
    await h.live_call(sessions("a-mac-with-a-rather-long-name-indeed")[0]["session_id"])


# name: (set up, APNs key, calls waiting for speech, words the status page must show)
STATES = {
    "working": (working, "good", [], ["Calls ring iPhone, and gpu-tower hears and speaks them", "On a call", "Speaks calls", "Taking calls"]),
    "no-node-voice-off": (no_node_voice_off, "good", [("infra", "Backups moved to the new bucket")],
                          ["Calls wait instead of ringing: there's no speech node", "Not set up", "None linked, so calls wait"]),
    "no-node-voice-on": (no_node_voice_on, "good", [],
                         ["Calls ring iPhone, which hears and speaks them itself", "On-device voice on", "Speaks calls"]),
    "node-offline": (node_offline, "good", [], ["gpu-tower is offline", "Offline", "Forget"]),
    "mac-asleep": (mac_asleep, "good", [], ["Asleep", "laptop", "Calls ring iPhone"]),
    "push-failing": (push_failing, "broken", [], ["the push to Apple fails", "Key can't be read", "can't read its sandbox push key"]),
    "on-hold": (on_hold, "good", [], ["On hold", "Once you take calls", "not with the phone's own voice", "Take calls"]),
    "no-phone": (no_phone, None, [], ["No phone yet", "Not set up"]),
    "fresh": (fresh, None, [], ["No phone yet", "Not set up"]),
    "busy": (busy, "good", [], ["and 1 more", "spare-box", "iPad", "a-project-with-a-rather-long-name-indeed"]),
}


class CDP:
    """Headless Chrome, through its DevTools protocol."""

    def __init__(self, ws, out):
        self.ws, self.n, self.out = ws, 0, out

    async def call(self, method, **params):
        self.n += 1
        await self.ws.send_json({"id": self.n, "method": method, "params": params})
        while True:
            m = await self.ws.receive_json(timeout=30)
            if m.get("id") == self.n:
                if "error" in m:
                    raise RuntimeError(f"{method}: {m['error']}")
                return m.get("result", {})

    async def go(self, url):
        await self.call("Page.navigate", url=url)
        await asyncio.sleep(0.9)

    async def js(self, expression):
        r = await self.call("Runtime.evaluate", expression=expression, awaitPromise=True, returnByValue=True)
        return r.get("result", {}).get("value")

    async def submit(self, fields: dict, form="form"):
        sets = "".join(f"document.getElementById({json.dumps(k)}).value = {json.dumps(v)};" for k, v in fields.items())
        await self.js(sets + f"document.querySelector({json.dumps(form)}).submit()")
        await asyncio.sleep(1.0)

    async def size(self, width, height, dark):
        await self.call("Emulation.setDeviceMetricsOverride", width=width, height=height, deviceScaleFactor=2, mobile=width < 500)
        await self.call("Emulation.setEmulatedMedia", features=[{"name": "prefers-color-scheme", "value": "dark" if dark else "light"}])

    async def shot(self, name, full=True):
        """A screenshot: the whole page, or what the window shows. A whole page's capture
        would draw a phone's dock where the window's bottom was when it began, over the page,
        so it leaves the dock out."""
        await asyncio.sleep(0.35)
        params = {"format": "png"}
        dock = "(() => { const d = document.querySelector('.dock'); if (d && innerWidth <= 640) d.style.visibility = '%s'; })()"
        if full:
            await self.js(dock % "hidden")
            content = (await self.call("Page.getLayoutMetrics"))["cssContentSize"]
            params |= {"captureBeyondViewport": True,
                       "clip": {"x": 0, "y": 0, "width": content["width"], "height": content["height"], "scale": 1}}
        data = await self.call("Page.captureScreenshot", **params)
        if full:
            await self.js(dock % "")
        (self.out / f"{name}.png").write_bytes(base64.b64decode(data["data"]))

    async def sign_in(self, hub):
        await self.go(hub.base + "/portal/login")
        await self.submit({"name": USER, "password": PASSWORD})
        await self.submit({"code": hub.code()})
        return await self.js("location.pathname") == "/portal/"

    async def overflows(self):
        """Whether anything is wider than the window: a page that scrolls sideways."""
        return await self.js("document.documentElement.scrollWidth > window.innerWidth + 1")


def check(name, ok, detail=""):
    print(f"{'ok  ' if ok else 'FAIL'} {name}{': ' + detail if detail else ''}", flush=True)
    return ok


async def run_state(cdp, out, name, port):
    setup, push, waiting, words = STATES[name]
    hub = Hub(out, name, port, push)
    hub.waiting(waiting)
    try:
        if name == "fresh":
            await hub.start()
            for dark, (size, (w, h)) in ((False, ("phone", SIZES["phone"])), (True, ("desktop", SIZES["desktop"]))):
                await cdp.size(w, h, dark)
                await cdp.go(hub.base + "/portal/")
                await cdp.shot(f"noaccount-{size}-{'dark' if dark else 'light'}")
            await hub.stop()
            hub.make_account()
            await hub.start()
            # Signing in, on a phone, the way you would: a wrong password first.
            await cdp.size(*SIZES["phone"], False)
            await cdp.go(hub.base + "/portal/")
            await cdp.shot("login-phone-light", full=False)
            await cdp.submit({"name": USER, "password": "not it"})
            await cdp.shot("login-wrong-phone-light", full=False)
            await cdp.size(*SIZES["desktop"], True)
            await cdp.go(hub.base + "/portal/login")
            await cdp.shot("login-desktop-dark", full=False)
            await cdp.submit({"name": USER, "password": PASSWORD})
            await cdp.size(*SIZES["phone"], True)
            await cdp.shot("code-phone-dark", full=False)
            await cdp.submit({"code": hub.code()})
            signed = await cdp.js("location.pathname") == "/portal/"
        else:
            hub.make_account()
            await hub.start()
            await setup(hub)
            signed = await cdp.sign_in(hub)
        check(f"{name}: signed in", signed)
        if name == "on-hold":
            # Held through the status page's own button, as you would.
            await cdp.go(hub.base + "/portal/")
            await cdp.js("document.querySelector('#hold form').submit()")
            await asyncio.sleep(1.0)
            studio = hub.links[0]
            await on_hold_calls(hub, studio)
            _, answer = await hub.post("/hold", {"token": "f00d" * 16})
            check("on-hold: the app's hold switch is on", answer.get("hold") is True, json.dumps(answer))

        for dark in (False, True):
            for size, (w, h) in SIZES.items():
                await cdp.size(w, h, dark)
                await cdp.go(hub.base + "/portal/")
                text = await cdp.js("document.body.innerText + ' ' + [...document.querySelectorAll('svg text')].map(t => t.textContent).join(' ')")
                missing = [word for word in words if word not in (text or "")]
                tone = "dark" if dark else "light"
                if not dark:
                    check(f"{name}: status, {size}", not missing, "missing " + ", ".join(missing) if missing else "")
                if await cdp.overflows():
                    check(f"{name}: status, {size}, {tone}, no sideways scroll", False)
                await cdp.shot(f"status-{name}-{size}-{tone}")
                if size == "phone":
                    # The first screen as the phone shows it, with the dock.
                    await cdp.shot(f"status-{name}-{size}-{tone}-screen", full=False)
                    if not dark:
                        docked = await cdp.js("(() => { const r = document.querySelector('.dock').getBoundingClientRect();"
                                              " return r.bottom <= innerHeight && r.bottom > innerHeight - 40; })()")
                        check(f"{name}: the dock at the bottom of a phone's screen", docked)

        if name in ("working", "on-hold"):
            await cdp.size(820, 1180, False)
            await cdp.go(hub.base + "/portal/")
            await cdp.shot(f"status-{name}-tablet-light")
            for dark in (False, True):
                for size, (w, h) in SIZES.items():
                    tone = "dark" if dark else "light"
                    await cdp.size(w, h, dark)
                    for page in ("calls", "log", "setup"):
                        if name == "on-hold" and page != "calls":
                            continue
                        await cdp.go(f"{hub.base}/portal/{page}")
                        if page == "calls":
                            await cdp.js("document.querySelectorAll('details.call')[1]?.setAttribute('open', '')")
                        if page == "setup":
                            await cdp.js("document.querySelector('[data-reveal]').click()")
                        if await cdp.overflows():
                            check(f"{name}: {page}, {size}, {tone}, no sideways scroll", False)
                        await cdp.shot(f"{page}-{name}-{size}-{tone}", full=page != "log")
        if name == "working":
            # The refresh keeps the page up to date: a node that goes shows as offline.
            await cdp.size(*SIZES["desktop"], False)
            await cdp.go(hub.base + "/portal/")
            await hub.gone([link for link in hub.links if link.kind == "speech"][0])
            await asyncio.sleep(6)
            refreshed = await cdp.js("[...document.querySelectorAll('svg.wide text')].some(t => t.textContent === 'Offline')")
            check("working: the page refreshed itself, the node offline", refreshed)
            await cdp.shot("status-working-refreshed-desktop-light")
            # The setup page hides the tokens until shown.
            await cdp.go(hub.base + "/portal/setup")
            check("working: setup hides the tokens", await cdp.js(f"!document.body.innerText.includes({json.dumps(hub.tokens['speech'])})"))
    finally:
        await hub.stop()


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("out", nargs="?", help="where the screenshots go (default: a new temporary folder)")
    parser.add_argument("--only", help="comma-separated states, of: " + ", ".join(STATES))
    args = parser.parse_args()
    out = Path(args.out) if args.out else Path(tempfile.mkdtemp(prefix="portal-screens-"))
    out.mkdir(parents=True, exist_ok=True)
    names = args.only.split(",") if args.only else list(STATES)
    subprocess.run(["go", "build", "-o", str(BINARY), "."], cwd=REPO / "hub", check=True)
    t0 = time.monotonic()
    profile = out / "chrome"
    chrome = subprocess.Popen([CHROME, "--headless=new", "--remote-debugging-port=9341", f"--user-data-dir={profile}",
                               "--no-first-run", "--hide-scrollbars", "about:blank"],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        async with aiohttp.ClientSession() as http:
            for _ in range(100):
                try:
                    async with http.get("http://127.0.0.1:9341/json/list") as r:
                        page = next(t for t in await r.json() if t["type"] == "page")
                        break
                except Exception:
                    await asyncio.sleep(0.1)
            async with http.ws_connect(page["webSocketDebuggerUrl"], max_msg_size=0) as ws:
                cdp = CDP(ws, out)
                await cdp.call("Page.enable")
                await cdp.call("Runtime.enable")
                for i, name in enumerate(names):
                    await run_state(cdp, out, name, 18860 + i)
    finally:
        chrome.terminate()
        shutil.rmtree(profile, ignore_errors=True)
    print(f"{len(list(out.glob('*.png')))} screenshots in {out}, {time.monotonic() - t0:.0f} s")


asyncio.run(main())
