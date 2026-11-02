"""Frames the app's screenshots as phones, three in a row, for the README: one image for light
mode and one for dark, on a transparent background. The screenshots come from
tools/app-screens (run.sh OUT_DIR); needs rsvg-convert (brew install librsvg).

    python3 docs/images/src/screens.py OUT_DIR
"""

import subprocess
import sys
from pathlib import Path

SHOTS = ["recents", "sessions", "recents-swipe"]  # on hold and breathing; the cards; the Claude App swipe
W, H = 1206, 2622  # the screenshots' pixels (iPhone 17 Pro)
SCALE = 0.39  # three phones in GitHub's 838-pixel column, at twice its resolution
BEZEL, GAP, MARGIN = 14, 70, 30


def frame(shots: Path, look: str) -> str:
    sw, sh = W * SCALE, H * SCALE
    pw, ph = sw + 2 * BEZEL, sh + 2 * BEZEL
    width, height = 3 * pw + 2 * GAP + 2 * MARGIN, ph + 2 * MARGIN
    bezel = "#1f2126" if look == "light" else "#3a3d44"
    phones = []
    for i, name in enumerate(SHOTS):
        x, y = MARGIN + i * (pw + GAP), MARGIN
        phones.append(f'''
  <g filter="url(#shadow)"><rect x="{x}" y="{y}" width="{pw}" height="{ph}" rx="{64 + BEZEL}" fill="{bezel}"/></g>
  <clipPath id="c{i}"><rect x="{x + BEZEL}" y="{y + BEZEL}" width="{sw}" height="{sh}" rx="64"/></clipPath>
  <image href="{look}-{name}.png" x="{x + BEZEL}" y="{y + BEZEL}" width="{sw}" height="{sh}" clip-path="url(#c{i})"/>''')
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {width:.0f} {height:.0f}" width="{width:.0f}" height="{height:.0f}">
  <defs><filter id="shadow" x="-10%" y="-10%" width="120%" height="120%"><feDropShadow dx="0" dy="10" stdDeviation="12" flood-color="#000" flood-opacity=".25"/></filter></defs>{"".join(phones)}
</svg>'''


def main() -> None:
    shots = Path(sys.argv[1])
    out = Path(__file__).resolve().parent.parent
    for look in ("light", "dark"):
        # Next to the screenshots: librsvg loads images only from the SVG's own folder.
        svg = shots / f"screens-{look}.svg"
        svg.write_text(frame(shots, look))
        subprocess.run(["rsvg-convert", str(svg), "-o", str(out / f"screens-{look}.png")], check=True)
        svg.unlink()


if __name__ == "__main__":
    main()
