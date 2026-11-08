package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// placeAs places a call as an agent does, under stateMu, and says how the hub answered: ""
// if it rings, or why it's held, with until when.
func placeAs(t *testing.T, callID, session string) (string, string) {
	t.Helper()
	stateMu.Lock()
	defer stateMu.Unlock()
	answer, err := placeCall("line-mac", map[string]any{"project": "demo", "call_id": callID, "subject": "About " + session,
		"session_id": session})
	if err != nil {
		t.Fatal(err)
	}
	if answer["held"] != true {
		return "", ""
	}
	why, _ := answer["reason"].(string)
	until, _ := answer["held_until"].(string)
	return why, until
}

func phoneSays(callID, what string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	event(map[string]any{"call_id": callID, "event": what})
}

// heldTill asks about a held call every 10 min, as its agent does, until its time, and says
// whether it stayed held, missed, all the while.
func heldTill(t *testing.T, callID, session string) bool {
	t.Helper()
	due, ok := parseISO8601(waitingEntry(callID)["held_until"])
	if !ok {
		t.Fatalf("not held until a time: %v", waitingEntry(callID))
	}
	for now().Before(due) {
		if why, _ := placeAs(t, callID, session); why != missed {
			return false
		}
		step := min(10*time.Minute, due.Sub(now()))
		advance(step)
	}
	return true
}

func waitingEntry(callID string) map[string]string {
	stateMu.Lock()
	defer stateMu.Unlock()
	return loadWaiting()[callID]
}

// resetLine starts a test with the line free, nothing waiting, and no rest after calls (as
// TestLineRests sets it).
func resetLine(t *testing.T) {
	t.Helper()
	gap := callGap
	lineMu.Lock()
	onLine, restUntil, callGap = nil, time.Time{}, 0
	lineMu.Unlock()
	os.Remove(waitingPath)
	os.Remove(devicesPath)
	t.Cleanup(func() {
		lineMu.Lock()
		onLine, restUntil, callGap = nil, time.Time{}, gap
		lineMu.Unlock()
		os.Remove(waitingPath)
		os.Remove(devicesPath)
	})
	// A phone that speaks itself, so calls ring with no speech node linked, outside no hours.
	registerPhone(t, "line-phone-1", "iPhone", "yes")
}

const (
	callA = "line-call-aaaaaaaaaaaa"
	callB = "line-call-bbbbbbbbbbbb"
	callC = "line-call-cccccccccccc"
)

// Calls ring one at a time, in the order they came; a call you miss rings again at its tries,
// and nothing else rings meanwhile.
func TestLineInTurn(t *testing.T) {
	resetLine(t)
	saved := now()
	defer setClock(saved)

	if why, _ := placeAs(t, callA, "session-a"); why != "" {
		t.Fatalf("the first call is held: %s", why)
	}
	if why, _ := placeAs(t, callB, "session-b"); why != inTurn {
		t.Errorf("while one rings, the next waits in turn: %q", why)
	}
	advance(time.Second)
	if why, _ := placeAs(t, callC, "session-c"); why != inTurn {
		t.Errorf("and the one after: %q", why)
	}

	first := now()
	phoneSays(callA, "missed")
	a := waitingEntry(callA)
	if a["reason"] != missed || a["held_until"] != first.Add(5*time.Minute).UTC().Format(time.RFC3339) || a["tries"] != "1" {
		t.Fatalf("missed, it waits to ring again 5 min after it first rang: %v", a)
	}
	if why, _ := placeAs(t, callB, "session-b"); why != inTurn {
		t.Errorf("while a missed call waits to ring again, nothing else rings: %q", why)
	}
	if why, until := placeAs(t, callA, "session-a"); why != missed || until != a["held_until"] {
		t.Errorf("nor does it before its time: %q %q", why, until)
	}

	advance(5 * time.Minute)
	if why, _ := placeAs(t, callA, "session-a"); why != "" {
		t.Fatalf("at its time it rings again: %q", why)
	}
	if waitingEntry(callA) != nil {
		t.Error("ringing, it no longer waits")
	}
	phoneSays(callA, "answered")
	phoneSays(callA, "ended")
	if why, _ := placeAs(t, callC, "session-c"); why != inTurn {
		t.Errorf("answered and over, the call placed first goes next, not the later one: %q", why)
	}
	if why, _ := placeAs(t, callB, "session-b"); why != "" {
		t.Errorf("the call placed first rings: %q", why)
	}
	if tail := strings.Join(logTail(), "\n"); !strings.Contains(tail, "call line-cal: its turn, it asks again") {
		t.Errorf("the next in turn isn't asked again:\n%s", tail)
	}
}

// A call you keep missing rings at 5 min, 20 min, 1 h, 2½ h and 6 h after it first rang, at
// least 5 min after its last ring; after that it waits until you call its session back, and
// so does every other call. Calling it back takes it off, and the next rings.
func TestLineRetries(t *testing.T) {
	resetLine(t)
	saved := now()
	defer setClock(saved)

	first := now()
	if why, _ := placeAs(t, callA, "session-a"); why != "" {
		t.Fatalf("held: %s", why)
	}
	phoneSays(callA, "declined")
	for i, after := range []time.Duration{20 * time.Minute, time.Hour, 150 * time.Minute, 6 * time.Hour} {
		if !heldTill(t, callA, "session-a") {
			t.Fatalf("before try %d, it didn't stay held", i+2)
		}
		if why, _ := placeAs(t, callA, "session-a"); why != "" {
			t.Fatalf("try %d doesn't ring: %s", i+2, why)
		}
		phoneSays(callA, "missed")
		want := first.Add(after)
		if gap := now().Add(retryGap); want.Before(gap) {
			want = gap
		}
		if got := waitingEntry(callA); got["held_until"] != want.UTC().Format(time.RFC3339) || got["reason"] != missed {
			t.Errorf("after try %d: %v, want %s", i+2, got, want.UTC().Format(time.RFC3339))
		}
	}
	heldTill(t, callA, "session-a")
	placeAs(t, callA, "session-a")
	phoneSays(callA, "missed")
	if got := waitingEntry(callA); got["reason"] != callBack || got["held_until"] != "" || got["tries"] != "6" {
		t.Fatalf("after its last try it waits until you call back: %v", got)
	}
	for range 6 { // an hour, asked about as its agent does
		advance(10 * time.Minute)
		if why, _ := placeAs(t, callA, "session-a"); why != callBack {
			t.Errorf("however long after: %q", why)
		}
	}
	if why, _ := placeAs(t, callB, "session-b"); why != inTurn {
		t.Errorf("and every other call waits with it: %q", why)
	}

	lineOffered(callA, "now")
	if waitingEntry(callA) != nil {
		t.Error("called now, it no longer waits")
	}
	if why, _ := placeAs(t, callB, "session-b"); why != inTurn {
		t.Errorf("while you're on the call you made, the next waits: %q", why)
	}
	stateMu.Lock()
	lineEnded(callA)
	stateMu.Unlock()
	if why, _ := placeAs(t, callB, "session-b"); why != "" {
		t.Errorf("once it's over, the next rings: %q", why)
	}
}

// A call the phone says nothing of within ringFor, as when it's off, counts as missed; a call
// you answer doesn't.
func TestLineNoWordFromThePhone(t *testing.T) {
	resetLine(t)
	savedRing := ringFor
	ringFor = 50 * time.Millisecond
	defer func() { ringFor = savedRing }()

	placeAs(t, callA, "session-a")
	deadline := time.Now().Add(2 * time.Second)
	for waitingEntry(callA)["reason"] != missed && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := waitingEntry(callA); got["reason"] != missed {
		t.Errorf("nothing from the phone: %v", got)
	}

	placeAs(t, callB, "session-b") // held: A waits to ring again
	stateMu.Lock()
	os.Remove(waitingPath)
	stateMu.Unlock()
	placeAs(t, callB, "session-b")
	lineAnswered(callB)
	time.Sleep(150 * time.Millisecond)
	if got := waitingEntry(callB); got != nil {
		t.Errorf("an answered call counted as missed: %v", got)
	}
}

// Once a call you were on ends, the line rests a minute: a session's call placed meanwhile
// waits in turn until then, and is asked again when it's over. A call you make yourself goes
// through, and one you didn't answer doesn't make the line rest.
func TestLineRests(t *testing.T) {
	resetLine(t)
	saved, timer := now(), afterRest
	var rested []func()
	callGap, afterRest = time.Minute, func(f func()) { rested = append(rested, f) }
	defer func() { setClock(saved); afterRest = timer }()

	if why, _ := placeAs(t, callA, "session-a"); why != "" {
		t.Fatalf("the first call is held: %s", why)
	}
	phoneSays(callA, "answered")
	advance(30*time.Second + 500*time.Millisecond) // the rest ends mid-second, and is rounded up
	phoneSays(callA, "ended")
	ended := now()
	advance(20*time.Second + 300*time.Millisecond)
	why, until := placeAs(t, callB, "session-b")
	if want := ended.Add(time.Minute + time.Second).Truncate(time.Second).UTC().Format(time.RFC3339); why != inTurn || until != want {
		t.Fatalf("20 s after a call ended, the next waits in turn until a minute after it, rounded up: %q %q, want %q", why, until, want)
	}
	if e := waitingEntry(callB); e["reason"] != inTurn || e["held_until"] != until {
		t.Errorf("and the waiting calls say so: %v", e)
	}
	if len(rested) != 1 {
		t.Fatalf("the hub waits for the rest to end once: %d", len(rested))
	}
	asked := strings.Count(strings.Join(logTail(), "\n"), "its turn, it asks again")
	stateMu.Lock()
	nextInTurn()
	stateMu.Unlock()
	if strings.Count(strings.Join(logTail(), "\n"), "its turn, it asks again") != asked {
		t.Error("during the rest, no call in turn is asked again")
	}

	// You call a session yourself meanwhile: it goes through.
	lineOffered(callC, "now")
	lineMu.Lock()
	mine := onLine != nil && onLine.id == callC
	lineMu.Unlock()
	if !mine {
		t.Fatal("a call you make yourself doesn't wait for the rest")
	}
	stateMu.Lock()
	lineEnded(callC)
	stateMu.Unlock()
	if why, until = placeAs(t, callB, "session-b"); why != inTurn || until == "" {
		t.Errorf("once it's over, the line rests again: %q %q", why, until)
	}

	// The rest is over: the first in turn is asked again, and rings.
	advance(time.Minute)
	rested[len(rested)-1]()
	if tail := strings.Join(logTail(), "\n"); !strings.Contains(tail, "call line-cal: its turn, it asks again") {
		t.Errorf("the next in turn isn't asked again:\n%s", tail)
	}
	if why, _ := placeAs(t, callB, "session-b"); why != "" {
		t.Fatalf("after the rest it rings: %q", why)
	}

	// Not answered: no rest, as nothing rings until it does again anyway.
	phoneSays(callB, "missed")
	stateMu.Lock()
	lineMu.Lock()
	resting := now().Before(restUntil)
	lineMu.Unlock()
	stateMu.Unlock()
	if resting || len(rested) != 2 {
		t.Errorf("a call you didn't answer makes the line rest: %v, %d", resting, len(rested))
	}
}
