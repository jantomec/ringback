"""Draws the README's "how it works" diagram, in GitHub's light and dark colours, on a
transparent background. Needs rsvg-convert (brew install librsvg).

    python3 docs/images/src/diagram.py
"""

import subprocess
from pathlib import Path

LOOKS = {
    "light": dict(card="#ffffff", edge="#d0d7de", text="#1f2328", soft="#59636e", line="#8c959f", green="#1a7f37", tint="#dafbe1"),
    "dark": dict(card="#161b22", edge="#30363d", text="#e6edf3", soft="#9198a1", line="#6e7681", green="#3fb950", tint="#12261e"),
}
W, H = 1800, 470
CARD_W, CARD_H, TOP = 330, 210, 120
XS = [30, 500, 970, 1440]
NODES = [
    ("Claude Code", "with the ringback mod:", "type /ringback", "term"),
    ("Voice server", "on the same Mac: hears,", "thinks and speaks", "mac"),
    ("Switchboard", "on an always-on Mac,", "rings your phone", "board"),
    ("iPhone", "rings like a call,", "talks to your Mac", "phone"),
]


def glyph(kind: str, x: float, y: float, c: dict) -> str:
    g, s = c["green"], c["text"]
    if kind == "term":
        return f'''<rect x="{x}" y="{y}" width="58" height="46" rx="10" fill="none" stroke="{s}" stroke-width="3.5"/>
      <path d="M{x+13} {y+14}l9 9-9 9" fill="none" stroke="{g}" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"/>
      <path d="M{x+28} {y+32}h14" stroke="{g}" stroke-width="4" stroke-linecap="round"/>'''
    if kind == "mac":
        return f'''<rect x="{x+4}" y="{y}" width="50" height="34" rx="5" fill="none" stroke="{s}" stroke-width="3.5"/>
      <path d="M{x} {y+42}h58" stroke="{s}" stroke-width="3.5" stroke-linecap="round"/>
      <path d="M{x+22} {y+12}a7 7 0 0 1 0 10M{x+28} {y+8}a13 13 0 0 1 0 18" fill="none" stroke="{g}" stroke-width="3" stroke-linecap="round"/>'''
    if kind == "board":
        jacks = "".join(f'<circle cx="{x + 14 + 15 * i}" cy="{y + 14 + 18 * j}" r="4.5" fill="{g if (i, j) == (1, 1) else s}"/>' for i in range(3) for j in range(2))
        return f'<rect x="{x}" y="{y}" width="58" height="46" rx="10" fill="none" stroke="{s}" stroke-width="3.5"/>{jacks}'
    return f'''<rect x="{x+14}" y="{y-6}" width="30" height="56" rx="8" fill="none" stroke="{s}" stroke-width="3.5"/>
      <path d="M{x+25} {y+1}h8" stroke="{s}" stroke-width="3" stroke-linecap="round"/>
      <path d="M{x+46} {y+14}a8 8 0 0 1 0 12M{x+52} {y+9}a15 15 0 0 1 0 22" fill="none" stroke="{g}" stroke-width="3" stroke-linecap="round"/>'''


def svg(c: dict) -> str:
    cards = []
    for (title, l1, l2, kind), x in zip(NODES, XS):
        cards.append(f'''
    <rect x="{x}" y="{TOP}" width="{CARD_W}" height="{CARD_H}" rx="26" fill="{c["card"]}" stroke="{c["edge"]}" stroke-width="2"/>
    {glyph(kind, x + 32, TOP + 32, c)}
    <text x="{x + 32}" y="{TOP + 122}" font-family="SF Pro Display" font-weight="600" font-size="31" fill="{c["text"]}">{title}</text>
    <text x="{x + 32}" y="{TOP + 158}" font-family="SF Pro Text" font-size="21" fill="{c["soft"]}">{l1}</text>
    <text x="{x + 32}" y="{TOP + 186}" font-family="SF Pro Text" font-size="21" fill="{c["soft"]}">{l2}</text>''')
    mid = TOP + CARD_H / 2
    arrows = []
    for i, label in enumerate(["1  briefing", "2  ring me", "3  push"]):
        x1, x2 = XS[i] + CARD_W + 14, XS[i + 1] - 16
        arrows.append(f'''
    <path d="M{x1} {mid}H{x2}" stroke="{c["line"]}" stroke-width="3.5" marker-end="url(#head)"/>
    <text x="{(x1 + x2) / 2}" y="{mid - 18}" text-anchor="middle" font-family="SF Pro Text" font-weight="500" font-size="20" fill="{c["soft"]}">{label}</text>''')
    # The call's audio, both ways between the iPhone and the voice server, under the cards; and
    # what you said, from the voice server back to the session.
    y0, y1 = TOP + CARD_H + 14, TOP + CARD_H + 92
    a, b = XS[3] + CARD_W / 2, XS[1] + CARD_W / 2
    s0 = XS[0] + CARD_W / 2
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}">
  <defs>
    <marker id="head" viewBox="0 0 12 12" refX="10" refY="6" markerWidth="12" markerHeight="12" orient="auto-start-reverse" markerUnits="userSpaceOnUse">
      <path d="M1 1.5 10.5 6 1 10.5z" fill="{c["line"]}"/></marker>
    <marker id="ghead" viewBox="0 0 12 12" refX="10" refY="6" markerWidth="14" markerHeight="14" orient="auto-start-reverse" markerUnits="userSpaceOnUse">
      <path d="M1 1.5 10.5 6 1 10.5z" fill="{c["green"]}"/></marker>
  </defs>
  <text x="30" y="62" font-family="SF Pro Display" font-weight="600" font-size="26" fill="{c["text"]}">Your Mac</text>
  <text x="970" y="62" font-family="SF Pro Display" font-weight="600" font-size="26" fill="{c["text"]}">Anywhere on your Nebula network</text>
  <path d="M30 80H830M970 80H1770" stroke="{c["edge"]}" stroke-width="2"/>
  {"".join(cards)}{"".join(arrows)}
  <path d="M{a} {y0}V{y1 - 26}a26 26 0 0 1-26 26H{b + 26}a26 26 0 0 1-26-26V{y0}" fill="none" stroke="{c["green"]}" stroke-width="4" marker-start="url(#ghead)" marker-end="url(#ghead)"/>
  <rect x="{(a + b) / 2 - 190}" y="{y1 - 18}" width="380" height="36" rx="18" fill="{c["tint"]}"/>
  <text x="{(a + b) / 2}" y="{y1 + 7}" text-anchor="middle" font-family="SF Pro Text" font-weight="600" font-size="20" fill="{c["green"]}">4  your voice, straight over Nebula</text>
  <path d="M{b - 70} {y0}V{y1 - 26}a26 26 0 0 1-26 26H{s0 + 26}a26 26 0 0 1-26-26V{y0 + 6}" fill="none" stroke="{c["line"]}" stroke-width="3.5" marker-end="url(#head)"/>
  <text x="{(s0 + b - 70) / 2}" y="{y1 - 12}" text-anchor="middle" font-family="SF Pro Text" font-weight="500" font-size="20" fill="{c["soft"]}">5  what you said</text>
</svg>'''


def main() -> None:
    out = Path(__file__).resolve().parent.parent
    for look, c in LOOKS.items():
        src = out / "src" / f"how-it-works-{look}.svg"
        src.write_text(svg(c))
        subprocess.run(["rsvg-convert", str(src), "-o", str(out / f"how-it-works-{look}.png")], check=True)
        src.unlink()


if __name__ == "__main__":
    main()
