//go:build parity

// Compares the voice-activity states and Smart Turn's verdict with Pipecat's own, on the
// speech node with the real models:
//
//	RINGBACK_HEARING_REF=DIR RINGBACK_MODELS=INSTALL_DIR/models go test -tags parity ./internal/hearing
package hearing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ringback/speech/internal/engine"
)

func TestVADAndTurnMatchPipecat(t *testing.T) {
	dir := refDir(t)
	models := os.Getenv("RINGBACK_MODELS")
	silero, err := engine.OpenSilero(filepath.Join(models, "silero-vad-v5.onnx"))
	if err != nil {
		t.Fatal(err)
	}
	turn, err := engine.OpenSmartTurn(filepath.Join(models, "smart-turn-v3.2-cpu.onnx"), 1)
	if err != nil {
		t.Fatal(err)
	}
	stream := readRaw[int16](t, filepath.Join(dir, "vad_stream.i16"))
	data, err := os.ReadFile(filepath.Join(dir, "vad_states.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ref map[string]struct {
		States          []int     `json:"states"`
		TurnProbability []float64 `json:"turn_probability"`
	}
	if err := json.Unmarshal(data, &ref); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]VADParams{
		"default":   {0.7, 0.2, 0.2, 0.6},
		"gated":     {0.7, 0.2, 0.2, 0.8},
		"sensitive": {0.55, 0.1, 0.2, 0.6},
	} {
		a := newAnalyzer(silero, p)
		differ, changes := 0, 0
		prev := quiet
		for i := 0; i*frameLen < len(stream); i++ {
			st := a.analyze(stream[i*frameLen : min((i+1)*frameLen, len(stream))])
			if int(st) != ref[name].States[i] {
				differ++
			}
			if st != prev {
				changes++
			}
			prev = st
		}
		t.Logf("%s: %d of %d frames differ from Pipecat's state (%d state changes)", name, differ, len(ref[name].States), changes)
		if differ > 2 {
			t.Errorf("%s: %d frames differ", name, differ)
		}
	}
	audio := readRaw[float32](t, filepath.Join(dir, "feat_audio.f32"))
	p, err := turn.Run(logMel(audio))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Smart Turn: %.5f, Pipecat's %.5f", p, ref["default"].TurnProbability[0])
	if d := float64(p) - ref["default"].TurnProbability[0]; d > 0.01 || d < -0.01 {
		t.Errorf("Smart Turn differs by %.4f", d)
	}
}

const frameLen = Rate / 50
