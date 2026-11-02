// Package speaking turns a piece of a reply into Kokoro's phonemes. sherpa-onnx 2.x reads text
// only through its lexicon and silently drops every word it doesn't have (numbers, file
// names, "onnx"), so the node does what the Python voice's G2P (misaki) does instead: words
// from the same lexicon, numbers and symbols read out, identifiers split into their words,
// acronyms spelled, other unknown words through espeak-ng, and the result handed to
// sherpa-onnx as phonemes.
package speaking

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// Lexicon maps lowercase words to their phonemes, as in lexicon-us-en.txt (misaki's).
type Lexicon map[string]string

// LoadLexicon reads a lexicon file: a word per line, then its phonemes, all separated by
// spaces.
func LoadLexicon(path string) (Lexicon, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	lex := Lexicon{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		word, phonemes, ok := strings.Cut(sc.Text(), " ")
		if ok && word != "" {
			lex[word] = strings.ReplaceAll(phonemes, " ", "")
		}
	}
	return lex, sc.Err()
}

// LoadVocab reads Kokoro's tokens.txt: a token and its id per line (the space token's line
// starts with a space).
func LoadVocab(path string) (map[rune]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	vocab := map[rune]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		i := strings.LastIndex(line, " ")
		if i <= 0 {
			continue
		}
		if r := []rune(line[:i]); len(r) == 1 {
			vocab[r[0]] = true
		}
	}
	if len(vocab) == 0 {
		return nil, fmt.Errorf("no tokens in %s", path)
	}
	return vocab, nil
}

// G2P is the node's text-to-phoneme front end.
type G2P struct {
	lex      Lexicon
	vocab    map[rune]bool
	fallback func(word string) string // espeak-ng; nil if it isn't installed
	letters  map[rune]string
}

const (
	// MaxChunk is the most phonemes Kokoro gets at once (it takes 510 tokens; sherpa-onnx
	// cuts at 400).
	MaxChunk = 400
	// minWords: shorter sentences are spoken together with the next, as sherpa-onnx does.
	minWords = 5
)

// New makes a front end; fallback, if not nil, gives the phonemes of a word that's in no
// lexicon (espeak-ng's, already in misaki's phoneme set) or "".
func New(lex Lexicon, vocab map[rune]bool, fallback func(string) string) *G2P {
	g := &G2P{lex: lex, vocab: vocab, fallback: fallback, letters: map[rune]string{}}
	for c := 'a'; c <= 'z'; c++ {
		g.letters[c] = lex[string(c)]
	}
	g.letters['a'] = "ˈA" // the lexicon's "a" is the article
	return g
}

// item is a word's phonemes or a punctuation mark.
type item struct {
	ps    string
	punct bool
	word  string // the word as written, lowercase, for the words read by context
	last  bool   // the last word of its token
}

// Chunks turns a piece of a reply into phonemes, in chunks of whole sentences (or clauses,
// for a long sentence) of at most MaxChunk phonemes, to be spoken one after the other.
func (g *G2P) Chunks(text string) []string {
	items := g.items(text)
	g.byContext(items)
	return g.chunk(items)
}

// Phonemes is the whole piece as one string, for checking what the voice will say.
func (g *G2P) Phonemes(text string) string {
	return strings.Join(g.Chunks(text), " ")
}

var replacer = strings.NewReplacer(
	"’", "'", "‘", "'", "ʼ", "'", "“", `"`, "”", `"`, "...", "…", "--", " — ", " – ", " — ",
	" - ", " — ", "\t", " ", "\n", " ", "\r", " ",
)

const (
	leading  = `"'([{<`
	trailing = `,.!?;:…—)]}>"'`
	spoken   = ",.!?;:…—" // punctuation Kokoro hears
)

func (g *G2P) items(text string) []item {
	var items []item
	tokens := strings.Fields(replacer.Replace(text))
	for i := 0; i < len(tokens); i++ {
		core := strings.TrimLeft(tokens[i], leading)
		end := len(core)
		for end > 0 && strings.ContainsRune(trailing, lastRune(core[:end])) {
			_, size := lastRuneSize(core[:end])
			end -= size
		}
		punct := core[end:]
		core = core[:end]
		// Abbreviations keep their full stop: "etc.", "Dr.".
		if strings.HasPrefix(punct, ".") && core != "" {
			if _, ok := g.lex[strings.ToLower(core)+"."]; ok || abbreviations[strings.ToLower(core)+"."] != "" {
				core, punct = core+".", punct[1:]
			}
		}
		if core == "" && punct == "" {
			continue
		}
		// A unit after a number: "38 s", "500 ms", "42 %".
		if isNumber(core) && punct == "" && i+1 < len(tokens) {
			next := strings.TrimRight(tokens[i+1], trailing)
			if u, ok := units[strings.ToLower(next)]; ok && next != "x" {
				words, _ := spokenNumber(core)
				items = append(items, g.lookupAll(append(words, unitWords(u, core)...))...)
				core, punct = "", tokens[i+1][len(next):]
				i++
			}
		}
		items = append(items, g.token(core)...)
		if len(items) > 0 && !items[len(items)-1].punct {
			items[len(items)-1].last = true
		}
		for _, c := range punct {
			if strings.ContainsRune(spoken, c) {
				items = append(items, item{ps: string(c), punct: true})
			}
		}
	}
	return items
}

var abbreviations = map[string]string{
	"e.g.": "for example", "i.e.": "that is", "vs.": "versus", "etc.": "et cetera",
	"approx.": "approximately", "no.": "number",
}

// separators inside a token and how they're read; "" is silent.
var separators = map[rune]string{
	'_': "", '-': "", '.': "dot", '/': "slash", '\\': "backslash", '@': "at", '#': "hash",
	'&': "and", '+': "plus", '=': "equals", '~': "tilde", ':': "colon", '%': "percent",
	'*': "star", '|': "pipe", '^': "", '$': "", '`': "", '"': "", '\'': "", '<': "", '>': "",
	'(': "", ')': "", '[': "", ']': "", '{': "", '}': "", ',': "", ';': "", '!': "", '?': "",
}

// extensions read after a dot.
var extensions = map[string][]string{
	"py": {"pie"}, "txt": {"text"}, "json": {"jason"}, "js": {"j", "s"}, "ts": {"t", "s"},
	"md": {"m", "d"}, "sh": {"s", "h"}, "rs": {"r", "s"}, "yml": {"yaml"}, "cc": {"c", "c"},
}

// token reads one whitespace-separated token without its surrounding punctuation.
func (g *G2P) token(s string) []item {
	if s == "" {
		return nil
	}
	lower := strings.ToLower(s)
	if words := abbreviations[lower]; words != "" {
		return g.lookupAll(strings.Fields(words))
	}
	if ps, ok := g.lex[lower]; ok {
		return []item{{ps: ps, word: lower}}
	}
	if words, ok := spokenNumber(s); ok {
		return g.lookupAll(words)
	}
	if strings.HasPrefix(s, "#") && len(s) > 1 && isDigits(s[1:]) {
		return g.lookupAll(append([]string{"number"}, cardinalOf(s[1:])...))
	}
	if isWord(s) {
		return g.word(s)
	}
	// A hyphenated word reads its parts; anything else is an identifier or a symbol.
	if parts := strings.Split(s, "-"); len(parts) > 1 && allWords(parts) {
		var out []item
		for _, p := range parts {
			out = append(out, g.token(p)...)
		}
		return out
	}
	return g.identifier(s)
}

// identifier reads a file name, a path or a name from code: "voice_server.py" is "voice
// server dot pie", "camelCase" "camel case", "mp3" "m p three".
func (g *G2P) identifier(s string) []item {
	var out []item
	runes := []rune(s)
	afterDot := false
	for i := 0; i < len(runes); {
		c := runes[i]
		switch {
		case unicode.IsLetter(c) || c == '\'':
			j := i
			for j < len(runes) && (unicode.IsLetter(runes[j]) || runes[j] == '\'') {
				j++
			}
			part := string(runes[i:j])
			if ext, ok := extensions[strings.ToLower(part)]; ok && afterDot {
				out = append(out, g.lookupAll(ext)...)
			} else {
				for _, w := range splitCase(part) {
					out = append(out, g.word(w)...)
				}
			}
			i = j
			afterDot = false
		case unicode.IsDigit(c):
			j := i
			for j < len(runes) && unicode.IsDigit(runes[j]) {
				j++
			}
			out = append(out, g.lookupAll(inner(string(runes[i:j])))...)
			i = j
			afterDot = false
		default:
			if word, ok := separators[c]; ok {
				if word != "" {
					out = append(out, g.lookupAll([]string{word})...)
				}
			} else if !unicode.IsSymbol(c) && !unicode.IsPunct(c) && !unicode.IsSpace(c) {
				out = append(out, g.word(string(c))...)
			} // other symbols are left out
			afterDot = c == '.'
			i++
		}
	}
	return out
}

// splitCase splits camelCase and PascalCase: "camelCase" "camel" "Case", "HTTPServer" "HTTP"
// "Server", "macOS" "mac" "OS"; "APNs" and "GPUs" stay whole.
func splitCase(s string) []string {
	r := []rune(s)
	var out []string
	start := 0
	for i := 1; i < len(r); i++ {
		lowerToUpper := unicode.IsLower(r[i-1]) && unicode.IsUpper(r[i])
		acronymEnd := unicode.IsUpper(r[i-1]) && unicode.IsUpper(r[i]) && i+1 < len(r) &&
			unicode.IsLower(r[i+1]) && !(i+2 == len(r) && r[i+1] == 's')
		if lowerToUpper || acronymEnd {
			out = append(out, string(r[start:i]))
			start = i
		}
	}
	return append(out, string(r[start:]))
}

// Short acronyms are spelled even when the lexicon has a word with their letters ("OS",
// "UI", "IT"), apart from these.
var sayAsWord = map[string]bool{"ok": true, "ram": true, "rom": true}

// aliases: words read as another word the lexicon has.
var aliases = map[string]string{"json": "jason"}

// word reads a word made of letters (and apostrophes).
func (g *G2P) word(w string) []item {
	lower := strings.ToLower(w)
	letters := []rune(strings.ReplaceAll(w, "'", ""))
	if len(letters) == 0 {
		return nil
	}
	upper := strings.ToUpper(string(letters))
	allCaps := len(letters) > 1 && string(letters) == upper && upper != strings.ToLower(upper)
	if allCaps && len(letters) <= 3 && !sayAsWord[lower] {
		return []item{{ps: g.spell(lower), word: lower}}
	}
	if ps, ok := g.lex[lower]; ok {
		return []item{{ps: ps, word: lower}}
	}
	if alias, ok := aliases[lower]; ok {
		return []item{{ps: g.lex[alias], word: lower}}
	}
	trimmed := strings.Trim(lower, "'")
	if ps, ok := g.lex[trimmed]; ok {
		return []item{{ps: ps, word: trimmed}}
	}
	if ps := g.stem(trimmed); ps != "" {
		return []item{{ps: ps, word: trimmed}}
	}
	// Acronyms: "GPU", "APNs" (a plural), and words without vowels: "rtc".
	if allCaps {
		return []item{{ps: g.spell(lower), word: lower}}
	}
	if len(letters) > 2 && string(letters[len(letters)-1]) == "s" && string(letters[:len(letters)-1]) == strings.ToUpper(string(letters[:len(letters)-1])) {
		return []item{{ps: pluralPhonemes(g.spell(strings.ToLower(string(letters[:len(letters)-1])))), word: lower}}
	}
	if !strings.ContainsAny(lower, "aeiouy") {
		return []item{{ps: g.spell(lower), word: lower}}
	}
	if len(letters) == 1 {
		return []item{{ps: g.spell(lower), word: lower}}
	}
	if g.fallback != nil {
		if ps := g.fallback(w); ps != "" {
			return []item{{ps: ps, word: lower}}
		}
	}
	if parts := g.compound(trimmed); parts != nil {
		var out []item
		for _, p := range parts {
			out = append(out, item{ps: g.lex[p], word: p})
		}
		return out
	}
	return []item{{ps: g.spell(lower), word: lower}}
}

// spell reads letters by name, stressed on the last one as misaki does: "gpu" ʤˌipˌijˈu.
func (g *G2P) spell(s string) string {
	var b strings.Builder
	for _, c := range s {
		ps := g.letters[unicode.ToLower(c)]
		if ps == "" {
			if c >= '0' && c <= '9' {
				ps = g.lex[ones[c-'0']]
			} else {
				continue
			}
		}
		b.WriteString(strings.NewReplacer("ˌ", "", "ˈ", "ˌ").Replace(ps))
	}
	ps := b.String()
	if i := strings.LastIndex(ps, "ˌ"); i >= 0 {
		ps = ps[:i] + "ˈ" + ps[i+len("ˌ"):]
	}
	return ps
}

// compound splits a word the lexicon doesn't have into two or three words it has, each of
// at least three letters ("pipecat" is "pipe" "cat"); nil if it can't.
func (g *G2P) compound(w string) []string {
	known := func(s string) bool { _, ok := g.lex[s]; return len(s) >= 3 && ok }
	for i := 3; i <= len(w)-3; i++ {
		if known(w[:i]) && known(w[i:]) {
			return []string{w[:i], w[i:]}
		}
	}
	for i := 3; i <= len(w)-6; i++ {
		for j := i + 3; j <= len(w)-3; j++ {
			if known(w[:i]) && known(w[i:j]) && known(w[j:]) {
				return []string{w[:i], w[i:j], w[j:]}
			}
		}
	}
	return nil
}

func (g *G2P) lookupAll(words []string) []item {
	var out []item
	for _, w := range words {
		out = append(out, g.word(w)...)
	}
	return out
}

func lastRune(s string) rune {
	r := []rune(s)
	return r[len(r)-1]
}

func lastRuneSize(s string) (rune, int) {
	r := lastRune(s)
	return r, len(string(r))
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func cardinalOf(s string) []string { return integer(s, false) }

// isWord: letters and apostrophes only, in one case or capitalised (not camelCase).
func isWord(s string) bool {
	for _, c := range s {
		if !unicode.IsLetter(c) && c != '\'' {
			return false
		}
	}
	return len(splitCase(s)) == 1
}

func allWords(parts []string) bool {
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if !unicode.IsLetter(c) && !unicode.IsDigit(c) {
				return false
			}
		}
	}
	return true
}
