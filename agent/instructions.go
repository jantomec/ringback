package main

import (
	"strings"
)

// What the voice is told: how to talk on the phone (the conversation rules in
// docs/design.md), when to put a better model on, what it can do for the session, how to put
// what the session asks to the developer, then the session's briefing.
//
// On a call back, the briefing is the earlier call's, with a lead that says the developer
// called, and whether they didn't answer then or talked (calledBack); if the session isn't
// listening any more, the voice is told it can't act on it. A waiting call the developer
// calls from the app gets a lead too (calledNowBriefing), and so does a session they call
// from the app's Contacts, with its latest briefing if it has one (calledContactBriefing).
func instructions(report string, session, listening bool) string {
	text := noSession
	if session && listening {
		text = sessionRules
	} else if session {
		text = notListening
	}
	if report == "" {
		report = standIn
	}
	return rules + text + strings.TrimSpace(report) + "\n"
}

func fill(template string, values ...string) string {
	return strings.NewReplacer(values...).Replace(template)
}

// since is the briefing of the session's latest call and how long ago it was placed, when
// that came after the call called back.
type since struct {
	briefing string
	ago      float64
}

// calledBack is the briefing of a call back: the earlier call's, placed ago seconds before,
// with a lead. settled names what the earlier call asks and no longer waits for. talked is
// how long ago the developer last talked on it, if they answered it or called it; otherwise
// they missed it.
func calledBack(briefing string, ago float64, later *since, settled []string, talked *float64) string {
	earlier := "missed call"
	lead := fill(calledBackLead, "{ago}", whileAgo(ago))
	if talked != nil {
		earlier = "earlier call"
		lead = fill(calledBackTalked, "{ago}", whileAgo(*talked))
	}
	if later != nil {
		lead += "\n" + fill(movedOn, "{ago}", whileAgo(later.ago), "{earlier}", earlier)
	}
	if len(settled) > 0 {
		lead += "\n" + fill(settledLead, "{names}", strings.Join(settled, " or "), "{earlier}", earlier)
	}
	text := strings.TrimSpace(briefing)
	if text == "" {
		text = noBriefing
	}
	text = lead + "\n\nThe " + earlier + "'s briefing:\n" + text
	if later != nil {
		text += "\n\nSince then, the session's latest briefing:\n" + strings.TrimSpace(later.briefing)
	}
	return text
}

// calledNowBriefing is the briefing of a waiting call the developer called from the app,
// with a lead.
func calledNowBriefing(briefing string) string {
	return calledNow + "\n\nThe call's briefing:\n" + strings.TrimSpace(briefing)
}

// calledContactBriefing is the briefing of a call to a session from the app's Contacts: its
// latest briefing, written ago seconds before, if it has one, with a lead. heard is how long
// ago the developer last talked on a call with that briefing, if they did.
func calledContactBriefing(briefing string, ago float64, heard *float64) string {
	if strings.TrimSpace(briefing) == "" {
		return calledContact + "\n" + contactNone
	}
	lead := fill(contactBriefed, "{ago}", whileAgo(ago))
	if heard != nil {
		lead = fill(contactHeard, "{ago}", whileAgo(ago), "{heard}", whileAgo(*heard))
	}
	return calledContact + "\n" + lead + "\n\nThe session's latest briefing:\n" + strings.TrimSpace(briefing)
}
