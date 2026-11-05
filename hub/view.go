package main

// A model's live view (docs/design.md, "Showing you things"): the phone offers a peer
// connection (/view) for a model a session showed, and the hub asks its agents for it (`view`);
// the one whose session showed it streams it from its renderer. The hub passes its frames,
// H.264 over the agent's link (binary "V", the view's ID, then the frame), on to the phone as a
// video track, and what its renderer says (`view_message`) on the phone's data channel; the
// phone's camera, frame and play, on that channel, go back to the agent (`view_input`), and
// so does a request for a key frame when the phone lost one, or has just connected. A view
// ends when the phone closes it, its connection fails, the agent ends it (`view_ended`) or
// goes away; the agent is told (`view_end`).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// viewH264 is what the hub sends: H.264 Constrained Baseline, which the phone decodes and the
// Mac's encoder makes, packetized in non-interleaved mode.
const viewH264 = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"

// viewIDLength is the length of a view's ID, as in a frame's binary message.
const viewIDLength = 36

type modelView struct {
	id, short, name string
	agent           *link
	pc              *webrtc.PeerConnection
	track           *webrtc.TrackLocalStaticSample

	mu        sync.Mutex
	channel   *webrtc.DataChannel
	open      bool
	waiting   []string // what the renderer said before the phone's channel opened
	lastFrame time.Time
	frames    int
	ended     bool
}

var (
	viewsMu sync.Mutex
	views   = map[string]*modelView{}
)

// viewModel answers the phone's offer for a model's live view: {"token", "key", "shown_id",
// "sdp", "width", "height"}, the size the phone wants it in. It answers {"sdp", "view_id"},
// or the agents' refusal: 404 if none has the model, 409 if its computer can't render it, 410
// if it's gone from there.
func viewModel(w http.ResponseWriter, r *http.Request) {
	body, valid := readBody(r)
	if !valid {
		phoneJSON(w, http.StatusBadRequest, map[string]any{"error": "needs shown_id and sdp"})
		return
	}
	stateMu.Lock()
	_, _, err := pairedPhone(body)
	stateMu.Unlock()
	if err != nil {
		phoneJSON(w, http.StatusUnauthorized, map[string]any{"error": "not paired"})
		return
	}
	shown := str(body, "shown_id")
	meta := keptMeta(shown)
	if meta == nil || str(meta, "kind") != "model" {
		phoneJSON(w, http.StatusNotFound, map[string]any{"error": "not kept"})
		return
	}
	network, err := audioNetwork(loadConfig())
	if err != nil {
		phoneJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	v, sdp, err := newModelView(newUUID(), str(meta, "name"), str(body, "sdp"), network)
	if err != nil {
		log("view of " + str(meta, "name") + ": " + err.Error())
		phoneJSON(w, http.StatusBadRequest, map[string]any{"error": "bad offer"})
		return
	}
	// Known before the agent is asked: what its renderer says can come before its answer.
	viewsMu.Lock()
	views[v.id] = v
	viewsMu.Unlock()
	agent, answer := ask("", "", map[string]any{
		"type": "view", "view_id": v.id, "shown_id": shown, "width": number(body["width"]), "height": number(body["height"]),
	})
	if agent == nil {
		v.mu.Lock()
		v.ended = true
		v.mu.Unlock()
		viewsMu.Lock()
		delete(views, v.id)
		viewsMu.Unlock()
		v.pc.Close()
		status := int(number(answer["status"]))
		if status == 0 || status == http.StatusForbidden {
			status = http.StatusNotFound
		}
		v.say("refused: " + str(answer, "error"))
		phoneJSON(w, status, map[string]any{"error": orDefault(str(answer, "error"), "no computer has it")})
		return
	}
	v.mu.Lock()
	v.agent = agent
	v.mu.Unlock()
	v.say("on " + agent.name)
	phoneJSON(w, http.StatusOK, map[string]any{"sdp": sdp, "view_id": v.id})
}

// keptMeta is what's kept of a shown item, or nil.
func keptMeta(id string) map[string]any {
	if _, _, err := shownPart(id, "thumb"); err != nil {
		return nil
	}
	shownMu.Lock()
	defer shownMu.Unlock()
	var meta map[string]any
	readJSON(filepath.Join(shownDir(), id, "meta.json"), &meta)
	return meta
}

// newModelView answers the phone's offer with a peer connection that sends the model's video
// and takes its data channel, on the hub's own address, as a call's does (audioNet).
func newModelView(id, name, offer string, network audioNet) (*modelView, string, error) {
	v := &modelView{id: id, short: prefix(id, 8), name: name}
	engine := &webrtc.MediaEngine{}
	feedback := []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}}
	if err := engine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: viewH264, RTCPFeedback: feedback},
		PayloadType:        102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, "", err
	}
	// Resending what the phone lost (NACK), and the reports it needs.
	interceptors := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(engine, interceptors); err != nil {
		return nil, "", err
	}
	settings := webrtc.SettingEngine{}
	if err := network.apply(&settings); err != nil {
		return nil, "", err
	}
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	settings.SetICETimeouts(15*time.Second, 30*time.Second, 2*time.Second)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithInterceptorRegistry(interceptors), webrtc.WithSettingEngine(settings))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	v.pc = pc
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: viewH264}, "model", "ringback")
	if err != nil {
		pc.Close()
		return nil, "", err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		pc.Close()
		return nil, "", err
	}
	v.track = track
	go v.readFeedback(sender)
	pc.OnDataChannel(func(channel *webrtc.DataChannel) {
		v.mu.Lock()
		v.channel = channel
		v.mu.Unlock()
		channel.OnOpen(v.channelOpened)
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			var input map[string]any
			if json.Unmarshal(message.Data, &input) == nil {
				v.toAgent(map[string]any{"type": "view_input", "view_id": v.id, "input": input})
			}
		})
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			v.say("connected")
			v.keyFrame()
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			v.end("the phone closed it", true)
		}
	})
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		pc.Close()
		return nil, "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return nil, "", err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return nil, "", err
	}
	<-gathered
	return v, pc.LocalDescription().SDP, nil
}

func (v *modelView) say(line string) { log("view " + v.short + " of " + v.name + ": " + line) }

// readFeedback asks the renderer for a key frame when the phone lost a picture (PLI, FIR).
func (v *modelView) readFeedback(sender *webrtc.RTPSender) {
	for {
		packets, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, packet := range packets {
			switch packet.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				v.keyFrame()
			}
		}
	}
}

func (v *modelView) keyFrame() {
	v.toAgent(map[string]any{"type": "view_input", "view_id": v.id, "input": map[string]any{"keyframe": true}})
}

func (v *modelView) toAgent(message map[string]any) {
	v.mu.Lock()
	agent, ended := v.agent, v.ended
	v.mu.Unlock()
	if agent != nil && !ended {
		_ = agent.send(message)
	}
}

// channelOpened sends the phone what the renderer said before its channel opened.
func (v *modelView) channelOpened() {
	v.mu.Lock()
	v.open = true
	waiting, channel := v.waiting, v.channel
	v.waiting = nil
	v.mu.Unlock()
	for _, text := range waiting {
		_ = channel.SendText(text)
	}
}

// toPhone sends the phone what the renderer said, or keeps it until its channel opens: its
// ready, and the latest of its state.
func (v *modelView) toPhone(message map[string]any) {
	data, err := json.Marshal(message)
	if err != nil {
		return
	}
	v.mu.Lock()
	if !v.open {
		if _, state := message["playing"]; state {
			kept := v.waiting[:0]
			for _, text := range v.waiting {
				if !containsKey(text, "playing") {
					kept = append(kept, text)
				}
			}
			v.waiting = kept
		}
		v.waiting = append(v.waiting, string(data))
		v.mu.Unlock()
		return
	}
	channel := v.channel
	v.mu.Unlock()
	_ = channel.SendText(string(data))
}

func containsKey(text, key string) bool {
	var m map[string]any
	if json.Unmarshal([]byte(text), &m) != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// frame sends the phone a frame of the model, timed by when it came.
func (v *modelView) frame(data []byte) {
	at := now()
	v.mu.Lock()
	duration := 33 * time.Millisecond
	if !v.lastFrame.IsZero() {
		duration = min(max(at.Sub(v.lastFrame), time.Millisecond), time.Second)
	}
	v.lastFrame = at
	v.frames++
	ended := v.ended
	v.mu.Unlock()
	if !ended {
		_ = v.track.WriteSample(media.Sample{Data: data, Duration: duration})
	}
}

// end ends the view: the phone is told why, and the agent is told to stop if it didn't end it.
func (v *modelView) end(why string, tellAgent bool) {
	v.mu.Lock()
	if v.ended {
		v.mu.Unlock()
		return
	}
	v.ended = true
	agent, frames, channel, open := v.agent, v.frames, v.channel, v.open
	v.mu.Unlock()
	viewsMu.Lock()
	delete(views, v.id)
	viewsMu.Unlock()
	if tellAgent && agent != nil {
		_ = agent.send(map[string]any{"type": "view_end", "view_id": v.id, "why": why})
	}
	if channel != nil && open {
		if data, err := json.Marshal(map[string]any{"status": "ended", "why": why}); err == nil {
			_ = channel.SendText(string(data))
		}
	}
	v.say(fmt.Sprintf("ended, %s (%d frames)", why, frames))
	go func() {
		// Time for the phone to hear why, then the connection closes.
		time.Sleep(time.Second)
		_ = v.pc.Close()
	}()
}

func viewByID(id string) *modelView {
	viewsMu.Lock()
	defer viewsMu.Unlock()
	return views[id]
}

// fromAgentView handles what an agent says of a view.
func fromAgentView(message map[string]any) {
	v := viewByID(str(message, "view_id"))
	if v == nil {
		return
	}
	switch message["type"] {
	case "view_message":
		if said, ok := message["message"].(map[string]any); ok {
			v.toPhone(said)
		}
	case "view_ended":
		v.end(orDefault(str(message, "why"), "ended"), false)
	}
}

// agentFrame is a frame of a model's live view from an agent's link.
func agentFrame(_ *link, data []byte) {
	if len(data) <= 1+viewIDLength || data[0] != 'V' {
		return
	}
	if v := viewByID(string(data[1 : 1+viewIDLength])); v != nil {
		v.frame(data[1+viewIDLength:])
	}
}

// endViewsOf ends the views an agent streams, when it goes away.
func endViewsOf(l *link) {
	viewsMu.Lock()
	var gone []*modelView
	for _, v := range views {
		v.mu.Lock()
		if v.agent == l {
			gone = append(gone, v)
		}
		v.mu.Unlock()
	}
	viewsMu.Unlock()
	for _, v := range gone {
		v.end("its computer went away", false)
	}
}
