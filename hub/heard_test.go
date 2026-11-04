package main

import (
	"testing"
	"time"
)

// What you've heard never shrinks below what the agent was told, though a piece's audio,
// still coming, makes the share of it played smaller: before, the agent could be told an
// empty heard.
func TestHeardSoFar(t *testing.T) {
	start := time.Now()
	p := &piece{text: "Going on now.", samples: speakingRate * 4 / 10, start: start} // 0.4 s of audio so far
	c := &Call{pieces: []*piece{p}}
	at := start.Add(350 * time.Millisecond)
	first := c.heardSoFar(at)
	if first != "Going on" {
		t.Fatalf("0.35 s into 0.4 s: %q", first)
	}
	c.heardSent = first
	p.samples = speakingRate * 12 / 10 // the rest comes: 1.2 s in all
	at = at.Add(50 * time.Millisecond)
	if plain := c.heard(at); plain != "" {
		t.Fatalf("the share played is now less than the first word: %q", plain)
	}
	if got := c.heardSoFar(at); got != "Going on" {
		t.Errorf("what you've heard shrank to %q", got)
	}
	p.done = true
	if got := c.heardSoFar(start.Add(1300 * time.Millisecond)); got != "Going on now." {
		t.Errorf("played to its end: %q", got)
	}
}
