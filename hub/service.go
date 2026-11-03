package main

// The hub as a service, on macOS: `install` writes a launchd agent that starts the hub at
// login and again whenever it stops, and starts it now; `uninstall` stops it and takes the
// agent away. Installing again, as after copying a new binary over the old, restarts it.

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const serviceLabel = "ringback.hub"

var (
	servicePlist = expand("~/Library/LaunchAgents/" + serviceLabel + ".plist")
	serviceLog   = expand("~/Library/Logs/ringback-hub.log")
)

func installService() {
	if runtime.GOOS != "darwin" {
		fail("install sets the hub up with launchd, on macOS. Elsewhere, run `ringback-hub serve` from your service manager.")
	}
	config := loadConfig()
	listen := config["listen"]
	if listen == "" {
		fail("No listen address in " + configPath + ": fill it in first (hub/hub.example.toml).")
	}
	for _, kind := range []string{"agent", "speech"} {
		if len(config["tokens."+kind]) < 16 {
			fail("No [tokens] " + kind + " in " + configPath + ": make it a long random string, such as `openssl rand -hex 32` prints.")
		}
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fail("Can't tell where this binary is: " + err.Error())
	}
	stopService()
	// A hub started by hand would keep the address, and the service would never start.
	if !freeWithin(listen, 5*time.Second) {
		fail("Something already listens on " + listen + ", such as a hub started by hand: stop it, then install again.")
	}
	if err := os.MkdirAll(filepath.Dir(servicePlist), 0o755); err != nil {
		fail(err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(serviceLog), 0o755); err != nil {
		fail(err.Error())
	}
	if err := os.WriteFile(servicePlist, []byte(launchdPlist(serviceLabel, []string{exe, "serve"}, serviceLog)), 0o644); err != nil {
		fail(err.Error())
	}
	if out, err := bootstrap(servicePlist); err != nil {
		fail("launchctl couldn't start it: " + strings.TrimSpace(out))
	}
	if !runningWithin(5 * time.Second) {
		fail("The hub didn't start. Its log, " + serviceLog + ", says why.")
	}
	fmt.Printf("The hub runs as a launchd agent (%s), from %s: it starts at login and again whenever it stops.\n", serviceLabel, exe)
	fmt.Println("Its log is " + serviceLog + ". `ringback-hub uninstall` stops it.")
}

func uninstallService() {
	stopService()
	if err := os.Remove(servicePlist); err != nil && !os.IsNotExist(err) {
		fail(err.Error())
	}
	fmt.Println("The hub's launchd agent is stopped and gone.")
}

// launchdPlist is a launchd agent that runs args at login and again whenever it stops, at
// most every 30 s (while the network is down, the hub can't listen on its address), with
// its output in log.
func launchdPlist(label string, args []string, log string) string {
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

// stopService stops the hub's launchd agent, if it's loaded, and waits for it to go.
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

// runningWithin waits for the agent to be running with a process of its own.
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

// freeWithin waits for nothing to listen on address. An address that isn't there yet, as
// while the VPN is down, counts as free: the service waits for it.
func freeWithin(address string, d time.Duration) bool {
	for end := time.Now().Add(d); ; time.Sleep(200 * time.Millisecond) {
		l, err := net.Listen("tcp4", address)
		if err == nil {
			l.Close()
			return true
		}
		if !errors.Is(err, syscall.EADDRINUSE) || time.Now().After(end) {
			return !errors.Is(err, syscall.EADDRINUSE)
		}
	}
}
