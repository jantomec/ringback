package main

// The speech node as a service, on Linux: `install` writes a systemd user unit that starts it
// at boot and again whenever it stops, and starts it now; `uninstall` stops it and takes the
// unit away. Installing again, as after building a new binary, restarts it.

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const unitName = "ringback-speech.service"

func unitPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", unitName)
}

func installService(c config) {
	if runtime.GOOS != "linux" {
		fail("install sets the speech node up with systemd, on Linux.")
	}
	if c.hub == "" || strings.Contains(c.hub, "HUB-ADDRESS") || (c.key == "" && (c.token == "" || strings.Contains(c.token, "SAME AS"))) {
		fail("Pair the node first (`ringback-speech pair`, with a code from the hub's portal), or set hub and token in " + c.path + ".")
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fail("Can't tell where this binary is: " + err.Error())
	}
	cfg, _ := filepath.Abs(c.path)
	dir := filepath.Dir(filepath.Dir(exe)) // the install directory, with env.sh
	if _, err := os.Stat(filepath.Join(dir, "env.sh")); err != nil {
		fail("No env.sh in " + dir + ": install runs the binary in the install directory's bin/, as speech/build.sh puts it.")
	}
	// Stops the node's unit, if one runs, even one made for the moment with systemd-run.
	_ = exec.Command("systemctl", "--user", "stop", unitName).Run()
	_ = exec.Command("systemctl", "--user", "reset-failed", unitName).Run()
	// A node started by hand would link to the hub beside the service's.
	if pids := othersServing(); len(pids) > 0 {
		fail("A speech node already runs (pid " + strings.Join(pids, ", ") + "), such as one started by hand: stop it, then install again.")
	}
	unit := `# Written by ringback-speech install; install again to rewrite it.
[Unit]
Description=ringback speech node

[Service]
WorkingDirectory=` + dir + `
ExecStart=/bin/sh -c '. ./env.sh && exec "` + exe + `" --config "` + cfg + `" serve'
Restart=always
RestartSec=5
# The stop signal goes to serve alone, which stops its worker (a model-loading process).
KillMode=mixed

[Install]
WantedBy=default.target
`
	if err := os.MkdirAll(filepath.Dir(unitPath()), 0o755); err != nil {
		fail(err.Error())
	}
	if err := os.WriteFile(unitPath(), []byte(unit), 0o644); err != nil {
		fail(err.Error())
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", unitName}, {"restart", unitName}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			fail("systemctl --user " + strings.Join(args, " ") + ": " + strings.TrimSpace(string(out)))
		}
	}
	// Without lingering, a user's services run only while they're logged in.
	lingering := true
	if u, err := user.Current(); err == nil {
		out, _ := exec.Command("loginctl", "show-user", u.Username, "--property=Linger").Output()
		if strings.TrimSpace(string(out)) != "Linger=yes" {
			if err := exec.Command("loginctl", "enable-linger", u.Username).Run(); err != nil {
				lingering = false
			}
		}
	}
	active := false
	for end := time.Now().Add(10 * time.Second); !active && time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		out, _ := exec.Command("systemctl", "--user", "is-active", unitName).Output()
		active = strings.TrimSpace(string(out)) == "active"
	}
	if !active {
		fail("The speech node didn't start: `journalctl --user -u ringback-speech` says why.")
	}
	fmt.Println("The speech node runs as a systemd user service (" + unitName + "), from " + exe + ": it starts at boot and again whenever it stops.")
	if !lingering {
		fmt.Println("It runs only while you're logged in: `sudo loginctl enable-linger " + os.Getenv("USER") + "` makes it run from boot.")
	}
	fmt.Println("Its log: `journalctl --user -u ringback-speech -f`. `ringback-speech uninstall` stops it.")
}

func uninstallService() {
	_ = exec.Command("systemctl", "--user", "disable", "--now", unitName).Run()
	if err := os.Remove(unitPath()); err != nil && !os.IsNotExist(err) {
		fail(err.Error())
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Println("The speech node's service is stopped and gone.")
}

// othersServing is every other process running `ringback-speech … serve`.
func othersServing() []string {
	var pids []string
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if len(args) > 1 && filepath.Base(args[0]) == "ringback-speech" && args[len(args)-1] == "serve" {
			pids = append(pids, e.Name())
		}
	}
	return pids
}
