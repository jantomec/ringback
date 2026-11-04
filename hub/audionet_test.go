package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
)

// candidatesFor answers an offer with network's settings, as a call does, and returns the
// answer's candidates.
func candidatesFor(t *testing.T, network audioNet) []string {
	t.Helper()
	settings := webrtc.SettingEngine{}
	if err := network.apply(&settings); err != nil {
		t.Fatal(err)
	}
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	engine := &webrtc.MediaEngine{}
	engine.RegisterDefaultCodecs()
	answerer, err := webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer answerer.Close()
	offerer, _ := webrtc.NewPeerConnection(webrtc.Configuration{})
	defer offerer.Close()
	offerer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio)
	offer, _ := offerer.CreateOffer(nil)
	offerer.SetLocalDescription(offer)
	if err := answerer.SetRemoteDescription(offer); err != nil {
		t.Fatal(err)
	}
	answer, _ := answerer.CreateAnswer(nil)
	gathered := webrtc.GatheringCompletePromise(answerer)
	answerer.SetLocalDescription(answer)
	<-gathered
	var candidates []string
	for _, line := range strings.Split(answerer.LocalDescription().SDP, "\r\n") {
		if strings.HasPrefix(line, "a=candidate:") {
			candidates = append(candidates, line)
		}
	}
	return candidates
}

// The call's audio offers the hub's own address, or its public one, on the ports it's given.
func TestAudioNetwork(t *testing.T) {
	for _, bad := range []map[string]string{
		{"public_ip": "hub.example.com"}, {"public_ip": "2001:db8::1"},
		{"udp_ports": "50000"}, {"udp_ports": "50100-50000"}, {"udp_ports": "0-10"}, {"udp_ports": "a-b"},
		{"listen": "0.0.0.0:8787"},
	} {
		if _, err := audioNetwork(bad); err == nil {
			t.Errorf("%v is taken", bad)
		}
	}

	if _, err := audioNetwork(map[string]string{"listen": "0.0.0.0:8787", "url": "https://hub.example.com:8787"}); err != nil {
		t.Errorf("every address, with url: %v", err)
	}
	plain, _ := audioNetwork(map[string]string{"listen": "127.0.0.1:8787"})
	got := candidatesFor(t, plain)
	if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), " 127.0.0.1 ") {
		t.Errorf("on a private network, its listen address: %v", got)
	}

	public, err := audioNetwork(map[string]string{"listen": "127.0.0.1:8787", "public_ip": "203.0.113.5", "udp_ports": "50300-50309"})
	if err != nil {
		t.Fatal(err)
	}
	got = candidatesFor(t, public)
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	for _, c := range got {
		fields := strings.Fields(c) // a=candidate:… 1 udp PRIORITY ADDRESS PORT typ host
		port, _ := strconv.Atoi(fields[5])
		if fields[4] != "203.0.113.5" || port < 50300 || port > 50309 || fields[7] != "host" {
			t.Errorf("on a public address, its public one, on its ports: %s", c)
		}
	}
}
