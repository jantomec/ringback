package hearing

import "math"

// Silero is the voice-activity model. Run takes one window, the last 64 samples of the window
// before it as context and then 512 new ones (576 floats in -1…1), and the model's recurrent
// state (2·1·128 floats), which it replaces with the new state; it returns the probability
// of speech.
type Silero interface {
	Run(input []float32, state []float32) (float32, error)
}

const (
	sileroWindow  = 512
	sileroContext = 64
	sileroState   = 2 * 128
	// The model's state is reset every 2 s of audio, when it's clearly not speech: non-speech
	// shifts it so that it under-reports the speech that follows (Pipecat's silero.py).
	resetSecs       = 2.0
	resetConfidence = 0.1
	resetWindows    = 5
)

type vadState int

const (
	quiet vadState = iota + 1
	starting
	speaking
	stopping
)

// analyzer is Pipecat's VADAnalyzer with SileroVADAnalyzer: Silero's confidence and the
// volume of each 32 ms window, through a quiet/starting/speaking/stopping state machine. The
// volume is measured differently (speechVolume).
type analyzer struct {
	model       Silero
	params      VADParams
	startFrames int
	stopFrames  int

	buf        []int16 // samples not yet in a window
	state      vadState
	startCount int
	stopCount  int
	modelState [sileroState]float32
	context    [sileroContext]float32
	sinceReset int // windows since the model's state was reset
	lowWindows int // windows in a row that were clearly not speech
	volumeBuf  []int16
	voicedRun  int // windows in a row Silero has taken for speech
	input      [sileroContext + sileroWindow]float32
	err        error // the model's last failure, if any

	// A stretch Silero takes for speech while you aren't speaking (see Missed): its windows,
	// those too quiet for the gate, and its peaks.
	heard, gated   int
	peakVolume     float64
	peakConfidence float32
	missed         *Missed // the last one that ended without starting speech, until taken
}

// Missed is a stretch that Silero took for speech but that didn't start speaking: too quiet
// for the background filter for part of it, or shorter than the sensitivity needs. It changes
// nothing; the node logs it, to tell why something you said went unheard.
type Missed struct {
	Secs       float64 // how long Silero heard speech
	Quiet      float64 // how much of that was quieter than the gate
	Volume     float64 // its loudest, 0 to 1 for -110 to -10 LUFS
	Confidence float64 // Silero's highest
	Gate       float64 // the background filter's MinVolume
	Needed     float64 // the speech the sensitivity needs, StartSecs
}

// minMissed is the shortest stretch reported as missed, in windows (96 ms): Silero's single
// windows on noise aren't worth a line.
const minMissed = 3

func newAnalyzer(model Silero, params VADParams) *analyzer {
	a := &analyzer{model: model}
	a.setParams(params)
	return a
}

func (a *analyzer) setParams(p VADParams) {
	a.params = p
	secsPerWindow := float64(sileroWindow) / Rate
	a.startFrames = int(math.RoundToEven(p.StartSecs / secsPerWindow))
	a.stopFrames = int(math.RoundToEven(p.StopSecs / secsPerWindow))
	a.startCount, a.stopCount = 0, 0
	a.state = quiet
}

// analyze adds audio and returns the state after the windows it completes.
func (a *analyzer) analyze(samples []int16) vadState {
	a.buf = append(a.buf, samples...)
	if len(a.buf) < sileroWindow {
		return a.state
	}
	for len(a.buf) >= sileroWindow {
		window := a.buf[:sileroWindow]
		confidence := a.confidence(window)
		// numpy compares the model's float32 with the threshold as float32
		voiced := confidence >= float32(a.params.Confidence)
		volume := a.speechVolume(window, voiced)
		a.buf = a.buf[sileroWindow:]

		isSpeech := voiced && volume >= a.params.MinVolume
		a.track(voiced, isSpeech, confidence, volume)
		if isSpeech {
			switch a.state {
			case quiet:
				a.state, a.startCount = starting, 1
			case starting:
				a.startCount++
			case stopping:
				a.state, a.stopCount = speaking, 0
			}
		} else {
			switch a.state {
			case starting:
				a.state, a.startCount = quiet, 0
			case speaking:
				a.state, a.stopCount = stopping, 1
			case stopping:
				a.stopCount++
			}
		}
	}
	a.buf = append(a.buf[:0:0], a.buf...) // don't keep the consumed samples alive
	if a.state == starting && a.startCount >= a.startFrames {
		a.state, a.startCount = speaking, 0
		a.heard, a.gated, a.peakVolume, a.peakConfidence = 0, 0, 0, 0 // it was heard
	}
	if a.state == stopping && a.stopCount >= a.stopFrames {
		a.state, a.stopCount = quiet, 0
	}
	return a.state
}

// track follows a stretch Silero takes for speech while you aren't speaking, and keeps it as
// missed when it ends without speech having started.
func (a *analyzer) track(voiced, isSpeech bool, confidence float32, volume float64) {
	if a.state != quiet && a.state != starting {
		return
	}
	if voiced {
		a.heard++
		if !isSpeech {
			a.gated++
		}
		a.peakVolume = max(a.peakVolume, volume)
		a.peakConfidence = max(a.peakConfidence, confidence)
		return
	}
	if a.heard >= minMissed {
		secs := float64(sileroWindow) / Rate
		a.missed = &Missed{
			Secs: float64(a.heard) * secs, Quiet: float64(a.gated) * secs, Volume: a.peakVolume,
			Confidence: float64(a.peakConfidence), Gate: a.params.MinVolume, Needed: a.params.StartSecs,
		}
	}
	a.heard, a.gated, a.peakVolume, a.peakConfidence = 0, 0, 0, 0
}

// takeMissed returns the last stretch missed since it was last taken, if any.
func (a *analyzer) takeMissed() *Missed {
	m := a.missed
	a.missed = nil
	return m
}

// confidence runs Silero on one window and resets its state when that's due.
func (a *analyzer) confidence(window []int16) float32 {
	copy(a.input[:sileroContext], a.context[:])
	for i, s := range window {
		a.input[sileroContext+i] = float32(s) / 32768
	}
	p, err := a.model.Run(a.input[:], a.modelState[:])
	copy(a.context[:], a.input[sileroWindow:])
	if err != nil {
		a.err = err
		return 0
	}
	a.sinceReset++
	if p < resetConfidence {
		a.lowWindows++
	} else {
		a.lowWindows = 0
	}
	if float64(a.sinceReset*sileroWindow) >= resetSecs*Rate && a.state == quiet && a.lowWindows >= resetWindows {
		a.modelState = [sileroState]float32{}
		a.context = [sileroContext]float32{}
		a.sinceReset = 0
	}
	return p
}

// speechVolume adds a window to the last 400 ms of audio and returns the volume the
// background filter compares with its gate: that of the speech Silero hears, over as much of
// it as has come, up to those 400 ms (outside speech, the whole block's). Pipecat's
// AudioVolumeTracker takes the whole 400 ms, silence before the speech included, and smooths
// it from one window to the next, so a short word's volume rose only as the word ended: a
// "yes" of 0.38 s at -33 LUFS never passed the gate at -40 (tools/short-answers).
func (a *analyzer) speechVolume(window []int16, voiced bool) float64 {
	a.volumeBuf = append(a.volumeBuf, window...)
	if over := len(a.volumeBuf) - volumeWindow; over > 0 {
		a.volumeBuf = append(a.volumeBuf[:0], a.volumeBuf[over:]...)
	}
	if !voiced {
		a.voicedRun = 0
		return volume(a.volumeBuf)
	}
	a.voicedRun++
	n := min(len(a.volumeBuf), a.voicedRun*sileroWindow)
	return volume(a.volumeBuf[len(a.volumeBuf)-n:])
}
