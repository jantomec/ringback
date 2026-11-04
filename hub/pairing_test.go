package main

import (
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Pairing: a code makes a phone a key once, and expires; the phone's endpoints need the key; a
// key moves a phone to a new push token; only paired phones count; and the portal's pairing
// page, whose login comes back to it and nowhere else.

func pairWith(t *testing.T, code, token string) (string, error) {
	t.Helper()
	stateMu.Lock()
	defer stateMu.Unlock()
	answer, err := pair(map[string]any{"code": code, "token": token, "environment": "sandbox", "device": "iPhone"})
	if err != nil {
		return "", err
	}
	return answer["key"].(string), nil
}

func makeCode(t *testing.T) string {
	t.Helper()
	stateMu.Lock()
	defer stateMu.Unlock()
	code, _, err := newPairingCode("")
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestPairing(t *testing.T) {
	os.Remove(devicesPath)
	os.Remove(pairingPath)
	defer os.Remove(devicesPath)
	defer os.Remove(pairingPath)
	code := makeCode(t)
	if info, err := os.Stat(pairingPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the pending codes: %v, %v", info, err)
	}
	if data, _ := os.ReadFile(pairingPath); strings.Contains(string(data), code) {
		t.Errorf("the code itself is kept: %s", data)
	}
	key, err := pairWith(t, code, "pair-token-1")
	if err != nil || len(key) != 64 {
		t.Fatalf("a good code: %q, %v", key, err)
	}
	stateMu.Lock()
	device := loadDevices()["pair-token-1"]
	stateMu.Unlock()
	if device["key"] != hashOf(key) || device["device"] != "iPhone" {
		t.Errorf("the phone as kept: %v", device)
	}
	if data, _ := os.ReadFile(devicesPath); strings.Contains(string(data), key) {
		t.Errorf("the key itself is kept: %s", data)
	}
	if _, err := pairWith(t, code, "pair-token-2"); !errors.Is(err, errPairingCode) {
		t.Errorf("the same code again: %v", err)
	}
	if _, err := pairWith(t, "not-a-code", "pair-token-2"); !errors.Is(err, errPairingCode) {
		t.Errorf("a made-up code: %v", err)
	}
	expiring := makeCode(t)
	saved := now()
	advance(pairingValid + time.Second)
	_, err = pairWith(t, expiring, "pair-token-2")
	setClock(saved)
	if !errors.Is(err, errPairingCode) {
		t.Errorf("an expired code: %v", err)
	}
}

func TestPhoneEndpointsNeedTheKey(t *testing.T) {
	os.Remove(devicesPath)
	defer os.Remove(devicesPath)
	defer os.Remove(pairingPath)
	key, err := pairWith(t, makeCode(t), "key-token-1")
	if err != nil {
		t.Fatal(err)
	}
	endpoints := map[string]endpoint{
		"/register": register, "/check": checkIn, "/waiting": waiting, "/contacts": contacts, "/hold": holdSwitch, "/pair": pair,
	}
	post := func(path string, body string) (int, string) {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		return answer(endpoints, r)
	}
	for _, path := range []string{"/register", "/check", "/waiting", "/contacts", "/hold"} {
		for _, body := range []string{`{"token": "key-token-1"}`, `{"token": "key-token-1", "key": "` + strings.Repeat("0", 64) + `"}`} {
			if status, answered := post(path, body); status != http.StatusUnauthorized || !strings.Contains(answered, `"not paired"`) {
				t.Errorf("%s with %s: %d %s", path, body, status, answered)
			}
		}
		if status, answered := post(path, `{"token": "key-token-1", "key": "`+key+`"}`); status != http.StatusOK {
			t.Errorf("%s with its key: %d %s", path, status, answered)
		}
	}
	if status, _ := post("/check", `{}`); status != http.StatusOK {
		t.Errorf("a check without a token, which only says the hub was reached: %d", status)
	}
	if status, answered := post("/pair", `{"code": "nope", "token": "key-token-2"}`); status != http.StatusForbidden ||
		!strings.Contains(answered, `"pairing code not valid"`) {
		t.Errorf("/pair with a bad code: %d %s", status, answered)
	}

	// APNs gave the phone a new push token: its key moves it there.
	if status, answered := post("/register", `{"token": "key-token-new", "key": "`+key+`"}`); status != http.StatusOK {
		t.Fatalf("a new push token with the key: %d %s", status, answered)
	}
	stateMu.Lock()
	devices := loadDevices()
	stateMu.Unlock()
	if _, old := devices["key-token-1"]; old || devices["key-token-new"]["key"] != hashOf(key) {
		t.Errorf("after the new token: %v", devices)
	}
}

func TestOnlyPairedPhonesCount(t *testing.T) {
	os.Remove(devicesPath)
	defer os.Remove(devicesPath)
	stateMu.Lock()
	defer stateMu.Unlock()
	if err := registerDevice("old-phone", "sandbox", "Old iPhone", "note", "yes", nil); err != nil {
		t.Fatal(err)
	}
	if paired := pairedDevices(); len(paired) != 0 {
		t.Errorf("a phone registered without pairing counts: %v", paired)
	}
	if phoneSpeaks() {
		t.Error("an unpaired phone's own voice counts")
	}
	if n, _ := holdEveryPhone(true); n != 0 {
		t.Errorf("held %d phones, with none paired", n)
	}
	if held, _ := heldUntil(now()); held {
		t.Error("an unpaired phone's hold holds calls")
	}
}

var pairLink = regexp.MustCompile(`ringback://pair\?hub=[^"&]+&(?:amp;)?code=([A-Za-z0-9_-]+)`)

func TestPortalPairing(t *testing.T) {
	makeAccount(t)
	os.Remove(devicesPath)
	defer os.Remove(devicesPath)
	defer os.Remove(pairingPath)
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)

	// Signed out, the page sends you to sign in, which comes back to it.
	status, to, _ := b.get("/portal/pair")
	if status != http.StatusSeeOther || to != "/portal/login?next=%2Fportal%2Fpair" {
		t.Fatalf("signed out: %d → %q", status, to)
	}
	_, _, page := b.get(to)
	if !strings.Contains(page, `name="next" value="/portal/pair"`) {
		t.Fatalf("the login form doesn't carry where to go back to:\n%s", page)
	}
	status, to, _ = b.post("/portal/login", url.Values{"csrf": {csrfOf(t, page)}, "name": {"ana"}, "password": {testPassword}, "next": {"/portal/pair"}})
	if status != http.StatusSeeOther || to != "/portal/code" {
		t.Fatalf("password: %d → %q", status, to)
	}
	_, _, page = b.get("/portal/code")
	status, to, _ = b.post("/portal/code", url.Values{"csrf": {csrfOf(t, page)}, "code": {codeNow(t, 0)}})
	if status != http.StatusSeeOther || to != "/portal/pair" {
		t.Fatalf("passcode, then back to pairing: %d → %q", status, to)
	}

	// Signed in: one button, which needs the form's token.
	_, _, page = b.get("/portal/pair")
	if !strings.Contains(page, `action="/portal/pair"`) || !strings.Contains(page, "Pair this phone</button>") || pairLink.MatchString(page) {
		t.Fatalf("the pairing page, before the button:\n%s", page)
	}
	csrf := csrfOf(t, page)
	if status, _, _ := b.post("/portal/pair", url.Values{}); status != http.StatusForbidden {
		t.Errorf("the button without its CSRF token: %d", status)
	}
	status, _, page = b.post("/portal/pair", url.Values{"csrf": {csrf}})
	m := pairLink.FindStringSubmatch(page)
	if status != http.StatusOK || m == nil || !strings.Contains(page, `http-equiv="refresh"`) || !strings.Contains(page, "Open Ringback") {
		t.Fatalf("the button: %d\n%s", status, page)
	}
	link, _ := url.Parse(html.UnescapeString(pairLink.FindString(page)))
	if link.Query().Get("hub") != "http://127.0.0.1:18899" {
		t.Errorf("the link's hub: %q", link.Query().Get("hub"))
	}
	if _, err := pairWith(t, m[1], "portal-phone"); err != nil {
		t.Errorf("the portal's code: %v", err)
	}
}

func TestLoginGoesBackOnlyToThePortal(t *testing.T) {
	for next, want := range map[string]string{
		"/portal/pair": "/portal/pair", "/portal/": "/portal/", "/portal/setup": "/portal/setup",
		"https://evil.example/": "", "//evil.example/portal/": "", "/portal//evil.example": "", "/portal/\\evil": "",
		"/portal/../evil": "", "/elsewhere": "", "portal/pair": "", "/portal/pair?x=1": "", "javascript:alert(1)": "",
		"/portal/%2F%2Fevil": "",
	} {
		if got := portalPath(next); got != want {
			t.Errorf("portalPath(%q) = %q, want %q", next, got, want)
		}
	}

	makeAccount(t)
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)
	_, _, page := b.get("/portal/login?next=" + url.QueryEscape("https://evil.example/"))
	if strings.Contains(page, `name="next"`) {
		t.Errorf("the login form carries an outside address:\n%s", page)
	}
	status, to, _ := b.post("/portal/login", url.Values{"csrf": {csrfOf(t, page)}, "name": {"ana"}, "password": {testPassword}, "next": {"//evil.example/"}})
	if status != http.StatusSeeOther || to != "/portal/code" {
		t.Fatalf("password: %d → %q", status, to)
	}
	_, _, page = b.get("/portal/code")
	if status, to, _ := b.post("/portal/code", url.Values{"csrf": {csrfOf(t, page)}, "code": {codeNow(t, 0)}}); status != http.StatusSeeOther || to != "/portal/" {
		t.Errorf("after the passcode, with an outside address to go back to: %d → %q", status, to)
	}
}
