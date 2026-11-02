package main

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// The status page: the hub's hold; what a call would do now, in a sentence; a map of the
// system, each part and each link working, a problem, gone or not set up (portal_map.go); and
// below it the calls, the sessions, the phones, and the speech nodes and Macs. Each fact is
// said where it belongs, once: the map shows what's linked, the lists what the map can't.

// holdView is the hub's hold: on hold while every registered phone is, and then no call
// rings, whatever speech there is. Setting it sets every phone's hold switch, so the app
// shows it too, and work hours set it at their edges, as they set the app's switch.
type holdView struct {
	Phones  int
	On      bool
	Waiting int    // calls waiting, for whatever reason
	Title   string // Status: "Not on hold", "On hold until you take calls", …
	Text    string // what holding does, or what it does now
}

// currentHold is the hold now, for every page's bar.
func currentHold() holdView {
	stateMu.Lock()
	defer stateMu.Unlock()
	waiting, _ := listWaiting()
	return hubHold(loadDevices(), len(waiting))
}

// hubHold is the hold now; the caller holds stateMu. Its words promise no more than the hold
// does: with work hours, their start ends it.
func hubHold(devices map[string]map[string]string, waiting int) holdView {
	h := holdView{Phones: len(devices), Waiting: waiting}
	if len(devices) == 0 {
		h.Title, h.Text = "No phone yet", "Once your iPhone registers with the hub, you can hold its calls here or in the app."
		return h
	}
	t := now()
	const nothing = "Nothing rings, not through a speech node and not with the phone's own voice."
	if held, until := heldUntil(t); held {
		h.On, h.Title = true, "On hold until you take calls"
		h.Text = nothing + " Calls wait here, and ring when you take calls."
		if at, ok := parseISO8601(until); ok {
			h.Title = "On hold until " + ahead(at) + ", when your work hours start"
			h.Text = nothing + " Calls wait here, and ring when your work hours start, or sooner if you take calls."
		}
		if waiting > 0 {
			h.Text += " " + plural(waiting, "call is", "calls are") + " waiting now."
		}
		return h
	}
	h.Title = "Not on hold"
	h.Text = "Hold calls and nothing rings, not through a speech node and not with the phone's own voice, until you take them."
	// With work hours, holding lasts until they next start, and their end holds calls by
	// itself, once the last phone taking calls goes on hold.
	var last time.Time
	hours := false
	for _, d := range devices {
		_, has := newWorkHours(d)
		hours = hours || has
		if hold := newHold(d, t); !hold.On && (last.IsZero() || hold.Until.After(last)) {
			last = hold.Until
		}
	}
	if hours {
		h.Text = "Hold calls and nothing rings, not through a speech node and not with the phone's own voice, until you take them or your work hours next start."
	}
	if !last.IsZero() {
		h.Text += " Your work hours hold calls at " + ahead(last) + "."
	}
	return h
}

// ahead is a time to come: "08:00" today, "08:00 tomorrow", "08:00 on Monday" within the
// week, "08:00 on 12 Nov" after.
func ahead(t time.Time) string {
	t, today := t.Local(), now().Local()
	day := func(x time.Time) time.Time { y, m, d := x.Date(); return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
	days := int(day(t).Sub(day(today)).Hours()/24 + 0.5)
	switch {
	case days <= 0:
		return t.Format("15:04")
	case days == 1:
		return t.Format("15:04") + " tomorrow"
	case days < 7:
		return t.Format("15:04") + " on " + t.Format("Monday")
	}
	return t.Format("15:04") + " on " + t.Format("2 Jan")
}

type problemView struct{ Text, Tone string } // Tone: bad, or wait

// partView is a speech node or a Mac in the lists under the map: what the map doesn't show.
type partView struct {
	Kind, Name, State, Status string // Kind: speech or agent; State: working, problem or asleep
	Gone                      bool   // not linked now, so it can be forgotten
}

type sessionView struct{ Project, Subject, Mac, Since string }

type liveView struct {
	Title, Subject, State, From, Speech, For string
	Untitled, Ringing                        bool
}

type waitingView struct {
	Project, Subject, Why, Rings string
	Question, NoSpeech           bool
}

type phoneView struct {
	Name, Environment, Token, Registered, CheckedIn, Hours, OnDevice, Push string
	PushFailed                                                             bool
}

type statusView struct {
	Headline, Hint, Tone, Detail string // Tone: good, wait, bad, hold or unset
	Problems                     []problemView
	Hold                         holdView
	Wide, Narrow                 mapView
	Live                         []liveView
	Waiting                      []waitingView
	Sessions                     []sessionView
	Phones                       []phoneView
	Nodes, Macs                  []partView
}

// heardWithin is how recently a phone must have reached the hub (registering or on a
// connection check, every 30 min) for its link through Nebula to count as working.
const heardWithin = 2 * time.Hour

// onDevice is "On‑device", with a hyphen that doesn't break a line.
const onDevice = "On‑device"

func statusData() statusView {
	var v statusView
	registryMu.Lock()
	nodeLinks := make([]*link, 0, len(nodes))
	for l := range nodes {
		nodeLinks = append(nodeLinks, l)
	}
	type agentSessions struct {
		l        *link
		sessions []map[string]string
	}
	var agentList []agentSessions
	for l, sessions := range agents {
		agentList = append(agentList, agentSessions{l, sessions})
	}
	calls := make([]*Call, 0, len(live))
	for _, c := range live {
		calls = append(calls, c)
	}
	registryMu.Unlock()
	sort.Slice(nodeLinks, func(i, j int) bool { return nodeLinks[i].name < nodeLinks[j].name })
	sort.Slice(agentList, func(i, j int) bool { return agentList[i].l.name < agentList[j].l.name })

	seenMu.Lock()
	seen := loadSeen()
	seenMu.Unlock()
	var waitingCalls []map[string]any
	var devices map[string]map[string]string
	func() {
		stateMu.Lock()
		defer stateMu.Unlock()
		waitingCalls, _ = listWaiting()
		devices = loadDevices()
		v.Hold = hubHold(devices, len(waitingCalls))
	}()
	config := loadConfig()
	var m mapModel

	// Calls, live: ringing (its offer came while the phone rang) or answered, and which Mac
	// and which speech each one's on, to light its path on the map.
	liveMacs, liveNodes := map[string]string{}, map[string]string{} // by name: "Ringing" or "On a call"
	ringing, answeredCalls := 0, 0
	sort.Slice(calls, func(i, j int) bool { return calls[i].offeredAt.Before(calls[j].offeredAt) })
	for _, c := range calls {
		c.mu.Lock()
		answered, connected, ended := c.answeredAt, c.connectedAt, c.ended
		c.mu.Unlock()
		if ended {
			continue
		}
		lv := liveView{From: c.agent.name, Speech: c.node.name + " hears and speaks it"}
		word := "On a call"
		if answered.IsZero() {
			word = "Ringing"
		}
		if c.phone {
			lv.Speech = "The phone hears and speaks it itself"
		} else if liveNodes[c.node.name] != "On a call" {
			liveNodes[c.node.name] = word
		}
		if liveMacs[c.agent.name] != "On a call" {
			liveMacs[c.agent.name] = word
		}
		note, _ := noteOf(c.id)
		lv.Title, lv.Subject = note.project, note.subject
		if lv.Title == "" {
			lv.Title, lv.Untitled = "Project unknown", true
		}
		switch {
		case answered.IsZero():
			lv.State, lv.For, lv.Ringing = "Ringing", span(now().Sub(c.offeredAt)), true
			ringing++
		case connected.IsZero():
			lv.State, lv.For = "Answered, connecting", span(now().Sub(answered))
			answeredCalls++
		default:
			lv.State, lv.For = "On the call", span(now().Sub(maxTime(answered, connected)))
			answeredCalls++
		}
		v.Live = append(v.Live, lv)
	}

	// Macs: those linked, then those gone, asleep most likely, as seen.json remembers them.
	linkedMac := map[string]bool{}
	sessions := 0
	for _, a := range agentList {
		linkedMac[a.l.name] = true
		for _, s := range a.sessions {
			started := ""
			if t, ok := parseISO8601(s["since"]); ok {
				started = "since " + clock(t)
			}
			v.Sessions = append(v.Sessions, sessionView{Project: s["project"], Subject: s["subject"], Mac: a.l.name, Since: started})
		}
		sessions += len(a.sessions)
		count := plural(len(a.sessions), "session", "sessions")
		if len(a.sessions) == 0 {
			count = "No sessions"
		}
		v.Macs = append(v.Macs, partView{Kind: "agent", Name: a.l.name, State: "working", Status: "Linked " + since(a.l.since)})
		part := mapPart{Kind: "mac", State: "working", Name: a.l.name, Sub: count, Wire: "working"}
		if word := liveMacs[a.l.name]; word != "" {
			part.Sub, part.Live, part.WireLive = word, true, true
		}
		m.Macs = append(m.Macs, part)
	}
	for _, name := range goneParts(seen["agent"], linkedMac) {
		last, _ := parseISO8601(seen["agent"][name].Last)
		v.Macs = append(v.Macs, partView{Kind: "agent", Name: name, State: "asleep", Gone: true,
			Status: "Asleep or off, last seen " + clock(last) + " (" + span(now().Sub(last)) + " ago)"})
		m.Macs = append(m.Macs, mapPart{Kind: "mac", State: "asleep", Name: name, Sub: "Asleep", Wire: "asleep"})
	}
	sort.SliceStable(v.Sessions, func(i, j int) bool {
		return strings.ToLower(v.Sessions[i].Project) < strings.ToLower(v.Sessions[j].Project)
	})
	if len(m.Macs) == 0 {
		m.Macs = []mapPart{{Kind: "mac", State: "unset", Name: "Mac", Sub: "Not set up", Setup: "mac", Wire: "unset"}}
	}

	// Speech nodes: those linked, then those gone, which is a problem: calls need them.
	linkedNode := map[string]bool{}
	for _, l := range nodeLinks {
		linkedNode[l.name] = true
		status := "Linked " + since(l.since) + ". Models idle until a call comes"
		if loaded, at := l.models(); loaded {
			status = "Linked " + since(l.since) + ". Models loaded " + since(at)
		}
		v.Nodes = append(v.Nodes, partView{Kind: "speech", Name: l.name, State: "working", Status: status})
		part := mapPart{Kind: "node", State: "working", Name: l.name, Sub: "Ready", Wire: "working"}
		if word := liveNodes[l.name]; word != "" {
			part.Live, part.WireLive = true, true
			if word != "Ringing" { // it's the phone that rings; the node is ready for it
				part.Sub = word
			}
		}
		m.Nodes = append(m.Nodes, part)
	}
	var goneNodes []string
	for _, name := range goneParts(seen["speech"], linkedNode) {
		last, _ := parseISO8601(seen["speech"][name].Last)
		goneNodes = append(goneNodes, name)
		v.Nodes = append(v.Nodes, partView{Kind: "speech", Name: name, State: "problem", Gone: true,
			Status: "Offline, last seen " + clock(last) + " (" + span(now().Sub(last)) + " ago)"})
		m.Nodes = append(m.Nodes, mapPart{Kind: "node", State: "problem", Name: name, Sub: "Offline", Wire: "problem"})
	}
	if len(m.Nodes) == 0 {
		m.Nodes = []mapPart{{Kind: "node", State: "unset", Name: "Speech node", Sub: "Not set up", Setup: "node", Wire: "unset"}}
	}

	// The phones, and Apple push between the hub and them. Which phone a call's on, the hub
	// can't tell: a ringing call rings every phone, and an answered one is on whichever
	// answered, which neither its offer nor its link says. With one phone, it's that one.
	tokens := make([]string, 0, len(devices))
	for token := range devices {
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool { return devices[tokens[i]]["device"] < devices[tokens[j]]["device"] })
	push := checkPush(config, devices)
	m.Push = mapPart{Kind: "push", State: "untried", Name: "Apple push", Sub: "Not tried yet", Wire: "working"}
	switch {
	case !push.set:
		m.Push.State, m.Push.Sub, m.Push.Setup, m.Push.Wire = "unset", "Not set up", "push", "unset"
	case push.problem != "":
		m.Push.State, m.Push.Sub, m.Push.Wire = "problem", push.label, "problem"
	}
	var lastPushAt time.Time
	var speakers, ringNames []string
	var pushFailure *pushResult // the latest failed push, if the last push to any phone failed
	pushFailedTo := ""
	for _, token := range tokens {
		d := devices[token]
		name := orDefault(d["device"], "A phone")
		ph := phoneView{Name: name, Environment: orDefault(d["environment"], "sandbox"),
			Token: "…" + suffix(token, 8), Registered: isoClock(d["registered"]), Hours: "Off", OnDevice: "Not reported by the app"}
		heard, _ := parseISO8601(d["registered"])
		switch at, checked := parseISO8601(d["checked_in"]); {
		case d["notification_token"] == "":
			ph.CheckedIn = "None: notifications aren't allowed in the app"
		case checked:
			ph.CheckedIn = clock(at) + ", " + span(now().Sub(at)) + " ago"
			heard = maxTime(heard, at)
		default:
			ph.CheckedIn = "Not yet"
		}
		if h := describe(d); h != "" {
			ph.Hours = strings.TrimPrefix(h, "work hours ")
		}
		part := mapPart{Kind: "phone", State: "working", Name: name, Sub: onDevice + " voice off", Wire: "working", PushWire: "working"}
		if !push.set {
			part.PushWire = "unset"
		}
		switch d["on_device"] {
		case "yes":
			ph.OnDevice, part.Sub, part.speaks = "On: it hears and speaks a call itself when no speech node is linked", onDevice+" voice on", true
			speakers = append(speakers, name)
		case "no":
			ph.OnDevice = "Off"
		}
		if now().Sub(heard) > heardWithin {
			part.Wire = "asleep"
		}
		if last, found := lastPush(d, token); !found {
			ph.Push = "None since the hub started"
		} else if last.ok() {
			ph.Push = "Delivered to Apple at " + clock(last.at)
			lastPushAt = maxTime(lastPushAt, last.at)
		} else {
			// What went wrong is said once, above the map; here, only when.
			ph.PushFailed, ph.Push = true, "Failed at "+clock(last.at)
			lastPushAt = maxTime(lastPushAt, last.at)
			switch last.problem() {
			case "key", "network":
				if m.Push.State != "unset" && m.Push.State != "problem" {
					m.Push.State, m.Push.Wire = "problem", "problem"
					m.Push.Sub = map[string]string{"key": "Key refused", "network": "Can't reach Apple"}[last.problem()]
				}
			case "phone":
				part.State, part.PushWire = "problem", "problem"
			default:
				part.PushWire = "problem"
			}
			if pushFailure == nil || last.at.After(pushFailure.at) {
				failure := last
				pushFailure, pushFailedTo = &failure, name
			}
		}
		ringNames = append(ringNames, name)
		m.Phones = append(m.Phones, part)
		v.Phones = append(v.Phones, ph)
	}
	if m.Push.State == "untried" && !lastPushAt.IsZero() {
		m.Push.State, m.Push.Sub = "working", "Last push "+clock(lastPushAt)
	}
	// A call that rings, rings every phone; an answered one lights a phone only if there's
	// one.
	for i := range m.Phones {
		switch {
		case ringing > 0:
			m.Phones[i].Sub, m.Phones[i].Live, m.Phones[i].PushLive = "Ringing", true, true
			m.Phones[i].WireLive = len(m.Phones) == 1
		case answeredCalls > 0 && len(m.Phones) == 1:
			m.Phones[i].Sub, m.Phones[i].Live, m.Phones[i].WireLive = "On a call", true, true
		}
	}
	if len(m.Phones) == 0 {
		m.Phones = []mapPart{{Kind: "phone", State: "unset", Name: "iPhone", Sub: "Not set up", Setup: "phone", Wire: "unset", PushWire: "unset"}}
	}

	// The speech a call would have now: a speech node, the phone's own, or none.
	var linkedNames []string
	for _, l := range nodeLinks {
		linkedNames = append(linkedNames, l.name)
	}
	speech := ""
	switch {
	case len(nodeLinks) > 0:
		for i := range m.Nodes {
			if m.Nodes[i].State == "working" {
				m.Nodes[i].Tag, m.Nodes[i].TagTone = "Used for calls", "good"
			}
		}
		speech = orList(linkedNames) + " hears and speaks them"
	case len(speakers) > 0:
		for i := range m.Phones {
			if m.Phones[i].speaks {
				m.Phones[i].Tag, m.Phones[i].TagTone = "Used for calls", "good"
			}
		}
		speech = speakers[0] + " hears and speaks them itself"
		if len(speakers) > 1 {
			speech = "the phone that answers hears and speaks them itself"
		}
	case len(goneNodes) == 0 && (len(agentList) > 0 || len(seen["agent"]) > 0):
		// Said once a Mac could place a call, and only of a node never set up: one that's
		// gone says so itself.
		m.SpeechNote = "None yet, so calls wait"
	}

	// What a call would do now, in a sentence, with what to do about it, and what else is
	// wrong, each said once.
	your := "your " + andList(ringNames)
	type issue struct{ tone, headline, hint string }
	var issues []issue
	switch {
	case !push.set:
		issues = append(issues, issue{"unset", "Calls can't ring yet: Apple push isn't set up.", "Add an Apple push key to the hub's config, as Setup shows."})
	case push.problem != "":
		issues = append(issues, issue{"bad", "Calls can't ring: " + push.problem + ".", push.detail})
	case pushFailure != nil:
		detail := outcome(pushFailure.status, pushFailure.reason, pushFailure.err)
		switch pushFailure.problem() {
		case "key":
			issues = append(issues, issue{"bad", "Calls can't ring: Apple refused the hub's push key.", "Check the key in the hub's config (" + detail + ")."})
		case "network":
			issues = append(issues, issue{"bad", "Calls can't ring: the hub couldn't reach Apple.", "Check the hub's connection to the internet (" + detail + ")."})
		case "phone":
			issues = append(issues, issue{"bad", "Calls can't ring your " + pushFailedTo + ": Apple no longer knows it.", "Open the app on it to register again (" + detail + ")."})
		default:
			issues = append(issues, issue{"bad", "Calls can't ring your " + pushFailedTo + ": Apple refused the last push.", detail + "."})
		}
	}
	switch {
	case speech != "":
		for _, name := range goneNodes {
			issues = append(issues, issue{"bad", name + " is offline.", "Start it again, or forget it below if it's gone for good."})
		}
	case len(goneNodes) > 0:
		are := " is offline"
		if len(goneNodes) > 1 {
			are = " are offline"
		}
		issues = append(issues, issue{"bad", "Calls wait instead of ringing: " + andList(goneNodes) + are + ".",
			"Start it again, or turn on " + onDevice + " voice in the app's settings. If it's gone for good, forget it below."})
	default:
		issues = append(issues, issue{"wait", "Calls wait instead of ringing: there's no speech node, and " + onDevice + " voice is off.",
			"Add a speech node, as Setup shows, or turn on " + onDevice + " voice in the app's settings."})
	}
	switch {
	case len(devices) == 0:
		v.Tone, v.Headline, v.Hint = "unset", "No phone yet: calls have nowhere to ring.", "Open the app on your iPhone and enter the hub's address in its settings."
	case len(issues) > 0:
		v.Tone, v.Headline, v.Hint = issues[0].tone, issues[0].headline, issues[0].hint
		for _, more := range issues[1:] {
			v.Problems = append(v.Problems, problemView{more.headline + " " + more.hint, more.tone})
		}
	case speech != "":
		v.Tone, v.Headline = "good", "Calls ring "+your+", and "+speech+"."
		if len(nodeLinks) == 0 && len(ringNames) == 1 && len(speakers) == 1 {
			v.Headline = "Calls ring " + your + ", which hears and speaks them itself."
		}
	}
	if v.Hold.On && len(devices) > 0 {
		if v.Tone == "good" {
			v.Tone = "hold"
		}
		v.Headline = "When the hold ends, " + strings.ToLower(v.Headline[:1]) + v.Headline[1:]
	}
	var detail []string
	switch {
	case answeredCalls > 0 && len(devices) > 1:
		detail = append(detail, plural(answeredCalls, "call", "calls")+" live, on one of your phones: the hub can't tell which")
	case answeredCalls > 0:
		detail = append(detail, plural(answeredCalls, "call", "calls")+" live")
	}
	if ringing > 0 {
		detail = append(detail, plural(ringing, "call", "calls")+" ringing")
	}
	if n := len(waitingCalls); n > 0 {
		detail = append(detail, plural(n, "call", "calls")+" waiting")
	}
	switch macs := len(agentList); {
	case macs == 0 && len(seen["agent"]) == 0:
		detail = append(detail, "No Mac linked yet")
	case macs == 0:
		detail = append(detail, "No Mac linked: they're asleep or off")
	case sessions == 0:
		detail = append(detail, "No session listening on "+plural(macs, "Mac", "Macs"))
	default:
		detail = append(detail, plural(sessions, "session", "sessions")+" listening on "+plural(macs, "Mac", "Macs"))
	}
	v.Detail = strings.Join(detail, ". ") + "."
	v.Waiting = waitingViews(waitingCalls)

	m.Held = v.Hold.On
	m.Hub = mapPart{Kind: "hub", State: "working", Name: "Hub"}
	if v.Hold.On {
		m.Hub.Tag, m.Hub.TagTone = "On hold", "hold"
	}
	m.Title = v.Headline
	v.Wide, v.Narrow = m.wide(), m.narrow()
	return v
}

// waitingViews are the calls that wait, as the status and calls pages list them.
func waitingViews(calls []map[string]any) []waitingView {
	var views []waitingView
	for _, call := range calls {
		wv := waitingView{Question: call["question"] == true}
		wv.Project, _ = call["project"].(string)
		wv.Subject, _ = call["subject"].(string)
		switch until, _ := call["held_until"].(string); {
		case call["reason"] == "no speech":
			wv.NoSpeech, wv.Why, wv.Rings = true, "Waiting for a speech node", "Rings once one links"
		case until != "":
			wv.Why, wv.Rings = "On hold", "Rings at "+isoAhead(until)
		default:
			wv.Why, wv.Rings = "On hold", "Rings when you take calls"
		}
		views = append(views, wv)
	}
	return views
}

// goneParts are the names of the parts seen but not linked now, the latest seen first.
func goneParts(seen map[string]seenPart, linkedNow map[string]bool) []string {
	var names []string
	for name := range seen {
		if !linkedNow[name] {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if a, b := seen[names[i]].Last, seen[names[j]].Last; a != b {
			return a > b
		}
		return names[i] < names[j]
	})
	return names
}

func isoAhead(text string) string {
	if t, ok := parseISO8601(text); ok {
		return ahead(t)
	}
	return text
}

// andList is "a", "a and b", "a, b and c"; orList the same with "or".
func andList(items []string) string { return joinList(items, " and ") }
func orList(items []string) string  { return joinList(items, " or ") }

func joinList(items []string, last string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + last + items[len(items)-1]
}

// pushCheck is Apple push in the hub's config: set up (a team, a bundle ID and a key for at
// least one environment), and what's wrong with it, if anything: in the headline's words, on
// the map, and in detail.
type pushCheck struct {
	set                    bool
	problem, label, detail string
}

// checkPush finds a key that can't be read, or none for the environment a phone's build gets
// its pushes from.
func checkPush(config map[string]string, devices map[string]map[string]string) pushCheck {
	has := func(env string) bool {
		return config["apns."+env+".key_id"] != "" && config["apns."+env+".key_file"] != ""
	}
	if config["apns.team_id"] == "" || config["apns.bundle_id"] == "" || !(has("sandbox") || has("production")) {
		return pushCheck{}
	}
	for _, env := range []string{"sandbox", "production"} {
		if !has(env) {
			continue
		}
		file := config["apns."+env+".key_file"]
		if _, err := readPushKey(expand(file)); errors.Is(err, fs.ErrNotExist) {
			return pushCheck{true, "the hub's Apple push key is missing", "Key missing",
				file + " isn't there. Put the key there, or fix key_file under [apns." + env + "] in the hub's config, then restart the hub."}
		} else if err != nil {
			return pushCheck{true, "the hub can't read its Apple push key", "Key can't be read",
				fmt.Sprintf("%s: %v. Put the .p8 file from Apple there, then restart the hub.", file, err)}
		}
	}
	for _, d := range devices {
		if env := orDefault(d["environment"], "sandbox"); !has(env) {
			name := orDefault(d["device"], "a phone")
			return pushCheck{true, "the hub has no Apple push key for " + name, "No key for " + env,
				name + " gets its pushes from Apple's " + env + " environment: add [apns." + env + "] to the hub's config."}
		}
	}
	return pushCheck{set: true}
}
