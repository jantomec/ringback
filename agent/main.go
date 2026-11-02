// The ringback agent, on each Mac that runs Claude Code sessions: what the voice server did
// for the sessions' side, ported to Go (the Go port, under Stages in docs/design.md). The
// sessions' mods give it briefings and ask it things over a Unix socket (sessions.go, api.go);
// it places their calls through the hub, and on a call Haiku (Opus on request) talks it
// through as `claude -p`, on this Mac (voice.go, haiku.go). The hub does the call's audio and
// speech; the agent's link to it carries the call's text both ways (hub.go,
// docs/protocol.md). What it keeps for sessions and their calls is written beside the socket,
// and read again at start (state.go).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const usage = `usage: ringback-agent serve [--socket PATH] [--config PATH] | mcp SOCKET [TOOL...]

  serve  place this Mac's sessions' calls through the hub, and talk on them with Haiku.
         Sessions' mods reach it at PATH (default ~/.local/state/ringback/voice.sock).
         The config (default ~/.config/ringback/agent.toml) says:
           hub = "10.0.0.1:8787"   the hub's address on Nebula
           token = "…"             the hub's [tokens] agent
           name = "…"              this Mac, for the hub's log (default: its host name)
  mcp    the voice's tools for Claude Code, relayed to the call over SOCKET (Haiku starts it)`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fail(usage)
	}
	switch args[0] {
	case "serve":
		serve(parseOptions(args[1:], "--socket", "--config"))
	case "mcp":
		os.Exit(mcpMain(args[1:], os.Stdin, os.Stdout))
	case "-h", "--help":
		fmt.Println(usage)
	default:
		fail(usage)
	}
}

func serve(options map[string]string) {
	socket := expand(or(options["--socket"], "~/.local/state/ringback/voice.sock"))
	configPath := expand(or(options["--config"], "~/.config/ringback/agent.toml"))
	config, err := loadConfig(configPath)
	if err != nil {
		fail(fmt.Sprintf("No config at %s: %v", configPath, err))
	}
	if config["hub"] == "" || config["token"] == "" {
		fail("The config at " + configPath + " needs hub and token.")
	}
	name := config["name"]
	if name == "" {
		name, _ = os.Hostname()
		name = strings.TrimSuffix(name, ".local")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hub := newHubLink(config["hub"], config["token"], name, logLine)
	// What it keeps across a restart, beside the socket.
	calls := newCalls(hub, logLine, filepath.Join(filepath.Dir(socket), stateFile))
	a := newAgent(calls, hub, logLine)
	resend := make(chan struct{}, 1)
	hub.up = func() {
		select {
		case resend <- struct{}{}:
		default:
		}
	}
	hub.down = a.linkDown
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		fail(err.Error())
	}
	calls.restore()
	// The sessions that listen, for the phone's Contacts.
	go calls.publish(ctx, resend)
	go hub.run(ctx)
	if _, err := listen(sessionSide(calls), socket); err != nil {
		fail(fmt.Sprintf("can't listen on %s: %v", socket, err))
	}
	logLine(fmt.Sprintf("voice listening for sessions on %s, through the hub at %s", socket, config["hub"]))
	<-ctx.Done()
}

// loadConfig reads agent.toml: `key = "value"` lines and `#` comments, as the hub reads its
// own.
func loadConfig(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	config := map[string]string{}
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ") + "."
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			if end := strings.Index(value[1:], `"`); end >= 0 {
				value = value[1 : end+1]
			}
		} else if hash := strings.Index(value, "#"); hash >= 0 {
			value = strings.TrimSpace(value[:hash])
		}
		config[section+key] = value
	}
	return config, nil
}

// parseOptions reads `--name value` pairs; anything else prints the usage and exits.
func parseOptions(args []string, allowed ...string) map[string]string {
	options := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		known := false
		for _, a := range allowed {
			known = known || a == args[i]
		}
		if !known || i+1 >= len(args) {
			fail(usage)
		}
		options[args[i]] = args[i+1]
	}
	return options
}

var logMu sync.Mutex

// logLine writes a line of the log, with the time to the millisecond, as the voice server
// did.
func logLine(line string) {
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Printf("%s  %s\n", time.Now().Format("15:04:05.000"), line)
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
