// The hub smoke test's stand-ins for the hub's links (docs/protocol.md): agents, which place
// calls and say which sessions listen, and a speech node, so a call can ring. It takes plain
// HTTP on ADDRESS, so a shell script with curl can drive them:
//
//	POST /place?agent=NAME     a place request's fields → the hub's reply, with its status
//	POST /sessions?agent=NAME  {"sessions": [...]}      → {"ok": true} once it's sent
//
// Each agent links the first time it's named, and again whenever its link has dropped, as
// when the hub restarts; so does the speech node, before each call is placed.
//
//	standin -hub 127.0.0.1:18787 -listen 127.0.0.1:18788 -agent-token A -speech-token S
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var hub, agentToken, speechToken string

// link is one open WebSocket to the hub, and the replies it waits for.
type link struct {
	ws      *websocket.Conn
	ctx     context.Context
	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	waiting map[int64]chan map[string]any
}

func open(kind, name, token string) (*link, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://"+hub+"/"+kind, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		return nil, err
	}
	l := &link{ws: ws, waiting: map[int64]chan map[string]any{}}
	var cancelLink context.CancelFunc
	l.ctx, cancelLink = context.WithCancel(context.Background())
	if err := l.send(map[string]any{"type": "hello", "version": 1, "name": name}); err != nil {
		cancelLink()
		return nil, err
	}
	_, data, err := ws.Read(ctx)
	var welcome map[string]any
	if err != nil || json.Unmarshal(data, &welcome) != nil || welcome["type"] != "welcome" {
		cancelLink()
		ws.CloseNow()
		return nil, fmt.Errorf("no welcome: %v", err)
	}
	// Reading answers the hub's pings, and brings the replies; the rest (reask, warm) is
	// ignored.
	go func() {
		defer cancelLink()
		for {
			_, data, err := ws.Read(l.ctx)
			if err != nil {
				return
			}
			var message map[string]any
			if json.Unmarshal(data, &message) != nil || message["type"] != "reply" {
				continue
			}
			id, _ := message["id"].(float64)
			l.mu.Lock()
			wait := l.waiting[int64(id)]
			l.mu.Unlock()
			if wait != nil {
				wait <- message
			}
		}
	}()
	return l, nil
}

func (l *link) alive() bool { return l != nil && l.ctx.Err() == nil }

func (l *link) send(message map[string]any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return l.ws.Write(ctx, websocket.MessageText, data)
}

// request sends a message with an ID and waits for its reply.
func (l *link) request(message map[string]any) (map[string]any, error) {
	wait := make(chan map[string]any, 1)
	l.mu.Lock()
	l.nextID++
	id := l.nextID
	l.waiting[id] = wait
	l.mu.Unlock()
	message["id"] = id
	if err := l.send(message); err != nil {
		return nil, err
	}
	select {
	case reply := <-wait:
		return reply, nil
	case <-time.After(5 * time.Second):
		return nil, errors.New("no reply")
	case <-l.ctx.Done():
		return nil, errors.New("the link closed")
	}
}

var (
	linksMu sync.Mutex
	agents  = map[string]*link{}
	node    *link
)

// agent is the named agent's link, opened again if it has dropped.
func agent(name string) (*link, error) {
	linksMu.Lock()
	defer linksMu.Unlock()
	if l := agents[name]; l.alive() {
		return l, nil
	}
	l, err := open("agent", name, agentToken)
	if err != nil {
		return nil, err
	}
	agents[name] = l
	return l, nil
}

// speechNode links the speech node, if it isn't, so the hub has speech for the call.
func speechNode() error {
	linksMu.Lock()
	defer linksMu.Unlock()
	if node.alive() {
		return nil
	}
	l, err := open("speech", "stand-in node", speechToken)
	if err != nil {
		return err
	}
	node = l
	return nil
}

func answer(w http.ResponseWriter, status int, body map[string]any) {
	data, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func handle(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(io.LimitReader(r.Body, 65536))
	var body map[string]any
	if r.Method != http.MethodPost || json.Unmarshal(data, &body) != nil || body == nil {
		answer(w, http.StatusBadRequest, map[string]any{"ok": false})
		return
	}
	name := r.URL.Query().Get("agent")
	if name == "" {
		name = "stand-in"
	}
	a, err := agent(name)
	if err != nil {
		answer(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	switch r.URL.Path {
	case "/place":
		if err := speechNode(); err != nil {
			answer(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		body["type"] = "place"
		reply, err := a.request(body)
		if err != nil {
			answer(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		delete(reply, "type")
		delete(reply, "id")
		status := http.StatusOK
		if code, refused := reply["status"].(float64); refused && reply["ok"] != true {
			status = int(code)
		}
		answer(w, status, reply)
	case "/sessions":
		body["type"] = "sessions"
		if err := a.send(body); err != nil {
			answer(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		// The hub takes it in its own time; a moment, so a request after this sees it.
		time.Sleep(100 * time.Millisecond)
		answer(w, http.StatusOK, map[string]any{"ok": true})
	default:
		answer(w, http.StatusNotFound, map[string]any{"ok": false})
	}
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18788", "where it takes requests")
	flag.StringVar(&hub, "hub", "127.0.0.1:18787", "the hub's address")
	flag.StringVar(&agentToken, "agent-token", "", "the hub's [tokens] agent")
	flag.StringVar(&speechToken, "speech-token", "", "the hub's [tokens] speech")
	flag.Parse()
	if agentToken == "" || speechToken == "" {
		fmt.Fprintln(os.Stderr, "needs -agent-token and -speech-token")
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "stand-ins taking requests on "+*listen)
	fmt.Fprintln(os.Stderr, http.ListenAndServe(*listen, http.HandlerFunc(handle)))
	os.Exit(1)
}
