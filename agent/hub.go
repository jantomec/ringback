package main

// The agent's link to the hub (docs/protocol.md, "The agent"): one WebSocket it opens, so the
// Mac needn't be reachable. Over it the agent places calls and says which sessions listen,
// and the hub passes on the phone's requests for this agent's calls and, during a call, when
// you answer, when the audio connects, your turns and interruptions and how much of each
// reply you've heard; the agent streams the voice's replies back.
//
// The phone offers a call while it rings, marked ringing, and the hub says when you answer;
// an offer without ringing is answered at once. The voice opens when you've answered and the
// audio is connected. A new offer for the same call replaces the earlier one, as when an
// offer made while ringing didn't connect. An offer of kind "back" calls back a call from the
// phone's call history, answered or not: it's answered at once, and the voice picks up. So is
// one of kind "now", for a call held outside your work hours that you called from the app's
// waiting calls, and one of kind "contact", for a session you called from the app's Sessions
// tab.
// From the waiting calls you can also send a held call's question to the Claude app
// (claude_app): the session asks it in its own dialog, which Remote Control shows there, and
// the call is forgotten.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const protocolVersion = 1

var errNotLinked = errors.New("not linked to the hub")

var (
	// pingEvery and silentFor: either side pings every 15 s; a link silent for 45 s is closed.
	pingEvery = 15 * time.Second
	silentFor = 45 * time.Second
	// The link is opened again after 1, 2, 4… s, up to 30 s.
	firstRetry = time.Second
	lastRetry  = 30 * time.Second
)

type hubLink struct {
	url, addr, token, name string
	client                 *http.Client // trusting a private CA too (trustCA); nil for the default
	log                    func(string)
	handle                 func(message map[string]any)
	up, down               func() // the link has opened, or closed

	mu      sync.Mutex
	conn    *websocket.Conn
	nextID  int
	pending map[int]chan map[string]any
	writing sync.Mutex
}

// newHubLink is the link to the hub at addr: its listen address ("10.0.0.1:8787"), linked
// over plain ws://; or its URL, which turns into its link's ("https://hub.example.com:8787"
// into wss://…/agent); or the link's own URL.
func newHubLink(addr, token, name string, log func(string)) *hubLink {
	url := addr
	switch {
	case !strings.Contains(addr, "://"):
		url = "ws://" + addr + "/agent"
	case strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://"):
		url = "ws" + strings.TrimPrefix(strings.TrimSuffix(addr, "/"), "http") + "/agent"
	}
	return &hubLink{url: url, addr: addr, token: token, name: name, log: log, pending: map[int]chan map[string]any{}}
}

func (h *hubLink) address() string { return h.addr }

// trustCA has the link trust the certificate authority in the PEM file at path as well as the
// system's: for a hub whose certificate a CA of your own signed.
func (h *hubLink) trustCA(path string) error {
	client, err := caClient(path)
	h.client = client
	return err
}

// caClient is an HTTP client that trusts the certificate authority in the PEM file at path as
// well as the system's.
func caClient(path string) (*http.Client, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("no certificate in %s", path)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool}
	return &http.Client{Transport: transport}, nil
}

// run keeps the link open until ctx ends.
func (h *hubLink) run(ctx context.Context) {
	retry := firstRetry
	for ctx.Err() == nil {
		welcomed, err := h.connect(ctx)
		if ctx.Err() != nil {
			return
		}
		if welcomed {
			retry = firstRetry
		}
		h.log(fmt.Sprintf("hub link: %v; opening it again in %.0f s", err, retry.Seconds()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
		retry = min(retry*2, lastRetry)
	}
}

// connect opens the link, says hello, and reads it until it closes; welcomed if the hub took
// the hello.
func (h *hubLink) connect(ctx context.Context) (bool, error) {
	dial, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, response, err := websocket.Dial(dial, h.url, &websocket.DialOptions{
		HTTPClient: h.client,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + h.token}},
	})
	cancel()
	if err != nil {
		if response != nil && response.StatusCode == http.StatusUnauthorized {
			return false, errors.New("the hub refused the key or token (401)")
		}
		return false, fmt.Errorf("can't reach the hub at %s: %v", h.addr, err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	hello, _ := json.Marshal(map[string]any{"type": "hello", "version": protocolVersion, "name": h.name})
	greet, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.Write(greet, websocket.MessageText, hello); err != nil {
		return false, fmt.Errorf("can't say hello: %v", err)
	}
	_, data, err := conn.Read(greet)
	if err != nil {
		return false, fmt.Errorf("no welcome: %v", err)
	}
	var welcome map[string]any
	json.Unmarshal(data, &welcome)
	if welcome["type"] != "welcome" || welcome["version"] != float64(protocolVersion) {
		return false, fmt.Errorf("the hub answered the hello with %s", strings.TrimSpace(string(data)))
	}
	h.mu.Lock()
	h.conn = conn
	h.mu.Unlock()
	h.log("linked to the hub at " + h.addr)
	if h.up != nil {
		h.up()
	}
	pinging, stop := context.WithCancel(ctx)
	defer stop()
	go h.ping(pinging, conn)
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			h.closed()
			return true, err
		}
		if kind != websocket.MessageText {
			continue
		}
		var message map[string]any
		if json.Unmarshal(data, &message) != nil || message == nil {
			continue
		}
		if message["type"] == "reply" {
			id, _ := message["id"].(float64)
			h.mu.Lock()
			answer, ok := h.pending[int(id)]
			delete(h.pending, int(id))
			h.mu.Unlock()
			if ok {
				answer <- message
			}
			continue
		}
		h.handle(message)
	}
}

// ping pings every pingEvery, and closes a link that doesn't answer within silentFor.
func (h *hubLink) ping(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(pingEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		answered, cancel := context.WithTimeout(ctx, silentFor-pingEvery)
		err := conn.Ping(answered)
		cancel()
		if err != nil && ctx.Err() == nil {
			conn.Close(websocket.StatusGoingAway, "silent")
			return
		}
	}
}

func (h *hubLink) closed() {
	h.mu.Lock()
	h.conn = nil
	for id, answer := range h.pending {
		close(answer)
		delete(h.pending, id)
	}
	h.mu.Unlock()
	if h.down != nil {
		h.down()
	}
}

func (h *hubLink) write(conn *websocket.Conn, message map[string]any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h.writing.Lock()
	defer h.writing.Unlock()
	return conn.Write(ctx, websocket.MessageText, data)
}

// sendBinary sends a binary message, such as a frame of a model's live view (view.go).
func (h *hubLink) sendBinary(data []byte) error {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return errNotLinked
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.writing.Lock()
	defer h.writing.Unlock()
	return conn.Write(ctx, websocket.MessageBinary, data)
}

// send sends a message that needs no answer.
func (h *hubLink) send(message map[string]any) error {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return errNotLinked
	}
	return h.write(conn, message)
}

// request sends a message with an ID and waits for the hub's reply to it.
func (h *hubLink) request(ctx context.Context, message map[string]any) (map[string]any, error) {
	h.mu.Lock()
	conn := h.conn
	if conn == nil {
		h.mu.Unlock()
		return nil, errNotLinked
	}
	h.nextID++
	id := h.nextID
	answer := make(chan map[string]any, 1)
	h.pending[id] = answer
	h.mu.Unlock()
	forget := func() {
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
	}
	sent := map[string]any{"id": id}
	for k, v := range message {
		sent[k] = v
	}
	if err := h.write(conn, sent); err != nil {
		forget()
		return nil, err
	}
	select {
	case reply, ok := <-answer:
		if !ok {
			return nil, errors.New("the link closed")
		}
		return reply, nil
	case <-ctx.Done():
		forget()
		return nil, errors.New("no answer in time")
	}
}

// answer replies to the hub's request: ok, or refused with an HTTP status and why.
func (h *hubLink) answer(request map[string]any, status int, why string) {
	reply := map[string]any{"type": "reply", "id": request["id"], "ok": status == 0}
	if status != 0 {
		reply["status"], reply["error"] = status, why
	}
	if err := h.send(reply); err != nil {
		h.log("couldn't answer the hub: " + err.Error())
	}
}

// agent answers the hub for this Mac's calls: the phone's requests, and what happens during a
// call, which goes to the call's voice.
type agent struct {
	calls *Calls
	hub   *hubLink
	log   func(string)

	mu      sync.Mutex
	offered map[string]*offer // the latest offer for each call, while its voice runs

	viewMu  sync.Mutex
	viewing *viewer // a model's live view, while it streams (view.go)
}

// offer is an offer the phone made for a call, while its voice runs.
type offer struct {
	voice     *voice
	at        time.Time // when it came
	answered  time.Time // when you answered
	connected time.Time // when the audio connected
}

func newAgent(calls *Calls, hub *hubLink, log func(string)) *agent {
	a := &agent{calls: calls, hub: hub, log: log, offered: map[string]*offer{}}
	hub.handle = a.handle
	return a
}

func (a *agent) logCall(id, line string) { a.log("call " + prefix(id, 8) + ": " + line) }

func (a *agent) handle(m map[string]any) {
	id, _ := m["call_id"].(string)
	switch m["type"] {
	case "offer":
		status, why := a.offer(m)
		a.hub.answer(m, status, why)
	case "claude_app":
		status, why := a.claudeApp(id)
		a.hub.answer(m, status, why)
	case "model":
		value, _ := m["value"].(string)
		a.calls.setModelChoice(value)
	case "models_refresh":
		go a.tellModels()
	case "reask":
		// You took calls on the phone (its hold switch): the waiting calls the hub names that
		// are held here ask it again now, so they ring at once.
		var ids []string
		list, _ := m["call_ids"].([]any)
		for _, item := range list {
			if s, ok := item.(string); ok {
				ids = append(ids, s)
			}
		}
		a.calls.reask(ids)
	case "cancel":
		// You cancelled a waiting call on the phone or the hub's portal.
		by, _ := m["by"].(string)
		status, why := a.calls.cancel(id, by)
		a.hub.answer(m, status, why)
	case "missed":
		// You didn't answer a call the hub rang, or declined it: it's held to ring again.
		heldUntil, _ := m["held_until"].(string)
		if reason, _ := m["reason"].(string); reason == callBack {
			heldUntil = callBack
		}
		tries, _ := m["tries"].(float64)
		a.calls.missed(id, heldUntil, int(tries))
	case "view":
		status, why := a.view(m)
		a.hub.answer(m, status, why)
	case "view_input":
		a.viewInput(m)
	case "view_end":
		why, _ := m["why"].(string)
		viewID, _ := m["view_id"].(string)
		a.endView(viewID, or(why, "the phone closed it"))
	case "answered":
		a.markAnswered(id)
	case "connected":
		a.toVoice(id, func(o *offer) any {
			o.connected = time.Now()
			a.logCall(id, fmt.Sprintf("connected, %.2f s after the offer", time.Since(o.at).Seconds()))
			return connectedMsg{o.connected}
		})
	case "speaking":
		a.toVoice(id, func(*offer) any { return speakingMsg{} })
	case "idle":
		a.toVoice(id, func(*offer) any { return idleMsg{} })
	case "quiet":
		waited, _ := m["waited"].(bool)
		a.toVoice(id, func(*offer) any { return quietMsg{waited} })
	case "heard":
		text, _ := m["text"].(string)
		reply, _ := m["reply"].(float64)
		a.toVoice(id, func(*offer) any { return heardMsg{text, int(reply)} })
	case "interrupted":
		reply, _ := m["reply"].(float64)
		heard, _ := m["heard"].(string)
		paused, _ := m["paused"].(bool)
		a.toVoice(id, func(*offer) any { return interrupted{int(reply), heard, paused} })
	case "turn":
		text, _ := m["text"].(string)
		cut, _ := m["interrupted"].(bool)
		a.toVoice(id, func(*offer) any { return turnMsg{text, cut} })
	case "ended":
		var lasted *float64
		if l, ok := m["lasted"].(float64); ok {
			lasted = &l
		}
		a.ended(id, lasted)
	}
}

func (a *agent) toVoice(id string, message func(*offer) any) {
	a.mu.Lock()
	o := a.offered[id]
	var m any
	if o != nil {
		m = message(o)
	}
	a.mu.Unlock()
	if o != nil {
		o.voice.inbox.put(m)
	}
}

func (a *agent) toHub(message map[string]any) {
	if err := a.hub.send(message); err != nil {
		id, _ := message["call_id"].(string)
		a.logCall(id, fmt.Sprintf("couldn't send the hub %s: %v", message["type"], err))
	}
}

// offer answers the phone's offer of a call: 0 if it goes ahead, or a status and why not.
func (a *agent) offer(m map[string]any) (int, string) {
	at := time.Now()
	id, _ := m["call_id"].(string)
	kind, _ := m["kind"].(string)
	// Made while the phone rings; an offer without it comes once you've answered.
	ringing := m["ringing"] == true && (kind == "ring" || kind == "")
	var call *Call
	switch kind {
	case "contact":
		// You called a session from the app's Sessions tab: a new call under the ID the phone made,
		// answered at once. It must be long enough not to be guessed.
		session, _ := m["session_id"].(string)
		if len(id) >= 16 {
			call = a.calls.contact(session, id)
		}
		if call == nil {
			a.logCall(id, fmt.Sprintf("called session %s from Sessions, refused: it isn't listening", prefix(session, 8)))
			return 403, "not listening"
		}
	case "back":
		call = a.calls.back(id)
	case "now":
		call = a.calls.now(id)
	default:
		call = a.calls.offered(id)
	}
	if call != nil {
		a.calls.mu.Lock()
		heldUntil := call.HeldUntil
		a.calls.mu.Unlock()
		if heldUntil != "" {
			// Held while your phone holds calls, it hasn't rung, so the phone knows its ID only
			// from the hub's waiting calls, and calls it now.
			a.logCall(id, "refused, held "+until(heldUntil))
			return 403, "unknown call"
		}
	}
	if call == nil {
		switch kind {
		case "now":
			a.logCall(id, "called now, refused: not a call waiting here")
		case "back":
			// Never placed here, held and not rung yet, forgotten over a day ago, or before a
			// restart.
			a.logCall(id, "call back refused, not a call this agent remembers")
		default:
			a.logCall(id, "refused, not a call this agent placed")
		}
		return 403, "unknown call"
	}
	a.mu.Lock()
	earlier := a.offered[id]
	a.mu.Unlock()
	if earlier != nil {
		// The phone gave up on its earlier offer, such as one made while it rang that didn't
		// connect. This one replaces it, and its voice stops using the call's model.
		a.logCall(id, "a new offer replaces the earlier one")
		a.logCall(id, "replaced by a new offer")
		earlier.voice.inbox.put(endMsg{})
		select {
		case <-earlier.voice.done:
		case <-time.After(5 * time.Second):
		}
	}
	a.calls.mu.Lock()
	if call.Talker != nil && (call.Talker.Used() || (earlier != nil && !isClosed(earlier.voice.done))) {
		// A voice has read its replies already, so this offer's voice starts its own.
		a.calls.closeLater(call.Talker)
		call.Talker = nil
	}
	talker := call.Talker
	a.calls.mu.Unlock()
	o := &offer{voice: newVoice(a.calls, call, talker, a.toHub), at: at}
	a.mu.Lock()
	a.offered[id] = o
	a.mu.Unlock()
	go o.voice.run()
	if !ringing {
		a.markAnswered(id)
	}
	how := map[string]string{"ring": "", "back": ", called back", "now": ", called now", "contact": ", from Sessions"}[kind]
	if ringing {
		how = ", ringing"
	}
	a.logCall(id, fmt.Sprintf("offer answered in %.2f s%s", time.Since(at).Seconds(), how))
	return 0, ""
}

func isClosed(done chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// markAnswered: you answered a call, once. Its transcript starts, and its voice may open.
func (a *agent) markAnswered(id string) {
	a.mu.Lock()
	o := a.offered[id]
	if o == nil {
		a.mu.Unlock()
		a.logCall(id, "answered, but no offer for it")
		return
	}
	if !o.answered.IsZero() {
		a.mu.Unlock()
		return
	}
	o.answered = time.Now()
	a.mu.Unlock()
	a.logCall(id, "answered")
	if call := a.calls.markAnswered(id); call != nil {
		a.calls.mu.Lock()
		if call.Link != nil && call.Link.transcript != nil {
			a.logCall(id, "transcript "+call.Link.transcript.Path)
		}
		a.calls.mu.Unlock()
	}
	o.voice.inbox.put(answeredMsg{o.answered})
}

// ended: the call has ended on the phone's side. Its voice passes on what you'd heard, and a
// call you answered is forgotten, which tells its session; one you didn't stays placed until
// it's forgotten, so the phone can offer again.
func (a *agent) ended(id string, lasted *float64) {
	a.mu.Lock()
	o := a.offered[id]
	delete(a.offered, id)
	a.mu.Unlock()
	if o == nil {
		return
	}
	o.voice.end()
	if o.answered.IsZero() || o.connected.IsZero() || lasted == nil {
		a.logCall(id, "finished, its audio never connected")
		lasted = nil
	} else {
		a.logCall(id, fmt.Sprintf("finished: you talked %.1f s", *lasted))
	}
	if !o.answered.IsZero() {
		a.calls.end(id, lasted, o.voice.unheardNews())
	}
}

// linkDown: the hub's link has closed, and with it the calls' audio: each call on it ends, as
// if the hub had said so, having lasted from when you answered or it connected, whichever came
// last, until now.
func (a *agent) linkDown() {
	a.endView("", "the link to the hub closed")
	a.mu.Lock()
	type gone struct {
		id     string
		lasted *float64
	}
	var calls []gone
	for id, o := range a.offered {
		var lasted *float64
		if !o.answered.IsZero() && !o.connected.IsZero() {
			start := o.answered
			if o.connected.After(start) {
				start = o.connected
			}
			l := time.Since(start).Seconds()
			lasted = &l
		}
		calls = append(calls, gone{id, lasted})
	}
	a.mu.Unlock()
	for _, c := range calls {
		a.logCall(c.id, "the hub's link closed, so the call has ended")
		a.ended(c.id, c.lasted)
	}
}

// claudeApp: you sent a waiting call's question to the Claude app, from the app's waiting
// calls: the session asks it in its own dialog, which Remote Control shows there, and the call
// is forgotten. Like an offer, it needs the call's ID. 403 for a call that doesn't wait here;
// 409 if its session isn't listening, or it has no question.
func (a *agent) claudeApp(id string) (int, string) {
	why := a.calls.toClaudeApp(id)
	if why == "" {
		return 0, ""
	}
	a.logCall(id, "its question can't go to the Claude app, "+why)
	if why == "not waiting" {
		return 403, why
	}
	return 409, why
}
