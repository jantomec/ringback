package main

import (
	"testing"
	"time"
)

// A tool call ends the words before it: the hub hears so with `break`, before the reply ends,
// so a voice that speaks whole replies needn't wait for the tool and what follows it.
func TestBreakAtAToolCall(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("s1-session")
	a.setReplies(
		fakeReply{Match: answeredTurn, Steps: []fakeStep{say("Hi, the parser fix is done.")}},
		fakeReply{Match: "push the branch", Steps: []fakeStep{say("Sure, I'll pass that on."),
			tool("pass_to_session", map[string]any{"message": "Push the branch."})}},
	)
	callID, _ := a.placeCall("s1-session", "Parser fix", "The parser fix is done; it needs a push.")
	a.waitTurn(1, 5*time.Second)
	opening := a.answer(callID)
	a.played(callID, opening)
	before := 0

	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Tell it to push the branch."})
	brk, at := hub.wait(before, 5*time.Second, func(m map[string]any) bool {
		return m["type"] == "say" && m["break"] == true && m["reply"] == float64(2)
	})
	check(t, "a break for reply 2 at its tool call", brk != nil && brk["text"] == "", brk)
	said, saidAt := hub.wait(before, 5*time.Second, func(m map[string]any) bool {
		return m["type"] == "said" && m["reply"] == float64(2)
	})
	check(t, "before the reply ends", said != nil && at < saidAt, at, saidAt)
	words, _ := hub.reply(callID, 2, time.Second)
	check(t, "with the reply's words before it", words == "Sure, I'll pass that on.", words)

	// The opening has no tool, so no break.
	opened, _ := hub.wait(0, 100*time.Millisecond, func(m map[string]any) bool {
		return m["type"] == "say" && m["break"] == true && m["reply"] == float64(1)
	})
	check(t, "no break in a reply with no tool", opened == nil, opened)
}
