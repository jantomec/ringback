package main

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// The status page: what a call would do now, in a sentence; a map of the system, each part
// and each link working, a problem, offline or not set up (portal_map.go); and below it the
// calls, the sessions, the phones, and the speech nodes and Macs. The hub's hold, which every
// page shows in its bar, has its control here too.

// holdView is the hub's hold: on hold while every registered phone is, and then no call
// rings, whatever speech there is. Setting it sets every phone's hold switch, so the app
// shows it too.
type holdView struct {
	Phones  int
	On      bool
	Label   string // "On hold", "Taking calls" or "No phone yet"
	Until   string // "until tomorrow at 08:00", "holds calls at 19:00", or empty
	Waiting int    // calls waiting, for whatever reason
}

// hubHold is the hold now; the caller holds stateMu.
func hubHold(devices map[string]map[string]string, waiting int) holdView {
	h := holdView{Phones: len(devices), Waiting: waiting, Label: "No phone yet"}
	if len(devices) == 0 {
		return h
	}
	t := now()
	held, until := heldUntil(t)
	if held {
		h.On, h.Label, h.Until = true, "On hold", "until you take calls"
		if at, ok := parseISO8601(until); ok {
			h.Until = "until " + ahead(at)
		}
		return h
	}
	// Taking calls: it goes on hold by itself when the last phone taking calls does, by its
	// work hours.
	h.Label = "Taking calls"
	var last time.Time
	for _, d := range devices {
		hold := newHold(d, t)
		if hold.On {
			continue
		}
		if hold.Until.IsZero() {
			return h
		}
		if hold.Until.After(last) {
			last = hold.Until
		}
	}
	if !last.IsZero() {
		h.Until = "holds calls " + ahead(last)
	}
	return h
}

// ahead is a time to come: "at 19:00" today, "tomorrow at 08:00", "on Mon at 08:00" within
// the week, "on 12 Nov at 08:00" after.
func ahead(t time.Time) string {
	t, today := t.Local(), now().Local()
	day := func(x time.Time) time.Time { y, m, d := x.Date(); return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
	days := int(day(t).Sub(day(today)).Hours()/24 + 0.5)
	switch {
	case days <= 0:
		return "at " + t.Format("15:04")
	case days == 1:
		return "tomorrow at " + t.Format("15:04")
	case days < 7:
		return "on " + t.Format("Mon") + " at " + t.Format("15:04")
	}
	return "on " + t.Format("2 Jan") + " at " + t.Format("15:04")
}

type problemView struct{ Text, Tone string } // Tone: bad, or wait

// partView is a speech node or a Mac in the lists under the map.
type partView struct {
	Kind, Name, State string // Kind: speech or agent; State: working, problem or asleep
	Status, Detail    string
	Gone              bool // not linked now, so it can be forgotten
}

type sessionView struct{ Project, Subject, Mac, Since string }

type liveView struct {
	Project, Subject, State, For, Mac, Speech string
	Connected                                 bool
}

type waitingView struct {
	Project, Subject, Why, Rings string
	Question, NoSpeech           bool
}

type phoneView struct {
	Name, Environment, Token, Registered, CheckedIn, Hours, Hold, HoldSet, OnDevice, Push string
	OnHold, PushFailed                                                                    bool
}

type statusView struct {
	Headline, Tone, Detail string // Tone: good, wait, bad or unset
	Problems               []problemView
	Hold                   holdView
	Wide, Narrow           mapView
	Live                   []liveView
	Waiting                []waitingView
	Sessions               []sessionView
	Phones                 []phoneView
	Nodes, Macs            []partView
}

// heardWithin is how recently a phone must have reached the hub (registering or on a
// connection check, every 30 min) for its link through Nebula to count as working.
const heardWithin = 2 * time.Hour

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
	stateMu.Lock()
	waitingCalls, _ := listWaiting()
	devices := loadDevices()
	v.Hold = hubHold(devices, len(waitingCalls))
	stateMu.Unlock()
	config := loadConfig()
	var m mapModel

	// Calls, live: which Mac and which speech each one's on, to light its path on the map.
	liveMacs, liveNodes, liveOnPhone := map[string]bool{}, map[string]bool{}, false
	sort.Slice(calls, func(i, j int) bool { return calls[i].offeredAt.Before(calls[j].offeredAt) })
	for _, c := range calls {
		c.mu.Lock()
		answered, connected, ended := c.answeredAt, c.connectedAt, c.ended
		c.mu.Unlock()
		if ended {
			continue
		}
		lv := liveView{Mac: c.agent.name, Speech: c.node.name + " hears and speaks it", Connected: !connected.IsZero()}
		if c.phone {
			lv.Speech, liveOnPhone = "The phone hears and speaks it itself", true
		} else {
			liveNodes[c.node.name] = true
		}
		liveMacs[c.agent.name] = true
		if note, ok := noteOf(c.id); ok {
			lv.Project, lv.Subject = note.project, note.subject
		}
		switch {
		case answered.IsZero() && connected.IsZero():
			lv.State, lv.For = "Ringing", span(now().Sub(c.offeredAt))
		case answered.IsZero():
			lv.State, lv.For = "Ringing, its audio connected", span(now().Sub(c.offeredAt))
		case connected.IsZero():
			lv.State, lv.For = "Answered, connecting", span(now().Sub(answered))
		default:
			lv.State, lv.For = "On the call", span(now().Sub(maxTime(answered, connected)))
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
		v.Macs = append(v.Macs, partView{Kind: "agent", Name: a.l.name, State: "working", Status: "Linked " + since(a.l.since), Detail: count + " listening"})
		part := mapPart{Kind: "mac", State: "working", Name: a.l.name, Sub: count, Wire: "working", WireLive: liveMacs[a.l.name], Live: liveMacs[a.l.name]}
		if part.Live {
			part.Sub = "On a call"
		}
		m.Macs = append(m.Macs, part)
	}
	for _, name := range goneParts(seen["agent"], linkedMac) {
		last, _ := parseISO8601(seen["agent"][name].Last)
		v.Macs = append(v.Macs, partView{Kind: "agent", Name: name, State: "asleep", Gone: true,
			Status: "Asleep or off, last seen " + clock(last) + " (" + span(now().Sub(last)) + " ago)", Detail: "A Mac drops off while it sleeps, and links again as it wakes."})
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
		loaded, at := l.models()
		models, sub, brief := "Models idle: it loads them when a call is coming", "Models idle", "Idle"
		switch {
		case loaded:
			models, sub, brief = "Models loaded "+since(at), "Models loaded", "Loaded"
		case !at.IsZero():
			models = "Models idle since " + clock(at) + ": it let them go after the last call"
		}
		v.Nodes = append(v.Nodes, partView{Kind: "speech", Name: l.name, State: "working", Status: "Linked " + since(l.since), Detail: models})
		part := mapPart{Kind: "node", State: "working", Name: l.name, Sub: sub, Brief: brief, Wire: "working", WireLive: liveNodes[l.name], Live: liveNodes[l.name]}
		if part.Live {
			part.Sub = "On a call"
		}
		m.Nodes = append(m.Nodes, part)
	}
	for _, name := range goneParts(seen["speech"], linkedNode) {
		last, _ := parseISO8601(seen["speech"][name].Last)
		v.Nodes = append(v.Nodes, partView{Kind: "speech", Name: name, State: "problem", Gone: true,
			Status: "Offline since " + clock(last) + " (" + span(now().Sub(last)) + ")", Detail: "Start it again, or forget it if it's gone for good."})
		m.Nodes = append(m.Nodes, mapPart{Kind: "node", State: "problem", Name: name, Sub: "Offline", Wire: "problem"})
	}
	if len(m.Nodes) == 0 {
		m.Nodes = []mapPart{{Kind: "node", State: "unset", Name: "Speech node", Sub: "Not set up", Setup: "node", Wire: "unset"}}
	}

	// The phones, and Apple push between the hub and them.
	tokens := make([]string, 0, len(devices))
	for token := range devices {
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool { return devices[tokens[i]]["device"] < devices[tokens[j]]["device"] })
	pushSet, keyProblem := pushSetup(config, devices)
	m.Push = mapPart{Kind: "push", State: "working", Name: "Apple push", Sub: "No push yet", Wire: "working"}
	switch {
	case !pushSet:
		m.Push.State, m.Push.Sub, m.Push.Setup, m.Push.Wire = "unset", "Not set up", "push", "unset"
	case keyProblem != "":
		m.Push.State, m.Push.Sub, m.Push.Wire = "problem", "Key can't be read", "problem"
		v.Problems = append(v.Problems, problemView{"Calls can't ring: " + keyProblem + ".", "bad"})
	}
	var lastPushAt time.Time
	var speakers []string
	var ringNames []string
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
		hold := newHold(d, now())
		ph.OnHold = hold.On
		switch {
		case hold.On && hold.Until.IsZero():
			ph.Hold = "On hold, until you take calls"
		case hold.On:
			ph.Hold = "On hold, until " + ahead(hold.Until)
		case hold.Until.IsZero():
			ph.Hold = "Taking calls"
		default:
			ph.Hold = "Taking calls, holds calls " + ahead(hold.Until)
		}
		if set := d["hold_set"]; set != "" {
			ph.HoldSet = "Last set at " + isoClock(set)
		}
		// Apple push's link to the phone shows what's wrong between them; a fault on the hub's
		// side shows on the hub's link to Apple push.
		part := mapPart{Kind: "phone", State: "working", Name: name, Sub: "On-device voice off", Wire: "working", PushWire: "working"}
		if !pushSet {
			part.PushWire = "unset"
		}
		switch d["on_device"] {
		case "yes":
			ph.OnDevice, part.Sub, part.speaks = "On: it hears and speaks a call itself when no speech node is linked", "On-device voice on", true
			speakers = append(speakers, name)
		case "no":
			ph.OnDevice = "Off"
		}
		if now().Sub(heard) > heardWithin {
			part.Wire = "offline"
		}
		if last, found := lastPush(d, token); !found {
			ph.Push = "None since the hub started"
		} else if last.ok() {
			ph.Push = "Delivered to Apple at " + clock(last.at)
			lastPushAt = maxTime(lastPushAt, last.at)
		} else {
			ph.PushFailed = true
			ph.Push = "Failed at " + clock(last.at) + ": " + pushProblem(last)
			lastPushAt = maxTime(lastPushAt, last.at)
			switch last.problem() {
			case "key", "network":
				if m.Push.State == "working" {
					m.Push.State, m.Push.Wire = "problem", "problem"
					m.Push.Sub = map[string]string{"key": "Key refused", "network": "Can't reach Apple"}[last.problem()]
				}
			case "phone":
				part.State, part.PushWire = "problem", "problem"
			default:
				part.PushWire = "problem"
			}
			// A key the hub can't read is said once, above.
			if keyProblem == "" || last.problem() != "key" {
				v.Problems = append(v.Problems, problemView{"The last push to " + name + " failed: " + pushProblem(last) + ".", "bad"})
			}
		}
		if liveOnPhone || len(liveNodes) > 0 {
			part.Live, part.WireLive, part.Sub = true, true, "On a call"
		}
		ringNames = append(ringNames, name)
		m.Phones = append(m.Phones, part)
		v.Phones = append(v.Phones, ph)
	}
	if m.Push.State == "working" && !lastPushAt.IsZero() {
		m.Push.Sub = "Last push " + clock(lastPushAt)
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
				m.Nodes[i].Tag, m.Nodes[i].TagTone = "Speaks calls", "good"
			}
		}
		speech = orList(linkedNames) + " hears and speaks them"
	case len(speakers) > 0:
		for i := range m.Phones {
			if m.Phones[i].speaks {
				m.Phones[i].Tag, m.Phones[i].TagTone = "Speaks calls", "good"
			}
		}
		speech = speakers[0] + " hears and speaks them itself"
		if len(speakers) > 1 {
			speech = "the phone that answers hears and speaks them itself"
		}
	case len(agentList) > 0 || len(seen["agent"]) > 0:
		// Said once a Mac could place a call: before, nothing waits.
		m.SpeechNote = "None linked, so calls wait"
	}

	// A node that's gone is a problem; more so when nothing else can speak calls.
	for _, n := range v.Nodes {
		if n.Gone {
			text := n.Name + " is offline. Start it again"
			if speech == "" {
				text = n.Name + " is offline, so calls wait instead of ringing. Start it again, or turn on On-device voice in the app's settings"
			}
			v.Problems = append(v.Problems, problemView{text + ". If it's gone for good, forget it under Speech nodes.", "bad"})
		}
	}

	// What a call would do now, in a sentence.
	pushBroken := m.Push.State == "problem"
	for _, p := range m.Phones {
		pushBroken = pushBroken || p.PushWire == "problem"
	}
	switch {
	case len(devices) == 0:
		v.Tone, v.Headline = "unset", "No phone yet: calls have nowhere to ring. Open the app on your iPhone and enter the hub's address in its settings."
	case !pushSet:
		v.Tone, v.Headline = "unset", "Calls can't ring yet: Apple push isn't set up in the hub's config."
	case pushBroken:
		v.Tone, v.Headline = "bad", "Calls can't ring "+andList(ringNames)+": the push to Apple fails."
	case speech == "" && len(seen["speech"]) == 0:
		v.Tone, v.Headline = "wait", "Calls wait instead of ringing: there's no speech node, and On-device voice is off."
	case speech == "":
		v.Tone, v.Headline = "wait", "Calls wait instead of ringing: no speech node is linked, and On-device voice is off."
	default:
		v.Tone, v.Headline = "good", "Calls ring "+andList(ringNames)+", and "+speech+"."
		if len(nodeLinks) == 0 && len(ringNames) == 1 && len(speakers) == 1 {
			v.Headline = "Calls ring " + ringNames[0] + ", which hears and speaks them itself."
		}
	}
	if v.Hold.On && v.Tone != "unset" {
		v.Tone = "hold"
		v.Headline = "Once you take calls: " + strings.ToLower(v.Headline[:1]) + v.Headline[1:]
	}
	var detail []string
	if n := len(v.Live); n > 0 {
		detail = append(detail, plural(n, "call", "calls")+" live")
	}
	if n := len(waitingCalls); n > 0 {
		detail = append(detail, plural(n, "call", "calls")+" waiting")
	}
	switch macs := len(agentList); {
	case macs == 0 && len(seen["agent"]) == 0:
		detail = append(detail, "No Mac linked yet")
	case macs == 0:
		detail = append(detail, "No Mac linked: they're asleep or off")
	default:
		listening := plural(sessions, "session", "sessions") + " listening"
		if sessions == 0 {
			listening = "No session listening"
		}
		detail = append(detail, listening+" on "+plural(macs, "Mac", "Macs"))
	}
	v.Detail = strings.Join(detail, ". ") + "."

	// Calls waiting.
	for _, call := range waitingCalls {
		wv := waitingView{Question: call["question"] == true}
		wv.Project, _ = call["project"].(string)
		wv.Subject, _ = call["subject"].(string)
		switch until, _ := call["held_until"].(string); {
		case call["reason"] == "no speech":
			wv.NoSpeech, wv.Why, wv.Rings = true, "No speech", "Rings once a speech node links"
		case until != "":
			wv.Why, wv.Rings = "On hold", "Rings "+isoAhead(until)
		default:
			wv.Why, wv.Rings = "On hold", "Rings when you take calls"
		}
		v.Waiting = append(v.Waiting, wv)
	}

	m.Held = v.Hold.On
	m.Hub = mapPart{Kind: "hub", State: "working", Name: "Hub", TagTone: "neutral"}
	if v.Hold.Phones > 0 {
		m.Hub.Tag = v.Hold.Label
	}
	if v.Hold.On {
		m.Hub.TagTone = "hold"
	}
	m.Title = v.Headline
	v.Wide, v.Narrow = m.wide(), m.narrow()
	return v
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

// pushSetup says whether Apple push is set up in the hub's config (a team, a bundle ID and a
// key for at least one environment) and what's wrong with it if anything is: a key that
// can't be read, or none for the environment a phone's build gets its pushes from.
func pushSetup(config map[string]string, devices map[string]map[string]string) (bool, string) {
	has := func(env string) bool {
		return config["apns."+env+".key_id"] != "" && config["apns."+env+".key_file"] != ""
	}
	if config["apns.team_id"] == "" || config["apns.bundle_id"] == "" || !(has("sandbox") || has("production")) {
		return false, ""
	}
	for _, env := range []string{"sandbox", "production"} {
		if !has(env) {
			continue
		}
		file := config["apns."+env+".key_file"]
		if _, err := readPushKey(expand(file)); errors.Is(err, fs.ErrNotExist) {
			return true, "the hub's " + env + " push key, " + file + ", isn't there"
		} else if err != nil {
			return true, fmt.Sprintf("the hub can't read its %s push key, %s (%v)", env, file, err)
		}
	}
	for _, d := range devices {
		if env := orDefault(d["environment"], "sandbox"); !has(env) {
			return true, orDefault(d["device"], "a phone") + " gets its pushes from Apple's " + env + " environment, and the hub has no key for it"
		}
	}
	return true, ""
}
