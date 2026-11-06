package main

// The voice's tools on a call: a small MCP server that `claude -p` starts.
//
//	ringback-agent mcp SOCKET [TOOL...]
//
// It speaks MCP over stdin and stdout, and relays each tool call to the call that started
// The model, over SOCKET in that call's private folder. A tool call ends the model's turn until its
// result comes back, so the model can't go on to imagine the session's answer, as it did when it
// passed messages in tags. Its tools pass the session a message, answer the permission or
// questions the session asks you, or put a better model on the line. It offers the TOOLs
// named, or all.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
)

func mcpMain(args []string, in io.Reader, out io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(out, "usage: ringback-agent mcp SOCKET [TOOL...]")
		return 2
	}
	path, names := args[0], args[1:]
	var all []json.RawMessage
	if err := json.Unmarshal([]byte(toolsJSON), &all); err != nil {
		panic(err)
	}
	tools := []json.RawMessage{}
	for _, tool := range all {
		var named struct{ Name string }
		json.Unmarshal(tool, &named)
		if len(names) == 0 || slices.Contains(names, named.Name) {
			tools = append(tools, tool)
		}
	}
	reader := bufio.NewReader(in)
	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var request map[string]any
			decoder := json.NewDecoder(strings.NewReader(string(line)))
			decoder.UseNumber()
			// A notification, such as notifications/initialized, has no id and no answer.
			if decoder.Decode(&request) == nil && request != nil && request["id"] != nil {
				response := map[string]any{"jsonrpc": "2.0", "id": request["id"]}
				if result := mcpAnswer(path, request, tools); result == nil {
					response["error"] = map[string]any{"code": -32601, "message": "unknown method"}
				} else {
					response["result"] = result
				}
				data, _ := json.Marshal(response)
				out.Write(append(data, '\n'))
			}
		}
		if err != nil {
			return 0
		}
	}
}

func mcpAnswer(path string, request map[string]any, tools []json.RawMessage) any {
	params, _ := request["params"].(map[string]any)
	switch request["method"] {
	case "initialize":
		version, ok := params["protocolVersion"]
		if !ok {
			version = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ringback", "version": "0.1.0"},
		}
	case "tools/list":
		return map[string]any{"tools": tools}
	case "tools/call":
		name, _ := params["name"].(string)
		arguments, _ := params["arguments"].(map[string]any)
		if arguments == nil {
			arguments = map[string]any{}
		}
		text, err := relay(path, name, arguments)
		if err != nil {
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": "The call couldn't take it: " + err.Error()}}, "isError": true}
		}
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": false}
	case "ping":
		return map[string]any{}
	}
	return nil
}

// relay gives a tool call to the call, and returns what it answers.
func relay(path, name string, arguments map[string]any) (string, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	request, _ := json.Marshal(map[string]any{"tool": name, "input": arguments})
	if _, err := conn.Write(append(request, '\n')); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	var answer struct {
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(line, &answer); err != nil {
		return "", err
	}
	if answer.Text == nil {
		return "", fmt.Errorf("'text'")
	}
	return *answer.Text, nil
}
