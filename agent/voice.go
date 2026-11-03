package main

// The call's voice: Haiku in a Claude Code session that stays open for the whole call, on the
// Claude subscription, between the hub and the session.
//
// The hub owns the call's timing: it hears you, cuts the voice off when you interrupt, speaks
// the reply in pieces, and says how much of it you've heard. The voice owns what's said. The
// session (haiku.go) keeps the conversation itself, so each turn sends only what's new: your
// words, notes from the session, and a note when you cut the voice off. Replies go to the hub
// as they're written (`say`, then `said`), numbered 1, 2, 3… from the opening on. A call that
// a session placed brings its session already started, with the opening written while the
// phone rang; the opening is spoken once you've answered and the audio has connected.
//
// On a call for a session, Haiku has three tools (tools.go): pass_to_session, and
// answer_permission and answer_question for what the session asks you. A message it passes,
// or an answer it gives, is held until you've heard the voice say so, and dropped if you cut
// the voice off before that (see "Acting on the call" in docs/design.md).
//
// On every call, better_model puts Opus on the line when you ask for a better model, the same
// way: once you've heard Haiku say so and its turn has ended, Haiku's process is closed and
// Opus resumes its session, with the whole call so far, and speaks next.
//
// A note from the session waits until the hub says the voice is idle (it has finished
// speaking and you aren't), then starts a reply.

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var okayStart = regexp.MustCompile(`(?i)^\s*(okay|ok|alright|all right)\b[,.!]?\s*`)

// toolNamed finds a tool's name in a sentence: Haiku once wrote out the call it was about to
// make ("Then call answer_permission with allow true."), which isn't for saying.
var toolNamed = regexp.MustCompile(`\b(` + strings.Join(append([]string{betterModel}, sessionTools...), "|") + `)\b`)

// held is a message or an answer for the session, or Opus taking over ("model"), until
// you've heard the reply up to where Haiku gave it.
type held struct {
	turn  int
	words *int // of the reply before Haiku gave it; nil if it said nothing first
	kind  string
	text  string
	give  func() bool // false if the session no longer waits for it
}

// part is what a turn brings: your words, or a note in square brackets.
type part struct {
	note bool
	text string
}

type (
	answeredMsg  struct{ at time.Time }
	connectedMsg struct{ at time.Time }
	speakingMsg  struct{}
	idleMsg      struct{}
	quietMsg     struct{}
	heardMsg     struct {
		text  string
		reply int
	}
	interrupted struct {
		reply int
		heard string
	}
	turnMsg struct {
		text        string
		interrupted bool // you cut the voice off in this turn, or it hadn't finished its first reply
	}
	noteMsg struct{ text string }
	toolMsg struct {
		name   string
		input  map[string]any
		answer chan string
	}
	switchedMsg struct {
		err  error
		took time.Duration
	}
	endMsg struct{}
)

type voice struct {
	calls        *Calls
	callID       string
	link         *Link // to the calling session; its fields are guarded by calls.mu
	haiku        *Haiku
	ownHaiku     bool // started here, so it's closed here
	instructions string
	out          func(map[string]any) // to the hub
	log          func(string)
	inbox        queue
	done         chan struct{} // closed once it has ended and passed on what you'd heard

	answeredAt  time.Time
	connectedAt time.Time
	opened      bool // the opening has started
	spoke       bool // the voice has started speaking
	idle        bool // the hub says it has finished speaking and you aren't
	ended       bool

	replies     int    // replies started, numbered from 1
	pending     int    // the reply your turn started, until it's sent to Haiku
	recoveries  int    // stops with no words answered in a row
	parts       []part // what the next reply answers
	due         bool   // your turn came: the next reply goes as soon as it can
	current     int    // the reply Haiku writes or wrote last
	writing     bool   // its text is still coming
	hearing     bool   // the hub is playing it, so its heard counts
	heard       string // what you've heard of it
	turnAt      time.Time
	firstWords  bool // the current reply's first words were logged
	sentAt      time.Time
	handingOver bool // you heard Haiku say it's putting Opus on: Opus takes the next reply
	switching   bool // Haiku's process closing and Opus's starting
	handed      bool // Opus has taken over, and its first reply hasn't gone yet
	unsent      string

	said       string // your words in the last turn sent
	turn       int    // turns sent to Haiku
	held       []*held
	unsaid     string  // reply text after a tool call, which isn't spoken
	quiet      bool    // this reply passed a message after saying so: the rest isn't spoken
	ack        string  // ack, said for Haiku when it reached for a tool before saying anything
	opener     *string // Haiku's first words after the ack, until checked
	heardWords *int    // of the last reply, if you cut it off
	unfinished int     // turns sent whose reply hasn't fully arrived
	reply      string  // the last reply, as it's spoken
	unspoken   string  // its sentence still being written (see speakable)
	written    bool    // the last reply is in the call's transcript
	cut        bool    // you interrupted the last reply while it was arriving
	cutOff     *string // what you'd heard of the reply you interrupted

	reasked map[string]bool // asks the voice has asked again after you said nothing
}

func newVoice(calls *Calls, call *Call, haiku *Haiku, out func(map[string]any)) *voice {
	v := &voice{
		calls: calls, callID: call.ID, link: call.Link, haiku: haiku, out: out,
		log:  func(line string) { calls.log("call " + prefix(call.ID, 8) + ": " + line) },
		done: make(chan struct{}),
	}
	v.inbox.init()
	calls.mu.Lock()
	listening := v.link.listening
	v.instructions = instructions(call.Briefing, true, listening)
	// Notes for the voice wait until it has started its opening.
	v.link.listen(func(text string) {
		v.log("note from the session: " + pyrepr(prefix(text, 80)))
		v.inbox.put(noteMsg{text})
	})
	calls.mu.Unlock()
	return v
}

// run is the voice from the phone's offer until the call ends or a new offer replaces it.
func (v *voice) run() {
	defer close(v.done)
	if v.haiku == nil {
		// The call starts when the phone's offer arrives, before its audio connects.
		v.calls.mu.Lock()
		tools := v.link.listening
		v.calls.mu.Unlock()
		v.haiku = newHaiku(v.instructions, v.log, tools, opening)
		v.ownHaiku = true
		if err := v.haiku.Start(); err != nil {
			// Without it the voice can't talk; the call goes on until you hang up.
			v.log(fmt.Sprintf("Haiku didn't start (%v)", err))
			v.haiku.Close()
			v.haiku = nil
		}
	}
	// Haiku was asked for the opening when it started, and writes it meanwhile. It waits
	// until it can be spoken.
	v.turn, v.unfinished = 1, 1
	for !v.ended {
		var events chan event
		if v.opened && v.unfinished > 0 {
			events = v.haiku.Events
		}
		select {
		case e := <-events:
			v.onEvent(e)
		case <-v.inbox.signal:
			for _, m := range v.inbox.take() {
				v.handle(m)
				if v.ended {
					break
				}
			}
		}
	}
	v.close()
}

func (v *voice) handle(m any) {
	switch m := m.(type) {
	case answeredMsg:
		v.answeredAt = m.at
		v.open()
	case connectedMsg:
		v.connectedAt = m.at
		v.open()
	case speakingMsg:
		v.idle = false
		if !v.spoke {
			v.spoke = true
			if !v.answeredAt.IsZero() {
				v.log(fmt.Sprintf("voice started %.2f s after you answered", time.Since(v.answeredAt).Seconds()))
			}
			// The notes kept for the voice go once it has started talking.
			v.calls.mu.Lock()
			v.link.opened()
			v.calls.mu.Unlock()
		} else if !v.turnAt.IsZero() {
			v.log(fmt.Sprintf("voice answered %.2f s after your turn", time.Since(v.turnAt).Seconds()))
		}
		v.turnAt = time.Time{}
	case idleMsg:
		v.idle = true
		v.tryAnswer()
	case quietMsg:
		v.onQuiet()
	case heardMsg:
		if m.reply == v.current {
			v.onHeard(m.text)
		}
	case interrupted:
		v.onInterrupted(m.reply, m.heard)
	case turnMsg:
		v.onTurn(m.text, m.interrupted)
	case noteMsg:
		v.parts = append(v.parts, part{true, m.text})
		v.tryAnswer()
	case toolMsg:
		v.drainEvents()
		m.answer <- v.tool(m.name, m.input)
	case switchedMsg:
		v.switching = false
		if m.err != nil {
			v.log(fmt.Sprintf("Opus didn't start on Haiku's session (%v)", m.err))
		} else {
			v.log(fmt.Sprintf("handed over: Haiku closed and Opus started on its session in %.2f s", m.took.Seconds()))
		}
		v.handed, v.due = true, true
		v.tryAnswer()
	case endMsg:
		v.ended = true
	}
}

// open starts the opening once you've answered and the audio has connected, whichever comes
// last: it's reply 1.
func (v *voice) open() {
	if v.opened || v.answeredAt.IsZero() || v.connectedAt.IsZero() || v.haiku == nil {
		return
	}
	v.opened = true
	v.haiku.markUsed()
	v.replies++
	v.current, v.writing, v.hearing, v.heard, v.idle = v.replies, true, false, "", false
	v.sentAt = v.haiku.OpeningAt
	v.firstWords = false
}

// drainEvents reads what Haiku has written so far, so a tool call sees the reply up to it.
func (v *voice) drainEvents() {
	for v.opened && v.unfinished > 0 {
		select {
		case e := <-v.haiku.Events:
			v.onEvent(e)
		default:
			return
		}
	}
}

func (v *voice) onEvent(e event) {
	v.speak(v.nextText(e))
	if v.unfinished == 0 && v.writing {
		v.writing = false
		// Written as soon as it's complete, so it comes before what it passed on.
		v.writeReply()
		if !v.cut {
			if words(v.reply) == 0 {
				// Nothing was said, so there's nothing to hear first.
				v.pass(func(h *held) bool { return h.turn == v.turn }, "")
			}
			v.out(map[string]any{"type": "said", "call_id": v.callID, "reply": v.current})
		}
	}
	if v.unfinished == 0 {
		v.tryAnswer()
	}
}

// speak sends reply text to the hub to be spoken.
func (v *voice) speak(text string) {
	if text == "" || !v.writing || v.cut {
		return
	}
	if !v.firstWords {
		v.firstWords = true
		// When they came, which for an opening written while the phone rang was before
		// it's spoken.
		came := v.haiku.FirstWords()
		if came.IsZero() {
			came = time.Now()
		}
		v.log(fmt.Sprintf("%s: first words after %.2f s", v.haiku.Name(), came.Sub(v.sentAt).Seconds()))
	}
	v.out(map[string]any{"type": "say", "call_id": v.callID, "reply": v.current, "text": text})
	v.hearing = true
}

// speakable takes the reply's next text and returns what of it can be spoken now: each
// sentence once the text after it has begun (a sentence that ends the text so far may not
// have ended: "2." of "2.5"), and none that names a tool. The hub speaks only whole sentences
// anyway, so holding back the one being written costs no time.
func (v *voice) speakable(text string) string {
	v.unspoken += text
	spoken := ""
	for {
		n := matchEndOfSentence(v.unspoken)
		if n == 0 || strings.TrimSpace(v.unspoken[n:]) == "" {
			return spoken
		}
		spoken += v.sentence(v.unspoken[:n])
		v.unspoken = v.unspoken[n:]
	}
}

// flush returns the rest of what Haiku has written, finished or not: it has reached for a
// tool, or its reply has ended.
func (v *voice) flush() string {
	text := v.sentence(v.unspoken)
	v.unspoken = ""
	return text
}

// sentence is a sentence of the reply as it's spoken: nothing if it names a tool.
func (v *voice) sentence(s string) string {
	if toolNamed.MatchString(s) {
		v.log("not spoken, names a tool: " + pyrepr(strings.TrimSpace(s)))
		return ""
	}
	v.reply += s
	return s
}

// nextText is the next piece of reply text in an event ("" for other events).
func (v *voice) nextText(e event) string {
	if e == nil {
		v.unfinished = 0
		v.log("error: the Claude Code session ended")
		return ""
	}
	if e["type"] == "result" {
		v.unfinished--
		if v.opener != nil && *v.opener != "" {
			// Haiku's words after the ack were too short to check; say them as they are.
			v.unspoken += *v.opener
			v.opener = nil
		}
		text := v.flush()
		if v.unsaid != "" {
			v.log("not spoken, after passing a message: " + pyrepr(strings.TrimSpace(v.unsaid)))
			v.unsaid = ""
		}
		if isError, _ := e["is_error"].(bool); isError {
			v.log(fmt.Sprintf("%s: error %s", v.haiku.Name(), pyrepr(e["result"])))
		}
		return text
	}
	s := stream(e)
	if s["type"] == "content_block_start" {
		block, _ := s["content_block"].(map[string]any)
		kind := block["type"]
		said := ""
		if kind == "tool_use" {
			// What it wrote before the tool is all it says first.
			said = v.flush()
		}
		switch {
		case kind == "tool_use" && words(skip(v.reply, utf8.RuneCountInString(v.ack))) > 0:
			// Haiku has said it's passing something on. Told so or not, it tends to say it
			// again after the tool, so the rest of this reply isn't spoken.
			v.quiet = true
		case kind == "tool_use" && strings.TrimSpace(v.said) != "" && v.ack == "":
			// Haiku answers you, or passes something on, before saying anything: its first
			// words would come only after the tool, 2-3.6 s after yours on the phone. "Okay."
			// fills that, and doesn't count as having said what it does. Said once: a tool it
			// tries again (a refused id) still waits for its words.
			v.ack = ack
			opener := ""
			v.opener = &opener
			v.reply += ack
			return said + ack
		case kind == "text" && !v.quiet && v.reply != "" && !endsInSpace(v.reply):
			// Text after a tool call starts a new block; keep a space between them.
			v.reply += " "
			return " "
		}
		return said
	}
	d, _ := s["delta"].(map[string]any)
	if d["type"] == "text_delta" {
		text, _ := d["text"].(string)
		if v.quiet {
			v.unsaid += text
			return ""
		}
		if v.opener != nil {
			// After the ack, Haiku tends to start with its own "Okay": drop it.
			*v.opener += text
			if utf8.RuneCountInString(*v.opener) < 12 && matchEndOfSentence(*v.opener) == 0 {
				return ""
			}
			text = okayStart.ReplaceAllString(*v.opener, "")
			v.opener = nil
			if r, size := utf8.DecodeRuneInString(text); size > 0 {
				text = string(unicode.ToUpper(r)) + text[size:]
			}
		}
		return v.speakable(text)
	}
	return ""
}

func endsInSpace(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.IsSpace(r)
}

func (v *voice) onHeard(text string) {
	if !v.hearing {
		return
	}
	v.heard = text
	if len(v.held) > 0 && !v.cut {
		heard := words(text)
		v.pass(func(h *held) bool { return h.turn == v.turn && v.needed(h) <= heard }, "")
		// Opus's first reply: it goes once the voice has finished speaking.
		v.tryAnswer()
	}
}

// onInterrupted: you cut the voice off. The rest of that reply is never spoken; what you'd
// heard of it is in the next turn's note. A reply your turn started that hasn't gone to
// Haiku yet is dropped, and your words go with your next turn.
func (v *voice) onInterrupted(reply int, heard string) {
	v.idle = false
	switch {
	case reply == v.pending && reply != 0:
		v.pending = 0
		v.log("interrupted before its reply came")
	case reply == v.current && reply != 0:
		if v.writing {
			v.cut = true
		}
		v.cutOff = &heard
		v.hearing, v.heard = false, ""
		tail := []rune(heard)
		if len(tail) > 60 {
			tail = tail[len(tail)-60:]
		}
		v.log("interrupted; you'd heard " + pyrepr(string(tail)))
	}
}

// onTurn: your words, which start the next reply at once. A turn with no words, a stop where
// nothing was recognised, has the voice carry on if it cut the voice off, once in a row
// (maxRecoveries); otherwise it goes unanswered, and its reply is just its end.
func (v *voice) onTurn(text string, interrupted bool) {
	empty := strings.TrimSpace(text) == ""
	if empty && (!interrupted || v.recoveries >= maxRecoveries) {
		v.log("a stop with no words, left unanswered")
		if v.pending == 0 {
			v.replies++
			v.out(map[string]any{"type": "said", "call_id": v.callID, "reply": v.replies})
		}
		return
	}
	if empty {
		v.recoveries++
	} else {
		v.recoveries = 0
	}
	v.idle = false
	if v.pending != 0 {
		// A reply started for an earlier turn hasn't gone yet: this one answers both.
		v.out(map[string]any{"type": "said", "call_id": v.callID, "reply": v.pending})
	}
	v.replies++
	v.pending = v.replies
	if strings.TrimSpace(text) == "" {
		v.parts = append(v.parts, part{true, noise})
	} else {
		v.parts = append(v.parts, part{false, text})
	}
	v.due = true
	v.turnAt = time.Now()
	v.tryAnswer()
}

// onQuiet: the voice finished a while ago and you haven't spoken since (the hub's quietAfter).
// If the session still waits for your answer to something put to you, as when your "yes" went
// unheard, the voice asks again, once for each.
func (v *voice) onQuiet() {
	if !v.idle || v.unfinished > 0 || len(v.parts) > 0 || v.holds("answer") {
		return
	}
	v.calls.mu.Lock()
	var asks []string
	for _, a := range v.link.asks.list() {
		if !v.reasked[a.ID] {
			asks = append(asks, a.ID)
		}
	}
	v.calls.mu.Unlock()
	if len(asks) == 0 {
		return
	}
	if v.reasked == nil {
		v.reasked = map[string]bool{}
	}
	for _, id := range asks {
		v.reasked[id] = true
	}
	v.log("nothing heard since the voice finished, and an answer is waited for: it asks again")
	v.parts = append(v.parts, part{true, reask})
	v.tryAnswer()
}

// tryAnswer starts the next reply when it can: your turn as soon as Haiku has finished the
// last one and Opus, if it's taking over, has started; notes and Opus's first reply once the
// voice is idle.
// maxRecoveries is how many stops with no words in a row the voice answers.
const maxRecoveries = 1

func (v *voice) tryAnswer() {
	if !v.opened || v.ended || v.unfinished > 0 || v.switching || v.haiku == nil {
		return
	}
	if v.due || (v.idle && (len(v.parts) > 0 || v.handingOver)) {
		v.answer()
	}
}

func (v *voice) answer() {
	v.haiku.markUsed()
	note := v.unsent + v.cutOffNote()
	// What the last reply announced goes to the session if you heard it announced.
	heard := v.heardWords
	note += v.pass(func(h *held) bool { return heard == nil || v.needed(h) <= *heard }, "you cut the voice off first")
	if v.handingOver {
		// Opus starts even if you speak meanwhile, and then this turn's notes go to it with
		// your words.
		v.handingOver = false
		v.switching = true
		v.unsent = note
		go func() {
			t := time.Now()
			err := v.haiku.HandOver()
			v.inbox.put(switchedMsg{err, time.Since(t)})
		}()
		return
	}
	handed := v.handed
	v.handed, v.unsent = false, ""
	if handed {
		note = "[" + handOver + "]\n" + note
	}
	reply := v.pending
	if reply == 0 {
		v.replies++
		reply = v.replies
	}
	v.pending, v.due = 0, false
	var parts, said []string
	for _, p := range v.parts {
		if p.note {
			parts = append(parts, "["+p.text+"]")
		} else {
			parts = append(parts, p.text)
			said = append(said, p.text)
		}
	}
	v.parts = nil
	v.said = strings.Join(said, "\n")
	text := strings.Join(parts, "\n")
	if text == "" && !handed {
		text = "[Carry on.]"
	}
	if note != "" {
		v.log(fmt.Sprintf("told %s: %s", v.haiku.Name(), strings.TrimSpace(note)))
	}
	v.calls.mu.Lock()
	if t := v.link.transcript; t != nil {
		for _, line := range strings.Split(strings.TrimRight(note, "\n"), "\n") {
			t.add("Note to the voice", strings.Trim(strings.TrimSpace(line), "[]"))
		}
		t.add("Developer", v.said)
	}
	v.calls.mu.Unlock()
	v.send(note+text, reply)
}

// send sends a turn to Haiku, as reply number reply.
func (v *voice) send(message string, reply int) {
	v.reply, v.unspoken, v.written, v.cut, v.quiet = "", "", false, false, false
	v.ack, v.opener = "", nil
	v.turn++
	// From your first words on, Haiku's tool calls are yours to pass on (see haiku.go).
	v.haiku.TakeTools(v.fromHaiku)
	t, err := v.haiku.Send(message)
	if err != nil {
		v.log(fmt.Sprintf("couldn't send %s the turn: %v", v.haiku.Name(), err))
	}
	v.unfinished++
	v.sentAt = t
	v.current, v.writing, v.hearing, v.heard, v.idle = reply, true, false, "", false
	v.firstWords = false
}

// fromHaiku answers a tool call from Haiku's tool socket, in turn with what the voice does.
func (v *voice) fromHaiku(name string, input map[string]any) string {
	answer := make(chan string, 1)
	v.inbox.put(toolMsg{name, input, answer})
	select {
	case text := <-answer:
		return text
	case <-v.done:
		return "Nothing done: the call has ended."
	}
}

func (v *voice) writeReply() {
	if v.written {
		return
	}
	v.calls.mu.Lock()
	defer v.calls.mu.Unlock()
	if v.link.transcript != nil {
		v.link.transcript.add("Voice", v.reply)
		v.written = true
	}
}

func (v *voice) tool(name string, arguments map[string]any) string {
	// What Haiku wrote before the call is spoken first, if the call came before its event did.
	v.speak(v.flush())
	if name == betterModel {
		return v.betterModel()
	}
	v.calls.mu.Lock()
	listening := v.link.listening
	v.calls.mu.Unlock()
	if !listening {
		return "Nothing done: the session isn't listening any more."
	}
	switch name {
	case "answer_permission":
		return v.answerPermission(arguments)
	case "answer_question":
		return v.answerQuestion(arguments)
	}
	message, ok := arguments["message"].(string)
	if name != "pass_to_session" || !ok || strings.TrimSpace(message) == "" {
		return "Nothing passed: pass_to_session needs a message."
	}
	message = strings.TrimSpace(message)
	v.hold("message", message, func() bool {
		v.calls.mu.Lock()
		defer v.calls.mu.Unlock()
		v.link.send(message)
		return true
	})
	v.log(fmt.Sprintf("%s → session, held until heard: %s", v.haiku.Name(), pyrepr(message)))
	return heldText
}

func (v *voice) waitingAsk(kind string, id any) *Ask {
	key, ok := id.(string)
	if !ok {
		return nil
	}
	v.calls.mu.Lock()
	defer v.calls.mu.Unlock()
	if a, ok := v.link.asks.get(key); ok && a.Kind == kind {
		return a
	}
	return nil
}

func (v *voice) answerPermission(arguments map[string]any) string {
	a := v.waitingAsk("permission", arguments["permission"])
	if a == nil {
		return fmt.Sprintf("Nothing answered: no permission %s waits. %s", pyrepr(arguments["permission"]), v.waiting())
	}
	allow, ok := arguments["allow"].(bool)
	if !ok {
		return "Nothing answered: answer_permission needs allow, true or false."
	}
	verdict, said := "deny", v.words(arguments)
	if allow {
		verdict = "allow"
	}
	text := fmt.Sprintf("%s: %s (%s)", a.name(), verdict, pyrepr(said))
	v.hold("answer", text, func() bool {
		v.calls.mu.Lock()
		defer v.calls.mu.Unlock()
		return v.link.answer(a.ID, verdict, said, nil)
	})
	v.log(fmt.Sprintf("%s answers the session, held until heard: %s", v.haiku.Name(), text))
	return answerHeld
}

func (v *voice) answerQuestion(arguments map[string]any) string {
	a := v.waitingAsk("question", arguments["question"])
	if a == nil {
		return fmt.Sprintf("Nothing answered: no question %s waits. %s", pyrepr(arguments["question"]), v.waiting())
	}
	answers, missing := a.match(arguments["answers"])
	if len(missing) > 0 {
		return "Nothing answered: answer_question needs an answer to each question. Missing: " + pyrepr(missing)
	}
	said := v.words(arguments)
	text := fmt.Sprintf("%s: %s (%s)", a.name(), pyrepr(answers), pyrepr(said))
	v.hold("answer", text, func() bool {
		v.calls.mu.Lock()
		defer v.calls.mu.Unlock()
		return v.link.answer(a.ID, "answered", said, answers)
	})
	v.log(fmt.Sprintf("%s answers the session, held until heard: %s", v.haiku.Name(), text))
	return answerHeld
}

func (v *voice) betterModel() string {
	if v.haiku.Model() == "opus" {
		v.log("Opus asked for a better model: nothing done")
		return opusAlready
	}
	if !v.handingOver && !v.switching && !v.holds("model") {
		v.hold("model", "Opus takes the call over from Haiku", func() bool {
			// Opus takes the next reply: started once the voice is idle, or by your words if
			// they come first.
			v.handingOver = true
			return true
		})
		v.log("Haiku puts Opus on, held until heard")
	}
	return handOverHeld
}

func (v *voice) holds(kind string) bool {
	for _, h := range v.held {
		if h.kind == kind {
			return true
		}
	}
	return false
}

// words are your words that Haiku gave with its answer, or else your last turn as recognised.
func (v *voice) words(arguments map[string]any) string {
	if said, ok := arguments["said"].(string); ok && strings.TrimSpace(said) != "" {
		return strings.TrimSpace(said)
	}
	return strings.TrimSpace(v.said)
}

func (v *voice) waiting() string {
	v.calls.mu.Lock()
	defer v.calls.mu.Unlock()
	names := []string{}
	for _, a := range v.link.asks.list() {
		names = append(names, a.name())
	}
	if len(names) == 0 {
		return "Nothing waits for an answer."
	}
	return "Waiting for an answer: " + strings.Join(names, ", ") + "."
}

// hold holds a message or an answer for the session until you've heard the reply so far.
func (v *voice) hold(kind, text string, give func() bool) {
	// The words of the reply so far are what you must hear before it goes; nil if Haiku said
	// nothing first.
	h := &held{turn: v.turn, kind: kind, text: text, give: give}
	if n := words(skip(v.reply, utf8.RuneCountInString(v.ack))); n > 0 {
		h.words = &n
	}
	v.held = append(v.held, h)
}

// needed is the words of the reply you must hear before something held goes: those before
// Haiku gave it, or if it said nothing first, the sentence that follows. Once it answered your
// yes before saying anything, and the answer went when you'd heard "Okay,".
func (v *voice) needed(h *held) int {
	acked := words(v.ack)
	if h.words != nil {
		return acked + *h.words
	}
	rest := skip(v.reply, utf8.RuneCountInString(v.ack))
	end := matchEndOfSentence(rest)
	if end == 0 {
		end = len(rest)
	}
	return acked + max(1, words(rest[:end]))
}

// pass passes on the held messages, answers and hand-over that go; drops the rest if dropped
// gives why. Returns a note for Haiku about those dropped.
func (v *voice) pass(goes func(*held) bool, dropped string) string {
	note := ""
	for _, h := range append([]*held{}, v.held...) {
		message := h.kind == "message"
		text := h.text
		if message {
			text = pyrepr(h.text)
		}
		if h.kind == "model" {
			if goes(h) {
				v.remove(h)
				h.give()
				v.log("heard: " + text)
			} else if dropped != "" {
				v.remove(h)
				v.log(fmt.Sprintf("not handed over, %s: %s", dropped, text))
				v.addTranscript("Not handed over, "+dropped, text)
				note += "[Opus didn't take over, because the developer cut you off before hearing you say so. " +
					"Call better_model again only if they still want it.]\n"
			}
			continue
		}
		if goes(h) {
			v.remove(h)
			if !h.give() {
				v.log("not given, the session no longer waits for it: " + text)
			} else if message {
				v.log("passed to the session: " + text)
			} else {
				v.log("answered the session: " + text)
			}
		} else if dropped != "" {
			v.remove(h)
			what := "answered"
			if message {
				what = "passed"
			}
			v.log(fmt.Sprintf("not %s, %s: %s", what, dropped, text))
			if message {
				v.addTranscript("Not passed to the session, "+dropped, h.text)
				note += fmt.Sprintf("[Not passed to the session, because the developer cut you off before hearing you say "+
					"so: \"%s\". Pass it again only if they still want it.]\n", h.text)
			} else {
				v.addTranscript("Not answered, "+dropped, h.text)
				note += fmt.Sprintf("[Your answer wasn't given, because the developer cut you off before hearing you say "+
					"it: %s. The session still waits. Read what they said before together with what they've just said: "+
					"answer again only if together it's a clear answer, otherwise ask.]\n", h.text)
			}
		}
	}
	return note
}

func (v *voice) remove(h *held) {
	for i, x := range v.held {
		if x == h {
			v.held = append(v.held[:i:i], v.held[i+1:]...)
			return
		}
	}
}

func (v *voice) addTranscript(who, text string) {
	v.calls.mu.Lock()
	defer v.calls.mu.Unlock()
	if v.link.transcript != nil {
		v.link.transcript.add(who, text)
	}
}

// cutOffNote tells Haiku how much of its last reply you heard, if you cut it off.
func (v *voice) cutOffNote() string {
	heard := v.cutOff
	v.cutOff = nil
	if heard == nil && v.cut {
		// Cut off so early that none of it was heard.
		empty := ""
		heard = &empty
	}
	v.cut = false
	if heard == nil || strings.Join(strings.Fields(*heard), " ") == strings.Join(strings.Fields(v.reply), " ") {
		v.heardWords = nil
		return ""
	}
	n := words(*heard)
	v.heardWords = &n
	if n == 0 {
		return "[You were interrupted before the developer heard any of your last reply.]\n"
	}
	fields := strings.Fields(*heard)
	if len(fields) > 12 {
		fields = fields[len(fields)-12:]
	}
	return fmt.Sprintf("[You were interrupted. The developer heard your last reply up to: \"…%s\"]\n", strings.Join(fields, " "))
}

// close: you hung up, or a new offer replaced this one. Opus isn't needed any more, but what
// you'd heard announced still goes.
func (v *voice) close() {
	kept := v.held[:0]
	for _, h := range v.held {
		if h.kind != "model" {
			kept = append(kept, h)
		}
	}
	v.held = kept
	heard := words(v.heard)
	v.pass(func(h *held) bool { return v.needed(h) <= heard }, "you hung up first")
	v.writeReply()
	v.reply = ""
	if v.ownHaiku && v.haiku != nil {
		v.haiku.Close()
	}
	v.haiku = nil
}

// end ends the voice and waits until it has passed on what you'd heard.
func (v *voice) end() {
	v.inbox.put(endMsg{})
	<-v.done
}

// queue is an inbox that never blocks its senders: the hub's messages, the session's notes
// and Haiku's tool calls, in the order they came.
type queue struct {
	mu     sync.Mutex
	items  []any
	signal chan struct{}
	once   sync.Once
}

func (q *queue) init() { q.once.Do(func() { q.signal = make(chan struct{}, 1) }) }

func (q *queue) put(item any) {
	q.init()
	q.mu.Lock()
	q.items = append(q.items, item)
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *queue) take() []any {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.items
	q.items = nil
	return items
}
