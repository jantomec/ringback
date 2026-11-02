package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The state file in the voice server's format: the agent restores what the voice server wrote,
// and the voice server what the agent wrote, so either can take over from the other. It needs
// the voice server's Python: RINGBACK_PYTHON=voice/.venv/bin/python go test -run StateWithPython
func TestStateWithPython(t *testing.T) {
	python := os.Getenv("RINGBACK_PYTHON")
	if python == "" {
		t.Skip("RINGBACK_PYTHON names the voice server's Python, to check the state file both ways")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, stateFile)
	script, _ := filepath.Abs("testdata/state_check.py")
	if out, err := exec.Command(python, "-I", script, "write", path).CombinedOutput(); err != nil {
		t.Fatalf("the voice server didn't write its state: %v\n%s", err, out)
	}
	hub := newFakeHub(t)
	hub.place = func(map[string]any) map[string]any {
		return map[string]any{"held": true, "held_until": "2030-01-01T08:00:00+01:00"}
	}
	a := startAgent(t, hub, dir)
	a.waitLog("restored 1 held calls, 1 to call back, 1 asks, 1 messages for sessions", time.Second)
	a.waitLog("call held-cal: still held until 2030-01-01T08:00:00+01:00 after a restart: Held", time.Second)
	_, got := a.mod("GET", "/sessions/s1/permissions/abcd", nil)
	check(t, "the voice server's ask, by its id", got["verdict"] == "pending" && got["held"] == true, got)
	messages := a.collect("s2")
	check(t, "its message for a session", len(messages) == 1 && messages[0]["text"] == "Done." && messages[0]["kind"] == "outcome", messages)
	reply := hub.request(map[string]any{"type": "offer", "call_id": "past-call-0123456789", "kind": "back"})
	check(t, "its call to call back", reply["ok"] == true, reply)
	hub.send(map[string]any{"type": "ended", "call_id": "past-call-0123456789", "lasted": nil})
	a.waitMessage("s2", "outcome", 3*time.Second)
	a.stop()
	a.closeAll()

	a.calls.mu.Lock()
	// The agent's own file, not what the voice server wrote: Go writes JSON without spaces.
	data, _ := os.ReadFile(path)
	check(t, "the agent has written the state", strings.HasPrefix(string(data), `{"calls":[{"id":"held-call-0123456789"`), string(data[:min(80, len(data))]))
	out, err := exec.Command(python, "-I", script, "read", path).Output()
	a.calls.mu.Unlock()
	if err != nil {
		t.Fatalf("the voice server didn't read the agent's state: %v\n%s", err, out)
	}
	var restored struct {
		Log  []string
		Asks []string
		Held map[string]struct {
			HeldUntil string `json:"held_until"`
			Asks      []string
			Briefed   []string
		}
		Past []string
	}
	if err := json.Unmarshal(out, &restored); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	check(t, "the voice server restores the agent's state", slices.ContainsFunc(restored.Log, func(line string) bool {
		return line == "restored 1 held calls, 1 to call back, 1 asks, 0 messages for sessions"
	}), restored.Log)
	held := restored.Held["held-call-0123456789"]
	check(t, "the held call, with what its briefing asks", held.HeldUntil == "2030-01-01T08:00:00+01:00" &&
		slices.Equal(held.Asks, []string{"abcd"}) && slices.Equal(held.Briefed, []string{"permission abcd"}), held)
	check(t, "the call called back, to call back again", slices.Equal(restored.Past, []string{"past-call-0123456789"}), restored.Past)
	check(t, "the ask", slices.Equal(restored.Asks, []string{"abcd"}), restored.Asks)
}
