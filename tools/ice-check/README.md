# ICE check

Checks how long a call's connection lasts on a slow network, with aioice's consent checks as
they come and as the voice server sets them up (`patient_consent` in
`voice/ringback_voice/__main__.py`). During a call, aioice checks every 4–6 s that the other
side still wants it (consent, RFC 7675); as it comes, it sends each check once, gives up on it
after 0.5 s, and ends the connection after 6 unanswered in a row. On the phone with Very Bad
Network, every call then ended about 30 s after it connected (`docs/design.md`, "Data and
weak signal").

Two aioice connections on this Mac's loopback stand in for the Mac and the phone. Once
they've connected, each one's incoming packets are held back for half the round trip, and
some are dropped at random. Each case runs 3 times with each consent check, all at once:
- fast, none lost: lasts either way;
- Very Bad Network: 1.1 s round trip, 10% lost each way (as on the phone, with the
  conditioner on the cellular interface only);
- Very Bad Network applied twice: 2.1 s round trip, 19% lost each way (as on the phone with
  the conditioner as it comes, which slows Nebula's packets twice);
- phone gone: everything lost. The connection should end, within 45 s.

Run it from the repo; it takes about a minute:

    uv run --project voice tools/ice-check/ice_check.py [--hold 60] [--runs 3]

It prints a line per case and check, and exits 1 if `patient_consent` doesn't do what's
expected (lasts the hold on a slow network, ends when the phone is gone).

Results on 2026-10-30 (aioice 0.10.2, 60 s hold, two runs of the whole check): with the
check as it comes, both slow cases ended after 30–36 s; with `patient_consent`, they lasted
60 s. The phone gone ended after 32–36 s with either. On the phone, the same call that
ended 31 s after connecting lasted 91 s, until it was hung up.
