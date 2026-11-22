package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// You hang up before hearing the voice answer your last words, or say it would pass something
// on: the model gets one more turn, told so, and what it passes goes at once. If it doesn't
// answer in time, your words go as heard; a bare goodbye passes nothing.

// hangUp ends the call on the phone's side, as the hub tells the agent, and returns what the
// voice passed to the session once every model has finished with it. What the last turn passes
// can come before the call's outcome or after it.
func hangUp(a *testAgent, callID, session string) []map[string]any {
	a.t.Helper()
	a.hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 20.0})
	var messages []map[string]any
	keep := func() bool {
		outcome := false
		for _, m := range a.collect(session) {
			outcome = outcome || m["kind"] == "outcome"
			if m["kind"] == "message" {
				messages = append(messages, m)
			}
		}
		return outcome
	}
	eventually(a.t, "the call's outcome", 3*time.Second, keep)
	// The voice has ended, so a last turn has started if it has one.
	a.calls.closing.Wait()
	keep()
	return messages
}

func callTranscript(a *testAgent) string {
	files, _ := filepath.Glob(filepath.Join(a.project(), ".ringback", "calls", "*.md"))
	if len(files) == 0 {
		return ""
	}
	data, _ := os.ReadFile(files[len(files)-1])
	return string(data)
}

// As on the call of 2026-11-22: you hang up 2 s after asking for something, before the model
// has written a word.
func TestHangUpBeforeTheAnswer(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	request := "Ask the session about the release plan. Call me back with the answer."
	callID := onCall(t, a, "l1-session",
		fakeReply{Match: "release plan", Steps: []fakeStep{{Sleep: 400}, say("Sure, I'll ask the session.")}},
		fakeReply{Match: hungUpLead, Steps: []fakeStep{
			tool("pass_to_session", map[string]any{"message": "Tell me about the release plan, and call me back with the answer."}),
			say("Passed."),
		}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": request})
	a.waitTurn(2, 3*time.Second)
	messages := hangUp(a, callID, "l1-session")
	check(t, "one message reaches the session", len(messages) == 1, messages)
	if len(messages) == 1 {
		text := messages[0]["text"].(string)
		check(t, "the model's, at once", strings.HasPrefix(text, "Tell me about the release plan, and call me back with the answer."), text)
		check(t, "with your words as heard", strings.Contains(text, `"`+request+`"`), text)
	}
	turns := a.turns()
	check(t, "the model was told you hung up before hearing its answer", len(turns) == 3 &&
		strings.Contains(turns[2], hungUpLead) && strings.Contains(turns[2], hungUpWords), turns)
	transcript := callTranscript(a)
	check(t, "the transcript says so", strings.Contains(transcript, "**Voice:** (after you hung up, passes it on)\n\n**Passed to the session:** Tell me"), transcript)
	check(t, "and nothing after the hang-up is said", hub.count(isType("say", 3)) == 0)
}

// You hang up before hearing the voice say it would pass your words on: what it held is dropped,
// and the model, told what it was, passes it again.
func TestHangUpBeforeTheAnnouncement(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "l2-session",
		fakeReply{Match: "run the tests", Steps: []fakeStep{say("Okay, I'll have the session run the tests."),
			tool("pass_to_session", map[string]any{"message": "Run the tests."})}},
		fakeReply{Match: hungUpLead, Steps: []fakeStep{tool("pass_to_session", map[string]any{"message": "Run the tests."})}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Have it run the tests."})
	hub.reply(callID, 2, 5*time.Second)
	a.waitLog("held until heard", 2*time.Second)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "Okay, I'll have"})
	time.Sleep(50 * time.Millisecond)
	messages := hangUp(a, callID, "l2-session")
	check(t, "the session gets it once", len(messages) == 1 && strings.HasPrefix(messages[0]["text"].(string), "Run the tests."), messages)
	turns := a.turns()
	check(t, "the model was told what it held", len(turns) == 3 && strings.Contains(turns[2], `so it hasn't gone: "Run the tests."`), turns)
}

// You say goodbye and hear the voice's goodbye to the end: the call ends, and nothing more.
func TestNothingAfterAGoodbyeHeard(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "l3-session",
		fakeReply{Match: "thanks, bye", Steps: []fakeStep{say("Bye, talk soon."), tool("end_call", map[string]any{})}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Okay, thanks, bye."})
	hub.reply(callID, 2, 5*time.Second)
	a.waitLog("ends the call, held until heard", 2*time.Second)
	a.played(callID, "Bye, talk soon.")
	hub.wait(0, 2*time.Second, is("hang_up"))
	check(t, "nothing goes to the session", len(hangUp(a, callID, "l3-session")) == 0)
	check(t, "and the model has no more turns", len(a.turns()) == 2, a.turns())
}

// You say only goodbye and hang up before the voice answers: nothing to pass on, so no turn.
func TestBareGoodbyeUnanswered(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "l4-session",
		fakeReply{Match: "Thanks, bye", Steps: []fakeStep{{Sleep: 300}, say("Bye!")}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Thanks, bye."})
	a.waitTurn(2, 3*time.Second)
	check(t, "nothing goes to the session", len(hangUp(a, callID, "l4-session")) == 0)
	check(t, "and the model has no more turns", len(a.turns()) == 2, a.turns())
}

// The model doesn't answer in time: your words go to the session as they were heard.
func TestYourWordsGoIfTheModelIsSlow(t *testing.T) {
	was := lastTurnMost
	lastTurnMost = 300 * time.Millisecond
	t.Cleanup(func() { lastTurnMost = was })
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	request := "Tell it to push the branch."
	callID := onCall(t, a, "l5-session",
		fakeReply{Match: "push the branch", Steps: []fakeStep{{Sleep: 1000}, say("Sure.")}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": request})
	a.waitTurn(2, 3*time.Second)
	messages := hangUp(a, callID, "l5-session")
	check(t, "your words reach the session", len(messages) == 1 && strings.Contains(messages[0]["text"].(string), `"`+request+`"`), messages)
	transcript := callTranscript(a)
	check(t, "the transcript says why", strings.Contains(transcript, "your last words go to the session as heard"), transcript)
}

// As on a call of 2026-11-22: you go on before hearing a reply that ends the call, and the hub
// doesn't stop it. Not cut off isn't heard to the end: the call doesn't end, and the model is told.
func TestNoHangUpBeforeItsHeard(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "l6-session",
		fakeReply{Match: "other thing", Steps: []fakeStep{say("The other thing was the flaky test.")}},
		fakeReply{Match: "that's it", Steps: []fakeStep{say("Good, then that's covered. Talk soon!"), tool("end_call", map[string]any{})}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "All right, that's it."})
	hub.reply(callID, 2, 5*time.Second)
	a.waitLog("ends the call, held until heard", 2*time.Second)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "reply": 2.0, "text": "Good, then"})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "again": true, "text": "All right, that's it. What was the other thing?"})
	turn := a.waitTurn(3, 3*time.Second)
	check(t, "the model is told the call didn't end", strings.Contains(turn, hangUpDropped), turn)
	time.Sleep(100 * time.Millisecond)
	check(t, "and the hub isn't told to hang up", hangUps(hub, callID) == 0)
}

func TestJustGoodbye(t *testing.T) {
	for text, want := range map[string]bool{
		"Bye.": true, "Thanks, bye.": true, "Um, thank you. Bye bye.": true, "That's all, talk to you later.": true,
		"Okay, bye.": false, "Yes, bye.": false, "Push it. Bye.": false, "": false, "Um.": false,
	} {
		check(t, "justGoodbye("+text+")", justGoodbye(text) == want, !want)
	}
}
