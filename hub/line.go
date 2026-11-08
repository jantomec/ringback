package main

// The line: every Mac's calls ring one at a time. While a call rings or you're on one, a call
// that would ring is held in turn, as a held call is (its agent keeps it and asks again), and
// once the line is free the first of those is asked again, so it rings. A call you decline or
// miss rings again 5 min, 20 min, 1 h, 2½ h and 6 h after it first rang (held, missed, until
// then), and until it does nothing else rings; after its last try it waits until you call its
// session back, and so does every other call. Calling the session back, or calling the waiting
// call now, takes it off the waiting calls (state.go), which keep the calls held in turn and
// those missed, with when each was first held, how often it has rung and when it first did.
//
// Once a call you were on ends, the line rests for callGap before a session's next call rings,
// so you can think about the last one first: a call placed meanwhile waits in turn until the
// rest is over, and the first in turn is asked again then. Calls you make yourself don't wait.

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	inTurn   = "in turn"   // the line is busy, or calls wait before it
	missed   = "missed"    // you didn't answer: it rings again at held_until
	callBack = "call back" // you didn't answer its last try: it waits until you call its session
)

// retries are when a call you didn't answer rings again, after its first ring; at least
// retryGap after its last ring, as when it waited outside your work hours meanwhile.
var retries = []time.Duration{5 * time.Minute, 20 * time.Minute, time.Hour, 150 * time.Minute, 6 * time.Hour}

const retryGap = 5 * time.Minute

// callGap is how long the line rests once a call you were on has ended. RINGBACK_CALL_GAP sets
// fewer seconds, for the checks; 0, none.
var callGap = envSeconds("RINGBACK_CALL_GAP", 60)

// afterRest runs f once the line has rested (a variable, for the tests' clock).
var afterRest = func(f func()) { time.AfterFunc(callGap, f) }

// ringFor: a call that rang but that the phone hasn't said it answered, declined or missed
// within this long counts as missed, as when the phone is off. The app rings for 30 s.
// RINGBACK_RING_FOR sets fewer seconds, for the checks.
var ringFor = envSeconds("RINGBACK_RING_FOR", 75)

// lineCall is the call on the line: ringing, or answered and on.
type lineCall struct {
	id, project, subject, voice, session string
	question                             bool
	tries                                int       // rings so far, this one included
	firstRang                            time.Time // when its first did
	answered                             bool
}

var (
	lineMu    sync.Mutex
	onLine    *lineCall
	restUntil time.Time // the line rests until then, after a call you were on
)

// resting is when the line's rest ends, as ISO 8601 rounded up to the second, if it hasn't yet.
func resting(t time.Time) string {
	lineMu.Lock()
	until := restUntil
	lineMu.Unlock()
	if !t.Before(until) {
		return ""
	}
	if whole := until.Truncate(time.Second); whole.Before(until) {
		until = whole.Add(time.Second)
	}
	return until.UTC().Format(time.RFC3339)
}

// lineWaits is why a call that would ring waits for the line, and until when ("" if it rings
// now): a missed call before its next try, or after its last; and any call while another rings
// or is on, while a missed call waits to ring again, while calls placed before it wait their
// turn, or, until a time, while the line rests after a call. entry is the call's own on the
// waiting calls, if it's there. The caller holds stateMu.
func lineWaits(callID string, entry map[string]string, waiting map[string]map[string]string) (string, string) {
	t := now()
	switch entry["reason"] {
	case callBack:
		return callBack, ""
	case missed:
		if due, ok := parseISO8601(entry["held_until"]); ok && t.Before(due) {
			return missed, entry["held_until"]
		}
	}
	lineMu.Lock()
	busy := onLine != nil && onLine.id != callID
	lineMu.Unlock()
	if busy {
		return inTurn, ""
	}
	ownMissed := entry["reason"] == missed
	for id, other := range waiting {
		if id == callID {
			continue
		}
		switch {
		case other["reason"] == missed || other["reason"] == callBack:
			// A missed call rings again before any other, and after its last try nothing rings.
			if !ownMissed || other["reason"] == callBack {
				return inTurn, ""
			}
		case other["reason"] == inTurn && !ownMissed && before(id, other, callID, entry):
			return inTurn, ""
		}
	}
	if until := resting(t); until != "" {
		return inTurn, until
	}
	return "", ""
}

// before says whether waiting call a was held before call b, which hasn't been held at all if
// its entry is empty; by call ID if at the same time.
func before(aID string, a map[string]string, bID string, b map[string]string) bool {
	if b == nil || b["since"] == "" {
		return true
	}
	if a["since"] != b["since"] {
		return a["since"] < b["since"]
	}
	return aID < bID
}

// takeLine puts a call that rings on the line, with how often it rang before (from its entry
// on the waiting calls), and counts it as missed if the phone says nothing of it within
// ringFor.
func takeLine(callID, project, subject, voice, session string, question bool, entry map[string]string) {
	c := &lineCall{id: callID, project: project, subject: subject, voice: voice, session: session, question: question,
		tries: 1, firstRang: now()}
	if entry["first_rang"] != "" {
		if first, ok := parseISO8601(entry["first_rang"]); ok {
			c.firstRang = first
		}
		fmt.Sscan(entry["tries"], &c.tries)
		c.tries++
	}
	lineMu.Lock()
	onLine = c
	lineMu.Unlock()
	go func() {
		time.Sleep(ringFor)
		stateMu.Lock()
		defer stateMu.Unlock()
		lineUnanswered(callID, "the phone didn't say it rang it")
	}()
}

// lineAnswered: you answered the call on the line, so it stays there until it ends.
func lineAnswered(callID string) {
	lineMu.Lock()
	defer lineMu.Unlock()
	if onLine != nil && onLine.id == callID {
		onLine.answered = true
	}
}

// lineOffered: the phone offered a call its agent accepted. One you made (a call now, a call
// back, a session from Sessions) is on the line, unless another already is, and no longer
// waits: called now or back, a missed call won't ring again.
func lineOffered(callID, kind string) {
	if kind == "ring" || kind == "" {
		return
	}
	stateMu.Lock()
	if removed, err := removeWaiting(callID); err == nil && removed {
		log("call " + prefix(callID, 8) + ": no longer waiting, you called it")
	}
	stateMu.Unlock()
	lineMu.Lock()
	defer lineMu.Unlock()
	if onLine == nil {
		onLine = &lineCall{id: callID, tries: 1, firstRang: now(), answered: true}
	}
}

// lineEnded: a call ended, or its agent dropped it, so if it's on the line the line is free,
// and the next call in turn is asked again: at once, or if you were on the call, once the line
// has rested. The caller holds stateMu.
func lineEnded(callID string) {
	lineMu.Lock()
	freed := onLine != nil && onLine.id == callID
	rest := freed && onLine.answered && callGap > 0
	if freed {
		onLine = nil
	}
	if rest {
		restUntil = now().Add(callGap)
	}
	lineMu.Unlock()
	if rest {
		log(fmt.Sprintf("the line rests until %s, before a session's next call rings", timeOfDay(restUntil.Local())))
		afterRest(func() {
			stateMu.Lock()
			defer stateMu.Unlock()
			nextInTurn()
		})
	} else if freed {
		nextInTurn()
	}
}

// lineUnanswered: the phone declined or missed the call on the line, or said nothing of it in
// time: it's held to ring again at its next try, or after its last until you call its session
// back, and its agent is told. The line is free, but nothing rings meanwhile. The caller holds
// stateMu.
func lineUnanswered(callID, how string) {
	lineMu.Lock()
	c := onLine
	if c == nil || c.id != callID || c.answered {
		lineMu.Unlock()
		return
	}
	onLine = nil
	lineMu.Unlock()
	reason, next := callBack, ""
	if c.tries <= len(retries) {
		at := c.firstRang.Add(retries[c.tries-1])
		if gap := now().Add(retryGap); at.Before(gap) {
			at = gap
		}
		reason, next = missed, at.UTC().Format(time.RFC3339)
	}
	calls := freshWaiting()
	entry := map[string]string{"project": c.project, "voice": c.voice, "refreshed": isoNow(), "reason": reason,
		"since": isoNow(), "tries": fmt.Sprint(c.tries), "first_rang": c.firstRang.UTC().Format(time.RFC3339)}
	for key, value := range map[string]string{"subject": c.subject, "session_id": c.session, "held_until": next} {
		if value != "" {
			entry[key] = value
		}
	}
	if c.question {
		entry["question"] = "yes"
	}
	if old := calls[callID]; old["notified"] != "" {
		entry["notified"] = old["notified"]
	}
	calls[callID] = entry
	if err := writeJSON(waitingPath, calls); err != nil {
		log("call " + prefix(callID, 8) + ": can't keep it waiting: " + err.Error())
	}
	tries := plural(c.tries, "ring", "rings")
	if next != "" {
		at, _ := parseISO8601(next)
		log(fmt.Sprintf("call %s: not answered (%s), after %s; rings again at %s, nothing else rings meanwhile",
			prefix(callID, 8), how, tries, timeOfDay(at.Local())))
	} else {
		log(fmt.Sprintf("call %s: not answered (%s), after %s; it waits until you call its session back, and every call with it",
			prefix(callID, 8), how, tries))
	}
	registryMu.Lock()
	agent := placedBy[callID]
	registryMu.Unlock()
	if agent != nil {
		message := map[string]any{"type": "missed", "call_id": callID, "reason": reason, "tries": c.tries}
		if next != "" {
			message["held_until"] = next
		}
		_ = agent.send(message)
	}
}

// nextInTurn asks the call that's waited longest in turn to ask again, so it rings, while the
// line is free and not resting, and no missed call waits to ring again. The caller holds
// stateMu.
func nextInTurn() {
	lineMu.Lock()
	busy := onLine != nil || now().Before(restUntil)
	lineMu.Unlock()
	if busy {
		return
	}
	calls := freshWaiting()
	var turn []string
	for id, call := range calls {
		switch call["reason"] {
		case missed, callBack:
			return
		case inTurn:
			turn = append(turn, id)
		}
	}
	if len(turn) == 0 {
		return
	}
	sort.Slice(turn, func(i, j int) bool {
		a, b := calls[turn[i]], calls[turn[j]]
		if a["since"] != b["since"] {
			return a["since"] < b["since"]
		}
		return turn[i] < turn[j]
	})
	log("call " + prefix(turn[0], 8) + ": its turn, it asks again")
	broadcast(map[string]any{"type": "reask", "call_ids": []any{turn[0]}})
}
