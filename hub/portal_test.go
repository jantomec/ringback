package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The portal's checks: passcodes against RFC 6238's vectors, passwords, and signing in and
// out through its pages, with CSRF tokens, the rate limit and sessions that expire. The hub's
// state and config are in a throwaway folder, and its clock is the tests'.

var clockNanos atomic.Int64

func setClock(t time.Time)    { clockNanos.Store(t.UnixNano()) }
func advance(d time.Duration) { clockNanos.Add(int64(d)) }

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ringback-portal-test-")
	if err != nil {
		panic(err)
	}
	stateDir = filepath.Join(dir, "state")
	devicesPath = filepath.Join(stateDir, "devices.json")
	waitingPath = filepath.Join(stateDir, "waiting.json")
	contactsPath = filepath.Join(stateDir, "contacts.json")
	eventsPath = filepath.Join(stateDir, "events.jsonl")
	accountPath = filepath.Join(stateDir, "portal-user.json")
	seenPath = filepath.Join(stateDir, "seen.json")
	pairingPath = filepath.Join(stateDir, "pairing.json")
	configPath = filepath.Join(dir, "hub.toml")
	config := "listen = \"127.0.0.1:18899\"\n[tokens]\nspeech = \"speech-secret-1234\"\nagent = \"agent-secret-5678\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		panic(err)
	}
	setClock(time.Date(2026, 11, 2, 12, 0, 10, 0, time.UTC))
	now = func() time.Time { return time.Unix(0, clockNanos.Load()) }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestTOTPMatchesRFC6238(t *testing.T) {
	// RFC 6238, Appendix B: SHA-1, the seed "12345678901234567890", 8 digits, 30 s steps.
	secret := []byte("12345678901234567890")
	for _, c := range []struct {
		at   int64
		code string
	}{
		{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"},
		{1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"},
	} {
		if got := totp(secret, c.at/30, 8); got != c.code {
			t.Errorf("at %d: %s, want %s", c.at, got, c.code)
		}
		if got := totp(secret, c.at/30, 6); got != c.code[2:] {
			t.Errorf("at %d, 6 digits: %s, want %s", c.at, got, c.code[2:])
		}
	}
}

func TestPasswordHashing(t *testing.T) {
	a := newAccount("ana", "correct horse battery")
	if a.Iterations < 600_000 {
		t.Errorf("%d iterations, want at least 600,000", a.Iterations)
	}
	if salt, err := base64.StdEncoding.DecodeString(a.Salt); err != nil || len(salt) < 16 {
		t.Errorf("salt %q: %v", a.Salt, err)
	}
	if !a.checkPassword("ana", "correct horse battery") {
		t.Error("the right name and password are refused")
	}
	for _, wrong := range [][2]string{{"ana", "correct horse batterY"}, {"Ana", "correct horse battery"}, {"", ""}} {
		if a.checkPassword(wrong[0], wrong[1]) {
			t.Errorf("%q, %q accepted", wrong[0], wrong[1])
		}
	}
	b := newAccount("ana", "correct horse battery")
	if a.Salt == b.Salt || a.Hash == b.Hash || a.TOTPSecret == b.TOTPSecret {
		t.Error("two accounts with one password share a salt, hash or secret")
	}
	if !strings.HasPrefix(otpauthURI(a), "otpauth://totp/ringback%20hub:ana?") || !strings.Contains(otpauthURI(a), "secret="+a.TOTPSecret) {
		t.Errorf("otpauth URI: %s", otpauthURI(a))
	}
}

const testPassword = "a long test password"

// makeAccount writes an account and forgets every session and failed login.
func makeAccount(t *testing.T) *portalAccount {
	t.Helper()
	a := writeAccount(t)
	portalMu.Lock()
	portalSessions, pendingLogins, failures = map[string]*portalSession{}, map[string]*portalSession{}, map[string]*loginFailures{}
	portalMu.Unlock()
	return a
}

// writeAccount writes an account, hashed with fewer iterations so the tests run quickly.
func writeAccount(t *testing.T) *portalAccount {
	t.Helper()
	a := newAccount("ana", "unused")
	salt, _ := base64.StdEncoding.DecodeString(a.Salt)
	a.Iterations = 1000
	a.Hash = base64.StdEncoding.EncodeToString(hashPassword(testPassword, salt, a.Iterations))
	if err := saveAccount(a); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(accountPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the account's file: %v, %v", info.Mode(), err)
	}
	return a
}

// codeNow is the account's passcode at the tests' time, offset by steps.
func codeNow(t *testing.T, steps int64) string {
	t.Helper()
	secret, err := b32.DecodeString(loadAccount().TOTPSecret)
	if err != nil {
		t.Fatal(err)
	}
	return totp(secret, now().Unix()/totpPeriod+steps, totpDigits)
}

type browser struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newBrowser(t *testing.T, server *httptest.Server) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t, server.URL, &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

func (b *browser) get(path string, header ...string) (int, string, string) {
	b.t.Helper()
	request, _ := http.NewRequest(http.MethodGet, b.base+path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		request.Header.Set(header[i], header[i+1])
	}
	return b.do(request)
}

func (b *browser) post(path string, form url.Values) (int, string, string) {
	b.t.Helper()
	request, _ := http.NewRequest(http.MethodPost, b.base+path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return b.do(request)
}

// do returns the status, where it redirects to, and the body.
func (b *browser) do(request *http.Request) (int, string, string) {
	b.t.Helper()
	response, err := b.client.Do(request)
	if err != nil {
		b.t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, response.Header.Get("Location"), string(body)
}

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func csrfOf(t *testing.T, body string) string {
	t.Helper()
	m := csrfField.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF token in the page:\n%s", body)
	}
	return m[1]
}

// signIn signs in through the pages, and returns the session's CSRF token.
func (b *browser) signIn() string {
	b.t.Helper()
	_, _, page := b.get("/portal/login")
	status, to, _ := b.post("/portal/login", url.Values{"csrf": {csrfOf(b.t, page)}, "name": {"ana"}, "password": {testPassword}})
	if status != http.StatusSeeOther || to != "/portal/code" {
		b.t.Fatalf("password: %d → %q", status, to)
	}
	_, _, page = b.get("/portal/code")
	status, to, _ = b.post("/portal/code", url.Values{"csrf": {csrfOf(b.t, page)}, "code": {codeNow(b.t, 0)}})
	if status != http.StatusSeeOther || to != "/portal/" {
		b.t.Fatalf("passcode: %d → %q", status, to)
	}
	_, _, page = b.get("/portal/")
	return csrfOf(b.t, page)
}

func TestLoginFlow(t *testing.T) {
	makeAccount(t)
	advance(5 * time.Minute)
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)

	if status, to, _ := b.get("/portal/"); status != http.StatusSeeOther || to != "/portal/login" {
		t.Fatalf("without a session: %d → %q", status, to)
	}
	_, _, page := b.get("/portal/login")
	csrf := csrfOf(t, page)

	// A wrong password, then the right one.
	status, to, body := b.post("/portal/login", url.Values{"csrf": {csrf}, "name": {"ana"}, "password": {"not it"}})
	if status != http.StatusUnauthorized || to != "" || !strings.Contains(body, "That name and password") {
		t.Fatalf("a wrong password: %d → %q", status, to)
	}
	if status, to, _ := b.get("/portal/code"); status != http.StatusSeeOther || to != "/portal/login" {
		t.Fatalf("the passcode page after a wrong password: %d → %q", status, to)
	}
	status, to, _ = b.post("/portal/login", url.Values{"csrf": {csrfOf(t, body)}, "name": {"ana"}, "password": {testPassword}})
	if status != http.StatusSeeOther || to != "/portal/code" {
		t.Fatalf("the right password: %d → %q", status, to)
	}
	if status, to, _ := b.get("/portal/"); status != http.StatusSeeOther || to != "/portal/login" {
		t.Fatalf("after the password alone: %d → %q", status, to)
	}

	// A wrong passcode, then the right one.
	_, _, page = b.get("/portal/code")
	csrf = csrfOf(t, page)
	wrong := fmt.Sprintf("%06d", (atoi(codeNow(t, 0))+1)%1_000_000)
	if status, _, _ := b.post("/portal/code", url.Values{"csrf": {csrf}, "code": {wrong}}); status != http.StatusUnauthorized {
		t.Fatalf("a wrong passcode: %d", status)
	}
	code := codeNow(t, 0)
	status, to, _ = b.post("/portal/code", url.Values{"csrf": {csrf}, "code": {code[:3] + " " + code[3:]}})
	if status != http.StatusSeeOther || to != "/portal/" {
		t.Fatalf("the right passcode: %d → %q", status, to)
	}
	status, _, page = b.get("/portal/")
	if status != http.StatusOK || !strings.Contains(page, "Speech nodes") {
		t.Fatalf("the status page: %d\n%s", status, page)
	}
	if status, to, _ := b.get("/portal/login"); status != http.StatusSeeOther || to != "/portal/" {
		t.Fatalf("the login page, signed in: %d → %q", status, to)
	}

	// The same code again, from another browser, is refused, though the password is right;
	// the next step's code works.
	other := newBrowser(t, server)
	_, _, page = other.get("/portal/login")
	other.post("/portal/login", url.Values{"csrf": {csrfOf(t, page)}, "name": {"ana"}, "password": {testPassword}})
	_, _, page = other.get("/portal/code")
	csrf = csrfOf(t, page)
	if status, _, _ := other.post("/portal/code", url.Values{"csrf": {csrf}, "code": {code}}); status != http.StatusUnauthorized {
		t.Fatalf("a passcode used before: %d", status)
	}
	if status, _, _ := other.post("/portal/code", url.Values{"csrf": {csrf}, "code": {codeNow(t, -1)}}); status != http.StatusUnauthorized {
		t.Fatalf("an older passcode: %d", status)
	}
	advance(totpPeriod * time.Second)
	if status, to, _ := other.post("/portal/code", url.Values{"csrf": {csrf}, "code": {codeNow(t, 0)}}); status != http.StatusSeeOther || to != "/portal/" {
		t.Fatalf("the next passcode: %d → %q", status, to)
	}

	// Signing out ends the session; the other browser's goes on.
	_, _, page = b.get("/portal/")
	if status, to, _ := b.post("/portal/logout", url.Values{"csrf": {csrfOf(t, page)}}); status != http.StatusSeeOther || to != "/portal/login" {
		t.Fatalf("signing out: %d → %q", status, to)
	}
	if status, to, _ := b.get("/portal/"); status != http.StatusSeeOther || to != "/portal/login" {
		t.Fatalf("after signing out: %d → %q", status, to)
	}
	if status, _, _ := other.get("/portal/"); status != http.StatusOK {
		t.Fatalf("the other browser after the first signed out: %d", status)
	}
}

func atoi(s string) int {
	n := 0
	fmt.Sscan(s, &n)
	return n
}

func TestCSRFRefused(t *testing.T) {
	makeAccount(t)
	advance(5 * time.Minute)
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)

	// The login form: without its token, or with another, nothing is checked.
	b.get("/portal/login")
	for _, token := range []string{"", "forged"} {
		status, to, _ := b.post("/portal/login", url.Values{"csrf": {token}, "name": {"ana"}, "password": {testPassword}})
		if status != http.StatusForbidden || to != "" {
			t.Fatalf("a login with token %q: %d → %q", token, status, to)
		}
	}

	// Signed in: signing out, holding calls and forgetting a part need the session's token.
	registerPhone(t, "phone-token-1", "iPhone", "")
	defer os.Remove(devicesPath)
	seePart(t, "speech", "gpu-tower")
	defer os.Remove(seenPath)
	csrf := b.signIn()
	for _, token := range []string{"", "forged", csrf[:len(csrf)-1]} {
		if status, _, _ := b.post("/portal/logout", url.Values{"csrf": {token}}); status != http.StatusForbidden {
			t.Fatalf("signing out with token %q: %d", token, status)
		}
		if status, _, _ := b.post("/portal/hold", url.Values{"csrf": {token}, "hold": {"on"}}); status != http.StatusForbidden {
			t.Fatalf("holding calls with token %q: %d", token, status)
		}
		if status, _, _ := b.post("/portal/forget", url.Values{"csrf": {token}, "kind": {"speech"}, "name": {"gpu-tower"}}); status != http.StatusForbidden {
			t.Fatalf("forgetting a node with token %q: %d", token, status)
		}
	}
	if held, _ := heldUntil(now()); held {
		t.Fatal("a forged form held calls")
	}
	if _, kept := loadSeen()["speech"]["gpu-tower"]; !kept {
		t.Fatal("a forged form forgot a node")
	}
	if status, _, _ := b.get("/portal/"); status != http.StatusOK {
		t.Fatalf("a forged sign-out signed out: %d", status)
	}
	// Another session's token isn't this one's.
	other := newBrowser(t, server)
	advance(totpPeriod * time.Second)
	otherCSRF := other.signIn()
	if status, _, _ := b.post("/portal/logout", url.Values{"csrf": {otherCSRF}}); status != http.StatusForbidden {
		t.Fatalf("signing out with another session's token: %d", status)
	}
}

func TestRateLimit(t *testing.T) {
	makeAccount(t)
	advance(5 * time.Minute)
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)
	_, _, page := b.get("/portal/login")
	csrf := csrfOf(t, page)
	logMu.Lock()
	logLines, logStart = logLines[:0], 0 // only this test's lines
	logMu.Unlock()
	for i := range maxFailures {
		if status, _, _ := b.post("/portal/login", url.Values{"csrf": {csrf}, "name": {"ana"}, "password": {"guess " + fmt.Sprint(i)}}); status != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i+1, status)
		}
	}
	// Refused for a minute, even with the right password.
	status, _, body := b.post("/portal/login", url.Values{"csrf": {csrf}, "name": {"ana"}, "password": {testPassword}})
	if status != http.StatusTooManyRequests || !strings.Contains(body, "Try again in a minute") {
		t.Fatalf("after %d failures: %d", maxFailures, status)
	}
	advance(59 * time.Second)
	if status, _, _ := b.post("/portal/login", url.Values{"csrf": {csrf}, "name": {"ana"}, "password": {testPassword}}); status != http.StatusTooManyRequests {
		t.Fatalf("59 s later: %d", status)
	}
	advance(2 * time.Second)
	if status, to, _ := b.post("/portal/login", url.Values{"csrf": {csrf}, "name": {"ana"}, "password": {testPassword}}); status != http.StatusSeeOther || to != "/portal/code" {
		t.Fatalf("a minute later: %d → %q", status, to)
	}
	// Wrong passcodes count too.
	_, _, page = b.get("/portal/code")
	codeCSRF := csrfOf(t, page)
	for range maxFailures {
		b.post("/portal/code", url.Values{"csrf": {codeCSRF}, "code": {"000000"}})
	}
	if status, _, _ := b.post("/portal/code", url.Values{"csrf": {codeCSRF}, "code": {codeNow(t, 0)}}); status != http.StatusTooManyRequests {
		t.Fatalf("the right passcode after %d wrong ones: %d", maxFailures, status)
	}
	// The failures are logged.
	logged := strings.Join(logTail(), "\n")
	if strings.Count(logged, "portal: a failed login from 127.0.0.1") != 2*maxFailures || strings.Count(logged, "is refused for a minute") != 2 {
		t.Fatalf("the log:\n%s", logged)
	}
}

func TestPagesNeedASession(t *testing.T) {
	makeAccount(t)
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)
	pages := []string{"/portal/", "/portal/calls", "/portal/log", "/portal/setup", "/portal/pair", "/portal/nope"}
	for _, path := range pages {
		status, to, body := b.get(path)
		// Signed in, the login comes back to the page asked for.
		want := "/portal/login?next=" + url.QueryEscape(path)
		if path == "/portal/" {
			want = "/portal/login"
		}
		if status != http.StatusSeeOther || to != want || strings.Contains(body, "secret") {
			t.Errorf("GET %s: %d → %q", path, status, to)
		}
		if status, _, _ := b.get(path, refreshHeader, "1"); status != http.StatusUnauthorized {
			t.Errorf("GET %s, a refresh: %d", path, status)
		}
	}
	registerPhone(t, "phone-token-1", "iPhone", "")
	defer os.Remove(devicesPath)
	seePart(t, "agent", "studio")
	defer os.Remove(seenPath)
	for _, path := range []string{"/portal/logout", "/portal/hold", "/portal/forget"} {
		form := url.Values{"hold": {"on"}, "kind": {"agent"}, "name": {"studio"}}
		if status, to, _ := b.post(path, form); status != http.StatusSeeOther || to != "/portal/login" {
			t.Errorf("POST %s: %d → %q", path, status, to)
		}
	}
	if held, _ := heldUntil(now()); held {
		t.Error("holding calls without a session held them")
	}
	if _, kept := loadSeen()["agent"]["studio"]; !kept {
		t.Error("forgetting a Mac without a session forgot it")
	}
	// The stylesheet and script are for anyone; the setup page with its tokens only signed in.
	if status, _, body := b.get("/portal/static/portal.css"); status != http.StatusOK || !strings.Contains(body, "prefers-color-scheme: dark") {
		t.Errorf("the stylesheet: %d", status)
	}
	if status, _, _ := b.get("/portal/static/"); status != http.StatusNotFound {
		t.Errorf("the static folder: %d", status)
	}

	// Signed in, every page shows; the setup page has the tokens, hidden until shown.
	advance(5 * time.Minute)
	b.signIn()
	for _, path := range pages[:4] {
		if status, _, _ := b.get(path); status != http.StatusOK {
			t.Errorf("GET %s signed in: %d", path, status)
		}
	}
	if _, _, body := b.get("/portal/setup"); !strings.Contains(body, `data-secret="speech-secret-1234"`) || !strings.Contains(body, "ws://127.0.0.1:18899/speech") {
		t.Errorf("the setup page:\n%s", body)
	}

	// 12 h of no use ends the session; the pages' own refreshes don't count as use.
	advance(11 * time.Hour)
	if status, _, _ := b.get("/portal/", refreshHeader, "1"); status != http.StatusOK {
		t.Fatalf("a refresh after 11 h: %d", status)
	}
	advance(time.Hour + time.Second)
	if status, to, _ := b.get("/portal/"); status != http.StatusSeeOther || to != "/portal/login" {
		t.Fatalf("after 12 h without use: %d → %q", status, to)
	}

	// A new account ends the sessions of the one before, of the same name too.
	advance(time.Minute)
	b.signIn()
	if status, _, _ := b.get("/portal/"); status != http.StatusOK {
		t.Fatalf("signed in again: %d", status)
	}
	writeAccount(t)
	if status, to, _ := b.get("/portal/"); status != http.StatusSeeOther || to != "/portal/login" {
		t.Fatalf("after a new account: %d → %q", status, to)
	}
}

func TestNoAccount(t *testing.T) {
	os.Remove(accountPath)
	defer makeAccount(t)
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)
	for _, path := range []string{"/portal/", "/portal/login", "/portal/setup"} {
		status, _, body := b.get(path)
		if status != http.StatusOK || !strings.Contains(body, "ringback-hub portal-user NAME") || strings.Contains(body, "secret-") {
			t.Errorf("GET %s with no account: %d\n%s", path, status, body)
		}
	}
	if status, to, _ := b.post("/portal/login", url.Values{"name": {"ana"}, "password": {testPassword}}); status != http.StatusSeeOther || to != "/portal/" {
		t.Errorf("signing in with no account: %d → %q", status, to)
	}
}

func TestLogKeepsItsTail(t *testing.T) {
	for i := range logKept + 100 {
		log(fmt.Sprintf("line %d", i))
	}
	tail := logTail()
	if len(tail) != logKept || !strings.HasSuffix(tail[0], "line 100") || !strings.HasSuffix(tail[logKept-1], fmt.Sprintf("line %d", logKept+99)) {
		t.Fatalf("%d lines, from %q to %q", len(tail), tail[0], tail[len(tail)-1])
	}
}

func TestRecentCalls(t *testing.T) {
	os.Remove(eventsPath)
	noteCall("aaaaaaaa-1", "parser", "Parser fix ready")
	events := []map[string]any{
		{"call_id": "aaaaaaaa-1", "event": "ringing", "at": "2026-11-02T11:00:00Z"},
		{"call_id": "bbbbbbbb-2", "event": "ringing", "at": "2026-11-02T11:01:00Z"},
		{"call_id": "aaaaaaaa-1", "event": "answered", "at": "2026-11-02T11:00:05Z"},
		{"call_id": "aaaaaaaa-1", "event": "ended", "duration_s": "65.2", "at": "2026-11-02T11:01:10Z"},
		{"call_id": "bbbbbbbb-2", "event": "missed", "at": "2026-11-02T11:01:30Z"},
	}
	for _, e := range events {
		if err := appendEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	calls := recentCalls(50)
	if len(calls) != 2 {
		t.Fatalf("%d calls", len(calls))
	}
	missed, answered := calls[0], calls[1]
	if missed.Short != "bbbbbbbb" || missed.Outcome != "Missed" || !missed.Bad || missed.Title != "Project unknown" || !missed.Untitled {
		t.Errorf("the missed call: %+v", missed)
	}
	if answered.Title != "parser" || answered.Outcome != "Answered" || answered.Duration != "1 min 5 s" || len(answered.Events) != 3 ||
		answered.Events[2].Detail != "lasted 1 min 5 s" {
		t.Errorf("the answered call: %+v", answered)
	}
	if one := recentCalls(1); len(one) != 1 || one[0].Short != "bbbbbbbb" {
		t.Errorf("the last call: %+v", one)
	}

	// A call without its project is titled by its Mac; one still live says so.
	noteMac("cccccccc-3", "studio")
	appendEvent(map[string]any{"call_id": "cccccccc-3", "event": "audio connected", "at": "2026-11-02T11:02:00Z"})
	c := &Call{id: "cccccccc-3", answeredAt: now()}
	registryMu.Lock()
	live[c.id] = c
	registryMu.Unlock()
	defer func() {
		registryMu.Lock()
		delete(live, c.id)
		registryMu.Unlock()
	}()
	if latest := recentCalls(1)[0]; latest.Title != "From studio" || latest.Untitled || latest.Outcome != "Live now" || !latest.Live {
		t.Errorf("a live call from a Mac: %+v", latest)
	}
}
