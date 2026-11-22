package main

import (
	"strings"
	"testing"
	"time"
)

// check_session: asked whether the session got something, the voice hears whether it's
// listening and working, and how far each thing passed to it got; and what it says after the
// tool is spoken.
func TestCheckSession(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "k1-session",
		fakeReply{Match: "run the tests", Steps: []fakeStep{say("I'll have it run the tests."), tool("pass_to_session", map[string]any{"message": "Run the tests."})}},
		fakeReply{Match: "and push", Steps: []fakeStep{say("And I'll have it push."), tool("pass_to_session", map[string]any{"message": "Push it."})}},
		fakeReply{Match: "get it", Steps: []fakeStep{say("Let me check."), tool("check_session", map[string]any{}), say("It took it, and it's working on it.")}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Have it run the tests."})
	text, _ := hub.reply(callID, 2, 5*time.Second)
	a.played(callID, text)
	a.waitLog("passed to the session", 3*time.Second)
	// Its mod takes it, while a turn runs.
	a.mod("GET", "/sessions/k1-session/messages?project=demo&cwd="+a.project()+"&turning=1", nil)

	// The next one you cut off before hearing it announced.
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Oh, and push."})
	hub.reply(callID, 3, 5*time.Second)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "interrupted", "call_id": callID, "reply": 3, "heard": "And"})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Did it get it?", "interrupted": true})
	reply, _ := hub.reply(callID, 4, 5*time.Second)
	report := lastToolResult(a)
	for _, want := range []string{
		"The session is listening",
		"It's working on a turn",
		`"Run the tests.", 0 s ago on this call: the session took it 0 s later; no answer has come back yet.`,
		`"Push it.", 0 s ago on this call: not passed, you cut the voice off first.`,
	} {
		check(t, "the report says "+want, strings.Contains(report, want), "\n"+report)
	}
	check(t, "and what the voice says after it is spoken", strings.HasSuffix(reply, "It took it, and it's working on it."), reply)
}
