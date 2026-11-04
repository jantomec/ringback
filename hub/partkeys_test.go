package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Pairing a Mac or a speech node: a code made for its kind makes it a key once, with which it
// links instead of the token, under the name it paired as; pairing again replaces the key, and
// forgetting the part drops it. The portal's Setup page makes the code and the command.

func pairPartWith(code, kind, name string) (string, error) {
	stateMu.Lock()
	defer stateMu.Unlock()
	answer, err := pairPart(map[string]any{"code": code, "kind": kind, "name": name})
	if err != nil {
		return "", err
	}
	return answer["key"].(string), nil
}

func makePartCode(t *testing.T, kind string) string {
	t.Helper()
	stateMu.Lock()
	defer stateMu.Unlock()
	code, _, err := newPairingCode(kind)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// linkWith opens an agent's link to server with secret, saying name in its hello; nil if it's
// refused.
func linkWith(t *testing.T, server *httptest.Server, secret, name string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/agent",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}})
	if err != nil {
		return nil
	}
	hello, _ := json.Marshal(map[string]any{"type": "hello", "version": 1, "name": name})
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	if _, data, err := ws.Read(ctx); err != nil || !strings.Contains(string(data), "welcome") {
		t.Fatalf("no welcome: %s %v", data, err)
	}
	return ws
}

func TestPairPart(t *testing.T) {
	os.Remove(pairingPath)
	os.Remove(partKeysPath)
	defer os.Remove(pairingPath)
	defer os.Remove(partKeysPath)

	forMac := makePartCode(t, "agent")
	if _, err := pairPartWith(forMac, "speech", "gpu-tower"); !errors.Is(err, errPairingCode) {
		t.Errorf("a Mac's code pairs a speech node: %v", err)
	}
	if _, err := pairWith(t, forMac, "part-phone"); !errors.Is(err, errPairingCode) {
		t.Errorf("a Mac's code pairs a phone: %v", err)
	}
	if _, err := pairPartWith(makeCode(t), "agent", "build-mac"); !errors.Is(err, errPairingCode) {
		t.Errorf("a phone's code pairs a Mac: %v", err)
	}
	if _, err := pairPartWith(forMac, "phone", "build-mac"); !errors.Is(err, errBadRequest) {
		t.Errorf("a kind that doesn't pair: %v", err)
	}
	key, err := pairPartWith(forMac, "agent", "build-mac")
	if err != nil || len(key) != 64 {
		t.Fatalf("the Mac's code, after the others: %q %v", key, err)
	}
	if _, err := pairPartWith(forMac, "agent", "laptop"); !errors.Is(err, errPairingCode) {
		t.Errorf("the same code again: %v", err)
	}
	if info, err := os.Stat(partKeysPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the keys: %v, %v", info, err)
	}
	if data, _ := os.ReadFile(partKeysPath); strings.Contains(string(data), key) {
		t.Errorf("the key itself is kept: %s", data)
	}
	if name, ok := pairedPart("agent", key); !ok || name != "build-mac" {
		t.Errorf("the key names its Mac: %q %v", name, ok)
	}
	if _, ok := pairedPart("speech", key); ok {
		t.Error("a Mac's key links a speech node")
	}

	// It links with its key, under the name it paired as, and with no tokens in the config.
	before, _ := os.ReadFile(configPath)
	defer os.WriteFile(configPath, before, 0o600)
	os.WriteFile(configPath, []byte("listen = \"127.0.0.1:18899\"\n"), 0o600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptLink(w, r, "agent", func(*link) {}, func(*link, map[string]any) {}, func(*link, []byte) {}, func(*link) {})
	}))
	defer server.Close()
	if linkWith(t, server, "agent-secret-5678", "build-mac") != nil {
		t.Error("a token the config no longer has links")
	}
	ws := linkWith(t, server, key, "someone-else")
	if ws == nil {
		t.Fatal("its key doesn't link")
	}
	ws.CloseNow()
	if tail := strings.Join(logTail(), "\n"); !strings.Contains(tail, "agent build-mac: connected") || strings.Contains(tail, "someone-else") {
		t.Errorf("not under the name it paired as:\n%s", tail)
	}

	again, err := pairPartWith(makePartCode(t, "agent"), "agent", "build-mac")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pairedPart("agent", key); ok {
		t.Error("paired again, the key before still works")
	}
	if !strings.HasSuffix(logTail()[len(logTail())-1], "the Mac build-mac: paired again, its key before no longer works") {
		t.Errorf("pairing again isn't logged: %s", logTail()[len(logTail())-1])
	}
	if _, err := forgetPart("agent", "build-mac"); err != nil {
		t.Fatal(err)
	}
	if _, ok := pairedPart("agent", again); ok {
		t.Error("forgotten, its key still works")
	}
}

var partCommandLine = regexp.MustCompile(`~/\.local/bin/ringback-agent pair http://127\.0\.0\.1:18899 ([A-Za-z0-9_-]{43})`)

func TestPortalPairPart(t *testing.T) {
	makeAccount(t)
	os.Remove(pairingPath)
	os.Remove(partKeysPath)
	defer os.Remove(pairingPath)
	defer os.Remove(partKeysPath)
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)
	b.signIn()
	_, _, page := b.get("/portal/setup")
	if strings.Count(page, `action="/portal/pair-part"`) != 2 || partCommandLine.MatchString(page) {
		t.Fatalf("the setup page, before the buttons:\n%s", page)
	}
	csrf := csrfOf(t, page)
	if status, _, _ := b.post("/portal/pair-part", url.Values{"kind": {"agent"}}); status != http.StatusForbidden {
		t.Errorf("the button without its CSRF token: %d", status)
	}
	if status, _, _ := b.post("/portal/pair-part", url.Values{"csrf": {csrf}, "kind": {"phone"}}); status != http.StatusBadRequest {
		t.Errorf("a kind that doesn't pair: %d", status)
	}
	status, _, page := b.post("/portal/pair-part", url.Values{"csrf": {csrf}, "kind": {"agent"}})
	m := partCommandLine.FindStringSubmatch(page)
	if status != http.StatusOK || m == nil || strings.Count(page, `action="/portal/pair-part"`) != 1 ||
		!strings.Contains(page, "ringback-hub pair mac</code> makes one too") {
		t.Fatalf("the Mac's button: %d\n%s", status, page)
	}
	if _, err := pairPartWith(m[1], "agent", "build-mac"); err != nil {
		t.Errorf("the portal's code: %v", err)
	}
}

// A part linked with its own key can be unpaired from the portal: its link closes, its key
// stops working, and it isn't left among the parts seen. One linked with the hub's token can't.
func TestUnpairLinked(t *testing.T) {
	for _, path := range []string{pairingPath, partKeysPath, seenPath} {
		os.Remove(path)
		defer os.Remove(path)
	}
	key, err := pairPartWith(makePartCode(t, "agent"), "agent", "unpair-mac")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptLink(w, r, "agent", agentOpened, fromAgent, func(*link, []byte) {}, agentClosed)
	}))
	defer server.Close()
	linkedNames := func() map[string]bool {
		registryMu.Lock()
		defer registryMu.Unlock()
		names := map[string]bool{}
		for l := range agents {
			names[l.name] = true
		}
		return names
	}
	paired := linkWith(t, server, key, "unpair-mac")
	defer paired.CloseNow()
	shared := linkWith(t, server, "agent-secret-5678", "token-mac")
	defer shared.CloseNow()
	for end := time.Now().Add(2 * time.Second); len(linkedNames()) < 2 && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
	}
	macs := map[string]partView{}
	for _, p := range statusData().Macs {
		macs[p.Name] = p
	}
	if p := macs["unpair-mac"]; !p.Paired || !strings.HasSuffix(p.Status, ", paired") {
		t.Errorf("the paired Mac on the status page: %+v", p)
	}
	if p := macs["token-mac"]; p.Paired || !strings.HasSuffix(p.Status, ", with the hub's token") {
		t.Errorf("the Mac with the token: %+v", p)
	}
	var rendered bytes.Buffer
	if err := portalPages["status"].ExecuteTemplate(&rendered, "base", page{CSRF: "c", Data: statusData(), Hold: currentHold()}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(rendered.String(), "Unpair</button>") != 1 || !strings.Contains(rendered.String(), `name="name" value="unpair-mac"`) {
		t.Errorf("the status page doesn't offer to unpair the paired Mac alone:\n%s", rendered.String())
	}

	if forgot, _ := forgetPart("agent", "token-mac"); forgot || !linkedNames()["token-mac"] {
		t.Error("a Mac linked with the token is unpaired")
	}
	if forgot, err := forgetPart("agent", "unpair-mac"); !forgot || err != nil {
		t.Fatalf("the paired Mac isn't unpaired: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := paired.Read(ctx); err == nil || ctx.Err() != nil {
		t.Errorf("its link isn't closed: %v", err)
	}
	if _, ok := pairedPart("agent", key); ok {
		t.Error("its key still works")
	}
	for end := time.Now().Add(2 * time.Second); linkedNames()["unpair-mac"] && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
	}
	time.Sleep(200 * time.Millisecond) // a link that goes is noted among the parts seen after that
	seenMu.Lock()
	seen := loadSeen()
	seenMu.Unlock()
	if _, left := seen["agent"]["unpair-mac"]; left || !linkedNames()["token-mac"] {
		t.Errorf("after unpairing, seen: %v, linked: %v", seen["agent"], linkedNames())
	}
}
