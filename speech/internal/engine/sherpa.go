package engine

/*
#cgo LDFLAGS: -lsherpa-onnx-c-api
#include <stdlib.h>
#include "sherpa-onnx/c-api/c-api.h"
*/
import "C"

import (
	"errors"
	"path/filepath"
	"strings"
	"unsafe"
)

// cstrings keeps C strings for a config until it's been used.
type cstrings []*C.char

func (cs *cstrings) add(s string) *C.char {
	p := C.CString(s)
	*cs = append(*cs, p)
	return p
}

func (cs cstrings) free() {
	for _, p := range cs {
		C.free(unsafe.Pointer(p))
	}
}

// Parakeet is Parakeet TDT 0.6B v2 on sherpa-onnx: the words of one stretch of speech.
type Parakeet struct {
	r *C.SherpaOnnxOfflineRecognizer
}

// OpenParakeet loads the model in dir (encoder*.onnx, decoder*.onnx, joiner*.onnx,
// tokens.txt) on a provider ("cuda" or "cpu").
func OpenParakeet(dir, provider string, threads int) (*Parakeet, error) {
	find := func(prefix string) string {
		m, _ := filepath.Glob(filepath.Join(dir, prefix+"*.onnx"))
		if len(m) == 0 {
			return ""
		}
		return m[0]
	}
	encoder, decoder, joiner := find("encoder"), find("decoder"), find("joiner")
	if encoder == "" || decoder == "" || joiner == "" {
		return nil, errors.New("no Parakeet model in " + dir)
	}
	var cs cstrings
	defer cs.free()
	var c C.SherpaOnnxOfflineRecognizerConfig
	c.feat_config.sample_rate = 16000
	c.feat_config.feature_dim = 80
	c.model_config.transducer.encoder = cs.add(encoder)
	c.model_config.transducer.decoder = cs.add(decoder)
	c.model_config.transducer.joiner = cs.add(joiner)
	c.model_config.tokens = cs.add(filepath.Join(dir, "tokens.txt"))
	c.model_config.num_threads = C.int32_t(threads)
	c.model_config.provider = cs.add(provider)
	c.model_config.model_type = cs.add("nemo_transducer")
	c.decoding_method = cs.add("greedy_search")
	r := C.SherpaOnnxCreateOfflineRecognizer(&c)
	if r == nil {
		return nil, errors.New("Parakeet didn't load from " + dir)
	}
	return &Parakeet{r}, nil
}

// Transcribe returns the words of float samples at 16 kHz.
func (p *Parakeet) Transcribe(samples []float32) string {
	if len(samples) == 0 {
		return ""
	}
	s := C.SherpaOnnxCreateOfflineStream(p.r)
	defer C.SherpaOnnxDestroyOfflineStream(s)
	C.SherpaOnnxAcceptWaveformOffline(s, 16000, (*C.float)(&samples[0]), C.int32_t(len(samples)))
	C.SherpaOnnxDecodeOfflineStream(p.r, s)
	res := C.SherpaOnnxGetOfflineStreamResult(s)
	if res == nil {
		return ""
	}
	defer C.SherpaOnnxDestroyOfflineRecognizerResult(res)
	return strings.TrimSpace(C.GoString(res.text))
}

// Kokoro is Kokoro v1.0 on sherpa-onnx, given phonemes rather than text (see the speaking
// package for why).
type Kokoro struct {
	t     *C.SherpaOnnxOfflineTts
	voice int
	Rate  int
}

// OpenKokoro loads kokoro-multi-lang-v1_0 from dir, to speak with one of its voices (af_heart
// is 3).
func OpenKokoro(dir, provider string, threads, voice int) (*Kokoro, error) {
	var cs cstrings
	defer cs.free()
	var c C.SherpaOnnxOfflineTtsConfig
	c.model.kokoro.model = cs.add(filepath.Join(dir, "model.onnx"))
	c.model.kokoro.voices = cs.add(filepath.Join(dir, "voices.bin"))
	c.model.kokoro.tokens = cs.add(filepath.Join(dir, "tokens.txt"))
	c.model.kokoro.length_scale = 1
	c.model.num_threads = C.int32_t(threads)
	c.model.provider = cs.add(provider)
	c.max_num_sentences = 1
	c.silence_scale = 1 // Kokoro's pauses as it makes them, as the Python voice plays them
	t := C.SherpaOnnxCreateOfflineTts(&c)
	if t == nil {
		return nil, errors.New("Kokoro didn't load from " + dir)
	}
	return &Kokoro{t: t, voice: voice, Rate: int(C.SherpaOnnxOfflineTtsSampleRate(t))}, nil
}

// Speak returns the audio of one chunk of phonemes, float samples at k.Rate.
func (k *Kokoro) Speak(phonemes string) ([]float32, error) {
	cps := []rune(phonemes)
	if len(cps) == 0 {
		return nil, nil
	}
	// The config holds pointers, so it and what it points to live in C memory.
	cfg := (*C.SherpaOnnxGenerationConfig)(C.calloc(1, C.sizeof_SherpaOnnxGenerationConfig))
	defer C.free(unsafe.Pointer(cfg))
	points := (*C.int32_t)(C.calloc(C.size_t(len(cps)), 4))
	defer C.free(unsafe.Pointer(points))
	lens := (*C.int32_t)(C.calloc(1, 4))
	defer C.free(unsafe.Pointer(lens))
	copy(unsafe.Slice(points, len(cps)), *(*[]C.int32_t)(unsafe.Pointer(&cps)))
	*lens = C.int32_t(len(cps))
	cfg.sid = C.int32_t(k.voice)
	cfg.speed = 1
	cfg.silence_scale = 1
	cfg.phoneme_codepoints = points
	cfg.phoneme_codepoints_lens = lens
	cfg.phoneme_codepoints_num_sentences = 1
	empty := C.CString("")
	defer C.free(unsafe.Pointer(empty))
	audio := C.SherpaOnnxOfflineTtsGenerateWithConfig(k.t, empty, cfg, nil, nil)
	if audio == nil {
		return nil, errors.New("Kokoro made no audio")
	}
	defer C.SherpaOnnxDestroyOfflineTtsGeneratedAudio(audio)
	n := int(audio.n)
	if n == 0 {
		return nil, nil
	}
	return append([]float32(nil), unsafe.Slice((*float32)(unsafe.Pointer(audio.samples)), n)...), nil
}
