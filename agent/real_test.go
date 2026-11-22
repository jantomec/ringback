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

// You hang up half a second after asking for something, with the real model still on its
// answer: it's stopped, told you hung up, and passes your words on itself, rather than your
// words going as heard once its time is up. Three turns of Claude Code's default model (it
// runs with no settings, so no --model): Opus 5.5 on 2026-11-22.
// RINGBACK_REAL_CLAUDE=1 go test -run RealHangUp -v
func TestRealHangUp(t *testing.T) {
	if os.Getenv("RINGBACK_REAL_CLAUDE") == "" {
		t.Skip("RINGBACK_REAL_CLAUDE=1 runs it, with three turns of the default model")
	}
	old := claudeCommand
	claudeCommand = "claude"
	t.Cleanup(func() { claudeCommand = old })
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("real-hangup")
	callID, _ := a.placeCall("real-hangup", "Parser fix merged",
		"The parser fix is merged, and the nightly build passes. The release notes haven't been written; "+
			"the session has a plan for the release, which the developer hasn't heard yet.")
	opening := a.answer(callID)
	t.Logf("opening: %s", opening)
	a.played(callID, opening)
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Ask the session about the release plan. Call me back with the answer."})
	time.Sleep(500 * time.Millisecond)
	began := time.Now()
	messages := hangUp(a, callID, "real-hangup")
	t.Logf("%.1f s from the hang-up until the model was done with it", time.Since(began).Seconds())
	for _, m := range messages {
		t.Logf("passed: %v", m["text"])
	}
	check(t, "one message reaches the session", len(messages) == 1, messages)
	check(t, "and the model passed it, not the fallback", a.logged_("after you hung up, passed to the session"), "\n"+strings.Join(a.log(), "\n"))
	if len(messages) == 1 {
		check(t, "about the release plan", strings.Contains(strings.ToLower(messages[0]["text"].(string)), "release"), messages[0])
	}
}

// You ask for something only the session can answer, and nothing else needs you: the real
// model says it'll call you back once the session has answered, passes it on and ends the
// call, rather than keeping you on the line. Two turns of the default model.
// RINGBACK_REAL_CLAUDE=1 go test -run RealCallsBack -v
func TestRealCallsBack(t *testing.T) {
	if os.Getenv("RINGBACK_REAL_CLAUDE") == "" {
		t.Skip("RINGBACK_REAL_CLAUDE=1 runs it, with two turns of the default model")
	}
	old := claudeCommand
	claudeCommand = "claude"
	t.Cleanup(func() { claudeCommand = old })
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("real-callback")
	callID, _ := a.placeCall("real-callback", "Parser fix merged",
		"The parser fix is merged, and the nightly build passes. Nothing waits for the developer.")
	opening := a.answer(callID)
	t.Logf("opening: %s", opening)
	a.played(callID, opening)
	from := hub.mark()
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Ask the session when the next release is planned."})
	reply, ok := hub.reply(callID, 2, 60*time.Second)
	t.Logf("reply: %s", reply)
	check(t, "it answered", ok && strings.TrimSpace(reply) != "")
	t.Log(a.waitLog("→ session, held until heard:", 5*time.Second))
	check(t, "it ends the call", a.logged_("ends the call, held until heard"), "\n"+strings.Join(a.log(), "\n"))
	check(t, "saying it'll call back", strings.Contains(strings.ToLower(reply), "call"), reply)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": reply})
	passed := a.waitMessage("real-callback", "message", 5*time.Second)
	t.Logf("passed: %v", passed["text"])
	hungUp, _ := hub.wait(from, 5*time.Second, is("hang_up"))
	check(t, "and once you've heard it, the hub is told to hang up", hungUp != nil)
}

// The same, but you ask to wait on the line: it passes it on and stays on. Two turns of the
// default model. RINGBACK_REAL_CLAUDE=1 go test -run RealStaysOn -v
func TestRealStaysOn(t *testing.T) {
	if os.Getenv("RINGBACK_REAL_CLAUDE") == "" {
		t.Skip("RINGBACK_REAL_CLAUDE=1 runs it, with two turns of the default model")
	}
	old := claudeCommand
	claudeCommand = "claude"
	t.Cleanup(func() { claudeCommand = old })
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("real-stayson")
	callID, _ := a.placeCall("real-stayson", "Parser fix merged",
		"The parser fix is merged, and the nightly build passes. Nothing waits for the developer.")
	a.played(callID, a.answer(callID))
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Ask the session when the next release is planned. I'll wait on the line."})
	reply, _ := hub.reply(callID, 2, 60*time.Second)
	t.Logf("reply: %s", reply)
	t.Log(a.waitLog("→ session, held until heard:", 5*time.Second))
	check(t, "it doesn't end the call", !a.logged_("ends the call"), "\n"+strings.Join(a.log(), "\n"))
}

// A briefing with several things to tell: the real model's opening, and its turns after a
// "mm-hmm" and an "okay", for comparing ways of telling them ("Telling in parts" in
// docs/design.md). It checks only that each turn says something. Three turns of the default
// model.
// RINGBACK_REAL_CLAUDE=1 go test -run RealInstallments -v
func TestRealInstallments(t *testing.T) {
	if os.Getenv("RINGBACK_REAL_CLAUDE") == "" {
		t.Skip("RINGBACK_REAL_CLAUDE=1 runs it, with three turns of the default model")
	}
	old := claudeCommand
	claudeCommand = "claude"
	t.Cleanup(func() { claudeCommand = old })
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("real-parts")
	callID, _ := a.placeCall("real-parts", "Three things done, one choice",
		"Three things since the last call. The parser fix is merged into main, and the nightly build passes. "+
			"The docs site was rebuilt, and three broken links on the install page were fixed. The release notes "+
			"draft is written. One thing needs the developer: whether the release goes out today or on Monday.")
	opening := a.answer(callID)
	t.Logf("opening (%d words): %s", len(strings.Fields(opening)), opening)
	a.played(callID, opening)
	for i, said := range []string{"Mm-hmm.", "Okay."} {
		hub.send(map[string]any{"type": "turn", "call_id": callID, "text": said})
		reply, _ := hub.reply(callID, i+2, 60*time.Second)
		t.Logf("after %q (%d words): %s", said, len(strings.Fields(reply)), reply)
		check(t, "it says something after "+said, strings.TrimSpace(reply) != "")
		a.played(callID, reply)
	}
}
