package main

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The hub's address turns into its speech link: over TLS, its https:// URL into wss://.
func TestLinkURL(t *testing.T) {
	for hub, want := range map[string]string{
		"ws://10.0.0.1:8787/speech":     "ws://10.0.0.1:8787/speech",
		"http://10.0.0.1:8787":          "ws://10.0.0.1:8787/speech",
		"https://hub.example.com:8787/": "wss://hub.example.com:8787/speech",
		"wss://hub.example.com/speech":  "wss://hub.example.com/speech",
	} {
		if got := linkURL(hub); got != want {
			t.Errorf("%s: got %s, want %s", hub, got, want)
		}
	}
}

// Over TLS, the link trusts the system's CAs, and one of your own (ca in speech.toml).
func TestHubClient(t *testing.T) {
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if conn, err := websocket.Accept(w, r, nil); err == nil {
			conn.Close(websocket.StatusNormalClosure, "")
		}
	}))
	defer hub.Close()
	url := linkURL(hub.URL)
	dial := func(client *http.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: client})
		if err == nil {
			conn.CloseNow()
		}
		return err
	}

	if client, err := hubClient(""); client != nil || err != nil {
		t.Error("no CA: the default", client, err)
	}
	if err := dial(nil); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("a certificate it doesn't trust: %v", err)
	}
	ca := filepath.Join(t.TempDir(), "hub-ca.pem")
	if _, err := hubClient(ca); err == nil {
		t.Error("a CA file that isn't there is refused")
	}
	os.WriteFile(ca, []byte("not a certificate"), 0o600)
	if _, err := hubClient(ca); err == nil {
		t.Error("so is one with no certificate in it")
	}
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: hub.Certificate().Raw}), 0o600)
	client, err := hubClient(ca)
	if err != nil {
		t.Fatal(err)
	}
	if err := dial(client); err != nil {
		t.Errorf("trusting a CA of your own, the link opens over TLS: %v", err)
	}
}
