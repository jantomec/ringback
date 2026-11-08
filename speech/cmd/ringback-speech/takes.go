package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"ringback/speech/internal/audiocpp"
)

// takes speaks with a voice that makes a whole reply in one take (Qwen3 through audio.cpp,
// "Speaking a reply" in docs/design.md): one piece at a time, each streamed to the hub as
// it's made. It runs beside the GPU worker's queue, not in it: the voice is another
// process, so your words are transcribed while it speaks.
type takes struct {
	n      *node
	voice  *audiocpp.Server
	mu     sync.Mutex
	wake   chan struct{}
	queue  []*piece
	epochs map[uint32]uint64  // a stream's cancellations
	now    *piece             // being spoken
	cut    context.CancelFunc // cuts now off
	secs   float64            // the audio of the pieces spoken whole, for the voice's pace
	chars  int                // and their characters
}

func newTakes(n *node, voice *audiocpp.Server) *takes {
	t := &takes{n: n, voice: voice, wake: make(chan struct{}, 1), epochs: map[uint32]uint64{}}
	go t.run()
	return t
}

func (t *takes) speak(p *piece) {
	t.mu.Lock()
	p.epoch = t.epochs[p.stream]
	t.queue = append(t.queue, p)
	t.mu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// cancel drops the stream's pieces and cuts off the one being spoken, if it's the stream's.
func (t *takes) cancel(stream uint32) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.epochs[stream]++
	kept := t.queue[:0]
	for _, p := range t.queue {
		if p.stream != stream {
			kept = append(kept, p)
		}
	}
	dropped := len(t.queue) - len(kept)
	t.queue = kept
	if t.now != nil && t.now.stream == stream {
		t.cut()
		dropped++
	}
	return dropped
}

func (t *takes) busy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.queue) > 0 || t.now != nil
}

func (t *takes) run() {
	for {
		t.mu.Lock()
		if len(t.queue) == 0 {
			t.mu.Unlock()
			<-t.wake
			continue
		}
		p := t.queue[0]
		t.queue = t.queue[1:]
		ctx, cut := context.WithCancel(context.Background())
		t.now, t.cut = p, cut
		t.mu.Unlock()
		t.take(ctx, p)
		cut()
		t.mu.Lock()
		t.now, t.cut = nil, nil
		t.mu.Unlock()
	}
}

// What the voice says after its server was started again partway through a reply, before
// it goes on from the sentence it was in; or before the reply, when the server failed before
// any of it was spoken.
const (
	crashed = "The voice server crashed and has been restarted."
	goingOn = "Continuing from the last sentence."
)

// take speaks one piece. When the voice's server hangs or dies partway, it's started again
// and the piece goes on from the sentence it was in, after saying so; a server that fails
// a third time in one piece leaves it cut short.
func (t *takes) take(ctx context.Context, p *piece) {
	start := time.Now()
	text := []rune(spokenText(p.text))
	from, notice := 0, "" // where in text this try starts, and what it says first
	for try := 1; ; try++ {
		sent := 0 // samples this try
		err := t.voice.Speak(ctx, notice+string(text[from:]), func(samples []float32) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.epochs[p.stream] != p.epoch {
				return // cut off: what was still on its way is dropped
			}
			t.n.sendAudio(p.stream, p.number, samples)
			p.samples += len(samples)
			sent += len(samples)
		})
		switch {
		case err == nil:
		case errors.Is(err, context.Canceled):
			logf("stream %d: voice piece %d cut off after %.1f s", p.stream, p.number, float64(p.samples)/speakRate)
			return
		case errors.Is(err, audiocpp.ErrHung):
			logf("stream %d piece %d: %v, %.1f s into the piece; it's started again", p.stream, p.number, err, float64(p.samples)/speakRate)
			restart := time.Now()
			if rerr := t.voice.Restart(); rerr != nil {
				logf("the voice's server didn't start again: %v", rerr)
				fail("the voice's server failed: " + rerr.Error()) // the link process starts a new worker
			}
			logf("the voice's server is back in %.1f s", time.Since(restart).Seconds())
			if try == 3 || ctx.Err() != nil {
				break
			}
			if p.samples == 0 {
				notice = crashed + " "
				continue
			}
			at, rest := resumeAt(text, from, len([]rune(notice)), float64(sent)/speakRate, t.pace())
			if !rest {
				break // all of it was spoken
			}
			from, notice = at, crashed+" "+goingOn+" "
			logf("stream %d piece %d: going on from %q", p.stream, p.number, string(text[from:min(len(text), from+40)]))
			continue
		default:
			logf("stream %d piece %d: %v", p.stream, p.number, err)
		}
		break
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.epochs[p.stream] != p.epoch {
		return
	}
	secs := float64(p.samples) / speakRate
	took := time.Since(start)
	if from == 0 && notice == "" && secs > 0 {
		t.secs += secs
		t.chars += len(text)
	}
	logf("stream %d: voice piece %d (%.1f s, made in %.2f s): %q", p.stream, p.number, secs, took.Seconds(), p.text)
	t.n.out(map[string]any{"type": "spoken", "stream": p.stream, "piece": p.number, "secs": round3(secs), "took": round3(took.Seconds())})
}

// pace is the voice's seconds a character, from the pieces it has spoken whole.
func (t *takes) pace() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.chars < 200 {
		return 0.05 // Hadley, from replies of 50 to 300 words (tools/tts-trial)
	}
	return t.secs / float64(t.chars)
}

// resumeAt is where a take that stopped partway goes on from: the start of the sentence it
// was in. The take began at from in text, said notice characters first, and sent secs of
// audio; the voice's pace places that in the text, and a margin for the pace's error goes
// back a little further, so a sentence is said again rather than skipped. rest is false if
// all of it was sent.
func resumeAt(text []rune, from, notice int, secs, pace float64) (at int, rest bool) {
	said := int(secs/pace) - notice // characters of text the audio covers
	if from+said >= len(text) {
		return len(text), false
	}
	back := max(int(1/pace), said*15/100) // a second, or 15% of the way
	return sentenceStart(text, from, max(from, from+said-back)), true
}

// sentenceStart is where the sentence that holds text[i] begins, not before from.
func sentenceStart(text []rune, from, i int) int {
	for ; i > from; i-- {
		if text[i-1] != ' ' {
			continue
		}
		j := i - 1
		for j > 0 && strings.ContainsRune(`"')]”’`, text[j-1]) {
			j--
		}
		if j > 0 && strings.ContainsRune(".!?…", text[j-1]) {
			return i
		}
	}
	return from
}

// spokenText is the reply as the voice reads it: one line, since a reply's line breaks
// aren't pauses the voice needs to hear.
func spokenText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// serverLog passes audio.cpp's server's lines to the node's log, less those it writes for
// every take.
type serverLog struct{ rest []byte }

func (l *serverLog) Write(b []byte) (int, error) {
	l.rest = append(l.rest, b...)
	for {
		i := bytes.IndexByte(l.rest, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(l.rest[:i]))
		l.rest = l.rest[i+1:]
		if line != "" && !strings.Contains(line, "warmup complete") && !strings.HasPrefix(line, "[SERVER_HTTP_DEBUG]") &&
			!strings.HasPrefix(line, "[TRACE") && !strings.HasPrefix(line, "[TIMING") {
			logf("audio.cpp: %s", line)
		}
	}
	return len(b), nil
}
