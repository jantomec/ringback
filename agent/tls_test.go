package main

import (
	"context"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The hub's address turns into its link's: over TLS, its https:// URL into wss://, trusting the
// system's CAs and one of your own (ca in agent.toml).
func TestHubLinkTLS(t *testing.T) {
	for addr, url := range map[string]string{
		"10.0.0.1:8787":                  "ws://10.0.0.1:8787/agent",
		"http://10.0.0.1:8787":           "ws://10.0.0.1:8787/agent",
		"https://hub.example.com:8787/":  "wss://hub.example.com:8787/agent",
		"wss://hub.example.com:443/link": "wss://hub.example.com:443/link",
	} {
		check(t, addr+" links at "+url, newHubLink(addr, "", "", nil).url == url, newHubLink(addr, "", "", nil).url)
	}

	oldFirst := firstRetry
	firstRetry = 50 * time.Millisecond
	t.Cleanup(func() { firstRetry = oldFirst })
	hub := newFakeTLSHub(t)
	var lines []string
	var mu sync.Mutex
	logged := func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	}
	untrusted := newHubLink("https://"+hub.addr(), hub.token, "test-mac", logged)
	untrusted.handle = func(map[string]any) {}
	ctx, cancel := context.WithCancel(context.Background())
	go untrusted.run(ctx)
	eventually(t, "a certificate it doesn't trust: not linked, and logged", 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) > 0 && strings.Contains(lines[0], "certificate")
	})
	cancel()

	ca := filepath.Join(t.TempDir(), "hub-ca.pem")
	link := newHubLink("https://"+hub.addr(), hub.token, "test-mac", logged)
	check(t, "a CA file that isn't there is refused", link.trustCA(ca) != nil)
	os.WriteFile(ca, []byte("not a certificate"), 0o600)
	check(t, "nor one with no certificate in it", link.trustCA(ca) != nil)
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: hub.server.Certificate().Raw}), 0o600)
	check(t, "a CA of your own", link.trustCA(ca) == nil)
	up := make(chan struct{})
	link.handle, link.up, link.down = func(map[string]any) {}, func() { close(up) }, func() {}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go link.run(ctx)
	select {
	case <-up:
	case <-time.After(3 * time.Second):
		t.Fatal("trusting it, the link doesn't open over TLS")
	}
}
