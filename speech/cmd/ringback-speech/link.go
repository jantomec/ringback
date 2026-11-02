package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"ringback/speech/internal/hearing"
)

// The link to the hub (docs/protocol.md, "The speech node"): a WebSocket the node opens, with
// its token; JSON in text messages, audio in binary ones.

const (
	protocolVersion = 1
	pingEvery       = 15 * time.Second
	silentFor       = 45 * time.Second // a link that answers no ping for this long is closed
	maxBackoff      = 30 * time.Second
)

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
func serve(ctx context.Context, c config, n *node) {
	backoff := time.Second
	for ctx.Err() == nil {
		welcomed, err := link(ctx, c, n)
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
func link(ctx context.Context, c config, n *node) (welcomed bool, err error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dialCtx, c.hub, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.token}},
	})
	cancel()
	if err != nil {
		return false, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)

	hello, _ := json.Marshal(map[string]any{
		"type": "hello", "version": protocolVersion, "name": c.name, "hearing": true, "speaking": true,
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
	n.sink.Store(&sink{
		text: func(msg any) {
			data, _ := json.Marshal(msg)
			enqueue(outgoing{websocket.MessageText, data})
		},
		bin: func(data []byte) { enqueue(outgoing{websocket.MessageBinary, data}) },
	})
	defer func() {
		n.sink.Store(nil)
		n.endAll()
	}()

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
		if kind == websocket.MessageBinary {
			if len(data) < 4 || len(data)%2 != 0 {
				continue
			}
			samples := make([]int16, (len(data)-4)/2)
			for i := range samples {
				samples[i] = int16(binary.LittleEndian.Uint16(data[4+2*i:]))
			}
			n.audio(binary.BigEndian.Uint32(data), samples)
			continue
		}
		var m message
		if err := json.Unmarshal(data, &m); err != nil {
			logf("not JSON from the hub: %.80q", data)
			continue
		}
		switch m.Type {
		case "hear":
			n.hear(m.Stream, setting(m.Sensitivity, hearing.DefaultSensitivity), setting(m.Gate, hearing.DefaultGate))
		case "hear_end":
			n.endHearing(m.Stream)
		case "speak":
			n.speak(m.Stream, m.Piece, m.Text)
		case "speak_cancel":
			n.cancel(m.Stream)
		case "reply":
			if m.Error != "" {
				logf("the hub: %s", m.Error)
			}
		default:
			logf("unknown message from the hub: %.80q", data)
		}
	}
}

func setting(v *float64, fallback float64) float64 {
	if v == nil {
		return fallback
	}
	return *v
}
