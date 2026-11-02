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
	smoothing       = 0.2 // of the volume, from one window to the next
)

type vadState int

const (
	quiet vadState = iota + 1
	starting
	speaking
	stopping
)

// analyzer is Pipecat's VADAnalyzer with SileroVADAnalyzer: Silero's confidence and the
// volume of each 32 ms window, through a quiet/starting/speaking/stopping state machine.
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
	prevVolume float64
	input      [sileroContext + sileroWindow]float32
	err        error // the model's last failure, if any
}

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
		volume := a.smoothedVolume(window)
		a.prevVolume = volume
		a.buf = a.buf[sileroWindow:]

		// numpy compares the model's float32 with the threshold as float32
		isSpeech := confidence >= float32(a.params.Confidence) && volume >= a.params.MinVolume
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
	}
	if a.state == stopping && a.stopCount >= a.stopFrames {
		a.state, a.stopCount = quiet, 0
	}
	return a.state
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

// smoothedVolume adds a window to the last 400 ms of audio and returns its volume, smoothed;
// the volume is 0 until 400 ms have come (Pipecat's AudioVolumeTracker).
func (a *analyzer) smoothedVolume(window []int16) float64 {
	a.volumeBuf = append(a.volumeBuf, window...)
	if over := len(a.volumeBuf) - volumeWindow; over > 0 {
		a.volumeBuf = append(a.volumeBuf[:0], a.volumeBuf[over:]...)
	}
	v := 0.0
	if len(a.volumeBuf) == volumeWindow {
		v = volume(a.volumeBuf)
	}
	return a.prevVolume + smoothing*(v-a.prevVolume)
}
