package main

import (
	"strings"
	"unicode"
)

// Where a sentence of the reply ends, for cutting it into spoken pieces: the first sentence
// is spoken on its own, so the voice starts fast, and later pieces end at the last finished
// sentence (see "Speaking a reply" in docs/design.md). No sentence tokenizer is needed: the
// voice's replies are plain spoken English, so punctuation, abbreviations and numbers are
// enough.

// abbreviations end in a period without ending a sentence.
var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true, "sr": true, "jr": true, "st": true,
	"vs": true, "etc": true, "e.g": true, "i.e": true, "approx": true, "no": true, "vol": true,
	"a.m": true, "p.m": true, "fig": true, "inc": true, "ltd": true, "co": true, "dept": true,
}

// sentenceStart is where the sentence that text stops in began: after its last finished one.
func sentenceStart(text string) int {
	start := 0
	for {
		n := endOfSentence(text[start:])
		if n == 0 {
			return start
		}
		start += n
		for start < len(text) && text[start] == ' ' {
			start++
		}
	}
}

// endOfSentence is the length of text up to the end of its first finished sentence, closing
// quotes and brackets included; 0 if it has none yet.
func endOfSentence(text string) int {
	r := []rune(text)
	for i := 0; i < len(r); i++ {
		if !strings.ContainsRune(".!?…", r[i]) {
			continue
		}
		j := i + 1
		for j < len(r) && strings.ContainsRune(".!?…", r[j]) {
			j++ // "?!" and "..."
		}
		for j < len(r) && strings.ContainsRune(`"')]”’`, r[j]) {
			j++
		}
		if j < len(r) && !unicode.IsSpace(r[j]) {
			continue // inside a word or a number: "3.14", "v1.2", "e.g."
		}
		if r[i] == '.' && !strings.Contains(string(r[i:j]), "..") && notAnEnd(r[:i], j == len(r)) {
			continue
		}
		return len(string(r[:j]))
	}
	return 0
}

// notAnEnd says whether a period after before doesn't end a sentence: after an abbreviation,
// an initial, or, at the end of the text so far, a number that may go on ("version 3.").
func notAnEnd(before []rune, atEnd bool) bool {
	k := len(before)
	for k > 0 && !unicode.IsSpace(before[k-1]) && !strings.ContainsRune(`"'([“‘`, before[k-1]) {
		k--
	}
	word := strings.ToLower(string(before[k:]))
	if abbreviations[word] {
		return true
	}
	if len([]rune(word)) == 1 && unicode.IsUpper(before[k]) {
		return true // an initial: "J. Smith"
	}
	return atEnd && len(word) > 0 && unicode.IsDigit(before[len(before)-1])
}

// finishedSentences is the length of text up to the end of its last finished sentence, or 0.
func finishedSentences(text string) int {
	end := 0
	for {
		n := endOfSentence(text[end:])
		if n == 0 {
			return end
		}
		end += n
	}
}
