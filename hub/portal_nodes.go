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
// ("speech" or "agent") and name, each with when it first linked ("first") and when it was
// last linked ("last", written as it links and as it goes). The status page tells a part
// that's gone (a Mac asleep, a speech node offline) from one never set up by it. One gone for
// good is forgotten there.
var seenPath = filepath.Join(stateDir, "seen.json")

type seenPart struct {
	First string `json:"first"`
	Last  string `json:"last"`
}

var seenMu sync.Mutex

func loadSeen() map[string]map[string]seenPart {
	seen := map[string]map[string]seenPart{}
	readJSON(seenPath, &seen)
	return seen
}

// sawLink notes a speech node or a Mac as it links and as it goes.
func sawLink(l *link) {
	seenMu.Lock()
	defer seenMu.Unlock()
	seen := loadSeen()
	if seen[l.kind] == nil {
		seen[l.kind] = map[string]seenPart{}
	}
	part := seen[l.kind][l.name]
	part.Last = isoNow()
	if part.First == "" {
		part.First = part.Last
	}
	seen[l.kind][l.name] = part
	if err := writeJSON(seenPath, seen); err != nil {
		log("can't keep the parts seen: " + err.Error())
	}
}

// forgetPart takes a speech node or a Mac off seen.json, as the status page's Forget does;
// false if it's linked now, or wasn't there.
func forgetPart(kind, name string) (bool, error) {
	registryMu.Lock()
	linkedNow := false
	for l := range nodes {
		linkedNow = linkedNow || (kind == "speech" && l.name == name)
	}
	for l := range agents {
		linkedNow = linkedNow || (kind == "agent" && l.name == name)
	}
	registryMu.Unlock()
	if linkedNow {
		return false, nil
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

// What each call is about, as it was placed (its project and subject), for the portal's
// lists of calls: the Call itself doesn't keep them. In memory, the last callNotesKept, so
// calls from before the hub started show without their project.
const callNotesKept = 500

type callNote struct {
	project, subject string
	at               time.Time
}

var (
	callNotesMu sync.Mutex
	callNotes   = map[string]callNote{}
)

// noteCall keeps what a call is about, as an agent or a voice server places it, or the phone
// calls a session.
func noteCall(callID, project, subject string) {
	if callID == "" || project == "" {
		return
	}
	callNotesMu.Lock()
	defer callNotesMu.Unlock()
	callNotes[callID] = callNote{project, subject, now()}
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
