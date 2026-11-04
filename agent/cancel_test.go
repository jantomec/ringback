package main

import (
	"strings"
	"testing"
	"time"
)

// heldAgent is an agent whose hub holds every call until an hour from now.
func heldAgent(t *testing.T) (*fakeHub, *testAgent) {
	t.Helper()
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	heldUntil := time.Now().Add(time.Hour).Format(time.RFC3339)
	hub.mu.Lock()
	hub.place = func(map[string]any) map[string]any { return map[string]any{"held": true, "held_until": heldUntil} }
	hub.mu.Unlock()
	return hub, a
}

// A waiting call you cancel on the phone never rings: the agent forgets it, the hub hears it
// no longer waits (drop), what it asked goes back to the terminal (cancelled), and the session
// is told.
func TestCancelHeldCall(t *testing.T) {
	withTimes(t, 30*time.Second, 400*time.Millisecond)
	hub, a := heldAgent(t)
	a.collect("s9-session")
	callID, _ := a.placeCall("s9-session", "Push", "Ready to push.")
	_, asked := a.mod("POST", "/sessions/s9-session/permissions", permissionBody(a, "Bash"))
	id, _ := asked["permission"].(string)
	check(t, "a permission joins the held call", asked["call"] == "held", asked)

	from := hub.mark()
	reply := hub.request(map[string]any{"type": "cancel", "call_id": callID, "by": "phone"})
	check(t, "cancelled", reply["ok"] == true, reply)
	dropped, _ := hub.wait(from, 2*time.Second, is("drop"))
	check(t, "the hub hears it no longer waits", dropped != nil && dropped["call_id"] == callID, dropped)
	_, verdict := a.mod("GET", "/sessions/s9-session/permissions/"+id+"?wait=2", nil)
	check(t, "its permission goes back to the terminal", verdict["verdict"] == "cancelled", verdict)
	note := a.waitMessage("s9-session", "outcome", 3*time.Second)
	check(t, "the session is told", note["text"] == `The call about "Push" won't ring: the developer cancelled it on the phone without hearing it. `+
		"What it asked is asked in the terminal instead.", note)
	reply = hub.request(map[string]any{"type": "cancel", "call_id": callID, "by": "phone"})
	check(t, "cancelled again: not waiting", reply["status"] == float64(403) && reply["error"] == "not waiting", reply)
	before := placesFor(hub, callID)
	time.Sleep(time.Second)
	check(t, "and it's no longer asked about", placesFor(hub, callID) == before, placesFor(hub, callID)-before)
}

// Cancelled while the agent couldn't be told, the hub says so when the call is asked about
// again, and the agent forgets it then.
func TestCancelledWhileAway(t *testing.T) {
	withTimes(t, 30*time.Second, 300*time.Millisecond)
	hub, a := heldAgent(t)
	a.collect("s10-session")
	callID, _ := a.placeCall("s10-session", "Deploy", "Deployed.")
	from := hub.mark()
	hub.mu.Lock()
	hub.place = func(map[string]any) map[string]any { return map[string]any{"cancelled": true, "by": "portal"} }
	hub.mu.Unlock()
	dropped, _ := hub.wait(from, 3*time.Second, is("drop"))
	check(t, "asking again, it hears it was cancelled, and drops it", dropped != nil && dropped["call_id"] == callID, dropped)
	note := a.waitMessage("s10-session", "outcome", 3*time.Second)
	check(t, "the session is told where", note["text"] == `The call about "Deploy" won't ring: the developer cancelled it on the hub's portal without hearing it.`, note)
}

// A held call whose session no longer listens (ringback off in it, or it ended) won't ring.
func TestHeldCallOfASessionThatLeft(t *testing.T) {
	withTimes(t, 30*time.Second, time.Hour)
	oldListening, oldTick := listeningFor, tick
	listeningFor, tick = 300*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { listeningFor, tick = oldListening, oldTick })
	hub, a := heldAgent(t)
	a.collect("s11-session")
	callID, _ := a.placeCall("s11-session", "Lint", "Lint is clean.")
	from := hub.mark()
	dropped, _ := hub.wait(from, 3*time.Second, is("drop"))
	check(t, "its session stopped collecting: the call is dropped", dropped != nil && dropped["call_id"] == callID, dropped)
	line := a.waitLog("call "+callID[:8]+": its session no longer listens, so it won't ring", time.Second)
	check(t, "and logged", strings.Contains(line, "won't ring"), line)
}
