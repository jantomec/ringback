package main

import (
	"testing"
	"time"
)

// A call with a stand-in link, playing piece of secs seconds.
func pausedCall(text string, secs, heardSecs float64) (*Call, *piece) {
	c := &Call{id: "test", short: "test", replying: true, replyNo: 2, lastReply: 2}
	p := &piece{id: 1, text: text, samples: int(secs * speakingRate), done: true}
	c.pieces = []*piece{p}
	frames := min(int(heardSecs/frameSecs), int(secs/frameSecs))
	for i := 0; i < frames; i++ {
		c.played = append(c.played, outFrame{piece: 1})
	}
	for i := frames; i < int(secs/frameSecs); i++ {
		c.queue = append(c.queue, outFrame{piece: 1})
	}
	t := now()
	p.start = t.Add(-time.Duration(heardSecs * float64(time.Second)))
	c.pausedAt, c.paused = t, true
	return c, p
}

func TestRewindGoesBackToTheSentence(t *testing.T) {
	// 6 s for 60 characters: "The first sentence, in full." ends at 28, 2.8 s in.
	text := "The first sentence, in full. And the second one goes on and."
	c, p := pausedCall(text, 6, 4)
	queued := len(c.queue)
	back := c.rewind()
	// Stopped 4 s in, in the second sentence, which began at character 29.
	want := 4 - 6*float64(sentenceStart(text[:40]))/float64(len(text))
	if back < want-0.05 || back > want+0.05 {
		t.Errorf("went back %.2f s, want about %.2f", back, want)
	}
	if got := len(c.queue) - queued; got != int(back/frameSecs+0.5) {
		t.Errorf("%d frames went back on the queue for %.2f s", got, back)
	}
	if heard := c.heard(now()); heard != "The first sentence, in full." {
		t.Errorf("after going back you've heard %q", heard)
	}
	if p.start.After(now()) {
		t.Error("its start is in the future")
	}
}

func TestRewindInTheFirstSentence(t *testing.T) {
	c, _ := pausedCall("Only one sentence here, which goes on for a while yet.", 5, 2)
	if back := c.rewind(); back < 1.9 || back > 2.1 {
		t.Errorf("went back %.2f s, want all 2 s", back)
	}
	if len(c.played) != 0 {
		t.Errorf("%d frames left as played", len(c.played))
	}
}

func TestRewindAtAPiecesEnd(t *testing.T) {
	c, _ := pausedCall("A whole sentence.", 2, 2.5)
	if back := c.rewind(); back != 0 {
		t.Errorf("went back %.2f s, for a piece played to its end", back)
	}
}

func TestSentenceStart(t *testing.T) {
	for _, c := range []struct {
		text string
		want int
	}{
		{"", 0},
		{"Half a sen", 0},
		{"One. Two", 5},
		{"One. Two. Thr", 10},
		{"Version 3.14 is out. It", 21},
		{"One.", 4},
	} {
		if got := sentenceStart(c.text); got != c.want {
			t.Errorf("sentenceStart(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

// interruptedReply is the reply named in the next `interrupted` the agent got.
func interruptedReply(t *testing.T, got chan map[string]any) int {
	t.Helper()
	for {
		select {
		case m := <-got:
			if m["type"] == "interrupted" {
				n, _ := m["reply"].(float64)
				return int(n)
			}
		case <-time.After(time.Second):
			t.Fatal("the agent wasn't told of an interruption")
			return 0
		}
	}
}

// Going on twice while the model decides, as on a call of 2026-11-22: the first cut a reply that
// never came, so the agent's next reply has the number after it, and the second must cut that
// one. It named the cut reply again, so the next reply was spoken and hung up the call.
func TestGoingOnAgainCutsTheNextReply(t *testing.T) {
	for _, paused := range []bool{true, false} {
		agent, got := recorder(t, false)
		c := &Call{id: "again", short: "again", agent: agent, lastReply: 2}
		if paused {
			c.replying, c.replyNo, c.paused = true, 2, true
		}
		c.turn("Oh okay.", 0.4, 0.1, false)
		c.speechStarted()
		if n := interruptedReply(t, got); n != 3 {
			t.Fatalf("paused %v: going on cut reply %d, want 3", paused, n)
		}
		c.speechStopped()
		c.turn("All right, that's it.", 1.9, 0.1, true)
		c.speechStarted()
		if n := interruptedReply(t, got); n != 4 {
			t.Fatalf("paused %v: going on again cut reply %d, want 4, the agent's next", paused, n)
		}
		c.write(4, "Talk soon!")
		if c.replying && c.replyNo == 4 {
			t.Fatalf("paused %v: the reply cut off is spoken", paused)
		}
	}
}
