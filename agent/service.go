package main

// The agent as a service, on macOS: `install` writes a launchd agent that starts it at login
// and again whenever it stops, and starts it now; `uninstall` stops it and takes it away.
// Installing again, as after copying a new binary over the old, restarts it.

import (
	"encoding/xml"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const serviceLabel = "ringback.agent"

var (
	servicePlist = expand("~/Library/LaunchAgents/" + serviceLabel + ".plist")
	serviceLog   = expand("~/Library/Logs/ringback-agent.log")
)

func installService() {
	if runtime.GOOS != "darwin" {
		fail("install sets the agent up with launchd, on macOS.")
	}
	configPath := expand("~/.config/ringback/agent.toml")
	config, err := loadConfig(configPath)
	if err != nil {
		fail("No config at " + configPath + ": pair this Mac with the hub first (`ringback-agent pair`), or copy agent/agent.example.toml there and fill it in.")
	}
	if config["hub"] == "" || (config["key"] == "" && (config["token"] == "" || strings.Contains(config["token"], "…"))) {
		fail("The config at " + configPath + " needs hub and key, which `ringback-agent pair` writes, or the hub's token.")
	}
	// launchd starts it with a bare PATH, and on a call it runs `claude`.
	claude, err := exec.LookPath("claude")
	if err != nil {
		fail("Can't find claude on your PATH: install Claude Code first, or run install from a shell that has it.")
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fail("Can't tell where this binary is: " + err.Error())
	}
	stopService()
	// An agent started by hand would keep the sessions' socket.
	socket := expand("~/.local/state/ringback/voice.sock")
	if conn, err := net.DialTimeout("unix", socket, time.Second); err == nil {
		conn.Close()
		fail("An agent already answers on " + socket + ", such as one started by hand: stop it, then install again.")
	}
	for _, dir := range []string{filepath.Dir(servicePlist), filepath.Dir(serviceLog)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fail(err.Error())
		}
	}
	path := filepath.Dir(claude) + ":/usr/bin:/bin:/usr/sbin:/sbin"
	if err := os.WriteFile(servicePlist, []byte(launchdPlist(serviceLabel, []string{exe, "serve"}, path, serviceLog)), 0o644); err != nil {
		fail(err.Error())
	}
	if out, err := bootstrap(servicePlist); err != nil {
		fail("launchctl couldn't start it: " + strings.TrimSpace(out))
	}
	if !runningWithin(5 * time.Second) {
		fail("The agent didn't start. Its log, " + serviceLog + ", says why.")
	}
	fmt.Printf("The agent runs as a launchd agent (%s), from %s: it starts at login and again whenever it stops.\n", serviceLabel, exe)
	fmt.Println("Its log is " + serviceLog + ". `ringback-agent uninstall` stops it.")
}

func uninstallService() {
	stopService()
	if err := os.Remove(servicePlist); err != nil && !os.IsNotExist(err) {
		fail(err.Error())
	}
	fmt.Println("The agent's launchd agent is stopped and gone.")
}

// launchdPlist is a launchd agent that runs args with PATH set to path, at login and again
// whenever it stops, at most every 30 s, with its output in log.
func launchdPlist(label string, args []string, path, log string) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	var program strings.Builder
	for _, a := range args {
		program.WriteString("\n    <string>" + esc(a) + "</string>")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + esc(label) + `</string>
  <key>ProgramArguments</key>
  <array>` + program.String() + `
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>` + esc(path) + `</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>30</integer>
  <key>StandardOutPath</key>
  <string>` + esc(log) + `</string>
  <key>StandardErrorPath</key>
  <string>` + esc(log) + `</string>
</dict>
</plist>
`
}

// domains are where the agent can run: the logged-in user's session, or, from ssh with no
// one logged in at the screen, the user's background domain.
func domains() []string {
	uid := fmt.Sprint(os.Getuid())
	return []string{"gui/" + uid, "user/" + uid}
}

func bootstrap(plist string) (string, error) {
	var out []byte
	var err error
	for _, domain := range domains() {
		if out, err = exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput(); err == nil {
			return "", nil
		}
	}
	return string(out), err
}

// stopService stops the launchd agent, if it's loaded, and waits for it to go.
func stopService() {
	for _, domain := range domains() {
		_ = exec.Command("launchctl", "bootout", domain+"/"+serviceLabel).Run()
	}
	for end := time.Now().Add(5 * time.Second); serviceLoaded() && time.Now().Before(end); {
		time.Sleep(100 * time.Millisecond)
	}
}

func serviceLoaded() bool {
	for _, domain := range domains() {
		if exec.Command("launchctl", "print", domain+"/"+serviceLabel).Run() == nil {
			return true
		}
	}
	return false
}

// runningWithin waits for the launchd agent to be running with a process of its own.
func runningWithin(d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		for _, domain := range domains() {
			out, err := exec.Command("launchctl", "print", domain+"/"+serviceLabel).Output()
			if err == nil && strings.Contains(string(out), "state = running") {
				return true
			}
		}
	}
	return false
}
