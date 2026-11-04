package main

import (
	"strings"
	"testing"
	"time"
)

// A call you don't answer, or decline, is held to ring again, as the hub says, rather than
// forgotten; its session is told. At its time it asks the hub again, and rings with a fresh
// Haiku. After its last try it waits until you call its session back.
func TestMissedCallRingsAgain(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("s7-session")
	callID, _ := a.placeCall("s7-session", "Tests pass", "The tests pass.")
	eventually(t, "its Haiku writes the opening", 5*time.Second, func() bool { return len(a.starts()) == 1 })

	places := func() int { return hub.count(is("place")) }
	again := time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339)
	hub.send(map[string]any{"type": "missed", "call_id": callID, "reason": "missed", "held_until": again, "tries": 1})
	note := a.waitMessage("s7-session", "outcome", 3*time.Second)
	check(t, "its session is told it wasn't answered, and that it rings again",
		strings.HasPrefix(note["text"].(string), `The call about "Tests pass" wasn't answered (1 ring so far). It rings again at `), note)
	a.waitLog("call "+callID[:8]+": not answered (ring 1), held until "+again, time.Second)
	before := places()
	eventually(t, "at its time it asks the hub again, and rings", 5*time.Second, func() bool { return places() > before })
	eventually(t, "with a fresh Haiku", 5*time.Second, func() bool { return len(a.starts()) == 2 })
	a.waitLog("call "+callID[:8]+": placed for demo (s7-sessi), held until now: Tests pass", time.Second)

	hub.send(map[string]any{"type": "missed", "call_id": callID, "reason": "call back", "tries": 6})
	note = a.waitMessage("s7-session", "outcome", 3*time.Second)
	check(t, "after its last try, it waits until you call back", note["text"] == `The call about "Tests pass" wasn't answered after 6 rings. `+
		"It doesn't ring again: it waits until the developer calls the session back, and other calls wait with it.", note)
	status, _ := a.mod("POST", "/calls", map[string]any{"session_id": "s7-session", "project": "demo", "cwd": a.project(),
		"subject": "Merged", "briefing": "It's merged."})
	check(t, "a new briefing from its session joins the waiting call", status == 200 && places() == before+1, status, places())

	// Called back from Recents: it's the call back, and the hub is told it no longer waits.
	reply := hub.request(map[string]any{"type": "offer", "call_id": callID, "kind": "back"})
	check(t, "a call you didn't answer can be called back while it waits to ring again", reply["ok"] == true, reply)
}

// Calling a session yourself drops its calls you didn't answer, held to ring again: they
// won't, the hub is told, and the session hears why.
func TestCallingTheSessionDropsMissed(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("s8-session")
	callID, _ := a.placeCall("s8-session", "Deploy", "Deployed.")
	later := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	hub.send(map[string]any{"type": "missed", "call_id": callID, "reason": "missed", "held_until": later, "tries": 1})
	a.waitMessage("s8-session", "outcome", 3*time.Second)

	from := hub.mark()
	reply := hub.request(map[string]any{"type": "offer", "call_id": "contact-call-0000000001", "kind": "contact", "session_id": "s8-session"})
	check(t, "you call the session from Sessions", reply["ok"] == true, reply)
	dropped, _ := hub.wait(from, 3*time.Second, is("drop"))
	check(t, "its missed call no longer waits at the hub", dropped != nil && dropped["call_id"] == callID, dropped)
	note := a.waitMessage("s8-session", "outcome", 3*time.Second)
	check(t, "and its session hears why", note["text"] == `The call about "Deploy" wasn't answered, and the developer called the session since, so it doesn't ring again.`, note)
}
