package hearing

import (
	"strings"
	"time"
)

// Events is what a stream reports. The calls come on the goroutine that feeds the stream.
type Events interface {
	SpeechStarted()
	SpeechStopped()
	// Turn: you've paused for the turn wait. Your words since the voice last spoke, the whole
	// turn written down at once; how long you spoke in it; how long writing it down took, from
	// asking to the answer; and again, whether an earlier Turn gave the start of this one, so
	// these words take the place of its.
	Turn(text string, secs, took float64, again bool)
	// Transcribe asks for the words of your turn so far (float samples at Rate); the answer
	// comes back through Stream.Transcribed with the same id. Asking again takes the place of
	// an answer still to come: only the latest id's counts.
	Transcribe(id int, samples []float32)
	// Missed: Silero heard speech that didn't start speaking (see Missed), for the log.
	Missed(m Missed)
}

const (
	preRoll     = Rate     // audio kept before speech starts, for the transcription (1 s)
	trailingPad = Rate / 2 // silence after a stretch of speech, so its last word is heard
	retryLead   = 0.1      // seconds before the speech began that a second transcription keeps
	IdleTimeout = 1.0      // seconds without audio that end your speech (Pipecat's VADController)
	// A turn longer than a minute is written down again only from its last Turn on: the words
	// before stay as they were heard. A minute takes about 0.5 s with the voice speaking, all
	// of the turn wait (tools/stt-trial, turns.py).
	maxTurn = 60 * Rate
	// A turn the voice still hasn't answered after 30 s of quiet is let go, so its audio isn't
	// kept for ever: after a [wait], the hub has the model answer within a few seconds.
	forgetAfter = 30 * Rate
)

// Stream hears one call's audio. It isn't safe for concurrent use: one goroutine feeds it
// audio, transcriptions and the voice speaking.
//
// Your words go to the model at each pause of the turn wait, as Pipecat's segmented STT does,
// but each time with all of your turn written down at once: Parakeet gets far fewer words wrong
// in a whole turn than stretch by stretch (tools/stt-trial). A turn is all you've said since the
// voice last started speaking (VoiceSpeaking).
type Stream struct {
	ev     Events
	params VADParams
	vad    *analyzer
	turn   *turnAnalyzer
	said   vadState // quiet or speaking: what the stream last reported
	now    float64  // seconds of audio so far
	heard  int      // samples of audio so far

	// The turn's audio: from 1 s before you started speaking in it, until now. Between turns,
	// only the last second.
	audio        []int16
	from         int  // the sample audio[0] is
	open         bool // you've spoken since the voice last did
	userSpeaking bool
	segmentStart float64 // when the stretch you're in began, in seconds
	first        float64 // when the turn's first stretch began
	lastStop     int     // the sample your last stretch ended at

	// The turn (Pipecat's TurnAnalyzerUserTurnStopStrategy).
	vadSpeaking bool
	complete    bool // the pause has lasted the turn wait
	asked       int  // the latest transcription asked for; earlier ones no longer count
	pending     bool // its answer hasn't come
	askedTo     int  // the sample it reaches
	retry       []float32
	words       string  // the latest transcription's
	before      string  // a long turn's start, kept as heard (maxTurn)
	spoke       float64 // seconds of speech in the turn
	took        float64

	// What the last Turn gave.
	reported      bool    // a Turn has given this turn's words
	reportedTo    int     // the sample its words reach
	reportedWords string  // its words since before
	reportedSpoke float64 // its speech
	since         float64 // when the first stretch after it began; -1 if none has
}

// NewStream starts hearing with the thresholds for a sensitivity and background filter.
func NewStream(ev Events, vad Silero, turn TurnModel, params VADParams) *Stream {
	return &Stream{
		ev:     ev,
		params: params,
		vad:    newAnalyzer(vad, params),
		turn:   &turnAnalyzer{model: turn, stopSecs: TurnWait},
		said:   quiet,
		since:  -1,
	}
}

// Audio adds one frame of your audio (16-bit samples at Rate).
func (s *Stream) Audio(samples []int16) {
	s.now += float64(len(samples)) / Rate
	s.heard += len(samples)

	// The transcription sees the frame before the voice-activity events it causes.
	s.audio = append(s.audio, samples...)
	switch {
	case !s.open && !s.userSpeaking:
		s.trim(s.heard - preRoll)
	case s.open && !s.userSpeaking && s.since < 0 && s.heard-s.lastStop > forgetAfter:
		s.close()
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

// VoiceSpeaking: the voice has started speaking, so what the last Turn gave is answered, and
// what you say next starts a new turn. What you've said since that Turn, or are saying now,
// starts it.
func (s *Stream) VoiceSpeaking() {
	if s.open {
		s.close()
	}
}

// Transcribed gives the words that Events.Transcribe asked for.
func (s *Stream) Transcribed(id int, text string, took float64) {
	if id != s.asked {
		return // a later stretch's takes its place
	}
	if s.retry != nil && strings.TrimSpace(text) == "" {
		// Parakeet often writes nothing for a short word with audio before it ("Yes." after the
		// second kept before you started), and hears it alone (tools/short-answers): ask once
		// more from just before your speech began.
		audio := s.retry
		s.retry = nil
		s.ev.Transcribe(id, audio)
		return
	}
	s.retry, s.pending = nil, false
	s.words, s.took = strings.TrimSpace(text), took
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
	if !s.open {
		s.open, s.first = true, s.segmentStart
	}
	if s.since < 0 {
		s.since = s.segmentStart
	}
	s.turn.vadStartSecs = s.params.StartSecs
	s.vadSpeaking = true
	s.complete = false // you've spoken again since an earlier verdict
}

func (s *Stream) stopped() {
	s.ev.SpeechStopped()
	s.userSpeaking = false
	s.lastStop = s.heard
	s.spoke += max(0, s.now-s.params.StopSecs-s.segmentStart)
	if s.reported && s.lastStop-s.from > maxTurn && s.reportedTo > s.from {
		// A long turn: what the last Turn gave stays as it was heard, and only what you've said
		// since is written down again.
		s.before = join(s.before, s.reportedWords)
		s.trim(max(s.reportedTo, sample(s.since)-preRoll))
		s.first = s.since
	}
	s.ask()

	s.vadSpeaking = false
	s.turn.analyze()
	s.finish()
}

// ask asks for the words of the turn up to your last stop, with a second's lead and silence after.
func (s *Stream) ask() {
	audio := make([]float32, s.lastStop-s.from+trailingPad)
	for i, v := range s.audio[:s.lastStop-s.from] {
		audio[i] = float32(v) / 32768
	}
	s.asked++
	s.pending, s.askedTo, s.retry = true, s.lastStop, nil
	if from := sample(s.first-retryLead) - s.from; from > 0 && from < s.lastStop-s.from {
		s.retry = audio[from:]
	}
	s.ev.Transcribe(s.asked, audio)
}

// finish reports the turn once the pause has lasted the turn wait and it's written down.
func (s *Stream) finish() {
	if !s.complete || s.pending {
		return
	}
	s.ev.Turn(join(s.before, s.words), s.spoke, s.took, s.reported)
	s.reported, s.reportedTo, s.reportedWords, s.reportedSpoke = true, s.askedTo, s.words, s.spoke
	s.since, s.took = -1, 0
	s.complete = false
	s.turn.clear(true)
}

// close ends the turn: the voice has answered what the last Turn gave. Speech after it is
// kept, from up to a second before it began, but never what that Turn heard.
func (s *Stream) close() {
	s.before, s.words, s.spoke = "", "", s.spoke-s.reportedSpoke
	s.reported, s.reportedWords, s.reportedSpoke = false, "", 0
	if s.since < 0 {
		s.open, s.spoke = false, 0
		s.trim(s.heard - preRoll)
		return
	}
	s.trim(max(s.reportedTo, sample(s.since)-preRoll))
	s.first = s.since
	// What was asked for heard the turn before too.
	if s.userSpeaking {
		s.asked++ // the stop you're coming to asks again
		s.pending, s.retry = false, nil
	} else {
		s.ask()
	}
}

// trim forgets the audio before sample to.
func (s *Stream) trim(to int) {
	if k := min(to-s.from, len(s.audio)); k > 0 {
		s.audio = append(s.audio[:0], s.audio[k:]...)
		s.from += k
	}
}

func sample(secs float64) int { return int(secs * Rate) }

func join(a, b string) string { return strings.TrimSpace(a + " " + b) }
