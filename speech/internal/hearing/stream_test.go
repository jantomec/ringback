package hearing

import (
	"math"
	"testing"
)

// certain is a Smart Turn that's sure of its verdict, whatever it hears.
type certain struct{ p float32 }

func (c certain) Run([]float32) (float32, error) { return c.p, nil }

type events struct {
	turns   []string
	asked   []int
	started int
}

func (e *events) SpeechStarted()                 { e.started++ }
func (e *events) SpeechStopped()                 {}
func (e *events) Turn(text string, _, _ float64) { e.turns = append(e.turns, text) }
func (e *events) Transcribe(id int, _ []float32) { e.asked = append(e.asked, id) }
func (e *events) Missed(Missed)                  {}

// hear plays stretches of speech with pauses between them, in seconds, and writes each down as
// the next of words.
func hear(t *testing.T, smartTurn float32, words []string, speech, pause []float64) *events {
	t.Helper()
	const window = sileroWindow
	frame := func(voiced bool) []int16 {
		w := make([]int16, window)
		for i := range w {
			if voiced {
				w[i] = int16(0.3 * 32767 * math.Sin(2*math.Pi*440*float64(i)/Rate))
			}
		}
		return w
	}
	windows := func(secs float64) int { return int(secs * Rate / window) }
	silero := &scripted{}
	var audio [][]int16
	add := func(voiced bool, n int) {
		for i := 0; i < n; i++ {
			p := float32(0.05)
			if voiced {
				p = 0.9
			}
			silero.next = append(silero.next, p)
			audio = append(audio, frame(voiced))
		}
	}
	add(false, 20)
	for i, secs := range speech {
		add(true, windows(secs))
		add(false, windows(pause[i]))
	}
	e := &events{}
	s := NewStream(e, silero, certain{smartTurn}, Thresholds(DefaultSensitivity, DefaultGate))
	answered := 0
	for _, w := range audio {
		s.Audio(w)
		for ; answered < len(e.asked); answered++ {
			s.Transcribed(e.asked[answered], words[answered], 0.05)
		}
	}
	return e
}

// Smart Turn's verdict no longer ends a turn: only the pause does, whatever it says.
func TestTurnEndsAtThePause(t *testing.T) {
	for _, p := range []float32{0.99, 0.01} {
		e := hear(t, p, []string{"Yes."}, []float64{0.5}, []float64{1})
		if len(e.turns) != 1 || e.turns[0] != "Yes." {
			t.Errorf("Smart Turn %.2f: turns %q", p, e.turns)
		}
	}
}

// A pause shorter than the turn wait is one turn, even when Smart Turn says you've finished.
func TestShortPauseStaysInTheTurn(t *testing.T) {
	e := hear(t, 0.99, []string{"So the plan", "is good."}, []float64{0.6, 0.6}, []float64{0.45, 1})
	if len(e.turns) != 1 || e.turns[0] != "So the plan is good." {
		t.Errorf("turns %q", e.turns)
	}
}

// A longer pause sends your words so far, and what follows is the next turn.
func TestLongPauseSendsWhatYouSaid(t *testing.T) {
	e := hear(t, 0.01, []string{"So the plan", "is good."}, []float64{0.6, 0.6}, []float64{1.2, 1})
	if len(e.turns) != 2 || e.turns[0] != "So the plan" || e.turns[1] != "is good." {
		t.Errorf("turns %q", e.turns)
	}
}

func TestNoTurnBeforeTheWait(t *testing.T) {
	e := hear(t, 0.99, []string{"Yes."}, []float64{0.5}, []float64{0.45})
	if len(e.turns) != 0 {
		t.Errorf("a turn after a pause of 0.45 s: %q", e.turns)
	}
}
