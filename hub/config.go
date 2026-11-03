package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	home       = homeDir()
	configPath = findConfig()
	stateDir   = expand("~/.local/state/ringback")
)

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func expand(path string) string {
	if strings.HasPrefix(path, "~/") {
		return home + path[1:]
	}
	return path
}

// findConfig is the hub's config: hub.toml, or, while there's none, switchboard.toml, which is
// read the same way.
func findConfig() string {
	path := expand("~/.config/ringback/hub.toml")
	if _, err := os.Stat(path); err != nil {
		if old := expand("~/.config/ringback/switchboard.toml"); fileExists(old) {
			return old
		}
	}
	return path
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// loadConfig reads hub.toml. It uses only `[section]` headers, `key = "value"`
// lines and `#` comments, so that's all this reads. Keys come back with their section, e.g.
// "apns.sandbox.key_id".
func loadConfig() map[string]string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		fail("No config at " + configPath + "; copy hub/hub.example.toml there.")
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
	return config
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

// now is the clock, a variable so it reads the same everywhere.
var now = time.Now

func timeOfDay(t time.Time) string { return t.Format("15:04:05") }

// isoNow is the time now as the state files keep it: UTC, to the second.
func isoNow() string { return now().UTC().Format("2006-01-02T15:04:05Z") }

func parseISO8601(text string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, text)
	return t, err == nil
}

// The log's last logKept lines, for the portal: a ring, oldest at logStart once it's full.
const logKept = 500

var (
	logMu    sync.Mutex
	logLines = make([]string, 0, logKept)
	logStart int
)

func log(line string) {
	text := timeOfDay(now()) + "  " + line
	fmt.Println(text)
	logMu.Lock()
	defer logMu.Unlock()
	if len(logLines) < logKept {
		logLines = append(logLines, text)
		return
	}
	logLines[logStart] = text
	logStart = (logStart + 1) % logKept
}

// logTail is the log's last lines, oldest first.
func logTail() []string {
	logMu.Lock()
	defer logMu.Unlock()
	return append(append([]string{}, logLines[logStart:]...), logLines[:logStart]...)
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

// prefix is the first n characters of s, as the logs shorten call IDs.
func prefix(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// suffix is the last n characters of s, as the logs shorten push tokens.
func suffix(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
