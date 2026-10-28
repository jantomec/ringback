"""HTTP endpoints the phone talks to over Nebula."""

import json
from datetime import datetime

from aiohttp import web

from . import STATE, devices

EVENTS = STATE / "events.jsonl"


def log(line: str) -> None:
    print(f"{datetime.now():%H:%M:%S}  {line}", flush=True)


async def register(request: web.Request) -> web.Response:
    body = await request.json()
    devices.register(body["token"], body.get("environment", "sandbox"), body.get("device", ""))
    log(f"registered {body.get('device', 'a phone')} ({body.get('environment')}), token …{body['token'][-8:]}")
    return web.json_response({"ok": True})


async def event(request: web.Request) -> web.Response:
    body = await request.json()
    record = {"time": datetime.now().isoformat(timespec="seconds"), **body}
    STATE.mkdir(parents=True, exist_ok=True)
    with EVENTS.open("a") as f:
        f.write(json.dumps(record) + "\n")
    extra = "".join(f", {k} {body[k]}" for k in ("on_another_call", "duration_s", "since_answer_s", "error", "note") if k in body)
    at = f" (on the phone at {datetime.fromisoformat(body['at']).astimezone():%H:%M:%S})" if "at" in body else ""
    log(f"call {body.get('call_id', '?')[:8]}: {body.get('event')}{extra}{at}")
    return web.json_response({"ok": True})


def serve(listen: str) -> None:
    host, port = listen.rsplit(":", 1)
    app = web.Application()
    app.add_routes([web.post("/register", register), web.post("/events", event)])
    log(f"switchboard listening on {listen}")
    web.run_app(app, host=host, port=int(port), print=None)
