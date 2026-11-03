// The ringback hub: rings the phone, holds calls while it's on hold, lists the calls that
// wait and the sessions you can call, carries each call's audio and runs its timing, links
// the speech nodes and the agents, and serves the portal ("The parts" in docs/design.md).
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	_ "time/tzdata" // the phones' time zones, wherever the hub runs
)

const usage = `usage: ringback-hub serve | ring [--project NAME] [--subject TEXT] [--voice URL] | check | devices
       | portal-user NAME

  serve    serve the phone, the speech nodes and the agents: ring calls, carry their audio,
           and every 30 min ask each phone to check that it can still reach the hub
  ring     ring every registered phone, at any time, to try the push: without --voice it
           rings without audio (an agent places a session's calls)
             --project NAME  shown as the caller (default: ringback)
             --subject TEXT  a few words on what the call is about
             --voice URL     where the phone offers the call's audio (its /offer)
  check    ask every phone to check its connection now; one that can't reach the
           hub shows a warning
  devices  list registered phones, when each last checked in, their work hours and
           their hold switch
  portal-user NAME
           make the account for the hub's portal (/portal/ on its address), or replace
           it: asks for its password and prints the setup key for an authenticator app`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fail(usage)
	}
	switch args[0] {
	case "serve":
		listen := loadConfig()["listen"]
		if listen == "" {
			fail("No listen address in " + configPath + ".")
		}
		serve(listen)
	case "ring":
		options := parseOptions(args[1:], "--project", "--subject", "--voice")
		if len(loadDevices()) == 0 {
			fail("No phone registered yet: open the app and enter the hub's address.")
		}
		project := options["--project"]
		if project == "" {
			project = "ringback"
		}
		for _, line := range ring(context.Background(), project, options["--subject"], options["--voice"], "", "") {
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
	return fmt.Sprintf("…%s  %s  %s  registered %s%s%s  %s", suffix(token, 8), d["device"], d["environment"], d["registered"], checks, hours, state)
}
