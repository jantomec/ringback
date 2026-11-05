package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A stand-in for a renderer's stream (render/README.md): ready, one frame, then what it reads
// on stdin back as state, until stdin closes; a model called broken fails at once.
const fakeServe = `#!/usr/bin/env python3
import json, struct, sys
def send(kind, data):
    sys.stdout.buffer.write(struct.pack(">I", len(data) + 1) + kind + data); sys.stdout.flush()
if "broken" in sys.argv[2]:
    sys.stderr.write("can't open the model\n"); sys.exit(1)
send(b"R", json.dumps({"ready": True, "frames": 48, "size": sys.argv[3:5]}).encode())
send(b"V", b"\x00\x00\x00\x01frame")
for line in sys.stdin:
    send(b"S", json.dumps({"got": json.loads(line)}).encode())
`

const viewID = "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"

// The hub asks for a model's live view: the agent starts its renderer's stream on the model a
// session here showed, relays its frames and what it says, passes the phone's input to it, and
// ends it when the hub says; what it can't view, it refuses, saying why.
func TestView(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	dir := t.TempDir()
	fake := filepath.Join(dir, "ringback-render")
	os.WriteFile(fake, []byte(fakeServe), 0o755)
	model, broken := filepath.Join(dir, "bracket.usdc"), filepath.Join(dir, "broken.usdc")
	os.WriteFile(model, []byte("PXR-USDC"), 0o644)
	os.WriteFile(broken, []byte("nope"), 0o644)
	saved := renderer
	renderer = fake
	defer func() { renderer = saved }()
	shownModels.remember("shown-model", model)
	shownModels.remember("shown-broken", broken)
	shownModels.remember("shown-gone", filepath.Join(dir, "gone.usdc"))

	for what, c := range map[string]struct {
		shown  string
		status float64
		why    string
	}{
		"not shown here": {"shown-elsewhere", 404, "not shown from here"},
		"gone":           {"shown-gone", 410, "the model is no longer at"},
	} {
		reply := hub.request(map[string]any{"type": "view", "view_id": viewID, "shown_id": c.shown, "width": 640.0, "height": 480.0})
		if reply["ok"] != false || reply["status"] != c.status || !strings.Contains(reply["error"].(string), c.why) {
			t.Errorf("%s: %v", what, reply)
		}
	}

	reply := hub.request(map[string]any{"type": "view", "view_id": viewID, "shown_id": "shown-model", "width": 641.0, "height": 99999.0})
	if reply["ok"] != true {
		t.Fatalf("a model shown here: %v", reply)
	}
	ready, at := hub.wait(0, 5*time.Second, func(m map[string]any) bool { return m["type"] == "view_message" && m["view_id"] == viewID })
	if message, _ := ready["message"].(map[string]any); message["ready"] != true || strings.Join(strs(message["size"]), "×") != "640×2048" {
		t.Fatalf("ready, at an even size of at most 2048: %v", ready)
	}
	frame := waitBinary(t, hub, 5*time.Second)
	if !bytes.Equal(frame, append([]byte("V"+viewID), "\x00\x00\x00\x01frame"...)) {
		t.Errorf("the frame, relayed: %q", frame)
	}
	hub.send(map[string]any{"type": "view_input", "view_id": viewID, "input": map[string]any{"camera": map[string]any{"yaw": 1.5}}})
	hub.send(map[string]any{"type": "view_input", "view_id": "another-view", "input": map[string]any{"play": true}})
	got, _ := hub.wait(at+1, 5*time.Second, func(m map[string]any) bool { return m["type"] == "view_message" })
	if message, _ := got["message"].(map[string]any); !strings.Contains(strings.ReplaceAll(jsonOf(message), " ", ""), `"camera":{"yaw":1.5}`) {
		t.Errorf("the phone's input reaches the renderer: %v", got)
	}
	hub.send(map[string]any{"type": "view_end", "view_id": viewID})
	if ended, _ := hub.wait(0, 5*time.Second, func(m map[string]any) bool { return m["type"] == "view_ended" }); ended["why"] != "ended" {
		t.Errorf("ended: %v", ended)
	}
	if hub.count(func(m map[string]any) bool { return m["type"] == "view_message" && strings.Contains(jsonOf(m), "play") }) != 0 {
		t.Errorf("another view's input reached this one")
	}

	// A renderer that fails says why.
	reply = hub.request(map[string]any{"type": "view", "view_id": viewID, "shown_id": "shown-broken", "width": 640.0, "height": 480.0})
	if reply["ok"] != true {
		t.Fatalf("a broken model's view starts: %v", reply)
	}
	if ended, _ := hub.wait(0, 5*time.Second, func(m map[string]any) bool {
		return m["type"] == "view_ended" && m["why"] != "ended"
	}); ended["why"] != "can't open the model" {
		t.Errorf("a renderer that fails: %v", ended)
	}

	renderer = ""
	reply = hub.request(map[string]any{"type": "view", "view_id": viewID, "shown_id": "shown-model", "width": 640.0, "height": 480.0})
	if reply["status"] != 409.0 || !strings.Contains(reply["error"].(string), "can't render 3D models") {
		t.Errorf("no renderer: %v", reply)
	}
	a.waitLog("view 0f1e2d3c: "+model, time.Second)
}

func waitBinary(t *testing.T, hub *fakeHub, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		if len(hub.binary) > 0 {
			frame := hub.binary[0]
			hub.mu.Unlock()
			return frame
		}
		hub.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no frame came")
	return nil
}

func strs(value any) []string {
	var out []string
	list, _ := value.([]any)
	for _, item := range list {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

func jsonOf(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
