package hearing

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// certain is a Smart Turn that's sure of its verdict, whatever it hears.
type certain struct{ p float32 }

func (c certain) Run([]float32) (float32, error) { return c.p, nil }

type turnEvent struct {
	text  string
	again bool
}

type asked struct {
	id    int
	audio []float32
}

type events struct {
	turns   []turnEvent
	asks    []asked
	started int
}

func (e *events) SpeechStarted() { e.started++ }
func (e *events) SpeechStopped() {}
func (e *events) Turn(text string, _, _ float64, again bool) {
	e.turns = append(e.turns, turnEvent{text, again})
}
func (e *events) Transcribe(id int, audio []float32) { e.asks = append(e.asks, asked{id, audio}) }
func (e *events) Missed(Missed)                      {}

// Stretch n of a call is a tone (toneOf) of loudness 0.2 + 0.02·n, and heardIn writes it down as "wn":
// so what a transcription gives is which stretches its audio held, in order.
func toneOf(n int) float64 { return 0.2 + 0.02*float64(n) }

func heardIn(audio []float32) string {
	var words []string
	peak, zeros := float32(0), 0
	end := func() {
		if peak > 0.1 {
			words = append(words, fmt.Sprintf("w%d", int(math.Round(float64(peak-0.2)/0.02))))
		}
		peak = 0
	}
	for _, v := range audio {
		if v == 0 {
			if zeros++; zeros == 100 {
				end()
			}
			continue
		}
		zeros = 0
		peak = max(peak, float32(math.Abs(float64(v))))
	}
	end()
	return strings.Join(words, " ")
}

// call is stretches of speech with pauses after them, in seconds; when the voice starts
// speaking, in seconds from the start; how many transcriptions, from the first, come back with
// nothing; and Smart Turn's verdict.
type call struct {
	speech, pause []float64
	voice         []float64
	blank         int
	smartTurn     float32
}

func (c call) hear(t *testing.T) *events {
	t.Helper()
	const window = sileroWindow
	frame := func(n int) []int16 {
		w := make([]int16, window)
		for i := range w {
			if n >= 0 {
				w[i] = int16(toneOf(n) * 32767 * math.Sin(2*math.Pi*440*float64(i)/Rate))
			}
		}
		return w
	}
	windows := func(secs float64) int { return int(secs * Rate / window) }
	silero := &scripted{}
	var audio [][]int16
	add := func(n, count int) {
		for i := 0; i < count; i++ {
			p := float32(0.05)
			if n >= 0 {
				p = 0.9
			}
			silero.next = append(silero.next, p)
			audio = append(audio, frame(n))
		}
	}
	add(-1, 20)
	for i, secs := range c.speech {
		add(i, windows(secs))
		add(-1, windows(c.pause[i]))
	}
	e := &events{}
	s := NewStream(e, silero, certain{c.smartTurn}, Thresholds(DefaultSensitivity, DefaultGate))
	answered, voice := 0, c.voice
	for _, w := range audio {
		s.Audio(w)
		for ; answered < len(e.asks); answered++ {
			text := heardIn(e.asks[answered].audio)
			if answered < c.blank {
				text = ""
			}
			s.Transcribed(e.asks[answered].id, text, 0.05)
		}
		if len(voice) > 0 && s.now >= voice[0] {
			s.VoiceSpeaking()
			voice = voice[1:]
		}
	}
	return e
}

func (e *events) want(t *testing.T, want ...turnEvent) {
	t.Helper()
	if fmt.Sprint(e.turns) != fmt.Sprint(want) {
		t.Errorf("turns %v, want %v", e.turns, want)
	}
}

// Smart Turn's verdict doesn't end a turn: only the pause does, whatever it says.
func TestTurnEndsAtThePause(t *testing.T) {
	for _, p := range []float32{0.99, 0.01} {
		call{speech: []float64{0.5}, pause: []float64{1}, smartTurn: p}.hear(t).want(t, turnEvent{"w0", false})
	}
}

// A pause shorter than the turn wait is one turn, written down whole, once at each stop.
func TestShortPauseStaysInTheTurn(t *testing.T) {
	e := call{speech: []float64{0.6, 0.6}, pause: []float64{0.45, 1}}.hear(t)
	e.want(t, turnEvent{"w0 w1", false})
	if len(e.asks) != 2 {
		t.Errorf("%d transcriptions, want one at each stop", len(e.asks))
	}
}

// A longer pause sends your words so far. If the voice doesn't speak, what follows is the same
// turn, written down again from its start.
func TestLongPauseSendsTheTurnSoFar(t *testing.T) {
	e := call{speech: []float64{0.6, 0.6}, pause: []float64{1.2, 1}}.hear(t)
	e.want(t, turnEvent{"w0", false}, turnEvent{"w0 w1", true})
}

func TestNoTurnBeforeTheWait(t *testing.T) {
	e := call{speech: []float64{0.5}, pause: []float64{0.45}}.hear(t)
	e.want(t)
}

// Once the voice has spoken, what you say next is a turn of its own.
func TestTheVoiceStartsANewTurn(t *testing.T) {
	// The first stop is at 0.4 + 0.6 + 0.2 s, its turn 0.5 s later.
	e := call{speech: []float64{0.6, 0.6}, pause: []float64{2, 1}, voice: []float64{2}}.hear(t)
	e.want(t, turnEvent{"w0", false}, turnEvent{"w1", false})
}

// Speaking over the voice starts the new turn.
func TestSpeakingOverTheVoice(t *testing.T) {
	e := call{speech: []float64{0.6, 1}, pause: []float64{1.2, 1}, voice: []float64{2.6}}.hear(t)
	e.want(t, turnEvent{"w0", false}, turnEvent{"w1", false})
}

// The voice speaking between your stop and its turn: what you said since the last turn is
// written down again on its own.
func TestTheVoiceBeforeYourWordsGo(t *testing.T) {
	// The second stretch stops at 0.4 + 0.6 + 1.2 + 0.6 + 0.2 = 3 s.
	e := call{speech: []float64{0.6, 0.6}, pause: []float64{1.2, 1}, voice: []float64{3.2}}.hear(t)
	e.want(t, turnEvent{"w0", false}, turnEvent{"w1", false})
	if last := heardIn(e.asks[len(e.asks)-1].audio); last != "w1" {
		t.Errorf("the last transcription heard %q, want only the stretch since the last turn", last)
	}
}

// A turn over a minute keeps what its last turn gave, and writes down only what came since.
func TestLongTurnKeepsItsStart(t *testing.T) {
	e := call{speech: []float64{25, 25, 25}, pause: []float64{1, 1, 1}}.hear(t)
	e.want(t, turnEvent{"w0", false}, turnEvent{"w0 w1", true}, turnEvent{"w0 w1 w2", true})
	if last := heardIn(e.asks[len(e.asks)-1].audio); last != "w2" {
		t.Errorf("the last transcription heard %q, want only the stretch after the minute", last)
	}
}

// A turn the voice hasn't answered after a long quiet is let go.
func TestLongQuietLetsTheTurnGo(t *testing.T) {
	e := call{speech: []float64{0.6, 0.6}, pause: []float64{31, 1}}.hear(t)
	e.want(t, turnEvent{"w0", false}, turnEvent{"w1", false})
}

// A turn written down as nothing is asked again from just before your speech began.
func TestNothingHeardIsAskedAgain(t *testing.T) {
	e := call{speech: []float64{0.5}, pause: []float64{1}, blank: 1}.hear(t)
	e.want(t, turnEvent{"w0", false})
	if len(e.asks) != 2 || e.asks[1].id != e.asks[0].id || len(e.asks[1].audio) >= len(e.asks[0].audio) {
		t.Fatalf("asks %d, want the same one again, shorter", len(e.asks))
	}
	if lead := len(e.asks[1].audio) - trailingPad - int((0.5+0.2)*Rate); lead > int(0.2*Rate) {
		t.Errorf("asked again with %.2f s before the speech", float64(lead)/Rate)
	}
}
