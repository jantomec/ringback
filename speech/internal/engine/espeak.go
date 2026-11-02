package engine

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>

typedef int (*espeak_init_fn)(int, int, const char *, int);
typedef int (*espeak_voice_fn)(const char *);
typedef const char *(*espeak_phonemes_fn)(const void **, int, int);

static espeak_phonemes_fn espeak_phonemes;

// rb_espeak_open loads libespeak-ng at run time, so the node runs without it.
static char *rb_espeak_open(const char *lib, const char *data) {
	void *h = dlopen(lib, RTLD_NOW | RTLD_LOCAL);
	if (!h) return strdup(dlerror());
	espeak_init_fn init = (espeak_init_fn)dlsym(h, "espeak_Initialize");
	espeak_voice_fn voice = (espeak_voice_fn)dlsym(h, "espeak_SetVoiceByName");
	espeak_phonemes = (espeak_phonemes_fn)dlsym(h, "espeak_TextToPhonemes");
	if (!init || !voice || !espeak_phonemes) return strdup("not espeak-ng");
	if (init(0x02, 0, data, 0) <= 0) return strdup("espeak_Initialize failed"); // AUDIO_OUTPUT_SYNCHRONOUS
	if (voice("en-us") != 0) return strdup("no en-us voice");
	return NULL;
}

// rb_espeak_ipa is espeak-ng's IPA for text, with U+0361 tying the sounds of a diphthong,
// as phonemizer asks for it; clauses are joined by spaces. The caller frees it.
static char *rb_espeak_ipa(const char *text) {
	const void *p = text;
	size_t cap = 256, len = 0;
	char *out = malloc(cap);
	out[0] = 0;
	int mode = 0x02 | 0x01 << 7 | 0x0361 << 8;
	while (p) {
		const char *ph = espeak_phonemes(&p, 1, mode); // espeakCHARS_UTF8
		if (!ph) break;
		size_t n = strlen(ph);
		if (len + n + 2 > cap) {
			cap = 2 * (len + n + 2);
			out = realloc(out, cap);
		}
		if (len) out[len++] = ' ';
		memcpy(out + len, ph, n + 1);
		len += n;
	}
	return out;
}
*/
import "C"

import (
	"path/filepath"
	"sync"
	"unsafe"
)

// Espeak gives the sounds of words in no lexicon, as the Python voice's misaki does.
type Espeak struct{ mu sync.Mutex } // espeak-ng isn't thread-safe

// OpenEspeak loads espeak-ng from dir: lib/libespeak-ng.so.1 and espeak-ng-data/.
func OpenEspeak(dir string) (*Espeak, error) {
	lib := C.CString(filepath.Join(dir, "lib", "libespeak-ng.so.1"))
	defer C.free(unsafe.Pointer(lib))
	data := C.CString(dir)
	defer C.free(unsafe.Pointer(data))
	if err := cErr(C.rb_espeak_open(lib, data)); err != nil {
		return nil, err
	}
	return &Espeak{}, nil
}

// IPA returns espeak-ng's American English IPA for a word.
func (e *Espeak) IPA(word string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := C.CString(word)
	defer C.free(unsafe.Pointer(t))
	out := C.rb_espeak_ipa(t)
	defer C.free(unsafe.Pointer(out))
	return C.GoString(out)
}
