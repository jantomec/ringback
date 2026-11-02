package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The state file, in the voice server's format (voice-state.json), so either reads what the
// other wrote: each Ask once, in "asks", which the calls, the pool and the asks a mod can read
// refer to by its place there.
type savedState struct {
	Calls   []savedCall          `json:"calls"`
	Past    []savedCall          `json:"past"`
	Pool    map[string][]int     `json:"pool"`
	Kept    []int                `json:"kept"`
	Waiting map[string][]Message `json:"waiting"`
	Asks    []savedAsk           `json:"asks"`
}

type savedCall struct {
	ID        string     `json:"id"`
	Session   string     `json:"session"`
	Project   string     `json:"project"`
	Cwd       string     `json:"cwd"`
	Subject   string     `json:"subject"`
	Briefing  string     `json:"briefing"`
	At        float64    `json:"at"`
	HeldUntil *string    `json:"held_until"`
	Asked     []string   `json:"asked"`
	Answered  bool       `json:"answered"`
	Now       bool       `json:"now"`
	Contact   bool       `json:"contact"`
	Talked    *float64   `json:"talked"`
	Back      *savedCall `json:"back"`
	Link      *savedLink `json:"link"`
	Forgotten *float64   `json:"forgotten,omitempty"`
}

type savedLink struct {
	Listening  bool        `json:"listening"`
	Asks       []int       `json:"asks"`
	Briefed    []int       `json:"briefed"`
	Notes      []savedNote `json:"notes"`
	Transcript *string     `json:"transcript"`
}

// savedNote is [ask, text]: the ask's place, or null for a note about none.
type savedNote struct {
	Ask  *int
	Text string
}

func (n savedNote) MarshalJSON() ([]byte, error) { return json.Marshal([]any{n.Ask, n.Text}) }

func (n *savedNote) UnmarshalJSON(data []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(data, &pair); err != nil {
		return err
	}
	if len(pair) != 2 {
		return fmt.Errorf("a note is [ask, text]")
	}
	if err := json.Unmarshal(pair[0], &n.Ask); err != nil {
		return err
	}
	return json.Unmarshal(pair[1], &n.Text)
}

type savedAsk struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Session string          `json:"session"`
	Request json.RawMessage `json:"request"`
	Outcome *string         `json:"outcome"`
	Said    *string         `json:"said"`
	Answers pairs           `json:"answers"`
}

// requestJSON is a request as the voice server keeps it: a permission's fields, or the
// questions'.
func (a *Ask) requestJSON() any {
	r := a.Request
	if a.Kind == "permission" {
		return struct {
			Tool        string `json:"tool"`
			Description string `json:"description"`
			Input       string `json:"input"`
			Context     string `json:"context"`
			Project     string `json:"project"`
			Cwd         string `json:"cwd"`
			Subject     string `json:"subject"`
		}{r.Tool, r.Description, r.Input, r.Context, r.Project, r.Cwd, r.Subject}
	}
	questions := r.Questions
	if questions == nil {
		questions = []Question{}
	}
	return struct {
		Project   string     `json:"project"`
		Cwd       string     `json:"cwd"`
		Subject   string     `json:"subject"`
		Questions []Question `json:"questions"`
		Context   string     `json:"context"`
	}{r.Project, r.Cwd, r.Subject, questions, r.Context}
}

// save writes what it keeps to the state file, if it changed: atomically, so a restart finds
// either the old state or the new one. Only you can read it: it holds the calls' IDs, which
// are their secrets. Called with mu held.
func (c *Calls) save() {
	if c.state == "" {
		return
	}
	data, err := json.Marshal(c.snapshot())
	if err != nil {
		c.log(fmt.Sprintf("couldn't write %s: %v", c.state, err))
		return
	}
	text := string(data)
	if text == c.written {
		return
	}
	tmp := c.state + ".new"
	err = func() error {
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return os.Rename(tmp, c.state)
	}()
	if err != nil {
		c.log(fmt.Sprintf("couldn't write %s: %v", c.state, err))
		return
	}
	c.written = text
}

func (c *Calls) snapshot() savedState {
	var asks []*Ask
	places := map[*Ask]int{}
	ref := func(a *Ask) int {
		if n, ok := places[a]; ok {
			return n
		}
		places[a] = len(asks)
		asks = append(asks, a)
		return places[a]
	}
	state := savedState{
		Calls: []savedCall{}, Past: []savedCall{}, Pool: map[string][]int{}, Kept: []int{},
		Waiting: map[string][]Message{}, Asks: []savedAsk{},
	}
	for _, call := range c.calls.list() {
		state.Calls = append(state.Calls, saved(call, ref))
	}
	for _, p := range c.past.list() {
		s := saved(p.call, ref)
		at := p.at
		s.Forgotten = &at
		state.Past = append(state.Past, s)
	}
	for session, pool := range c.pool {
		if pool.len() > 0 {
			refs := []int{}
			for _, a := range pool.list() {
				refs = append(refs, ref(a))
			}
			state.Pool[session] = refs
		}
	}
	for _, a := range c.asks.list() {
		state.Kept = append(state.Kept, ref(a))
	}
	for session, messages := range c.waiting {
		if len(messages) > 0 {
			state.Waiting[session] = messages
		}
	}
	for _, a := range asks {
		request, _ := json.Marshal(a.requestJSON())
		saved := savedAsk{ID: a.ID, Kind: a.Kind, Session: a.Session, Request: request, Said: a.Said, Answers: a.Answers}
		if a.Outcome != "" {
			outcome := a.Outcome
			saved.Outcome = &outcome
		}
		state.Asks = append(state.Asks, saved)
	}
	return state
}

// saved is a call as the state file keeps it: what it was placed with, its link's asks and
// notes by their place in the file's asks, and its transcript, if you answered it.
func saved(call *Call, ref func(*Ask) int) savedCall {
	s := savedCall{
		ID: call.ID, Session: call.Session, Project: call.Project, Cwd: call.Cwd, Subject: call.Subject,
		Briefing: call.Briefing, At: call.At, Asked: call.Asked, Answered: call.answered(), Now: call.Now,
		Contact: call.Contact, Talked: call.Talked,
	}
	if s.Asked == nil {
		s.Asked = []string{}
	}
	if call.HeldUntil != "" {
		heldUntil := call.HeldUntil
		s.HeldUntil = &heldUntil
	}
	if call.Back != nil {
		back := saved(call.Back, ref)
		s.Back = &back
	}
	if l := call.Link; l != nil {
		link := &savedLink{Listening: l.listening, Asks: []int{}, Briefed: []int{}, Notes: []savedNote{}}
		for _, a := range l.asks.list() {
			link.Asks = append(link.Asks, ref(a))
		}
		for _, a := range l.briefed {
			link.Briefed = append(link.Briefed, ref(a))
		}
		for _, n := range l.notes {
			note := savedNote{Text: n.text}
			if n.ask != nil {
				r := ref(n.ask)
				note.Ask = &r
			}
			link.Notes = append(link.Notes, note)
		}
		if l.transcript != nil {
			link.Transcript = &l.transcript.Relative
		} else if l.transcriptPath != "" {
			path := l.transcriptPath
			link.Transcript = &path
		}
		s.Link = link
	}
	return s
}

// restore reads what was kept before a restart. Held calls are held again, and ask the hub
// when they're due. A call that was ringing or on has ended: its session is told so, what it
// asked waits for the session's next call you answer, as a missed call's does, and it can be
// called back. A state file it can't read is set aside.
func (c *Calls) restore() {
	if c.state == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := os.ReadFile(c.state)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var calls []*Call
	var past ordered[pastCall]
	pool := map[string]*ordered[*Ask]{}
	var kept ordered[*Ask]
	waiting := map[string][]Message{}
	if err == nil {
		err = func() error {
			var top map[string]json.RawMessage
			if err := json.Unmarshal(data, &top); err != nil {
				return err
			}
			for _, key := range []string{"calls", "past", "pool", "kept", "waiting", "asks"} {
				if _, ok := top[key]; !ok {
					return fmt.Errorf("KeyError: %q", key)
				}
			}
			var state savedState
			if err := json.Unmarshal(data, &state); err != nil {
				return err
			}
			asks := make([]*Ask, len(state.Asks))
			for i, s := range state.Asks {
				var request Request
				if err := json.Unmarshal(s.Request, &request); err != nil {
					return err
				}
				a := newAsk(s.ID, s.Kind, s.Session, request)
				a.Said, a.Answers = s.Said, s.Answers
				if s.Outcome != nil && *s.Outcome != "" {
					a.settle(*s.Outcome, s.Said, s.Answers)
				}
				asks[i] = a
			}
			at := func(i int) (*Ask, error) {
				if i < 0 || i >= len(asks) {
					return nil, fmt.Errorf("IndexError: ask %d", i)
				}
				return asks[i], nil
			}
			for _, s := range state.Calls {
				call, err := c.loaded(s, at)
				if err != nil {
					return err
				}
				calls = append(calls, call)
			}
			for _, s := range state.Past {
				call, err := c.loaded(s, at)
				if err != nil {
					return err
				}
				forgotten := 0.0
				if s.Forgotten != nil {
					forgotten = *s.Forgotten
				}
				past.set(s.ID, pastCall{call, forgotten})
			}
			for session, refs := range state.Pool {
				o := &ordered[*Ask]{}
				for _, i := range refs {
					a, err := at(i)
					if err != nil {
						return err
					}
					o.set(a.ID, a)
				}
				pool[session] = o
			}
			for _, i := range state.Kept {
				a, err := at(i)
				if err != nil {
					return err
				}
				kept.set(a.ID, a)
			}
			for session, messages := range state.Waiting {
				waiting[session] = messages
			}
			return nil
		}()
	}
	if err != nil {
		aside := c.state + ".unreadable"
		c.log(fmt.Sprintf("can't read %s (%v); set aside as %s, starting afresh", c.state, err, filepath.Base(aside)))
		os.Rename(c.state, aside)
		return
	}
	c.past, c.pool, c.asks, c.waiting = past, pool, kept, waiting
	for _, call := range calls {
		if call.HeldUntil != "" {
			c.calls.set(call.ID, call)
			c.logCall(call, fmt.Sprintf("still held %s after a restart: %s", until(call.HeldUntil), call.Subject))
			c.startHolding(call)
			continue
		}
		// It was ringing or on: it ended when the agent stopped.
		if call.Link != nil && call.Link.listening {
			p := c.poolOf(call.Session)
			for _, a := range call.Link.asks.list() {
				p.set(a.ID, a)
			}
		}
		c.logCall(call, "ended by the restart: "+call.Subject)
		var transcript *string
		if call.Link != nil && call.Link.transcriptPath != "" {
			path := call.Link.transcriptPath
			transcript = &path
		}
		c.waiting[call.Session] = append(c.waiting[call.Session], Message{"outcome", restarted(call), transcript})
		c.past.set(call.ID, pastCall{talked(call, true), unixNow()})
	}
	for _, a := range c.asks.list() {
		c.keepUntilRead(a)
	}
	c.expire()
	held, messages := 0, 0
	for _, call := range c.calls.list() {
		if call.HeldUntil != "" {
			held++
		}
	}
	for _, m := range c.waiting {
		messages += len(m)
	}
	c.log(fmt.Sprintf("restored %d held calls, %d to call back, %d asks, %d messages for sessions",
		held, c.past.len(), c.asks.len(), messages))
	c.save()
	c.stayAwake()
}

// loaded is a call as saved wrote it, with its link to its session if it had one.
func (c *Calls) loaded(s savedCall, at func(int) (*Ask, error)) (*Call, error) {
	call := &Call{
		ID: s.ID, Session: s.Session, Project: s.Project, Cwd: s.Cwd, Subject: s.Subject, Briefing: s.Briefing,
		Placed: time.Now(), At: s.At, Asked: s.Asked, Now: s.Now, Contact: s.Contact, Talked: s.Talked,
		reask: make(chan struct{}, 1),
	}
	if call.Asked == nil {
		call.Asked = []string{}
	}
	if s.HeldUntil != nil {
		call.HeldUntil = *s.HeldUntil
	}
	if s.Answered {
		call.AnsweredAt = time.Now()
	}
	if s.Back != nil {
		back, err := c.loaded(*s.Back, at)
		if err != nil {
			return nil, err
		}
		call.Back = back
	}
	if l := s.Link; l != nil {
		link := c.newLink(call)
		link.listening = l.Listening
		for _, i := range l.Asks {
			a, err := at(i)
			if err != nil {
				return nil, err
			}
			link.asks.set(a.ID, a)
		}
		for _, i := range l.Briefed {
			a, err := at(i)
			if err != nil {
				return nil, err
			}
			link.briefed = append(link.briefed, a)
		}
		for _, n := range l.Notes {
			note := linkNote{text: n.Text}
			if n.Ask != nil {
				a, err := at(*n.Ask)
				if err != nil {
					return nil, err
				}
				note.ask = a
			}
			link.notes = append(link.notes, note)
		}
		if l.Transcript != nil {
			link.transcriptPath = *l.Transcript
		}
		call.Link = link
	}
	return call, nil
}
