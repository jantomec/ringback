#!/usr/bin/env python3
"""Stage 0 check: does a Nebula peer (the phone) stay reachable?

Pings HOST every INTERVAL seconds for MINUTES minutes and prints one line per
probe, then a summary: replies, round-trip times and every outage. An outage's
length is measured from the last reply before it to the first reply after it.

    tools/nebula_check.py <phone-nebula-ip>                          # every 30 s for 20 min
    tools/nebula_check.py <phone-nebula-ip> --interval 1 --minutes 1 # every second for a minute

On macOS it keeps the Mac awake while it runs; a sleeping Mac would look like
a lost phone. Stop early with Ctrl-C and the summary is still printed.
"""

import argparse
import os
import re
import signal
import statistics
import subprocess
import sys
import time
from datetime import datetime


def probe(host, timeout):
    """One ping; returns the round-trip time in ms, or None if lost."""
    flag = "-t" if sys.platform == "darwin" else "-W"
    r = subprocess.run(["ping", "-c", "1", flag, str(timeout), host], capture_output=True, text=True)
    m = re.search(r"time=([\d.]+) ms", r.stdout)
    return float(m.group(1)) if r.returncode == 0 and m else None


def outages(results):
    """Runs of lost probes as (first_lost, lost_count, last_ok, next_ok); either ok may be None."""
    runs, last_ok, first_lost, lost = [], None, None, 0
    for t, rtt in results:
        if rtt is None:
            if lost == 0:
                first_lost = t
            lost += 1
        else:
            if lost:
                runs.append((first_lost, lost, last_ok, t))
                lost = 0
            last_ok = t
    if lost:
        runs.append((first_lost, lost, last_ok, None))
    return runs


def clock(t):
    return datetime.fromtimestamp(t).strftime("%H:%M:%S")


def summary(results):
    if not results:
        return "no probes sent"
    rtts = [rtt for _, rtt in results if rtt is not None]
    lines = [f"replies: {len(rtts)}/{len(results)} ({100 * (1 - len(rtts) / len(results)):.1f}% lost)"]
    if rtts:
        lines.append(f"rtt ms: min {min(rtts):.0f}, median {statistics.median(rtts):.0f}, max {max(rtts):.0f}")
    runs = outages(results)
    lines.append(f"outages: {len(runs)}")
    for first_lost, lost, last_ok, next_ok in runs:
        if last_ok is None:
            gap = "from the start"
        elif next_ok is None:
            gap = f"still down at the end, {results[-1][0] - last_ok:.1f}s since the last reply"
        else:
            gap = f"{next_ok - last_ok:.1f}s between replies"
        lines.append(f"  {clock(first_lost)}  {lost} lost, {gap}")
    return "\n".join(lines)


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("host", help="the peer's Nebula IP")
    p.add_argument("--interval", type=float, default=30, help="seconds between probes (default 30)")
    p.add_argument("--minutes", type=float, default=20, help="how long to run (default 20)")
    args = p.parse_args()

    if sys.platform == "darwin":
        subprocess.Popen(["caffeinate", "-i", "-w", str(os.getpid())])
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))

    timeout = max(1, min(10, int(args.interval) - 1))
    results, start = [], time.time()
    print(f"pinging {args.host} every {args.interval:g}s for {args.minutes:g} min, timeout {timeout}s", flush=True)
    try:
        next_t = start
        while time.time() < start + args.minutes * 60:
            t = time.time()
            rtt = probe(args.host, timeout)
            results.append((t, rtt))
            print(f"{clock(t)}  {'ok' if rtt is not None else 'LOST'}  {'' if rtt is None else f'{rtt:.0f} ms'}", flush=True)
            next_t += args.interval
            time.sleep(max(0, next_t - time.time()))
    except KeyboardInterrupt:
        pass
    finally:
        print("\n" + summary(results), flush=True)


if __name__ == "__main__":
    main()
