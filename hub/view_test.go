package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

// standInAgent links to the hub as an agent, and answers its views as refuse says: with a
// status and why, or else ok, then the renderer's ready and frames until the view ends. It keeps
// what the hub sends it.
type standInAgent struct {
	t      *testing.T
	ws     *websocket.Conn
	refuse map[string]any

	mu  sync.Mutex
	got []map[string]any
}

func linkStandInAgent(t *testing.T, server *httptest.Server) *standInAgent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/agent",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer agent-secret-5678"}}})
	if err != nil {
		t.Fatal(err)
	}
	hello, _ := json.Marshal(map[string]any{"type": "hello", "version": 1, "name": "render-mac"})
	ws.Write(ctx, websocket.MessageText, hello)
	if _, data, err := ws.Read(ctx); err != nil || !strings.Contains(string(data), "welcome") {
		t.Fatalf("the agent's welcome: %s, %v", data, err)
	}
	a := &standInAgent{t: t, ws: ws}
	go a.listen()
	t.Cleanup(func() { ws.CloseNow() })
	return a
}

func (a *standInAgent) write(m map[string]any) {
	data, _ := json.Marshal(m)
	_ = a.ws.Write(context.Background(), websocket.MessageText, data)
}

func (a *standInAgent) listen() {
	for {
		_, data, err := a.ws.Read(context.Background())
		if err != nil {
			return
		}
		var m map[string]any
		json.Unmarshal(data, &m)
		a.mu.Lock()
		a.got = append(a.got, m)
		refuse := a.refuse
		a.mu.Unlock()
		if m["type"] != "view" {
			continue
		}
		if refuse != nil {
			a.write(map[string]any{"type": "reply", "id": m["id"], "ok": false, "status": refuse["status"], "error": refuse["error"]})
			continue
		}
		a.write(map[string]any{"type": "reply", "id": m["id"], "ok": true})
		viewID := str(m, "view_id")
		a.write(map[string]any{"type": "view_message", "view_id": viewID, "message": map[string]any{"ready": true, "frames": 48}})
		go func() {
			// An IDR frame's NAL unit, as Annex B, a few times a second until the view ends.
			frame := append([]byte("V"+viewID), 0, 0, 0, 1, 0x65, 0x88, 0x84, 0x00, 0x33)
			for i := 0; i < 100 && !a.saw("view_end"); i++ {
				_ = a.ws.Write(context.Background(), websocket.MessageBinary, frame)
				time.Sleep(50 * time.Millisecond)
			}
		}()
	}
}

func (a *standInAgent) saw(kind string) bool { return a.find(kind, nil) != nil }

func (a *standInAgent) find(kind string, match func(map[string]any) bool) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range a.got {
		if m["type"] == kind && (match == nil || match(m)) {
			return m
		}
	}
	return nil
}

func (a *standInAgent) waitFor(kind string, match func(map[string]any) bool, timeout time.Duration) map[string]any {
	a.t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if m := a.find(kind, match); m != nil {
			return m
		}
	}
	a.t.Fatalf("the agent got no %s", kind)
	return nil
}

// standInPhone offers a model's view as the app does: video received only, and a data channel
// for its input. It keeps the video's packets and what comes on the channel.
type standInPhone struct {
	pc      *webrtc.PeerConnection
	channel *webrtc.DataChannel
	mu      sync.Mutex
	packets int
	payload []byte
	said    []map[string]any
}

func newStandInPhone(t *testing.T) (*standInPhone, string) {
	t.Helper()
	engine := &webrtc.MediaEngine{}
	engine.RegisterDefaultCodecs()
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	p := &standInPhone{pc: pc}
	pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	p.channel, _ = pc.CreateDataChannel("control", nil)
	p.channel.OnMessage(func(message webrtc.DataChannelMessage) {
		var m map[string]any
		json.Unmarshal(message.Data, &m)
		p.mu.Lock()
		p.said = append(p.said, m)
		p.mu.Unlock()
	})
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.packets++
			p.payload = packet.Payload
			p.mu.Unlock()
		}
	})
	offer, _ := pc.CreateOffer(nil)
	gathered := webrtc.GatheringCompletePromise(pc)
	pc.SetLocalDescription(offer)
	<-gathered
	return p, pc.LocalDescription().SDP
}

func postView(server *httptest.Server, body map[string]any) (int, map[string]any) {
	data, _ := json.Marshal(body)
	response, err := http.Post(server.URL+"/view", "application/json", bytes.NewReader(data))
	if err != nil {
		return 0, nil
	}
	defer response.Body.Close()
	var answer map[string]any
	json.NewDecoder(response.Body).Decode(&answer)
	return response.StatusCode, answer
}

// A model's live view: the phone's offer reaches the agent that has the model, its frames and
// its renderer's ready reach the phone, the phone's input and requests for key frames reach the
// agent, and closing the phone's side ends it there; what no agent can view is refused, saying why.
func TestModelView(t *testing.T) {
	os.Remove(devicesPath)
	os.RemoveAll(shownDir())
	defer os.Remove(devicesPath)
	defer os.Remove(pairingPath)
	defer os.RemoveAll(shownDir())
	key, err := pairWith(t, makeCode(t), "view-token")
	if err != nil {
		t.Fatal(err)
	}
	thumb := [3]string{"thumb", "image/jpeg", "a thumbnail"}
	_, answer := upload{shownMeta("model", "bracket.usdc"), [][3]string{{"poster", "image/png", "poster"}, thumb}}.post(t, "agent-secret-5678")
	model := str(answer, "id")
	_, answer = upload{shownMeta("image", "a.png"), [][3]string{{"file", "image/png", "png"}, thumb}}.post(t, "agent-secret-5678")
	image := str(answer, "id")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent":
			acceptLink(w, r, "agent", agentOpened, fromAgent, agentFrame, agentClosed)
		case "/view":
			viewModel(w, r)
		}
	}))
	defer server.Close()
	agent := linkStandInAgent(t, server)
	phone, offer := newStandInPhone(t)
	body := map[string]any{"token": "view-token", "key": key, "shown_id": model, "sdp": offer, "width": 720, "height": 1280}

	if status, _ := postView(server, map[string]any{"token": "view-token", "key": strings.Repeat("0", 64), "shown_id": model, "sdp": offer}); status != 401 {
		t.Errorf("without the phone's key: %d", status)
	}
	if status, _ := postView(server, map[string]any{"token": "view-token", "key": key, "shown_id": image, "sdp": offer}); status != 404 {
		t.Errorf("an image: %d", status)
	}

	status, answer := postView(server, body)
	if status != 200 || str(answer, "view_id") == "" {
		t.Fatalf("a model's view: %d %v", status, answer)
	}
	asked := agent.waitFor("view", nil, time.Second)
	if asked["shown_id"] != model || asked["width"] != 720.0 || asked["height"] != 1280.0 {
		t.Errorf("what the agent was asked: %v", asked)
	}
	phone.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: str(answer, "sdp")})

	// Connected, the agent is asked for a key frame, and the phone gets frames and the ready.
	agent.waitFor("view_input", func(m map[string]any) bool { return strings.Contains(jsonOf(m), `"keyframe":true`) }, 10*time.Second)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		phone.mu.Lock()
		done := phone.packets >= 3 && len(phone.said) > 0
		phone.mu.Unlock()
		if done {
			break
		}
	}
	phone.mu.Lock()
	if phone.packets < 3 || !bytes.Contains(phone.payload, []byte{0x88, 0x84, 0x00, 0x33}) {
		t.Errorf("the phone's video: %d packets, the last %x", phone.packets, phone.payload)
	}
	if len(phone.said) == 0 || phone.said[0]["ready"] != true {
		t.Errorf("the phone's channel: %v", phone.said)
	}
	phone.mu.Unlock()

	phone.channel.SendText(`{"camera": {"yaw": 0.5}}`)
	agent.waitFor("view_input", func(m map[string]any) bool { return strings.Contains(jsonOf(m), `"yaw":0.5`) }, 5*time.Second)

	phone.pc.Close()
	if ended := agent.waitFor("view_end", nil, 40*time.Second); ended["view_id"] != answer["view_id"] {
		t.Errorf("the view's end: %v", ended)
	}

	// An agent that can't render it: the phone hears why.
	agent.mu.Lock()
	agent.refuse = map[string]any{"status": 409, "error": "this Mac can't render 3D models: it has neither…"}
	agent.mu.Unlock()
	another, offer2 := newStandInPhone(t)
	defer another.pc.Close()
	body["sdp"] = offer2
	if status, answer := postView(server, body); status != 409 || !strings.Contains(str(answer, "error"), "can't render 3D models") {
		t.Errorf("refused: %d %v", status, answer)
	}
}

func jsonOf(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
