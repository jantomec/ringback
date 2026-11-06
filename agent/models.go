package main

// Which model talks on a call. Claude Code says which models it offers and what it uses by
// default, without a model turn: `initialize` on a `claude -p` in stream-json, in about 0.5 s
// (Claude Code 2.1.319). The agent asks when it links to the hub and every hour after, and tells
// the hub, so the app can show them, and the choice you make there comes back through the hub.
// Names such as "opus" follow the newest version by themselves. "default" is what a session on
// this Mac would use: the model in your Claude Code settings, or else Claude Code's own default.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// modelRefresh is how often the agent asks Claude Code for its models.
const modelRefresh = time.Hour

// modelInfo is a model Claude Code offers. Default marks the entry that stands for its default.
type modelInfo struct {
	Value    string `json:"value"` // what --model takes
	Name     string `json:"name"`  // such as "Opus 5.5"
	About    string `json:"about"`
	Resolved string `json:"resolved"` // such as "claude-opus-5-5"
	Default  bool   `json:"default,omitempty"`
}

// chosenModel is the model a call uses: flag is what --model takes, or nothing for Claude Code's
// own default.
type chosenModel struct {
	flag, display, resolved string
}

// opusModel is the model that takes over when you ask for a better one.
var opusModel = chosenModel{flag: "opus", display: "Opus", resolved: "opus"}

func (m chosenModel) flags() []string {
	if m.flag == "" {
		return nil
	}
	return []string{"--model", m.flag}
}

func (m chosenModel) name() string {
	switch {
	case m.display != "":
		return m.display
	case m.flag != "":
		return strings.ToUpper(m.flag[:1]) + m.flag[1:]
	}
	return "The model"
}

func (m chosenModel) isOpus() bool {
	return strings.Contains(strings.ToLower(m.resolved+" "+m.flag), "opus")
}

// settingsModel is the model in your Claude Code settings, if there is one. The call's Claude
// Code doesn't read those settings, whose hooks could start another call.
func settingsModel() string {
	config := os.Getenv("CLAUDE_CONFIG_DIR")
	if config == "" {
		config = filepath.Join(homeDir(), ".claude")
	}
	data, err := os.ReadFile(filepath.Join(config, "settings.json"))
	if err != nil {
		return ""
	}
	var settings struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return ""
	}
	return settings.Model
}

// offeredModels asks Claude Code which models it offers.
func offeredModels() ([]modelInfo, string, error) {
	dir, err := os.MkdirTemp("", "ringback-models-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	cmd := exec.Command(claudeCommand, "-p", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--setting-sources", "",
		"--no-session-persistence")
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	timeout := time.AfterFunc(15*time.Second, func() { cmd.Process.Kill() })
	defer timeout.Stop()
	defer cmd.Wait()
	defer stdin.Close()
	line, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": "models-" + uuid4(),
		"request": map[string]any{"subtype": "initialize"}})
	if _, err := stdin.Write(append(line, '\n')); err != nil {
		return nil, "", err
	}
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 1<<20), 1<<24)
	for lines.Scan() {
		var m struct {
			Type     string
			Response struct {
				Response struct {
					Models []struct {
						Value, DisplayName, Description, ResolvedModel string
					}
					ClaudeCodeVersion string `json:"claude_code_version"`
				}
			}
		}
		if json.Unmarshal(lines.Bytes(), &m) != nil || m.Type != "control_response" {
			continue
		}
		var models []modelInfo
		for _, o := range m.Response.Response.Models {
			models = append(models, modelInfo{Value: o.Value, Name: o.DisplayName, About: o.Description, Resolved: o.ResolvedModel})
		}
		return applyDefault(models, settingsModel()), m.Response.Response.ClaudeCodeVersion, nil
	}
	return nil, "", fmt.Errorf("Claude Code didn't say which models it offers")
}

// applyDefault marks the entry for the default, as what a session here would use: if your
// settings name a model it offers, that one.
func applyDefault(models []modelInfo, setting string) []modelInfo {
	var chosen *modelInfo
	for i := range models {
		if models[i].Value == setting || models[i].Resolved == setting {
			chosen = &models[i]
		}
	}
	for i := range models {
		if models[i].Value != "default" {
			continue
		}
		models[i].Default = true
		if chosen != nil {
			models[i].Resolved, models[i].Name = chosen.Resolved, chosen.Name
			continue
		}
		// It's named for what it is, and its description says which model that is today:
		// "Opus 5.5 · Best for everyday, complex tasks".
		name, _, _ := strings.Cut(models[i].About, " · ")
		models[i].Name = strings.TrimSpace(name)
	}
	return models
}

// chosen is the model a new call uses: your choice, if this Mac offers it, or else the default.
func (c *Calls) chosen() chosenModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.chosenLocked()
}

// chosenLocked is chosen, for a caller that holds c.mu.
func (c *Calls) chosenLocked() chosenModel {
	var fallback chosenModel
	for _, m := range c.models {
		if m.Default {
			fallback = chosenModel{display: m.Name, resolved: m.Resolved}
			if setting := settingsModel(); setting != "" {
				fallback.flag = setting
			}
		}
	}
	if c.modelChoice != "" && c.modelChoice != "default" {
		for _, m := range c.models {
			if m.Value == c.modelChoice {
				return chosenModel{flag: m.Value, display: m.Name, resolved: m.Resolved}
			}
		}
	}
	return fallback
}

// setModels keeps what Claude Code offers now.
func (c *Calls) setModels(models []modelInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = models
}

// setModelChoice is your choice in the app, from the hub.
func (c *Calls) setModelChoice(value string) {
	c.mu.Lock()
	changed := value != c.modelChoice
	c.modelChoice = value
	c.mu.Unlock()
	if changed {
		c.log("the model for calls: " + value)
	}
}

// tellModels asks Claude Code which models it offers and tells the hub.
func (a *agent) tellModels() {
	models, version, err := offeredModels()
	if err != nil {
		a.log(fmt.Sprintf("can't ask Claude Code for its models: %v", err))
		return
	}
	a.calls.setModels(models)
	a.toHub(map[string]any{"type": "models", "models": models, "claude_code": version})
}

// keepTellingModels tells the hub what Claude Code offers every modelRefresh, so a new model
// shows without restarting anything. The link coming up tells it too.
func (a *agent) keepTellingModels(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(modelRefresh):
			a.tellModels()
		}
	}
}
