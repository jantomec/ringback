// Package wav reads and writes 16-bit mono WAV files and resamples audio, for the node's own
// checks.
package wav

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
)

// Read returns a WAV file's samples (16-bit PCM; channels after the first are dropped) and
// its sample rate.
func Read(path string) ([]int16, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, errors.New(path + ": not a WAV file")
	}
	var rate, channels, bits int
	for p := 12; p+8 <= len(data); {
		id, size := string(data[p:p+4]), int(binary.LittleEndian.Uint32(data[p+4:]))
		body := data[p+8 : min(p+8+size, len(data))]
		switch id {
		case "fmt ":
			channels = int(binary.LittleEndian.Uint16(body[2:]))
			rate = int(binary.LittleEndian.Uint32(body[4:]))
			bits = int(binary.LittleEndian.Uint16(body[14:]))
		case "data":
			if bits != 16 || channels < 1 {
				return nil, 0, errors.New(path + ": not 16-bit PCM")
			}
			n := len(body) / 2 / channels
			out := make([]int16, n)
			for i := range out {
				out[i] = int16(binary.LittleEndian.Uint16(body[i*2*channels:]))
			}
			return out, rate, nil
		}
		p += 8 + size + size%2
	}
	return nil, 0, errors.New(path + ": no audio")
}

// Write saves 16-bit mono samples.
func Write(path string, samples []int16, rate int) error {
	b := make([]byte, 44+2*len(samples))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(samples)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(2*rate))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(samples)))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(s))
	}
	return os.WriteFile(path, b, 0o644)
}

// FromFloat turns float samples in -1…1 into 16-bit ones, as the Python voice does.
func FromFloat(samples []float32) []int16 {
	out := make([]int16, len(samples))
	for i, s := range samples {
		out[i] = int16(math.Max(-1, math.Min(1, float64(s))) * 32767)
	}
	return out
}

// Resample changes the sample rate with a windowed-sinc filter.
func Resample(in []int16, from, to int) []int16 {
	if from == to {
		return append([]int16(nil), in...)
	}
	ratio := float64(from) / float64(to)
	cutoff := math.Min(1, 1/ratio) // of the input's Nyquist frequency
	const half = 32                // taps on each side
	out := make([]int16, int(float64(len(in))/ratio))
	for i := range out {
		t := float64(i) * ratio
		c := int(t)
		sum, norm := 0.0, 0.0
		for j := c - half + 1; j <= c+half; j++ {
			x := t - float64(j)
			w := cutoff * sinc(cutoff*x) * (0.5 + 0.5*math.Cos(math.Pi*x/half)) // Hann window
			norm += w
			if j >= 0 && j < len(in) {
				sum += w * float64(in[j])
			}
		}
		out[i] = int16(math.Max(-32768, math.Min(32767, math.Round(sum/norm))))
	}
	return out
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}
