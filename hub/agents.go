package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Who's connected: the agents (one per Mac with sessions) and the speech nodes, which call
// each agent placed, and the calls whose audio is live.
var (
	registryMu sync.Mutex
	agents     = map[*link][]map[string]string{} // each agent's sessions, as it last said
	nodes      = map[*link]bool{}
	placedBy   = map[string]*link{} // call ID → the agent that placed it
	live       = map[string]*Call{} // call ID → its call, while its audio is up
	byStream   = map[uint32]*Call{}
	streams    atomic.Uint32
)

func nextStream() uint32 { return streams.Add(1) }

// hubURL is how the phone reaches the hub, which the pushes and the Sessions tab give as the
// call's voice server: `url` in the config, or the address it listens on.
func hubURL() string {
	config := loadConfig()
	if url := config["url"]; url != "" {
		return strings.TrimSuffix(url, "/")
	}
	return "http://" + config["listen"]
}

// agentKey is how an agent's sessions are kept among the voice servers' in contacts.json.
func agentKey(l *link) string { return "agent:" + l.name }

func agentOpened(l *link) {
	registryMu.Lock()
	agents[l] = nil
	registryMu.Unlock()
}

func agentClosed(l *link) {
	registryMu.Lock()
	delete(agents, l)
	for id, a := range placedBy {
		if a == l {
			delete(placedBy, id)
		}
	}
	var gone []*Call
	for _, c := range live {
		if c.agent == l {
			gone = append(gone, c)
		}
	}
	registryMu.Unlock()
	// Its calls can't go on without it; it ends them on its side too.
	for _, c := range gone {
		c.say("its agent went away")
		c.end("disconnected")
	}
	stateMu.Lock()
	dropContacts(agentKey(l))
	stateMu.Unlock()
}

// fromAgent handles what an agent sends, outside a call (sessions, place) and in one.
func fromAgent(l *link, message map[string]any) {
	switch message["type"] {
	case "sessions":
		given, _ := message["sessions"].([]any)
		stateMu.Lock()
		listed, err := keepSessions(agentKey(l), hubURL(), given)
		stateMu.Unlock()
		if err != nil {
			log("agent " + l.name + ": sessions refused: " + err.Error())
			return
		}
		registryMu.Lock()
		agents[l] = listed
		registryMu.Unlock()
	case "place":
		message["voice"] = hubURL()
		stateMu.Lock()
		answer, err := call(message)
		stateMu.Unlock()
		if err != nil {
			reply(l, message, map[string]any{"ok": false, "status": 400, "error": err.Error()})
			return
		}
		registryMu.Lock()
		placedBy[str(message, "call_id")] = l
		registryMu.Unlock()
		delete(answer, "type")
		reply(l, message, answer)
	case "say", "said", "hang_up":
		c := liveCall(str(message, "call_id"))
		if c == nil || c.agent != l {
			return
		}
		n := int(number(message["reply"]))
		if n < 1 && message["type"] != "hang_up" {
			c.say(fmt.Sprintf("%s without a reply number, dropped", message["type"]))
			return
		}
		switch message["type"] {
		case "say":
			c.write(n, str(message, "text"))
		case "said":
			c.written(n)
		case "hang_up":
			c.end("hung up by the voice")
		}
	}
}

func nodeOpened(l *link) {
	registryMu.Lock()
	nodes[l] = true
	registryMu.Unlock()
}

func nodeClosed(l *link) {
	registryMu.Lock()
	delete(nodes, l)
	var gone []*Call
	for _, c := range live {
		if c.node == l {
			gone = append(gone, c)
		}
	}
	registryMu.Unlock()
	for _, c := range gone {
		c.say("its speech node went away")
		c.end("disconnected")
	}
}

// aNode is a connected speech node, or nil.
func aNode() *link {
	registryMu.Lock()
	defer registryMu.Unlock()
	for l := range nodes {
		return l
	}
	return nil
}

func fromNode(_ *link, message map[string]any) {
	registryMu.Lock()
	c := byStream[uint32(number(message["stream"]))]
	registryMu.Unlock()
	if c == nil {
		return
	}
	switch message["type"] {
	case "speech_started":
		c.speechStarted()
	case "speech_stopped":
		c.speechStopped()
	case "turn":
		c.turn(str(message, "text"), number(message["secs"]), number(message["took"]))
	case "spoken":
		c.spoken(int(number(message["piece"])), number(message["secs"]), number(message["took"]))
	}
}

// nodeAudio: a binary frame from a node is the voice's audio for a piece: the stream, the
// piece, then the samples.
func nodeAudio(_ *link, data []byte) {
	if len(data) < 8 {
		return
	}
	stream, pieceID := binary.BigEndian.Uint32(data), int(binary.BigEndian.Uint32(data[4:]))
	registryMu.Lock()
	c := byStream[stream]
	registryMu.Unlock()
	if c == nil {
		return
	}
	samples := make([]int16, (len(data)-8)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(data[8+2*i:]))
	}
	c.audio(pieceID, samples)
}

func liveCall(id string) *Call {
	registryMu.Lock()
	defer registryMu.Unlock()
	return live[id]
}

func keepCall(c *Call) (earlier *Call) {
	registryMu.Lock()
	defer registryMu.Unlock()
	earlier = live[c.id]
	live[c.id] = c
	byStream[c.stream] = c
	return earlier
}

func forgetCall(c *Call) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if live[c.id] == c {
		delete(live, c.id)
	}
	delete(byStream, c.stream)
}

// ask sends a request to the agent that placed a call, or, not knowing it, to every agent,
// and returns the first that accepts; otherwise the most telling refusal.
func ask(callID, session string, message map[string]any) (*link, map[string]any) {
	registryMu.Lock()
	var targets []*link
	if a := placedBy[callID]; a != nil && session == "" {
		targets = []*link{a}
	} else {
		for a, listed := range agents {
			if session == "" || listsSession(listed, session) {
				targets = append(targets, a)
			}
		}
	}
	registryMu.Unlock()
	refusal := map[string]any{"ok": false, "status": 403.0, "error": "unknown call"}
	if session != "" {
		refusal["error"] = "not listening"
	}
	for _, a := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		answer, err := a.request(ctx, copyMessage(message))
		cancel()
		if err != nil {
			continue
		}
		if answer["ok"] == true {
			return a, answer
		}
		refusal = answer
	}
	return nil, refusal
}

func listsSession(listed []map[string]string, session string) bool {
	for _, s := range listed {
		if s["session_id"] == session {
			return true
		}
	}
	return false
}

func copyMessage(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// broadcast sends a message to every agent.
func broadcast(message map[string]any) {
	registryMu.Lock()
	list := make([]*link, 0, len(agents))
	for a := range agents {
		list = append(list, a)
	}
	registryMu.Unlock()
	for _, a := range list {
		_ = a.send(message)
	}
}
