package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
	"unicode/utf8"
)

// The expected answers for sample inputs (testdata/golden.json).
type goldenData struct {
	Outcomes []struct {
		Name, Subject, Text          string
		Answered, Contact, Back, Now bool
		Lasted                       *float64
		Transcript                   *string
	}
	Restarts []struct {
		Name, Subject, Text          string
		Answered, Contact, Back, Now bool
		Transcript                   *string
	}
	Durations []struct {
		Seconds float64
		Text    string
	}
	WhileAgo []struct {
		Seconds float64
		Text    string
	}
	Leads []struct {
		Kind, Text string
		Briefing   *string
		Ago        float64
		Since      []any
		Settled    []string
		Talked     *float64
		Heard      *float64
	}
	Instructions []struct {
		Report             *string
		Session, Listening bool
		Text               string
	}
	Asks []struct {
		ID, Kind                string
		Request                 Request
		Details, Note, Briefing string
	}
	Answered []struct {
		Kind, ID, Outcome, Text string
		Request                 Request
		Said                    *string
		Answers                 pairs
	}
	Matches []struct {
		Given   any
		Answers [][2]string
		Missing []string
	}
	Plain []struct{ Text, Plain string }
	Cut   []struct {
		Text, Cut string
		Most      int
	}
	HeldFields []struct {
		HeldUntil *string `json:"held_until"`
		Fields    map[string]any
	}
	Until []struct {
		HeldUntil string `json:"held_until"`
		Text      string
	}
	Sentences []struct {
		Text string
		End  int
	}
	Reprs   []struct{ Text, Repr string }
	Replies []goldenReply
}

type goldenReply struct {
	Name    string
	Spoken  []string
	Reply   string
	Answers []string
	Held    []struct {
		Kind, Text string
		Words      *int
		Needed     int
	}
	Unsaid string
	Cuts   []struct {
		Heard, Note string
		Left        int
	}
}

func loadGolden(t *testing.T) goldenData {
	t.Helper()
	data, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldenData
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func same(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %#v\nwant %#v", what, got, want)
	}
}

func TestGoldenTexts(t *testing.T) {
	g := loadGolden(t)
	for _, o := range g.Outcomes {
		call := &Call{Subject: o.Subject, Lasted: o.Lasted, Contact: o.Contact, Now: o.Now, Placed: time.Now()}
		if o.Answered {
			call.AnsweredAt = time.Now()
		}
		if o.Back {
			call.Back = &Call{}
		}
		same(t, "outcome "+o.Name, outcome(call, o.Transcript), o.Text)
	}
	for _, r := range g.Restarts {
		call := &Call{Subject: r.Subject, Contact: r.Contact, Now: r.Now, Link: &Link{}}
		if r.Answered {
			call.AnsweredAt = time.Now()
		}
		if r.Back {
			call.Back = &Call{}
		}
		if r.Transcript != nil {
			call.Link.transcriptPath = *r.Transcript
		}
		same(t, "restarted "+r.Name, restarted(call), r.Text)
	}
	for _, d := range g.Durations {
		same(t, fmt.Sprint("duration ", d.Seconds), duration(d.Seconds), d.Text)
	}
	for _, w := range g.WhileAgo {
		same(t, fmt.Sprint("while ago ", w.Seconds), whileAgo(w.Seconds), w.Text)
	}
	for i, l := range g.Leads {
		briefing := ""
		if l.Briefing != nil {
			briefing = *l.Briefing
		}
		var got string
		switch l.Kind {
		case "back":
			var later *since
			if l.Since != nil {
				later = &since{l.Since[0].(string), l.Since[1].(float64)}
			}
			got = calledBack(briefing, l.Ago, later, l.Settled, l.Talked)
		case "now":
			got = calledNowBriefing(briefing)
		case "contact":
			got = calledContactBriefing(briefing, l.Ago, l.Heard)
		}
		same(t, fmt.Sprintf("lead %d (%s)", i, l.Kind), got, l.Text)
	}
	for _, in := range g.Instructions {
		report := ""
		if in.Report != nil {
			report = *in.Report
		}
		same(t, fmt.Sprint("instructions ", in.Report, in.Session, in.Listening), instructions(report, in.Session, in.Listening), in.Text)
	}
	for _, a := range g.Asks {
		ask := newAsk(a.ID, a.Kind, "s1", a.Request)
		same(t, "details "+a.ID, ask.details(), a.Details)
		same(t, "note "+a.ID, ask.note(), a.Note)
		same(t, "briefing "+a.ID, ask.briefing(), a.Briefing)
	}
	for _, a := range g.Answered {
		ask := newAsk(a.ID, a.Kind, "s1", a.Request)
		ask.settle(a.Outcome, a.Said, a.Answers)
		same(t, "answered "+a.ID+" "+a.Outcome, ask.answered(), a.Text)
	}
	questions := newAsk("efgh", "question", "s1", g.Asks[1].Request)
	for i, m := range g.Matches {
		answers, missing := questions.match(m.Given)
		got := [][2]string{}
		for _, p := range answers {
			got = append(got, [2]string{p.Key, p.Value})
		}
		same(t, fmt.Sprint("match ", i), got, m.Answers)
		same(t, fmt.Sprint("missing ", i), missing, m.Missing)
	}
	for _, p := range g.Plain {
		same(t, "plain "+p.Text, plain(p.Text), p.Plain)
	}
	for _, c := range g.Cut {
		same(t, "cut "+c.Text, cut(c.Text, c.Most), c.Cut)
	}
	for _, h := range g.HeldFields {
		heldUntil := ""
		if h.HeldUntil != nil {
			heldUntil = *h.HeldUntil
		}
		same(t, "held fields "+heldUntil, heldFields(heldUntil), h.Fields)
	}
	for _, u := range g.Until {
		same(t, "until "+u.HeldUntil, until(u.HeldUntil), u.Text)
	}
	for _, s := range g.Sentences {
		end := matchEndOfSentence(s.Text)
		same(t, "end of sentence in "+s.Text, utf8.RuneCountInString(s.Text[:end]), s.End)
	}
	for _, r := range g.Reprs {
		same(t, "repr", pyrepr(r.Text), r.Repr)
	}
}

// TestGoldenReplies runs sample replies from Haiku through the voice (testdata/golden.json):
// what's spoken, what the tools answer, how many words of the reply you must hear before
// what's held goes, and the notes when you cut the voice off after each word.
func TestGoldenReplies(t *testing.T) {
	g := loadGolden(t)
	var data struct {
		Scripts map[string]struct {
			Said      string
			Model     string
			Listening *bool
			Asks      bool
			Steps     []map[string]any
		}
	}
	raw, _ := os.ReadFile("testdata/golden.json")
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	scripts := data.Scripts
	if len(g.Replies) == 0 || len(g.Replies) != len(scripts) {
		t.Fatalf("%d replies in golden.json, %d scripts", len(g.Replies), len(scripts))
	}
	for _, want := range g.Replies {
		s, ok := scripts[want.Name]
		if !ok {
			t.Fatalf("no script for %q", want.Name)
		}
		c := newCalls(nil, func(string) {}, "")
		call := &Call{ID: "c1", Session: "s1"}
		link := c.newLink(call)
		if s.Listening != nil {
			link.listening = *s.Listening
		}
		if s.Asks {
			link.asks.set("perm", newAsk("perm", "permission", "s1", g.Asks[0].Request))
			link.asks.set("ques", newAsk("ques", "question", "s1", g.Asks[1].Request))
		}
		talker := newTalker(chosenModel{}, "", func(string) {}, true, "")
		if s.Model != "" {
			talker.model = chosenModel{flag: s.Model}
		}
		var spoken, answers []string
		// What's spoken comes from the stream, and from a tool call that flushes it.
		say := func(m map[string]any) { spoken = append(spoken, m["text"].(string)) }
		v := &voice{calls: c, callID: "c1", link: link, talker: talker, log: func(string) {}, out: say, said: s.Said, turn: 2, unfinished: 1, writing: true}
		for _, step := range s.Steps {
			if name, ok := step["tool"].(string); ok {
				input, _ := step["input"].(map[string]any)
				answers = append(answers, v.tool(name, input))
				continue
			}
			if text := v.nextText(step); text != "" {
				spoken = append(spoken, text)
			}
		}
		same(t, want.Name+": spoken", spoken, nilIfEmpty(want.Spoken))
		same(t, want.Name+": reply", v.reply, want.Reply)
		same(t, want.Name+": tools", answers, nilIfEmpty(want.Answers))
		same(t, want.Name+": unsaid", v.unsaid, want.Unsaid)
		if len(v.held) != len(want.Held) {
			t.Errorf("%s: %d held, want %d", want.Name, len(v.held), len(want.Held))
			continue
		}
		for i, h := range want.Held {
			got := v.held[i]
			same(t, want.Name+": held kind", got.kind, h.Kind)
			same(t, want.Name+": held text", got.text, h.Text)
			same(t, want.Name+": held words", got.words, h.Words)
			same(t, want.Name+": needed", v.needed(got), h.Needed)
		}
		for _, cut := range want.Cuts {
			copied := &voice{calls: c, callID: "c1", link: link, talker: talker, log: func(string) {}, reply: v.reply, ack: v.ack, turn: v.turn}
			for _, h := range v.held {
				h := *h
				h.give = func() bool { return true }
				copied.held = append(copied.held, &h)
			}
			heard := cut.Heard
			copied.cutOff = &heard
			note := copied.cutOffNote()
			got := copied.heardWords
			note += copied.pass(func(h *held) bool { return got == nil || copied.needed(h) <= *got }, "you cut the voice off first")
			same(t, fmt.Sprintf("%s: note when cut off at %q", want.Name, cut.Heard), note, cut.Note)
			same(t, fmt.Sprintf("%s: held left when cut off at %q", want.Name, cut.Heard), len(copied.held), cut.Left)
		}
	}
}

func nilIfEmpty(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	return list
}
