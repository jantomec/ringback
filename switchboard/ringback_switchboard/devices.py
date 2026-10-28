"""Phones that registered for calls, keyed by VoIP push token."""

import json
from datetime import datetime, timezone

from . import STATE

FILE = STATE / "devices.json"


def load() -> dict[str, dict]:
    return json.loads(FILE.read_text()) if FILE.exists() else {}


def register(token: str, environment: str, device: str) -> None:
    devices = load()
    devices[token] = {
        "environment": environment,
        "device": device,
        "registered": datetime.now(timezone.utc).isoformat(timespec="seconds"),
    }
    STATE.mkdir(parents=True, exist_ok=True)
    FILE.write_text(json.dumps(devices, indent=1))


def forget(token: str) -> None:
    devices = load()
    if devices.pop(token, None) is not None:
        FILE.write_text(json.dumps(devices, indent=1))
