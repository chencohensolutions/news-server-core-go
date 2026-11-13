package summary

import (
	"regexp"
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
	// minSaid is the fewest content words of a sentence that can give another number for an old sentence's fact
	// (contradicted).
	minSaid = 2
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

// sameWord says two words are one: numbers only when they are the same, a count's word only when the other counts
// to the same ("שלוש" and "שלושה", not "שלושים", which the word likeness would take for it), other words as alike
// has it.
func sameWord(a, b string) bool {
	if isNumber(a) || isNumber(b) {
		return a == b
	}
	if x, y := cardinal(a), cardinal(b); x > 0 || y > 0 {
		return x == y
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
// (restatedAt), and the rest are words the story has elsewhere. A number that sentence lacks, in digits or in a
// word, or a word the story lacks (a name, mostly), makes it news: "three were hurt" after "two were hurt" is not a
// restatement.
func (x *wordIndex) restates(text string) bool {
	_, ok := x.restated(text)
	return ok
}

// restated is restates with the sentence text says again: the first of them, from 0; -1 for a text without words.
func (x *wordIndex) restated(text string) (sentence int, ok bool) { return x.restatedTo(text, x.has) }

// restatedTo is restated for a reader who has more than x's text: known says a word is one they have (x's, and
// those of a story x's sentences are added to).
func (x *wordIndex) restatedTo(text string, known func(word string) bool) (sentence int, ok bool) {
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
			if !ok && (isNumber(now[i]) || numberWord(now[i]) || !known(now[i])) {
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

// moreWords are the words that make a sentence with another number than an old one's another fact, and not
// another value for that one's: more of the same ("4 more roads were closed").
var moreWords = map[string]bool{"עוד": true, "נוסף": true, "נוספת": true, "נוספים": true, "נוספות": true}

// said are text's content words: what it tells a number of, without its numbers, in digits and in words, and
// without the words that tell nothing (functionWords). more says one of its words is of moreWords, alone or after
// joined letters.
func said(text string) (out []string, more bool) {
	for _, word := range words(text) {
		for _, stem := range stems(word) {
			more = more || moreWords[stem]
		}
		if content(word) {
			out = append(out, word)
		}
	}
	return out, more
}

// numberJoined is a number in digits as a word's key has it after joined letters: "ב3" of "ב-3".
var numberJoined = regexp.MustCompile(`^[` + prefixes + `]{1,2}(\d+)$`)

// numbered is text's words with each count as its number in digits, to compare two sentences' counts by what they
// count to: a count in a word of its own (counts: "שלושה", "ושלושת") and a number after joined letters ("ב-3") are
// "3".
func numbered(text string) string {
	ts := keys(tokens(text))
	for _, c := range counts(text) {
		ts[c.at] = c.figure().text
	}
	for i, word := range ts {
		if m := numberJoined.FindStringSubmatch(word); m != nil {
			ts[i] = m[1]
		}
	}
	return strings.Join(ts, " ")
}

// contradicted is the sentence of story (the one x is the index of), from 1, that text says again with another
// number: "3 were arrested" beside "2 were arrested". 0 when there is none. It is the same sentence but for the
// number: that sentence has every content word of text (said: the numbers and the words that tell nothing aside;
// minSaid of them at least), and text has most of that sentence's (restatedAt); each gives a number the other
// doesn't state (differ: in digits or in a count's word, "שלושה" beside "שני" or "2"), neither a date or a
// year the other lacks (sameTime), and text names no day that sentence doesn't (anotherDay: "ביום שלישי" beside
// "ביום שני" is another day's number, and the words of a day of the week are no content words). Narrow on purpose,
// as text then takes that sentence's place: one content word more in text (another place, "killed" for "hurt")
// makes it another fact, and so does a word of moreWords ("4 more"). Of several, the first.
func (x *wordIndex) contradicted(text string, story []Sentence) int {
	return x.contradicting(text, story, false)
}

// contradicting is contradicted; anyDay leaves out that text names no other day, to tell that this alone is what
// kept a sentence from being one.
func (x *wordIndex) contradicting(text string, story []Sentence, anyDay bool) int {
	now, more := said(text)
	if len(now) < minSaid || more {
		return 0
	}
	mine := newWordIndex()
	mine.add(text)
	covered := x.covered(now)
sentences:
	for at, s := range story {
		has := covered[at]
		if has == nil {
			continue
		}
		for _, ok := range has {
			if !ok {
				continue sentences
			}
		}
		if !differ(text, s.Text) || !sameTime(text, s.Text) || !anyDay && anotherDay(text, s.Text) {
			continue
		}
		old, _ := said(s.Text)
		shared := 0
		for _, word := range old {
			if mine.has(word) {
				shared++
			}
		}
		if float64(shared) >= restatedAt*float64(len(old)) {
			return at + 1
		}
	}
	return 0
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

// The headline check's thresholds: conservative, as each story it flags is a whole writing's request.
const (
	// headlineLetters is the fewest letters of a word that can be what a headline names: shorter ones are joined
	// letters and small words more often than not.
	headlineLetters = 4
	// headlineWords is how many such words the story must lack. One is what any rewording has (Hebrew inflects,
	// and the word likeness knows only joined letters and endings); a number, or a word of an outcome or a
	// decision, is enough alone.
	headlineWords = 2
)

// commonWords are words a headline has whatever it tells: who said and how it is presented. With the function
// words, the time words, the months and the words of "more", they are never what a headline names.
var commonWords = func() map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.Fields(`
		אמר אמרה אמרו מסר מסרה מסרו נמסר הודיע הודיעה הודיעו ציין ציינה טען טענה טוען טוענים הוסיף הוסיפה
		דיווח דיווחה דווח דיווחים לדברי לדבריו לדבריה לטענת לטענתו בעקבות במהלך לאחר אחרי לפני אחר אתמול מחר
		השבוע החודש השנה עכשיו צפו תיעוד וידאו סרטון תמונות תמונה בלעדי עדכון עדכונים פרשנות דעה ראיון פרסום
		חדש חדשה חדשים חדשות גדול גדולה קטן קטנה רבים רבות כולם אנשים דברים הכל למה איך מדוע האם כך ככה
		אחרים אחרת מאוד יותר פחות ביותר שוב עדיין כמעט בערך לגבי בנוגע מול נגד בעד אצל דרך תחת מעל בלי ללא`) {
		out[word] = true
	}
	return out
}()

// commonWord says word is never what a headline names: a function word, a time word, a month, a word of "more"
// or one of commonWords, alone or after joined letters.
func commonWord(word string) bool {
	if functionWord(word) || isMonth(word) {
		return true
	}
	for _, stem := range stems(word) {
		if commonWords[stem] || timeWords[stem] || moreWords[stem] {
			return true
		}
	}
	return false
}

// headlineNews is what a headline (an article's title and subtitle) names that the story lacks, by its words
// alone; "" when nothing. index and told are the words and the numbers of the story and of the articles it has. It
// is one of: a number that is no date, year, hour or age and that they don't state, however written; a word of an
// outcome or a decision (outcomes) they lack; headlineWords content words of headlineLetters letters or more that
// they lack, the common ones (commonWord) aside.
func headlineNews(headline string, index *wordIndex, told *numbers) string {
	for _, f := range figures(headline) {
		if f.measure() && !told.states(f) {
			return "the number " + f.text
		}
	}
	var lacking []string
	for _, word := range words(headline) {
		if !content(word) || commonWord(word) || len([]rune(word)) < headlineLetters || index.has(word) {
			continue
		}
		for _, stem := range stems(word) {
			if outcomes[stem] {
				return word
			}
		}
		lacking = append(lacking, word)
	}
	if len(lacking) >= headlineWords {
		return strings.Join(lacking[:headlineWords], ", ")
	}
	return ""
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
