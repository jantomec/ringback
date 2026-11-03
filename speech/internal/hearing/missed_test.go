package hearing

import (
	"math"
	"testing"
)

// scripted is a Silero that answers with the next confidence it's given.
type scripted struct{ next []float32 }

func (s *scripted) Run([]float32, []float32) (float32, error) {
	p := s.next[0]
	s.next = s.next[1:]
	return p, nil
}

// A stretch Silero takes for speech is reported as missed when the gate or its shortness kept
// it from starting speech, and not when it started.
func TestMissed(t *testing.T) {
	window := func(amplitude float64) []int16 {
		w := make([]int16, sileroWindow)
		for i := range w {
			w[i] = int16(amplitude * 32767 * math.Sin(2*math.Pi*440*float64(i)/Rate))
		}
		return w
	}
	params := Thresholds(DefaultSensitivity, DefaultGate) // 0.2 s of speech, 6 windows; gate 0.7
	run := func(amplitude float64, voiced int) (*Missed, bool) {
		lead, after := 40, 3
		model := &scripted{}
		for i := 0; i < lead+voiced+after; i++ {
			p := float32(0.05)
			if i >= lead && i < lead+voiced {
				p = 0.9
			}
			model.next = append(model.next, p)
		}
		a := newAnalyzer(model, params)
		var missed *Missed
		started := false
		for i := 0; i < lead+voiced+after; i++ {
			if a.analyze(window(amplitude)) == speaking {
				started = true
			}
			if m := a.takeMissed(); m != nil {
				missed = m
			}
		}
		return missed, started
	}
	secs := float64(sileroWindow) / Rate

	m, started := run(0.3, 4)
	if started || m == nil || m.Secs != 4*secs || m.Quiet != 0 || m.Volume < params.MinVolume {
		t.Errorf("too short: started %v, missed %+v", started, m)
	}
	m, started = run(0.0003, 10)
	if started || m == nil || m.Secs != 10*secs || m.Quiet != 10*secs || m.Volume >= params.MinVolume {
		t.Errorf("too quiet: started %v, missed %+v", started, m)
	}
	m, started = run(0.3, 10)
	if !started || m != nil {
		t.Errorf("heard: started %v, missed %+v", started, m)
	}
	m, started = run(0.3, 2)
	if started || m != nil {
		t.Errorf("a blip shorter than minMissed: started %v, missed %+v", started, m)
	}
}
