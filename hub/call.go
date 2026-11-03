package main

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// A call in the hub, from the phone's offer until it ends. The hub owns the call's timing
// (see "Who does what in a call" in docs/design.md): your audio goes to the speech node; the
// node says when you start and stop speaking and what you said; the agent writes the replies,
// which the hub cuts into pieces, has the node speak, and plays to the phone, keeping track of
// how much of each you've heard.

// lead is how long before the current piece ends the next is handed over.
const lead = time.Second

// quietAfter is how long the voice waits, once it's idle and you haven't spoken since, before
// it tells the agent (`quiet`): an answer it asked for may have gone unheard.
const quietAfter = 8 * time.Second

type piece struct {
	id      int // unique in the call, so audio for a piece that was cut off can be told apart
	text    string
	samples int       // of its audio, at 24 kHz, so far
	done    bool      // all its audio has come
	start   time.Time // when its first audio played; zero until then
	partial []int16   // audio not yet a whole 20 ms frame
	played  bool      // all of it has played (a phone that speaks itself says so)
}

func (p *piece) secs() float64 { return float64(p.samples) / speakingRate }

type outFrame struct {
	piece int
	pcm   []int16 // 480 samples: 20 ms at 24 kHz
}

type Call struct {
	id, short string
	// phone: the phone hears and speaks itself (phonespeech.go), and node is its link: no
	// audio comes through the hub, and the phone says what it plays.
	phone           bool
	hearing         bool // a phone that speaks itself was told to listen
	agent, node     *link
	stream          uint32
	ringing         bool
	sensitivity     float64
	gate            float64
	pc              *webrtc.PeerConnection
	track           *webrtc.TrackLocalStaticSample
	enc             *opusEncoder
	offeredAt       time.Time
	mu              sync.Mutex
	answeredAt      time.Time
	connectedAt     time.Time
	lastHeard       time.Time // when your audio last came (for how long you talked)
	ended, replaced bool
	voiceStarted    bool
	replying        bool // a reply is being spoken or written
	replyNo         int  // its number, as the agent gave it
	lastReply       int  // the highest number of a reply the agent has sent
	cut             int  // replies up to this number were cut off: what more comes of them is dropped
	awaiting        bool // your turn went to the agent, and nothing of its reply has come yet
	cutThisTurn     bool // the voice was cut off since your last turn
	finishedOnce    bool // the voice has finished speaking once (the opening)
	text            string
	handed          int
	replyEnded, due bool
	generation      int // a reply or interruption bumps it, so stale work stops
	pieces          []*piece
	nextPiece       int
	queue           []outFrame
	playing         bool
	userSpeaking    bool
	idleSent        bool
	quietSince      int // bumped when the voice goes idle or you start speaking, so a stale quiet isn't sent
	heardSent       string
}

func (c *Call) say(line string) { log("call " + c.short + ": " + line) }

// newCall answers the phone's offer: a peer connection offering only the hub's own address,
// with Opus asked of the phone as phoneOpus says. It returns the
// answer's SDP once its addresses are gathered.
func newCall(id string, offer string, ringing bool, sensitivity, gate float64, agent, node *link, listen string) (*Call, string, error) {
	c := &Call{id: id, short: prefix(id, 8), agent: agent, node: node, ringing: ringing,
		sensitivity: sensitivity, gate: gate, offeredAt: now(), stream: nextStream()}
	engine := &webrtc.MediaEngine{}
	if err := engine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: rtpRate, Channels: 2, SDPFmtpLine: phoneOpus},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, "", err
	}
	settings := webrtc.SettingEngine{}
	host, _, _ := net.SplitHostPort(listen)
	if ip := net.ParseIP(host); ip != nil {
		settings.SetIPFilter(func(candidate net.IP) bool { return candidate.Equal(ip) })
		// Pion leaves loopback out, which a hub on localhost (tools/hub-check) needs.
		settings.SetIncludeLoopbackCandidate(ip.IsLoopback())
	}
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	// Patient with a slow network: a call on 3G or worse lost its consent checks with
	// shorter limits.
	settings.SetICETimeouts(15*time.Second, 30*time.Second, 2*time.Second)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithSettingEngine(settings))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	c.pc = pc
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: rtpRate, Channels: 2}, "voice", "ringback")
	if err != nil {
		pc.Close()
		return nil, "", err
	}
	if _, err := pc.AddTrack(track); err != nil {
		pc.Close()
		return nil, "", err
	}
	c.track = track
	if c.enc, err = newOpusEncoder(); err != nil {
		pc.Close()
		return nil, "", err
	}
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { go c.hear(remote) })
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			c.connected()
		case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			c.end("disconnected")
		}
	})
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		c.close()
		return nil, "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		c.close()
		return nil, "", err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		c.close()
		return nil, "", err
	}
	<-gathered
	_ = node.send(map[string]any{"type": "hear", "stream": c.stream, "sensitivity": sensitivity, "gate": gate})
	c.say(fmt.Sprintf("sensitivity %.2f, background filter %.2f", sensitivity, gate))
	go c.play()
	return c, pc.LocalDescription().SDP, nil
}

func (c *Call) close() {
	if c.phone {
		c.node.cancel() // its link: the phone hangs up with it
		return
	}
	c.pc.Close()
	if c.enc != nil {
		c.enc.close()
	}
}

// answered: you answered, on /answered, or with an offer made after answering.
func (c *Call) markAnswered() {
	c.mu.Lock()
	if !c.answeredAt.IsZero() || c.ended {
		c.mu.Unlock()
		return
	}
	c.answeredAt = now()
	c.mu.Unlock()
	c.say("answered")
	_ = c.agent.send(map[string]any{"type": "answered", "call_id": c.id})
	c.hearIfLive()
}

func (c *Call) connected() {
	c.mu.Lock()
	if !c.connectedAt.IsZero() || c.ended {
		c.mu.Unlock()
		return
	}
	c.connectedAt = now()
	c.mu.Unlock()
	c.say(fmt.Sprintf("connected, %.2f s after the offer", now().Sub(c.offeredAt).Seconds()))
	_ = c.agent.send(map[string]any{"type": "connected", "call_id": c.id})
	c.hearIfLive()
}

// hear reads your audio: packets through the jitter buffer and the decoder, then in 20 ms
// frames at 16 kHz to the speech node, once you've answered.
func (c *Call) hear(remote *webrtc.TrackRemote) {
	decoder, err := newOpusDecoder()
	if err != nil {
		c.say("can't decode your audio: " + err.Error())
		return
	}
	defer decoder.close()
	buffer := newJitterBuffer()
	var pending []int16
	for {
		packet, _, err := remote.ReadRTP()
		if err != nil {
			c.end("disconnected")
			return
		}
		c.mu.Lock()
		answered, ended := !c.answeredAt.IsZero(), c.ended
		if answered {
			c.lastHeard = now()
		}
		c.mu.Unlock()
		if ended {
			return
		}
		for _, p := range buffer.add(rtpPacket{packet.SequenceNumber, packet.Timestamp, packet.Payload}) {
			samples, err := decoder.decode(p.payload, p.timestamp)
			if err != nil || !answered {
				continue
			}
			pending = append(pending, samples...)
			for len(pending) >= hearingRate/50 {
				_ = c.node.sendAudio(c.stream, pending[:hearingRate/50])
				pending = pending[hearingRate/50:]
			}
		}
	}
}

// play sends the voice's audio every 20 ms, once you've answered and the audio connected,
// and keeps the agent told of what you've heard.
func (c *Call) play() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	frames := 0
	for range ticker.C {
		c.mu.Lock()
		if c.ended {
			c.mu.Unlock()
			return
		}
		live := !c.answeredAt.IsZero() && !c.connectedAt.IsZero()
		if !live || len(c.queue) == 0 {
			var final map[string]any
			if c.playing && len(c.queue) == 0 {
				c.playing = false
				// All of it has played, its last frame within the tick: you've heard it to the end.
				if heard := c.heard(now().Add(20 * time.Millisecond)); heard != c.heardSent {
					c.heardSent = heard
					final = map[string]any{"type": "heard", "call_id": c.id, "reply": c.replyNo, "text": heard}
				}
				if final != nil {
					c.mu.Unlock()
					_ = c.agent.send(final)
					c.mu.Lock()
				}
				c.checkIdle()
			}
			c.mu.Unlock()
			continue
		}
		f := c.queue[0]
		c.queue = c.queue[1:]
		t := now()
		if p := c.piece(f.piece); p != nil && p.start.IsZero() {
			p.start = t
		}
		var events []map[string]any
		if !c.playing {
			c.playing, c.idleSent = true, false
			events = append(events, map[string]any{"type": "speaking", "call_id": c.id})
			if !c.voiceStarted {
				c.voiceStarted = true
				c.say(fmt.Sprintf("voice started %.2f s after you answered", t.Sub(c.answeredAt).Seconds()))
			}
		}
		frames++
		if frames%5 == 0 {
			if heard := c.heard(t); heard != c.heardSent {
				c.heardSent = heard
				events = append(events, map[string]any{"type": "heard", "call_id": c.id, "reply": c.replyNo, "text": heard})
			}
		}
		c.mu.Unlock()
		for _, e := range events {
			_ = c.agent.send(e)
		}
		if data, err := c.enc.encode(f.pcm); err == nil {
			_ = c.track.WriteSample(media.Sample{Data: data, Duration: 20 * time.Millisecond})
		}
	}
}

func (c *Call) piece(id int) *piece {
	for _, p := range c.pieces {
		if p.id == id {
			return p
		}
	}
	return nil
}

// heard is everything of the current reply you've heard by t. Words are taken as spread
// evenly over a piece's audio, and the word being spoken is left out.
func (c *Call) heard(t time.Time) string {
	var words []string
	for _, p := range c.pieces {
		if p.start.IsZero() || !t.After(p.start) {
			continue
		}
		elapsed, secs := t.Sub(p.start).Seconds(), p.secs()
		if p.done && (elapsed >= secs || p.played) {
			words = append(words, p.text)
			continue
		}
		if secs == 0 {
			continue
		}
		r := []rune(p.text)
		part := string(r[:min(len(r), int(float64(len(r))*elapsed/secs))])
		if i := strings.LastIndex(part, " "); i >= 0 {
			words = append(words, part[:i])
		}
	}
	return strings.TrimSpace(strings.Join(words, " "))
}

// checkIdle tells the agent when the voice has finished and you aren't speaking, once.
func (c *Call) checkIdle() {
	if c.idleSent || c.playing || len(c.queue) > 0 || c.userSpeaking {
		return
	}
	if c.replying && !(c.replyEnded && strings.TrimSpace(c.text) == "") {
		return
	}
	for _, p := range c.pieces {
		if !p.done {
			return
		}
	}
	c.idleSent, c.finishedOnce = true, true
	_ = c.agent.send(map[string]any{"type": "idle", "call_id": c.id})
	c.quietSince++
	go c.quiet(c.quietSince)
}

// quiet tells the agent, once, that the voice has been idle for quietAfter and you haven't
// started speaking since.
func (c *Call) quiet(since int) {
	time.Sleep(quietAfter)
	c.mu.Lock()
	still := !c.ended && c.idleSent && !c.userSpeaking && c.quietSince == since
	c.mu.Unlock()
	if still {
		_ = c.agent.send(map[string]any{"type": "quiet", "call_id": c.id})
	}
}

// write: reply n, as it's written. The first sentence goes to be spoken at once; later
// pieces go when the one before is about to end, with every finished sentence since. What
// comes of a reply that was cut off is dropped.
func (c *Call) write(n int, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended || n <= c.cut {
		return
	}
	c.lastReply, c.awaiting = max(c.lastReply, n), false
	if !c.replying || n != c.replyNo {
		c.replying, c.replyNo, c.text, c.handed, c.replyEnded, c.due = true, n, "", 0, false, false
		c.pieces, c.heardSent = nil, ""
		c.generation++
	}
	c.text += text
	c.handOver()
}

// written: reply n is complete.
func (c *Call) written(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended || n <= c.cut {
		return
	}
	c.lastReply, c.awaiting = max(c.lastReply, n), false
	if !c.replying || n != c.replyNo {
		// Nothing of it was written: the voice is idle once what it says now has played.
		c.idleSent = false
		c.checkIdle()
		return
	}
	c.replyEnded = true
	c.handOver()
	c.checkIdle()
}

func (c *Call) handOver() {
	var n int
	switch {
	case c.replyEnded:
		n = len(c.text)
	case c.handed == 0:
		n = endOfSentence(c.text)
	case c.due:
		n = finishedSentences(c.text)
	default:
		return
	}
	text := strings.TrimSpace(c.text[:n])
	c.text = c.text[n:]
	if text == "" {
		return
	}
	c.handed++
	c.due = false
	c.nextPiece++
	p := &piece{id: c.nextPiece, text: text}
	c.pieces = append(c.pieces, p)
	_ = c.node.send(map[string]any{"type": "speak", "stream": c.stream, "piece": p.id, "text": text})
	go c.waitForLead(c.generation, len(c.pieces))
}

// waitForLead marks the next piece due once the last one handed over has all its audio and
// is lead from its end.
func (c *Call) waitForLead(generation, count int) {
	for {
		time.Sleep(50 * time.Millisecond)
		c.mu.Lock()
		if c.ended || c.generation != generation || len(c.pieces) != count {
			c.mu.Unlock()
			return
		}
		end, known := c.expectedEnd(now())
		if known && !now().Before(end.Add(-lead)) {
			c.due = true
			c.handOver()
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}

// expectedEnd is when the current reply's audio so far will have played, if all of it has come.
func (c *Call) expectedEnd(t time.Time) (time.Time, bool) {
	end := t
	for _, p := range c.pieces {
		if !p.done {
			return time.Time{}, false
		}
		start := p.start
		if start.IsZero() {
			start = maxTime(end, t)
		}
		end = start.Add(time.Duration(p.secs() * float64(time.Second)))
	}
	return end, true
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// spoken: audio for a piece came from the node, in 20 ms frames to play.
func (c *Call) audio(pieceID int, samples []int16) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.piece(pieceID)
	if p == nil || c.ended {
		return // a piece cut off by an interruption
	}
	p.samples += len(samples)
	p.partial = append(p.partial, samples...)
	for len(p.partial) >= speakingRate/50 {
		c.queue = append(c.queue, outFrame{p.id, p.partial[:speakingRate/50]})
		p.partial = p.partial[speakingRate/50:]
	}
}

func (c *Call) spoken(pieceID int, secs, took float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.piece(pieceID)
	if p == nil || c.ended {
		return
	}
	if len(p.partial) > 0 {
		frame := make([]int16, speakingRate/50)
		copy(frame, p.partial)
		c.queue = append(c.queue, outFrame{p.id, frame})
		p.partial = nil
	}
	p.done = true
	c.say(fmt.Sprintf("voice (%.1f s, made in %.2f s): %q", secs, took, p.text))
}

// speechStarted: the node hears you. If the voice was still speaking, or had more to say,
// it stops at once; what you'd heard goes to the agent, and the rest is never spoken. A
// reply to your last turn that hasn't come yet is cut off too, unheard.
func (c *Call) speechStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	c.userSpeaking = true
	c.quietSince++
	end, known := c.expectedEnd(now())
	unfinished := c.replying && (!c.replyEnded || strings.TrimSpace(c.text) != "" || len(c.queue) > 0 || !known || now().Before(end))
	heard := ""
	switch {
	case unfinished:
		heard = c.heard(now())
		c.cut = c.replyNo
		_ = c.node.send(map[string]any{"type": "speak_cancel", "stream": c.stream})
	case c.awaiting:
		c.cut = c.lastReply + 1
	default:
		c.replying = false
		return
	}
	c.replying, c.awaiting, c.queue, c.pieces, c.text, c.heardSent = false, false, nil, nil, "", ""
	c.cutThisTurn = true
	if c.phone {
		c.playing = false // it stops at speak_cancel, and says nothing more of those pieces
	}
	c.generation++
	_ = c.agent.send(map[string]any{"type": "interrupted", "call_id": c.id, "reply": c.cut, "heard": heard})
	c.say(fmt.Sprintf("interrupted reply %d; you'd heard %q", c.cut, lastRunes(heard, 60)))
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func (c *Call) speechStopped() {
	c.mu.Lock()
	c.userSpeaking = false
	c.mu.Unlock()
}

// turn: you've finished, and these are your words for the next reply.
func (c *Call) turn(text string, secs, took float64) {
	c.mu.Lock()
	c.userSpeaking, c.idleSent, c.awaiting = false, false, true
	// Interrupted: you cut the voice off, or a reply it was thinking of, or it hadn't
	// finished speaking once yet. The agent answers a stop with no words only then.
	interrupted := c.cutThisTurn || !c.finishedOnce
	c.cutThisTurn = false
	ended := c.ended
	c.mu.Unlock()
	if ended {
		return
	}
	c.say(fmt.Sprintf("you (%.1f s, heard in %.2f s): %q", secs, took, text))
	_ = c.agent.send(map[string]any{"type": "turn", "call_id": c.id, "text": text, "interrupted": interrupted})
}

// end: the call is over, or replaced by a new offer for it. Unless replaced, the agent hears
// how long you talked: from when its audio connected or you answered, whichever came last,
// until your audio last came; null if it never connected after you answered.
func (c *Call) end(why string) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.ended = true
	replaced := why == "replaced"
	var lasted any
	if !c.connectedAt.IsZero() && !c.answeredAt.IsZero() {
		start := maxTime(c.connectedAt, c.answeredAt)
		end := now()
		if c.lastHeard.After(start) {
			end = c.lastHeard
		}
		lasted = end.Sub(start).Seconds()
	}
	c.mu.Unlock()
	if c.phone {
		_ = c.node.send(map[string]any{"type": "end"})
	} else {
		_ = c.node.send(map[string]any{"type": "speak_cancel", "stream": c.stream})
		_ = c.node.send(map[string]any{"type": "hear_end", "stream": c.stream})
	}
	go c.close()
	forgetCall(c)
	if replaced {
		c.say("replaced by a new offer")
		return
	}
	c.say(why)
	if lasted == nil {
		c.say("finished, its audio never connected")
	} else {
		c.say(fmt.Sprintf("finished: you talked %.1f s", lasted))
	}
	_ = c.agent.send(map[string]any{"type": "ended", "call_id": c.id, "lasted": lasted})
}
