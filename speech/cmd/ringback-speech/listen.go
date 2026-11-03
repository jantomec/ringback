package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"ringback/speech/internal/hearing"
	"ringback/speech/internal/wav"
)

// listen hears WAV files as a call does (Silero with the background filter, Smart Turn and
// Parakeet, in 20 ms frames), each file as a call of its own, and prints each event as a JSON
// line: when you started and stopped speaking, a stretch not taken for speech, and each turn
// with its words. Each stretch is transcribed as soon as it ends, as on an idle GPU. Two
// seconds of silence follow each file, so the turn wait can end its last turn.
func listen(c config, args []string) {
	sensitivity, gate := hearing.DefaultSensitivity, hearing.DefaultGate
	var files []string
	for i := 0; i < len(args); i++ {
		switch {
		case (args[i] == "--sensitivity" || args[i] == "--gate") && i+1 < len(args):
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				fail(usage)
			}
			if args[i] == "--gate" {
				gate = v
			} else {
				sensitivity = v
			}
			i++
		default:
			files = append(files, args[i])
		}
	}
	if len(files) == 0 {
		fail(usage)
	}
	m, err := loadModels(c, true, false)
	if err != nil {
		fail(err.Error())
	}
	params := hearing.Thresholds(sensitivity, gate)
	const frame = hearing.Rate / 50
	for _, f := range files {
		samples, rate, err := wav.Read(f)
		if err != nil {
			fail(err.Error())
		}
		samples = append(wav.Resample(samples, rate, hearing.Rate), make([]int16, 2*hearing.Rate)...)
		l := &listener{file: filepath.Base(f)}
		l.say(map[string]any{"event": "hearing", "sensitivity": sensitivity, "gate": gate,
			"confidence": params.Confidence, "start_secs": params.StartSecs, "min_volume": params.MinVolume})
		s := hearing.NewStream(l, m.silero, m.turn, params)
		for i := 0; i+frame <= len(samples); i += frame {
			l.at = float64(i+frame) / hearing.Rate
			s.Audio(samples[i : i+frame])
			for len(l.queue) > 0 {
				q := l.queue[0]
				l.queue = l.queue[1:]
				t := time.Now()
				text := m.asr.Transcribe(q.audio)
				s.Transcribed(q.id, text, time.Since(t).Seconds())
			}
			if err := s.Err(); err != nil {
				fail(err.Error())
			}
		}
	}
}

// listener prints a stream's events, at their time in the file.
type listener struct {
	file  string
	at    float64
	queue []struct {
		id    int
		audio []float32
	}
}

func (l *listener) say(m map[string]any) {
	m["file"], m["at"] = l.file, round3(l.at)
	b, _ := json.Marshal(m)
	fmt.Fprintln(os.Stdout, string(b))
}

func (l *listener) SpeechStarted() { l.say(map[string]any{"event": "started"}) }
func (l *listener) SpeechStopped() { l.say(map[string]any{"event": "stopped"}) }

func (l *listener) Turn(text string, secs, took float64) {
	l.say(map[string]any{"event": "turn", "text": text, "secs": round3(secs)})
}

func (l *listener) Transcribe(id int, samples []float32) {
	l.queue = append(l.queue, struct {
		id    int
		audio []float32
	}{id, samples})
}

func (l *listener) Missed(m hearing.Missed) {
	l.say(map[string]any{"event": "missed", "secs": round3(m.Secs), "quiet": round3(m.Quiet),
		"volume": round3(m.Volume), "confidence": round3(m.Confidence)})
}
