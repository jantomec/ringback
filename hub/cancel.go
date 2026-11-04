package main

// Cancelling a waiting call, from the phone (/cancel) or the portal: it never rings. The hub
// acts first, so it works while the Mac that holds the call is asleep: the call leaves the
// waiting calls at once, so the next in turn can ring, and its entry stays as cancelled, unlisted,
// for cancelKept. The agent that holds the call is told now if it's linked (`cancel`), or hears
// it when it next asks about the call (place answers `cancelled`): it forgets the call, which
// tells the session, and what the call asked goes back to the terminal; its `drop` ends the
// entry.

import (
	"errors"
	"net/http"
)

const cancelled = "cancelled"

// cancelKept is how long a cancelled call's entry waits for its agent to hear of it.
// RINGBACK_CANCEL_KEPT sets fewer seconds, for the checks.
var cancelKept = envSeconds("RINGBACK_CANCEL_KEPT", 3*24*3600)

var errNotWaiting = errors.New("not waiting")

// cancelWaiting cancels a waiting call, by "phone" or "portal"; errNotWaiting if it doesn't
// wait (it rang meanwhile, or was cancelled already). The caller holds stateMu.
func cancelWaiting(callID, by string) error {
	calls := freshWaiting()
	entry, ok := calls[callID]
	if callID == "" || !ok || entry["reason"] == cancelled {
		return errNotWaiting
	}
	entry["reason"], entry["cancelled_by"], entry["refreshed"] = cancelled, by, isoNow()
	delete(entry, "held_until")
	if err := writeJSON(waitingPath, calls); err != nil {
		return err
	}
	if err := appendEvent(map[string]any{"call_id": callID, "event": "cancelled", "by": by}); err != nil {
		log("call " + prefix(callID, 8) + ": can't keep its event: " + err.Error())
	}
	log("call " + prefix(callID, 8) + ": cancelled on the " + by + ", it won't ring")
	lineEnded(callID)
	nextInTurn()
	go func() {
		agent, answer := ask(callID, "", map[string]any{"type": "cancel", "call_id": callID, "by": by})
		if agent == nil {
			log("call " + prefix(callID, 8) + ": its agent isn't told yet (" + orDefault(str(answer, "error"), "not linked") +
				"); it is when it asks about the call again")
		}
	}()
	return nil
}

// cancelCall: you cancelled a waiting call in the app: {"call_id"}. Its ID, which only a paired
// phone gets from /waiting, is what lets it, as for /offer. 403 if it doesn't wait.
func cancelCall(w http.ResponseWriter, r *http.Request) {
	body, valid := readBody(r)
	if !valid {
		phoneJSON(w, http.StatusBadRequest, map[string]any{"error": "needs call_id"})
		return
	}
	stateMu.Lock()
	err := cancelWaiting(str(body, "call_id"), "phone")
	stateMu.Unlock()
	switch {
	case errors.Is(err, errNotWaiting):
		phoneJSON(w, http.StatusForbidden, map[string]any{"error": "not waiting"})
	case err != nil:
		log("call " + prefix(str(body, "call_id"), 8) + ": can't cancel it: " + err.Error())
		phoneJSON(w, http.StatusInternalServerError, map[string]any{"error": "can't cancel it"})
	default:
		phoneJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// waitingByShort is the waiting call whose ID starts with short, as the portal names it (it
// never shows a call's whole ID, its secret); "" unless exactly one does. The caller holds
// stateMu.
func waitingByShort(short string) string {
	found := ""
	for id, call := range freshWaiting() {
		if short != "" && prefix(id, 8) == short && call["reason"] != cancelled {
			if found != "" {
				return ""
			}
			found = id
		}
	}
	return found
}
