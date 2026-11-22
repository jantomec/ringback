package main

import (
	"strings"
	"testing"
	"time"
)

// A reply told in parts ([pause] between them): the first is said at once, and each next one
// as soon as you say "mm-hmm" or stay silent for the pause, with no turn of the model's. Saying
// anything else drops the rest, and the model is told what you didn't hear. A reply that calls
// a tool is said whole.

const twoParts = "Two things. First, the build passes. [pause] Second, the docs are fixed."

func said(hub *fakeHub, callID string, n int) map[string]any {
	m, _ := hub.wait(0, 3*time.Second, func(m map[string]any) bool {
		return m["type"] == "said" && m["call_id"] == callID && m["reply"] == float64(n)
	})
	return m
}

func TestPartsGoOnAfterMmHmm(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "p1-session", fakeReply{Match: "What's new", Steps: []fakeStep{say(twoParts)}})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "What's new?"})
	first, _ := hub.reply(callID, 2, 5*time.Second)
	check(t, "the first part is said", first == "Two things. First, the build passes.", first)
	check(t, "with a pause after it", said(hub, callID, 2)["pause"] == 1.0, said(hub, callID, 2))
	a.played(callID, first)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Mm-hmm."})
	second, _ := hub.reply(callID, 3, 3*time.Second)
	check(t, "a mm-hmm brings the next", strings.TrimSpace(second) == "Second, the docs are fixed.", second)
	check(t, "the last has no pause", said(hub, callID, 3)["pause"] == nil, said(hub, callID, 3))
	check(t, "and the model has no turn for it", len(a.turns()) == 2, a.turns())
	a.played(callID, second)
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 20.0})
	a.waitMessage("p1-session", "outcome", 3*time.Second)
	transcript := callTranscript(a)
	check(t, "the transcript has both parts, and your mm-hmm between", strings.Contains(transcript,
		"**Voice:** Two things. First, the build passes.\n\n**Developer:** Mm-hmm.\n\n**Voice:** Second, the docs are fixed."), transcript)
}

func TestPartsGoOnAfterSilence(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "p2-session", fakeReply{Match: "What's new", Steps: []fakeStep{say(twoParts)}})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "What's new?"})
	first, _ := hub.reply(callID, 2, 5*time.Second)
	a.played(callID, first)
	hub.send(map[string]any{"type": "quiet", "call_id": callID, "waited": true})
	second, _ := hub.reply(callID, 3, 3*time.Second)
	check(t, "silence brings the next", strings.TrimSpace(second) == "Second, the docs are fixed.", second)
}

func TestPartsDroppedWhenYouSpeak(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "p3-session",
		fakeReply{Match: "What's new", Steps: []fakeStep{say(twoParts)}},
		fakeReply{Match: "which build", Steps: []fakeStep{say("The nightly one.")}},
	)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "What's new?"})
	first, _ := hub.reply(callID, 2, 5*time.Second)
	a.played(callID, first)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Wait, which build?"})
	turn := a.waitTurn(3, 3*time.Second)
	check(t, "the model is told what you didn't hear", strings.Contains(turn, `"Second, the docs are fixed."`) &&
		strings.HasSuffix(turn, "Wait, which build?"), turn)
	reply, _ := hub.reply(callID, 3, 3*time.Second)
	check(t, "and answers you", reply == "The nightly one.", reply)
	a.played(callID, reply)
	hub.send(map[string]any{"type": "quiet", "call_id": callID, "waited": true})
	time.Sleep(200 * time.Millisecond)
	check(t, "the rest is never said", hub.count(isType("say", 4)) == 0)
}

func TestPartsGiveWayToATool(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	callID := onCall(t, a, "p4-session", fakeReply{Match: "push", Steps: []fakeStep{
		say("Two things. First, it's merged. [pause] Second, I'll have it push."),
		tool("pass_to_session", map[string]any{"message": "Push it."}),
	}})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Okay, push it."})
	reply, _ := hub.reply(callID, 2, 5*time.Second)
	check(t, "with a tool, it's said whole", reply == "Two things. First, it's merged. Second, I'll have it push.", reply)
	check(t, "with no pause", said(hub, callID, 2)["pause"] == nil)
}
