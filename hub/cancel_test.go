package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func cancelAs(callID, by string) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	return cancelWaiting(callID, by)
}

func listedIDs(t *testing.T) []string {
	t.Helper()
	stateMu.Lock()
	defer stateMu.Unlock()
	calls, err := listWaiting()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range calls {
		ids = append(ids, c["call_id"].(string))
	}
	return ids
}

// A cancelled call leaves the waiting calls at once and frees the queue; it's kept, unlisted,
// so its agent hears of it when it asks again, until cancelKept.
func TestCancelWaiting(t *testing.T) {
	resetLine(t)
	saved := now()
	defer setClock(saved)

	placeAs(t, callA, "session-a")
	placeAs(t, callB, "session-b")
	phoneSays(callA, "missed")
	if err := cancelAs(callC, "phone"); err != errNotWaiting {
		t.Errorf("a call that doesn't wait: %v", err)
	}
	if err := cancelAs(callA, "phone"); err != nil {
		t.Fatal(err)
	}
	if ids := listedIDs(t); strings.Join(ids, " ") != callB {
		t.Errorf("cancelled, it's no longer listed: %v", ids)
	}
	if tail := strings.Join(logTail(), "\n"); !strings.Contains(tail, "call line-cal: cancelled on the phone, it won't ring") ||
		!strings.Contains(tail, "its turn, it asks again") {
		t.Errorf("the next in turn isn't asked again:\n%s", tail)
	}
	if why, _ := placeAs(t, callB, "session-b"); why != "" {
		t.Errorf("the missed call it cancelled no longer holds the next up: %q", why)
	}
	if err := cancelAs(callA, "phone"); err != errNotWaiting {
		t.Errorf("cancelled twice: %v", err)
	}
	stateMu.Lock()
	answer, _ := placeCall("line-mac", map[string]any{"project": "demo", "call_id": callA, "session_id": "session-a"})
	stateMu.Unlock()
	if answer["cancelled"] != true || answer["by"] != "phone" {
		t.Errorf("its agent, asking again, hears it was cancelled: %v", answer)
	}
	advance(time.Hour)
	if waitingEntry(callA)["reason"] != cancelled || len(func() map[string]map[string]string {
		stateMu.Lock()
		defer stateMu.Unlock()
		return freshWaiting()
	}()) == 0 {
		t.Error("its record went with the stale calls")
	}
	advance(cancelKept)
	stateMu.Lock()
	_, kept := freshWaiting()[callA]
	stateMu.Unlock()
	if kept {
		t.Error("its record outlives cancelKept")
	}
}

// From the phone: /cancel with the call's ID; 403 if it doesn't wait.
func TestCancelFromThePhone(t *testing.T) {
	resetLine(t)
	placeAs(t, callA, "session-a")
	placeAs(t, callB, "session-b") // in turn
	server := httptest.NewServer(http.HandlerFunc(cancelCall))
	defer server.Close()
	post := func(id string) int {
		r, err := http.Post(server.URL, "application/json", strings.NewReader(`{"call_id": "`+id+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if got := post(callB); got != http.StatusOK {
		t.Errorf("a waiting call: %d", got)
	}
	if got := post(callB); got != http.StatusForbidden {
		t.Errorf("again: %d", got)
	}
	if got := post(callA); got != http.StatusForbidden {
		t.Errorf("a call that rings: %d", got)
	}
}

// Sending a missed call's question to the Claude app frees the queue too.
func TestClaudeAppFreesTheLine(t *testing.T) {
	resetLine(t)
	placeAs(t, callA, "session-a")
	placeAs(t, callB, "session-b")
	phoneSays(callA, "missed")
	before := len(logTail())
	phoneSays(callA, "claude app")
	if tail := strings.Join(logTail()[before:], "\n"); !strings.Contains(tail, "its turn, it asks again") {
		t.Errorf("the next in turn isn't asked again:\n%s", tail)
	}
}

// On the portal: each waiting row has Cancel call, naming the call by its ID's start, never
// the whole ID; a call with a question asks first.
func TestPortalCancel(t *testing.T) {
	resetLine(t)
	makeAccount(t)
	defer os.Remove(accountPath)
	placeAs(t, callA, "session-a")
	placeAs(t, callB, "session-b")
	server := httptest.NewServer(http.HandlerFunc(portal))
	defer server.Close()
	b := newBrowser(t, server)
	b.signIn()
	_, _, page := b.get("/portal/")
	short := prefix(callB, 8)
	if !strings.Contains(page, `name="call" value="`+short+`"`) || strings.Contains(page, callB) ||
		!strings.Contains(page, "Cancel call</button>") || strings.Contains(page, "data-confirm") {
		t.Fatalf("the waiting row:\n%s", page)
	}
	if status, _, _ := b.post("/portal/cancel", url.Values{"call": {short}}); status != http.StatusForbidden {
		t.Errorf("without its CSRF token: %d", status)
	}
	status, to, _ := b.post("/portal/cancel", url.Values{"csrf": {csrfOf(t, page)}, "call": {short}, "from": {"calls"}})
	if status != http.StatusSeeOther || to != "/portal/calls" {
		t.Errorf("cancelled: %d → %q", status, to)
	}
	if ids := listedIDs(t); len(ids) != 0 {
		t.Errorf("still listed: %v", ids)
	}
	_, _, calls := b.get("/portal/calls")
	if !strings.Contains(calls, "Cancelled") {
		t.Errorf("the Calls page doesn't say it was cancelled:\n%s", calls)
	}
}
