"""Builds the README's hero (docs/images/hero.svg) from hero.svg here: the scene as a PNG, so
its text looks the same everywhere, with the three arcs over it pulsing outward like a phone
ringing (still with Reduce Motion). GitHub shows CSS animation in an SVG shown as an image, and
a PNG embedded in it, but no fonts but the reader's own. Needs rsvg-convert.

    python3 docs/images/src/hero.py
"""

import base64
import subprocess
import tempfile
from pathlib import Path

ARCS = ["M912 362a54 54 0 0 1 0 96", "M950 330a96 96 0 0 1 0 160", "M990 298a138 138 0 0 1 0 224"]


def main() -> None:
    here = Path(__file__).resolve().parent
    src = (here / "hero.svg").read_text()
    start, end = src.index("  <!-- the ring travelling across"), src.index("  <!-- the phone, ringing -->")
    with tempfile.TemporaryDirectory() as tmp:
        scene = Path(tmp) / "scene.svg"
        scene.write_text(src[:start] + src[end:])
        subprocess.run(["rsvg-convert", str(scene), "-o", str(Path(tmp) / "scene.png")], check=True)
        png = base64.b64encode((Path(tmp) / "scene.png").read_bytes()).decode()
    arcs = "\n".join(f'  <path class="arc a{i + 1}" d="{d}"/>' for i, d in enumerate(ARCS))
    (here.parent / "hero.svg").write_text(f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1600 820" width="1600" height="820">
  <title>A Claude Code session finishes in a terminal, and an iPhone rings with the call</title>
  <style>
    .arc {{ fill: none; stroke: #ffffff; stroke-width: 10; stroke-linecap: round; opacity: .15;
      animation: ring 2.4s ease-in-out infinite; }}
    .a2 {{ animation-delay: .25s; }}
    .a3 {{ animation-delay: .5s; }}
    @keyframes ring {{ 0%, 100% {{ opacity: .15; }} 18% {{ opacity: 1; }} 45% {{ opacity: .15; }} }}
    @media (prefers-reduced-motion: reduce) {{
      .arc {{ animation: none; }} .a1 {{ opacity: 1; }} .a2 {{ opacity: .55; }} .a3 {{ opacity: .22; }}
    }}
  </style>
  <image width="1600" height="820" href="data:image/png;base64,{png}"/>
{arcs}
</svg>
''')


if __name__ == "__main__":
    main()
