package main

import (
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// What the portal keeps about the parts of the system, beside the registry in agents.go: the
// speech nodes and Macs the hub has seen, whether each node's models are loaded, and what each
// call is about.

// The speech nodes and Macs the hub has seen: seen.json in the state folder, by kind
// ("speech" or "agent") and name, each with when it first linked ("first"), when it was last
// linked ("last", written as it links and as it goes), and whether it's linked now
// ("linked"). The status page tells a part that's gone (a Mac asleep, a speech node offline)
// from one never set up by it. One gone for good is forgotten there.
var seenPath = filepath.Join(stateDir, "seen.json")

type seenPart struct {
	First  string `json:"first"`
	Last   string `json:"last"`
	Linked bool   `json:"linked,omitempty"`
}

var seenMu sync.Mutex

func loadSeen() map[string]map[string]seenPart {
	seen := map[string]map[string]seenPart{}
	readJSON(seenPath, &seen)
	return seen
}

// sawLink notes a speech node or a Mac as it links (linked) and as it goes; not one that goes
// because it was unpaired, which forgetPart has taken off.
func sawLink(l *link, linked bool) {
	if l.unpaired.Load() {
		return
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	seen := loadSeen()
	if seen[l.kind] == nil {
		seen[l.kind] = map[string]seenPart{}
	}
	part := seen[l.kind][l.name]
	part.Last, part.Linked = isoNow(), linked
	if part.First == "" {
		part.First = part.Last
	}
	seen[l.kind][l.name] = part
	if err := writeJSON(seenPath, seen); err != nil {
		log("can't keep the parts seen: " + err.Error())
	}
}

// partsGoneAtStart: as the hub starts, the parts that were linked when it last stopped are
// gone as of now. Otherwise one would show as gone since it last linked, which could be days.
func partsGoneAtStart() {
	seenMu.Lock()
	defer seenMu.Unlock()
	seen, changed := loadSeen(), false
	for _, parts := range seen {
		for name, part := range parts {
			if part.Linked {
				part.Last, part.Linked, changed = isoNow(), false, true
				parts[name] = part
			}
		}
	}
	if changed {
		if err := writeJSON(seenPath, seen); err != nil {
			log("can't keep the parts seen: " + err.Error())
		}
	}
}

// forgetPart takes a speech node or a Mac off seen.json, as the status page's Forget and
// Unpair do, and drops its key if it paired. One linked now with its own key is unpaired: its
// link closes, and it can't link again until it pairs again. False if it's linked with the hub's
// token, as it would only link again, or wasn't there.
func forgetPart(kind, name string) (bool, error) {
	registryMu.Lock()
	var linked []*link
	for l := range nodes {
		if kind == "speech" && l.name == name {
			linked = append(linked, l)
		}
	}
	for l := range agents {
		if kind == "agent" && l.name == name {
			linked = append(linked, l)
		}
	}
	registryMu.Unlock()
	for _, l := range linked {
		if !l.byKey {
			return false, nil
		}
	}
	if err := forgetPartKey(kind, name); err != nil {
		return false, err
	}
	for _, l := range linked {
		l.unpaired.Store(true)
		l.cancel()
		l.ws.CloseNow()
		log("the " + partKinds[kind] + " " + name + ": unpaired, its link is closed")
	}
	if len(linked) > 0 {
		seenMu.Lock()
		defer seenMu.Unlock()
		seen := loadSeen()
		delete(seen[kind], name)
		return true, writeJSON(seenPath, seen)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	seen := loadSeen()
	if _, ok := seen[kind][name]; !ok {
		return false, nil
	}
	delete(seen[kind], name)
	return true, writeJSON(seenPath, seen)
}

// nodeMessage takes what a speech node says about itself, whether its models are loaded
// (`models`), and gives everything else to fromNode.
func nodeMessage(l *link, message map[string]any) {
	if message["type"] != "models" {
		fromNode(l, message)
		return
	}
	loaded, _ := message["loaded"].(bool)
	l.modelsMu.Lock()
	l.loaded, l.loadedAt = loaded, now()
	l.modelsMu.Unlock()
	if loaded {
		log("speech " + l.name + ": models loaded")
	} else {
		log("speech " + l.name + ": models let go")
	}
}

// models is whether a node's models are loaded, as it last said, and since when; zero time
// if it hasn't said since it linked.
func (l *link) models() (bool, time.Time) {
	l.modelsMu.Lock()
	defer l.modelsMu.Unlock()
	return l.loaded, l.loadedAt
}

// What each call is about, as it was placed (its project and subject), and the Mac it's from,
// for the portal's lists of calls: the Call itself doesn't keep them. In memory, the last
// callNotesKept, so calls from before the hub started show without them.
const callNotesKept = 500

type callNote struct {
	project, subject, mac string
	at                    time.Time
}

var (
	callNotesMu sync.Mutex
	callNotes   = map[string]callNote{}
)

// noteCall keeps what a call is about, as an agent places it, or the phone calls a session.
func noteCall(callID, project, subject string) {
	if project != "" {
		keepNote(callID, func(n *callNote) { n.project, n.subject = project, subject })
	}
}

// noteMac keeps which Mac a call is from, as its agent places it or takes its offer.
func noteMac(callID, mac string) {
	keepNote(callID, func(n *callNote) { n.mac = mac })
}

func keepNote(callID string, set func(*callNote)) {
	if callID == "" {
		return
	}
	callNotesMu.Lock()
	defer callNotesMu.Unlock()
	note := callNotes[callID]
	set(&note)
	if note.at.IsZero() {
		note.at = now()
	}
	callNotes[callID] = note
	if len(callNotes) <= callNotesKept {
		return
	}
	ids := make([]string, 0, len(callNotes))
	for id := range callNotes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return callNotes[ids[i]].at.Before(callNotes[ids[j]].at) })
	for _, id := range ids[:len(ids)-callNotesKept] {
		delete(callNotes, id)
	}
}

// noteContactCall keeps what a call to a session from the Sessions tab is about: the
// session's project and its latest subject.
func noteContactCall(callID, session string) {
	stateMu.Lock()
	listed, _ := listContacts()
	stateMu.Unlock()
	for _, contact := range listed {
		if contact["session_id"] == session {
			noteCall(callID, contact["project"], contact["subject"])
			return
		}
	}
}

func noteOf(callID string) (callNote, bool) {
	callNotesMu.Lock()
	defer callNotesMu.Unlock()
	note, ok := callNotes[callID]
	return note, ok
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
