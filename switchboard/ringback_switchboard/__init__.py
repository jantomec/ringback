"""The ringback switchboard: knows the phone, places calls through APNs, hears what happened."""

import tomllib
from pathlib import Path

CONFIG = Path("~/.config/ringback/switchboard.toml").expanduser()
STATE = Path("~/.local/state/ringback").expanduser()


def load_config() -> dict:
    if not CONFIG.exists():
        raise SystemExit(f"No config at {CONFIG}; copy switchboard/switchboard.example.toml there.")
    return tomllib.loads(CONFIG.read_text())
