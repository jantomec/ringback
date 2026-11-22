package main

// What each session was passed, and how far it got, for check_session: the voice calls it when
// you ask whether the session got something, or why it isn't answering (see "Checking on the
// session" in docs/design.md). The agent knows when the voice passed a message on, or didn't
// and why; when the session's mod took it, as it collects once a second; whether the session is
// working on a turn, which the mod says as it collects; and when the session's answer came back.
// Kept in memory only, the last few for each session.

import (
	"fmt"
	"strings"
	"time"
)

type delivery struct {
	text      string
	call      string    // the call it was passed on
	at        time.Time // when the voice passed it on, or would have
	dropped   string    // why it didn't go, if it didn't
	collected time.Time // when the session's mod took it
	answered  time.Time // when the session's answer came back to a call
}

// turning is what a session's mod last said of it: whether a turn runs, and since when that's so.
type turning struct {
	on    bool
	since time.Time
}

// deliveriesKept is how many it keeps of each session's.
const deliveriesKept = 8

// delivered keeps a message the voice passed to a session, or one it didn't pass and why. The
// caller holds c.mu.
func (c *Calls) delivered(session, call, text, dropped string) {
	list := append(c.deliveries[session], &delivery{text: text, call: call, at: time.Now(), dropped: dropped})
	if len(list) > deliveriesKept {
		list = list[len(list)-deliveriesKept:]
	}
	c.deliveries[session] = list
}

// taken: the session's mod collected these messages. The caller holds c.mu.
func (c *Calls) taken(session string, messages []Message) {
	for _, m := range messages {
		if m.Kind != "message" {
			continue
		}
		for _, d := range c.deliveries[session] {
			if d.dropped == "" && d.collected.IsZero() && strings.HasPrefix(m.Text, d.text) {
				d.collected = time.Now()
				break
			}
		}
	}
}

// answered: the session's answer came back to a call. The caller holds c.mu.
func (c *Calls) answered(session string) {
	for _, d := range c.deliveries[session] {
		if d.dropped == "" && !d.collected.IsZero() && d.answered.IsZero() {
			d.answered = time.Now()
		}
	}
}

// working keeps what a session's mod says of it as it collects: "1" while a turn runs, "0"
// while none does, "" from a mod that doesn't say. The caller holds c.mu.
func (c *Calls) working(session, said string) {
	if said != "0" && said != "1" {
		return
	}
	if t, ok := c.turning[session]; !ok || t.on != (said == "1") {
		c.turning[session] = turning{on: said == "1", since: time.Now()}
	}
}

// sessionReport is what check_session answers on call: what the agent knows of the session and
// of what was passed to it. The caller holds c.mu.
func (c *Calls) sessionReport(session, call string) string {
	now := time.Now()
	ago := func(t time.Time) string { return duration(now.Sub(t).Seconds()) }
	var lines []string
	last, seen := c.seen.get(session)
	listening := seen && now.Sub(last) < 10*time.Second
	switch {
	case !seen:
		lines = append(lines, "The session's ringback mod hasn't asked for messages since the agent started, so the "+
			"session isn't listening: Claude Code there may be closed, or ringback switched off in it.")
	case listening:
		lines = append(lines, fmt.Sprintf("The session is listening: its ringback mod last asked for messages %s ago.", ago(last)))
	default:
		lines = append(lines, fmt.Sprintf("The session isn't listening now: its ringback mod last asked for messages %s ago. "+
			"Claude Code there may be closed or stuck, or ringback switched off in it.", ago(last)))
	}
	t, known := c.turning[session]
	if listening && known {
		if t.on {
			lines = append(lines, fmt.Sprintf("It's working on a turn, for %s so far.", ago(t.since)))
		} else {
			lines = append(lines, fmt.Sprintf("It isn't working on anything: its last turn ended %s ago.", ago(t.since)))
		}
	}
	list := c.deliveries[session]
	if len(list) == 0 {
		lines = append(lines, "Nothing has been passed to it, on this call or earlier ones, since the agent started.")
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "What the voice passed to it, oldest first:")
	for _, d := range list {
		where := "on this call"
		if d.call != call {
			where = "on an earlier call"
		}
		line := fmt.Sprintf("- %q, %s ago %s: ", d.text, ago(d.at), where)
		switch {
		case d.dropped != "":
			line += "not passed, " + d.dropped + "."
		case d.collected.IsZero():
			line += "passed, but the session hasn't taken it yet."
		case d.answered.IsZero():
			line += fmt.Sprintf("the session took it %s later; no answer has come back yet.", duration(d.collected.Sub(d.at).Seconds()))
			if listening && known && !t.on && t.since.After(d.collected) {
				line += fmt.Sprintf(" It finished working on it %s ago, yet no answer came back: it may have answered in its terminal only.", ago(t.since))
			}
		default:
			line += fmt.Sprintf("the session took it %s later, and answered %s after that.",
				duration(d.collected.Sub(d.at).Seconds()), duration(d.answered.Sub(d.collected).Seconds()))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
