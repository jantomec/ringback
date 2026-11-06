package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyDefault(t *testing.T) {
	offered := func() []modelInfo {
		return []modelInfo{
			{Value: "default", Name: "Default (recommended)", About: "Opus 5.5 · Best for everyday tasks", Resolved: "claude-opus-5-5"},
			{Value: "opus", Name: "Opus 5.5", Resolved: "claude-opus-5-5"},
			{Value: "sonnet", Name: "Sonnet 5.5", Resolved: "claude-sonnet-5-5"},
		}
	}
	m := applyDefault(offered(), "")
	check(t, "the default is named for what it is today", m[0].Default && m[0].Name == "Opus 5.5" && m[0].Resolved == "claude-opus-5-5", m[0])
	m = applyDefault(offered(), "sonnet")
	check(t, "a model in your settings is the default", m[0].Default && m[0].Name == "Sonnet 5.5" && m[0].Resolved == "claude-sonnet-5-5", m[0])
	m = applyDefault(offered(), "claude-sonnet-5-5")
	check(t, "by its full name too", m[0].Name == "Sonnet 5.5", m[0])
	m = applyDefault(offered(), "gone")
	check(t, "a model it no longer offers isn't", m[0].Name == "Opus 5.5", m[0])
}

func TestChosenModel(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	c := &Calls{log: func(string) {}}
	c.models = applyDefault([]modelInfo{
		{Value: "default", About: "Opus 5.5 · Best", Resolved: "claude-opus-5-5"},
		{Value: "sonnet", Name: "Sonnet 5.5", Resolved: "claude-sonnet-5-5"},
	}, "")
	m := c.chosen()
	check(t, "with no choice, Claude Code's own default", m.flags() == nil && m.name() == "Opus 5.5" && m.isOpus(), m)
	c.setModelChoice("sonnet")
	m = c.chosen()
	check(t, "your choice", len(m.flags()) == 2 && m.flags()[1] == "sonnet" && m.name() == "Sonnet 5.5" && !m.isOpus(), m)
	c.setModelChoice("gone")
	m = c.chosen()
	check(t, "a choice this Mac doesn't offer is the default", m.flags() == nil && m.name() == "Opus 5.5", m)
	c.setModelChoice("default")
	os.WriteFile(filepath.Join(config, "settings.json"), []byte(`{"model": "sonnet"}`), 0o600)
	m = c.chosen()
	check(t, "the default follows your settings", len(m.flags()) == 2 && m.flags()[1] == "sonnet", m)
}

// A call uses your choice, and the agent tells the hub what Claude Code offers.
func TestCallUsesTheChoice(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	told, _ := hub.wait(0, 3e9, is("models"))
	if told == nil {
		t.Fatal("the hub didn't hear which models Claude Code offers")
	}
	list := told["models"].([]any)
	first := list[0].(map[string]any)
	check(t, "the hub hears the models, the default named", len(list) == 3 && first["default"] == true && first["name"] == "Test 1" && told["claude_code"] == "9.9.9", told)
	hub.send(map[string]any{"type": "model", "value": "haiku"})
	a.waitLog("the model for calls: haiku", 3e9)
	a.collect("m1-session")
	callID, _ := a.placeCall("m1-session", "The fix", "The fix is in.")
	a.waitTurn(1, 5e9)
	check(t, "the call uses your choice", a.starts()[0]["model"] == "haiku", a.starts()[0])
	_ = callID
}
