package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The status page's map of the system: the Macs, the hub with the speech nodes under it, and
// Apple push and the phones, each part and each link in one state: working, a problem, gone
// (a Mac asleep), or not set up. Laid out here in two shapes, wide for a desk and tall for a
// phone; status.html draws both as SVG, and the stylesheet shows the one that fits.

// mapPart is one part on the map.
type mapPart struct {
	Kind                    string // its icon: mac, hub, node, push or phone
	State                   string // working, problem, asleep or unset
	Name, Sub, Tag, TagTone string // TagTone: good, hold, neutral or link
	Brief                   string // Sub on the tall map, if shorter
	Setup                   string // for a part not set up: its section on the setup page
	Live                    bool   // on a live call
	// Wire is the state of its link to the hub (a phone's, through Nebula), PushWire a phone's
	// from Apple push; WireLive lights a link that a live call runs on.
	Wire, PushWire string
	WireLive       bool
	speaks         bool // a phone whose On-device voice is on

	// Where it's drawn, as the layout sets it: its tile, icon (G), badge (B), name, the line
	// under it and its tag.
	Short                              string
	X, Y, S, TX, TY, R, GX, GY, GS     float64
	BX, BY, NameY, SubY                float64
	TagX, TagY, TagW, TagMid, TagTextY float64
}

type mapWire struct {
	D, State string
	Live     bool
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
	Gate      *mapPoint // where calls stop while on hold: the hub's way out to the phones
}

// mapModel is what the map shows, before it's laid out.
type mapModel struct {
	Macs, Nodes, Phones []mapPart
	Hub, Push           mapPart
	SpeechNote          string // under the speech nodes: none linked, so calls wait
	Held                bool
	Title               string
}

const (
	tagHeight = 20.0
	tagFont   = 6.3 // a tag's width per character, roughly, at its size
)

// place puts a part's tile, s wide, centred at x, y, its name cut to n characters.
func place(p mapPart, x, y, s float64, n int) mapPart {
	p.X, p.Y, p.S = x, y, s
	p.TX, p.TY, p.R = x-s/2, y-s/2, s*0.27
	p.GS = s * 0.52
	p.GX, p.GY = x-p.GS/2, y-p.GS/2
	p.BX, p.BY = x+s/2-2, y-s/2+2
	p.Short = cut(p.Name, n)
	p.NameY = y + s/2 + 19
	p.SubY = p.NameY + 16
	if p.Setup != "" {
		p.Tag, p.TagTone = "Set up", "link"
	}
	if p.Tag != "" {
		p.TagW = float64(utf8.RuneCountInString(p.Tag))*tagFont + 20
		p.TagX, p.TagY = x-p.TagW/2, p.SubY+8
		if p.Sub == "" {
			p.TagY = p.NameY + 9
		}
		p.TagMid, p.TagTextY = p.TagY+tagHeight/2, p.TagY+14
	}
	return p
}

// width is how wide a part and its words are, roughly: 7.6 px a character of its name, 6.4
// of the line under it.
func (p mapPart) width() float64 {
	return max(p.S, float64(utf8.RuneCountInString(p.Short))*7.6, float64(utf8.RuneCountInString(p.Sub))*6.4, p.TagW)
}

// bottom is how far down a part's words reach.
func (p mapPart) bottom() float64 {
	switch {
	case p.Tag != "":
		return p.TagY + tagHeight
	case p.Sub != "":
		return p.SubY + 4
	}
	return p.NameY + 4
}

// cut shortens a name to n characters, with an ellipsis.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func num(x float64) string { return strconv.FormatFloat(x, 'f', 1, 64) }

// curveH is a link that leaves and arrives level; curveV one that leaves and arrives upright.
func curveH(x1, y1, x2, y2 float64) string {
	mid := (x1 + x2) / 2
	return "M" + num(x1) + " " + num(y1) + "C" + num(mid) + " " + num(y1) + " " + num(mid) + " " + num(y2) + " " + num(x2) + " " + num(y2)
}

func curveV(x1, y1, x2, y2 float64) string {
	mid := (y1 + y2) / 2
	return "M" + num(x1) + " " + num(y1) + "C" + num(x1) + " " + num(mid) + " " + num(x2) + " " + num(mid) + " " + num(x2) + " " + num(y2)
}

// spine goes straight down from x, y1 to turn, then bends to arrive upright at x2, y2.
func spine(x, y1, turn, x2, y2 float64) string {
	return "M" + num(x) + " " + num(y1) + "L" + num(x) + " " + num(turn) + curveV(x, turn, x2, y2)[len("M"+num(x)+" "+num(turn)):]
}

// first is at most n parts, and how many more there are.
func first(parts []mapPart, n int) ([]mapPart, int) {
	if len(parts) > n {
		return parts[:n], len(parts) - n
	}
	return parts, 0
}

// spread is where the i-th of n sits, centred on 0, gap apart.
func spread(i, n int, gap float64) float64 { return (float64(i) - float64(n-1)/2) * gap }

func (v *mapView) finish(parts ...mapPart) {
	top, bottom := 1e9, 0.0
	for _, p := range parts {
		v.Parts = append(v.Parts, p)
		top, bottom = min(top, p.TY), max(bottom, p.bottom())
	}
	for _, t := range v.Texts {
		top, bottom = min(top, t.Y-14), max(bottom, t.Y+6)
	}
	v.Top = top - 12
	v.H = bottom + 18 - v.Top
	// Links not set up or gone underneath, the live ones on top.
	rank := map[string]int{"unset": 0, "asleep": 1, "offline": 1, "problem": 2, "working": 3}
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

// wide: the Macs in a column on the left, the hub in the middle with the speech nodes under
// it, Apple push above the way to the phones on the right.
func (m mapModel) wide() mapView {
	v := mapView{W: 1000, Shape: "wide", Title: m.Title}
	const hubX, hubY = 470.0, 200.0
	hub := place(m.Hub, hubX, hubY, 84, 12)
	var parts []mapPart

	macs, moreMacs := first(m.Macs, 3)
	top := hubY
	for i, p := range macs {
		p = place(p, 130, hubY+spread(i, len(macs), 132), 60, 20)
		v.Wires = append(v.Wires, mapWire{curveH(p.X+p.S/2+8, p.Y, hub.TX-8, hubY+spread(i, len(macs), 16)), p.Wire, p.WireLive})
		parts = append(parts, p)
		top = min(top, p.TY)
	}
	if caption := macsCaption(m.Macs); caption != "" {
		v.Texts = append(v.Texts, mapText{130, top - 16, caption, "cap", "middle"})
	}
	if moreMacs > 0 {
		v.Texts = append(v.Texts, mapText{130, parts[len(parts)-1].bottom() + 18, fmt.Sprintf("and %d more", moreMacs), "cap", "middle"})
	}

	nodes, moreNodes := first(m.Nodes, 2)
	ny := max(hubY+215, hub.bottom()+110)
	for i, p := range nodes {
		p = place(p, hubX+spread(i, len(nodes), 180), ny, 60, 18)
		v.Wires = append(v.Wires, mapWire{curveV(hubX+spread(i, len(nodes), 22), hub.bottom()+8, p.X, p.TY-8), p.Wire, p.WireLive})
		parts = append(parts, p)
	}
	last := parts[len(parts)-1]
	capX := last.X + last.width()/2 + 22
	if caption := speechCaption(m.Nodes); caption != "" {
		v.Texts = append(v.Texts, mapText{capX, ny - 4, caption, "cap", "start"})
	}
	if moreNodes > 0 {
		v.Texts = append(v.Texts, mapText{capX, ny + 13, fmt.Sprintf("and %d more", moreNodes), "cap", "start"})
	}
	if m.SpeechNote != "" {
		v.Texts = append(v.Texts, mapText{capX, ny + 13 + 17*float64(min(moreNodes, 1)), m.SpeechNote, "cap wait", "start"})
	}

	// Higher with two phones, clear of the link through Nebula up to the first.
	push := place(m.Push, 730, map[bool]float64{true: 78, false: 40}[len(m.Phones) < 2], 56, 18)
	v.Wires = append(v.Wires, mapWire{curveH(hub.X+hub.S/2+8, hubY-20, push.TX-8, push.Y), m.Push.Wire, false})
	phones, _ := first(m.Phones, 2)
	right := hub.X + hub.S/2 + 8
	for i, p := range phones {
		p = place(p, 905, hubY+spread(i, len(phones), 150), 60, 18)
		v.Wires = append(v.Wires,
			mapWire{curveH(right, hubY+8+spread(i, len(phones), 14), p.TX-8, p.Y+8), p.Wire, p.WireLive},
			mapWire{curveH(push.X+push.S/2+8, push.Y, p.TX-8, p.Y-14), p.PushWire, false})
		parts = append(parts, p)
	}
	v.Texts = append(v.Texts, mapText{(right + 905 - 38) / 2, hubY + 30, "Nebula", "cap wire-label", "middle"})
	if m.Held {
		v.Gate = &mapPoint{right + 18, hubY - 5}
	}
	v.finish(append(parts, push, hub)...)
	return v
}

// tall: the Macs in a row at the top, the hub under them; from under the hub the speech nodes
// to the lower left, Apple push to the lower right, and the phones at the bottom, their links
// through Nebula straight down the middle between the two.
func (m mapModel) narrow() mapView {
	v := mapView{W: 360, Shape: "tall", Title: m.Title}
	const hubX = 180.0
	var parts []mapPart

	macs, moreMacs := first(m.Macs, 3)
	xs := map[int][]float64{1: {180}, 2: {108, 252}, 3: {62, 180, 298}}[len(macs)]
	lowest := 0.0
	for i, p := range macs {
		p = place(p, xs[i], 60, 52, 11)
		parts = append(parts, p)
		lowest = max(lowest, p.bottom())
	}
	hubY := lowest + 70
	hub := place(m.Hub, hubX, hubY, 72, 10)
	for i, p := range parts {
		v.Wires = append(v.Wires, mapWire{curveV(p.X, p.bottom()+8, hubX+spread(i, len(parts), 18), hub.TY-8), p.Wire, p.WireLive})
	}
	caption := macsCaption(m.Macs)
	if moreMacs > 0 {
		caption = fmt.Sprintf("%d Macs, %d shown", len(m.Macs), len(macs))
	}
	if caption != "" {
		v.Texts = append(v.Texts, mapText{hubX, 18, caption, "cap", "middle"})
	}

	// Everything else leaves from under the hub's words.
	out := hub.bottom() + 8
	ny := out + 70

	// The nodes, short of room beside each other, say they speak calls under their caption.
	nodes, moreNodes := first(m.Nodes, 2)
	nodeXs := map[int][]float64{1: {70}, 2: {44, 126}}[len(nodes)]
	nodesBottom := 0.0
	speaking := 0
	for i, p := range nodes {
		if p.TagTone == "good" {
			p.Tag, p.TagTone = "", ""
			speaking++
		}
		if p.Brief != "" {
			p.Sub = p.Brief
		}
		p = place(p, nodeXs[i], ny, 50, map[int]int{1: 12, 2: 8}[len(nodes)])
		v.Wires = append(v.Wires, mapWire{curveV(hubX-24+spread(i, len(nodes), 10), out, p.X, p.TY-8), p.Wire, p.WireLive})
		parts = append(parts, p)
		nodesBottom = max(nodesBottom, p.bottom())
	}
	// The caption's lines, under the nodes: what they are, then what's so of the speech.
	var lines []mapText
	text := speechCaption(m.Nodes)
	if moreNodes > 0 {
		text = fmt.Sprintf("%d speech nodes, 2 shown", len(m.Nodes))
	}
	if text != "" {
		lines = append(lines, mapText{Text: text, Class: "cap"})
	}
	if m.SpeechNote != "" {
		// On two lines, clear of the links down the middle.
		first, rest, two := strings.Cut(m.SpeechNote, ", ")
		if two {
			first += ","
		}
		for _, line := range []string{first, rest} {
			if line != "" {
				lines = append(lines, mapText{Text: line, Class: "cap wait"})
			}
		}
	}
	if speaking > 0 {
		lines = append(lines, mapText{Text: map[bool]string{true: "Speaks calls", false: "They speak calls"}[speaking == 1], Class: "cap good"})
	}
	capY := nodesBottom + 3
	for _, line := range lines {
		capY += 17
		line.X, line.Y, line.Anchor = 14, capY, "start"
		v.Texts = append(v.Texts, line)
	}

	push := place(m.Push, 300, ny, 50, 14)
	v.Wires = append(v.Wires, mapWire{curveV(hubX+24, out, push.X, push.TY-8), m.Push.Wire, false})
	turn := max(nodesBottom, push.bottom())
	phones, _ := first(m.Phones, 2)
	py := max(capY, push.bottom()) + 70
	phoneXs := map[int][]float64{1: {180}, 2: {118, 242}}[len(phones)]
	for i, p := range phones {
		if p.Brief != "" {
			p.Sub = p.Brief
		}
		p = place(p, phoneXs[i], py, 56, 13)
		x := hubX + spread(i, len(phones), 12)
		v.Wires = append(v.Wires,
			mapWire{spine(x, out, turn, p.X, p.TY-8), p.Wire, p.WireLive},
			mapWire{curveV(push.X, push.bottom()+8, p.X+p.S/2+4, p.TY-4), p.PushWire, false})
		parts = append(parts, p)
	}
	v.Texts = append(v.Texts, mapText{hubX + 14, (out+turn)/2 + 8, "Nebula", "cap wire-label", "start"})
	if m.Held {
		v.Gate = &mapPoint{hubX + 8, out + 14}
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

// speechCaption names the speech nodes, unless the only one is the one not set up, which
// names itself.
func speechCaption(nodes []mapPart) string {
	switch {
	case len(nodes) > 1:
		return "Speech nodes"
	case nodes[0].Setup != "":
		return ""
	}
	return "Speech node"
}
