// Package engine holds the models, through C: Silero and Smart Turn on ONNX Runtime's C API,
// Parakeet and Kokoro on sherpa-onnx's, and espeak-ng for words in no lexicon. The headers
// and libraries come from the speech node's install directory, through CGO_CFLAGS and
// CGO_LDFLAGS (see speech/build.sh).
package engine

/*
#cgo LDFLAGS: -lonnxruntime
#include <stdlib.h>
#include <string.h>
#include "onnxruntime_c_api.h"

static const OrtApi *ort;
static OrtEnv *env;
static OrtMemoryInfo *cpu;

static char *rb_err(OrtStatus *s) {
	if (!s) return NULL;
	char *m = strdup(ort->GetErrorMessage(s));
	ort->ReleaseStatus(s);
	return m;
}

static char *rb_init(void) {
	ort = OrtGetApiBase()->GetApi(ORT_API_VERSION);
	if (!ort) return strdup("this ONNX Runtime doesn't have the C API version of its headers");
	char *e = rb_err(ort->CreateEnv(ORT_LOGGING_LEVEL_WARNING, "ringback-speech", &env));
	if (e) return e;
	return rb_err(ort->CreateCpuMemoryInfo(OrtArenaAllocator, OrtMemTypeDefault, &cpu));
}

static char *rb_open(const char *path, int threads, OrtSession **out) {
	OrtSessionOptions *o;
	char *e = rb_err(ort->CreateSessionOptions(&o));
	if (e) return e;
	if (!(e = rb_err(ort->SetIntraOpNumThreads(o, threads))) &&
	    !(e = rb_err(ort->SetInterOpNumThreads(o, 1))) &&
	    !(e = rb_err(ort->SetSessionExecutionMode(o, ORT_SEQUENTIAL))) &&
	    !(e = rb_err(ort->SetSessionGraphOptimizationLevel(o, ORT_ENABLE_ALL))))
		e = rb_err(ort->CreateSession(env, path, o, out));
	ort->ReleaseSessionOptions(o);
	return e;
}

static char *rb_tensor(float *data, size_t n, const int64_t *shape, size_t dims, OrtValue **out) {
	return rb_err(ort->CreateTensorWithDataAsOrtValue(cpu, data, n * sizeof(float), shape, dims,
		ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT, out));
}

static char *rb_copy_out(OrtValue *v, float *dst, size_t n) {
	float *p;
	char *e = rb_err(ort->GetTensorMutableData(v, (void **)&p));
	if (!e) memcpy(dst, p, n * sizeof(float));
	return e;
}

// rb_silero runs Silero on one window: input is 576 floats (64 of context, 512 new), state
// is 2·1·128 floats and is replaced by the new state.
static char *rb_silero(OrtSession *s, float *input, float *state, float *prob) {
	static const int64_t in_shape[2] = {1, 576}, st_shape[3] = {2, 1, 128};
	int64_t sr = 16000;
	OrtValue *in[3] = {0}, *out[2] = {0};
	const char *in_names[3] = {"input", "state", "sr"}, *out_names[2] = {"output", "stateN"};
	char *e = rb_tensor(input, 576, in_shape, 2, &in[0]);
	if (!e) e = rb_tensor(state, 256, st_shape, 3, &in[1]);
	if (!e) e = rb_err(ort->CreateTensorWithDataAsOrtValue(cpu, &sr, sizeof sr, NULL, 0,
		ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64, &in[2]));
	if (!e) e = rb_err(ort->Run(s, NULL, in_names, (const OrtValue *const *)in, 3, out_names, 2, out));
	if (!e) e = rb_copy_out(out[0], prob, 1);
	if (!e) e = rb_copy_out(out[1], state, 256);
	for (int i = 0; i < 3; i++) if (in[i]) ort->ReleaseValue(in[i]);
	for (int i = 0; i < 2; i++) if (out[i]) ort->ReleaseValue(out[i]);
	return e;
}

// rb_smart_turn runs Smart Turn v3 on 80·800 features.
static char *rb_smart_turn(OrtSession *s, float *features, float *prob) {
	static const int64_t shape[3] = {1, 80, 800};
	OrtValue *in = NULL, *out = NULL;
	const char *in_names[1] = {"input_features"}, *out_names[1] = {"logits"};
	char *e = rb_tensor(features, 80 * 800, shape, 3, &in);
	if (!e) e = rb_err(ort->Run(s, NULL, in_names, (const OrtValue *const *)&in, 1, out_names, 1, &out));
	if (!e) e = rb_copy_out(out, prob, 1);
	if (in) ort->ReleaseValue(in);
	if (out) ort->ReleaseValue(out);
	return e;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

var ortOnce sync.Once
var ortErr error

func initORT() error {
	ortOnce.Do(func() {
		if e := C.rb_init(); e != nil {
			ortErr = errors.New(C.GoString(e))
			C.free(unsafe.Pointer(e))
		}
	})
	return ortErr
}

func cErr(e *C.char) error {
	if e == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(e))
	return errors.New(C.GoString(e))
}

type session struct{ s *C.OrtSession }

func openSession(path string, threads int) (session, error) {
	if err := initORT(); err != nil {
		return session{}, err
	}
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	var s *C.OrtSession
	if err := cErr(C.rb_open(p, C.int(threads), &s)); err != nil {
		return session{}, fmt.Errorf("%s: %w", path, err)
	}
	return session{s}, nil
}

// Silero is the voice-activity model on the CPU, one thread, as Pipecat runs it. One session
// serves every stream: each keeps its own state.
type Silero struct{ session }

func OpenSilero(path string) (*Silero, error) {
	s, err := openSession(path, 1)
	return &Silero{s}, err
}

// Run implements hearing.Silero.
func (m *Silero) Run(input []float32, state []float32) (float32, error) {
	if len(input) != 576 || len(state) != 256 {
		return 0, errors.New("silero: wrong sizes")
	}
	// The C side reads and writes the slices only during the call.
	var p C.float
	err := cErr(C.rb_silero(m.s, (*C.float)(&input[0]), (*C.float)(&state[0]), &p))
	return float32(p), err
}

// SmartTurn is Smart Turn v3 on the CPU.
type SmartTurn struct{ session }

func OpenSmartTurn(path string, threads int) (*SmartTurn, error) {
	s, err := openSession(path, threads)
	return &SmartTurn{s}, err
}

// Run implements hearing.TurnModel.
func (m *SmartTurn) Run(features []float32) (float32, error) {
	if len(features) != 80*800 {
		return 0, errors.New("smart turn: wrong size")
	}
	var p C.float
	err := cErr(C.rb_smart_turn(m.s, (*C.float)(&features[0]), &p))
	return float32(p), err
}
