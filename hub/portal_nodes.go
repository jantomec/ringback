package main

import (
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// What the portal keeps about speech nodes and calls, beside the registry in agents.go: the
// nodes you paused, whether each node's models are loaded, and what each call is about.

// The speech nodes' settings, set on the portal: nodes.json in the state folder, keyed by the
// node's name (its hello), each with when it was paused ("paused"). A paused node stays
// linked, but the hub neither warms it nor gives it calls, so calls wait for want of speech
// unless another node is linked.
var nodesPath = filepath.Join(stateDir, "nodes.json")

var (
	pausedMu     sync.Mutex
	pausedLoaded bool
	nodeSettings map[string]map[string]string
)

// loadNodeSettings reads nodes.json once; the caller holds pausedMu.
func loadNodeSettings() {
	if pausedLoaded {
		return
	}
	nodeSettings = map[string]map[string]string{}
	readJSON(nodesPath, &nodeSettings)
	pausedLoaded = true
}

// nodeUsable says whether the hub may warm a linked speech node and give it calls: not if
// you paused it on the portal. It's called with registryMu held, so it takes only its own.
func nodeUsable(l *link) bool {
	pausedMu.Lock()
	defer pausedMu.Unlock()
	loadNodeSettings()
	return nodeSettings[l.name]["paused"] == ""
}

// pausedNodes are the names of the nodes you paused, linked or not, with when.
func pausedNodes() map[string]string {
	pausedMu.Lock()
	defer pausedMu.Unlock()
	loadNodeSettings()
	paused := map[string]string{}
	for name, settings := range nodeSettings {
		if at := settings["paused"]; at != "" {
			paused[name] = at
		}
	}
	return paused
}

// pauseNode pauses or resumes a node by name, keeping it in nodes.json. Calls on it go on;
// resumed, and the hub's only usable node, the calls that waited for speech ask again.
func pauseNode(name string, pause bool) error {
	pausedMu.Lock()
	loadNodeSettings()
	settings := nodeSettings[name]
	if settings == nil {
		settings = map[string]string{}
	}
	if pause {
		settings["paused"] = isoNow()
	} else {
		delete(settings, "paused")
	}
	if len(settings) == 0 {
		delete(nodeSettings, name)
	} else {
		nodeSettings[name] = settings
	}
	err := writeJSON(nodesPath, nodeSettings)
	pausedMu.Unlock()
	if err != nil {
		return err
	}
	if pause {
		log("speech " + name + ": paused on the portal; the hub neither warms it nor gives it calls")
		return nil
	}
	log("speech " + name + ": resumed on the portal")
	if speechLinked() {
		stateMu.Lock()
		ids := waitingFor("no speech")
		stateMu.Unlock()
		if len(ids) > 0 {
			log("speech " + name + ": " + plural(len(ids), "call", "calls") + " waited for speech, they ask again")
			broadcast(map[string]any{"type": "reask", "call_ids": ids})
		}
	}
	return nil
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
