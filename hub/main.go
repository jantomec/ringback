// The ringback hub: rings the phone, holds calls while it's on hold, lists the calls that
// wait and the sessions you can call, carries each call's audio and runs its timing, links
// the speech nodes and the agents, and serves the portal ("The parts" in docs/design.md).
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	_ "time/tzdata" // the phones' time zones, wherever the hub runs
)

const usage = `usage: ringback-hub install | uninstall | serve | ring [--project NAME] [--subject TEXT]
       | check | devices | pair [mac | node] | portal-user NAME

  install  run the hub as a service (a launchd agent): now, at login, and again whenever
           it stops; again after updating the binary, to restart it; uninstall stops it
           and takes it away
  serve    serve the phone, the speech nodes and the agents: ring calls, carry their audio,
           and every 30 min ask each phone to check that it can still reach the hub
  ring     ring every paired phone, at any time, to try the push; the call has no audio
           (an agent places a session's calls)
             --project NAME  shown as the caller (default: ringback)
             --subject TEXT  a few words on what the call is about
  check    ask every paired phone to check its connection now; one that can't reach the
           hub shows a warning
  devices  list the phones, whether each has paired, when it last checked in, its work
           hours and its hold switch
  pair     print a one-time link that pairs a phone, valid 10 min: open it on the phone
           (the portal's Pair this phone does the same); with mac or node, the command
           that pairs a Mac's agent or a speech node, run there (as the portal's Setup)
  portal-user NAME
           make the account for the hub's portal (/portal/ on its address), or replace
           it: asks for its password and prints the setup key for an authenticator app`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fail(usage)
	}
	switch args[0] {
	case "install":
		installService()
	case "uninstall":
		uninstallService()
	case "serve":
		listen := loadConfig()["listen"]
		if listen == "" {
			fail("No listen address in " + configPath + ".")
		}
		serve(listen)
	case "ring":
		options := parseOptions(args[1:], "--project", "--subject")
		if len(pairedDevices()) == 0 {
			fail("No phone has paired yet: `ringback-hub pair` makes a link to open on the phone.")
		}
		project := options["--project"]
		if project == "" {
			project = "ringback"
		}
		for _, line := range ring(context.Background(), project, options["--subject"], "", "", "") {
			fmt.Println(line)
		}
	case "check":
		lines := askPhonesToCheck(context.Background())
		if len(lines) == 0 {
			fail("No phone has given a notification token yet: open the app and allow notifications.")
		}
		for _, line := range lines {
			fmt.Println(line)
		}
	case "pair":
		kind := map[string]string{"mac": "agent", "node": "speech"}[strings.Join(args[1:], " ")]
		if len(args) > 2 || (len(args) == 2 && kind == "") {
			fail(usage)
		}
		code, expires, err := newPairingCode(kind)
		if err != nil {
			fail("Can't make a pairing code: " + err.Error())
		}
		if kind != "" {
			fmt.Println("Run this on the " + partKinds[kind] + " before " + timeOfDay(expires.Local()) + "; it works once:")
			fmt.Println(partCommand(kind, code))
			return
		}
		fmt.Println("Open this on the phone, in Safari or from a message to yourself, before " +
			timeOfDay(expires.Local()) + "; it works once:")
		fmt.Println(pairingLink(code))
	case "devices":
		devices := loadDevices()
		tokens := make([]string, 0, len(devices))
		for token := range devices {
			tokens = append(tokens, token)
		}
		sort.Strings(tokens)
		for _, token := range tokens {
			fmt.Println(describeDevice(token, devices[token]))
		}
	case "portal-user":
		if len(args) != 2 {
			fail(usage)
		}
		createPortalUser(args[1])
	case "-h", "--help":
		fmt.Println(usage)
	default:
		fail(usage)
	}
}

// describeDevice is one line of `devices`: the token's end, the phone, its environment,
// when it registered and last checked in, its work hours and its hold switch.
func describeDevice(token string, d map[string]string) string {
	checks := ""
	if _, ok := d["notification_token"]; ok {
		if at, ok := d["checked_in"]; ok {
			checks = "  checked in " + at
		} else {
			checks = "  never checked in"
		}
	}
	hours := ""
	if h := describe(d); h != "" {
		hours = "  " + h
	}
	state := "taking calls"
	if newHold(d, now()).On {
		state = "on hold"
	}
	if set, ok := d["hold_set"]; ok {
		state += ", set " + set
	}
	if d["key"] == "" {
		state += "  not paired"
	}
	return fmt.Sprintf("…%s  %s  %s  registered %s%s%s  %s", suffix(token, 8), d["device"], d["environment"], d["registered"], checks, hours, state)
}
