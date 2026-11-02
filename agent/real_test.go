package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A call with the real Haiku (`claude -p --model haiku`), on your Claude subscription: two
// turns, the opening and one where you ask to pass something on. It checks Claude Code's
// stream-json and the tools' MCP server as Claude Code runs them, which the stand-in claude
// only imitates. RINGBACK_REAL_CLAUDE=1 go test -run RealHaiku -v
func TestRealHaiku(t *testing.T) {
	if os.Getenv("RINGBACK_REAL_CLAUDE") == "" {
		t.Skip("RINGBACK_REAL_CLAUDE=1 runs it, with two turns of Haiku")
	}
	old := claudeCommand
	claudeCommand = "claude"
	t.Cleanup(func() { claudeCommand = old })
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("real-session")
	callID, _ := a.placeCall("real-session", "Parser fix",
		"The config parser crashed on empty lines between sections. I fixed it on a branch called fix-parser, "+
			"and all tests pass. It's ready to merge into main once the developer says so.")
	began := time.Now()
	opening := a.answer(callID)
	t.Logf("opening (%.1f s): %s", time.Since(began).Seconds(), opening)
	check(t, "Haiku said an opening", strings.TrimSpace(opening) != "")
	a.played(callID, opening)
	began = time.Now()
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Great. Please tell the session to merge it into main."})
	reply, ok := hub.reply(callID, 2, 60*time.Second)
	t.Logf("reply 2 (%.1f s): %s", time.Since(began).Seconds(), reply)
	check(t, "Haiku answered", ok && strings.TrimSpace(reply) != "")
	line := a.waitLog("→ session, held until heard:", 5*time.Second)
	t.Log(line)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": reply})
	passed := a.waitMessage("real-session", "message", 5*time.Second)
	t.Logf("passed: %v", passed["text"])
	check(t, "the message mentions merging", strings.Contains(strings.ToLower(passed["text"].(string)), "merge"), passed)
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 20.0})
	outcome := a.waitMessage("real-session", "outcome", 5*time.Second)
	t.Logf("outcome: %v", outcome["text"])
}
