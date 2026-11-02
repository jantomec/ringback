package main

// The agent's session side: the calls that sessions' ringback mods place, and what passes
// between a call and its session.
//
// Each Mac's sessions reach their agent over a Unix socket that only you can open (see
// "Voice server's session side" in docs/design.md, and api.go). For each briefing it makes a
// random call ID, the call's secret, and asks the hub to ring the phone with it. The ID
// reaches the phone only through Apple's push, and the phone's offer must carry it. Haiku
// starts on the call's opening as soon as the hub takes the call, so it's written while the
// phone rings. The phone can make its offer while it rings, and says when you've answered.
//
// During the call, what the voice passes to the session waits here until the session's mod
// collects it. When the session has finished with it, the mod brings its answer back as
// news: the voice tells you if you're still on the call, and otherwise the session calls
// again.
//
// The session also learns how each call went: when it ends, or when it isn't answered in
// time, its outcome waits here for the mod too, as a note the session reads rather than a
// prompt.
//
// Outside your work hours (set in the app) the hub holds a call instead of ringing, and says
// when the hours start. The call is kept here meanwhile, without Haiku, and the hub is asked
// again then, or within reaskAfter, so hours you widen apply soon. A new briefing from its
// session replaces the held call's, and what the session asks goes to it, a question too: it
// waits for the call, and the hub is asked again at once, so the phone gets a notification.
// From the app's waiting calls, you can send a held call's question to the session's own
// dialog instead, which Remote Control (turned on with ringback) shows in the Claude app:
// the call is then forgotten.
//
// When the session needs your permission, or asks you questions, its mod holds the tool call
// and asks here (an Ask). The call you're on with that session gets it as a note; otherwise a
// call is placed to ask it. The mod waits for the answer the voice gives, and shows its own
// prompt if you hang up without one. If you don't answer the call, what it asked waits for
// the next call with that session you answer: a call back, or its next call.
//
// Any call can be called back from the phone's call history, answered or not. Each call is
// remembered for a day after it's forgotten. The call back is a new call for the same
// session, with a fresh Haiku, answered at once: its voice has the earlier call's briefing,
// with a lead that says whether you talked then, and the session's latest one if it wrote a
// newer one since, for a later call or a held one. Work hours don't apply to it: you called.
// A held call can be called from the app too, where it shows as waiting: it's released at
// once, without ringing, and its voice picks up as a call back's does.
//
// The sessions that listen (ringback on) are the phone's Contacts: the hub hears which they
// are, with each one's project and latest briefing, whenever that changes and every
// publishEvery. You can call one from the app at any time: a new call under the ID the phone
// made, answered at once, whose voice has the session's latest briefing, if it has one, with
// a lead that says you called.
//
// What it keeps is written to a file beside its socket on every change, and read again at
// start, so a restart loses nothing that matters: held calls are held again, calls are still
// remembered for calling back, and what sessions asked keeps its ids, so a mod's long poll
// carries on. A call that was ringing or on during the restart has ended: its session is told
// so, and what it asked waits as a missed call's does.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	// stateFile is what the agent keeps across a restart, in the folder of its socket, so a
	// check with a socket of its own has its own.
	stateFile = "voice-state.json"
	// unanswered is how long a placed call waits to be answered before the session is told it
	// wasn't and the call is forgotten. The phone rings for 30 s, and its push can take about
	// 16 s to arrive after it leaves Wi-Fi. RINGBACK_UNANSWERED overrides it, for checks.
	unanswered = envSeconds("RINGBACK_UNANSWERED", 90)
	// reaskAfter is the longest a held call waits before the hub is asked again, so hours you
	// widen apply within this long. If it can't be reached, it's asked again after retryAfter.
	// RINGBACK_REASK overrides it, for checks.
	reaskAfter = envSeconds("RINGBACK_REASK", 600)
	retryAfter = min(time.Minute, reaskAfter)
	// tick is the longest a held call sleeps before it looks at the clock again. Go's timers
	// stop while the Mac sleeps, so the wait is on the wall clock, looked at this often.
	tick = 30 * time.Second
	// keepFor is how long what a session asks is kept after it's answered, so its mod can still
	// read the answer, then forgotten.
	keepFor = 600 * time.Second
	// rememberFor is how long a forgotten call is remembered, so you can call it back: a day.
	rememberFor = 24 * 3600.0
	// listeningFor: a session whose mod hasn't collected its messages for this long isn't
	// listening: ringback is off in it, or it has ended. While it's on, the mod collects once
	// a second. RINGBACK_LISTENING overrides it, for checks.
	listeningFor = envSeconds("RINGBACK_LISTENING", 30)
	// publishEvery is how often the hub hears which sessions listen, for the phone's Contacts,
	// when nothing changed. A change goes at once, or changedRetry after a try that failed.
	// RINGBACK_PUBLISH overrides it.
	publishEvery = envSeconds("RINGBACK_PUBLISH", 30)
	changedRetry = 5 * time.Second
	// caffeinate keeps the Mac awake while calls wait; empty off macOS.
	caffeinate = func() string {
		if runtime.GOOS == "darwin" {
			return "caffeinate"
		}
		return ""
	}()
)

const (
	// onHold is a held call's heldUntil while your phone holds calls with no time to stop by
	// itself (paused in the app, without work hours to take calls again): it rings when you
	// take calls.
	onHold = "on hold"
	// The longest tool input and context the voice gets: the mod sends at most about this much.
	inputMost   = 2000
	contextMost = 1500
)

// Link is what a call's voice can do with its session: pass it messages, hear its news, and
// answer what it asks. Its fields are guarded by the Calls' mu, which the voice takes.
type Link struct {
	calls      *Calls
	session    string
	transcript *Transcript
	// Its transcript's path in the project, read from the state file after a restart.
	transcriptPath string
	// False on a call back to a session that isn't listening any more: the voice can't pass
	// it anything or answer what it asked.
	listening bool
	// Set by the call's voice: tells it a note from the session, when it's idle.
	notify  func(text string)
	asks    ordered[*Ask] // what the session asks you on this call, until answered
	briefed []*Ask        // what the call's briefing asks, placed to ask it
	// Notes kept until the voice has started talking: one appended before the opening is
	// spoken would make Haiku answer it instead.
	notes []linkNote
	open  bool
}

type linkNote struct {
	ask  *Ask
	text string
}

func (l *Link) send(text string) {
	var transcript *string
	if l.transcript != nil {
		transcript = &l.transcript.Relative
	}
	l.calls.sendLocked(l.session, text, transcript, "message")
	if l.transcript != nil {
		l.transcript.add("Passed to the session", text)
	}
}

func (l *Link) news(text string) {
	if l.transcript != nil {
		l.transcript.add("The session's answer", text)
	}
	l.tell("The session: "+text, nil)
}

// listen is the call's voice, from an offer on: notes wait until it has started talking.
func (l *Link) listen(notify func(string)) {
	l.notify = notify
	l.open = false
}

// opened says the voice has started talking: the notes kept for it go.
func (l *Link) opened() {
	if l.open {
		return
	}
	l.open = true
	notes := l.notes
	l.notes = nil
	for _, n := range notes {
		l.tell(n.text, n.ask)
	}
}

// tell tells the voice a note, or keeps it until the voice has started talking.
func (l *Link) tell(text string, ask *Ask) {
	if !l.open || l.notify == nil {
		l.notes = append(l.notes, linkNote{ask, text})
		return
	}
	if ask != nil && l.transcript != nil {
		l.transcript.add("The session asks", ask.details())
	}
	l.notify(text)
}

// ask puts what the session asks to this call's voice: in a note, unless the call was placed
// to ask it, so its briefing has it.
func (l *Link) ask(a *Ask, briefed bool) {
	l.asks.set(a.ID, a)
	if briefed {
		l.briefed = append(l.briefed, a)
	} else {
		l.tell(a.note(), a)
	}
}

// rebriefed: the call has a new briefing, which doesn't mention what the old one asked, so
// the asks still waiting for it go to the voice as notes instead.
func (l *Link) rebriefed() {
	briefed := l.briefed
	l.briefed = nil
	// Notes about what no longer waits are old news by now.
	kept := []linkNote{}
	for _, n := range l.notes {
		if a, ok := l.asks.get(nameOf(n.ask)); n.ask == nil || (ok && a == n.ask) {
			kept = append(kept, n)
		}
	}
	l.notes = kept
	for _, a := range briefed {
		if got, ok := l.asks.get(a.ID); ok && got == a {
			l.tell(a.note(), a)
		}
	}
}

func nameOf(a *Ask) string {
	if a == nil {
		return ""
	}
	return a.ID
}

// briefedNames is what the call's briefing asks, by name ("permission abcd").
func (l *Link) briefedNames() []string {
	names := []string{}
	for _, a := range l.briefed {
		names = append(names, a.name())
	}
	return names
}

// answer gives the session your answer, once you've heard the voice say it; false if it no
// longer waits for one.
func (l *Link) answer(id, outcome, said string, answers pairs) bool {
	a, ok := l.asks.pop(id)
	if !ok || !a.settle(outcome, &said, answers) {
		return false
	}
	if l.transcript != nil {
		l.transcript.add("Answered the session", a.answered())
	}
	return true
}

// drop takes back what the session asked, which no longer waits for this call: withdrawn, or
// answered elsewhere. The voice is told, if it was told of it.
func (l *Link) drop(a *Ask, why string) {
	if _, ok := l.asks.pop(a.ID); !ok {
		return
	}
	kept := []linkNote{}
	for _, n := range l.notes {
		if n.ask != a {
			kept = append(kept, n)
		}
	}
	if len(kept) == len(l.notes) {
		l.tell(fmt.Sprintf("The session no longer needs an answer to %s: %s. Don't put it to the developer "+
			"any more; if you were asking it, say so briefly.", a.name(), why), nil)
	}
	l.notes = kept
	if l.transcript != nil {
		l.transcript.add("No longer asked", a.name()+": "+why)
	}
}

// hasQuestion says whether the session asks questions on this call and waits for the answer.
func (l *Link) hasQuestion() bool {
	for _, a := range l.asks.list() {
		if a.Kind == "question" {
			return true
		}
	}
	return false
}

// Ask is a permission or questions a session asks you. Its mod holds the tool call meanwhile,
// and shows its own prompt if the call brings no answer.
type Ask struct {
	ID      string // short and safe to speak; the voice answers with it
	Kind    string // "permission" or "question"
	Session string
	Request Request // as the mod sent it, checked
	// allow, deny or answered; none if you hung up without one; terminal if you sent the held
	// call's question to the Claude app, so the session asks in its own dialog or prompt;
	// withdrawn; "" while it waits
	Outcome string
	Said    *string // your words, as the voice heard them
	Answers pairs   // each question's answer
	settled chan struct{}
}

// Request is a permission request (Tool, Description, Input) or questions, as a mod sends
// them, with what the session was doing and where.
type Request struct {
	Tool        string     `json:"tool,omitempty"`
	Description string     `json:"description,omitempty"`
	Input       string     `json:"input,omitempty"`
	Questions   []Question `json:"questions,omitempty"`
	Context     string     `json:"context"`
	Project     string     `json:"project"`
	Cwd         string     `json:"cwd"`
	Subject     string     `json:"subject"`
}

type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect"`
}

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

func newAsk(id, kind, session string, request Request) *Ask {
	return &Ask{ID: id, Kind: kind, Session: session, Request: request, settled: make(chan struct{})}
}

func (a *Ask) name() string { return a.Kind + " " + a.ID }

// settle records its outcome, once; guarded by the Calls' mu.
func (a *Ask) settle(outcome string, said *string, answers pairs) bool {
	if a.Outcome != "" {
		return false
	}
	a.Outcome, a.Said, a.Answers = outcome, said, answers
	close(a.settled)
	return true
}

// details is what it asks, for the voice and the transcript.
func (a *Ask) details() string {
	r := a.Request
	if a.Kind == "permission" {
		text := fmt.Sprintf("Permission %s: the session wants to use its tool %s.", a.ID, r.Tool)
		if d := strings.TrimSpace(r.Description); d != "" {
			text += " " + d
		}
		if in := strings.TrimSpace(r.Input); in != "" {
			text += "\nIts input:\n" + in
		}
		return text
	}
	lines := []string{fmt.Sprintf("Question %s, with %d question(s) for the developer:", a.ID, len(r.Questions))}
	for n, q := range r.Questions {
		line := fmt.Sprintf("%d. %s", n+1, strings.TrimSpace(q.Question))
		if q.Header != "" {
			line += " (" + strings.TrimSpace(q.Header) + ")"
		}
		if len(q.Options) > 0 {
			options := make([]string, len(q.Options))
			for i, o := range q.Options {
				options[i] = `"` + o.Label + `"`
				if o.Description != "" {
					options[i] += ": " + o.Description
				}
			}
			line += " Options: " + strings.Join(options, "; ") + "."
			if q.MultiSelect {
				line += " They can choose more than one."
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (a *Ask) context() string {
	if c := strings.TrimSpace(a.Request.Context); c != "" {
		return "\nWhat the session was doing, in its own last words:\n" + c
	}
	return ""
}

// note is the note that puts it to a call's voice.
func (a *Ask) note() string {
	how := "answer_question, once they've clearly chosen or answered"
	if a.Kind == "permission" {
		how = "answer_permission, once they've clearly said yes or no"
	}
	return "The session asks, and waits for the answer.\n" + a.details() + a.context() +
		"\nPut it to the developer, then answer with " + how + "."
}

// briefing is the briefing of a call placed to ask it.
func (a *Ask) briefing() string {
	what := "has questions for the developer"
	if a.Kind == "permission" {
		what = "needs the developer's permission"
	}
	return "The session has stopped because it " + what + ", and this call is about that. Open the call by " +
		"asking it.\n\n" + a.details() + "\n" + a.context()
}

// answered is the answer, for the transcript and the log.
func (a *Ask) answered() string {
	said := ""
	if a.Said != nil && *a.Said != "" {
		said = ` (they said "` + *a.Said + `")`
	}
	if a.Kind == "permission" {
		return a.name() + ": " + a.Outcome + said
	}
	parts := make([]string, len(a.Answers))
	for i, p := range a.Answers {
		parts[i] = p.Key + ` "` + p.Value + `"`
	}
	answers := strings.Join(parts, "; ")
	if answers == "" {
		answers = a.Outcome
	}
	return a.name() + ": " + answers + said
}

// match is the voice's answers as {question: answer}, with an option's label where the
// answer is one (labels joined by ", " where several can be chosen), and the questions left
// unanswered. Answers are matched to the questions by their text, or else in order.
func (a *Ask) match(given any) (pairs, []string) {
	type givenPair struct{ question, answer string }
	var got []givenPair
	list, _ := given.([]any)
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			got = append(got, givenPair{pyStr(m, "question"), strings.TrimSpace(pyStr(m, "answer"))})
		}
	}
	questions := a.Request.Questions
	answers, missing := pairs{}, []string{}
	for n, q := range questions {
		var answer *string
		for _, g := range got {
			if k := plain(g.question); k == plain(q.Question) || k == plain(q.Header) {
				answer = &g.answer
				break
			}
		}
		if answer == nil && len(got) == len(questions) {
			answer = &got[n].answer
		}
		if answer == nil || *answer == "" {
			missing = append(missing, q.Question)
			continue
		}
		text := *answer
		labels := map[string]string{}
		for _, o := range q.Options {
			labels[plain(o.Label)] = o.Label
		}
		chosen := []string{plain(text)}
		if q.MultiSelect {
			chosen = nil
			for _, part := range strings.Split(text, ",") {
				chosen = append(chosen, plain(part))
			}
		}
		all := true
		for _, c := range chosen {
			if _, ok := labels[c]; !ok {
				all = false
			}
		}
		if all {
			picked := make([]string, len(chosen))
			for i, c := range chosen {
				picked[i] = labels[c]
			}
			text = strings.Join(picked, ", ")
		}
		answers = append(answers, pair{q.Question, text})
	}
	return answers, missing
}

// pyStr is str(m.get(key, "")), as Python writes what Haiku gave.
func pyStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return pyrepr(v)
}

// Call is a call placed or made, from when it's placed until it's forgotten; a copy of it is
// remembered for a day after, for calling back.
type Call struct {
	ID       string
	Session  string
	Project  string
	Cwd      string // the session's working directory, where the transcript goes
	Subject  string
	Briefing string
	Placed   time.Time // when the phone started ringing
	// When you answered; zero until you do.
	AnsweredAt time.Time
	// Once it's ended: how long you talked, from when its audio connected or you answered,
	// whichever came last; nil if its audio never connected.
	Lasted *float64
	Link   *Link // to its session, made when it's placed
	// Haiku, writing the opening while the phone rings, for the call's voice; closed when the
	// call ends or is forgotten.
	Haiku    *Haiku
	deadline context.CancelFunc // forgets it if it isn't answered
	// Outside your work hours: when the hub said the call can ring (ISO 8601, with an
	// offset), until it rings; onHold with no time.
	HeldUntil string
	holding   context.CancelFunc // asks the hub again
	// Signalled while held, to ask the hub again at once: a question joined the call.
	reask chan struct{}
	// When it was placed, or its briefing replaced while it was held, as Unix seconds, for how
	// long ago.
	At float64
	// Once it's forgotten and remembered: what its briefing asks, by name ("permission abcd").
	Asked []string
	// On a call you made: the call you called, as it was placed. A call back calls back an
	// earlier call; with Now, you called a held call from the app while it waited; with
	// Contact, you called the session from the app's Contacts, and it's the session's latest
	// briefing, as the call will be remembered.
	Back    *Call
	Now     bool
	Contact bool
	// Once it's forgotten and remembered: when you last talked on it, a call back of it
	// included (Unix seconds); nil if you never answered it.
	Talked *float64
}

func (c *Call) answered() bool { return !c.AnsweredAt.IsZero() }

func (c *Call) signalReask() {
	select {
	case c.reask <- struct{}{}:
	default:
	}
}

// Message is what waits for a session until its mod collects it.
type Message struct {
	Kind       string  `json:"kind"` // "message" from the voice, "outcome" for how a call went
	Text       string  `json:"text"`
	Transcript *string `json:"transcript"`
}

type pastCall struct {
	call *Call
	at   float64 // when it was forgotten
}

// placer is the hub, as far as calls go: it rings the phone or holds the call, and hears
// which sessions listen.
type placer interface {
	request(ctx context.Context, message map[string]any) (map[string]any, error)
	send(message map[string]any) error
	address() string
}

// Calls are the calls placed and not yet ended, and what waits for each session. The Mac
// stays awake while there are calls, held ones too, so a call held overnight can ring in the
// morning. With a state file, what it keeps is written there on every change, and read again
// by restore.
type Calls struct {
	mu      sync.Mutex
	hub     placer
	log     func(string)
	state   string // the state file, if any
	written string // what the state file holds
	calls   ordered[*Call]
	waiting map[string][]Message // messages for each session
	asks    ordered[*Ask]        // what sessions ask you, by its id
	// What each session asked on calls you didn't answer, by id, until you answer one of its
	// calls or it's withdrawn or answered elsewhere.
	pool map[string]*ordered[*Ask]
	past ordered[pastCall]  // forgotten calls, and when, for a day
	seen ordered[time.Time] // when each session's mod last collected
	// Since when each session listens (Unix seconds), and its project and folder, as its mod
	// says them when it collects: the phone's Contacts.
	since   map[string]float64
	named   map[string][2]string
	asking  sync.Mutex
	awake   *exec.Cmd
	closing sync.WaitGroup // Haikus of forgotten calls, while they close
	// What changed for the hub's list of sessions, so it hears at once.
	changed chan struct{}
}

func newCalls(hub placer, log func(string), state string) *Calls {
	return &Calls{
		hub: hub, log: log, state: state, waiting: map[string][]Message{}, pool: map[string]*ordered[*Ask]{},
		since: map[string]float64{}, named: map[string][2]string{}, changed: make(chan struct{}, 1),
	}
}

func (c *Calls) logCall(call *Call, line string) { c.log("call " + prefix(call.ID, 8) + ": " + line) }

// place asks the hub to ring the phone. Outside your work hours the hub holds the call (its
// HeldUntil), and a new briefing for a session whose call is held goes to that call instead
// of placing another. A call placed to ask something has it in its briefing (ask), so the
// hub knows from the start whether it has a question.
func (c *Calls) place(session, project, cwd, subject, briefing string, ask *Ask) (*Call, error) {
	c.mu.Lock()
	for _, held := range c.calls.list() {
		if held.Session == session && held.HeldUntil != "" {
			held.Project, held.Cwd, held.Subject, held.Briefing = project, cwd, subject, briefing
			held.At = unixNow()
			held.Link.rebriefed()
			c.logCall(held, fmt.Sprintf("still held %s, with a new briefing: %s", until(held.HeldUntil), subject))
			c.save()
			c.mu.Unlock()
			return held, nil
		}
	}
	call := &Call{
		ID: token(), Session: session, Project: project, Cwd: cwd, Subject: subject, Briefing: briefing,
		Placed: time.Now(), At: unixNow(), Asked: []string{}, reask: make(chan struct{}, 1),
	}
	call.Link = c.newLink(call)
	if ask != nil {
		call.Link.ask(ask, true)
	}
	question := call.Link.hasQuestion()
	c.mu.Unlock()
	heldUntil, err := c.askHub(call, question)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls.set(call.ID, call)
	if heldUntil != "" {
		call.HeldUntil = heldUntil
		c.logCall(call, fmt.Sprintf("held %s for %s (%s): %s", until(heldUntil), project, prefix(session, 8), subject))
		c.startHolding(call)
		c.save()
	} else {
		c.logCall(call, fmt.Sprintf("placed for %s (%s): %s", project, prefix(session, 8), subject))
		c.ring(call)
	}
	c.stayAwake()
	return call, nil
}

// newLink is a call's link to its session. The transcript joins it when you answer.
func (c *Calls) newLink(call *Call) *Link {
	return &Link{calls: c, session: call.Session, listening: true}
}

// askHub asks the hub to ring the phone for a call: "" if it rings, or when it can ring if it
// holds the call while your phone holds calls (outside your work hours, or paused in the
// app); onHold if it holds it with no time. An error if it can't be reached or refuses. It
// hears whether the call has a question, so it can tell the phone of a held one, and the
// session's ID, which the push takes to the phone, so the app can call the session from
// Contacts once the agent has forgotten the call.
func (c *Calls) askHub(call *Call, question bool) (string, error) {
	c.mu.Lock()
	message := map[string]any{
		"type": "place", "call_id": call.ID, "project": call.Project, "subject": call.Subject,
		"session_id": call.Session, "question": question,
	}
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reply, err := c.hub.request(ctx, message)
	if err != nil {
		return "", fmt.Errorf("can't reach the hub at %s: %v", c.hub.address(), err)
	}
	if ok, _ := reply["ok"].(bool); !ok {
		status, _ := reply["status"].(float64)
		text := fmt.Sprintf("the hub answered %d", int(status))
		if why, _ := reply["error"].(string); why != "" {
			text += ": " + why
		}
		return "", errors.New(text)
	}
	if heldUntil, _ := reply["held_until"].(string); heldUntil != "" {
		return heldUntil, nil
	}
	if held, _ := reply["held"].(bool); held {
		return onHold, nil
	}
	return "", nil
}

// ring: the phone rings, Haiku thinks of what to say meanwhile, so the voice can start as
// soon as you answer, and the call is forgotten if you don't within unanswered.
func (c *Calls) ring(call *Call) {
	call.Placed = time.Now()
	haiku := newHaiku(instructions(call.Briefing, true, true), func(line string) { c.logCall(call, line) }, true, opening)
	if err := haiku.Start(); err != nil {
		c.logCall(call, fmt.Sprintf("Haiku didn't start (%v); the call starts its own", err))
		c.closeLater(haiku)
	} else {
		call.Haiku = haiku
	}
	c.startDeadline(call)
	c.save()
}

func (c *Calls) startDeadline(call *Call) {
	ctx, cancel := context.WithCancel(context.Background())
	call.deadline = cancel
	go c.deadline(ctx, call)
}

func (c *Calls) startHolding(call *Call) {
	ctx, cancel := context.WithCancel(context.Background())
	call.holding = cancel
	go c.hold(ctx, call)
}

// hold asks the hub again about a held call when it said the call can ring, or after
// reaskAfter if that's sooner, until it rings; retryAfter after a try that fails; and at once
// when it has news for the hub, or you take calls on the phone (reask). The wait is on the
// wall clock, which goes on while the Mac sleeps.
func (c *Calls) hold(ctx context.Context, call *Call) {
	c.mu.Lock()
	due := wake(call.HeldUntil)
	c.mu.Unlock()
	for {
		for {
			left := due - unixNow()
			if left <= 0 {
				break
			}
			wait := min(time.Duration(left*float64(time.Second)), tick)
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-call.reask:
				timer.Stop()
				left = 0
			case <-timer.C:
			}
			if left == 0 {
				break
			}
		}
		select {
		case <-call.reask:
		default:
		}
		c.mu.Lock()
		question := call.Link.hasQuestion()
		c.mu.Unlock()
		heldUntil, err := c.askHub(call, question)
		c.mu.Lock()
		if ctx.Err() != nil {
			c.mu.Unlock()
			return
		}
		if err != nil {
			c.logCall(call, fmt.Sprintf("still held, %v; asking again in %.0f s", err, retryAfter.Seconds()))
			due = unixNow() + retryAfter.Seconds()
			c.mu.Unlock()
			continue
		}
		if heldUntil == "" {
			break
		}
		if heldUntil != call.HeldUntil {
			c.logCall(call, "now held "+until(heldUntil))
		}
		call.HeldUntil = heldUntil
		c.save()
		due = wake(heldUntil)
		c.mu.Unlock()
	}
	defer c.mu.Unlock()
	call.HeldUntil = ""
	call.holding = nil
	c.logCall(call, fmt.Sprintf("placed for %s (%s), held until now: %s", call.Project, prefix(call.Session, 8), call.Subject))
	c.ring(call)
}

// reask: you took calls on the phone, so the held calls among ids ask the hub again now,
// rather than when it said or within reaskAfter. How many did.
func (c *Calls) reask(ids []string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, id := range ids {
		if call, ok := c.calls.get(id); ok && call.HeldUntil != "" && call.holding != nil {
			c.logCall(call, "you take calls, asking the hub again")
			call.signalReask()
			n++
		}
	}
	return n
}

// offered is the call an offer is for, if it's one this agent placed. The offer can come
// while the phone still rings, and a call you haven't answered stays placed until it's
// forgotten, so the phone can offer again. A held one is refused (hub.go), unless the offer
// calls it now.
func (c *Calls) offered(id string) *Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	call, _ := c.calls.get(id)
	return call
}

// back is the call you make by calling id back from the phone, which it answers at once: a
// call this agent placed or you made, answered or not, remembered for a day after it was
// forgotten, or a call you didn't answer that's still placed. Nil if there's no such call. A
// call back already on is given as it is, so a new offer replaces the earlier one, as for any
// call.
func (c *Calls) back(id string) *Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	var missed *Call
	if call, ok := c.calls.get(id); ok {
		if call.HeldUntil != "" {
			return nil // held outside your work hours, it hasn't rung, so the phone can't know its ID
		}
		if call.Back != nil || call.answered() {
			return call
		}
		// Still placed: the call back takes its place, so it's never told it wasn't
		// answered, and what it asked waits for the call back.
		c.calls.pop(call.ID)
		if call.deadline != nil {
			call.deadline()
		}
		c.closeHaiku(call)
		missed = remembered(call)
		pool := c.poolOf(call.Session)
		for _, a := range call.Link.asks.list() {
			pool.set(a.ID, a)
		}
		call.Link.asks.clear()
	} else if past, ok := c.past.pop(id); ok {
		missed = past.call
	} else {
		return nil
	}
	listening := c.listening(missed.Session)
	// The session's latest briefing, if it wrote one after this call's.
	var later *since
	if latest := c.latest(missed.Session); latest != nil && latest.At > missed.At {
		later = &since{latest.Briefing, unixNow() - latest.At}
	}
	// What the earlier call's briefing asks and no longer waits for an answer; a session that
	// isn't listening waits for none.
	waiting := map[string]bool{}
	if listening {
		if pool, ok := c.pool[missed.Session]; ok {
			for _, a := range pool.list() {
				waiting[a.name()] = true
			}
		}
	}
	settled := []string{}
	for _, name := range missed.Asked {
		if !waiting[name] {
			settled = append(settled, name)
		}
	}
	// You talked on it, or on a call back of it: the lead says so, rather than that you
	// didn't answer.
	var talked *float64
	if missed.Talked != nil {
		t := unixNow() - *missed.Talked
		talked = &t
	}
	call := *missed
	call.Briefing = calledBack(missed.Briefing, unixNow()-missed.At, later, settled, talked)
	call.Placed, call.Back, call.Contact = time.Now(), missed, false
	call.Link = c.newLink(&call)
	call.Link.listening = listening
	c.calls.set(call.ID, &call)
	how := "the session isn't listening"
	if listening {
		how = "the session listens"
	}
	if talked != nil {
		how += fmt.Sprintf(", answered %.0f min ago", *talked/60)
	}
	if later != nil {
		how += ", with a newer briefing since"
	}
	c.logCall(&call, fmt.Sprintf("called back about %s, %s", pyrepr(call.Subject), how))
	c.pickUp(&call)
	return &call
}

// now is the call you make by calling a waiting call from the phone, which it answers at once:
// a call held outside your work hours is released without ringing, and isn't asked about at
// the hub any more. Its voice has the briefing with a lead that says you called. A call that's
// no longer held but still placed (it rang meanwhile) is given as it is, for an ordinary offer.
// Nil if there's no such call.
func (c *Calls) now(id string) *Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	held, ok := c.calls.get(id)
	if !ok || held.HeldUntil == "" {
		return held
	}
	if held.holding != nil {
		held.holding()
	}
	// The held call's link goes with it: what it asks, and what its briefing asks.
	call := *held
	call.Briefing = calledNowBriefing(held.Briefing)
	call.Placed, call.HeldUntil, call.holding = time.Now(), "", nil
	call.Back, call.Now = remembered(held), true
	c.calls.set(call.ID, &call)
	c.logCall(&call, fmt.Sprintf("called now from the app, held %s: %s", until(held.HeldUntil), pyrepr(call.Subject)))
	c.pickUp(&call)
	return &call
}

// contact is the call you make by calling a session from the phone's Contacts, which it
// answers at once, under the ID the phone made for it. Its voice has the session's latest
// briefing, if it has one, with a lead that says you called; it talks with the session as an
// answered call does. Nil if the session isn't listening: the phone lists only sessions that
// do, but its list can be a little old. A contact call already on is given as it is, so a new
// offer replaces the earlier one.
func (c *Calls) contact(session, id string) *Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	if call, ok := c.calls.get(id); ok {
		if call.Contact && call.Session == session {
			return call
		}
		return nil
	}
	if _, ok := c.past.get(id); ok || !c.listening(session) {
		return nil
	}
	latest := c.latest(session)
	named := c.named[session]
	project, cwd := named[0], named[1]
	if latest != nil {
		project, cwd = or(project, latest.Project), or(cwd, latest.Cwd)
	}
	// What the call will be remembered as, for a call back: the latest briefing, as of now.
	plainCall := &Call{
		ID: id, Session: session, Project: or(project, "Claude"), Cwd: cwd, Placed: time.Now(), At: unixNow(),
		Asked: []string{}, Contact: true, reask: make(chan struct{}, 1),
	}
	var briefing string
	var heard *float64
	if latest != nil {
		plainCall.Subject, plainCall.Briefing, plainCall.At, plainCall.Asked = latest.Subject, latest.Briefing, latest.At, latest.Asked
		briefing = latest.Briefing
		if latest.Talked != nil {
			h := unixNow() - *latest.Talked
			heard = &h
		}
	}
	call := *plainCall
	call.Briefing = calledContactBriefing(briefing, unixNow()-plainCall.At, heard)
	call.Back = plainCall
	call.Link = c.newLink(&call)
	c.calls.set(call.ID, &call)
	about := "no briefing yet"
	if latest != nil {
		about = "its latest briefing " + pyrepr(latest.Subject)
	}
	c.logCall(&call, fmt.Sprintf("called from Contacts, %s (%s), with %s", call.Project, prefix(session, 8), about))
	c.pickUp(&call)
	return &call
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// latest is the call with a session's latest briefing: placed, held or remembered. A call you
// made counts as the call it calls; a call from Contacts has no briefing of its own.
func (c *Calls) latest(session string) *Call {
	var mine []*Call
	for _, call := range c.calls.list() {
		if call.Session == session {
			if call.Back != nil {
				mine = append(mine, call.Back)
			} else {
				mine = append(mine, call)
			}
		}
	}
	for _, p := range c.past.list() {
		if p.call.Session == session {
			mine = append(mine, p.call)
		}
	}
	var best *Call
	for _, call := range mine {
		if !call.Contact && (best == nil || call.At > best.At) {
			best = call
		}
	}
	return best
}

// contacts are the sessions that listen now, as the phone's Contacts list them: each one's ID,
// project, since when it listens here, and the subject of its latest briefing and when the
// session wrote it, if it has one. A session whose mod didn't say its project (a mod from
// before Contacts) goes by its latest call's, or isn't listed.
func (c *Calls) contacts() []map[string]any {
	listed := []map[string]any{}
	for _, session := range c.seen.keys {
		if !c.listening(session) {
			continue
		}
		latest := c.latest(session)
		project := c.named[session][0]
		if project == "" && latest != nil {
			project = latest.Project
		}
		if project == "" {
			continue
		}
		at, ok := c.since[session]
		if !ok {
			at = unixNow()
		}
		entry := map[string]any{"session_id": session, "project": project, "since": stamp(at)}
		if latest != nil {
			entry["subject"], entry["briefed_at"] = latest.Subject, stamp(latest.At)
		}
		listed = append(listed, entry)
	}
	sort.SliceStable(listed, func(i, j int) bool {
		a, b := strings.ToLower(listed[i]["project"].(string)), strings.ToLower(listed[j]["project"].(string))
		if a != b {
			return a < b
		}
		return listed[i]["since"].(string) < listed[j]["since"].(string)
	})
	return listed
}

// publish tells the hub which of this Mac's sessions listen, for the phone's Contacts: when
// that changes, and every publishEvery, so the hub can drop a Mac's sessions once it stops
// hearing from it. Runs as long as the agent does; resend makes it tell the hub again at
// once, as when its link has come back.
func (c *Calls) publish(ctx context.Context, resend <-chan struct{}) {
	sent := ""
	var tried, again time.Time
	failing := false
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		listed := c.contacts()
		c.mu.Unlock()
		data, _ := json.Marshal(listed)
		now := time.Now()
		if (string(data) != sent && !now.Before(again)) || now.Sub(tried) >= publishEvery {
			tried = now
			if err := c.hub.send(map[string]any{"type": "sessions", "sessions": listed}); err != nil {
				again = now.Add(changedRetry)
				// Unlinked, the link says so itself, and they go when it's back.
				if !errors.Is(err, errNotLinked) {
					if !failing {
						c.log("can't tell the hub which sessions listen: " + err.Error())
					}
					failing = true
				}
			} else {
				if string(data) != sent {
					names := []string{}
					for _, e := range listed {
						names = append(names, fmt.Sprintf("%s (%s)", e["project"], prefix(e["session_id"].(string), 8)))
					}
					c.log("sessions listening, told the hub: " + or(strings.Join(names, ", "), "none"))
				}
				sent, failing = string(data), false
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-resend:
			sent, tried, again = "\x00", time.Time{}, time.Time{}
		case <-c.changed:
		case <-ticker.C:
		}
	}
}

// toClaudeApp sends a held call's question to the session's own dialog, which Remote
// Control shows in the Claude app, as you asked from the app's waiting calls. What the call
// asks is settled as terminal: the mod lets the dialog show, and a permission's prompt, which
// shows anyway, stays. The call is forgotten: it never rings, isn't asked about at the hub
// any more, and can't be called back. Its session is told. Returns why it can't, if it can't:
// the call doesn't wait here ("not waiting", as when it rang meanwhile), its session isn't
// listening ("not listening"), or it has no question ("no question").
func (c *Calls) toClaudeApp(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	call, ok := c.calls.get(id)
	if !ok || call.HeldUntil == "" {
		return "not waiting"
	}
	if !c.listening(call.Session) {
		return "not listening"
	}
	if !call.Link.hasQuestion() {
		return "no question"
	}
	c.calls.pop(call.ID)
	if call.holding != nil {
		call.holding()
	}
	asked := call.Link.asks.list()
	call.Link.asks.clear()
	names := make([]string, len(asked))
	for i, a := range asked {
		a.settle("terminal", nil, nil)
		names[i] = a.name()
	}
	c.logCall(call, strings.Join(names, ", ")+" sent to the Claude app from the phone, and the call forgotten")
	about := "The held call"
	if call.Subject != "" {
		about = `The call about "` + call.Subject + `"`
	}
	c.sendLocked(call.Session, about+" won't ring: the developer chose on the phone to answer its question in the "+
		"Claude app instead, with Remote Control.", nil, "outcome")
	c.stayAwake()
	return ""
}

// listening says whether a session's mod collected what waits for it within listeningFor:
// ringback is on in it, and it hasn't ended.
func (c *Calls) listening(session string) bool {
	seen, ok := c.seen.get(session)
	return ok && time.Since(seen) < listeningFor
}

// pickUp: a call you made is answered at once. Haiku writes the opening of a call you made,
// and it's forgotten if its audio doesn't come within unanswered.
func (c *Calls) pickUp(call *Call) {
	listening := call.Link.listening
	haiku := newHaiku(instructions(call.Briefing, true, listening), func(line string) { c.logCall(call, line) }, listening, pickedUp)
	if err := haiku.Start(); err != nil {
		c.logCall(call, fmt.Sprintf("Haiku didn't start (%v); the call starts its own", err))
		c.closeLater(haiku)
	} else {
		call.Haiku = haiku
	}
	c.startDeadline(call)
	c.save()
	c.stayAwake()
}

// markAnswered marks an offered call you've answered, once, and starts its transcript, so a
// call you don't answer leaves none. What its session asked on calls you didn't answer goes
// to this one.
func (c *Calls) markAnswered(id string) *Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	call, ok := c.calls.get(id)
	if !ok || call.answered() {
		return call
	}
	call.AnsweredAt = time.Now()
	if call.Cwd != "" && call.Link != nil {
		t, err := newTranscript(call.Cwd, call.Subject, call.Briefing)
		if err != nil {
			c.logCall(call, fmt.Sprintf("no transcript (%v)", err))
		} else {
			call.Link.transcript = t
		}
	}
	if call.Link != nil && call.Link.listening {
		if pool, ok := c.pool[call.Session]; ok {
			delete(c.pool, call.Session)
			for _, a := range pool.list() {
				// In a note, unless the briefing asks it: a call back's can.
				call.Link.ask(a, contains(call.Asked, a.name()))
				c.logCall(call, a.name()+", asked on a call you didn't answer, goes to this one")
			}
		}
	}
	c.save()
	return call
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// end: a call you answered has ended. You talked for lasted seconds, or nil if its audio
// never connected.
func (c *Calls) end(id string, lasted *float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if call, ok := c.calls.get(id); ok {
		call.Lasted = lasted
		c.forget(call)
	}
}

// send keeps a message for a session until its mod collects it: what the voice passed on
// ("message"), or how a call went ("outcome"), which the session reads as a note.
func (c *Calls) send(session, text string, transcript *string, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendLocked(session, text, transcript, kind)
}

func (c *Calls) sendLocked(session, text string, transcript *string, kind string) {
	c.waiting[session] = append(c.waiting[session], Message{kind, text, transcript})
	c.log(fmt.Sprintf("%s for session %s: %s", kind, prefix(session, 8), pyrepr(text)))
	c.save()
}

// collect is what waits for a session, which its mod collects while ringback is on in it: so
// it listens. The mod says its project and folder, for the phone's Contacts.
func (c *Calls) collect(session, project, cwd string) []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.listening(session) {
		c.since[session] = unixNow()
		c.notifyChanged()
	}
	c.seen.set(session, time.Now())
	if project != "" {
		if c.named[session] != [2]string{project, cwd} {
			c.notifyChanged()
		}
		c.named[session] = [2]string{project, cwd}
	}
	messages := c.waiting[session]
	if len(messages) > 0 {
		delete(c.waiting, session)
		c.save()
	}
	if messages == nil {
		messages = []Message{}
	}
	return messages
}

func (c *Calls) notifyChanged() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

// news tells the call you're on with a session its news; false if there's no such call.
func (c *Calls) news(session, text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, call := range c.calls.list() {
		// Not while the phone rings: the voice can tell you only once you've answered.
		if call.Session == session && call.answered() && call.Link != nil && call.Link.notify != nil {
			call.Link.news(text)
			return true
		}
	}
	return false
}

// ask puts what a session asks to the call you're on with it ("live"), or to its call that's
// ringing, for once you answer ("placed"), or held outside your work hours ("held");
// otherwise places a call to ask it ("placed" or "held"). Returns it, how, and when a held
// call can ring; an error if no call can be placed. A question that joins a held call has it
// ask the hub again at once, which tells the phone.
func (c *Calls) ask(session, kind string, request Request) (*Ask, string, string, error) {
	// One at a time: parallel tool calls can ask at once, and should share one call.
	c.asking.Lock()
	c.mu.Lock()
	a := newAsk(newID(&c.asks), kind, session, request)
	// Not a call back whose voice can't answer the session.
	var call *Call
	for _, candidate := range c.calls.list() {
		if candidate.Session == session && candidate.Link.listening {
			if call == nil || !call.answered() || candidate.answered() {
				call = candidate
			}
		}
	}
	if call != nil {
		call.Link.ask(a, false)
		if call.HeldUntil != "" && kind == "question" {
			call.signalReask()
		}
		c.mu.Unlock()
	} else {
		c.mu.Unlock()
		placed, err := c.place(session, request.Project, request.Cwd, request.Subject, a.briefing(), a)
		if err != nil {
			c.asking.Unlock()
			return nil, "", "", err
		}
		call = placed
	}
	c.mu.Lock()
	how := "placed"
	if call.answered() {
		how = "live"
	} else if call.HeldUntil != "" {
		how = "held"
	}
	heldUntil := call.HeldUntil
	c.asks.set(a.ID, a)
	c.asking.Unlock()
	c.logCall(call, fmt.Sprintf("%s for session %s, %s: %s", a.name(), prefix(session, 8), how, pyrepr(prefix(a.details(), 100))))
	c.keepUntilRead(a)
	c.save()
	c.mu.Unlock()
	return a, how, heldUntil, nil
}

func (c *Calls) keepUntilRead(a *Ask) { go c.keep(a) }

// heldUntilOf is when the call that asks it can ring, while that call is held outside your
// work hours.
func (c *Calls) heldUntilOf(a *Ask) string {
	for _, call := range c.calls.list() {
		if call.HeldUntil != "" {
			if got, ok := call.Link.asks.get(a.ID); ok && got != nil {
				return call.HeldUntil
			}
		}
	}
	return ""
}

// asked is what a session asked, by its id, until it's withdrawn or forgotten.
func (c *Calls) asked(session, kind, id string) *Ask {
	a, ok := c.asks.get(id)
	if ok && a.Session == session && a.Kind == kind {
		return a
	}
	return nil
}

// settle records an answer given outside a call (by a tool); false if it has one already.
func (c *Calls) settle(a *Ask, outcome, said string, answers pairs) bool {
	if !a.settle(outcome, &said, answers) {
		return false
	}
	if pool, ok := c.pool[a.Session]; ok {
		pool.pop(a.ID)
	}
	for _, call := range c.calls.list() {
		call.Link.drop(a, "it was answered outside the call, "+a.answered())
	}
	c.save()
	return true
}

// withdraw: the session's mod no longer waits for an answer, so a call's voice no longer asks.
func (c *Calls) withdraw(a *Ask) {
	c.asks.pop(a.ID)
	if pool, ok := c.pool[a.Session]; ok {
		pool.pop(a.ID)
	}
	why := "it withdrew the request"
	if a.Kind == "question" {
		why = "it withdrew the question"
	}
	for _, call := range c.calls.list() {
		if _, ok := call.Link.asks.get(a.ID); ok && call.HeldUntil != "" && a.Kind == "question" {
			call.signalReask() // so the app soon stops showing the question
		}
		call.Link.drop(a, why)
	}
	a.settle("withdrawn", nil, nil) // wakes those waiting for it, who find it gone
	c.save()
}

// keep logs the answer, and forgets it a while after. An answer the voice gives on a call is
// saved here, once it's given.
func (c *Calls) keep(a *Ask) {
	c.mu.Lock()
	settled := a.Outcome != ""
	c.mu.Unlock()
	if !settled {
		<-a.settled
		c.mu.Lock()
		c.log(fmt.Sprintf("%s (session %s)", a.answered(), prefix(a.Session, 8)))
		c.save()
		c.mu.Unlock()
	}
	time.Sleep(keepFor)
	c.mu.Lock()
	defer c.mu.Unlock()
	if got, ok := c.asks.get(a.ID); ok && got == a {
		c.asks.pop(a.ID)
		c.save()
	}
}

// deadline forgets a call that isn't answered in time, which tells its session.
func (c *Calls) deadline(ctx context.Context, call *Call) {
	timer := time.NewTimer(unanswered)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if got, ok := c.calls.get(call.ID); !call.answered() && ok && got == call {
		c.logCall(call, "not answered, forgotten")
		c.forget(call)
	}
}

// forget drops a call, answered and ended or never answered: the one place a call goes. Its
// session is told how it went, the Haiku that wrote its opening is closed, and it's
// remembered for a day, so you can call it back.
func (c *Calls) forget(call *Call) {
	c.calls.pop(call.ID)
	for _, cancel := range []context.CancelFunc{call.deadline, call.holding} {
		if cancel != nil {
			cancel()
		}
	}
	c.closeHaiku(call)
	if !call.answered() && call.Link.listening {
		// You didn't answer: what it was asking waits for the next call with its session you
		// answer, a call back or its next call, while the session's mod waits.
		pool := c.poolOf(call.Session)
		for _, a := range call.Link.asks.list() {
			pool.set(a.ID, a)
		}
	} else {
		// You hung up without answering it: the session's mod asks you itself.
		for _, a := range call.Link.asks.list() {
			a.settle("none", nil, nil)
		}
	}
	call.Link.asks.clear()
	var transcript *string
	if call.Link.transcript != nil {
		transcript = &call.Link.transcript.Relative
	}
	c.sendLocked(call.Session, outcome(call, transcript), transcript, "outcome")
	// What's remembered is the call as it was placed, also after a call back, with when you
	// last talked on it. One whose audio never connected you didn't hear, so the next call
	// about it still tells it.
	c.past.set(call.ID, pastCall{talked(call, call.Lasted != nil), unixNow()})
	c.expire()
	c.save()
	c.stayAwake()
}

func (c *Calls) poolOf(session string) *ordered[*Ask] {
	pool, ok := c.pool[session]
	if !ok {
		pool = &ordered[*Ask]{}
		c.pool[session] = pool
	}
	return pool
}

func (c *Calls) closeHaiku(call *Call) {
	if call.Haiku != nil {
		c.closeLater(call.Haiku)
		call.Haiku = nil
	}
}

// closeLater closes a Haiku no call needs any more, which takes up to 2 s, without holding up
// what forgot it.
func (c *Calls) closeLater(h *Haiku) {
	c.closing.Add(1)
	go func() {
		defer c.closing.Done()
		h.Close()
	}()
}

// expire forgets the calls forgotten more than a day ago.
func (c *Calls) expire() {
	now := unixNow()
	for _, id := range append([]string{}, c.past.keys...) {
		if p, _ := c.past.get(id); now-p.at >= rememberFor {
			c.past.pop(id)
		}
	}
}

// stayAwake holds a sleep assertion while any call is placed or in progress.
func (c *Calls) stayAwake() {
	if caffeinate == "" {
		return
	}
	if c.calls.len() > 0 && c.awake == nil {
		cmd := exec.Command(caffeinate, "-i", "-w", strconv.Itoa(os.Getpid()))
		if err := cmd.Start(); err != nil {
			c.log("can't keep the Mac awake: " + err.Error())
			return
		}
		c.awake = cmd
	} else if c.calls.len() == 0 && c.awake != nil {
		c.awake.Process.Signal(syscall.SIGTERM)
		c.awake.Wait()
		c.awake = nil
	}
}

// remembered is what's remembered of a call once it's forgotten, so you can call it back: the
// call as it was placed, with what its briefing asks, and nothing running.
func remembered(call *Call) *Call {
	kept := *call
	kept.AnsweredAt, kept.Link, kept.Haiku, kept.deadline, kept.HeldUntil, kept.holding = time.Time{}, nil, nil, nil, "", nil
	if call.Link != nil {
		kept.Asked = call.Link.briefedNames()
	}
	return &kept
}

// talked is what's remembered of a call that has ended: the call as it was placed, also after
// a call back, and when you last talked on it, if you answered and its audio connected
// (heard).
func talked(call *Call, heard bool) *Call {
	kept := call.Back
	if kept == nil {
		kept = remembered(call)
	}
	if call.answered() && heard {
		copied := *kept
		now := unixNow()
		copied.Talked = &now
		return &copied
	}
	return kept
}

// outcome is how a call went, for its session to read. If it wasn't answered, the agent
// can't tell why; the hub's log can.
func outcome(call *Call, transcript *string) string {
	about := "The call"
	if call.Subject != "" {
		about = `The call about "` + call.Subject + `"`
	}
	withTranscript := func(text string) string {
		if transcript != nil && *transcript != "" {
			return text + " Its transcript: " + *transcript
		}
		return text
	}
	if call.Contact {
		if !call.answered() || call.Lasted == nil {
			return "The developer called the session from the app's Contacts, but the call didn't connect."
		}
		return withTranscript("The developer called the session from the app's Contacts and it lasted " + duration(*call.Lasted) + ".")
	}
	if call.Back != nil {
		what := "an earlier call"
		if call.Subject != "" {
			what = `"` + call.Subject + `"`
		}
		// From the app's call history, or a held call from the app's waiting calls.
		how := "called back about " + what
		if call.Now {
			how = "called about " + what + " from the app while it waited"
		}
		if !call.answered() || call.Lasted == nil {
			return "The developer " + how + ", but the call didn't connect."
		}
		return withTranscript("The developer " + how + " and it lasted " + duration(*call.Lasted) + ".")
	}
	if !call.answered() {
		took := duration(time.Since(call.Placed).Seconds())
		return about + " wasn't answered within " + took + ": declined, missed, or its audio didn't connect."
	}
	if call.Lasted == nil {
		return about + " was answered, but its audio didn't connect."
	}
	return withTranscript(about + " was answered and lasted " + duration(*call.Lasted) + ".")
}

// restarted is the outcome of a call that was ringing or on when the agent restarted.
func restarted(call *Call) string {
	ended := "ended when the voice server restarted"
	transcript := ""
	if call.Link != nil && call.Link.transcriptPath != "" {
		transcript = " Its transcript: " + call.Link.transcriptPath
	}
	if call.Contact {
		return "The developer called the session from the app's Contacts, and the call " + ended + "." + transcript
	}
	if call.Back != nil {
		what := "an earlier call"
		if call.Subject != "" {
			what = `"` + call.Subject + `"`
		}
		how := "called back about " + what
		if call.Now {
			how = "called about " + what + " from the app while it waited"
		}
		return "The developer " + how + ", and the call " + ended + "." + transcript
	}
	about := "The call"
	if call.Subject != "" {
		about = `The call about "` + call.Subject + `"`
	}
	if call.answered() {
		return about + " was answered, and " + ended + "." + transcript
	}
	return about + " " + ended + ", before it was answered."
}

// until is a held call's heldUntil for the log: "until 2026-11-03T08:15:00+01:00", or "until
// you take calls".
func until(heldUntil string) string {
	if heldUntil == onHold {
		return "until you take calls"
	}
	return "until " + heldUntil
}

// heldFields is what a session's mod hears of a held call: held, and when it rings
// (held_until) if there's a time; nothing for a call that isn't held.
func heldFields(heldUntil string) map[string]any {
	if heldUntil == "" {
		return map[string]any{}
	}
	if heldUntil == onHold {
		return map[string]any{"held": true}
	}
	return map[string]any{"held": true, "held_until": heldUntil}
}

// wake is when a held call asks the hub again (Unix seconds): when it said the call can ring,
// or after reaskAfter if that's sooner, as for a call held with no time (onHold). Never within
// a second, so a Mac whose clock is a little ahead of the hub's doesn't ask again and again.
func wake(heldUntil string) float64 {
	at := math.Inf(1)
	if t, err := time.Parse(time.RFC3339Nano, heldUntil); err == nil {
		at = float64(t.UnixNano()) / 1e9
	} else if t, err := time.ParseInLocation("2006-01-02T15:04:05", heldUntil, time.Local); err == nil {
		at = float64(t.UnixNano()) / 1e9
	}
	now := unixNow()
	return math.Max(math.Min(at, now+reaskAfter.Seconds()), now+1)
}

// stamp is a Unix time as ISO 8601, with this Mac's offset from UTC.
func stamp(at float64) string {
	sec, frac := math.Modf(at)
	return time.Unix(int64(sec), int64(frac*1e9)).Format("2006-01-02T15:04:05-07:00")
}

func unixNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// newID is four random lowercase letters, not taken yet: short, and safe for Haiku to write.
func newID(taken *ordered[*Ask]) string {
	for {
		b := make([]byte, 4)
		for i := range b {
			n, _ := rand.Int(rand.Reader, big.NewInt(26))
			b[i] = byte('a' + n.Int64())
		}
		if _, ok := taken.get(string(b)); !ok {
			return string(b)
		}
	}
}

// token is a call's ID, its secret: 24 random bytes, URL-safe.
func token() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ordered is a map that keeps its keys in the order they came, as a Python dict does: which
// call is a session's latest, and the order its asks are put to you, depend on it.
type ordered[V any] struct {
	keys   []string
	values map[string]V
}

func (o *ordered[V]) get(key string) (V, bool) {
	v, ok := o.values[key]
	return v, ok
}

func (o *ordered[V]) set(key string, value V) {
	if o.values == nil {
		o.values = map[string]V{}
	}
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *ordered[V]) pop(key string) (V, bool) {
	v, ok := o.values[key]
	if ok {
		delete(o.values, key)
		for i, k := range o.keys {
			if k == key {
				o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
				break
			}
		}
	}
	return v, ok
}

func (o *ordered[V]) list() []V {
	list := make([]V, len(o.keys))
	for i, k := range o.keys {
		list[i] = o.values[k]
	}
	return list
}

func (o *ordered[V]) len() int { return len(o.keys) }

func (o *ordered[V]) clear() {
	o.keys, o.values = nil, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envSeconds(name string, fallback float64) time.Duration {
	seconds := fallback
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			seconds = f
		}
	}
	return time.Duration(seconds * float64(time.Second))
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func expand(path string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
}
