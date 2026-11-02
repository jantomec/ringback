package speaking

import "strings"

// Words the lexicon has only in another form, by misaki's rules: "milliseconds" from
// "millisecond", "refactored" from "refactor", "rebasing" from "rebase".

const usTaus = "AIOWYiuæɑəɛɪɹʊʌ" // sounds after which a t becomes a flap

// doubled: "running", "kicking", whose stem drops a letter.
func doubled(w string) bool {
	if strings.HasSuffix(w, "cking") {
		return true
	}
	n := len(w)
	return n >= 5 && strings.HasSuffix(w, "ing") && w[n-4] == w[n-5] && strings.IndexByte("bcdgklmnprstvxz", w[n-4]) >= 0
}

func (g *G2P) known(w string) bool { _, ok := g.lex[w]; return ok }

// stem returns the phonemes of a word whose stem the lexicon has, or "".
func (g *G2P) stem(w string) string {
	n := len(w)
	switch {
	case n >= 3 && strings.HasSuffix(w, "s"):
		switch {
		case !strings.HasSuffix(w, "ss") && g.known(w[:n-1]):
			return pluralPhonemes(g.lex[w[:n-1]])
		case (strings.HasSuffix(w, "'s") || (n > 4 && strings.HasSuffix(w, "es") && !strings.HasSuffix(w, "ies"))) && g.known(w[:n-2]):
			return pluralPhonemes(g.lex[w[:n-2]])
		case n > 4 && strings.HasSuffix(w, "ies") && g.known(w[:n-3]+"y"):
			return pluralPhonemes(g.lex[w[:n-3]+"y"])
		}
	case n >= 4 && strings.HasSuffix(w, "d"):
		switch {
		case !strings.HasSuffix(w, "dd") && g.known(w[:n-1]):
			return pastPhonemes(g.lex[w[:n-1]])
		case n > 4 && strings.HasSuffix(w, "ed") && !strings.HasSuffix(w, "eed") && g.known(w[:n-2]):
			return pastPhonemes(g.lex[w[:n-2]])
		}
	case n >= 5 && strings.HasSuffix(w, "ing"):
		switch {
		case n > 5 && g.known(w[:n-3]):
			return ingPhonemes(g.lex[w[:n-3]])
		case g.known(w[:n-3] + "e"):
			return ingPhonemes(g.lex[w[:n-3]+"e"])
		case n > 5 && doubled(w) && g.known(w[:n-4]):
			return ingPhonemes(g.lex[w[:n-4]])
		}
	}
	return ""
}

func lastTwo(ps string) (rune, rune) {
	r := []rune(ps)
	switch len(r) {
	case 0:
		return 0, 0
	case 1:
		return 0, r[0]
	}
	return r[len(r)-2], r[len(r)-1]
}

func pluralPhonemes(stem string) string {
	_, last := lastTwo(stem)
	switch {
	case strings.ContainsRune("ptkfθ", last):
		return stem + "s"
	case strings.ContainsRune("szʃʒʧʤ", last):
		return stem + "ᵻz"
	}
	return stem + "z"
}

func pastPhonemes(stem string) string {
	before, last := lastTwo(stem)
	switch {
	case strings.ContainsRune("pkfθʃsʧ", last):
		return stem + "t"
	case last == 'd':
		return stem + "ᵻd"
	case last != 't':
		return stem + "d"
	case before == 0:
		return stem + "ɪd"
	case strings.ContainsRune(usTaus, before):
		return strings.TrimSuffix(stem, "t") + "ɾᵻd"
	}
	return stem + "ᵻd"
}

func ingPhonemes(stem string) string {
	before, last := lastTwo(stem)
	if before != 0 && last == 't' && strings.ContainsRune(usTaus, before) {
		return strings.TrimSuffix(stem, "t") + "ɾɪŋ"
	}
	return stem + "ɪŋ"
}
