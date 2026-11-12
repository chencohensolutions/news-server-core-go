package summary

import (
	"strings"
	"time"
	"unicode"
)

// Change kinds of a sentence against the version before it.
const (
	Same    = "same"    // the version before has it, word for word
	Changed = "changed" // the version before has it reworded, or with words added
	New     = "new"     // the version before has nothing like it
)

// Thresholds of sentence likeness (likeness: the share of words alike).
const (
	rewordedAt = 0.4 // a sentence at least this like one before it is that sentence changed, not a new one
	similarAt  = 0.5 // a sentence at least this like one of another story's says the same
	sameStory  = 0.6 // a new story with this share of its sentences in a recent story is that story's next version
)

// Changes are what a version of a story changed from the version before it.
type Changes struct {
	// Since is when the version before was written.
	Since time.Time
	// Title is the version before's title.
	Title string
	// Sentences are one for each of the version's sentences, in order.
	Sentences []SentenceChange
	// Removed are the version before's sentences this one has nothing like.
	Removed []Sentence
}

// SentenceChange is how a sentence differs from the closest one in the version before.
type SentenceChange struct {
	Kind string // Same, Changed or New
	// Parts are the sentence's text in order, together the whole of it: each new, or kept from the closest sentence
	// before. A Same sentence is one kept part, a New one one new part.
	Parts []Part
}

// Part is a run of a sentence's text, new in its version or not.
type Part struct {
	Text  string
	Added bool
}

// ChangesFrom are what cur changed from prev, the version before it.
func ChangesFrom(prev, cur StorySummary) Changes {
	out := Changes{Since: prev.WrittenAt, Title: prev.Title, Sentences: make([]SentenceChange, len(cur.Sentences))}
	before := make([][]token, len(prev.Sentences))
	for i, s := range prev.Sentences {
		before[i] = tokens(s.Text)
	}
	matched := make([]bool, len(prev.Sentences))
	for i, s := range cur.Sentences {
		now := tokens(s.Text)
		best, bestAt := -1, 0.0
		for j, b := range before {
			if like := likeness(now, b); like > bestAt {
				best, bestAt = j, like
			}
		}
		switch {
		case best >= 0 && sameWords(now, before[best]):
			matched[best] = true
			out.Sentences[i] = SentenceChange{Kind: Same, Parts: []Part{{Text: s.Text}}}
		case best >= 0 && bestAt >= rewordedAt:
			matched[best] = true
			out.Sentences[i] = SentenceChange{Kind: Changed, Parts: diff(before[best], now)}
		default:
			out.Sentences[i] = SentenceChange{Kind: New, Parts: []Part{{Text: s.Text, Added: true}}}
		}
	}
	for j, s := range prev.Sentences {
		if matched[j] {
			continue
		}
		gone := true
		for _, c := range cur.Sentences {
			if likeness(before[j], tokens(c.Text)) >= rewordedAt {
				gone = false
				break
			}
		}
		if gone {
			out.Removed = append(out.Removed, s)
		}
	}
	return out
}

// Similar says a new story's sentences say what old's do: most of them have a sentence like them in old.
func Similar(story, old []Sentence) bool {
	if len(story) == 0 || len(old) == 0 {
		return false
	}
	before := make([][]token, len(old))
	for i, s := range old {
		before[i] = tokens(s.Text)
	}
	alike := 0
	for _, s := range story {
		now := tokens(s.Text)
		for _, b := range before {
			if likeness(now, b) >= similarAt {
				alike++
				break
			}
		}
	}
	return float64(alike) >= sameStory*float64(len(story))
}

// token is a word and the spaces after it; key is the word as compared: letters and digits, lower case ("" for
// punctuation alone).
type token struct{ text, key string }

func tokens(text string) []token {
	var out []token
	runes := []rune(text)
	for i := 0; i < len(runes); {
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) {
			i++
		}
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
		word := string(runes[start:i])
		key := strings.ToLower(strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return r
			}
			return -1
		}, word))
		out = append(out, token{text: word, key: key})
	}
	return out
}

func sameWords(a, b []token) bool {
	ka, kb := keys(a), keys(b)
	if len(ka) != len(kb) {
		return false
	}
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

func keys(ts []token) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		if t.key != "" {
			out = append(out, t.key)
		}
	}
	return out
}

// likeness is how alike two sentences' words are: the share of the words of both that the other has (alike), 0 to 1.
func likeness(a, b []token) float64 {
	ka, kb := distinct(keys(a)), distinct(keys(b))
	if len(ka)+len(kb) == 0 {
		return 0
	}
	shared := 0
	for _, pair := range [][2][]string{{ka, kb}, {kb, ka}} {
		for _, x := range pair[0] {
			for _, y := range pair[1] {
				if alike(x, y) {
					shared++
					break
				}
			}
		}
	}
	return float64(shared) / float64(len(ka)+len(kb))
}

func distinct(words []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range words {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// prefixes are the letters Hebrew joins to the front of a word (and, the, in, to, from, that, as).
const prefixes = "והבלמשכ"

// alike says two words are one word, maybe inflected: the same, or, with up to two joined letters off the front, one
// starts the other with at most three letters more (כביש, כבישים, והכבישים).
func alike(a, b string) bool {
	if a == b {
		return true
	}
	for _, x := range stems(a) {
		for _, y := range stems(b) {
			short, long := []rune(x), []rune(y)
			if len(short) > len(long) {
				short, long = long, short
			}
			if len(short) >= 3 && len(long)-len(short) <= 3 && string(long[:len(short)]) == string(short) {
				return true
			}
		}
	}
	return false
}

// stems are w and w without one or two joined letters in front, while three letters are left.
func stems(w string) []string {
	out := []string{w}
	r := []rune(w)
	for i := 0; i < 2 && len(r) > 3 && strings.ContainsRune(prefixes, r[0]); i++ {
		r = r[1:]
		out = append(out, string(r))
	}
	return out
}

// diff is now's text in parts, the words not in the longest common run of words with before marked added.
func diff(before, now []token) []Part {
	// Longest common subsequence of the words' keys.
	n, m := len(now), len(before)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case now[i].key != "" && before[j].key != "" && alike(now[i].key, before[j].key):
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	added := make([]bool, n)
	for i, j := 0, 0; i < n; {
		switch {
		case j < m && now[i].key != "" && before[j].key != "" && alike(now[i].key, before[j].key) && lcs[i][j] == lcs[i+1][j+1]+1:
			i, j = i+1, j+1
		case j < m && lcs[i][j+1] > lcs[i+1][j]:
			j++
		default:
			added[i] = now[i].key != ""
			i++
		}
	}
	// Punctuation alone goes with the word before it.
	for i := 1; i < n; i++ {
		if now[i].key == "" {
			added[i] = added[i-1]
		}
	}
	var parts []Part
	push := func(text string, isNew bool) {
		if text == "" {
			return
		}
		if k := len(parts) - 1; k >= 0 && parts[k].Added == isNew {
			parts[k].Text += text
			return
		}
		parts = append(parts, Part{Text: text, Added: isNew})
	}
	for i, t := range now {
		if !added[i] {
			push(t.text, false)
			continue
		}
		// The spaces after a new word that ends a new run aren't new.
		word := strings.TrimRightFunc(t.text, unicode.IsSpace)
		if i+1 < n && added[i+1] {
			word = t.text
		}
		push(word, true)
		push(t.text[len(word):], false)
	}
	return parts
}
