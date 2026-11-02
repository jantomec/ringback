"""Draws the repository's social preview (docs/images/social-preview.png, 1280 x 640), shown
when a link to the repo is shared: the icon, name and tagline beside the ringing phone from the
hero. GitHub takes it in the repo's Settings, under Social preview, not from the README. Needs
rsvg-convert.

    python3 docs/images/src/social.py
"""

import subprocess
import tempfile
from pathlib import Path


def main() -> None:
    here = Path(__file__).resolve().parent
    hero = (here / "hero.svg").read_text()
    defs = hero[hero.index("<defs>") : hero.index("</defs>") + len("</defs>")]
    arcs = hero[hero.index("  <!-- the ring travelling across") : hero.index("  <!-- the phone, ringing -->")]
    phone = hero[hero.index("  <!-- the phone, ringing -->") : hero.rindex("</svg>")]
    icon = (here / "icon.svg").read_text()
    icon = icon[icon.index("<clipPath") : icon.rindex("</svg>")].replace('id="bg"', 'id="iconbg"').replace("url(#bg)", "url(#iconbg)")
    svg = f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1280 640" width="1280" height="640">
  {defs}
  <rect width="1280" height="640" fill="url(#bg)"/>
  <rect width="1280" height="640" fill="url(#glow)"/>
  <svg x="96" y="150" width="112" height="112" viewBox="0 0 100 100">{icon}</svg>
  <text x="96" y="346" font-family="SF Pro Display" font-weight="700" font-size="76" fill="#ffffff">Ringback</text>
  <text x="98" y="408" font-family="SF Pro Display" font-weight="500" font-size="32" fill="#e6e9ef">Your Claude Code sessions call you back.</text>
  <text x="98" y="456" font-family="SF Pro Text" font-size="25" fill="#8b93a3">When a session finishes, your iPhone rings.</text>
  <text x="98" y="490" font-family="SF Pro Text" font-size="25" fill="#8b93a3">Talk it through, hands-free.</text>
  <g transform="translate(60 40) scale(0.76)">
{arcs}{phone}
  </g>
</svg>
'''
    with tempfile.TemporaryDirectory() as tmp:
        src = Path(tmp) / "social.svg"
        src.write_text(svg)
        subprocess.run(["rsvg-convert", str(src), "-o", str(here.parent / "social-preview.png")], check=True)


if __name__ == "__main__":
    main()
