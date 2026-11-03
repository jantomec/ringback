// Package hearing decides when you start and stop speaking and when your turn is over, as
// Pipecat 1.14.0 does: Silero's voice-activity detection with a volume gate
// (Pipecat's VADAnalyzer and VADController), Smart Turn v3 (Pipecat's BaseSmartTurn and
// TurnAnalyzerUserTurnStopStrategy) and one transcription per stretch of speech (Pipecat's
// SegmentedSTTService). The models themselves come from outside, through interfaces, so this
// package is plain Go.
package hearing

import "math"

// Rate is the sample rate of your audio: 16 kHz, 16-bit mono.
const Rate = 16000

// VADParams are the thresholds of voice-activity detection, as Pipecat's VADParams.
type VADParams struct {
	Confidence float64 // how sure Silero must be that a window is speech
	StartSecs  float64 // how long speech must last to count as started
	StopSecs   float64 // how long a pause must last to count as stopped
	MinVolume  float64 // how loud speech must be, 0 to 1 for -110 to -10 LUFS
}

// The defaults, which the app's settings start at.
const (
	DefaultSensitivity = 0.5
	DefaultGate        = 0.5
)

// Thresholds at the app's lowest, middle and highest sensitivity, with straight lines in
// between. The middle is Pipecat's defaults.
var (
	lowest  = VADParams{Confidence: 0.85, StartSecs: 0.35}
	middle  = VADParams{Confidence: 0.7, StartSecs: 0.2}
	highest = VADParams{Confidence: 0.55, StartSecs: 0.1}
)

const (
	stopSecs      = 0.2 // Pipecat's default
	gateOff       = 0.6 // -50 LUFS, Pipecat's default volume
	gateStrongest = 0.8 // -30 LUFS
)

// Thresholds maps the app's interruption sensitivity and background filter (each 0 to 1, the
// filter 0 when it's off) to voice-activity thresholds, as hearing.py does.
func Thresholds(sensitivity, gate float64) VADParams {
	s := clamp(sensitivity)
	low, high, t := lowest, middle, 2*s
	if s >= 0.5 {
		low, high, t = middle, highest, 2*s-1
	}
	at := func(l, h float64) float64 { return round3(l + (h-l)*t) }
	return VADParams{
		Confidence: at(low.Confidence, high.Confidence),
		StartSecs:  at(low.StartSecs, high.StartSecs),
		StopSecs:   stopSecs,
		MinVolume:  round3(gateOff + (gateStrongest-gateOff)*clamp(gate)),
	}
}

func clamp(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Min(math.Max(v, 0), 1)
}

func round3(v float64) float64 { return math.RoundToEven(v*1000) / 1000 }
