package main

import (
	"strings"
	"testing"
)

// Where a take that stopped partway goes on from: the start of the sentence it was in, or
// the one before when it stopped just after a sentence began, since the voice's pace places
// it only roughly.
func TestResumeAt(t *testing.T) {
	text := "The parser fix is done. All the tests pass now, every one. I pushed the branch to main. Shall I tell the team about it this afternoon, or wait until tomorrow?"
	r := []rune(text)
	at := func(s string) int { return strings.Index(text, s) }
	const pace = 0.05
	secs := func(chars int) float64 { return float64(chars) * pace }
	for _, c := range []struct {
		name         string
		from, notice int
		sent         float64
		want         int
		rest         bool
	}{
		{"deep in the second sentence", 0, 0, secs(at("every") + 4), at("All"), true},
		{"just into the third", 0, 0, secs(at("I pushed") + 3), at("All"), true},
		{"all of it sent", 0, 0, secs(len(r) + 5), len(r), false},
		{"a try that began at the third, stopped in its notice", at("I pushed"), 60, secs(40), at("I pushed"), true},
		{"a try that began at the third, stopped in the fourth", at("I pushed"), 60, secs(60 + at("afternoon") - at("I pushed")), at("Shall"), true},
	} {
		got, rest := resumeAt(r, c.from, c.notice, c.sent, pace)
		if got != c.want || rest != c.rest {
			t.Errorf("%s: at %d %q (rest %v), want %d %q", c.name, got, string(r[got:]), rest, c.want, string(r[c.want:]))
		}
	}
}

func TestSentenceStart(t *testing.T) {
	text := []rune(`It said "done." Then it stopped. 3.5 s later, e.g. now`)
	for _, c := range []struct {
		i    int
		want string
	}{
		{20, "Then it stopped."},
		{40, "3.5 s later"},
		{2, "It said"},
	} {
		got := string(text[sentenceStart(text, 0, c.i):])
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("sentence holding %d starts %q, want %q", c.i, got, c.want)
		}
	}
}
