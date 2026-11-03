package speaking

import (
	"regexp"
	"strconv"
	"strings"
)

// Numbers become words before they become phonemes, the way misaki, Kokoro's own G2P,
// reads them: no "and" in "one hundred forty two", four digits as a year.

var (
	ones = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine",
		"ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen",
		"eighteen", "nineteen"}
	tens   = []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
	scales = []string{"", "thousand", "million", "billion", "trillion", "quadrillion"}
)

// cardinal reads n in words.
func cardinal(n uint64) []string {
	if n == 0 {
		return []string{"zero"}
	}
	var groups [][]string
	for i := 0; n > 0 && i < len(scales); i++ {
		g := n % 1000
		n /= 1000
		if g == 0 {
			groups = append(groups, nil)
			continue
		}
		words := belowThousand(int(g))
		if scales[i] != "" {
			words = append(words, scales[i])
		}
		groups = append(groups, words)
	}
	var out []string
	for i := len(groups) - 1; i >= 0; i-- {
		out = append(out, groups[i]...)
	}
	return out
}

func belowThousand(n int) []string {
	var out []string
	if n >= 100 {
		out = append(out, ones[n/100], "hundred")
		n %= 100
	}
	switch {
	case n >= 20:
		out = append(out, tens[n/10])
		if n%10 != 0 {
			out = append(out, ones[n%10])
		}
	case n > 0:
		out = append(out, ones[n])
	}
	return out
}

// year reads a four-digit number as a year, as num2words does: 2026 is "twenty twenty six",
// 1905 "nineteen oh five", 2005 "two thousand five".
func year(n uint64) []string {
	hi, lo := n/100, n%100
	if hi == 0 || (hi%10 == 0 && lo < 10) || hi >= 100 {
		return cardinal(n)
	}
	out := cardinal(hi)
	switch {
	case lo == 0:
		return append(out, "hundred")
	case lo < 10:
		return append(append(out, "oh"), cardinal(lo)...)
	}
	return append(out, cardinal(lo)...)
}

var ordinalOf = map[string]string{
	"one": "first", "two": "second", "three": "third", "five": "fifth", "eight": "eighth",
	"nine": "ninth", "twelve": "twelfth",
}

// ordinal reads n as "first", "twenty second"…
func ordinal(n uint64) []string {
	words := cardinal(n)
	last := words[len(words)-1]
	switch {
	case ordinalOf[last] != "":
		last = ordinalOf[last]
	case strings.HasSuffix(last, "y"):
		last = strings.TrimSuffix(last, "y") + "ieth"
	default:
		last += "th"
	}
	words[len(words)-1] = last
	return words
}

// plural reads "the 90s" and "the 1990s": the last word in the plural.
func plural(words []string) []string {
	last := words[len(words)-1]
	switch {
	case strings.HasSuffix(last, "y"):
		last = strings.TrimSuffix(last, "y") + "ies"
	case strings.HasSuffix(last, "x"):
		last += "es"
	default:
		last += "s"
	}
	return append(words[:len(words)-1:len(words)-1], last)
}

// digits reads a string of digits one at a time.
func digits(s string) []string {
	var out []string
	for _, c := range s {
		if c >= '0' && c <= '9' {
			out = append(out, ones[c-'0'])
		}
	}
	return out
}

// integer reads digits at the head of a token: a year if there are exactly four, otherwise a
// cardinal (digit by digit if they're too many for one).
func integer(s string, asYear bool) []string {
	s = strings.ReplaceAll(s, ",", "")
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || len(s) > 18 {
		return digits(s)
	}
	if asYear && len(s) == 4 {
		return year(n)
	}
	return cardinal(n)
}

// inner reads digits inside a word, such as the 3 of "mp3" or the 64 of "arm64": up to three
// digits as a number, longer ones or ones with a leading zero digit by digit.
func inner(s string) []string {
	if len(s) > 3 || (len(s) > 1 && s[0] == '0') {
		return digits(s)
	}
	return integer(s, false)
}

// decimal reads "3.14" as "three point one four".
func decimal(s string, asYear bool) []string {
	whole, frac, hasFrac := strings.Cut(s, ".")
	var out []string
	if whole != "" {
		out = integer(whole, asYear && !hasFrac)
	} else {
		out = []string{"zero"}
	}
	if hasFrac && frac != "" {
		out = append(out, "point")
		out = append(out, digits(frac)...)
	}
	return out
}

type unit struct{ one, many string }

// units that can follow a number, attached ("500ms") or as the next word ("38 s").
var units = map[string]unit{
	"ms": {"millisecond", "milliseconds"}, "s": {"second", "seconds"}, "sec": {"second", "seconds"},
	"secs": {"second", "seconds"}, "min": {"minute", "minutes"}, "mins": {"minute", "minutes"},
	"h": {"hour", "hours"}, "hr": {"hour", "hours"}, "hrs": {"hour", "hours"},
	"kb": {"kilobyte", "kilobytes"}, "mb": {"megabyte", "megabytes"}, "gb": {"gigabyte", "gigabytes"},
	"tb": {"terabyte", "terabytes"}, "hz": {"hertz", "hertz"}, "khz": {"kilohertz", "kilohertz"},
	"mhz": {"megahertz", "megahertz"}, "ghz": {"gigahertz", "gigahertz"},
	"kbps": {"kilobits per second", "kilobits per second"}, "mbps": {"megabits per second", "megabits per second"},
	"fps": {"frame per second", "frames per second"}, "px": {"pixel", "pixels"},
	"db": {"decibel", "decibels"}, "k": {"thousand", "thousand"}, "x": {"times", "times"},
	"×": {"times", "times"}, "%": {"percent", "percent"},
}

func unitWords(u unit, number string) []string {
	if number == "1" {
		return strings.Fields(u.one)
	}
	return strings.Fields(u.many)
}

var currencies = map[string]unit{"$": {"dollar", "dollars"}, "€": {"euro", "euros"}, "£": {"pound", "pounds"}}

var (
	plainNumber = regexp.MustCompile(`^([+-]?)(\d{1,3}(?:,\d{3})+|\d+)?(\.\d+)?$`)
	currencyRe  = regexp.MustCompile(`^([$€£])(\d{1,3}(?:,\d{3})+|\d+)(?:\.(\d{1,2}))?$`)
	ordinalRe   = regexp.MustCompile(`^(\d+)(st|nd|rd|th)$`)
	decadeRe    = regexp.MustCompile(`^(\d0|\d{3}0)s$`)
	timeRe      = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
	versionRe   = regexp.MustCompile(`^(v?)(\d+(?:\.\d+){2,}|\d+(?:\.\d+)*)$`)
	rangeRe     = regexp.MustCompile(`^(\d[\d,]*(?:\.\d+)?)[-–](\d[\d,]*(?:\.\d+)?)$`)
	withUnitRe  = regexp.MustCompile(`^(\d[\d,]*(?:\.\d+)?)([a-zA-Z%×]+)$`)
)

// isNumber says whether a token is a plain number, for a unit that may follow it.
func isNumber(s string) bool {
	m := plainNumber.FindStringSubmatch(s)
	return m != nil && (m[2] != "" || m[3] != "")
}

// spokenNumber reads a token that is a number, with what comes with it ("$5", "42%", "1st",
// "the 90s", "12:30", "v2.0.1", "2-10", "500ms"), as words.
func spokenNumber(s string) ([]string, bool) {
	if m := currencyRe.FindStringSubmatch(s); m != nil {
		c := currencies[m[1]]
		out := append(integer(m[2], false), unitWords(c, strings.ReplaceAll(m[2], ",", ""))...)
		if cents := strings.TrimLeft(m[3], "0"); cents != "" {
			if len(m[3]) == 1 {
				cents += "0"
			}
			out = append(out, "and")
			out = append(out, integer(cents, false)...)
			if cents == "1" {
				out = append(out, "cent")
			} else {
				out = append(out, "cents")
			}
		}
		return out, true
	}
	if m := plainNumber.FindStringSubmatch(s); m != nil && (m[2] != "" || m[3] != "") {
		var out []string
		switch m[1] {
		case "-":
			out = append(out, "minus")
		case "+":
			out = append(out, "plus")
		}
		return append(out, decimal(m[2]+m[3], m[3] == "" && !strings.Contains(m[2], ","))...), true
	}
	if m := ordinalRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseUint(m[1], 10, 64)
		if err == nil {
			return ordinal(n), true
		}
	}
	if m := decadeRe.FindStringSubmatch(s); m != nil {
		return plural(integer(m[1], true)), true
	}
	if m := timeRe.FindStringSubmatch(s); m != nil {
		h, _ := strconv.ParseUint(m[1], 10, 64)
		min, _ := strconv.ParseUint(m[2], 10, 64)
		out := cardinal(h)
		switch {
		case min == 0:
			return append(out, "o'clock"), true
		case min < 10:
			return append(append(out, "oh"), cardinal(min)...), true
		}
		return append(out, cardinal(min)...), true
	}
	if m := versionRe.FindStringSubmatch(s); m != nil {
		var out []string
		if m[1] != "" {
			out = append(out, "v")
		}
		for i, part := range strings.Split(m[2], ".") {
			if i > 0 {
				out = append(out, "point")
			}
			out = append(out, inner(part)...)
		}
		return out, true
	}
	if m := rangeRe.FindStringSubmatch(s); m != nil {
		a, _ := spokenNumber(m[1])
		b, _ := spokenNumber(m[2])
		return append(append(a, "to"), b...), true
	}
	if m := withUnitRe.FindStringSubmatch(s); m != nil {
		if u, ok := units[strings.ToLower(m[2])]; ok {
			n, _ := spokenNumber(m[1])
			return append(n, unitWords(u, m[1])...), true
		}
	}
	return nil, false
}
