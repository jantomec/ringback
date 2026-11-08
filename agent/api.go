package main

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// longestWait is the longest a mod's request for an answer waits for one.
const longestWait = 25 * time.Second

// sessionSide is what the mods call, over the socket:
//   - `POST /calls` places a call with a session's briefing. Outside your work hours the
//     answer has `held_until`, when the call can ring (ISO 8601, with an offset);
//   - `GET /sessions/{id}/messages` collects what waits for a session: each message has a
//     `kind`, `message` for what the voice passed on and `outcome` for how a call went, its
//     `text`, and the call's `transcript` if there is one. `?project=…&cwd=…` say the
//     session's project and folder, for the app's Sessions tab;
//   - `POST /sessions/{id}/messages` leaves one, as the voice does (for tools);
//   - `POST /sessions/{id}/news` brings a session's answer: `{"live": true}` if the voice
//     told the call you're on, false if the session should call you. If you hang up on that
//     call before you've heard it, without a word since the voice started on it (such as
//     "bye"), the agent calls you again with it;
//   - `POST /sessions/{id}/permissions` asks your permission for a tool call, and
//     `POST /sessions/{id}/questions` asks you questions, each with their options. Either is
//     put to the call you're on with the session (`"call": "live"`) or to one placed to ask
//     it (`"placed"`), or held outside your work hours (`"held"`, with `held_until`), and
//     answered with its id; 502 if no call can be placed;
//   - `GET .../{ask}?wait=S` waits up to S seconds (at most 25) for the answer: a
//     permission's `verdict` (allow, deny, none, terminal, cancelled or pending), or the questions'
//     `status` (answered, none, terminal, cancelled or pending) and `answers`, with your `said`, and
//     `"held": true` while its call is held. None: you hung up without answering it.
//     Terminal: you sent the held call's question to the Claude app, so the session asks in
//     its own dialog. A call you didn't answer leaves it pending, for the next call with the
//     session you answer;
//   - `POST .../{ask}` records an answer, as the voice does (for tools);
//   - `DELETE .../{ask}` withdraws it: the session no longer waits for an answer;
//   - `POST /sessions/{id}/show` shows you a file (show.go): `{"path", "caption", "project",
//     "cwd"}`. The answer has the hub's `id` for it once the hub has it, or `"sending": true`
//     if it's still on its way after 25 s; 400 if it can't be shown, 502 if the hub can't be
//     reached or refuses it.
func sessionSide(c *Calls) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /calls", func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodyOf(r, "session_id", "project", "briefing")
		if !ok {
			reply(w, 400, map[string]any{"error": "needs session_id, project and briefing"})
			return
		}
		call, err := c.place(body["session_id"].(string), body["project"].(string), pyString(body["cwd"]),
			pyString(body["subject"]), body["briefing"].(string), nil)
		if err != nil {
			reply(w, 502, map[string]any{"error": err.Error()})
			return
		}
		c.mu.Lock()
		answer := heldFields(call.HeldUntil)
		c.mu.Unlock()
		answer["ok"] = true
		reply(w, 200, answer)
	})
	mux.HandleFunc("GET /sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		reply(w, 200, map[string]any{"messages": c.collect(r.PathValue("id"), q.Get("project"), q.Get("cwd"))})
	})
	mux.HandleFunc("POST /sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodyOf(r, "text")
		if !ok {
			reply(w, 400, map[string]any{"error": "needs text"})
			return
		}
		var transcript *string
		if t, ok := body["transcript"]; ok && t != nil {
			s := pyString(t)
			transcript = &s
		}
		c.send(r.PathValue("id"), body["text"].(string), transcript, "message")
		reply(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /sessions/{id}/news", func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodyOf(r, "text")
		if !ok {
			reply(w, 400, map[string]any{"error": "needs text"})
			return
		}
		reply(w, 200, map[string]any{"live": c.news(r.PathValue("id"), body["text"].(string))})
	})
	mux.HandleFunc("POST /sessions/{id}/show", func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodyOf(r, "path", "project")
		if !ok {
			reply(w, 400, map[string]any{"error": "needs path and project"})
			return
		}
		link, _ := c.hub.(*hubLink)
		answer, err := show(link, showRequest{
			Session: r.PathValue("id"), Project: callerName(body["project"].(string), c.titles.of(r.PathValue("id"))),
			Cwd: pyString(body["cwd"]), Path: body["path"].(string), Caption: pyString(body["caption"]),
		}, c.log)
		var problem errShow
		switch {
		case errors.As(err, &problem):
			reply(w, 400, map[string]any{"error": err.Error()})
		case err != nil:
			reply(w, 502, map[string]any{"error": err.Error()})
		default:
			reply(w, 200, answer)
		}
	})
	asks(mux, c, "permission")
	asks(mux, c, "question")
	return mux
}

// asks are the endpoints for what sessions ask: permissions, or questions.
func asks(mux *http.ServeMux, c *Calls, kind string) {
	path := "/sessions/{id}/" + kind + "s"
	mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
		var body any
		var checked Request
		err := decode(r, &body)
		if err == nil {
			if kind == "permission" {
				checked, err = permission(body)
			} else {
				checked, err = questions(body)
			}
		}
		if err != nil {
			needs := "questions, each with a question and options with labels"
			if kind == "permission" {
				needs = "tool"
			}
			reply(w, 400, map[string]any{"error": "needs " + needs})
			return
		}
		a, how, heldUntil, err := c.ask(r.PathValue("id"), kind, checked)
		if err != nil {
			reply(w, 502, map[string]any{"error": err.Error()})
			return
		}
		answer := heldFields(heldUntil)
		answer[kind], answer["call"] = a.ID, how
		reply(w, 200, answer)
	})
	// asked is what the request names, or nil after answering 404; with mu held.
	asked := func(w http.ResponseWriter, r *http.Request) *Ask {
		if a := c.asked(r.PathValue("id"), kind, r.PathValue("ask")); a != nil {
			return a
		}
		reply(w, 404, map[string]any{"error": "unknown"})
		return nil
	}
	answer := func(a *Ask) map[string]any {
		outcome := a.Outcome
		if outcome == "" {
			outcome = "pending"
		}
		got := map[string]any{"said": a.Said}
		if kind == "permission" {
			got["verdict"] = outcome
		} else {
			got["status"], got["answers"] = outcome, a.Answers
		}
		if c.heldUntilOf(a) != "" {
			got["held"] = true
		}
		return got
	}
	mux.HandleFunc("GET "+path+"/{ask}", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		a := asked(w, r)
		c.mu.Unlock()
		if a == nil {
			return
		}
		seconds, err := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("wait")), 64)
		wait := time.Duration(0)
		if err == nil && seconds > 0 && !math.IsNaN(seconds) { // also NaN
			wait = min(time.Duration(min(seconds, longestWait.Seconds())*float64(time.Second)), longestWait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-a.settled:
		case <-timer.C:
		case <-r.Context().Done():
		}
		timer.Stop()
		c.mu.Lock()
		defer c.mu.Unlock()
		if asked(w, r) == nil { // 404 if it was withdrawn meanwhile
			return
		}
		reply(w, 200, answer(a))
	})
	mux.HandleFunc("POST "+path+"/{ask}", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		a := asked(w, r)
		c.mu.Unlock()
		if a == nil {
			return
		}
		data, err := readBody(r)
		var body map[string]any
		var outcome string
		var answers pairs
		bad := err != nil || json.Unmarshal(data, &body) != nil || body == nil
		said := ""
		if !bad {
			said = pyStringOr(body, "said")
			if kind == "permission" {
				outcome, _ = body["verdict"].(string)
				bad = outcome != "allow" && outcome != "deny"
			} else {
				// In the order the request gives them, which the log keeps.
				var given struct {
					Answers json.RawMessage `json:"answers"`
				}
				outcome = "answered"
				bad = json.Unmarshal(data, &given) != nil || len(given.Answers) == 0 ||
					json.Unmarshal(given.Answers, &answers) != nil || answers == nil
			}
		}
		if bad {
			needs := "answers, {question: answer}"
			if kind == "permission" {
				needs = "verdict, allow or deny"
			}
			reply(w, 400, map[string]any{"error": "needs " + needs})
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.settle(a, outcome, said, answers) {
			reply(w, 409, map[string]any{"error": "answered already: " + a.Outcome})
			return
		}
		reply(w, 200, answer(a))
	})
	mux.HandleFunc("DELETE "+path+"/{ask}", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if a := asked(w, r); a != nil {
			c.withdraw(a)
			reply(w, 200, map[string]any{"ok": true})
		}
	})
}

func readBody(r *http.Request) ([]byte, error) { return io.ReadAll(io.LimitReader(r.Body, 1<<20)) }

// decode reads a request's JSON body.
func decode(r *http.Request, into any) error {
	data, err := readBody(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

// bodyOf is a request's JSON object, if it is one and has the keys as strings.
func bodyOf(r *http.Request, keys ...string) (map[string]any, bool) {
	var body map[string]any
	if decode(r, &body) != nil || body == nil {
		return nil, false
	}
	for _, k := range keys {
		if _, ok := body[k].(string); !ok {
			return nil, false
		}
	}
	return body, true
}

// pyString is str(value) for what a mod sent, "" for nothing.
func pyString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	return pyrepr(value)
}

// pyStringOr is str(body.get(key) or ""): empty, false and zero values are "".
func pyStringOr(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if !v {
			return ""
		}
	case float64:
		if v == 0 {
			return ""
		}
	}
	return pyrepr(m[key])
}

func reply(w http.ResponseWriter, status int, body any) {
	data, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(data)
}

// permission is a permission request as a mod sends it, checked, with its input and context
// cut to what the voice gets; an error if it names no tool.
func permission(body any) (Request, error) {
	m, ok := body.(map[string]any)
	tool, isString := m["tool"].(string)
	if !ok || !isString || strings.TrimSpace(tool) == "" {
		return Request{}, errBadRequest
	}
	return Request{
		Tool: tool, Description: pyStringOr(m, "description"), Input: cut(pyStringOr(m, "input"), inputMost),
		Context: cut(pyStringOr(m, "context"), contextMost), Project: pyStringOr(m, "project"),
		Cwd: pyStringOr(m, "cwd"), Subject: pyStringOr(m, "subject"),
	}, nil
}

// questions are questions as a mod sends them (those of Claude Code's AskUserQuestion),
// checked; an error unless there are 1 to 4, each with its text and options with labels.
func questions(body any) (Request, error) {
	m, _ := body.(map[string]any)
	asked, ok := m["questions"].([]any)
	if !ok || len(asked) < 1 || len(asked) > 4 {
		return Request{}, errBadRequest
	}
	checked := []Question{}
	for _, item := range asked {
		q, ok := item.(map[string]any)
		text, isString := q["question"].(string)
		if !ok || !isString || strings.TrimSpace(text) == "" {
			return Request{}, errBadRequest
		}
		var options []any
		if o, present := q["options"]; present && o != nil && o != false {
			if options, ok = o.([]any); !ok {
				return Request{}, errBadRequest
			}
		}
		checkedOptions := []Option{}
		for _, o := range options {
			option, ok := o.(map[string]any)
			label, isString := option["label"].(string)
			if !ok || !isString {
				return Request{}, errBadRequest
			}
			checkedOptions = append(checkedOptions, Option{label, pyStringOr(option, "description")})
		}
		multi, _ := q["multiSelect"].(bool)
		checked = append(checked, Question{text, pyStringOr(q, "header"), checkedOptions, multi})
	}
	return Request{
		Questions: checked, Context: cut(pyStringOr(m, "context"), contextMost), Project: pyStringOr(m, "project"),
		Cwd: pyStringOr(m, "cwd"), Subject: pyStringOr(m, "subject"),
	}, nil
}

type badRequest struct{}

func (badRequest) Error() string { return "bad request" }

var errBadRequest error = badRequest{}

// listen serves handler on a Unix socket at path, which only you can open.
func listen(handler http.Handler, path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	os.Remove(path)
	old := syscall.Umask(0o177) // the socket is created readable and writable by you alone
	listener, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	go (&http.Server{Handler: handler}).Serve(listener)
	return listener, nil
}
