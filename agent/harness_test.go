package main

// What the tests run the agent against: a stand-in hub that speaks docs/protocol.md, the
// agent itself in the test process, a mod's requests over its socket, and the stand-in claude
// (fakeclaude_test.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(mcpMain(os.Args[2:], os.Stdin, os.Stdout))
	}
	if script := os.Getenv("RINGBACK_FAKE_CLAUDE"); script != "" {
		os.Exit(fakeClaude(script))
	}
	// Claude Code's own files stay out of the way, should forget look there.
	config, _ := os.MkdirTemp("", "claude-config-")
	os.Setenv("CLAUDE_CONFIG_DIR", config)
	claudeCommand = selfCommand
	caffeinate = "" // no sleep assertions from tests
	code := m.Run()
	os.RemoveAll(config)
	os.Exit(code)
}

// fakeHub is the hub, as far as the agent sees it: it takes the hello, answers place as the
// test says, and keeps every other message the agent sends.
type fakeHub struct {
	t      *testing.T
	server *httptest.Server
	token  string
	// The answer to place: ringing by default.
	place func(m map[string]any) map[string]any
	// What answers /show, what a session shows you; none by default.
	show http.HandlerFunc

	mu       sync.Mutex
	changed  *sync.Cond
	conn     *websocket.Conn
	got      []map[string]any
	binary   [][]byte // binary messages, as a model's live view's frames
	hellos   int
	refusals int
	next     int
	replies  map[int]chan map[string]any
}

func newFakeHub(t *testing.T) *fakeHub {
	h := &fakeHub{t: t, token: "secret", replies: map[int]chan map[string]any{}}
	h.changed = sync.NewCond(&h.mu)
	h.place = func(map[string]any) map[string]any { return map[string]any{"ok": true} }
	h.server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.server.Close)
	return h
}

// newFakeTLSHub is a fake hub that serves TLS, with a certificate its own CA signed.
func newFakeTLSHub(t *testing.T) *fakeHub {
	h := newFakeHub(t)
	h.server.Close()
	h.server = httptest.NewTLSServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.server.Close)
	return h
}

func (h *fakeHub) addr() string {
	return strings.TrimPrefix(strings.TrimPrefix(h.server.URL, "http://"), "https://")
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/show" && h.show != nil && r.Header.Get("Authorization") == "Bearer "+h.token {
		h.show(w, r)
		return
	}
	if r.URL.Path != "/agent" || r.Header.Get("Authorization") != "Bearer "+h.token {
		h.mu.Lock()
		h.refusals++
		h.changed.Broadcast()
		h.mu.Unlock()
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := context.Background()
	_, data, err := conn.Read(ctx)
	var hello map[string]any
	if err != nil || json.Unmarshal(data, &hello) != nil || hello["type"] != "hello" || hello["version"] != float64(1) {
		conn.Close(websocket.StatusPolicyViolation, "hello")
		return
	}
	welcome, _ := json.Marshal(map[string]any{"type": "welcome", "version": 1})
	conn.Write(ctx, websocket.MessageText, welcome)
	h.mu.Lock()
	h.conn = conn
	h.hellos++
	h.changed.Broadcast()
	h.mu.Unlock()
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			h.mu.Lock()
			if h.conn == conn {
				h.conn = nil
			}
			h.mu.Unlock()
			return
		}
		if kind == websocket.MessageBinary {
			h.mu.Lock()
			h.binary = append(h.binary, data)
			h.changed.Broadcast()
			h.mu.Unlock()
			continue
		}
		var m map[string]any
		json.Unmarshal(data, &m)
		h.mu.Lock()
		if m["type"] == "reply" {
			id, _ := m["id"].(float64)
			if answer, ok := h.replies[int(id)]; ok {
				answer <- m
			}
			h.mu.Unlock()
			continue
		}
		h.got = append(h.got, m)
		h.changed.Broadcast()
		place := h.place
		h.mu.Unlock()
		if m["type"] == "place" {
			reply := place(m)
			reply["type"], reply["id"] = "reply", m["id"]
			if _, ok := reply["ok"]; !ok {
				reply["ok"] = true
			}
			h.write(conn, reply)
		}
	}
}

func (h *fakeHub) write(conn *websocket.Conn, m map[string]any) {
	data, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		h.t.Errorf("the hub couldn't send %v: %v", m, err)
	}
}

// send sends the agent a message, as the hub does during a call.
func (h *fakeHub) send(m map[string]any) {
	h.t.Helper()
	h.mu.Lock()
	conn := h.conn
	if m["type"] == "heard" && m["reply"] == nil {
		// As the hub does: what you've heard is of the reply the agent sent last.
		m["reply"] = 0
		for i := len(h.got) - 1; i >= 0; i-- {
			if h.got[i]["type"] == "say" && h.got[i]["call_id"] == m["call_id"] {
				m["reply"] = h.got[i]["reply"]
				break
			}
		}
	}
	h.mu.Unlock()
	if conn == nil {
		h.t.Fatalf("no agent linked to send %v", m)
	}
	h.write(conn, m)
}

// request sends the agent a request and returns its reply.
func (h *fakeHub) request(m map[string]any) map[string]any {
	h.t.Helper()
	h.mu.Lock()
	h.next++
	id := h.next
	answer := make(chan map[string]any, 1)
	h.replies[id] = answer
	h.mu.Unlock()
	m["id"] = id
	h.send(m)
	select {
	case reply := <-answer:
		return reply
	case <-time.After(10 * time.Second):
		h.t.Fatalf("no reply to %v", m)
		return nil
	}
}

// wait waits for a message from the agent that passes match, among those from index from on,
// and returns it and its index.
func (h *fakeHub) wait(from int, timeout time.Duration, match func(map[string]any) bool) (map[string]any, int) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	timer := time.AfterFunc(timeout, func() {
		h.mu.Lock()
		h.changed.Broadcast()
		h.mu.Unlock()
	})
	defer timer.Stop()
	h.mu.Lock()
	defer h.mu.Unlock()
	for {
		for i := from; i < len(h.got); i++ {
			if match(h.got[i]) {
				return h.got[i], i
			}
		}
		if time.Now().After(deadline) {
			return nil, -1
		}
		h.changed.Wait()
	}
}

func (h *fakeHub) count(match func(map[string]any) bool) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.got {
		if match(m) {
			n++
		}
	}
	return n
}

func (h *fakeHub) mark() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.got)
}

// reply waits for reply n of a call to be said, and returns its text.
func (h *fakeHub) reply(callID string, n int, timeout time.Duration) (string, bool) {
	h.t.Helper()
	return h.replyAfter(0, callID, n, timeout)
}

// replyAfter is reply, of what the agent sent from index from on.
func (h *fakeHub) replyAfter(from int, callID string, n int, timeout time.Duration) (string, bool) {
	h.t.Helper()
	if m, _ := h.wait(from, timeout, func(m map[string]any) bool {
		return m["type"] == "said" && m["call_id"] == callID && m["reply"] == float64(n)
	}); m == nil {
		return "", false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	text := ""
	for _, m := range h.got[from:] {
		if m["type"] == "say" && m["call_id"] == callID && m["reply"] == float64(n) {
			text += m["text"].(string)
		}
	}
	return text, true
}

func is(kind string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["type"] == kind }
}

// testAgent is the agent, running in the test against a fakeHub.
type testAgent struct {
	t      *testing.T
	hub    *fakeHub
	calls  *Calls
	agent  *agent
	link   *hubLink
	socket string
	client *http.Client
	dir    string // the fake claude's script and log, and projects
	script string
	logged string

	mu    sync.Mutex
	lines []string
	stop  context.CancelFunc
}

func startAgent(t *testing.T, hub *fakeHub, state string) *testAgent {
	t.Helper()
	// A short folder for the socket: a socket's path has at most 104 bytes.
	short, err := os.MkdirTemp("", "rb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	a := &testAgent{t: t, hub: hub, socket: filepath.Join(short, "voice.sock"), dir: t.TempDir()}
	a.script = filepath.Join(a.dir, "claude.json")
	a.logged = filepath.Join(a.dir, "claude.log")
	a.setReplies()
	t.Setenv("RINGBACK_FAKE_CLAUDE", a.script)
	log := func(line string) {
		a.mu.Lock()
		a.lines = append(a.lines, line)
		a.mu.Unlock()
		if testing.Verbose() {
			t.Log(line)
		}
	}
	a.link = newHubLink(hub.addr(), hub.token, "test-mac", log)
	if state != "" {
		state = filepath.Join(state, stateFile)
	}
	a.calls = newCalls(a.link, log, state)
	a.agent = newAgent(a.calls, a.link, log)
	resend := make(chan struct{}, 1)
	a.link.up = func() {
		select {
		case resend <- struct{}{}:
		default:
		}
		go a.agent.tellModels()
	}
	a.link.down = a.agent.linkDown
	a.calls.restore()
	ctx, cancel := context.WithCancel(context.Background())
	a.stop = cancel
	go a.calls.publish(ctx, resend)
	go a.link.run(ctx)
	listener, err := listen(sessionSide(a.calls), a.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		listener.Close()
		a.closeAll()
	})
	a.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", a.socket)
	}}}
	a.waitLog("linked to the hub", 5*time.Second)
	return a
}

// closeAll closes the Haikus the calls still have, so no stand-in claude outlives the test.
func (a *testAgent) closeAll() {
	a.calls.mu.Lock()
	var haikus []*Talker
	for _, call := range a.calls.calls.list() {
		if call.Talker != nil {
			haikus = append(haikus, call.Talker)
		}
		if call.deadline != nil {
			call.deadline()
		}
		if call.holding != nil {
			call.holding()
		}
	}
	a.calls.mu.Unlock()
	a.agent.mu.Lock()
	var voices []*voice
	for _, o := range a.agent.offered {
		voices = append(voices, o.voice)
	}
	a.agent.mu.Unlock()
	for _, v := range voices {
		v.inbox.put(endMsg{})
		<-v.done
	}
	for _, h := range haikus {
		h.Close()
	}
	a.calls.closing.Wait()
}

// setReplies tells the stand-in claude what to reply, by what the turn says.
func (a *testAgent) setReplies(replies ...fakeReply) {
	data, _ := json.Marshal(fakeScript{Log: a.logged, Replies: replies})
	tmp := a.script + ".new"
	os.WriteFile(tmp, data, 0o644)
	os.Rename(tmp, a.script)
}

// claudeLog is what the stand-in claude logged: its starts, turns and tool results.
func (a *testAgent) claudeLog() []map[string]any {
	data, _ := os.ReadFile(a.logged)
	var entries []map[string]any
	for _, line := range bytes.Split(data, []byte("\n")) {
		var e map[string]any
		if json.Unmarshal(line, &e) == nil {
			entries = append(entries, e)
		}
	}
	return entries
}

// turns are the messages claude was sent, in order.
func (a *testAgent) turns() []string {
	var turns []string
	for _, e := range a.claudeLog() {
		if m, ok := e["message"].(string); ok {
			turns = append(turns, m)
		}
	}
	return turns
}

func (a *testAgent) starts() []map[string]any {
	var starts []map[string]any
	for _, e := range a.claudeLog() {
		if s, ok := e["start"].(map[string]any); ok {
			starts = append(starts, s)
		}
	}
	return starts
}

// waitTurn waits for claude to have been sent n turns, and returns the last.
func (a *testAgent) waitTurn(n int, timeout time.Duration) string {
	a.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if turns := a.turns(); len(turns) >= n {
			return turns[n-1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.t.Fatalf("claude wasn't sent turn %d in %v; it had %q", n, timeout, a.turns())
	return ""
}

func (a *testAgent) log() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.lines...)
}

// waitLog waits for a log line that has text, and returns it.
func (a *testAgent) waitLog(text string, timeout time.Duration) string {
	a.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, line := range a.log() {
			if strings.Contains(line, text) {
				return line
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.t.Fatalf("no log line with %q in %v; the log:\n%s", text, timeout, strings.Join(a.log(), "\n"))
	return ""
}

func (a *testAgent) logged_(text string) bool {
	for _, line := range a.log() {
		if strings.Contains(line, text) {
			return true
		}
	}
	return false
}

// mod makes a request as a session's mod does, over the socket.
func (a *testAgent) mod(method, path string, body any) (int, map[string]any) {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	request, _ := http.NewRequest(method, "http://voice"+path, reader)
	request.Header.Set("content-type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	var answer map[string]any
	json.Unmarshal(data, &answer)
	return response.StatusCode, answer
}

// collect collects a session's messages as its mod does every second, saying its project.
func (a *testAgent) collect(session string) []map[string]any {
	a.t.Helper()
	_, answer := a.mod("GET", "/sessions/"+session+"/messages?project=demo&cwd="+a.project(), nil)
	var messages []map[string]any
	for _, m := range answer["messages"].([]any) {
		messages = append(messages, m.(map[string]any))
	}
	return messages
}

// waitMessage collects a session's messages until one of kind comes, and returns it.
func (a *testAgent) waitMessage(session, kind string, timeout time.Duration) map[string]any {
	a.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, m := range a.collect(session) {
			if m["kind"] == kind {
				return m
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.t.Fatalf("no %s for session %s in %v", kind, session, timeout)
	return nil
}

func (a *testAgent) project() string {
	p := filepath.Join(a.dir, "demo")
	os.MkdirAll(p, 0o755)
	return p
}

// placeCall places a call as a mod does when a turn ends, and returns its ID.
func (a *testAgent) placeCall(session, subject, briefing string) (string, map[string]any) {
	a.t.Helper()
	from := a.hub.mark()
	status, answer := a.mod("POST", "/calls", map[string]any{
		"session_id": session, "project": "demo", "cwd": a.project(), "subject": subject, "briefing": briefing,
	})
	if status != 200 {
		a.t.Fatalf("placing a call: %d %v", status, answer)
	}
	m, _ := a.hub.wait(from, 5*time.Second, is("place"))
	if m == nil {
		a.t.Fatal("the hub wasn't asked to place the call")
	}
	return m["call_id"].(string), answer
}

// answer offers a call while it rings, answers it and connects its audio, as the phone and
// the hub do, and returns the opening the voice said.
func (a *testAgent) answer(callID string) string {
	a.t.Helper()
	if reply := a.hub.request(map[string]any{"type": "offer", "call_id": callID, "kind": "ring", "ringing": true}); reply["ok"] != true {
		a.t.Fatalf("offer refused: %v", reply)
	}
	a.hub.send(map[string]any{"type": "answered", "call_id": callID})
	a.hub.send(map[string]any{"type": "connected", "call_id": callID})
	opening, ok := a.hub.reply(callID, 1, 10*time.Second)
	if !ok {
		a.t.Fatal("no opening")
	}
	return opening
}

// played plays a reply to the end, as the hub does: speaking, everything heard, then idle.
func (a *testAgent) played(callID, text string) {
	a.hub.send(map[string]any{"type": "speaking", "call_id": callID})
	a.hub.send(map[string]any{"type": "heard", "call_id": callID, "text": text})
	a.hub.send(map[string]any{"type": "idle", "call_id": callID})
}

func say(text string) fakeStep { return fakeStep{Text: text} }

func tool(name string, input map[string]any) fakeStep { return fakeStep{Tool: name, Input: input} }

func eventually(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("not in %v: %s", timeout, what)
}

func check(t *testing.T, what string, ok bool, details ...any) {
	t.Helper()
	if !ok {
		t.Errorf("%s %s", what, fmt.Sprint(details...))
	}
}
