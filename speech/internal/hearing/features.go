package hearing

import "math"

// Smart Turn v3 reads Whisper's log-mel features of the last 8 s of your speech. This is
// Pipecat's _whisper_features.py (itself transformers' WhisperFeatureExtractor) in Go.

const (
	nFFT           = 400
	hopLength      = 160
	nMels          = 80
	featureSamples = 8 * Rate // 128000
	featureFrames  = featureSamples / hopLength
	melFloor       = 1e-10
	varianceEps    = 1e-7
)

var (
	hannWindow = func() (w [nFFT]float64) {
		// periodic, as torch.hann_window
		for i := range w {
			w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/nFFT)
		}
		return
	}()
	melFilters = buildMelFilters()
	whisperFFT = newFFT(nFFT)
)

func hertzToMel(f float64) float64 {
	if f >= 1000 {
		return 15 + math.Log(f/1000)*27/math.Log(6.4)
	}
	return 3 * f / 200
}

func melToHertz(m float64) float64 {
	if m >= 15 {
		return 1000 * math.Exp(math.Log(6.4)/27*(m-15))
	}
	return 200 * m / 3
}

func linspace(a, b float64, n int) []float64 {
	out := make([]float64, n)
	step := (b - a) / float64(n-1)
	for i := range out {
		out[i] = a + float64(i)*step
	}
	out[n-1] = b
	return out
}

// buildMelFilters is the Slaney-normalised triangular mel filterbank, [mel][frequency bin].
func buildMelFilters() [][]float64 {
	bins := nFFT/2 + 1
	mels := linspace(hertzToMel(0), hertzToMel(Rate/2), nMels+2)
	freqs := make([]float64, len(mels))
	for i, m := range mels {
		freqs[i] = melToHertz(m)
	}
	fftFreqs := linspace(0, Rate/2, bins)
	filters := make([][]float64, nMels)
	for j := range filters {
		filters[j] = make([]float64, bins)
		enorm := 2 / (freqs[j+2] - freqs[j])
		for i, f := range fftFreqs {
			down := -(freqs[j] - f) / (freqs[j+1] - freqs[j])
			up := (freqs[j+2] - f) / (freqs[j+2] - freqs[j+1])
			filters[j][i] = math.Max(0, math.Min(down, up)) * enorm
		}
	}
	return filters
}

// logMel returns the features of exactly featureSamples samples, [mel][frame] in one slice of
// nMels×featureFrames, ready for Smart Turn's input of shape [1, 80, 800].
func logMel(audio []float32) []float32 {
	n := len(audio)
	// Zero mean and unit variance, in float32 as the reference does.
	mean, sq := 0.0, 0.0
	for _, v := range audio {
		mean += float64(v)
	}
	mean /= float64(n)
	for _, v := range audio {
		d := float64(v) - mean
		sq += d * d
	}
	m32 := float32(mean)
	std := float32(math.Sqrt(float64(float32(sq/float64(n)) + varianceEps)))
	x := make([]float64, n+nFFT) // reflect-padded by nFFT/2 on each side
	pad := nFFT / 2
	for i := range x {
		j := i - pad
		if j < 0 {
			j = -j
		} else if j >= n {
			j = 2*(n-1) - j
		}
		x[i] = float64((audio[j] - m32) / std)
	}

	frames := 1 + (len(x)-nFFT)/hopLength // 801; the last is dropped below
	bins := nFFT/2 + 1
	power := make([]float64, frames*bins) // [frame][bin]
	buf := make([]complex128, nFFT)
	out := make([]complex128, nFFT)
	// Two real frames go through one complex FFT, one as the real part and one as the
	// imaginary, and are separated by symmetry afterwards.
	for f := 0; f < frames; f += 2 {
		a := x[f*hopLength:]
		var b []float64
		if f+1 < frames {
			b = x[(f+1)*hopLength:]
		}
		for i := 0; i < nFFT; i++ {
			im := 0.0
			if b != nil {
				im = b[i] * hannWindow[i]
			}
			buf[i] = complex(a[i]*hannWindow[i], im)
		}
		whisperFFT.transform(out, buf, nFFT, 1, whisperFFT.factors)
		for k := 0; k < bins; k++ {
			z, zc := out[k], out[(nFFT-k)%nFFT]
			zc = complex(real(zc), -imag(zc))
			ra, ia := (real(z)+real(zc))/2, (imag(z)+imag(zc))/2
			power[f*bins+k] = ra*ra + ia*ia
			if b != nil {
				d := z - zc // 2i·B
				rb, ib := imag(d)/2, -real(d)/2
				power[(f+1)*bins+k] = rb*rb + ib*ib
			}
		}
	}

	feats := make([]float64, nMels*featureFrames)
	top := math.Inf(-1)
	for m := 0; m < nMels; m++ {
		filter := melFilters[m]
		for f := 0; f < featureFrames; f++ {
			p := power[f*bins : (f+1)*bins]
			s := 0.0
			for k, w := range filter {
				if w != 0 {
					s += w * p[k]
				}
			}
			v := math.Log10(math.Max(melFloor, s))
			feats[m*featureFrames+f] = v
			top = math.Max(top, v)
		}
	}
	result := make([]float32, len(feats))
	for i, v := range feats {
		result[i] = float32((math.Max(v, top-8) + 4) / 4)
	}
	return result
}

// fft is a mixed-radix Cooley-Tukey FFT for sizes made of 2, 3, 4 and 5 (Whisper's 400 is
// 4·4·5·5).
type fft struct {
	n       int
	factors []int
	w       []complex128 // w[j] = e^(-2πij/n)
}

func newFFT(n int) *fft {
	f := &fft{n: n, w: make([]complex128, n)}
	for j := range f.w {
		s, c := math.Sincos(-2 * math.Pi * float64(j) / float64(n))
		f.w[j] = complex(c, s)
	}
	for rest := n; rest > 1; {
		r := 0
		for _, c := range []int{4, 2, 3, 5} {
			if rest%c == 0 {
				r = c
				break
			}
		}
		if r == 0 {
			panic("fft: size must be made of 2, 3 and 5")
		}
		f.factors = append(f.factors, r)
		rest /= r
	}
	return f
}

// transform writes the DFT of in[0], in[stride], in[2·stride]… (n values) to out[:n].
func (f *fft) transform(out, in []complex128, n, stride int, factors []int) {
	if n == 1 {
		out[0] = in[0]
		return
	}
	r := factors[0]
	m := n / r
	for q := 0; q < r; q++ {
		f.transform(out[q*m:(q+1)*m], in[q*stride:], m, stride*r, factors[1:])
	}
	step := f.n / n // w[j·step] = e^(-2πij/n)
	rStep := f.n / r
	var y [5]complex128
	for k := 0; k < m; k++ {
		for q := 0; q < r; q++ {
			y[q] = out[q*m+k] * f.w[q*k*step]
		}
		switch r {
		case 2:
			out[k], out[m+k] = y[0]+y[1], y[0]-y[1]
		case 4:
			t0, t1 := y[0]+y[2], y[0]-y[2]
			t2, d := y[1]+y[3], y[1]-y[3]
			t3 := complex(imag(d), -real(d)) // -i·d
			out[k], out[m+k], out[2*m+k], out[3*m+k] = t0+t2, t1+t3, t0-t2, t1-t3
		default:
			for s := 0; s < r; s++ {
				var acc complex128
				for q := 0; q < r; q++ {
					acc += y[q] * f.w[(q*s%r)*rStep]
				}
				out[s*m+k] = acc
			}
		}
	}
}
