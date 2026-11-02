package hearing

import "time"

// TurnModel is Smart Turn v3: from Whisper features of the last 8 s of your speech (80·800
// floats) it gives the probability that you've finished your turn.
type TurnModel interface {
	Run(features []float32) (float32, error)
}

// TurnWait is how long a pause ends your turn even if Smart Turn thinks you haven't finished,
// as it sometimes does with a short "mm-hmm" (TURN_WAIT in voice/ringback_voice/__main__.py).
const TurnWait = 1.5

const (
	preSpeechSecs   = 0.5 // audio before your speech started that Smart Turn hears too
	maxDurationSecs = 8.0 // Smart Turn hears at most the last 8 s
)

type chunk struct {
	at      float64 // seconds of audio up to the end of this chunk
	samples []int16
}

// turnAnalyzer is Pipecat's BaseSmartTurn with LocalSmartTurnAnalyzerV3: it keeps the turn's
// audio, counts the pause after you stop, and runs Smart Turn when you stop. Times are seconds
// of audio, where Pipecat uses the clock, which is the same for audio that comes in real time.
type turnAnalyzer struct {
	model    TurnModel
	stopSecs float64

	buffer          []chunk
	speechTriggered bool
	silence         float64 // seconds since you stopped speaking
	speechStart     float64
	hasStart        bool
	vadStartSecs    float64
	lastProbability float32
	lastTook        time.Duration // how long Smart Turn took, features and model
	err             error
}

// appendAudio adds a frame that ends at audio time now; it returns true once the pause after
// your speech has lasted the turn wait.
func (t *turnAnalyzer) appendAudio(samples []int16, isSpeech bool, now float64) bool {
	t.buffer = append(t.buffer, chunk{now, samples})
	if isSpeech {
		t.silence = 0
		t.speechTriggered = true
		if !t.hasStart {
			t.speechStart, t.hasStart = now, true
		}
		return false
	}
	if t.speechTriggered {
		t.silence += float64(len(samples)) / Rate
		if t.silence >= t.stopSecs {
			t.clear(true)
			return true
		}
		return false
	}
	// Before speech, keep only what a turn could need.
	keep := preSpeechSecs + t.stopSecs + maxDurationSecs
	drop := 0
	for drop < len(t.buffer) && t.buffer[drop].at < now-keep {
		drop++
	}
	if drop > 0 {
		t.buffer = append(t.buffer[:0:0], t.buffer[drop:]...)
	}
	return false
}

// analyze runs Smart Turn on the turn's audio so far and says whether the turn is complete;
// a complete turn clears the analyzer.
func (t *turnAnalyzer) analyze() bool {
	if len(t.buffer) == 0 {
		return false
	}
	from := t.speechStart - (preSpeechSecs + t.vadStartSecs)
	start := 0
	for i, c := range t.buffer {
		if c.at >= from {
			start = i
			break
		}
	}
	n := 0
	for _, c := range t.buffer[start:] {
		n += len(c.samples)
	}
	if n == 0 {
		return false
	}
	// The last 8 s, padded with silence in front to exactly 8 s.
	audio := make([]float32, featureSamples)
	skip := n - featureSamples // samples too early to keep
	pos := featureSamples - n
	if pos < 0 {
		pos = 0
	}
	for _, c := range t.buffer[start:] {
		for _, s := range c.samples {
			if skip > 0 {
				skip--
				continue
			}
			audio[pos] = float32(s) / 32768
			pos++
		}
	}
	began := time.Now()
	p, err := t.model.Run(logMel(audio))
	t.lastTook = time.Since(began)
	if err != nil {
		t.err = err
		return false
	}
	t.lastProbability = p
	if p > 0.5 {
		t.clear(true)
		return true
	}
	return false
}

// clear forgets the turn's audio; after an incomplete turn, speech counts as having started.
func (t *turnAnalyzer) clear(complete bool) {
	t.speechTriggered = !complete
	t.buffer = nil
	t.hasStart = false
	t.silence = 0
}
