package main

import (
	"context"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// The phone as the speech node (docs/protocol.md): with no speech node linked and its own
// voice on, the phone hears and speaks a call itself, and its link to the hub carries only
// text. The call is the same as one with a node, but no audio comes through the hub: the phone
// says when each piece starts and finishes playing, and the hub counts what you've heard from
// that, where it would count the audio it plays.

// phoneSpeech is a phone's link for one call, `GET /phone-speech`. The call's ID is its
// secret, as for an offer: the agent that placed the call accepts it, or it's refused.
func phoneSpeech(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		ws.CloseNow()
	}()
	hello, err := readFrame(ctx, ws, 10*time.Second)
	if err != nil || hello["type"] != "hello" || number(hello["version"]) != protocolVersion {
		ws.Close(websocket.StatusPolicyViolation, "needs hello, version 1")
		return
	}
	callID := str(hello, "call_id")
	short := prefix(callID, 8)
	l := &link{kind: "phone", name: short, ws: ws, ctx: ctx, cancel: cancel, waiting: map[int64]chan map[string]any{}}
	refuse := func(status int, why string) {
		log("call " + short + ": the phone's own speech refused, " + why)
		_ = l.send(map[string]any{"type": "refused", "status": status, "error": why})
		ws.Close(websocket.StatusNormalClosure, why)
	}
	kind := orDefault(str(hello, "kind"), "ring")
	session := ""
	if kind == "contact" {
		session = str(hello, "session_id")
		if session == "" || utf8.RuneCountInString(callID) < 16 {
			refuse(http.StatusForbidden, "not listening")
			return
		}
	}
	ringing := hello["ringing"] == true && kind == "ring"
	request := map[string]any{"type": "offer", "call_id": callID, "kind": kind, "ringing": ringing}
	if session != "" {
		request["session_id"] = session
	}
	agent, answer := ask(callID, session, request)
	if agent == nil {
		status := int(number(answer["status"]))
		if status == 0 {
			status = http.StatusForbidden
		}
		refuse(status, orDefault(str(answer, "error"), "unknown call"))
		return
	}
	c := &Call{
		id: callID, short: short, agent: agent, node: l, phone: true, stream: nextStream(), ringing: ringing,
		sensitivity: setting(hello, "sensitivity", 0.5), gate: setting(hello, "background_filter", 0.5), offeredAt: now(),
	}
	registryMu.Lock()
	placedBy[callID] = agent
	registryMu.Unlock()
	if earlier := keepCall(c); earlier != nil {
		c.say("a new offer replaces the earlier one")
		earlier.end("replaced")
	}
	if l.send(map[string]any{"type": "welcome", "version": protocolVersion}) != nil {
		c.end("disconnected")
		return
	}
	how := map[string]string{"ring": "", "now": ", called now", "back": ", called back", "contact": ", from Sessions"}[kind]
	if ringing {
		how = ", ringing"
	}
	c.say("the phone hears and speaks itself" + how)
	c.connected()
	if !ringing {
		c.markAnswered()
	}
	go keepAlive(l)
	for {
		message, err := readFrame(ctx, ws, time.Hour)
		if err != nil {
			break
		}
		switch message["type"] {
		case "answered":
			c.markAnswered()
		case "speech_started":
			c.speechStarted()
		case "speech_stopped":
			c.speechStopped()
		case "turn":
			c.turn(str(message, "text"), number(message["secs"]), number(message["took"]))
		case "spoken":
			c.phoneSpoken(int(number(message["piece"])), number(message["secs"]), number(message["took"]))
		case "playing":
			c.phonePlaying(int(number(message["piece"])))
		case "played":
			c.phonePlayed(int(number(message["piece"])))
		}
	}
	c.end("disconnected")
}

// hearIfLive tells a phone that speaks itself to listen, once you've answered and its link is
// up. (A speech node is told at once, and the hub sends it your audio only once you've
// answered.)
func (c *Call) hearIfLive() {
	c.mu.Lock()
	live := c.phone && !c.hearing && !c.answeredAt.IsZero() && !c.connectedAt.IsZero() && !c.ended
	if live {
		c.hearing = true
	}
	c.mu.Unlock()
	if live {
		_ = c.node.send(map[string]any{"type": "hear"})
		c.say(fmt.Sprintf("the phone listens, sensitivity %.2f, background filter %.2f", c.sensitivity, c.gate))
	}
}

// phoneSpoken: all of a piece's audio is made, on the phone, and this long.
func (c *Call) phoneSpoken(pieceID int, secs, took float64) {
	c.mu.Lock()
	if p := c.piece(pieceID); p != nil {
		p.samples = int(secs * speakingRate)
	}
	c.mu.Unlock()
	c.spoken(pieceID, secs, took)
}

// phonePlaying: a piece starts playing on the phone. The first that plays after a pause, the
// voice is speaking; what you've heard goes to the agent as it plays, as the hub's own playing
// does.
func (c *Call) phonePlaying(pieceID int) {
	c.mu.Lock()
	p := c.piece(pieceID)
	if p == nil || c.ended {
		c.mu.Unlock()
		return
	}
	t := now()
	if p.start.IsZero() {
		p.start = t
	}
	starting := !c.playing
	if starting {
		c.playing, c.idleSent = true, false
		if !c.voiceStarted {
			c.voiceStarted = true
			c.say(fmt.Sprintf("voice started %.2f s after you answered", t.Sub(c.answeredAt).Seconds()))
		}
	}
	c.mu.Unlock()
	if starting {
		_ = c.agent.send(map[string]any{"type": "speaking", "call_id": c.id})
		go c.tellHeard()
	}
}

// tellHeard sends the agent what you've heard every 100 ms while the phone plays.
func (c *Call) tellHeard() {
	for {
		time.Sleep(100 * time.Millisecond)
		c.mu.Lock()
		if c.ended || !c.playing {
			c.mu.Unlock()
			return
		}
		heard := c.heard(now())
		changed := heard != c.heardSent && heard != ""
		if changed {
			c.heardSent = heard
		}
		reply := c.replyNo
		c.mu.Unlock()
		if changed {
			_ = c.agent.send(map[string]any{"type": "heard", "call_id": c.id, "reply": reply, "text": heard})
		}
	}
}

// phonePlayed: a piece has played to its end. Once every piece so far has, the voice has
// stopped: you've heard them all, and it may be idle.
func (c *Call) phonePlayed(pieceID int) {
	c.mu.Lock()
	p := c.piece(pieceID)
	if p == nil || c.ended {
		c.mu.Unlock()
		return
	}
	p.played = true
	for _, other := range c.pieces {
		if !other.played {
			c.mu.Unlock()
			return
		}
	}
	c.playing = false
	heard := c.heard(now())
	var final map[string]any
	if heard != c.heardSent && heard != "" {
		c.heardSent = heard
		final = map[string]any{"type": "heard", "call_id": c.id, "reply": c.replyNo, "text": heard}
	}
	c.mu.Unlock()
	if final != nil {
		_ = c.agent.send(final)
	}
	c.mu.Lock()
	c.checkIdle()
	c.mu.Unlock()
}
