"""Measures the hold's breathing light in a simulator: whether the app's faint wash beats with the
pill's glow, at the same period and phase, and how much of the glow's swing reaches it.

Run tools/app-screens/run.sh first, which installs the screenshot build in a booted simulator.
This launches it on hold with sample calls, in dark mode (where the light shows most), records
the screen, and reads the blue channel of three regions in every frame: just above the pill,
mid-screen between rows, and the top-left corner, far from the pill. Needs the voice project's
Python (PyAV and NumPy):

    uv run --project tools tools/app-screens/breath_check.py [SECONDS] [--keep VIDEO]
"""

import argparse
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import av
import numpy as np

APP = "dev.ringback.screens"
# In points, on an iPhone 17 Pro (402 x 874): the pill's centre is 312, 808.
REGIONS = {"above the pill": (255, 760, 370, 768), "mid-screen": (150, 600, 250, 620), "far corner": (10, 150, 120, 200)}
FPS = 30


def record(seconds: float, video: Path) -> None:
    sim = ["xcrun", "simctl"]
    subprocess.run([*sim, "ui", "booted", "appearance", "dark"], check=True)
    subprocess.run([*sim, "terminate", "booted", APP], capture_output=True)
    subprocess.run([*sim, "launch", "booted", APP, "-RingbackSeed", "calls"], check=True, capture_output=True)
    time.sleep(4)
    rec = subprocess.Popen([*sim, "io", "booted", "recordVideo", "--codec=h264", "--force", str(video)],
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)  # fmt: skip
    time.sleep(seconds)
    rec.send_signal(2)
    rec.wait()
    subprocess.run([*sim, "ui", "booted", "appearance", "light"], check=True)


def blue(video: Path) -> tuple[np.ndarray, dict[str, np.ndarray]]:
    """Frame times, and each region's mean blue per frame, resampled evenly at FPS."""
    container = av.open(str(video))
    stream = container.streams.video[0]
    times, values = [], {name: [] for name in REGIONS}
    for frame in container.decode(stream):
        a = frame.to_ndarray(format="rgb24").astype(float)
        sx, sy = a.shape[1] / 402, a.shape[0] / 874
        for name, (x0, y0, x1, y1) in REGIONS.items():
            values[name].append(a[int(y0 * sy) : int(y1 * sy), int(x0 * sx) : int(x1 * sx), 2].mean())
        times.append(float(frame.pts * stream.time_base))
    t = np.array(times) - times[0]
    grid = np.arange(0, t[-1], 1 / FPS)
    return grid, {name: np.interp(grid, t, v) for name, v in values.items()}


def period(v: np.ndarray) -> float:
    """The strongest repeat between 2 and 8 s, by autocorrelation."""
    v = v - v.mean()
    ac = np.correlate(v, v, "full")[len(v) - 1 :]
    return (2 * FPS + int(np.argmax(ac[2 * FPS : 8 * FPS]))) / FPS


def lag(a: np.ndarray, b: np.ndarray) -> tuple[float, float]:
    """How far b lags a, within 1.5 s, and their correlation there."""
    a, b = a - a.mean(), b - b.mean()

    def overlap(k):
        return a[max(0, k) : len(a) + min(0, k)], b[max(0, -k) : len(b) - max(0, k)]

    best = max(range(-45, 46), key=lambda k: float(np.dot(*overlap(k))))
    return best / FPS, float(np.corrcoef(*overlap(best))[0, 1])


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("seconds", nargs="?", type=float, default=12, help="how long to record (default 12)")
    p.add_argument("--keep", type=Path, help="keep the recording here")
    args = p.parse_args()
    video = args.keep or Path(tempfile.mkdtemp(prefix="ringback-breath-")) / "breath.mp4"
    record(args.seconds, video)
    grid, values = blue(video)
    glow = values["above the pill"]
    print(f"{len(grid)} samples over {grid[-1]:.1f} s")
    for name, v in values.items():
        line = f"{name:15s} blue {v.min():5.1f}–{v.max():5.1f}, period {period(v):.2f} s"
        if v is not glow:
            delay, r = lag(glow, v)
            line += f", {delay:+.2f} s behind the pill (correlation {r:.2f}), {np.ptp(v) / np.ptp(glow):.0%} of its swing"
        print(line)
    return 0


if __name__ == "__main__":
    sys.exit(main())
