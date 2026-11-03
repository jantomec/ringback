package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
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

	"github.com/coder/websocket"
)

// The status page's checks: the hub's hold on every phone, and a phone that joins it; the
// calls held asking again; the parts the hub remembers, forgets and finds gone after a
// restart; what a live call shows; Apple push's setup; and the map's layout for every number
// of parts it's made for.

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
	sawLink(&link{kind: kind, name: name}, false)
}

func TestHoldEveryPhone(t *testing.T) {
	makeAccount(t)
	advance(5 * time.Minute)
	os.Remove(waitingPath)
	defer os.Remove(waitingPath)
	registerPhone(t, "phone-token-1", "iPhone", "no")
	registerPhone(t, "phone-token-2", "iPad", "yes") // speaks itself: still nothing rings on hold
	defer os.Remove(devicesPath)
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)
	csrf := b.signIn()
	if _, _, page := b.get("/portal/"); !strings.Contains(page, ">Taking calls<") || !strings.Contains(page, "Not on hold") || !strings.Contains(page, "Hold calls") {
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
	if status, to, _ := b.post("/portal/hold", url.Values{"csrf": {csrf}, "hold": {"on"}}); status != http.StatusSeeOther || to != "/portal/#hold" {
		t.Fatalf("holding calls: %d → %q", status, to)
	}
	stateMu.Lock()
	devices := loadDevices()
	answer, _ := holdSwitch(map[string]any{"token": "phone-token-2"})
	placed, _ := placeCall("test-mac", map[string]any{"project": "infra", "call_id": "rings-not-0000000001"})
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
	// Every page's bar says so, linking to the control, which is only on Status; Setup doesn't
	// refresh itself.
	for _, path := range []string{"/portal/", "/portal/calls", "/portal/log", "/portal/setup"} {
		_, _, page := b.get(path)
		if !strings.Contains(page, `id="hold-chip" href="/portal/#hold"`) || !strings.Contains(page, ">On hold<") || !strings.Contains(page, " · 3 waiting") {
			t.Errorf("%s's bar on hold:\n%s", path, page)
		}
		if strings.Contains(page, `action="/portal/hold"`) != (path == "/portal/") {
			t.Errorf("%s: the hold's control shows on it: %v", path, path != "/portal/")
		}
		if strings.Contains(page, "data-refresh") == (path == "/portal/setup") {
			t.Errorf("%s: refreshes itself: %v", path, path == "/portal/setup")
		}
	}
	_, _, page := b.get("/portal/")
	for _, words := range []string{"On hold until you take calls", "not with the phone&#39;s own voice", "Take calls", "When the hold ends, calls"} {
		if !strings.Contains(page, words) {
			t.Errorf("the status page on hold doesn't say %q:\n%s", words, page)
		}
	}
	if _, _, page := b.get("/portal/calls"); !strings.Contains(page, ">Waiting<") || !strings.Contains(page, "Waiting for a speech node") || !strings.Contains(page, "Rings when you take calls") {
		t.Errorf("the calls page on hold doesn't list the calls waiting:\n%s", page)
	}

	// Taking calls: every phone takes them, and the calls held ask again.
	logMu.Lock()
	logLines, logStart = logLines[:0], 0
	logMu.Unlock()
	if status, to, _ := b.post("/portal/hold", url.Values{"csrf": {csrf}, "hold": {"off"}}); status != http.StatusSeeOther || to != "/portal/#hold" {
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
	if err != nil || view.On || view.Title != "Not on hold" {
		t.Errorf("one phone on hold: %+v, %v", view, err)
	}
}

// With work hours, their start ends a hold, so the words say so, and never "until you take
// calls".
func TestHoldWithWorkHours(t *testing.T) {
	os.Remove(devicesPath)
	defer os.Remove(devicesPath)
	at := func(d time.Duration) string { return now().UTC().Add(d).Format("15:04") }
	hours := func(from, to string) map[string]string {
		return map[string]string{"hours_from": from, "hours_to": to, "time_zone": "UTC"}
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if err := registerDevice("hours-1", "sandbox", "iPhone", "", "no", hours(at(2*time.Hour), at(3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	start, _ := newWorkHours(loadDevices()["hours-1"])
	view := hubHold(loadDevices(), 0)
	want := "On hold until " + ahead(start.start(now())) + ", when your work hours start"
	if !view.On || view.Title != want || strings.Contains(view.Text, "ring when you take calls") {
		t.Errorf("outside work hours: %+v, want %q", view, want)
	}
	if err := registerDevice("hours-1", "sandbox", "iPhone", "", "no", hours(at(-time.Hour), at(time.Hour))); err != nil {
		t.Fatal(err)
	}
	view = hubHold(loadDevices(), 0)
	if view.On || !strings.Contains(view.Text, "until you take them or your work hours next start") || !strings.Contains(view.Text, "Your work hours hold calls at") {
		t.Errorf("inside work hours: %+v", view)
	}
}

// A phone new to the hub while every other is on hold joins the hold, or calls would ring
// again.
func TestNewPhoneJoinsTheHold(t *testing.T) {
	os.Remove(devicesPath)
	defer os.Remove(devicesPath)
	registerPhone(t, "join-1", "iPhone", "")
	stateMu.Lock()
	_, err := holdEveryPhone(true)
	set := loadDevices()["join-1"]["hold_set"]
	stateMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	advance(time.Minute)
	registerPhone(t, "join-2", "iPad", "") // a reinstall, or a second phone
	registerPhone(t, "join-1", "iPhone", "")
	stateMu.Lock()
	held, _ := heldUntil(now())
	joined := loadDevices()["join-2"]
	stateMu.Unlock()
	if !held || joined["hold"] != "yes" || joined["hold_set"] != set {
		t.Errorf("a phone registering while held: held %v, %v", held, joined)
	}
	// While taking calls, a new phone takes them too.
	stateMu.Lock()
	holdEveryPhone(false)
	stateMu.Unlock()
	registerPhone(t, "join-3", "iPad", "")
	stateMu.Lock()
	if loadDevices()["join-3"]["hold"] != "" {
		t.Error("a phone registering while taking calls is on hold")
	}
	// Held only by its work hours, a phone leaves a new one to its own: without hours, it
	// takes calls.
	os.Remove(devicesPath)
	from := now().UTC().Add(2 * time.Hour).Format("15:04")
	to := now().UTC().Add(3 * time.Hour).Format("15:04")
	err = registerDevice("hours-only", "sandbox", "iPhone", "", "", map[string]string{"hours_from": from, "hours_to": to, "time_zone": "UTC"})
	stateMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	registerPhone(t, "join-4", "iPad", "")
	stateMu.Lock()
	defer stateMu.Unlock()
	if held, _ := heldUntil(now()); held || loadDevices()["join-4"]["hold"] != "" {
		t.Errorf("a phone registering while another is held by its hours: held %v, %v", held, loadDevices()["join-4"])
	}
}

// Taking calls on the portal tells the Macs' agents to ask again about their calls held, over
// their links.
func TestTakingCallsAsksAgain(t *testing.T) {
	makeAccount(t)
	advance(5 * time.Minute)
	os.Remove(waitingPath)
	defer os.Remove(waitingPath)
	registerPhone(t, "asks-1", "iPhone", "")
	defer os.Remove(devicesPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent" {
			acceptLink(w, r, "agent", agentOpened, fromAgent, func(*link, []byte) {}, agentClosed)
			return
		}
		portal(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/agent",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer agent-secret-5678"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	hello, _ := json.Marshal(map[string]any{"type": "hello", "version": 1, "name": "studio"})
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	if _, data, err := ws.Read(ctx); err != nil || !strings.Contains(string(data), "welcome") {
		t.Fatalf("the agent's welcome: %s, %v", data, err)
	}
	stateMu.Lock()
	holdEveryPhone(true)
	_, err = holdCall("asks-again-000000001", "parser", "", hubURL(), "", false, "", "")
	stateMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t, server)
	csrf := b.signIn()
	if status, _, _ := b.post("/portal/hold", url.Values{"csrf": {csrf}, "hold": {"off"}}); status != http.StatusSeeOther {
		t.Fatalf("taking calls: %d", status)
	}
	read, cancelRead := context.WithTimeout(ctx, 2*time.Second)
	defer cancelRead()
	for {
		_, data, err := ws.Read(read)
		if err != nil {
			t.Fatalf("the agent heard no reask: %v", err)
		}
		var message map[string]any
		json.Unmarshal(data, &message)
		if message["type"] == "reask" {
			if ids := fmt.Sprint(message["call_ids"]); !strings.Contains(ids, "asks-again-000000001") {
				t.Errorf("the reask: %s", data)
			}
			return
		}
	}
}

// An agent's call that rings gets a reply of its own: the shared ok, which the phone's
// endpoints answer with, isn't written into.
func TestPlaceRepliesWithItsOwnMap(t *testing.T) {
	os.Remove(waitingPath)
	defer os.Remove(waitingPath)
	registerPhone(t, "place-1", "iPhone", "yes") // it speaks itself, so the call rings
	defer os.Remove(devicesPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptLink(w, r, "agent", agentOpened, fromAgent, func(*link, []byte) {}, agentClosed)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/agent",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer agent-secret-5678"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	for _, message := range []map[string]any{
		{"type": "hello", "version": 1, "name": "studio"},
		{"type": "place", "id": 1, "call_id": "place-own-map-000001", "project": "demo"},
	} {
		data, _ := json.Marshal(message)
		if err := ws.Write(ctx, websocket.MessageText, data); err != nil {
			t.Fatal(err)
		}
	}
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("no reply to the place: %v", err)
		}
		if strings.Contains(string(data), `"reply"`) {
			if !strings.Contains(string(data), `"ok":true`) || strings.Contains(string(data), "held") {
				t.Errorf("the reply: %s", data)
			}
			break
		}
	}
	stateMu.Lock()
	shared, _ := json.Marshal(ok)
	stateMu.Unlock()
	if string(shared) != `{"ok":true}` {
		t.Errorf("the phone's endpoints would answer %s", shared)
	}
}

// A device that's null in the file is dropped, not written to; and an endpoint that panics
// leaves the state's lock free.
func TestBrokenStateLeavesTheLockFree(t *testing.T) {
	makeAccount(t)
	advance(5 * time.Minute)
	if err := os.WriteFile(devicesPath, []byte(`{"null-1": null, "fine-1": {"device": "iPhone", "environment": "sandbox"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(devicesPath)
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)
	csrf := b.signIn()
	if status, _, _ := b.post("/portal/hold", url.Values{"csrf": {csrf}, "hold": {"on"}}); status != http.StatusSeeOther {
		t.Fatalf("holding calls with a null device: %d", status)
	}
	if !stateMu.TryLock() {
		t.Fatal("the state's lock is held after the hold")
	}
	stateMu.Unlock()
	func() {
		defer func() { recover() }()
		request := httptest.NewRequest(http.MethodPost, "/broken", strings.NewReader(`{}`))
		answer(map[string]endpoint{"/broken": func(map[string]any) (map[string]any, error) { panic("broken") }}, request)
	}()
	if !stateMu.TryLock() {
		t.Fatal("the state's lock is held after an endpoint panicked")
	}
	stateMu.Unlock()
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

	// A node linked is used for calls; gone, it's offline, a problem, said once, in the
	// headline. A Mac gone is asleep, which is none.
	gpu := &link{kind: "speech", name: "gpu-tower", since: now()}
	registryMu.Lock()
	nodes[gpu] = true
	registryMu.Unlock()
	sawLink(gpu, true)
	seePart(t, "agent", "studio")
	registerPhone(t, "phone-token-1", "iPhone", "no")
	defer os.Remove(devicesPath)
	v = statusData()
	if p := part(v.Wide, "node"); p.State != "working" || p.Sub != "Ready" || p.Tag != "Used for calls" {
		t.Errorf("a linked node: %+v", p)
	}
	registryMu.Lock()
	delete(nodes, gpu)
	registryMu.Unlock()
	sawLink(gpu, false)
	v = statusData()
	if p := part(v.Wide, "node"); p.State != "problem" || p.Wire != "problem" || p.Sub != "Offline" {
		t.Errorf("a node gone: %+v", p)
	}
	if p := part(v.Narrow, "mac"); p.State != "asleep" || p.Wire != "asleep" {
		t.Errorf("a Mac gone: %+v", p)
	}
	// Here Apple push isn't set up, which comes first, so the node is said under it, once.
	said := strings.Count(fmt.Sprint(v.Headline, v.Problems), "gpu-tower is offline")
	if said != 1 || len(v.Problems) != 1 || v.Problems[0].Tone != "bad" || strings.Contains(fmt.Sprint(v.Narrow.Texts, v.Wide.Texts), "calls wait") {
		t.Errorf("a node gone, said %d times: %q, %+v", said, v.Headline, v.Problems)
	}
	if data, err := os.ReadFile(seenPath); err != nil || !strings.Contains(string(data), `"gpu-tower"`) || !strings.Contains(string(data), `"studio"`) {
		t.Fatalf("seen.json: %s, %v", data, err)
	}

	// Forgotten on the status page, unless it's linked again; any name seen, however long.
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
	long := strings.Repeat("a-mac-with-a-long-name-", 20)
	seePart(t, "agent", long)
	for _, form := range []url.Values{{"kind": {"speech"}, "name": {"gpu-tower"}}, {"kind": {"agent"}, "name": {"studio"}}, {"kind": {"agent"}, "name": {long}}} {
		form.Set("csrf", csrf)
		if status, _, _ := b.post("/portal/forget", form); status != http.StatusSeeOther {
			t.Fatalf("forgetting %.40v: %d", form, status)
		}
	}
	if seen := loadSeen(); len(seen["speech"])+len(seen["agent"]) != 0 {
		t.Errorf("seen after forgetting them all: %v", seen)
	}
	if status, _, _ := b.post("/portal/forget", url.Values{"csrf": {csrf}, "kind": {"phone"}, "name": {"x"}}); status != http.StatusBadRequest {
		t.Errorf("forgetting a kind that isn't one: %d", status)
	}
	if p := part(statusData().Wide, "node"); p.State != "unset" {
		t.Errorf("a node forgotten: %+v", p)
	}
}

// After a restart, a part that was linked when the hub stopped is gone since it started, not
// since it last linked.
func TestPartsGoneAtStart(t *testing.T) {
	os.Remove(seenPath)
	defer os.Remove(seenPath)
	linkedAt := now().Add(-48 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	seen := map[string]map[string]seenPart{"speech": {"gpu-tower": {First: linkedAt, Last: linkedAt, Linked: true}},
		"agent": {"studio": {First: linkedAt, Last: linkedAt}}}
	if err := writeJSON(seenPath, seen); err != nil {
		t.Fatal(err)
	}
	partsGoneAtStart()
	after := loadSeen()
	if node := after["speech"]["gpu-tower"]; node.Linked || node.Last != isoNow() {
		t.Errorf("a node linked as the hub stopped: %+v", node)
	}
	if mac := after["agent"]["studio"]; mac.Last != linkedAt {
		t.Errorf("a Mac gone before the hub stopped: %+v", mac)
	}
}

// A call that rings rings every phone; one answered is on whichever answered, which the hub
// can't tell with two phones, so it lights none of them and says so.
func TestLiveCallShowsWhatsKnown(t *testing.T) {
	os.Remove(devicesPath)
	defer os.Remove(devicesPath)
	studio, gpu := &link{kind: "agent", name: "studio", since: now()}, &link{kind: "speech", name: "gpu-tower", since: now()}
	c := &Call{id: "live-call-0000000001", short: "live-cal", agent: studio, node: gpu, offeredAt: now()}
	registryMu.Lock()
	agents[studio], nodes[gpu], live[c.id] = nil, true, c
	registryMu.Unlock()
	defer func() {
		registryMu.Lock()
		delete(agents, studio)
		delete(nodes, gpu)
		delete(live, c.id)
		registryMu.Unlock()
	}()
	phones := func(v statusView) []mapPart {
		var list []mapPart
		for _, p := range v.Wide.Parts {
			if p.Kind == "phone" {
				list = append(list, p)
			}
		}
		return list
	}
	registerPhone(t, "live-1", "iPhone", "")
	registerPhone(t, "live-2", "iPad", "")
	for _, p := range phones(statusData()) {
		if p.Sub != "Ringing" || !p.PushLive || p.WireLive {
			t.Errorf("ringing, two phones: %+v", p)
		}
	}
	c.mu.Lock()
	c.answeredAt, c.connectedAt = now(), now()
	c.mu.Unlock()
	v := statusData()
	for _, p := range phones(v) {
		if p.Live || p.WireLive {
			t.Errorf("answered, two phones, one lit: %+v", p)
		}
	}
	if !strings.Contains(v.Detail, "on one of your phones: the hub can't tell which") {
		t.Errorf("answered, two phones: %q", v.Detail)
	}
	stateMu.Lock()
	forgetDevice("live-2")
	stateMu.Unlock()
	if p := phones(statusData())[0]; p.Sub != "On a call" || !p.WireLive {
		t.Errorf("answered, one phone: %+v", p)
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
		{"no key file", config(filepath.Join(dir, "gone.p8")), sandbox, true, "key is missing"},
		{"not a key", config(bad), sandbox, true, "can't read its Apple push key"},
		{"no production key", config(good), production, true, "no Apple push key for iPhone"},
	} {
		got := checkPush(c.config, c.devices)
		if got.set != c.set || !strings.Contains(got.problem, c.problem) || (c.problem == "") != (got.problem == "") || (got.problem != "") != (got.detail != "") {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

// The map, wide and tall, for 0 to 4 Macs, 0 to 3 speech nodes and 0 to 2 phones, with long
// names: every part inside it; no part's tile, name, line or tag over another's or over a
// caption; and no link through a tile or words but those of the parts it links, which are
// drawn over it. Text widths are estimates: 7.6 px a character for names, 6.4 for the rest.
func TestMapLayout(t *testing.T) {
	long := func(prefix string, i int) string { return fmt.Sprintf("%s-%d-with-a-long-name", prefix, i) }
	type box struct {
		x1, y1, x2, y2 float64
		what, owner    string
	}
	width := func(s string, per float64) float64 { return float64(utf8.RuneCountInString(s)) * per }
	for _, held := range []bool{false, true} {
		for macs := range 5 {
			for nodeCount := range 4 {
				for phones := range 3 {
					m := mapModel{Hub: mapPart{Kind: "hub", Name: "Hub"},
						Push: mapPart{Kind: "push", Name: "Apple push", Sub: "Last push 14:05"}, SpeechNote: "None yet, so calls wait", Held: held}
					if held {
						m.Hub.Tag, m.Hub.TagTone = "On hold", "hold"
					}
					for i := range macs {
						state := "working"
						if i == 3 {
							state = "asleep"
						}
						m.Macs = append(m.Macs, mapPart{Kind: "mac", State: state, Name: long("mac", i), Sub: "3 sessions"})
					}
					if macs == 0 {
						m.Macs = []mapPart{{Kind: "mac", Name: "Mac", Sub: "Not set up", Setup: "mac"}}
					}
					for i := range nodeCount {
						m.Nodes = append(m.Nodes, mapPart{Kind: "node", Name: long("node", i), Sub: "Ready", Tag: "Used for calls", TagTone: "good"})
					}
					if nodeCount == 0 {
						m.Nodes = []mapPart{{Kind: "node", Name: "Speech node", Sub: "Not set up", Setup: "node"}}
					}
					for i := range phones {
						m.Phones = append(m.Phones, mapPart{Kind: "phone", Name: long("iPhone", i), Sub: onDevice + " voice on", Tag: "Used for calls"})
					}
					if phones == 0 {
						m.Phones = []mapPart{{Kind: "phone", Name: "iPhone", Sub: "Not set up", Setup: "phone"}}
					}
					for _, v := range []mapView{m.wide(), m.narrow()} {
						where := fmt.Sprintf("%s, %d Macs, %d nodes, %d phones, held %v", v.Shape, macs, nodeCount, phones, held)
						var boxes []box
						for _, p := range v.Parts {
							w := max(p.S, width(p.Short, 7.6), width(p.Sub, 6.4), p.TagW)
							b := box{p.X - w/2, p.top(), p.X + w/2, p.bottom(), p.Kind + " " + p.Short, p.key()}
							if b.x1 < 0 || b.x2 > v.W || b.y1 < v.Top || b.y2 > v.Top+v.H {
								t.Errorf("%s: %s is outside the map (%v, %v from %v): %+v", where, b.what, v.W, v.H, v.Top, b)
							}
							boxes = append(boxes, b)
						}
						for _, c := range v.Texts {
							w := width(c.Text, 6.6)
							x1 := map[string]float64{"start": c.X, "middle": c.X - w/2, "end": c.X - w}[c.Anchor]
							b := box{x1, c.Y - 12, x1 + w, c.Y + 3, "caption " + c.Text, ""}
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
						var marks []box
						for _, p := range v.Parts {
							name, sub := width(p.Short, 7.6), width(p.Sub, 6.4)
							marks = append(marks, box{p.TX, p.TY, p.TX + p.S, p.TY + p.S, p.Kind + " " + p.Short + "'s tile", p.key()},
								box{p.X - name/2, p.NameY - 12, p.X + name/2, p.NameY + 3, p.Kind + " " + p.Short + "'s name", p.key()})
							if p.Sub != "" {
								marks = append(marks, box{p.X - sub/2, p.SubY - 11, p.X + sub/2, p.SubY + 3, p.Kind + " " + p.Short + "'s line", p.key()})
							}
							if p.Tag != "" {
								marks = append(marks, box{p.TagX, p.TagY, p.TagX + p.TagW, p.TagY + tagHeight, p.Kind + " " + p.Short + "'s tag", p.key()})
							}
						}
						marks = append(marks, boxes[len(v.Parts):]...)
						for _, w := range v.Wires {
							points := sample(w.D)
							for _, pt := range points[1 : len(points)-1] {
								for _, b := range marks {
									own := b.owner != "" && (b.owner == w.ends[0] || b.owner == w.ends[1])
									if !own && pt[0] > b.x1 && pt[0] < b.x2 && pt[1] > b.y1 && pt[1] < b.y2 {
										t.Errorf("%s: a link (%s to %s) runs through %s at %.0f, %.0f", where, w.ends[0], w.ends[1], b.what, pt[0], pt[1])
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
