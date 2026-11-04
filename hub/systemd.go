package main

// The hub as a service on Linux, as on a server: a systemd user service that starts it at boot
// and again whenever it stops, as the speech node's (speech/cmd/ringback-speech/service.go).

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

const unitName = "ringback-hub.service"

func unitPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", unitName)
}

func installSystemd(exe, listen string) {
	_ = exec.Command("systemctl", "--user", "stop", unitName).Run()
	_ = exec.Command("systemctl", "--user", "reset-failed", unitName).Run()
	// A hub started by hand would keep the address, and the service would never start.
	if !freeWithin(listen, 5*time.Second) {
		fail("Something already listens on " + listen + ", such as a hub started by hand: stop it, then install again.")
	}
	unit := `# Written by ringback-hub install; install again to rewrite it.
[Unit]
Description=ringback hub

[Service]
ExecStart="` + exe + `" serve
Restart=always
# While the network is down, the hub can't listen on its address.
RestartSec=10

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
		fail("The hub didn't start: `journalctl --user -u ringback-hub` says why.")
	}
	fmt.Println("The hub runs as a systemd user service (" + unitName + "), from " + exe + ": it starts at boot and again whenever it stops.")
	if !lingering {
		fmt.Println("It runs only while you're logged in: `sudo loginctl enable-linger " + os.Getenv("USER") + "` makes it run from boot.")
	}
	fmt.Println("Its log: `journalctl --user -u ringback-hub -f`. `ringback-hub uninstall` stops it.")
}

func uninstallSystemd() {
	_ = exec.Command("systemctl", "--user", "disable", "--now", unitName).Run()
	if err := os.Remove(unitPath()); err != nil && !os.IsNotExist(err) {
		fail(err.Error())
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Println("The hub's service is stopped and gone.")
}
