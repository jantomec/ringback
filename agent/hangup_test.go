package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The voice ends the call: once you've heard its goodbye to the end, the agent tells the hub to
// hang up, and nothing more is said. If you speak first, the call goes on, and the model is told.

func hangUps(hub *fakeHub, callID string) int {
	return hub.count(func(m map[string]any) bool { return m["type"] == "hang_up" && m["call_id"] == callID })
}

func TestHangsUpOnceHeard(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "h1-session",
		fakeReply{Match: "thanks, bye", Steps: []fakeStep{say("Bye, talk soon."), tool("end_call", map[string]any{})}},
		fakeReply{Match: "The session:", Steps: []fakeStep{say("The session says it's done.")}},
	)
	starts := a.starts()
	check(t, "every call has end_call", strings.Contains(strings.Join(anyStrings(starts[0]["tools"]), " "), "end_call"), starts[0]["tools"])
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Okay, thanks, bye."})
	text, _ := hub.reply(callID, 2, 5*time.Second)
	check(t, "the goodbye", text == "Bye, talk soon.", text)
	a.waitLog("ends the call, held until heard", 2*time.Second)
	check(t, "end_call answers that it's held", lastToolResult(a) == hangUpHeld, lastToolResult(a))
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "Bye, talk"})
	time.Sleep(100 * time.Millisecond)
	check(t, "no hang-up before you've heard it to the end", hangUps(hub, callID) == 0)
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "Bye, talk soon."})
	m, _ := hub.wait(0, 2*time.Second, is("hang_up"))
	check(t, "the hub is told to hang up once you've heard it", m != nil && m["call_id"] == callID, m)

	// The session's answer comes before the hub has hung up: nothing more is said.
	hub.send(map[string]any{"type": "idle", "call_id": callID})
	a.mod("POST", "/sessions/h1-session/news", map[string]any{"text": "Done."})
	time.Sleep(300 * time.Millisecond)
	check(t, "no reply starts after the goodbye", len(a.turns()) == 2, a.turns())
	check(t, "and the hub was told once", hangUps(hub, callID) == 1)
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 12.0})
	outcome := a.waitMessage("h1-session", "outcome", 3*time.Second)
	transcript, _ := os.ReadFile(filepath.Join(a.project(), outcome["transcript"].(string)))
	check(t, "the transcript has the goodbye, then the hang-up",
		strings.Contains(string(transcript), "**Voice:** Bye, talk soon.\n\n**Voice:** (hangs up)\n"), string(transcript))
}

func TestNoHangUpIfYouSpeakFirst(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "h2-session",
		fakeReply{Match: "That's all", Steps: []fakeStep{say("Alright, bye for now."), tool("end_call", map[string]any{})}},
		fakeReply{Match: "one more thing", Steps: []fakeStep{say("Sure, go ahead.")}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "That's all."})
	hub.reply(callID, 2, 5*time.Second)
	a.waitLog("ends the call, held until heard", 2*time.Second)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "Alright,"})
	hub.send(map[string]any{"type": "interrupted", "call_id": callID, "reply": 2, "heard": "Alright,"})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Wait, one more thing.", "interrupted": true})
	want := "[You were interrupted. The developer heard your last reply up to: \"…Alright,\"]\n" +
		"[" + hangUpDropped + "]\nWait, one more thing."
	got := a.waitTurn(3, 5*time.Second)
	check(t, "the model is told the call didn't end", got == want, "\n got: ", got, "\nwant: ", want)
	text, _ := hub.reply(callID, 3, 5*time.Second)
	check(t, "and answers you", text == "Sure, go ahead.", text)
	hub.send(map[string]any{"type": "heard", "call_id": callID, "reply": float64(3), "text": text})
	time.Sleep(100 * time.Millisecond)
	check(t, "the hub is never told to hang up", hangUps(hub, callID) == 0)
}

// Only the session's work is left: the voice passes it on and ends the call in one reply, and
// both go once you've heard it to the end, the message first.
func TestPassesItOnThenHangsUp(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	goodbye := "I'll pass that on. We agreed on the parser fix, and the session will run the tests. Bye."
	callID := onCall(t, a, "h3-session",
		fakeReply{Match: "run the tests", Steps: []fakeStep{say(goodbye),
			tool("pass_to_session", map[string]any{"message": "Run the tests."}), tool("end_call", map[string]any{}),
			say(" Talk soon.")}},
	)
	mark := hub.mark()
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Yes, have it run the tests."})
	text, _ := hub.reply(callID, 2, 5*time.Second)
	check(t, "nothing after the tools is said", text == goodbye, text)
	a.waitLog("ends the call, held until heard", 2*time.Second)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "I'll pass that on. We agreed on the parser fix,"})
	time.Sleep(100 * time.Millisecond)
	check(t, "neither goes before you've heard it all", hangUps(hub, callID) == 0 && !hasKind(a.collect("h3-session"), "message"))
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": goodbye})
	m, _ := hub.wait(mark, 2*time.Second, is("hang_up"))
	check(t, "the hub is told to hang up", m != nil, m)
	message := a.waitMessage("h3-session", "message", 2*time.Second)
	check(t, "and the session gets the message", message != nil && message["text"] == "Run the tests.", message)
}

// In its opening, before you've said anything, the voice can't end the call.
func TestNoHangUpInTheOpening(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("h4-session")
	a.setReplies(fakeReply{Match: answeredTurn, Steps: []fakeStep{say("The build passed, nothing needs you."), tool("end_call", map[string]any{})}})
	callID, _ := a.placeCall("h4-session", "Build", "The build passed.")
	opening := a.answer(callID)
	a.played(callID, opening)
	eventually(t, "end_call in the opening is answered", 3*time.Second, func() bool { return lastToolResult(a) != "" })
	check(t, "with nothing done", lastToolResult(a) == nothingYet, lastToolResult(a))
	time.Sleep(100 * time.Millisecond)
	check(t, "and the hub isn't told to hang up", hangUps(hub, callID) == 0)
}
