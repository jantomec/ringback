package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"
)

// The phone's side of a call, as the voice server answered it: the app's requests come to the
// hub unchanged (/offer, /answered, /claude-app, /reask, /ping), and the hub asks the agent
// that owns the call (docs/protocol.md).

func phoneJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	data, _ := json.Marshal(body)
	_, _ = w.Write(data)
}

func readBody(r *http.Request) (map[string]any, bool) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var body map[string]any
	if err != nil || json.Unmarshal(data, &body) != nil || body == nil {
		return nil, false
	}
	return body, true
}

// refused answers the phone as the agent's refusal says: its status and error.
func refused(w http.ResponseWriter, answer map[string]any) {
	status := int(number(answer["status"]))
	if status == 0 {
		status = http.StatusForbidden
	}
	phoneJSON(w, status, map[string]any{"error": orDefault(str(answer, "error"), "unknown call")})
}

func setting(body map[string]any, key string, fallback float64) float64 {
	if v, ok := body[key].(float64); ok && v >= 0 && v <= 1 {
		return v
	}
	return fallback
}

// offer: the phone's offer for a call: one that rings (made while it rings, `ringing`), a
// waiting call called now (`now`), a call back (`back`), or a session called from the
// Sessions tab (`contact`, the session's ID; the phone makes the call's ID). The owning agent
// accepts or refuses it; accepted, the call's audio starts, and the answer's SDP goes back.
func offer(w http.ResponseWriter, r *http.Request) {
	t0 := now()
	body, valid := readBody(r)
	if !valid {
		phoneJSON(w, http.StatusBadRequest, map[string]any{"error": "needs a JSON offer"})
		return
	}
	callID := str(body, "call_id")
	short := prefix(callID, 8)
	contact := str(body, "contact")
	back := body["back"] == true && contact == ""
	nowKind := body["now"] == true && !back && contact == ""
	ringing := body["ringing"] == true && !back && !nowKind && contact == ""
	kind := "ring"
	switch {
	case contact != "":
		kind = "contact"
	case back:
		kind = "back"
	case nowKind:
		kind = "now"
	}
	if contact != "" && utf8.RuneCountInString(callID) < 16 {
		phoneJSON(w, http.StatusForbidden, map[string]any{"error": "not listening"})
		return
	}
	node := aNode()
	if node == nil {
		log("call " + short + ": refused, no speech node is connected")
		phoneJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "no speech node"})
		return
	}
	// The first a node hears of a call from the Sessions tab or a call back: it starts loading
	// its models while the agent is asked.
	_ = node.send(map[string]any{"type": "warm"})
	request := map[string]any{"type": "offer", "call_id": callID, "kind": kind, "ringing": ringing}
	if contact != "" {
		request["session_id"] = contact
		noteContactCall(callID, contact)
	}
	agent, answer := ask(callID, contact, request)
	if agent == nil {
		log(fmt.Sprintf("call %s: offer refused: %s", short, str(answer, "error")))
		refused(w, answer)
		return
	}
	c, sdp, err := newCall(callID, str(body, "sdp"), ringing, setting(body, "sensitivity", 0.5),
		setting(body, "background_filter", 0.5), agent, node, loadConfig()["listen"])
	if err != nil {
		log("call " + short + ": offer failed: " + err.Error())
		phoneJSON(w, http.StatusBadRequest, map[string]any{"error": "bad offer"})
		return
	}
	registryMu.Lock()
	placedBy[callID] = agent
	registryMu.Unlock()
	noteMac(callID, agent.name)
	if earlier := keepCall(c); earlier != nil {
		c.say("a new offer replaces the earlier one")
		earlier.end("replaced")
	}
	if !ringing {
		c.markAnswered()
	}
	how := map[string]string{"ring": "", "now": ", called now", "back": ", called back", "contact": ", from Sessions"}[kind]
	if ringing {
		how = ", ringing"
	}
	c.say(fmt.Sprintf("offer answered in %.2f s%s", now().Sub(t0).Seconds(), how))
	phoneJSON(w, http.StatusOK, map[string]any{"sdp": sdp, "type": "answer"})
}

// answered: you answered a call whose offer came while the phone rang.
func answered(w http.ResponseWriter, r *http.Request) {
	body, valid := readBody(r)
	if !valid {
		phoneJSON(w, http.StatusBadRequest, map[string]any{"error": "needs call_id"})
		return
	}
	c := liveCall(str(body, "call_id"))
	if c == nil {
		log("call " + prefix(str(body, "call_id"), 8) + ": answered, but no offer for it")
		phoneJSON(w, http.StatusNotFound, map[string]any{"error": "unknown call"})
		return
	}
	c.markAnswered()
	phoneJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// claudeApp: you sent a waiting call's question to the Claude app, from the app's waiting
// calls. The agent that holds it releases the question to the session's own dialog.
func claudeApp(w http.ResponseWriter, r *http.Request) {
	body, valid := readBody(r)
	if !valid {
		phoneJSON(w, http.StatusBadRequest, map[string]any{"error": "needs call_id"})
		return
	}
	callID := str(body, "call_id")
	agent, answer := ask(callID, "", map[string]any{"type": "claude_app", "call_id": callID})
	if agent == nil {
		error := str(answer, "error")
		if error == "unknown call" {
			answer["error"] = "not waiting"
		}
		log("call " + prefix(callID, 8) + ": its question can't go to the Claude app, " + str(answer, "error"))
		refused(w, answer)
		return
	}
	phoneJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// reask: you took calls on the phone; the agents' held calls among these ask again now.
func reask(w http.ResponseWriter, r *http.Request) {
	body, valid := readBody(r)
	ids, isList := body["call_ids"].([]any)
	if !valid || !isList {
		phoneJSON(w, http.StatusBadRequest, map[string]any{"error": "needs call_ids"})
		return
	}
	broadcast(map[string]any{"type": "reask", "call_ids": ids})
	phoneJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// phoneRoutes are the voice server's endpoints, served by the hub.
var phoneRoutes = map[string]http.HandlerFunc{
	"/offer": offer, "/answered": answered, "/claude-app": claudeApp, "/reask": reask,
}

// withDeadline bounds a request: the phone's give it 15 s, as its offer does.
func withDeadline(w http.ResponseWriter, within time.Duration) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(now().Add(within))
	_ = rc.SetWriteDeadline(now().Add(within))
}
