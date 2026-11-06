package main

import (
	"strings"
	"testing"
	"time"
)

// The model decides when to speak: it answers your words at a pause or waits for more, and when
// you speak over the voice it answers you or carries on (see "Interruptions" in docs/design.md).

// onCall places a call with the session s and plays its opening to the end.
func onCall(t *testing.T, a *testAgent, session string, replies ...fakeReply) string {
	t.Helper()
	a.collect(session)
	a.setReplies(append([]fakeReply{{Match: answeredTurn, Steps: []fakeStep{say("The fix is in.")}}}, replies...)...)
	callID, _ := a.placeCall(session, "The fix", "The fix is in.")
	opening := a.answer(callID)
	a.played(callID, opening)
	return callID
}

func isType(kind string, reply float64) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["type"] == kind && m["reply"] == reply }
}

func TestModelWaitsForYou(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "w1-session",
		fakeReply{Match: "some kind of", Steps: []fakeStep{say("[wait]")}},
		fakeReply{Match: answerNow, Steps: []fakeStep{say("Some kind of what?")}},
		fakeReply{Match: "instructions", Steps: []fakeStep{say("Got it, instructions.")}},
	)
	from := hub.mark()
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "There should be some kind of"})
	said, _ := hub.wait(from, 5*time.Second, isType("said", 2))
	check(t, "the model waits, and the hub is told", said != nil && said["waiting"] == true, said)
	check(t, "nothing is said", hub.count(isType("say", 2)) == 0)

	// You go on: your words go, and the model has the earlier ones already.
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "instructions for that."})
	check(t, "what you say next goes as it is", a.waitTurn(3, 5*time.Second) == "instructions for that.")
	text, _ := hub.reply(callID, 3, 5*time.Second)
	check(t, "and it answers", text == "Got it, instructions.", text)
	a.played(callID, text)

	// You don't go on: after the hub's limit the model must answer.
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "But some kind of"})
	hub.wait(from, 5*time.Second, isType("said", 4))
	hub.send(map[string]any{"type": "quiet", "call_id": callID, "waited": true})
	check(t, "told to answer now", strings.Contains(a.waitTurn(5, 5*time.Second), answerNow))
	text, _ = hub.reply(callID, 5, 5*time.Second)
	check(t, "it answers", text == "Some kind of what?", text)
}

func TestNotesWaitWhileTheModelWaits(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "w2-session",
		fakeReply{Match: "some kind of", Steps: []fakeStep{say("[wait]")}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "There should be some kind of"})
	a.waitLog("waits for you to go on", 5*time.Second)
	hub.send(map[string]any{"type": "idle", "call_id": callID})
	if status, _ := a.mod("POST", "/sessions/w2-session/permissions", map[string]any{"tool": "Write", "project": "demo", "cwd": a.project()}); status != 200 {
		t.Fatalf("the permission wasn't taken: %d", status)
	}
	a.waitLog("note from the session", 2*time.Second)
	time.Sleep(200 * time.Millisecond)
	check(t, "a note doesn't start a reply while you're thinking", len(a.turns()) == 2, a.turns())
	hub.send(map[string]any{"type": "quiet", "call_id": callID, "waited": true})
	turn := a.waitTurn(3, 5*time.Second)
	check(t, "it goes with the next turn", strings.Contains(turn, answerNow) && strings.Contains(turn, "permission"), turn)
}

func TestVoiceCarriesOnAfterMmHmm(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "c1-session",
		fakeReply{Match: "the plan", Steps: []fakeStep{say("It has two parts. The first is the page. The second is the kit.")}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "What's the plan?"})
	text, _ := hub.reply(callID, 2, 5*time.Second)
	check(t, "reply 2", strings.HasPrefix(text, "It has two parts."), text)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "reply": float64(2), "text": "It has two parts. The first"})
	turns := len(a.turns())
	from := hub.mark()
	hub.send(map[string]any{"type": "interrupted", "call_id": callID, "reply": 2, "heard": "It has two parts. The first", "paused": true})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Mm-hmm.", "interrupted": true})
	resume, _ := hub.wait(from, 5*time.Second, isType("resume", 2))
	check(t, "a bare mm-hmm has the voice carry on", resume != nil, resume)
	check(t, "and the model needn't be asked", len(a.turns()) == turns, a.turns())
	done, _ := hub.wait(from, 5*time.Second, isType("said", 3))
	check(t, "your turn's reply is closed, so the hub isn't left waiting", done != nil, done)
}

func TestNoiseCarriesOn(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "c2-session",
		fakeReply{Match: "the plan", Steps: []fakeStep{say("It has two parts. The first is the page.")}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "What's the plan?"})
	hub.reply(callID, 2, 5*time.Second)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	turns := len(a.turns())
	from := hub.mark()
	hub.send(map[string]any{"type": "interrupted", "call_id": callID, "reply": 2, "heard": "It has two", "paused": true})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "", "interrupted": true})
	resume, _ := hub.wait(from, 5*time.Second, isType("resume", 2))
	check(t, "a noise has the voice carry on, with no model", resume != nil && len(a.turns()) == turns, resume)
}

func TestYesToAHeardQuestionIsAnAnswer(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "c3-session",
		fakeReply{Match: "go on", Steps: []fakeStep{say("Should I restart the server? I recommend yes.")}},
		fakeReply{Match: "Yes.", Steps: []fakeStep{say("Okay, restarting it.")}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "So go on."})
	hub.reply(callID, 2, 5*time.Second)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	from := hub.mark()
	hub.send(map[string]any{"type": "interrupted", "call_id": callID, "reply": 2, "heard": "Should I restart the server?", "paused": true})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Yes.", "interrupted": true})
	turn := a.waitTurn(3, 5*time.Second)
	check(t, "a yes after a question goes to the model, told what you heard and didn't",
		strings.Contains(turn, `They heard up to: "…Should I restart the server?"`) &&
			strings.Contains(turn, `They haven't heard the rest: "I recommend yes."`) && strings.HasSuffix(turn, "Yes."), turn)
	text, _ := hub.reply(callID, 3, 5*time.Second)
	check(t, "and it answers", text == "Okay, restarting it.", text)
	check(t, "so the voice doesn't carry on", hub.count(isType("resume", 2)) == 0, from)
}

func TestHeldThroughAStop(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	pass := tool("pass_to_session", map[string]any{"message": "Push the branch."})
	callID := onCall(t, a, "c4-session",
		fakeReply{Match: "Push it", Steps: []fakeStep{say("Sure, I'll pass that on to the session now."), pass}},
		fakeReply{Match: "Hold on", Steps: []fakeStep{say("[continue]")}},
		fakeReply{Match: "Forget it", Steps: []fakeStep{say("Alright, I won't.")}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Push it."})
	whole, _ := hub.reply(callID, 2, 5*time.Second)
	a.waitLog("held until heard", 2*time.Second)
	stop := func(n float64) {
		hub.send(map[string]any{"type": "speaking", "call_id": callID})
		hub.send(map[string]any{"type": "heard", "call_id": callID, "reply": n, "text": "Sure, I'll"})
		hub.send(map[string]any{"type": "interrupted", "call_id": callID, "reply": n, "heard": "Sure, I'll", "paused": true})
	}
	stop(2)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Hold on.", "interrupted": true})
	turn := a.waitTurn(3, 5*time.Second)
	check(t, "told that the message hasn't gone", strings.Contains(turn, "Your message to the session (Push the branch.) hasn't gone yet"), turn)
	check(t, "and it hasn't", len(a.collect("c4-session")) == 0)

	// It carries on: the message goes once you've heard it announced.
	resume, _ := hub.wait(0, 5*time.Second, isType("resume", 2))
	check(t, "the voice carries on", resume != nil, resume)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "reply": float64(2), "text": whole})
	message := a.waitMessage("c4-session", "message", 5*time.Second)
	check(t, "the message goes once it's heard", message != nil && strings.Contains(message["text"].(string), "Push the branch."), message)
	hub.send(map[string]any{"type": "idle", "call_id": callID})

	// This time you say something else: what it held is dropped, and says so.
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Push it again."})
	hub.reply(callID, 4, 5*time.Second)
	a.waitLog("held until heard: 'Push the branch.'", 2*time.Second)
	stop(4)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Forget it.", "interrupted": true})
	text, _ := hub.reply(callID, 5, 5*time.Second)
	check(t, "it answers you", text == "Alright, I won't.", text)
	a.waitLog("not passed, you cut the voice off first", 2*time.Second)
	check(t, "and nothing went", len(a.collect("c4-session")) == 0)
}

func TestYouGoOnWhileTheModelWrites(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "c5-session",
		fakeReply{Match: "So the plan", Steps: []fakeStep{{Sleep: 300}, say("Which plan?")}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "So the plan"})
	a.waitTurn(2, 5*time.Second)
	time.Sleep(100 * time.Millisecond)
	hub.send(map[string]any{"type": "interrupted", "call_id": callID, "reply": 2, "heard": ""})
	eventually(t, "the reply being written is stopped", 2*time.Second, func() bool {
		for _, entry := range a.claudeLog() {
			if entry["control"] == true {
				return true
			}
		}
		return false
	})
}
