package summary

import (
	"strings"
	"unicode"
)

// What a sentence adds to a text, by its words alone: the incremental update's checks on what Gemini answers. No
// model: the same word likeness as the versions' changes (changes.go), with numbers compared exactly.

// Thresholds of the checks.
const (
	// restatedAt is the share of a sentence's words one earlier sentence has from which it says that sentence
	// again, unless one of its other words is a number that sentence lacks or a word the story lacks.
	restatedAt = 0.75
	// A new article is clearly news to a story when at least novelSentences of its sentences, and novelShare of them,
	// have less than novelAt of their words in any one sentence the story or its earlier articles have, and it
	// has novelNumbers numbers none of them has.
	novelAt        = 0.5
	novelSentences = 5
	novelShare     = 0.5
	novelNumbers   = 2
	// minWords is the fewest words of a sentence that is checked for news: a shorter one is a caption or a credit.
	minWords = 5
)

// wordIndex is the words of a text by sentence, to find the sentence that has the most of another's words.
type wordIndex struct {
	sentences int
	// buckets are the words by what two alike words share: the first three letters of a stem, or the whole of a
	// short word or a number.
	buckets map[string][]indexedWord
}

type indexedWord struct {
	word     string
	sentence int
}

func newWordIndex() *wordIndex { return &wordIndex{buckets: map[string][]indexedWord{}} }

// words are text's words as compared, each once.
func words(text string) []string { return distinct(keys(tokens(text))) }

// isNumber says a word holds a digit: a number, a date, a sum.
func isNumber(word string) bool { return strings.IndexFunc(word, unicode.IsDigit) >= 0 }

// sameWord says two words are one: numbers only when they are the same, other words as alike has it.
func sameWord(a, b string) bool {
	if isNumber(a) || isNumber(b) {
		return a == b
	}
	return alike(a, b)
}

// bucketsOf are the buckets a word is found in.
func bucketsOf(word string) []string {
	if isNumber(word) {
		return []string{"#" + word}
	}
	var out []string
	for _, stem := range stems(word) {
		r := []rune(stem)
		if len(r) > 3 {
			r = r[:3]
		}
		if key := string(r); !contains(out, key) {
			out = append(out, key)
		}
	}
	return out
}

// add adds one sentence.
func (x *wordIndex) add(text string) {
	for _, word := range words(text) {
		for _, bucket := range bucketsOf(word) {
			x.buckets[bucket] = append(x.buckets[bucket], indexedWord{word, x.sentences})
		}
	}
	x.sentences++
}

// addText adds a text's sentences.
func (x *wordIndex) addText(text string) {
	for _, sentence := range splitSentences(text) {
		x.add(sentence)
	}
}

// has says the text has word.
func (x *wordIndex) has(word string) bool {
	for _, bucket := range bucketsOf(word) {
		for _, w := range x.buckets[bucket] {
			if sameWord(word, w.word) {
				return true
			}
		}
	}
	return false
}

// covered are, for each sentence of the text that has any of words, which of them it has.
func (x *wordIndex) covered(words []string) map[int][]bool {
	out := map[int][]bool{}
	for i, word := range words {
		for _, bucket := range bucketsOf(word) {
			for _, w := range x.buckets[bucket] {
				if sameWord(word, w.word) {
					if out[w.sentence] == nil {
						out[w.sentence] = make([]bool, len(words))
					}
					out[w.sentence][i] = true
				}
			}
		}
	}
	return out
}

// coverage is the largest share of words that one sentence of the text has.
func (x *wordIndex) coverage(words []string) float64 {
	if len(words) == 0 {
		return 1
	}
	best := 0
	for _, has := range x.covered(words) {
		n := 0
		for _, ok := range has {
			if ok {
				n++
			}
		}
		best = max(best, n)
	}
	return float64(best) / float64(len(words))
}

// indexOf is the index of a story's sentences.
func indexOf(sentences []Sentence) *wordIndex {
	x := newWordIndex()
	for _, s := range sentences {
		x.add(s.Text)
	}
	return x
}

// restates says text only says again what a sentence of the story does: that sentence has most of its words
// (restatedAt), and the rest are words the story has elsewhere. A number that sentence lacks, or a word the story
// lacks (a name, mostly), makes it news: "three were hurt" after "two were hurt" is not a restatement.
func (x *wordIndex) restates(text string) bool {
	_, ok := x.restated(text)
	return ok
}

// restated is restates with the sentence text says again: the first of them, from 0; -1 for a text without words.
func (x *wordIndex) restated(text string) (sentence int, ok bool) {
	now := words(text)
	if len(now) == 0 {
		return -1, true
	}
	sentence = -1
	for at, has := range x.covered(now) {
		n := 0
		for _, ok := range has {
			if ok {
				n++
			}
		}
		if float64(n) < restatedAt*float64(len(now)) {
			continue
		}
		news := false
		for i, ok := range has {
			if !ok && (isNumber(now[i]) || !x.has(now[i])) {
				news = true
				break
			}
		}
		if !news && (sentence < 0 || at < sentence) {
			sentence = at
		}
	}
	return sentence, sentence >= 0
}

// novelty is how much of an article's text (as the prompt showed it) a text lacks: how many of its sentences of
// minWords words or more it has, how many of those have less than novelAt of their words in any one sentence of the
// text, and how many of its numbers the text lacks.
func (x *wordIndex) novelty(article string) (sentences, novel, numbers int) {
	seen := map[string]bool{}
	for _, sentence := range splitSentences(article) {
		now := words(sentence)
		for _, word := range now {
			if isNumber(word) && !seen[word] {
				seen[word] = true
				if !x.has(word) {
					numbers++
				}
			}
		}
		if len(now) < minWords {
			continue
		}
		sentences++
		if x.coverage(now) < novelAt {
			novel++
		}
	}
	return sentences, novel, numbers
}

// clearlyNovel says an article with this novelty is news to the text by its words alone: Gemini finding nothing in
// it is to be doubted.
func clearlyNovel(sentences, novel, numbers int) bool {
	return novel >= novelSentences && float64(novel) >= novelShare*float64(sentences) && numbers >= novelNumbers
}

// splitSentences is text's sentences: cut after a period, a question or exclamation mark, or a line's end. A period
// between digits (1.5) or inside a word cuts nothing.
func splitSentences(text string) []string {
	var out []string
	runes := []rune(text)
	start := 0
	flush := func(end int) {
		if s := strings.TrimSpace(string(runes[start:end])); s != "" {
			out = append(out, s)
		}
		start = end
	}
	for i, r := range runes {
		switch {
		case r == '\n':
			flush(i + 1)
		case (r == '.' || r == '!' || r == '?' || r == '…') && (i+1 == len(runes) || unicode.IsSpace(runes[i+1])):
			flush(i + 1)
		}
	}
	flush(len(runes))
	return out
}
