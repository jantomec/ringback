package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The status page's checks: the hub's hold on every phone, the parts it remembers and
// forgets, Apple push's setup, and the map's layout for every number of parts it's made for.

func registerPhone(t *testing.T, token, name, onDevice string) {
	t.Helper()
	stateMu.Lock()
	defer stateMu.Unlock()
	if err := registerDevice(token, "sandbox", name, "", onDevice, nil); err != nil {
		t.Fatal(err)
	}
}

func seePart(t *testing.T, kind, name string) {
	t.Helper()
	sawLink(&link{kind: kind, name: name})
}

func TestHoldEveryPhone(t *testing.T) {
	makeAccount(t)
	advance(5 * time.Minute)
	os.Remove(waitingPath)
	registerPhone(t, "phone-token-1", "iPhone", "no")
	registerPhone(t, "phone-token-2", "iPad", "yes") // speaks itself: still nothing rings on hold
	defer os.Remove(devicesPath)
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)
	csrf := b.signIn()
	if _, _, page := b.get("/portal/"); !strings.Contains(page, "Taking calls") || !strings.Contains(page, "Hold calls") {
		t.Fatalf("the status page, taking calls:\n%s", page)
	}

	// A call held already, and one waiting for speech: only the held one asks again later.
	stateMu.Lock()
	_, err1 := holdCall("held-call-0000000001", "parser", "", "http://127.0.0.1:1", "", false, "", "")
	_, err2 := holdCall("speechless-000000001", "docs", "", "http://127.0.0.1:1", "", false, "", "no speech")
	stateMu.Unlock()
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}

	if status, _, _ := b.post("/portal/hold", url.Values{"csrf": {csrf}, "hold": {"maybe"}}); status != http.StatusBadRequest {
		t.Fatalf("hold=maybe: %d", status)
	}
	if status, to, _ := b.post("/portal/hold", url.Values{"csrf": {csrf}, "hold": {"on"}, "back": {"calls"}}); status != http.StatusSeeOther || to != "/portal/calls" {
		t.Fatalf("holding calls: %d → %q", status, to)
	}
	stateMu.Lock()
	devices := loadDevices()
	answer, _ := holdSwitch(map[string]any{"token": "phone-token-2"})
	placed, _ := call(map[string]any{"project": "infra", "voice": "http://127.0.0.1:1", "call_id": "rings-not-0000000001"})
	stateMu.Unlock()
	for token, d := range devices {
		if d["hold"] != "yes" || d["hold_set"] == "" {
			t.Errorf("%s after holding calls: %v", token, d)
		}
	}
	if answer["hold"] != true {
		t.Errorf("the app's hold switch after the portal held calls: %v", answer)
	}
	if placed["held"] != true || placed["reason"] != nil {
		t.Errorf("a call placed on hold, with a phone that speaks itself: %v", placed)
	}
	// Every page shows it, in the bar; the status page says no call rings, the phone's own
	// voice included.
	for _, path := range []string{"/portal/", "/portal/calls", "/portal/log", "/portal/setup"} {
		_, _, page := b.get(path)
		if !strings.Contains(page, `id="hold-pill"`) || !strings.Contains(page, ">On hold<") || !strings.Contains(page, `name="hold" value="off"`) {
			t.Errorf("%s on hold:\n%s", path, page)
		}
	}
	if _, _, page := b.get("/portal/"); !strings.Contains(page, "not with the phone's own voice") || !strings.Contains(page, "Take calls") {
		t.Errorf("the status page on hold:\n%s", page)
	}

	// Taking calls: every phone takes them, and the calls held ask again.
	logMu.Lock()
	logLines, logStart = logLines[:0], 0
	logMu.Unlock()
	if status, to, _ := b.post("/portal/hold", url.Values{"csrf": {csrf}, "hold": {"off"}, "back": {"nowhere"}}); status != http.StatusSeeOther || to != "/portal/" {
		t.Fatalf("taking calls: %d → %q", status, to)
	}
	if held, _ := heldUntil(now()); held {
		t.Fatal("still held after taking calls")
	}
	logged := strings.Join(logTail(), "\n")
	if !strings.Contains(logged, "portal: ana took calls, on 2 phones") || !strings.Contains(logged, "portal: 2 calls held, they ask again") {
		t.Errorf("the log after taking calls:\n%s", logged)
	}
	// One phone on hold while the other takes calls: calls ring, as before.
	stateMu.Lock()
	_, err := setHold("phone-token-1", true)
	view := hubHold(loadDevices(), 0)
	stateMu.Unlock()
	if err != nil || view.On || view.Label != "Taking calls" {
		t.Errorf("one phone on hold: %+v, %v", view, err)
	}
	os.Remove(waitingPath)
}

func TestPartsSeen(t *testing.T) {
	makeAccount(t)
	advance(5 * time.Minute)
	os.Remove(seenPath)
	os.Remove(devicesPath)
	part := func(v mapView, kind string) mapPart {
		for _, p := range v.Parts {
			if p.Kind == kind {
				return p
			}
		}
		t.Fatalf("no %s on the map", kind)
		return mapPart{}
	}

	// Nothing seen: every part but the hub is not set up, which is no problem.
	v := statusData()
	for _, kind := range []string{"mac", "node", "push", "phone"} {
		if p := part(v.Wide, kind); p.State != "unset" || p.Tag != "Set up" || p.Setup == "" {
			t.Errorf("%s with nothing set up: %+v", kind, p)
		}
	}
	if len(v.Problems) != 0 || v.Tone != "unset" {
		t.Errorf("nothing set up: %q, %v", v.Tone, v.Problems)
	}

	// A node linked speaks calls; gone, it's offline, a problem. A Mac gone is asleep, none.
	gpu := &link{kind: "speech", name: "gpu-tower", since: now()}
	registryMu.Lock()
	nodes[gpu] = true
	registryMu.Unlock()
	sawLink(gpu)
	seePart(t, "agent", "studio")
	registerPhone(t, "phone-token-1", "iPhone", "no")
	defer os.Remove(devicesPath)
	v = statusData()
	if p := part(v.Wide, "node"); p.State != "working" || p.Tag != "Speaks calls" {
		t.Errorf("a linked node: %+v", p)
	}
	registryMu.Lock()
	delete(nodes, gpu)
	registryMu.Unlock()
	sawLink(gpu)
	v = statusData()
	if p := part(v.Wide, "node"); p.State != "problem" || p.Wire != "problem" {
		t.Errorf("a node gone: %+v", p)
	}
	if p := part(v.Narrow, "mac"); p.State != "asleep" || p.Wire != "asleep" {
		t.Errorf("a Mac gone: %+v", p)
	}
	if len(v.Problems) != 1 || !strings.Contains(v.Problems[0].Text, "gpu-tower is offline, so calls wait") || v.Tone == "good" {
		t.Errorf("the problems with a node gone: %+v, %q", v.Problems, v.Tone)
	}
	if data, err := os.ReadFile(seenPath); err != nil || !strings.Contains(string(data), `"gpu-tower"`) || !strings.Contains(string(data), `"studio"`) {
		t.Fatalf("seen.json: %s, %v", data, err)
	}

	// Forgotten on the status page, unless it's linked again.
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)
	csrf := b.signIn()
	registryMu.Lock()
	nodes[gpu] = true
	registryMu.Unlock()
	if status, to, _ := b.post("/portal/forget", url.Values{"csrf": {csrf}, "kind": {"speech"}, "name": {"gpu-tower"}}); status != http.StatusSeeOther || to != "/portal/#parts" {
		t.Fatalf("forgetting a linked node: %d → %q", status, to)
	}
	if _, kept := loadSeen()["speech"]["gpu-tower"]; !kept {
		t.Error("a linked node was forgotten")
	}
	registryMu.Lock()
	delete(nodes, gpu)
	registryMu.Unlock()
	for _, form := range []url.Values{{"kind": {"speech"}, "name": {"gpu-tower"}}, {"kind": {"agent"}, "name": {"studio"}}} {
		form.Set("csrf", csrf)
		if status, _, _ := b.post("/portal/forget", form); status != http.StatusSeeOther {
			t.Fatalf("forgetting %v: %d", form, status)
		}
	}
	if seen := loadSeen(); len(seen["speech"])+len(seen["agent"]) != 0 {
		t.Errorf("seen after forgetting both: %v", seen)
	}
	if status, _, _ := b.post("/portal/forget", url.Values{"csrf": {csrf}, "kind": {"phone"}, "name": {"x"}}); status != http.StatusBadRequest {
		t.Errorf("forgetting a kind that isn't one: %d", status)
	}
	if p := part(statusData().Wide, "node"); p.State != "unset" {
		t.Errorf("a node forgotten: %+v", p)
	}
}

func TestPushSetup(t *testing.T) {
	dir := t.TempDir()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	good := filepath.Join(dir, "AuthKey_TEST.p8")
	bad := filepath.Join(dir, "not-a-key.p8")
	os.WriteFile(good, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	os.WriteFile(bad, []byte("hello"), 0o600)
	config := func(file string) map[string]string {
		return map[string]string{"apns.team_id": "ABCDE12345", "apns.bundle_id": "com.example.ringback",
			"apns.sandbox.key_id": "XYZ", "apns.sandbox.key_file": file}
	}
	sandbox := map[string]map[string]string{"t": {"device": "iPhone"}}
	production := map[string]map[string]string{"t": {"device": "iPhone", "environment": "production"}}
	for _, c := range []struct {
		name    string
		config  map[string]string
		devices map[string]map[string]string
		set     bool
		problem string
	}{
		{"no [apns]", map[string]string{}, sandbox, false, ""},
		{"a key", config(good), sandbox, true, ""},
		{"no key file", config(filepath.Join(dir, "gone.p8")), sandbox, true, "isn't there"},
		{"not a key", config(bad), sandbox, true, "not a PEM key"},
		{"no production key", config(good), production, true, "production environment, and the hub has no key"},
	} {
		set, problem := pushSetup(c.config, c.devices)
		if set != c.set || !strings.Contains(problem, c.problem) || (c.problem == "") != (problem == "") {
			t.Errorf("%s: %v, %q", c.name, set, problem)
		}
	}
}

// The map, wide and tall, for 0 to 4 Macs, 0 to 3 speech nodes and 0 to 2 phones, with long
// names: every part inside it, and no part's tile, name, line or tag over another's or over a
// caption. Text widths are estimates: 7.6 px a character for names, 6.4 for the rest.
func TestMapLayout(t *testing.T) {
	long := func(prefix string, i int) string { return fmt.Sprintf("%s-%d-with-a-long-name", prefix, i) }
	type box struct {
		x1, y1, x2, y2 float64
		what           string
	}
	width := func(s string, per float64) float64 { return float64(utf8.RuneCountInString(s)) * per }
	for macs := range 5 {
		for nodeCount := range 4 {
			for phones := range 3 {
				m := mapModel{Hub: mapPart{Kind: "hub", Name: "Hub", Tag: "Taking calls", TagTone: "neutral"},
					Push: mapPart{Kind: "push", Name: "Apple push", Sub: "Last push 14:05"}, SpeechNote: "None linked, so calls wait", Held: true}
				for i := range macs {
					m.Macs = append(m.Macs, mapPart{Kind: "mac", Name: long("mac", i), Sub: "3 sessions"})
				}
				if macs == 0 {
					m.Macs = []mapPart{{Kind: "mac", Name: "Mac", Sub: "Not set up", Setup: "mac"}}
				}
				for i := range nodeCount {
					m.Nodes = append(m.Nodes, mapPart{Kind: "node", Name: long("node", i), Sub: "Models loaded", Brief: "Loaded", Tag: "Speaks calls", TagTone: "good"})
				}
				if nodeCount == 0 {
					m.Nodes = []mapPart{{Kind: "node", Name: "Speech node", Sub: "Not set up", Setup: "node"}}
				}
				for i := range phones {
					m.Phones = append(m.Phones, mapPart{Kind: "phone", Name: long("iPhone", i), Sub: "On-device voice on", Tag: "Speaks calls"})
				}
				if phones == 0 {
					m.Phones = []mapPart{{Kind: "phone", Name: "iPhone", Sub: "Not set up", Setup: "phone"}}
				}
				for _, v := range []mapView{m.wide(), m.narrow()} {
					where := fmt.Sprintf("%s, %d Macs, %d nodes, %d phones", v.Shape, macs, nodeCount, phones)
					var boxes []box
					for _, p := range v.Parts {
						w := max(p.S, width(p.Short, 7.6), width(p.Sub, 6.4), p.TagW)
						b := box{p.X - w/2, p.TY, p.X + w/2, p.bottom(), p.Kind + " " + p.Short}
						if b.x1 < 0 || b.x2 > v.W || b.y1 < v.Top || b.y2 > v.Top+v.H {
							t.Errorf("%s: %s is outside the map (%v, %v): %+v", where, b.what, v.W, v.H, b)
						}
						boxes = append(boxes, b)
					}
					for _, c := range v.Texts {
						w := width(c.Text, 6.6)
						x1 := map[string]float64{"start": c.X, "middle": c.X - w/2, "end": c.X - w}[c.Anchor]
						b := box{x1, c.Y - 12, x1 + w, c.Y + 3, "caption " + c.Text}
						if b.x1 < 0 || b.x2 > v.W {
							t.Errorf("%s: %s is outside the map: %+v", where, b.what, b)
						}
						boxes = append(boxes, b)
					}
					for i, a := range boxes {
						for _, b := range boxes[i+1:] {
							if a.x1 < b.x2 && b.x1 < a.x2 && a.y1 < b.y2 && b.y1 < a.y2 {
								t.Errorf("%s: %s overlaps %s: %+v %+v", where, a.what, b.what, a, b)
							}
						}
					}
					// No link through a tile or words: each tile, name, line and tag on its own,
					// and the captions, but for a link's first and last 12 px, at its parts.
					var marks []box
					for _, p := range v.Parts {
						name, sub := width(p.Short, 7.6), width(p.Sub, 6.4)
						marks = append(marks, box{p.TX, p.TY, p.TX + p.S, p.TY + p.S, p.Kind + " " + p.Short + "'s tile"},
							box{p.X - name/2, p.NameY - 12, p.X + name/2, p.NameY + 3, p.Kind + " " + p.Short + "'s name"})
						if p.Sub != "" {
							marks = append(marks, box{p.X - sub/2, p.SubY - 11, p.X + sub/2, p.SubY + 3, p.Kind + " " + p.Short + "'s line"})
						}
						if p.Tag != "" {
							marks = append(marks, box{p.TagX, p.TagY, p.TagX + p.TagW, p.TagY + tagHeight, p.Kind + " " + p.Short + "'s tag"})
						}
					}
					marks = append(marks, boxes[len(v.Parts):]...)
					for _, w := range v.Wires {
						points := sample(w.D)
						ends := [2][2]float64{points[0], points[len(points)-1]}
						for _, pt := range points {
							if math.Hypot(pt[0]-ends[0][0], pt[1]-ends[0][1]) < 12 || math.Hypot(pt[0]-ends[1][0], pt[1]-ends[1][1]) < 12 {
								continue
							}
							for _, b := range marks {
								if pt[0] > b.x1 && pt[0] < b.x2 && pt[1] > b.y1 && pt[1] < b.y2 {
									t.Errorf("%s: a link (%s) runs through %s at %.0f, %.0f", where, w.D, b.what, pt[0], pt[1])
									break
								}
							}
						}
					}
				}
			}
		}
	}
}

// sample is points along a link's path, as the map writes it: a move, then lines and cubic
// curves.
func sample(d string) [][2]float64 {
	var points [][2]float64
	var at [2]float64
	for _, m := range regexp.MustCompile(`([MLC])([^MLC]*)`).FindAllStringSubmatch(d, -1) {
		var n []float64
		for _, field := range strings.Fields(m[2]) {
			x, _ := strconv.ParseFloat(field, 64)
			n = append(n, x)
		}
		switch m[1] {
		case "M":
			at = [2]float64{n[0], n[1]}
			points = append(points, at)
		case "L":
			for s := 1; s <= 20; s++ {
				t := float64(s) / 20
				points = append(points, [2]float64{at[0] + (n[0]-at[0])*t, at[1] + (n[1]-at[1])*t})
			}
			at = [2]float64{n[0], n[1]}
		case "C":
			for s := 1; s <= 40; s++ {
				t := float64(s) / 40
				u := 1 - t
				point := func(a, b, c, e float64) float64 { return u*u*u*a + 3*u*u*t*b + 3*u*t*t*c + t*t*t*e }
				points = append(points, [2]float64{point(at[0], n[0], n[2], n[4]), point(at[1], n[1], n[3], n[5])})
			}
			at = [2]float64{n[4], n[5]}
		}
	}
	return points
}
