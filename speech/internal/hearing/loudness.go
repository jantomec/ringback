package hearing

import "math"

// The volume the background filter compares with its gate is Pipecat's: the loudness of the
// last 400 ms of audio (ITU-R BS.1770, one gating block), from 0 to 1 for -110 to -10 LUFS.

const volumeWindow = Rate * 400 / 1000 // samples in a BS.1770 gating block

type biquad struct{ b0, b1, b2, a1, a2 float64 }

// kWeighting is BS.1770's K-weighting at Rate, a high shelf and then a high-pass, designed for
// the sample rate the way libebur128 does it.
var kWeighting = func() [2]biquad {
	f0, g, q := 1681.974450955533, 3.999843853973347, 0.7071752369554196
	k := math.Tan(math.Pi * f0 / Rate)
	vh := math.Pow(10, g/20)
	vb := math.Pow(vh, 0.4996667741545416)
	a0 := 1 + k/q + k*k
	shelf := biquad{
		b0: (vh + vb*k/q + k*k) / a0,
		b1: 2 * (k*k - vh) / a0,
		b2: (vh - vb*k/q + k*k) / a0,
		a1: 2 * (k*k - 1) / a0,
		a2: (1 - k/q + k*k) / a0,
	}
	f0, q = 38.13547087602444, 0.5003270373238773
	k = math.Tan(math.Pi * f0 / Rate)
	a0 = 1 + k/q + k*k
	highPass := biquad{b0: 1, b1: -2, b2: 1, a1: 2 * (k*k - 1) / a0, a2: (1 - k/q + k*k) / a0}
	return [2]biquad{shelf, highPass}
}()

// loudness is the integrated loudness of one gating block, in LUFS, or -Inf if it's below
// BS.1770's absolute gate (-70 LUFS). With a single block the relative gate always keeps it.
func loudness(samples []int16) float64 {
	var x1, x2, y1, y2 [2]float64 // each filter's last two inputs and outputs
	sum := 0.0
	for _, s := range samples {
		x := float64(float32(s) / 32768)
		for i, f := range kWeighting {
			y := f.b0*x + f.b1*x1[i] + f.b2*x2[i] - f.a1*y1[i] - f.a2*y2[i]
			x2[i], x1[i] = x1[i], x
			y2[i], y1[i] = y1[i], y
			x = y
		}
		sum += x * x
	}
	energy := sum / float64(len(samples))
	if energy <= 0 {
		return math.Inf(-1)
	}
	l := 10*math.Log10(energy) - 0.691
	if l < -70 {
		return math.Inf(-1)
	}
	return l
}

// volume is Pipecat's calculate_audio_volume of one gating block.
func volume(samples []int16) float64 {
	return clamp((loudness(samples) + 110) / 100)
}
