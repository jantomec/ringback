"""VoIP pushes through APNs, with token-based (JWT) authentication."""

import json
import time
from pathlib import Path

import httpx
import jwt

HOSTS = {
    "sandbox": "https://api.sandbox.push.apple.com",
    "production": "https://api.push.apple.com",
}


class APNs:
    def __init__(self, team_id: str, key_id: str, key_file: str, bundle_id: str):
        self.team_id, self.key_id = team_id, key_id
        self.topic = f"{bundle_id}.voip"
        self.key = Path(key_file).expanduser().read_text()
        self.client = httpx.Client(http2=True, timeout=10)
        self._jwt, self._issued = "", 0.0

    def _token(self) -> str:
        # Apple wants a token refreshed at most every 20 and at least every 60 minutes.
        if time.time() - self._issued > 50 * 60:
            self._issued = time.time()
            self._jwt = jwt.encode(
                {"iss": self.team_id, "iat": int(self._issued)},
                self.key,
                algorithm="ES256",
                headers={"kid": self.key_id},
            )
        return self._jwt

    def push(self, device_token: str, environment: str, payload: dict) -> tuple[int, str]:
        """Sends one VoIP push; returns APNs' status and its reason if it refused."""
        r = self.client.post(
            f"{HOSTS[environment]}/3/device/{device_token}",
            headers={
                "authorization": f"bearer {self._token()}",
                "apns-topic": self.topic,
                "apns-push-type": "voip",
                "apns-priority": "10",
                "apns-expiration": "0",
            },
            content=json.dumps(payload),
        )
        reason = r.json().get("reason", "") if r.content else ""
        return r.status_code, reason
