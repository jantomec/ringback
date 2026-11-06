package main

// A stand-in for `claude -p` with stream-json, so the tests run the voice without a model:
// the test binary itself, run as claude when RINGBACK_FAKE_CLAUDE names a script. It reads the
// script again for every turn, so a test can steer the next reply. For each turn it writes
// the first reply whose "match" is in the turn's message (or "Okay."): text a word at a time
// as Claude Code streams it, and tool calls, which it makes through the MCP server that
// --mcp-config names (this binary's `mcp`), as Claude Code does. It logs how it was started,
// each turn and each tool's result, as JSON lines, for the test to read.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type fakeScript struct {
	Log     string      `json:"log"`
	Replies []fakeReply `json:"replies"`
}

type fakeReply struct {
	Match string     `json:"match"`
	Steps []fakeStep `json:"steps"`
}

type fakeStep struct {
	Text  string         `json:"text,omitempty"`
	Tool  string         `json:"tool,omitempty"`
	Input map[string]any `json:"input,omitempty"`
	Wait  string         `json:"wait,omitempty"`  // a file to wait for
	Sleep int            `json:"sleep,omitempty"` // ms
}

func fakeClaude(path string) int {
	read := func() fakeScript {
		var s fakeScript
		data, _ := os.ReadFile(path)
		json.Unmarshal(data, &s)
		return s
	}
	script := read()
	logFile, err := os.OpenFile(script.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 1
	}
	defer logFile.Close()
	record := func(entry map[string]any) {
		data, _ := json.Marshal(entry)
		logFile.Write(append(data, '\n'))
	}
	args := map[string]string{}
	for i := 0; i+1 < len(os.Args); i++ {
		if strings.HasPrefix(os.Args[i], "--") {
			args[os.Args[i]] = os.Args[i+1]
		}
	}
	how, id := "--session-id", args["--session-id"]
	if r, ok := args["--resume"]; ok {
		how, id = "--resume", r
	}
	out := bufio.NewWriter(os.Stdout)
	emit := func(e map[string]any) {
		data, _ := json.Marshal(e)
		out.Write(append(data, '\n'))
		out.Flush()
	}
	// Where Claude Code keeps the session, which the agent deletes when the call's done.
	cwd, _ := os.Getwd()
	real, _ := filepath.EvalSymlinks(cwd)
	kept := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", notAlphanumeric.ReplaceAllString(real, "-"))
	os.MkdirAll(kept, 0o700)
	os.WriteFile(filepath.Join(kept, id+".jsonl"), []byte("{}\n"), 0o600)
	if id == "" {
		// Asked only which models it offers.
		in := bufio.NewReader(os.Stdin)
		in.ReadBytes('\n')
		emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "response": map[string]any{
			"claude_code_version": "9.9.9",
			"models": []any{
				map[string]any{"value": "default", "resolvedModel": "claude-test-1", "displayName": "Default (recommended)", "description": "Test 1 · Best for tests"},
				map[string]any{"value": "opus", "resolvedModel": "claude-opus-9", "displayName": "Opus 9", "description": "For hard things"},
				map[string]any{"value": "haiku", "resolvedModel": "claude-haiku-9", "displayName": "Haiku 9", "description": "Fastest"},
			},
		}}})
		return 0
	}
	mcp := startMCP(args["--mcp-config"])
	record(map[string]any{"start": map[string]any{
		"model": args["--model"], "how": how, "id": id, "system": args["--system-prompt"], "tools": mcp.tools,
		"allowed": args["--allowedTools"], "cwd": func() string { d, _ := os.Getwd(); return d }(),
	}})
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": id})
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if len(line) > 0 {
			var turn struct {
				Type    string
				Message struct{ Content string } `json:"message"`
			}
			json.Unmarshal(line, &turn)
			if turn.Type == "control_request" {
				// It reads one line at a time, so there's no reply to stop.
				record(map[string]any{"control": true})
				emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success"}})
				if err != nil {
					return 0
				}
				continue
			}
			message := turn.Message.Content
			record(map[string]any{"message": message})
			steps := []fakeStep{{Text: "Okay."}}
			for _, r := range read().Replies {
				if strings.Contains(message, r.Match) {
					steps = r.Steps
					break
				}
			}
			reply := ""
			for _, step := range steps {
				switch {
				case step.Text != "":
					emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": "text", "text": ""}}})
					words := strings.SplitAfter(step.Text, " ")
					for _, w := range words {
						emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": w}}})
						time.Sleep(2 * time.Millisecond)
					}
					emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_stop"}})
					reply += step.Text
				case step.Tool != "":
					emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": "tool_use", "name": "mcp__ringback__" + step.Tool}}})
					emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_stop"}})
					result := mcp.call(step.Tool, step.Input)
					record(map[string]any{"tool": step.Tool, "result": result})
					emit(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "content": result}}}})
				case step.Wait != "":
					for {
						if _, err := os.Stat(step.Wait); err == nil {
							break
						}
						time.Sleep(5 * time.Millisecond)
					}
				case step.Sleep > 0:
					time.Sleep(time.Duration(step.Sleep) * time.Millisecond)
				}
			}
			emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": reply})
		}
		if err != nil {
			record(map[string]any{"exit": true})
			mcp.close()
			return 0
		}
	}
}

// fakeMCP is the voice's MCP server, as Claude Code runs it.
type fakeMCP struct {
	cmd   *exec.Cmd
	in    *bufio.Writer
	out   *bufio.Reader
	next  int
	tools []string
}

func startMCP(config string) *fakeMCP {
	var c struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
		} `json:"mcpServers"`
	}
	json.Unmarshal([]byte(config), &c)
	server := c.MCPServers["ringback"]
	m := &fakeMCP{cmd: exec.Command(server.Command, server.Args...)}
	stdin, _ := m.cmd.StdinPipe()
	stdout, _ := m.cmd.StdoutPipe()
	if err := m.cmd.Start(); err != nil {
		return m
	}
	m.in, m.out = bufio.NewWriter(stdin), bufio.NewReader(stdout)
	m.request("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	m.notify("notifications/initialized")
	if listed, ok := m.request("tools/list", nil)["tools"].([]any); ok {
		for _, tool := range listed {
			m.tools = append(m.tools, tool.(map[string]any)["name"].(string))
		}
	}
	return m
}

func (m *fakeMCP) notify(method string) {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	m.in.Write(append(data, '\n'))
	m.in.Flush()
}

func (m *fakeMCP) request(method string, params map[string]any) map[string]any {
	if m.in == nil {
		return nil
	}
	m.next++
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.next, "method": method, "params": params})
	m.in.Write(append(data, '\n'))
	m.in.Flush()
	line, _ := m.out.ReadBytes('\n')
	var response struct {
		Result map[string]any
		Error  map[string]any
	}
	json.Unmarshal(line, &response)
	if response.Error != nil {
		return map[string]any{"error": response.Error}
	}
	return response.Result
}

func (m *fakeMCP) call(name string, input map[string]any) string {
	result := m.request("tools/call", map[string]any{"name": name, "arguments": input})
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return fmt.Sprintf("no result: %v", result)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	return text
}

func (m *fakeMCP) close() {
	if m.cmd.Process != nil {
		m.cmd.Process.Kill()
		m.cmd.Wait()
	}
}
