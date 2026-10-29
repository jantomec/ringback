# Nebula check

Checks whether a Nebula peer, such as the phone, stays reachable from the Mac. It pings the
peer every INTERVAL seconds for MINUTES minutes and prints one line per probe, then a
summary: replies, round-trip times and every outage. An outage lasts from the last reply
before it to the first reply after it.

Mobile Nebula drops all inbound traffic by default, so pinging the phone needs an inbound
ICMP rule for the Mac. On macOS the check keeps the Mac awake while it runs, because a
sleeping Mac would look like a lost phone.

Run from the repo root; it needs only Python 3:

    tools/nebula-check/nebula_check.py <phone-nebula-ip>                           # every 30 s for 20 min
    tools/nebula-check/nebula_check.py <phone-nebula-ip> --interval 1 --minutes 1  # every second for a minute

Stop early with Ctrl-C; the summary is still printed.

Results are under Stage 0 in `docs/design.md`: once the phone had sent to the Mac, the Mac
reached it at about 60 ms round trip, until the tunnel stopped answering within 30 s of
locking the phone.
