package main

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// The links that the speech nodes and the agents open to the hub (docs/protocol.md): a
// WebSocket each, JSON in text frames, audio in binary frames, and requests that wait for a
// reply.

const protocolVersion = 1

// link is one open WebSocket, an agent's or a speech node's.
type link struct {
	kind, name string
	ws         *websocket.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	writeMu    sync.Mutex
	nextID     atomic.Int64
	waitMu     sync.Mutex
	waiting    map[int64]chan map[string]any
	since      time.Time // when it linked
	byKey      bool      // with its own key, from pairing, rather than the hub's token
	unpaired   atomic.Bool
	// A speech node's models: whether they're loaded, as it last said (`models`), and when.
	modelsMu sync.Mutex
	loaded   bool
	loadedAt time.Time
}

func (l *link) send(message map[string]any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return l.write(websocket.MessageText, data)
}

func (l *link) write(kind websocket.MessageType, data []byte) error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(l.ctx, 5*time.Second)
	defer cancel()
	return l.ws.Write(ctx, kind, data)
}

// sendAudio sends a binary frame: the stream's number, big-endian, then the samples, 16-bit
// little-endian.
func (l *link) sendAudio(stream uint32, samples []int16) error {
	data := make([]byte, 4+2*len(samples))
	binary.BigEndian.PutUint32(data, stream)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(data[4+2*i:], uint16(s))
	}
	return l.write(websocket.MessageBinary, data)
}

var errLinkClosed = errors.New("the link closed")

// request sends a message with an ID and waits for its reply.
func (l *link) request(ctx context.Context, message map[string]any) (map[string]any, error) {
	id := l.nextID.Add(1)
	wait := make(chan map[string]any, 1)
	l.waitMu.Lock()
	l.waiting[id] = wait
	l.waitMu.Unlock()
	defer func() {
		l.waitMu.Lock()
		delete(l.waiting, id)
		l.waitMu.Unlock()
	}()
	message["id"] = id
	if err := l.send(message); err != nil {
		return nil, err
	}
	select {
	case reply := <-wait:
		return reply, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.ctx.Done():
		return nil, errLinkClosed
	}
}

// tokens are the ones the hub accepts on each link, from its config (`[tokens] speech = "…"`,
// `agent = "…"`), if it has them.
func linkToken(kind string) string { return loadConfig()["tokens."+kind] }

// acceptLink upgrades a request on /speech or /agent, checks its key or token and its hello,
// and reads it until it closes, giving each message to handle. A part that links with its own
// key (partkeys.go) is named as it paired, whatever its hello says.
func acceptLink(w http.ResponseWriter, r *http.Request, kind string, opened func(*link), handle func(*link, map[string]any), audio func(*link, []byte), closed func(*link)) {
	want := linkToken(kind)
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	paired, byKey := pairedPart(kind, got)
	if !byKey && (want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1) {
		http.Error(w, `{"ok": false}`, http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 22)
	ctx, cancel := context.WithCancel(context.Background())
	l := &link{kind: kind, ws: ws, ctx: ctx, cancel: cancel, waiting: map[int64]chan map[string]any{}, since: now()}
	defer func() {
		cancel()
		ws.CloseNow()
	}()
	hello, err := readFrame(ctx, ws, 10*time.Second)
	if err != nil || hello["type"] != "hello" || number(hello["version"]) != protocolVersion {
		ws.Close(websocket.StatusPolicyViolation, "needs hello, version 1")
		return
	}
	l.name = orDefault(str(hello, "name"), kind)
	if byKey {
		l.name, l.byKey = paired, true
	}
	if l.send(map[string]any{"type": "welcome", "version": protocolVersion}) != nil {
		return
	}
	log(kind + " " + l.name + ": connected")
	opened(l)
	go keepAlive(l)
	for {
		kindRead, data, err := ws.Read(ctx)
		if err != nil {
			break
		}
		if kindRead == websocket.MessageBinary {
			audio(l, data)
			continue
		}
		var message map[string]any
		if json.Unmarshal(data, &message) != nil {
			continue
		}
		if message["type"] == "reply" {
			l.waitMu.Lock()
			wait := l.waiting[int64(number(message["id"]))]
			l.waitMu.Unlock()
			if wait != nil {
				wait <- message
			}
			continue
		}
		handle(l, message)
	}
	log(kind + " " + l.name + ": disconnected")
	closed(l)
}

// keepAlive pings every 15 s; a link that doesn't answer within 30 s more is closed.
func keepAlive(l *link) {
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
		ctx, cancel := context.WithTimeout(l.ctx, 30*time.Second)
		err := l.ws.Ping(ctx)
		cancel()
		if err != nil {
			l.cancel()
			return
		}
	}
}

func readFrame(ctx context.Context, ws *websocket.Conn, within time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	_, data, err := ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	var message map[string]any
	return message, json.Unmarshal(data, &message)
}

func number(v any) float64 {
	f, _ := v.(float64)
	return f
}

// reply answers a request from the other side.
func reply(l *link, request map[string]any, answer map[string]any) {
	answer["type"], answer["id"] = "reply", request["id"]
	_ = l.send(answer)
}
