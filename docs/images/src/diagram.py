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
W, H = 1800, 690
CARD_W, CARD_H, TOP = 330, 210, 120
XS = [30, 500, 970, 1440]
# The speech node's card, under the hub's.
NODE_X, NODE_TOP = XS[2], TOP + CARD_H + 120
NODES = [
    ("Claude Code", "with the ringback mod:", "type /ringback", "term"),
    ("Agent", "on the same Mac:", "Haiku talks it through", "mac"),
    ("Hub", "always on: rings you", "and carries the call", "board"),
    ("iPhone", "rings like a call,", "you talk it through", "phone"),
]
SPEECH = ("Speech node", "on a machine with a GPU:", "hears you and speaks", "wave")


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
    if kind == "wave":
        bars = "".join(f'<path d="M{x + 13 + 8 * i} {y + 23 - h}v{2 * h}" stroke="{g}" stroke-width="4" stroke-linecap="round"/>'
                       for i, h in enumerate([4, 10, 15, 8, 12]))  # fmt: skip
        return f'<rect x="{x}" y="{y}" width="58" height="46" rx="10" fill="none" stroke="{s}" stroke-width="3.5"/>{bars}'
    return f'''<rect x="{x+14}" y="{y-6}" width="30" height="56" rx="8" fill="none" stroke="{s}" stroke-width="3.5"/>
      <path d="M{x+25} {y+1}h8" stroke="{s}" stroke-width="3" stroke-linecap="round"/>
      <path d="M{x+46} {y+14}a8 8 0 0 1 0 12M{x+52} {y+9}a15 15 0 0 1 0 22" fill="none" stroke="{g}" stroke-width="3" stroke-linecap="round"/>'''


def card(node: tuple, x: float, top: float, c: dict) -> str:
    title, l1, l2, kind = node
    return f'''
    <rect x="{x}" y="{top}" width="{CARD_W}" height="{CARD_H}" rx="26" fill="{c["card"]}" stroke="{c["edge"]}" stroke-width="2"/>
    {glyph(kind, x + 32, top + 32, c)}
    <text x="{x + 32}" y="{top + 122}" font-family="SF Pro Display" font-weight="600" font-size="31" fill="{c["text"]}">{title}</text>
    <text x="{x + 32}" y="{top + 158}" font-family="SF Pro Text" font-size="21" fill="{c["soft"]}">{l1}</text>
    <text x="{x + 32}" y="{top + 186}" font-family="SF Pro Text" font-size="21" fill="{c["soft"]}">{l2}</text>'''


def label(x: float, y: float, text: str, c: dict, green: bool = False, anchor: str = "middle") -> str:
    """A step's label; a green one (the call itself) on a tinted pill."""
    if not green:
        return f'<text x="{x}" y="{y}" text-anchor="{anchor}" font-family="SF Pro Text" font-weight="500" font-size="20" fill="{c["soft"]}">{text}</text>'
    width = 10.2 * len(text) + 30  # SF Pro Text semibold at 20 px
    left = x - width / 2 if anchor == "middle" else x - 14
    return f'''<rect x="{left}" y="{y - 25}" width="{width}" height="36" rx="18" fill="{c["tint"]}"/>
    <text x="{x}" y="{y}" text-anchor="{anchor}" font-family="SF Pro Text" font-weight="600" font-size="20" fill="{c["green"]}">{text}</text>'''


def svg(c: dict) -> str:
    cards = "".join(card(n, x, TOP, c) for n, x in zip(NODES, XS)) + card(SPEECH, NODE_X, NODE_TOP, c)
    upper, lower = TOP + 78, TOP + 150  # the two arrows in each gap between the cards
    arrows = []
    # What starts a call, left to right: the briefing, the ring, the push.
    for i, text in enumerate(["1  briefing", "2  ring me", "3  push"]):
        x1, x2 = XS[i] + CARD_W + 14, XS[i + 1] - 16
        arrows.append(f'''
    <path d="M{x1} {upper}H{x2}" stroke="{c["line"]}" stroke-width="3.5" marker-end="url(#head)"/>
    {label((x1 + x2) / 2, upper - 16, text, c)}''')
    # The call, both ways: your voice between the phone and the hub, and your words and the
    # voice's replies between the hub and the agent, whose Haiku writes them.
    for i, text, green in [(2, "4  voice", True), (1, "6  words", False)]:
        x1, x2 = XS[i] + CARD_W + 16, XS[i + 1] - 16
        color, head = (c["green"], "ghead") if green else (c["line"], "head")
        arrows.append(f'''
    <path d="M{x1} {lower}H{x2}" stroke="{color}" stroke-width="{4 if green else 3.5}" marker-start="url(#{head})" marker-end="url(#{head})"/>
    {label((x1 + x2) / 2, lower + 42, text, c, green)}''')
    # The hub and the speech node: your audio down, the voice's speech up.
    mid = NODE_X + CARD_W / 2
    arrows.append(f'''
    <path d="M{mid} {TOP + CARD_H + 16}V{NODE_TOP - 16}" stroke="{c["green"]}" stroke-width="4" marker-start="url(#ghead)" marker-end="url(#ghead)"/>
    {label(mid + 30, (TOP + CARD_H + NODE_TOP) / 2 + 8, "5  hears and speaks", c, True, "start")}''')
    # What you said, from the agent back to the session.
    y0, y1 = TOP + CARD_H + 14, TOP + CARD_H + 92
    a, s0 = XS[1] + CARD_W / 2, XS[0] + CARD_W / 2
    arrows.append(f'''
    <path d="M{a} {y0}V{y1 - 26}a26 26 0 0 1-26 26H{s0 + 26}a26 26 0 0 1-26-26V{y0 + 6}" fill="none" stroke="{c["line"]}" stroke-width="3.5" marker-end="url(#head)"/>
    {label((s0 + a) / 2, y1 - 12, "7  what you said", c)}''')
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}">
  <defs>
    <marker id="head" viewBox="0 0 12 12" refX="10" refY="6" markerWidth="12" markerHeight="12" orient="auto-start-reverse" markerUnits="userSpaceOnUse">
      <path d="M1 1.5 10.5 6 1 10.5z" fill="{c["line"]}"/></marker>
    <marker id="ghead" viewBox="0 0 12 12" refX="10" refY="6" markerWidth="14" markerHeight="14" orient="auto-start-reverse" markerUnits="userSpaceOnUse">
      <path d="M1 1.5 10.5 6 1 10.5z" fill="{c["green"]}"/></marker>
  </defs>
  <text x="30" y="62" font-family="SF Pro Display" font-weight="600" font-size="26" fill="{c["text"]}">Each Mac with sessions</text>
  <text x="970" y="62" font-family="SF Pro Display" font-weight="600" font-size="26" fill="{c["text"]}">On your private network</text>
  <path d="M30 80H830M970 80H1770" stroke="{c["edge"]}" stroke-width="2"/>
  {cards}{"".join(arrows)}
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
