package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The status page's map of the system: the Macs, the hub, the speech nodes, Apple push and the
// phones, each part and each link in one state: working, a problem, gone (a Mac asleep, a
// phone not heard from), or not set up. Laid out here in two shapes, wide for a desk and tall
// for a phone; status.html draws both as SVG, and the stylesheet shows the one that fits. Every
// link starts and ends at a tile; where one passes behind a part's own words, they're drawn
// over it with a halo.

// mapPart is one part on the map.
type mapPart struct {
	Kind                    string // its icon: mac, hub, node, push or phone
	State                   string // working, problem, asleep, untried or unset
	Name, Sub, Tag, TagTone string // TagTone: good, hold or link
	Setup                   string // for a part not set up: its section on the setup page
	Live                    bool   // on a call, or ringing
	// Wire is the state of its link to the hub (a phone's, through Nebula), PushWire a phone's
	// from Apple push; WireLive and PushLive light a link a call runs on.
	Wire, PushWire     string
	WireLive, PushLive bool
	speaks             bool // a phone whose On-device voice is on

	// Where it's drawn, as the layout sets it: its tile, icon (G), badge (B), name, the line
	// under it and its tag; Above puts the words above the tile.
	Short                              string
	Above                              bool
	X, Y, S, TX, TY, R, GX, GY, GS     float64
	BX, BY, NameY, SubY                float64
	TagX, TagY, TagW, TagMid, TagTextY float64
}

type mapWire struct {
	D, State string
	Live     bool
	ends     [2]string // the parts it links, by key
}

type mapText struct {
	X, Y                float64
	Text, Class, Anchor string
}

type mapPoint struct{ X, Y float64 }

type mapView struct {
	Top, W, H float64 // its viewBox: from Top down, W wide and H high
	Shape     string  // wide or tall
	Title     string
	Parts     []mapPart
	Wires     []mapWire
	Texts     []mapText
	Gate      *mapPoint // while on hold: a pause badge on the hub
}

// mapModel is what the map shows, before it's laid out.
type mapModel struct {
	Macs, Nodes, Phones []mapPart
	Hub, Push           mapPart
	SpeechNote          string // under the speech node not set up: calls wait for one
	Held                bool
	Title               string
}

const (
	tagHeight = 20.0
	tagFont   = 6.3 // a tag's width per character, roughly, at its size
)

func (p mapPart) key() string { return p.Kind + ":" + p.Name }

// place puts a part's tile, s wide, centred at x, y, its name cut to n characters, its words
// under it or (above) over it.
func place(p mapPart, x, y, s float64, n int, above bool) mapPart {
	p.X, p.Y, p.S, p.Above = x, y, s, above
	p.TX, p.TY, p.R = x-s/2, y-s/2, s*0.27
	p.GS = s * 0.52
	p.GX, p.GY = x-p.GS/2, y-p.GS/2
	p.BX, p.BY = x+s/2-2, y-s/2+2
	p.Short = cut(p.Name, n)
	if p.Setup != "" {
		p.Tag, p.TagTone = "Set up", "link"
	}
	if p.Tag != "" {
		p.TagW = float64(utf8.RuneCountInString(p.Tag))*tagFont + 20
		p.TagX = x - p.TagW/2
	}
	if above {
		// Bottom up: the line under the name, the name, the tag over them.
		p.SubY = p.TY - 9
		p.NameY = p.SubY - 16
		if p.Sub == "" {
			p.NameY = p.TY - 9
		}
		p.TagY = p.NameY - 14 - tagHeight - 4
	} else {
		p.NameY = y + s/2 + 19
		p.SubY = p.NameY + 16
		p.TagY = p.SubY + 8
		if p.Sub == "" {
			p.TagY = p.NameY + 9
		}
	}
	p.TagMid, p.TagTextY = p.TagY+tagHeight/2, p.TagY+14
	return p
}

// top and bottom are how far up and down a part and its words reach.
func (p mapPart) top() float64 {
	switch {
	case !p.Above:
		return p.TY
	case p.Tag != "":
		return p.TagY
	}
	return p.NameY - 13
}

func (p mapPart) bottom() float64 {
	switch {
	case p.Above:
		return p.TY + p.S
	case p.Tag != "":
		return p.TagY + tagHeight
	case p.Sub != "":
		return p.SubY + 4
	}
	return p.NameY + 4
}

// width is how wide a part and its words are, roughly: 7.6 px a character of its name, 6.4
// of the line under it.
func (p mapPart) width() float64 {
	return max(p.S, float64(utf8.RuneCountInString(p.Short))*7.6, float64(utf8.RuneCountInString(p.Sub))*6.4, p.TagW)
}

// cut shortens a name to n characters, with an ellipsis.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func num(x float64) string { return strconv.FormatFloat(x, 'f', 1, 64) }

func pt(x, y float64) string { return num(x) + " " + num(y) }

// curveH is a link that leaves and arrives level; curveV one that leaves and arrives upright;
// corner one that leaves level and arrives upright.
func curveH(x1, y1, x2, y2 float64) string {
	mid := (x1 + x2) / 2
	return "M" + pt(x1, y1) + "C" + pt(mid, y1) + " " + pt(mid, y2) + " " + pt(x2, y2)
}

func curveV(x1, y1, x2, y2 float64) string {
	mid := (y1 + y2) / 2
	return "M" + pt(x1, y1) + "C" + pt(x1, mid) + " " + pt(x2, mid) + " " + pt(x2, y2)
}

func corner(x1, y1, x2, y2 float64) string {
	return "M" + pt(x1, y1) + "C" + pt(x1+(x2-x1)*0.6, y1) + " " + pt(x2, y1+(y2-y1)*0.4) + " " + pt(x2, y2)
}

// spine goes straight down from x, y1 to turn, then bends to arrive upright at x2, y2.
func spine(x, y1, turn, x2, y2 float64) string {
	return "M" + pt(x, y1) + "L" + pt(x, turn) + curveV(x, turn, x2, y2)[len("M"+pt(x, turn)):]
}

// around goes from x1, y1 right to the column at x, down it, and left into x2, y2, its turns
// rounded: Apple push's way to two phones, outside the links through Nebula.
func around(x1, y1, x, x2, y2 float64) string {
	const r = 22.0
	k := r * 0.55 // a quarter circle, as a cubic
	return "M" + pt(x1, y1) + "L" + pt(x-r, y1) + "C" + pt(x-r+k, y1) + " " + pt(x, y1+r-k) + " " + pt(x, y1+r) +
		"L" + pt(x, y2-r) + "C" + pt(x, y2-r+k) + " " + pt(x-r+k, y2) + " " + pt(x-r, y2) + "L" + pt(x2, y2)
}

func wire(d, state string, live bool, a, b mapPart) mapWire {
	return mapWire{D: d, State: state, Live: live, ends: [2]string{a.key(), b.key()}}
}

// first is at most n parts, and how many more there are, and how many of those are asleep.
func first(parts []mapPart, n int) ([]mapPart, int, int) {
	if len(parts) <= n {
		return parts, 0, 0
	}
	asleep := 0
	for _, p := range parts[n:] {
		if p.State == "asleep" {
			asleep++
		}
	}
	return parts[:n], len(parts) - n, asleep
}

// more is the line under a group that shows only some: "and 1 asleep", "and 2 more".
func more(hidden, asleep int) string {
	if hidden == asleep {
		return fmt.Sprintf("and %d asleep", hidden)
	}
	return fmt.Sprintf("and %d more", hidden)
}

// spread is where the i-th of n sits, centred on 0, gap apart.
func spread(i, n int, gap float64) float64 { return (float64(i) - float64(n-1)/2) * gap }

func (v *mapView) finish(parts ...mapPart) {
	top, bottom := 1e9, 0.0
	for _, p := range parts {
		v.Parts = append(v.Parts, p)
		top, bottom = min(top, p.top()), max(bottom, p.bottom())
	}
	for _, t := range v.Texts {
		top, bottom = min(top, t.Y-14), max(bottom, t.Y+6)
	}
	v.Top = top - 12
	v.H = bottom + 18 - v.Top
	// Links not set up or gone underneath, the live ones on top.
	rank := map[string]int{"unset": 0, "asleep": 1, "problem": 2, "working": 3}
	sort.SliceStable(v.Wires, func(i, j int) bool {
		a, b := rank[v.Wires[i].State], rank[v.Wires[j].State]
		if v.Wires[i].Live {
			a = 4
		}
		if v.Wires[j].Live {
			b = 4
		}
		return a < b
	})
}

// wide: the Macs in a column on the left, the hub in the middle, Apple push up and the phones
// to the right, the speech nodes down to the right; a caption over each group.
func (m mapModel) wide() mapView {
	v := mapView{W: 1000, Shape: "wide", Title: m.Title}
	const hubX, hubY = 420.0, 210.0
	hub := place(m.Hub, hubX, hubY, 84, 12, false)
	right := hub.X + hub.S/2 + 8
	var parts []mapPart

	macs, hidden, asleep := first(m.Macs, 3)
	for i, p := range macs {
		p = place(p, 110, hubY+spread(i, len(macs), 132), 60, 20, false)
		v.Wires = append(v.Wires, wire(curveH(p.X+p.S/2+8, p.Y, hub.TX-8, hubY+spread(i, len(macs), 16)), p.Wire, p.WireLive, p, hub))
		parts = append(parts, p)
	}
	if caption := macsCaption(m.Macs); caption != "" {
		v.Texts = append(v.Texts, mapText{110, parts[0].TY - 16, caption, "cap", "middle"})
	}
	if hidden > 0 {
		v.Texts = append(v.Texts, mapText{110, parts[len(parts)-1].bottom() + 18, more(hidden, asleep), "cap", "middle"})
	}

	// The speech nodes, down to the right, each linked from under the hub, below the links to
	// the phones, into the tile's top left, leaving room for the caption over it.
	nodes, hiddenNodes, _ := first(m.Nodes, 2)
	ny := hubY + 205
	nodeXs := map[int][]float64{1: {660}, 2: {600, 780}}[len(nodes)]
	var placed []mapPart
	for i, p := range nodes {
		p = place(p, nodeXs[i], ny, 60, 12, false)
		v.Wires = append(v.Wires, wire(curveV(hub.X+28+12*float64(i), hub.TY+hub.S+6, p.TX+14, p.TY-8), p.Wire, p.WireLive, hub, p))
		placed = append(placed, p)
	}
	groupX := (placed[0].X + placed[len(placed)-1].X) / 2
	if caption := speechCaption(m.Nodes); caption != "" {
		v.Texts = append(v.Texts, mapText{placed[0].TX + 26, ny - 30 - 14, caption, "cap", "start"})
	}
	low := 0.0
	for _, p := range placed {
		low = max(low, p.bottom())
	}
	if hiddenNodes > 0 {
		v.Texts = append(v.Texts, mapText{groupX, low + 18, more(hiddenNodes, 0), "cap", "middle"})
		low += 18
	}
	if m.SpeechNote != "" {
		v.Texts = append(v.Texts, mapText{groupX, low + 18, m.SpeechNote, "cap wait", "middle"})
	}
	parts = append(parts, placed...)

	// Apple push above the way to the phones: with two, its links run round the outside of
	// theirs through Nebula, to their right sides.
	phones, _, _ := first(m.Phones, 2)
	push := place(m.Push, 690, map[bool]float64{true: 80, false: 44}[len(phones) == 1], 56, 18, false)
	v.Wires = append(v.Wires, wire(curveH(right, hubY-20, push.TX-8, push.Y), m.Push.Wire, false, hub, push))
	for i, p := range phones {
		p = place(p, 900, hubY+spread(i, len(phones), 150), 60, 18, false)
		v.Wires = append(v.Wires, wire(curveH(right, hubY+8+spread(i, len(phones), 14), p.TX-8, p.Y+8), p.Wire, p.WireLive, hub, p))
		if len(phones) == 1 {
			v.Wires = append(v.Wires, wire(curveH(push.X+push.S/2+8, push.Y, p.TX-8, p.Y-14), p.PushWire, p.PushLive, push, p))
		} else {
			v.Wires = append(v.Wires, wire(around(push.X+push.S/2+8, push.Y, 980, p.X+p.S/2+8, p.Y), p.PushWire, p.PushLive, push, p))
		}
		parts = append(parts, p)
	}
	if caption := phonesCaption(m.Phones); caption != "" {
		v.Texts = append(v.Texts, mapText{900, parts[len(parts)-len(phones)].TY - 16, caption, "cap", "middle"})
	}
	v.Texts = append(v.Texts, mapText{(right + 900 - 38) / 2, hubY - 2, "Nebula", "cap wire-label", "middle"})
	if m.Held {
		v.Gate = &mapPoint{hub.X + hub.S/2 - 4, hub.Y + hub.S/2 - 4}
	}
	v.finish(append(parts, push, hub)...)
	return v
}

// tall: the Macs in a row at the top, their words over them; the hub under them, the speech
// nodes beside it; and at the bottom the phones and Apple push in a row, push's links to them
// level, under the links through Nebula.
func (m mapModel) narrow() mapView {
	v := mapView{W: 360, Shape: "tall", Title: m.Title}
	const hubX = 180.0
	var parts []mapPart

	macs, hidden, asleep := first(m.Macs, 3)
	xs := map[int][]float64{1: {180}, 2: {108, 252}, 3: {62, 180, 298}}[len(macs)]
	capY := 16.0
	if macsCaption(m.Macs) == "" {
		capY = -4
	}
	macY := 0.0
	for i, p := range macs {
		p = place(p, xs[i], 0, 52, 13, true)
		macY = max(macY, capY+8-p.top())
		parts = append(parts, p)
	}
	for i, p := range parts {
		parts[i] = place(p, p.X, macY, 52, 13, true)
	}
	hubY := macY + 26 + 78
	hub := place(m.Hub, hubX, hubY, 72, 10, false)
	for i, p := range parts {
		v.Wires = append(v.Wires, wire(curveV(p.X, p.TY+p.S+6, hubX+spread(i, len(parts), 18), hub.TY-6), p.Wire, p.WireLive, p, hub))
	}
	caption := macsCaption(m.Macs)
	if hidden > 0 {
		caption = fmt.Sprintf("%d Macs: %d shown, %s", len(m.Macs), len(macs), strings.TrimPrefix(more(hidden, asleep), "and "))
	}
	if caption != "" {
		v.Texts = append(v.Texts, mapText{hubX, capY, caption, "cap", "middle"})
	}

	// The speech nodes beside the hub, linked level from its sides.
	nodes, hiddenNodes, _ := first(m.Nodes, 2)
	low := hub.bottom()
	for i, p := range nodes {
		x, side := 60.0, hub.TX-6
		if i == 1 {
			x, side = 300, hub.TX+hub.S+6
		}
		p = place(p, x, hubY, 50, 12, false)
		edge := p.TX + p.S + 6
		if i == 1 {
			edge = p.TX - 6
		}
		v.Wires = append(v.Wires, wire(curveH(side, hubY, edge, hubY), p.Wire, p.WireLive, hub, p))
		parts = append(parts, p)
		low = max(low, p.bottom())
		if i == 0 {
			if text := speechCaption(m.Nodes); text != "" {
				v.Texts = append(v.Texts, mapText{p.X, p.TY - 12, text, "cap", "middle"})
			}
			note := p.bottom()
			if hiddenNodes > 0 {
				note += 17
				v.Texts = append(v.Texts, mapText{p.X, note, more(hiddenNodes, 0), "cap", "middle"})
			}
			if m.SpeechNote != "" {
				first, rest, two := strings.Cut(m.SpeechNote, ", ")
				if two {
					first += ","
				}
				for _, line := range []string{first, rest} {
					if line != "" {
						note += 17
						v.Texts = append(v.Texts, mapText{p.X, note, line, "cap wait", "middle"})
					}
				}
			}
			low = max(low, note)
		}
	}

	// The phones and Apple push, in a row: one phone in the middle with push to its right, or
	// two either side of push. Push's links are level, under the links through Nebula, which
	// come down from the hub to the phones' tops.
	phones, _, _ := first(m.Phones, 2)
	py := low + 86
	out := hub.TY + hub.S + 6
	var push mapPart
	if len(phones) == 1 {
		p := place(phones[0], 180, py, 56, 14, false)
		push = place(m.Push, 300, py, 50, 14, false)
		v.Wires = append(v.Wires,
			wire(curveV(hubX, out, p.X, p.TY-6), p.Wire, p.WireLive, hub, p),
			wire(curveV(hubX+24, out, push.X, push.TY-6), m.Push.Wire, false, hub, push),
			wire(curveH(push.TX-6, py, p.TX+p.S+6, py), p.PushWire, p.PushLive, push, p))
		parts = append(parts, p)
		v.Texts = append(v.Texts, mapText{hubX - 8, (out+py-28)/2 + 4, "Nebula", "cap wire-label", "end"})
	} else {
		// Straight down past the nodes' words, then out to the phones.
		push = place(m.Push, 180, py, 50, 14, false)
		v.Wires = append(v.Wires, wire(curveV(hubX, out, push.X, push.TY-6), m.Push.Wire, false, hub, push))
		turn := low + 8
		for i, p := range phones {
			x, from, to := 64.0, hubX-24, push.TX-6
			if i == 1 {
				x, from, to = 296, hubX+24, push.TX+push.S+6
			}
			p = place(p, x, py, 56, 13, false)
			edge := p.TX + p.S + 6
			if i == 1 {
				edge = p.TX - 6
			}
			v.Wires = append(v.Wires,
				wire(spine(from, out, turn, p.X, p.TY-6), p.Wire, p.WireLive, hub, p),
				wire(curveH(to, py, edge, py), p.PushWire, p.PushLive, push, p))
			parts = append(parts, p)
		}
		v.Texts = append(v.Texts, mapText{hubX + 32, turn + 4, "Nebula", "cap wire-label", "start"})
	}
	if m.Held {
		v.Gate = &mapPoint{hub.X + hub.S/2 - 4, hub.Y + hub.S/2 - 4}
	}
	v.finish(append(parts, push, hub)...)
	return v
}

// macsCaption names the Macs, unless the only one is the one not set up, which names itself.
func macsCaption(macs []mapPart) string {
	switch {
	case len(macs) > 1:
		return "Macs"
	case macs[0].Setup != "":
		return ""
	}
	return "Mac"
}

// speechCaption names the speech nodes, unless the only one is the one not set up.
func speechCaption(nodes []mapPart) string {
	switch {
	case len(nodes) > 1:
		return "Speech nodes"
	case nodes[0].Setup != "":
		return ""
	}
	return "Speech node"
}

func phonesCaption(phones []mapPart) string {
	switch {
	case len(phones) > 1:
		return "Phones"
	case phones[0].Setup != "":
		return ""
	}
	return "Phone"
}
