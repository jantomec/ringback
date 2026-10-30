"""How long a call's connection lasts on a slow network, with aioice's consent checks as they
come and as the voice server sets them up (`patient_consent` in voice/ringback_voice/__main__.py).

Two aioice connections on this Mac's loopback stand in for the Mac and the phone. Once they've
connected, each one's incoming packets are held back and some are dropped, and the check
waits to see whether the Mac's side ends the connection. See README.md.
"""

import argparse
import asyncio
import random
import sys
import time
import types

import aioice.ice
from aioice import Connection

from ringback_voice.__main__ import patient_consent

aioice.ice.get_host_addresses = lambda use_ipv4, use_ipv6: ["127.0.0.1"]

ORIGINAL = aioice.ice.Connection.query_consent
patient_consent()
PATIENT = aioice.ice.Connection.query_consent
aioice.ice.Connection.query_consent = ORIGINAL


async def connected(consent) -> tuple[Connection, Connection]:
    """The Mac's side and the phone's, connected, both checking consent with consent."""
    mac, phone = Connection(ice_controlling=False), Connection(ice_controlling=True)
    for side in (mac, phone):
        side.query_consent = types.MethodType(consent, side)
        await side.gather_candidates()
    for side, other in ((mac, phone), (phone, mac)):
        for candidate in other.local_candidates:
            await side.add_remote_candidate(candidate)
        await side.add_remote_candidate(None)
        side.remote_username, side.remote_password = other.local_username, other.local_password
    await asyncio.gather(mac.connect(), phone.connect())
    return mac, phone


def slow(side: Connection, delay: float, loss: float, dice: random.Random) -> None:
    """Holds each packet side receives back for delay seconds, and drops loss of them."""
    loop = asyncio.get_running_loop()
    for protocol in side._protocols:
        received = protocol.datagram_received

        def deliver(data, addr, received=received):
            if not side._closed:
                received(data, addr)

        def late(data, addr, deliver=deliver):
            if dice.random() >= loss:
                loop.call_later(delay, deliver, data, addr)

        protocol.datagram_received = late


async def case(name: str, consent, round_trip: float, loss: float, hold: float, seed: int) -> dict:
    """How long the Mac's side kept the connection, up to hold seconds after it connected."""
    mac, phone = await connected(consent)
    dice = random.Random(seed)
    for side in (mac, phone):
        slow(side, round_trip / 2, loss, dice)
    start = time.monotonic()
    while not mac._closed and time.monotonic() - start < hold:
        await asyncio.sleep(0.1)
    lasted = time.monotonic() - start
    ended = mac._closed
    for side in (mac, phone):
        await side.close()
    return {"name": name, "ended": ended, "lasted": lasted}


async def main() -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--hold", type=float, default=60, help="seconds a connection should last (default 60)")
    p.add_argument("--runs", type=int, default=3, help="runs of each case (default 3)")
    args = p.parse_args()
    # Each: the round trip in seconds, the share of packets lost each way, and whether the
    # connection should last the hold (or else end: the phone is gone).
    cases = {
        "fast, none lost": (0.0, 0.0, True),
        "Very Bad Network: 1.1 s round trip, 10% lost each way": (1.1, 0.10, True),
        "Very Bad Network applied twice: 2.1 s round trip, 19% lost each way": (2.1, 0.19, True),
        "phone gone: everything lost": (0.0, 1.0, False),
    }
    jobs = [
        case(f"{name}, {label}", consent, trip, loss, args.hold, seed=run)
        for name, (trip, loss, _) in cases.items()
        for label, consent in (("aioice as it comes", ORIGINAL), ("patient_consent", PATIENT))
        for run in range(args.runs)
    ]
    print(f"{len(jobs)} connections, holding each up to {args.hold:g} s ...", flush=True)
    results = await asyncio.gather(*jobs)
    await asyncio.sleep(max(trip for trip, _, _ in cases.values()))  # packets still held back
    failed = 0
    for name, (_, _, lasts) in cases.items():
        for label in ("aioice as it comes", "patient_consent"):
            mine = [r for r in results if r["name"] == f"{name}, {label}"]
            ended = [r["lasted"] for r in mine if r["ended"]]
            line = f"{name}, {label}: " + (
                f"ended in {', '.join(f'{s:.0f}' for s in ended)} s" if ended else "lasted"
            ) + (f" ({len(mine) - len(ended)} of {len(mine)} lasted {args.hold:g} s)" if ended and len(ended) < len(mine) else "")
            if label == "patient_consent":
                ok = (not ended) if lasts else (len(ended) == len(mine) and max(ended) <= 45)
                failed += not ok
                line += "" if ok else "   FAIL"
            print(line)
    print("all as expected" if not failed else f"{failed} FAIL")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
