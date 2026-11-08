package main

import (
	"fmt"
	"net"
	"strconv"
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

// waitLimit is how long the model's wait lasts, once it has chosen to wait for you to go on and
// you haven't: then the agent is told (`quiet`, `waited`), and the model answers what you said.
var waitLimit = envSeconds("RINGBACK_WAIT_LIMIT", 3)

// hangUpAfter is how long the hub waits, once the voice has ended the call and its audio has
// played, before it hangs up: the phone's own buffer still holds the last of it.
const hangUpAfter = time.Second

// frameSecs is how long a frame of the voice's audio lasts.
const frameSecs = 0.02

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

// paceSecs is how long a character of text takes to say, before a call has measured its own
// voice: 0.05-0.06 s for Qwen3's Hadley and Kokoro's af_heart, on technical replies.
const paceSecs = 0.055

// length is how long a piece will be once its audio is all here. A voice that streams a whole
// reply in one take sends it about 3× faster than it plays, so its length so far is less than
// what's been heard of it by the end; until it's done, it's at least its text at the call's pace.
func (c *Call) length(p *piece) float64 {
	secs := p.secs()
	if p.done {
		return secs
	}
	pace, chars, total := paceSecs, 0, 0.0
	for _, q := range c.pieces {
		if q.done && q.samples > 0 {
			chars += len([]rune(q.text))
			total += q.secs()
		}
	}
	if chars > 0 {
		pace = total / float64(chars)
	}
	return max(secs, pace*float64(len([]rune(p.text))))
}

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
	channel         *webrtc.DataChannel // the phone's "call" channel, which says when the voice hangs up
	hangingUp       bool                // the voice ended the call: the phone is told so as it ends
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
	paused          bool // you spoke over the voice: it's stopped, and the rest of its reply is kept
	pausedAt        time.Time
	played          []outFrame // of the current reply, to go back to the start of a sentence
	finishedOnce    bool       // the voice has finished speaking once (the opening)
	text            string
	handed          int
	replyEnded, due bool
	breakNow        bool // the model stopped writing to call a tool (breakReply)
	generation      int  // a reply or interruption bumps it, so stale work stops
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

// audioNet is where a call's audio goes, from hub.toml: the hub's listen address; the public
// address it offers instead (public_ip), for a hub on a public address whose machine doesn't
// have it on an interface, as clouds that put their servers behind NAT do; and the UDP ports
// it may use (udp_ports, as "50000-50199"), for a firewall to open.
type audioNet struct {
	listen, public   string
	minPort, maxPort uint16
}

func audioNetwork(config map[string]string) (audioNet, error) {
	n := audioNet{listen: config["listen"], public: config["public_ip"]}
	host, _, _ := net.SplitHostPort(n.listen)
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() && config["url"] == "" {
		return n, fmt.Errorf("listening on every address (%s) needs url, how the phone reaches the hub", n.listen)
	}
	if ip := net.ParseIP(n.public); n.public != "" && (ip == nil || ip.To4() == nil) {
		return n, fmt.Errorf("public_ip %q isn't an IPv4 address", n.public)
	}
	if ports := config["udp_ports"]; ports != "" {
		low, high, ranged := strings.Cut(ports, "-")
		min, errMin := strconv.ParseUint(strings.TrimSpace(low), 10, 16)
		max, errMax := strconv.ParseUint(strings.TrimSpace(high), 10, 16)
		if !ranged || errMin != nil || errMax != nil || min == 0 || min > max {
			return n, fmt.Errorf("udp_ports %q isn't a range of ports such as 50000-50199", ports)
		}
		n.minPort, n.maxPort = uint16(min), uint16(max)
	}
	return n, nil
}

// apply has a call offer only the hub's own address, or its public one, on its ports. A hub
// that listens on every address (0.0.0.0) offers every one it has.
func (n audioNet) apply(settings *webrtc.SettingEngine) error {
	host, _, _ := net.SplitHostPort(n.listen)
	if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
		settings.SetIPFilter(func(candidate net.IP) bool { return candidate.Equal(ip) })
		// Pion leaves loopback out, which a hub on localhost (tools/hub-check) needs.
		settings.SetIncludeLoopbackCandidate(ip.IsLoopback())
	}
	if n.public != "" {
		if err := settings.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{External: []string{n.public},
			AsCandidateType: webrtc.ICECandidateTypeHost, Mode: webrtc.ICEAddressRewriteReplace}); err != nil {
			return err
		}
	}
	if n.maxPort != 0 {
		return settings.SetEphemeralUDPPortRange(n.minPort, n.maxPort)
	}
	return nil
}

// newCall answers the phone's offer: a peer connection offering only the hub's own address
// (audioNet), with Opus asked of the phone as phoneOpus says. It returns the answer's SDP once
// its addresses are gathered.
func newCall(id string, offer string, ringing bool, sensitivity, gate float64, agent, node *link, network audioNet) (*Call, string, error) {
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
	if err := network.apply(&settings); err != nil {
		return nil, "", err
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
	pc.OnDataChannel(func(channel *webrtc.DataChannel) {
		if channel.Label() == "call" {
			c.mu.Lock()
			c.channel = channel
			c.mu.Unlock()
		}
	})
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
	c.mu.Lock()
	channel, told := c.channel, c.hangingUp
	c.mu.Unlock()
	if told && channel != nil && channel.ReadyState() == webrtc.DataChannelStateOpen {
		// The phone ends the call itself, as the other side hanging up, rather than finding its
		// audio lost; the message gets a moment to leave before the connection closes.
		_ = channel.SendText(`{"type":"end"}`)
		for i := 0; i < 50 && channel.BufferedAmount() > 0; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
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
	lineAnswered(c.id)
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
		if c.paused {
			c.mu.Unlock()
			continue
		}
		live := !c.answeredAt.IsZero() && !c.connectedAt.IsZero()
		if !live || len(c.queue) == 0 {
			var final map[string]any
			if c.playing && len(c.queue) == 0 {
				c.playing = false
				// All of it has played, its last frame within the tick: you've heard it to the end.
				if heard := c.heardSoFar(now().Add(20 * time.Millisecond)); heard != c.heardSent {
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
		c.played = append(c.played, f)
		t := now()
		if p := c.piece(f.piece); p != nil && p.start.IsZero() {
			p.start = t
		}
		var events []map[string]any
		toNode := false
		if !c.playing {
			c.playing, c.idleSent = true, false
			events = append(events, map[string]any{"type": "speaking", "call_id": c.id})
			toNode = c.node != nil && c.node.wholeTurns
			if !c.voiceStarted {
				c.voiceStarted = true
				c.say(fmt.Sprintf("voice started %.2f s after you answered", t.Sub(c.answeredAt).Seconds()))
			}
		}
		frames++
		if frames%5 == 0 {
			if heard := c.heardSoFar(t); heard != c.heardSent {
				c.heardSent = heard
				events = append(events, map[string]any{"type": "heard", "call_id": c.id, "reply": c.replyNo, "text": heard})
			}
		}
		c.mu.Unlock()
		if toNode {
			// Your turn so far is answered: what you say next starts a new one.
			_ = c.node.send(map[string]any{"type": "speaking", "stream": c.stream})
		}
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
		elapsed, secs := t.Sub(p.start).Seconds(), c.length(p)
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

// heardSoFar is what you've heard of the reply at t, never less than the agent was told: a
// piece's length grows as its audio comes, so the share of it played can shrink, as when
// the audio comes slower than it plays.
func (c *Call) heardSoFar(t time.Time) string {
	if heard := c.heard(t); len(heard) >= len(c.heardSent) || !strings.HasPrefix(c.heardSent, heard) {
		return heard
	}
	return c.heardSent
}

// checkIdle tells the agent when the voice has finished and you aren't speaking, once.
func (c *Call) checkIdle() {
	if c.idleSent || c.playing || len(c.queue) > 0 || c.userSpeaking || c.paused {
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

// waited tells the agent, once, that you haven't gone on within waitLimit of the model choosing
// to wait.
func (c *Call) waited(since int) {
	time.Sleep(waitLimit)
	c.mu.Lock()
	still := !c.ended && !c.userSpeaking && c.quietSince == since
	c.mu.Unlock()
	if still {
		_ = c.agent.send(map[string]any{"type": "quiet", "call_id": c.id, "waited": true})
	}
}

// write: reply n, as it's written. The first sentence goes to be spoken at once; later
// pieces go when the one before is about to end, with every finished sentence since. A node
// whose voice speaks a reply in one take gets it all once it's written. What comes of a
// reply that was cut off is dropped.
func (c *Call) write(n int, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended || n <= c.cut {
		return
	}
	c.lastReply, c.awaiting = max(c.lastReply, n), false
	if !c.replying || n != c.replyNo {
		if c.paused {
			// A new reply takes the place of the one you stopped, and what's still to come of
			// that one is dropped.
			c.paused, c.cut = false, max(c.cut, c.replyNo)
			c.queue = nil
			_ = c.node.send(map[string]any{"type": "speak_cancel", "stream": c.stream})
			c.say(fmt.Sprintf("reply %d answers you, so the rest of reply %d is dropped", n, c.replyNo))
		}
		c.replying, c.replyNo, c.text, c.handed, c.replyEnded, c.due = true, n, "", 0, false, false
		c.pieces, c.heardSent, c.played = nil, "", nil
		c.generation++
	}
	c.text += text
	c.handOver()
}

// breakReply: the model stopped writing to call a tool, so what it's written of reply n goes to
// a node that speaks whole replies now, as one take; what it writes after is another.
func (c *Call) breakReply(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended || n <= c.cut || !c.replying || n != c.replyNo {
		return
	}
	c.breakNow = true
	c.handOver()
	c.breakNow = false
}

// written: reply n is complete. waiting: it said nothing, as the model waits for you to go on.
func (c *Call) written(n int, waiting bool) {
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
		if waiting {
			c.quietSince++
			go c.waited(c.quietSince)
		}
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
	case c.node != nil && c.node.wholeReplies && c.breakNow:
		n = len(c.text)
	case c.node != nil && c.node.wholeReplies:
		return
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

// speechStarted: the node hears you. If the voice was still speaking, or had more to say, it
// stops at once. Once you've heard some of the reply, the rest is kept: what you'd heard goes
// to the agent, and the model decides from what you say whether the voice carries on (resume)
// or answers you. A phone that speaks itself can only stop, and a reply you've heard none of
// is dropped, as is a reply to your last turn that hasn't come yet.
func (c *Call) speechStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	c.userSpeaking = true
	c.quietSince++
	if c.paused {
		if c.awaiting {
			// You've gone on while the model decides: its answer to what you said so far is dropped.
			c.cut, c.awaiting = c.lastReply+1, false
			_ = c.agent.send(map[string]any{"type": "interrupted", "call_id": c.id, "reply": c.cut, "heard": ""})
		}
		return
	}
	end, known := c.expectedEnd(now())
	unfinished := c.replying && (!c.replyEnded || strings.TrimSpace(c.text) != "" || len(c.queue) > 0 || !known || now().Before(end))
	heard := ""
	switch {
	case unfinished:
		heard = c.heardSoFar(now())
		if heard != "" && !c.phone {
			c.paused, c.pausedAt, c.playing, c.cutThisTurn, c.heardSent = true, now(), false, true, heard
			_ = c.agent.send(map[string]any{"type": "interrupted", "call_id": c.id, "reply": c.replyNo, "heard": heard, "paused": true})
			c.say(fmt.Sprintf("paused reply %d; you'd heard %q", c.replyNo, lastRunes(heard, 60)))
			return
		}
		c.cut = c.replyNo
		_ = c.node.send(map[string]any{"type": "speak_cancel", "stream": c.stream})
	case c.awaiting:
		c.cut = c.lastReply + 1
	default:
		c.replying = false
		return
	}
	c.replying, c.awaiting, c.queue, c.pieces, c.text, c.heardSent, c.played = false, false, nil, nil, "", "", nil
	c.cutThisTurn = true
	if c.phone {
		c.playing = false // it stops at speak_cancel, and says nothing more of those pieces
	}
	c.generation++
	_ = c.agent.send(map[string]any{"type": "interrupted", "call_id": c.id, "reply": c.cut, "heard": heard})
	c.say(fmt.Sprintf("interrupted reply %d; you'd heard %q", c.cut, lastRunes(heard, 60)))
}

// resume: the model carries on with reply n, which you stopped. The voice goes back to the
// start of the sentence it was in.
func (c *Call) resume(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended || !c.paused || n != c.replyNo {
		return
	}
	c.paused = false
	back := c.rewind()
	c.say(fmt.Sprintf("carries on with reply %d, %.1f s back", n, back))
	c.checkIdle()
}

// rewind puts what was played of the sentence the voice stopped in back on the queue, and says
// how many seconds that is. Words are taken as spread evenly over a piece's audio, as in heard.
func (c *Call) rewind() float64 {
	var p *piece
	for _, x := range c.pieces {
		if !x.start.IsZero() {
			p = x
		}
	}
	if p == nil || p.secs() == 0 {
		return 0
	}
	elapsed, secs := c.pausedAt.Sub(p.start).Seconds(), c.length(p)
	var frames int
	for _, f := range c.played {
		if f.piece == p.id {
			frames++
		}
	}
	keep := 0.0
	if at := int(float64(len(p.text)) * elapsed / secs); at < len(p.text) {
		keep = secs * float64(sentenceStart(p.text[:max(at, 0)])) / float64(len(p.text))
	} else {
		keep = secs // it had played to its end: the next piece starts a sentence
	}
	again := max(frames-int(keep/frameSecs), 0)
	if again > 0 {
		from := len(c.played) - again
		c.queue = append(append([]outFrame{}, c.played[from:]...), c.queue...)
		c.played = c.played[:from]
	}
	p.start = now().Add(-time.Duration(float64(frames-again) * frameSecs * float64(time.Second)))
	return float64(again) * frameSecs
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

// turn: you've paused, and these are your words for the next reply. again: they're the whole
// turn, written down again, so they take the place of the words the last turn brought.
func (c *Call) turn(text string, secs, took float64, again bool) {
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
	whole := ""
	if again {
		whole = " (the whole turn again)"
	}
	c.say(fmt.Sprintf("you (%.1f s, heard in %.2f s): %q%s", secs, took, text, whole))
	message := map[string]any{"type": "turn", "call_id": c.id, "text": text, "interrupted": interrupted}
	if again {
		message["again"] = true
	}
	_ = c.agent.send(message)
}

// hangUp: the voice has ended the call (the agent's hang_up), once you'd heard it to the end.
// The rest of its audio plays out, at most 10 s more, then hangUpAfter, and the call ends.
func (c *Call) hangUp() {
	c.mu.Lock()
	if c.hangingUp || c.ended {
		c.mu.Unlock()
		return
	}
	c.hangingUp = true
	c.mu.Unlock()
	c.say("the voice hangs up")
	go func() {
		for deadline := now().Add(10 * time.Second); now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			c.mu.Lock()
			played := c.ended || (!c.playing && len(c.queue) == 0)
			c.mu.Unlock()
			if played {
				break
			}
		}
		time.Sleep(hangUpAfter)
		c.end("hung up by the voice")
	}()
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
	answered := !c.answeredAt.IsZero()
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
	if answered {
		// A call that rang and wasn't answered is settled by what the phone says of it.
		go func() {
			stateMu.Lock()
			defer stateMu.Unlock()
			lineEnded(c.id)
		}()
	}
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
