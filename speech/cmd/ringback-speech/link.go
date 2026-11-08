package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// The link to the hub (docs/protocol.md, "The speech node"): a WebSocket the node opens, with
// its token; JSON in text messages, audio in binary ones. The link process keeps it and relays
// it to its worker (relay.go).

const (
	protocolVersion = 1
	pingEvery       = 15 * time.Second
	silentFor       = 45 * time.Second // a link that answers no ping for this long is closed
	maxBackoff      = 30 * time.Second
)

// linkURL is the hub's speech link: its own ws:// or wss:// URL, or the hub's URL, which turns
// into it ("https://hub.example.com:8787" into wss://…/speech).
func linkURL(hub string) string {
	if strings.HasPrefix(hub, "http://") || strings.HasPrefix(hub, "https://") {
		return "ws" + strings.TrimPrefix(strings.TrimSuffix(hub, "/"), "http") + "/speech"
	}
	return hub
}

// hubClient is how the node reaches the hub: trusting the CA in the PEM file ca as well as the
// system's, for a hub whose certificate a CA of your own signed; nil, the default, without one.
func hubClient(ca string) (*http.Client, error) {
	if ca == "" {
		return nil, nil
	}
	data, err := os.ReadFile(ca)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("no certificate in %s", ca)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool}
	return &http.Client{Transport: transport}, nil
}

type message struct {
	Type        string   `json:"type"`
	Version     int      `json:"version"`
	Stream      uint32   `json:"stream"`
	Piece       uint32   `json:"piece"`
	Text        string   `json:"text"`
	Sensitivity *float64 `json:"sensitivity"`
	Gate        *float64 `json:"gate"`
	Error       string   `json:"error"`
}

type outgoing struct {
	kind websocket.MessageType
	data []byte
}

// serve keeps a link to the hub, reconnecting after 1, 2, 4… up to 30 s.
func serve(ctx context.Context, c config, r *relay) {
	backoff := time.Second
	for ctx.Err() == nil {
		welcomed, err := link(ctx, c, r)
		if ctx.Err() != nil {
			return
		}
		if welcomed {
			backoff = time.Second
		}
		logf("link to the hub: %v; again in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

// link runs one connection until it fails; welcomed says whether the hub took the hello.
func link(ctx context.Context, c config, r *relay) (welcomed bool, err error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dialCtx, c.hub, &websocket.DialOptions{
		HTTPClient: c.client,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.secret()}},
	})
	cancel()
	if err != nil {
		return false, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)

	hello, _ := json.Marshal(map[string]any{
		"type": "hello", "version": protocolVersion, "name": c.name, "hearing": true, "speaking": true,
		// A voice that speaks a reply in one take wants all of it at once, and hearing writes down
		// your whole turn, so it needs to know when the voice speaks (docs/protocol.md).
		"whole_replies": c.voice == "qwen3", "whole_turns": true,
	})
	if err := conn.Write(ctx, websocket.MessageText, hello); err != nil {
		return false, err
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, data, err := conn.Read(readCtx)
	cancel()
	if err != nil {
		return false, err
	}
	var welcome message
	if json.Unmarshal(data, &welcome) != nil || welcome.Type != "welcome" || welcome.Version != protocolVersion {
		return false, fmt.Errorf("the hub didn't welcome us: %s", data)
	}
	logf("linked to the hub at %s as %q", c.hub, c.name)

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	out := make(chan outgoing, 8192)
	var dropped atomic.Int64
	enqueue := func(m outgoing) {
		select {
		case out <- m:
		default:
			if dropped.Add(1) == 1 {
				logf("the link to the hub is behind; messages are dropped")
			}
		}
	}
	r.linked(&hubLink{
		send: func(kind websocket.MessageType, data []byte) { enqueue(outgoing{kind, data}) },
		drop: stop,
	})
	defer r.unlinked()

	go func() { // the writer
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-out:
				wctx, cancel := context.WithTimeout(ctx, silentFor)
				err := conn.Write(wctx, m.kind, m.data)
				cancel()
				if err != nil {
					stop()
					return
				}
			}
		}
	}()
	go func() { // the pings
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, cancel := context.WithTimeout(ctx, silentFor-pingEvery)
				err := conn.Ping(pctx)
				cancel()
				if err != nil {
					logf("the hub answered no ping: %v", err)
					stop()
					return
				}
			}
		}
	}()

	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return true, err
		}
		r.fromHub(kind, data) // never waits: what's for the worker queues (pipe.go)
	}
}
