package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// prefix is the first n characters of s, as the logs shorten call and session IDs.
func prefix(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// skip is s without its first n characters.
func skip(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[i:]
		}
		n--
	}
	return ""
}

// words counts the words in s, as Python's str.split() splits them.
func words(s string) int { return len(strings.Fields(s)) }

// cut is text cut to most characters, with "…" where it was cut.
func cut(text string, most int) string {
	r := []rune(text)
	if len(r) <= most {
		return text
	}
	return string(r[:most-1]) + "…"
}

var plainWords = regexp.MustCompile(`[a-z0-9]+`)

// plain is text without case or punctuation, to compare what the model wrote with what was asked.
func plain(text string) string {
	return strings.Join(plainWords.FindAllString(strings.ToLower(text), -1), " ")
}

// duration is seconds as written for a person: "45 s", "1 min 5 s", "2 min".
func duration(seconds float64) string {
	total := int(math.RoundToEven(seconds))
	minutes, secs := total/60, total%60
	if minutes == 0 {
		return fmt.Sprintf("%d s", secs)
	}
	if secs != 0 {
		return fmt.Sprintf("%d min %d s", minutes, secs)
	}
	return fmt.Sprintf("%d min", minutes)
}

// whileAgo is seconds as said in "... ago": "less than a minute", "12 minutes", "about 3 hours".
func whileAgo(seconds float64) string {
	minutes := int(math.RoundToEven(seconds / 60))
	if minutes < 1 {
		return "less than a minute"
	}
	if minutes < 60 {
		if minutes == 1 {
			return "a minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	hours := int(math.RoundToEven(seconds / 3600))
	if hours == 1 {
		return "about an hour"
	}
	return fmt.Sprintf("about %d hours", hours)
}

// pyrepr is a value as Python's repr() writes it, for the log lines: strings in single
// quotes (double if they hold one and no double), dicts and
// lists with their items so.
func pyrepr(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case string:
		return reprString(v)
	case bool:
		if v {
			return "True"
		}
		return "False"
	case json.Number:
		return v.String()
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1e16 {
			return fmt.Sprintf("%.1f", v)
		}
		return fmt.Sprint(v)
	case []string:
		items := make([]string, len(v))
		for i, s := range v {
			items[i] = reprString(s)
		}
		return "[" + strings.Join(items, ", ") + "]"
	case []any:
		items := make([]string, len(v))
		for i, item := range v {
			items[i] = pyrepr(item)
		}
		return "[" + strings.Join(items, ", ") + "]"
	case pairs:
		items := make([]string, len(v))
		for i, p := range v {
			items[i] = reprString(p.Key) + ": " + reprString(p.Value)
		}
		return "{" + strings.Join(items, ", ") + "}"
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		items := make([]string, len(keys))
		for i, k := range keys {
			items[i] = reprString(k) + ": " + pyrepr(v[k])
		}
		return "{" + strings.Join(items, ", ") + "}"
	}
	return fmt.Sprint(value)
}

func reprString(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == quote:
			b.WriteRune('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r != ' ' && !unicode.IsPrint(r):
			switch {
			case r < 0x100:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r < 0x10000:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}

// pair is one question's answer; pairs keep the questions' order, as a Python dict does, in
// the log and in JSON.
type pair struct{ Key, Value string }

type pairs []pair

func (p pairs) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("null"), nil
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, kv := range p {
		if i > 0 {
			b.WriteString(", ")
		}
		k, _ := json.Marshal(kv.Key)
		v, _ := json.Marshal(kv.Value)
		b.Write(k)
		b.WriteString(": ")
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func (p *pairs) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		*p = nil
		return nil
	}
	if token != json.Delim('{') {
		return fmt.Errorf("answers aren't an object")
	}
	got := pairs{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		got = append(got, pair{key.(string), value})
	}
	*p = got
	return nil
}

// The marks that end a sentence, as Pipecat's match_endofsentence knows them; those outside
// Latin script end one anywhere.
const (
	sentenceEnds    = ".!?;…。？！；．｡।॥؟؛۔؏၊။។៕໌།༎։՜՞።፧፨"
	unambiguousEnds = "。？！；．｡।॥؟؛۔؏၊။។៕໌།༎։՜՞።፧፨"
)

// Words that end in a period without ending a sentence.
var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true, "sr": true, "jr": true, "st": true,
	"vs": true, "approx": true, "dept": true, "fig": true, "inc": true, "ltd": true,
}

// matchEndOfSentence is where the first sentence of text ends, in bytes, or 0 if none has
// ended yet. Pipecat's match_endofsentence uses sentencex's tokenizer, which Go doesn't have;
// this follows it for English: ".", "!" or "?" with closing quotes, then a space, a capital
// or the end, but not after an abbreviation or an initial ("Dr.", "U.S."); ";" and "…" end
// a sentence only at the end of the text. It decides only how much of a reply you must hear
// before what the model held goes, and when an opener is long enough to check
// (testdata/golden.json has Pipecat's answers for the cases that matter).
func matchEndOfSentence(text string) int {
	text = strings.TrimRightFunc(text, unicode.IsSpace)
	if text == "" {
		return 0
	}
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r != '.' && r != '!' && r != '?' {
			i += size
			continue
		}
		end := i + size
		for end < len(text) && strings.IndexByte(".!?", text[end]) >= 0 {
			end++
		}
		for end < len(text) {
			q, n := utf8.DecodeRuneInString(text[end:])
			if !strings.ContainsRune(`"')]”’»`, q) {
				break
			}
			end += n
		}
		if end == len(text) {
			return end
		}
		if r == '.' && end == i+1 && abbreviated(text[:i]) {
			i = end
			continue
		}
		if strings.HasPrefix(text[i:], "..") {
			// An ellipsis, as "…", ends a sentence only at the end of the text.
			i = end
			continue
		}
		if next, _ := utf8.DecodeRuneInString(text[end:]); unicode.IsSpace(next) || unicode.IsUpper(next) {
			return end
		}
		i = end
	}
	if last, _ := utf8.DecodeLastRuneInString(text); strings.ContainsRune(sentenceEnds, last) {
		return len(text)
	}
	for i, r := range text {
		if strings.ContainsRune(unambiguousEnds, r) {
			return i + utf8.RuneLen(r)
		}
	}
	return 0
}

// abbreviated says whether the word before a period is an abbreviation or an initial.
func abbreviated(before string) bool {
	start := len(before)
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(before[:start])
		if !unicode.IsLetter(r) && r != '.' {
			break
		}
		start -= size
	}
	word := before[start:]
	if word == "" {
		return false
	}
	if strings.Contains(word, ".") || abbreviations[strings.ToLower(word)] {
		return true
	}
	r, size := utf8.DecodeRuneInString(word)
	return size == len(word) && unicode.IsUpper(r)
}
