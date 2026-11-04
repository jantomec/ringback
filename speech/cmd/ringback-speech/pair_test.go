package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pairing writes the hub's address and the node's key into the settings install.sh made from
// the example, and the node then links with the key; a code the hub refuses says why.
func TestPairNode(t *testing.T) {
	var got map[string]any
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		if r.URL.Path != "/pair-link" || got["code"] != "good" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"ok": true, "key": "the-key"}`))
	}))
	defer hub.Close()
	key, err := pairWithHub(hub.URL, "good", "gpu-tower", "")
	if key != "the-key" || err != nil || got["kind"] != "speech" || got["name"] != "gpu-tower" {
		t.Fatalf("paired: %q %v, the hub got %v", key, err, got)
	}
	if _, err := pairWithHub(hub.URL, "used", "gpu-tower", ""); err == nil || !strings.Contains(err.Error(), "the hub refused the code") {
		t.Errorf("a refused code: %v", err)
	}

	example, err := os.ReadFile("../../speech.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "speech.toml")
	os.WriteFile(path, example, 0o600)
	if err := setConfig(path, [][2]string{{"hub", "https://hub.example.com:8787"}, {"key", key}}); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(path)
	if err != nil || c.hub != "wss://hub.example.com:8787/speech" || c.secret() != "the-key" || !strings.HasSuffix(c.parakeet, "parakeet-tdt-0.6b-v2-fp16") {
		t.Errorf("the settings after pairing: %+v %v", c, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "hub = \"https://hub.example.com:8787\"  # the hub's address") {
		t.Errorf("the hub's line loses its comment:\n%s", data)
	}
	if c.token = "explicit"; c.secret() != "the-key" {
		t.Error("the key goes before the token")
	}
}
