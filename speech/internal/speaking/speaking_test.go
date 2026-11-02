package speaking

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNumbers(t *testing.T) {
	for in, want := range map[string]string{
		"42":       "forty two",
		"142":      "one hundred forty two",
		"1,234":    "one thousand two hundred thirty four",
		"2026":     "twenty twenty six",
		"2005":     "two thousand five",
		"1905":     "nineteen oh five",
		"3.14":     "three point one four",
		"-4":       "minus four",
		"42%":      "forty two percent",
		"1st":      "first",
		"22nd":     "twenty second",
		"90s":      "nineties",
		"12:30":    "twelve thirty",
		"8:05":     "eight oh five",
		"9:00":     "nine o'clock",
		"2.0.1":    "two point zero point one",
		"v2":       "v two",
		"2-10":     "two to ten",
		"500ms":    "five hundred milliseconds",
		"1s":       "one second",
		"24kHz":    "twenty four kilohertz",
		"$5":       "five dollars",
		"$3.50":    "three dollars and fifty cents",
		"3x":       "three times",
		"1000000":  "one million",
		"12345678": "twelve million three hundred forty five thousand six hundred seventy eight",
	} {
		words, ok := spokenNumber(in)
		if got := strings.Join(words, " "); !ok || got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestSplitCase(t *testing.T) {
	for in, want := range map[string][]string{
		"camelCase":  {"camel", "Case"},
		"HTTPServer": {"HTTP", "Server"},
		"macOS":      {"mac", "OS"},
		"APNs":       {"APNs"},
		"iPhone":     {"i", "Phone"},
		"SwiftUI":    {"Swift", "UI"},
		"XcodeGen":   {"Xcode", "Gen"},
	} {
		if got := splitCase(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// RINGBACK_KOKORO_DIR names Kokoro's folder (kokoro-multi-lang-v1_0), for its lexicon and
// tokens.
func load(t *testing.T) *G2P {
	dir := os.Getenv("RINGBACK_KOKORO_DIR")
	if dir == "" {
		t.Skip("RINGBACK_KOKORO_DIR not set")
	}
	lex, err := LoadLexicon(filepath.Join(dir, "lexicon-us-en.txt"))
	if err != nil {
		t.Fatal(err)
	}
	vocab, err := LoadVocab(filepath.Join(dir, "tokens.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return New(lex, vocab, nil)
}

// Sentences whose words sherpa-onnx's own front end dropped, and others like them.
var oovSentences = []string{
	"I fixed the bug in voice_server.py at line 42, it was an off-by-one error.",
	"All 142 tests pass, and the build took 38 seconds.",
	"The ringback mod runs Parakeet through sherpa-onnx on the Nebula tunnel.",
	"Run tools/call-check, then merge the go-port branch into main.",
	"The node uses 2.1 GB of VRAM on a GPU, and 500ms is 0.5 s.",
	"APNs took the push in 1.2 s; the hub's PID is 48213.",
	"It's version 2.0.1, from 2026, about 3x faster than the GPU's 24kHz path.",
	"Open hub/server.go and look at handleSpeech, then camelCase and HTTPServer.",
}

func TestEveryWordIsSpoken(t *testing.T) {
	g := load(t)
	for _, s := range oovSentences {
		chunks := g.Chunks(s)
		t.Logf("%s\n    %s", s, strings.Join(chunks, " | "))
		for _, it := range g.items(s) {
			if !it.punct && g.clean(it.ps) == "" {
				t.Errorf("%q: %q has no sound", s, it.word)
			}
		}
		for _, c := range chunks {
			if runeLen(c) > MaxChunk {
				t.Errorf("chunk of %d phonemes", runeLen(c))
			}
		}
	}
}

func TestLongTextIsCut(t *testing.T) {
	g := load(t)
	long := strings.Repeat("This sentence goes on, and on, and on without a full stop for quite a while ", 12)
	chunks := g.Chunks(long + ".")
	if len(chunks) < 2 {
		t.Fatalf("one chunk of %d phonemes", runeLen(chunks[0]))
	}
	for _, c := range chunks {
		if runeLen(c) > MaxChunk {
			t.Errorf("chunk of %d phonemes", runeLen(c))
		}
	}
}

func TestFromEspeak(t *testing.T) {
	// espeak-ng's "onnx" and "kubernetes", with diphthongs tied by ^
	for in, want := range map[string]string{
		"ˈɑːnks":         "ˈɑnks",
		"kjˌuːbɚnˈɛɾiːz": "kjˌubəɹnˈɛɾiz",
		"pˈa͡ɪpkæt":      "pˈIpkæt",
		"ɡˈo͡ʊlæŋ":       "ɡˈOlæŋ",
	} {
		if got := FromEspeak(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
