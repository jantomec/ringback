package hearing

import (
	"encoding/binary"
	"math"
	"math/cmplx"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestThresholds(t *testing.T) {
	for _, c := range []struct {
		s, g float64
		want VADParams
	}{
		{0.5, 0.5, VADParams{0.7, 0.2, 0.2, 0.7}},
		{0, 0, VADParams{0.85, 0.35, 0.2, 0.6}},
		{1, 1, VADParams{0.55, 0.1, 0.2, 0.8}},
		{0.25, 0.25, VADParams{0.775, 0.275, 0.2, 0.65}},
	} {
		if got := Thresholds(c.s, c.g); got != c.want {
			t.Errorf("Thresholds(%v, %v) = %+v, want %+v", c.s, c.g, got, c.want)
		}
	}
}

func TestFFT(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	in := make([]complex128, nFFT)
	for i := range in {
		in[i] = complex(r.NormFloat64(), r.NormFloat64())
	}
	out := make([]complex128, nFFT)
	whisperFFT.transform(out, in, nFFT, 1, whisperFFT.factors)
	for k := 0; k < nFFT; k++ {
		var want complex128
		for n, v := range in {
			want += v * cmplx.Exp(complex(0, -2*math.Pi*float64(k*n)/nFFT))
		}
		if cmplx.Abs(out[k]-want) > 1e-9 {
			t.Fatalf("bin %d: %v, want %v", k, out[k], want)
		}
	}
}

// RINGBACK_HEARING_REF names a folder of reference values from Pipecat itself (made by a
// script outside the repo), to compare the volume and the Whisper features with.
func refDir(t *testing.T) string {
	dir := os.Getenv("RINGBACK_HEARING_REF")
	if dir == "" {
		t.Skip("RINGBACK_HEARING_REF not set")
	}
	return dir
}

func readRaw[T any](t *testing.T, path string) []T {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var zero T
	out := make([]T, len(data)/binary.Size(zero))
	if _, err := binary.Decode(data, binary.LittleEndian, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestVolumeMatchesPipecat(t *testing.T) {
	dir := refDir(t)
	blocks := readRaw[int16](t, filepath.Join(dir, "vol_blocks.i16"))
	want := readRaw[float64](t, filepath.Join(dir, "vol_values.f64"))
	worst := 0.0
	for i, w := range want {
		got := volume(blocks[i*volumeWindow : (i+1)*volumeWindow])
		worst = math.Max(worst, math.Abs(got-w))
	}
	t.Logf("%d blocks, largest difference %.2g (in 0-1 volume; 0.001 is 0.1 dB)", len(want), worst)
	if worst > 0.001 {
		t.Errorf("volume differs from Pipecat's by up to %.4f", worst)
	}
}

func TestFeaturesMatchPipecat(t *testing.T) {
	dir := refDir(t)
	audio := readRaw[float32](t, filepath.Join(dir, "feat_audio.f32"))
	want := readRaw[float32](t, filepath.Join(dir, "feat_values.f32"))
	got := logMel(audio)
	worst := 0.0
	for i := range want {
		worst = math.Max(worst, math.Abs(float64(got[i]-want[i])))
	}
	t.Logf("%d features, largest difference %.2g", len(want), worst)
	if worst > 1e-4 {
		t.Errorf("features differ from Pipecat's by up to %g", worst)
	}
}

func BenchmarkLogMel(b *testing.B) {
	audio := make([]float32, featureSamples)
	r := rand.New(rand.NewSource(1))
	for i := range audio {
		audio[i] = float32(r.NormFloat64() * 0.1)
	}
	for b.Loop() {
		logMel(audio)
	}
}
