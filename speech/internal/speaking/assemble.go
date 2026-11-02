package speaking

import (
	"regexp"
	"strings"
)

const vowels = "AIOQWYaiuæɑɒɔəɛɜɪʊʌᵻ"

func startsWithVowel(ps string) bool {
	for _, c := range ps {
		if c == 'ˈ' || c == 'ˌ' {
			continue
		}
		return strings.ContainsRune(vowels, c)
	}
	return false
}

// byContext reads the words whose sound depends on what follows, as misaki does: the
// lexicon has their citation forms ("the" as ði, "a" as A), which sound stilted in a
// sentence.
func (g *G2P) byContext(items []item) {
	for i := range items {
		it := &items[i]
		if it.punct {
			continue
		}
		var next *item // the next word, if no punctuation comes first
		if i+1 < len(items) && !items[i+1].punct {
			next = &items[i+1]
		}
		switch it.word {
		case "the":
			if next != nil && startsWithVowel(next.ps) {
				it.ps = "ði"
			} else {
				it.ps = "ðə"
			}
		case "to":
			if next != nil {
				if startsWithVowel(next.ps) {
					it.ps = "tʊ"
				} else {
					it.ps = "tə"
				}
			}
		case "a":
			if next != nil {
				it.ps = "ɐ"
			}
		case "an":
			it.ps = "ɐn"
		case "i":
			it.ps = "ˌI"
		case "point":
			// unstressed in a number, "two point one"
			if i > 0 && numberWords[items[i-1].word] && next != nil && numberWords[next.word] {
				it.ps = strings.NewReplacer("ˈ", "", "ˌ", "").Replace(it.ps)
			}
		}
	}
}

var numberWords = func() map[string]bool {
	m := map[string]bool{"hundred": true}
	for _, list := range [][]string{ones, tens, scales} {
		for _, w := range list {
			m[w] = w != ""
		}
	}
	return m
}()

// chunk joins the items into phoneme strings: sentences, short ones together, long ones cut
// at a comma or, failing that, between words.
func (g *G2P) chunk(items []item) []string {
	type part struct {
		text  string
		words int
	}
	var sentences []part
	var b strings.Builder
	words := 0
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			sentences = append(sentences, part{s, words})
		}
		b.Reset()
		words = 0
	}
	for _, it := range items {
		ps := g.clean(it.ps)
		if ps == "" {
			continue
		}
		if it.punct {
			// Punctuation follows its word with no space; one that starts a sentence is
			// dropped.
			s := strings.TrimRight(b.String(), " ")
			if s == "" {
				continue
			}
			b.Reset()
			b.WriteString(s + ps + " ")
			if strings.Contains(".!?…", ps) {
				flush()
			}
			continue
		}
		b.WriteString(ps + " ")
		words++
	}
	flush()

	// Cut what's too long: at clause punctuation, then between words.
	var parts []part
	for _, s := range sentences {
		if runeLen(s.text) <= MaxChunk {
			parts = append(parts, s)
			continue
		}
		var cur strings.Builder
		curWords := 0
		emit := func() {
			if t := strings.TrimSpace(cur.String()); t != "" {
				parts = append(parts, part{t, curWords})
			}
			cur.Reset()
			curWords = 0
		}
		for _, clause := range splitAfter(s.text, ",;:—") {
			if runeLen(cur.String())+runeLen(clause) > MaxChunk {
				emit()
			}
			if runeLen(clause) > MaxChunk {
				for _, w := range strings.Fields(clause) {
					if runeLen(cur.String())+runeLen(w)+1 > MaxChunk {
						emit()
					}
					cur.WriteString(w + " ")
					curWords++
				}
				continue
			}
			cur.WriteString(clause + " ")
			curWords += len(strings.Fields(clause))
		}
		emit()
	}

	// Short sentences go with the next one, and a short last one with the one before.
	var chunks []part
	for _, p := range parts {
		if n := len(chunks); n > 0 && chunks[n-1].words < minWords && runeLen(chunks[n-1].text)+1+runeLen(p.text) <= MaxChunk {
			chunks[n-1] = part{chunks[n-1].text + " " + p.text, chunks[n-1].words + p.words}
			continue
		}
		chunks = append(chunks, p)
	}
	if n := len(chunks); n > 1 && chunks[n-1].words < minWords && runeLen(chunks[n-2].text)+1+runeLen(chunks[n-1].text) <= MaxChunk {
		chunks[n-2] = part{chunks[n-2].text + " " + chunks[n-1].text, chunks[n-2].words + chunks[n-1].words}
		chunks = chunks[:n-1]
	}
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.text
	}
	return out
}

// clean applies misaki's last step (a flap is T and a glottal stop t, for Kokoro v1.0) and
// keeps only the sounds Kokoro has.
func (g *G2P) clean(ps string) string {
	ps = strings.NewReplacer("ɾ", "T", "ʔ", "t").Replace(ps)
	return strings.Map(func(c rune) rune {
		if g.vocab[c] && c != ' ' {
			return c
		}
		if c == ' ' {
			return c // between the words of one item, such as espeak's
		}
		return -1
	}, ps)
}

func splitAfter(s, cuts string) []string {
	var out []string
	start := 0
	for i, c := range s {
		if strings.ContainsRune(cuts, c) {
			end := i + len(string(c))
			out = append(out, strings.TrimSpace(s[start:end]))
			start = end
		}
	}
	if rest := strings.TrimSpace(s[start:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

func runeLen(s string) int { return len([]rune(s)) }

// espeak-ng's sounds in misaki's phoneme set (misaki's EspeakFallback, American English),
// longest first.
var fromEspeak = strings.NewReplacer(
	"ʔˌn̩", "ʔn", "ʔn̩", "ʔn",
	"a^ɪ", "I", "a^ʊ", "W", "d^ʒ", "ʤ", "e^ɪ", "A", "t^ʃ", "ʧ", "ɔ^ɪ", "Y", "ə^l", "ᵊl",
	"ʲo", "jo", "ʲə", "jə",
	"e", "A", "ʲ", "", "ɚ", "əɹ", "r", "ɹ", "x", "k", "ç", "k", "ɐ", "ə", "ɬ", "l", "̃", "",
)

var (
	syllabic = regexp.MustCompile(`(\S)̩`)
	langFlag = regexp.MustCompile(`\([a-z-]+\)`)
	usEspeak = strings.NewReplacer("o^ʊ", "O", "ɜːɹ", "ɜɹ", "ɜː", "ɜɹ", "ɪə", "iə", "ː", "")
)

// FromEspeak turns espeak-ng's IPA for a word (with ^ tying the sounds of a diphthong) into
// misaki's phonemes.
func FromEspeak(ipa string) string {
	ps := strings.ReplaceAll(ipa, "͡", "^")
	ps = strings.TrimSpace(langFlag.ReplaceAllString(ps, ""))
	ps = strings.Join(strings.Fields(ps), " ")
	ps = fromEspeak.Replace(ps)
	ps = syllabic.ReplaceAllString(ps, "ᵊ$1")
	ps = strings.ReplaceAll(ps, "̩", "")
	ps = usEspeak.Replace(ps)
	ps = strings.ReplaceAll(ps, "o", "ɔ")
	return strings.ReplaceAll(ps, "^", "")
}
