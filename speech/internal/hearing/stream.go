package hearing

import (
	"strings"
	"time"
)

// Events is what a stream reports. The calls come on the goroutine that feeds the stream.
type Events interface {
	SpeechStarted()
	SpeechStopped()
	// Turn: your words for the whole turn, how long you spoke and how long recognising the
	// turn's last stretch of speech took, from asking to the answer.
	Turn(text string, secs, took float64)
	// Transcribe asks for the words of one stretch of speech (float samples at Rate); the
	// answer comes back through Stream.Transcribed with the same id, in the order asked.
	Transcribe(id int, samples []float32)
	// Missed: Silero heard speech that didn't start speaking (see Missed), for the log.
	Missed(m Missed)
}

const (
	preRoll     = Rate     // audio kept before speech starts, for the transcription (1 s)
	trailingPad = Rate / 2 // silence after a stretch of speech, so its last word is heard
	IdleTimeout = 1.0      // seconds without audio that end your speech (Pipecat's VADController)
)

// Stream hears one call's audio. It isn't safe for concurrent use: one goroutine feeds it
// audio and transcriptions.
type Stream struct {
	ev     Events
	params VADParams
	vad    *analyzer
	turn   *turnAnalyzer
	said   vadState // quiet or speaking: what the stream last reported
	now    float64  // seconds of audio so far

	// The transcription's audio (Pipecat's SegmentedSTTService): 1 s before you start, and
	// all of it while you speak.
	segment      []int16
	userSpeaking bool
	segmentStart float64

	// The turn (Pipecat's TurnAnalyzerUserTurnStopStrategy).
	vadSpeaking bool
	complete    bool     // Smart Turn or the turn wait says the turn is over
	pending     int      // stretches of speech being transcribed
	nextID      int      // the next stretch's id
	words       []string // the turn's words so far
	spoke       float64  // seconds of speech in the turn
	took        float64
}

// NewStream starts hearing with the thresholds for a sensitivity and background filter.
func NewStream(ev Events, vad Silero, turn TurnModel, params VADParams) *Stream {
	return &Stream{
		ev:     ev,
		params: params,
		vad:    newAnalyzer(vad, params),
		turn:   &turnAnalyzer{model: turn, stopSecs: TurnWait},
		said:   quiet,
	}
}

// Audio adds one frame of your audio (16-bit samples at Rate; the stream keeps the slice).
func (s *Stream) Audio(samples []int16) {
	s.now += float64(len(samples)) / Rate

	// The transcription sees the frame before the voice-activity events it causes.
	s.segment = append(s.segment, samples...)
	if !s.userSpeaking && len(s.segment) > preRoll {
		s.segment = append(s.segment[:0], s.segment[len(s.segment)-preRoll:]...)
	}

	state := s.vad.analyze(samples)
	if m := s.vad.takeMissed(); m != nil {
		s.ev.Missed(*m)
	}
	var started, stopped bool
	if state != s.said && state != starting && state != stopping {
		started, stopped = state == speaking, state == quiet
		s.said = state
	}

	// So does the turn analyzer: Pipecat queues the events behind the frame.
	if s.turn.appendAudio(samples, s.vadSpeaking, s.now) {
		s.complete = true
		s.finish()
	}
	if started {
		s.started()
	} else if stopped {
		s.stopped()
	}
}

// Idle ends your speech when no audio has come for IdleTimeout while you were speaking.
func (s *Stream) Idle() {
	if s.said == speaking {
		s.said = quiet
		s.stopped()
	}
}

// Transcribed gives the words of a stretch of speech that Events.Transcribe asked for.
func (s *Stream) Transcribed(id int, text string, took float64) {
	s.pending--
	if text = strings.TrimSpace(text); text != "" {
		s.words = append(s.words, text)
	}
	s.took = took
	s.finish()
}

// Err is the last failure of a model, if any.
func (s *Stream) Err() error {
	if s.vad.err != nil {
		return s.vad.err
	}
	return s.turn.err
}

// LastTurn is Smart Turn's last answer and how long it took, for the log.
func (s *Stream) LastTurn() (float32, time.Duration) { return s.turn.lastProbability, s.turn.lastTook }

func (s *Stream) started() {
	s.ev.SpeechStarted()
	s.userSpeaking = true
	s.segmentStart = s.now - s.params.StartSecs
	s.turn.vadStartSecs = s.params.StartSecs
	s.vadSpeaking = true
	s.complete = false // you've spoken again since an earlier verdict
}

func (s *Stream) stopped() {
	s.ev.SpeechStopped()
	s.userSpeaking = false
	audio := make([]float32, len(s.segment)+trailingPad)
	for i, v := range s.segment {
		audio[i] = float32(v) / 32768
	}
	s.segment = nil
	s.spoke += max(0, s.now-s.params.StopSecs-s.segmentStart)
	s.pending++
	id := s.nextID
	s.nextID++
	s.ev.Transcribe(id, audio)

	s.vadSpeaking = false
	if s.turn.analyze() {
		s.complete = true
	}
	s.finish()
}

// finish reports the turn once it's complete and all of its speech is transcribed.
func (s *Stream) finish() {
	if !s.complete || s.pending > 0 {
		return
	}
	s.ev.Turn(strings.Join(s.words, " "), s.spoke, s.took)
	s.words, s.spoke, s.took = nil, 0, 0
	s.complete = false
	s.turn.clear(true)
}
