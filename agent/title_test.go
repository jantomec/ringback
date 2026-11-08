package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTranscript writes a session's transcript where Claude Code keeps it, with lines.
func writeTranscript(t *testing.T, session string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-home-someone-Projects-demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return path
}

func TestCallerName(t *testing.T) {
	for _, c := range []struct{ project, title, want string }{
		{"webshop", "Checkout page redesign", "webshop · Checkout page redesign"},
		{"ringback", "", "ringback"},
		{"ringback", "Ringback", "ringback"},
		{"demo", "  Two\nlines  ", "demo · Two lines"},
		{"demo", strings.Repeat("word ", 20), "demo · " + strings.TrimSpace(strings.Repeat("word ", 12)) + "…"},
	} {
		if got := callerName(c.project, c.title); got != c.want {
			t.Errorf("callerName(%q, %q) = %q, want %q", c.project, c.title, got, c.want)
		}
	}
}

// The name you gave a session wins over Claude Code's, and the latest of each counts; a cut
// first line, and a long transcript whose title is far from its end, are read.
func TestSessionTitle(t *testing.T) {
	var ts titles
	writeTranscript(t, "t1-session",
		`{"type":"user","message":{"content":"hi"}}`,
		`{"type":"ai-title","aiTitle":"First idea","sessionId":"t1-session"}`,
		`{"type":"ai-title","aiTitle":"Parser fix","sessionId":"t1-session"}`)
	if got := ts.of("t1-session"); got != "Parser fix" {
		t.Errorf("Claude Code's latest title: %q", got)
	}
	writeTranscript(t, "t2-session",
		`{"type":"custom-title","customTitle":"Release prep","sessionId":"t2-session"}`,
		`{"type":"ai-title","aiTitle":"Parser fix","sessionId":"t2-session"}`)
	if got := ts.of("t2-session"); got != "Release prep" {
		t.Errorf("the name you gave it: %q", got)
	}
	if got := ts.of("no-such-session"); got != "" {
		t.Errorf("no transcript, no title: %q", got)
	}
	// Far from the end: past the first tail, within the second.
	filler := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 1000) + `"}}`
	lines := []string{`{"type":"ai-title","aiTitle":"Deep in the file","sessionId":"t3-session"}`}
	for i := 0; i < 400; i++ {
		lines = append(lines, filler)
	}
	writeTranscript(t, "t3-session", lines...)
	if got := ts.of("t3-session"); got != "Deep in the file" {
		t.Errorf("a title 400 KB from the end: %q", got)
	}
}

// A call's caller name, and the session's in the list for the app's Sessions tab, have the
// session's title; renamed, the list follows.
func TestCallerNameOnTheHub(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	path := writeTranscript(t, "t4-session", `{"type":"ai-title","aiTitle":"Parser fix","sessionId":"t4-session"}`)
	a.collect("t4-session")
	a.setReplies(fakeReply{Match: answeredTurn, Steps: []fakeStep{say("Hi.")}})
	from := hub.mark()
	a.placeCall("t4-session", "Parser fix ready", "The parser fix is done.")
	place, _ := hub.wait(from, time.Second, is("place"))
	check(t, "the call rings as the project and the session's title", place["project"] == "demo · Parser fix", place)
	listed := func(name string) bool {
		m, _ := hub.wait(0, 5*time.Second, func(m map[string]any) bool {
			list, _ := m["sessions"].([]any)
			return m["type"] == "sessions" && len(list) == 1 && list[0].(map[string]any)["project"] == name
		})
		return m != nil
	}
	check(t, "and the Sessions tab lists it so", listed("demo · Parser fix"))
	os.WriteFile(path, []byte(`{"type":"custom-title","customTitle":"Release prep","sessionId":"t4-session"}`+"\n"), 0o644)
	a.calls.titles.mu.Lock()
	a.calls.titles.known["t4-session"].checked = time.Time{} // as if 10 s had passed
	a.calls.titles.mu.Unlock()
	check(t, "renamed, the list follows", listed("demo · Release prep"))
}
