package summary

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// The numbers, names and time words of an answered sentence: the incremental update's checks of a sentence against
// the articles it cites and the story it joins (round 4 of the replay, docs/incremental.md). No model, and each
// errs to the side that loses no news: a number is taken as stated when in doubt, and a name is one only with a
// sign that it is.

// figure is a number as a text writes it.
type figure struct {
	// text is the number as compared: without thousands separators ("1200" of "1,200"), a decimal without the
	// zeros that end it, anything else as written ("19:30", "14.3.2021").
	text string
	// date says it is a date or a year: "14.3.2021", "12/11", "2023", and the day before a month's name ("12
	// בנובמבר").
	date bool
	// scale is what the word after it multiplies it by: 1000 for "אלף", and so on; 0 for none.
	scale int64
	// apart says it is an hour or an age: "19:30", "בשעה 7", "בן 34", "(34)". With a date or a year, what is no
	// count or measure of anything (measure).
	apart bool
	// of is the word it counts or measures: the one after it, and after its scale ("3 חשודים", "2 מיליון שקל").
	of string
}

// measure says f is a count, a sum or a measure: no date or year, no hour and no age.
func (f figure) measure() bool { return !f.date && !f.apart }

var (
	numberRun = regexp.MustCompile(`\d+(?:[.,:/]\d+)*`)
	thousands = regexp.MustCompile(`^\d{1,3}(?:,\d{3})+(?:\.\d+)?$`)
	decimal   = regexp.MustCompile(`^\d+\.\d+$`)
	fullDate  = regexp.MustCompile(`^\d{1,2}[./]\d{1,2}[./](?:\d{2}|\d{4})$`)
	slashDate = regexp.MustCompile(`^\d{1,2}/\d{1,2}$`)
	yearAlone = regexp.MustCompile(`^(?:19|20)\d\d$`)
	dayAlone  = regexp.MustCompile(`^\d{1,2}$`)
	digitRun  = regexp.MustCompile(`\d+`)
)

// months are the months' names, as a date writes them after its day.
var months = map[string]bool{
	"ינואר": true, "פברואר": true, "מרץ": true, "מרס": true, "אפריל": true, "מאי": true, "יוני": true, "יולי": true,
	"אוגוסט": true, "ספטמבר": true, "אוקטובר": true, "נובמבר": true, "דצמבר": true,
}

// scales are the words that multiply the number before them.
var scales = map[string]int64{
	"אלף": 1000, "אלפים": 1000, "מיליון": 1_000_000, "מיליוני": 1_000_000, "מיליארד": 1_000_000_000, "מיליארדי": 1_000_000_000,
}

// plainNumber is a number as compared.
func plainNumber(raw string) string {
	if thousands.MatchString(raw) {
		raw = strings.ReplaceAll(raw, ",", "")
	}
	if decimal.MatchString(raw) {
		raw = strings.TrimSuffix(strings.TrimRight(raw, "0"), ".")
	}
	return raw
}

// wordAfter is the first word of rest, as compared: the word that follows a number ("" when a clause ends first).
// second is the word after that one, "" when the clause ends between them.
func wordAfter(rest string) (first, second string) {
	// The next words are near: no need to split a whole article after each of its numbers.
	if len(rest) > 80 {
		rest = strings.ToValidUTF8(rest[:80], "")
	}
	fields := strings.Fields(rest)
	for i, field := range fields {
		if i == 2 {
			break
		}
		if ts := tokens(field); len(ts) > 0 && ts[0].key != "" {
			if i+1 < len(fields) && !strings.ContainsAny(field, ".,;:!?") {
				if next := tokens(fields[i+1]); len(next) > 0 {
					second = next[0].key
				}
			}
			return ts[0].key, second
		}
		if strings.ContainsAny(field, ".,;:!?") {
			break
		}
	}
	return "", ""
}

// wordBefore is the last word of before, as compared: the word a number follows, without the letters joined to
// the number itself ("בן" of "בן 34", "שעה" of "בשעה 7" and of "בשעה ב-7").
func wordBefore(before string) string {
	if len(before) > 80 {
		before = strings.ToValidUTF8(before[len(before)-80:], "")
	}
	fields := strings.Fields(before)
	if r := []rune(before); len(r) > 0 && !unicode.IsSpace(r[len(r)-1]) && len(fields) > 0 {
		fields = fields[:len(fields)-1] // what is joined to the number: "ב-", "(", "כ-"
	}
	if len(fields) == 0 {
		return ""
	}
	if ts := tokens(fields[len(fields)-1]); len(ts) > 0 {
		return ts[0].key
	}
	return ""
}

// ageWords are the words an age follows.
var ageWords = map[string]bool{"בן": true, "בת": true, "כבן": true, "כבת": true, "בני": true, "בנות": true, "גיל": true, "בגיל": true, "גילאי": true, "בגילאי": true}

// hourOrAge says a number written raw, after the text before and with rest after it, is an hour or an age.
func hourOrAge(raw, before, rest string) bool {
	if strings.Contains(raw, ":") {
		return true
	}
	if strings.HasSuffix(before, "(") && strings.HasPrefix(rest, ")") {
		return true // "(34)"
	}
	word := wordBefore(before)
	if ageWords[word] {
		return true
	}
	for _, stem := range stems(word) {
		if stem == "שעה" {
			return true
		}
	}
	return false
}

// about says a number follows "כ-" ("בכ-", "לכ-"): about so many, which no year is.
func about(before string) bool {
	before = strings.TrimRight(before, "-־–")
	if !strings.HasSuffix(before, "כ") {
		return false
	}
	rest := []rune(strings.TrimSuffix(before, "כ"))
	if len(rest) > 0 && strings.ContainsRune(prefixes, rest[len(rest)-1]) {
		rest = rest[:len(rest)-1]
	}
	return len(rest) == 0 || !unicode.IsLetter(rest[len(rest)-1])
}

// isMonth says word is a month's name, alone or after a joined letter ("בנובמבר").
func isMonth(word string) bool {
	if months[word] {
		return true
	}
	r := []rune(word)
	return len(r) > 1 && strings.ContainsRune("בלומ", r[0]) && months[string(r[1:])]
}

// figures are text's numbers, in order.
func figures(text string) []figure {
	var out []figure
	for _, at := range numberRun.FindAllStringIndex(text, -1) {
		raw := text[at[0]:at[1]]
		next, second := wordAfter(text[at[1]:])
		f := figure{text: plainNumber(raw), scale: scales[next], of: next, apart: hourOrAge(raw, text[:at[0]], text[at[1]:])}
		if f.scale > 0 {
			f.of = second
		}
		switch {
		case fullDate.MatchString(raw), slashDate.MatchString(raw):
			f.date = true
		case yearAlone.MatchString(raw):
			f.date = !about(text[:at[0]])
		case dayAlone.MatchString(raw):
			f.date = isMonth(next)
		}
		out = append(out, f)
	}
	return out
}

// numbers are the numbers a text states: to tell whether it states another text's.
type numbers struct {
	whole map[string]bool // each number, as compared
	parts map[string]bool // the runs of digits of a time, a date or a score: "19" and "30" of "19:30"
	words map[string]bool // the text's words, for a number written in words
}

func newNumbers() *numbers {
	return &numbers{whole: map[string]bool{}, parts: map[string]bool{}, words: map[string]bool{}}
}

// add adds a text.
func (n *numbers) add(text string) {
	for _, f := range figures(text) {
		n.whole[f.text] = true
		if decimal.MatchString(f.text) {
			continue // one number: "1.5" states no 5
		}
		for _, part := range digitRun.FindAllString(f.text, -1) {
			n.parts[part] = true
		}
	}
	for _, word := range words(text) {
		n.words[word] = true
	}
}

// has says the text has f, written the same.
func (n *numbers) has(f figure) bool { return n.whole[f.text] }

// word says the text has one of words, alone or after a joined letter or two ("ושלושה").
func (n *numbers) word(words ...string) bool {
	for _, word := range words {
		if n.words[word] {
			return true
		}
		for _, p := range prefixes {
			if n.words[string(p)+word] {
				return true
			}
			for _, q := range prefixes {
				if n.words[string(p)+string(q)+word] {
					return true
				}
			}
		}
	}
	return false
}

// The numbers' words. A number is taken as written in words when each of its words is there, in any order and
// whatever else they mean ("שני" is Monday too): the check they serve must not drop a true sentence.
var (
	unitWords = map[int64][]string{
		1: {"אחד", "אחת", "ראשון", "ראשונה"}, 2: {"שניים", "שתיים", "שנים", "שתים", "שני", "שתי", "שנייה", "שניה"},
		3: {"שלושה", "שלוש", "שלושת", "שלישי", "שלישית"}, 4: {"ארבעה", "ארבע", "ארבעת", "רביעי", "רביעית"},
		5: {"חמישה", "חמש", "חמשת", "חמישת", "חמישי", "חמישית"}, 6: {"שישה", "שש", "ששת", "שישת", "שישי", "שישית"},
		7: {"שבעה", "שבע", "שבעת", "שביעי", "שביעית"}, 8: {"שמונה", "שמונת", "שמיני", "שמינית"},
		9: {"תשעה", "תשע", "תשעת", "תשיעי", "תשיעית"}, 10: {"עשרה", "עשר", "עשרת", "עשירי", "עשירית"},
	}
	tenWords = map[int64][]string{
		20: {"עשרים"}, 30: {"שלושים"}, 40: {"ארבעים"}, 50: {"חמישים"}, 60: {"שישים", "ששים"}, 70: {"שבעים"}, 80: {"שמונים"}, 90: {"תשעים"},
	}
)

// fraction says the text writes in words the part after number's point: "חצי" for .5, "רבע" for .25 and .75.
func (n *numbers) fraction(number string) bool {
	switch {
	case strings.HasSuffix(number, ".5"):
		return n.word("חצי")
	case strings.HasSuffix(number, ".25"), strings.HasSuffix(number, ".75"):
		return n.word("רבע", "רבעי")
	}
	return false
}

// written says the text writes v in words: 1 to 99, the hundreds to 900 and the thousands to 10,000; the ordinals
// to the tenth too.
func (n *numbers) written(v int64) bool {
	switch {
	case v >= 1 && v <= 10:
		return n.word(unitWords[v]...)
	case v > 10 && v < 20:
		return n.word(unitWords[v-10]...) && n.word("עשר", "עשרה")
	case v < 100 && v%10 == 0:
		return n.word(tenWords[v]...)
	case v > 20 && v < 100:
		return n.word(tenWords[v-v%10]...) && n.word(unitWords[v%10]...)
	case v == 100:
		return n.word("מאה")
	case v == 200:
		return n.word("מאתיים")
	case v < 1000 && v%100 == 0:
		return n.word(unitWords[v/100]...) && n.word("מאות")
	case v == 1000:
		return n.word("אלף")
	case v == 2000:
		return n.word("אלפיים")
	case v > 2000 && v <= 10_000 && v%1000 == 0:
		return n.word(unitWords[v/1000]...) && n.word("אלפים")
	}
	return false
}

// states says the text states f, however it writes it (stating, with any word a number is written with): the same number; a part of a time, a date or a score of
// the text's ("19" of "19:30"), or such a one whose parts the text has; in words ("שלושה" for 3, "חצי" for a half); or at another scale ("300 אלף" for 300,000,
// "1.5 מיליון" for 1,500,000, and the other way).
func (n *numbers) states(f figure) bool { return n.stating(f, nil) }

// stating is states for a text whose counts in a word are known (counted: what each of counts counts to; nil when
// they are not). A number a count's word writes (2 to 10, the tens) is then stated in a word only by such a count,
// and not by a word that is a number's elsewhere: "ביום שלישי" states no 3, "השופט השני" no 2.
func (n *numbers) stating(f figure, counted map[int64]bool) bool {
	if n.whole[f.text] {
		return true
	}
	parts := digitRun.FindAllString(f.text, -1)
	switch {
	case len(parts) == 1 && parts[0] == f.text:
		if n.parts[f.text] {
			return true
		}
	case decimal.MatchString(f.text):
		// "1.5" is one number: a text with a 1 and a 5 doesn't state it.
	default:
		all := true
		for _, part := range parts {
			all = all && (n.whole[part] || n.parts[part])
		}
		if all {
			return true
		}
	}
	value, err := strconv.ParseFloat(f.text, 64)
	if err != nil {
		return false
	}
	whole := int64(value)
	isWhole := float64(whole) == value
	switch {
	case !isWhole:
	case counted != nil && cardinalValues[whole]:
		if counted[whole] {
			return true
		}
	case n.written(whole):
		return true
	}
	if !isWhole && n.fraction(f.text) {
		return true
	}
	// The sentence's "300 אלף" is the text's 300,000.
	if f.scale > 0 {
		if scaled := value * float64(f.scale); scaled == float64(int64(scaled)) && n.whole[strconv.FormatInt(int64(scaled), 10)] {
			return true
		}
	}
	// The sentence's 300,000 is the text's "300 אלף".
	if isWhole && whole >= 1000 {
		for _, scale := range []struct {
			by    int64
			words []string
		}{{1000, []string{"אלף", "אלפים"}}, {1_000_000, []string{"מיליון", "מיליוני"}}, {1_000_000_000, []string{"מיליארד", "מיליארדי"}}} {
			if !n.word(scale.words...) {
				continue
			}
			// So many of the scale: in digits, in words, or a half and a quarter of it ("חצי מיליון").
			of := float64(whole) / float64(scale.by)
			text := strconv.FormatFloat(of, 'f', -1, 64)
			switch {
			case n.fraction(text):
				return true
			case of < 1:
			case n.whole[text], of == float64(int64(of)) && (int64(of) == 1 || n.written(int64(of))):
				return true
			}
		}
	}
	return false
}

// cardinals are the words a count is written with, by what they count: two to ten and the tens. No ordinal
// ("שלישי" is Tuesday too), no "one", which is seldom a count ("אחד החשודים"), and no "שנים", which is years.
var cardinals = map[string]int64{
	"שניים": 2, "שתיים": 2, "שני": 2, "שתי": 2,
	"שלושה": 3, "שלוש": 3, "שלושת": 3, "ארבעה": 4, "ארבע": 4, "ארבעת": 4, "חמישה": 5, "חמש": 5, "חמשת": 5, "חמישת": 5,
	"שישה": 6, "שש": 6, "ששת": 6, "שישת": 6, "שבעה": 7, "שבע": 7, "שבעת": 7, "שמונה": 8, "שמונת": 8,
	"תשעה": 9, "תשע": 9, "תשעת": 9, "עשרה": 10, "עשר": 10, "עשרת": 10,
	"עשרים": 20, "שלושים": 30, "ארבעים": 40, "חמישים": 50, "שישים": 60, "ששים": 60, "שבעים": 70, "שמונים": 80, "תשעים": 90,
}

// cardinalValues are what the words of cardinals count to.
var cardinalValues = func() map[int64]bool {
	out := map[int64]bool{}
	for _, value := range cardinals {
		out[value] = true
	}
	return out
}()

// cardinal is what word counts to when it is a count's word, alone or after joined letters ("ושלושה"); 0 when it
// is none.
func cardinal(word string) int64 {
	for _, stem := range stems(word) {
		if value := cardinals[stem]; value > 0 {
			return value
		}
	}
	return 0
}

// numberWords are the words a number is written with, the ordinals and the scales too: a count beside one of them
// is part of a longer number ("שלוש עשרה", "עשרים ושלושה", "שלושת אלפים").
var numberWords = func() map[string]bool {
	out := map[string]bool{"מאה": true, "מאות": true, "מאתיים": true, "אלפיים": true, "חצי": true}
	for _, forms := range unitWords {
		for _, word := range forms {
			out[word] = word != "שנים" // years, far more often than two
		}
	}
	for _, forms := range tenWords {
		for _, word := range forms {
			out[word] = true
		}
	}
	for word := range scales {
		out[word] = true
	}
	return out
}()

// numberWord says word is one a number is written with, alone or after joined letters ("ושלושה").
func numberWord(word string) bool {
	for _, stem := range stems(word) {
		if numberWords[stem] {
			return true
		}
	}
	return false
}

// count is a count a text writes in a word: the word as written, what it counts to, and the two words after it:
// what is counted ("שלושה איראנים", "שלושה תיירים סינים"). at is where the word stands among the text's words (the
// keys of its tokens).
type count struct {
	word  string
	value int64
	of    []string
	at    int
}

// figure is c as a number in digits, to ask a text whether it states it.
func (c count) figure() figure { return figure{text: strconv.FormatInt(c.value, 10)} }

// counts are the counts text writes in a word of their own ("שלושה חשודים", "עשרים בתים"), alone or after joined
// letters. A word beside another number's word or a number in digits is part of a longer number and is none
// ("שלוש עשרה", "עשרים ושלושה", "3 אלפים"), and neither is an hour or a day ("בשעה שבע", "ביום שני") or the other
// hand ("מצד שני"): narrow on purpose, as the checks they serve drop or replace a sentence.
func counts(text string) []count {
	var out []count
	ts := keys(tokens(text))
	beside := func(i int) bool { return i >= 0 && i < len(ts) && (isNumber(ts[i]) || numberWord(ts[i])) }
	for i, word := range ts {
		var value int64
		for _, stem := range stems(word) {
			if value = cardinals[stem]; value > 0 {
				// "השני" is the second, not two.
				if value == 2 && strings.Contains(strings.TrimSuffix(word, stem), "ה") {
					value = 0
				}
				break
			}
		}
		if value == 0 || beside(i-1) || beside(i+1) {
			continue
		}
		if i > 0 && (alike(ts[i-1], "שעה") || alike(ts[i-1], "יום") || ts[i-1] == "מצד" || ts[i-1] == "ומצד") {
			continue
		}
		out = append(out, count{word: word, value: value, of: ts[i+1 : min(i+3, len(ts))], at: i})
	}
	return out
}

// nationalities are a country's people, each as the singular its plurals are made from ("איראני": "איראנים",
// "איראניות"). Not the ones whose plural is another word too ("רומנים" are novels).
var nationalities = map[string]bool{
	"ישראלי": true, "פלסטיני": true, "איראני": true, "סורי": true, "לבנוני": true, "מצרי": true, "ירדני": true, "עיראקי": true,
	"סעודי": true, "קטארי": true, "תימני": true, "טורקי": true, "רוסי": true, "אוקראיני": true, "אמריקאי": true, "אמריקני": true,
	"קנדי": true, "בריטי": true, "צרפתי": true, "גרמני": true, "איטלקי": true, "ספרדי": true, "יווני": true, "פולני": true,
	"הולנדי": true, "בלגי": true, "שוודי": true, "סיני": true, "הודי": true, "יפני": true, "קוריאני": true, "תאילנדי": true,
	"פיליפיני": true, "אפגני": true, "פקיסטני": true, "אריתראי": true, "סודני": true, "אתיופי": true, "מרוקאי": true, "ברזילאי": true,
}

// countries are the countries of nationalities, each with its people: "אזרחי איראן" are "איראנים".
var countries = map[string]string{
	"ישראל": "ישראלי", "איראן": "איראני", "סוריה": "סורי", "לבנון": "לבנוני", "ירדן": "ירדני", "עיראק": "עיראקי",
	"סעודיה": "סעודי", "קטאר": "קטארי", "תימן": "תימני", "טורקיה": "טורקי", "רוסיה": "רוסי", "אוקראינה": "אוקראיני",
	"ארהב": "אמריקאי", "קנדה": "קנדי", "בריטניה": "בריטי", "צרפת": "צרפתי", "גרמניה": "גרמני",
	"איטליה": "איטלקי", "ספרד": "ספרדי", "יוון": "יווני", "פולין": "פולני", "הולנד": "הולנדי", "בלגיה": "בלגי",
	"שוודיה": "שוודי", "מצרים": "מצרי", "סין": "סיני", "הודו": "הודי", "יפן": "יפני", "קוריאה": "קוריאני", "תאילנד": "תאילנדי",
	"אפגניסטן": "אפגני", "פקיסטן": "פקיסטני", "אריתריאה": "אריתראי", "סודן": "סודני", "אתיופיה": "אתיופי",
	"מרוקו": "מרוקאי", "ברזיל": "ברזילאי",
}

// national is the people word names, as nationalities has it, and whether in the plural: "איראני" of "איראני" and
// "האיראנית", and in the plural of "איראנים", "איראניים" and "ואיראניות". "" when it names none.
func national(word string) (people string, plural bool) {
	people, plural, _ = nationalJoined(word)
	return people, plural
}

// nationalJoined is national with the letters joined to the people's word: "מה" of "מהאיראנים".
func nationalJoined(word string) (people string, plural bool, joined string) {
	one := func(stem string, endings ...string) string {
		for _, ending := range endings {
			if cut, ok := strings.CutSuffix(stem, ending); ok && nationalities[cut] {
				if cut == "אמריקני" {
					cut = "אמריקאי"
				}
				return cut
			}
		}
		return ""
	}
	for _, stem := range stems(word) {
		if people = one(stem, "ם", "ים", "ות"); people != "" {
			return people, true, strings.TrimSuffix(word, stem)
		}
		if people = one(stem, "", "ת", "ה", "יה"); people != "" {
			return people, false, strings.TrimSuffix(word, stem)
		}
	}
	return "", false, ""
}

// country is the people of the country word names, alone or after joined letters ("מאיראן"); "" when it names none.
func country(word string) string {
	for _, stem := range stems(word) {
		if people := countries[stem]; people != "" {
			return people
		}
	}
	return ""
}

// The words between a count and the people it counts.
var (
	// citizenWords are the words a people's word follows in the singular, of any number of people: "שלושה בעלי
	// אזרחות איראנית", "שלושה צעירים ממוצא סיני".
	citizenWords = map[string]bool{"אזרחות": true, "נתינות": true, "דרכון": true, "דרכונים": true, "מוצא": true}
	// peopleOf are the words a country's name follows as its people: "שלושה אזרחי איראן".
	peopleOf = map[string]bool{
		"אזרחי": true, "אזרחיות": true, "תושבי": true, "תושבות": true, "נתיני": true, "ילידי": true, "יוצאי": true,
	}
	// amongWords end what a count counts: what follows is some of them, or whose they are ("שלושה חשודים, בהם
	// איראנים", "שלושה בתים של ישראלים").
	amongWords = map[string]bool{
		"בהם": true, "בהן": true, "מהם": true, "מהן": true, "ביניהם": true, "ביניהן": true, "מתוכם": true, "מתוכן": true,
		"בתוכם": true, "כולל": true, "לרבות": true, "של": true,
	}
)

// oneOf says word is one of set, alone or after joined letters.
func oneOf(set map[string]bool, word string) bool {
	for _, stem := range stems(word) {
		if set[stem] {
			return true
		}
	}
	return false
}

// headcountReach is how many words after a count the people it counts are looked for: "שלושה אנשים בעלי אזרחות
// איראנית".
const headcountReach = 4

// headcount is a count of a country's people: how many, of which people (as nationalities has it), the count as
// written ("שלושה", "3"), and where the people's word stands among the text's words.
type headcount struct {
	value  int64
	people string
	word   string
	at     int
}

// clauseWord is a word of a text as a headcount is read from it: as compared, as written, and whether punctuation
// after it ends the clause it is in (ends), a comma alone aside (comma).
type clauseWord struct {
	key, raw    string
	ends, comma bool
}

// clauseWords are text's words, in the order and the number of the keys of its tokens.
func clauseWords(text string) []clauseWord {
	var out []clauseWord
	for _, t := range tokens(text) {
		raw := strings.TrimSpace(t.text)
		if t.key == "" || strings.HasPrefix(raw, "(") || strings.HasPrefix(raw, "[") {
			if len(out) > 0 {
				out[len(out)-1].ends = true // a dash alone, an opening bracket
			}
			if t.key == "" {
				continue
			}
		}
		// What follows the word's last letter or digit.
		tail := raw[len(strings.TrimRightFunc(raw, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })):]
		out = append(out, clauseWord{
			key: t.key, raw: raw,
			ends: strings.ContainsAny(tail, ".;:!?)]–—…"), comma: strings.Contains(tail, ","),
		})
	}
	return out
}

var wholeNumber = regexp.MustCompile(`^\d+$`)

// headcounts are the counts of a country's people text gives: a count, in a word of its own (counts) or in digits
// (no year, and no number with a point or a colon), and in the headcountReach words after it, in the same clause
// and before any other number or a month, the people it counts.
// That is a people's word in the plural ("שלושה תיירים סינים", "3 מהאיראנים"; not one after "ב" or "ל", which is a
// place as often: "במצרים"), one in any form after a word of citizenWords ("שלושה בעלי אזרחות איראנית"), or a
// country's name after a word of peopleOf ("שלושה אזרחי איראן"). loose takes a country's name anywhere there too
// ("שלושה תיירים מסין"), and reads on past a comma ("השלושה, אזרחי סין,"): for the text a sentence is checked
// against, which must not lose a true one.
func headcounts(text string, loose bool) []headcount {
	ws := clauseWords(text)
	values := map[int]int64{}
	for _, c := range counts(text) {
		values[c.at] = c.value
	}
	for i, w := range ws {
		if raw := plainNumber(numberRun.FindString(w.raw)); wholeNumber.MatchString(raw) && !yearAlone.MatchString(raw) {
			if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
				values[i] = value
			}
		}
	}
	var out []headcount
	for i, w := range ws {
		value, ok := values[i]
		if !ok {
			continue
		}
		for j := i + 1; j < len(ws) && j <= i+headcountReach && !ws[j-1].ends && (loose || !ws[j-1].comma); j++ {
			word := ws[j].key
			if isNumber(word) || numberWord(word) || isMonth(word) || oneOf(amongWords, word) {
				break
			}
			people, plural, joined := nationalJoined(word)
			switch {
			case people != "" && plural && strings.ContainsAny(joined, "בל"):
				people = ""
				if loose {
					people = country(word)
				}
			case people != "" && (plural || j > i+1 && oneOf(citizenWords, ws[j-1].key)):
			case people == "" && (loose || j > i+1 && oneOf(peopleOf, ws[j-1].key)):
				people = country(word)
			default:
				people = ""
			}
			if people != "" {
				written := w.key
				if isNumber(written) {
					written = numberRun.FindString(w.raw)
				}
				out = append(out, headcount{value: value, people: people, word: written, at: j})
				break
			}
		}
	}
	return out
}

// headcounted says one of texts gives h: the same count of the same people (headcounts, read loosely), or a
// sentence that names as many of them one by one: the people's word in the singular h.value times ("איראני בן 30,
// איראני בן 41 ואיראנית"), or that with its counts of them adding up to h.value ("שני איראנים ואיראני נוסף"). A text
// that counts three people and calls one of them by the people's word gives no three of that people.
func headcounted(h headcount, texts ...string) bool {
	for _, text := range texts {
		for _, sentence := range splitSentences(text) {
			var sum, singles int64
			inCount := map[int]bool{}
			for _, other := range headcounts(sentence, true) {
				if other.people != h.people {
					continue
				}
				if other.value == h.value {
					return true
				}
				sum += other.value
				inCount[other.at] = true
			}
			for i, word := range keys(tokens(sentence)) {
				if people, plural := national(word); people == h.people && !plural && !inCount[i] {
					singles++
				}
			}
			if singles >= h.value || sum+singles == h.value {
				return true
			}
		}
	}
	return false
}

// weekdays are the days of the week as they follow "יום": by name, or by letter ("יום ג׳").
var weekdays = map[string]string{
	"ראשון": "יום 1", "שני": "יום 2", "שלישי": "יום 3", "רביעי": "יום 4", "חמישי": "יום 5", "שישי": "יום 6", "ששי": "יום 6",
	"שבת": "יום 7", "א": "יום 1", "ב": "יום 2", "ג": "יום 3", "ד": "יום 4", "ה": "יום 5", "ו": "יום 6",
}

// relativeDays are the words of a day by the day they are said on, each with the day it is: this morning and
// tonight are today, last night is yesterday.
var relativeDays = map[string]string{
	"היום": "היום", "הבוקר": "היום", "הערב": "היום", "הלילה": "היום", "אתמול": "אתמול", "אמש": "אתמול", "שלשום": "שלשום",
	"מחר": "מחר", "מחרתיים": "מחרתיים",
}

// days are the days text names in words: a day by the day it is said on ("אתמול", "אמש", "מחר"), a day of the week
// ("ביום שלישי", "יום ג׳", "בשבת", and "ברביעי" for the days whose word is little else), and a month.
func days(text string) map[string]bool {
	out := map[string]bool{}
	ts := keys(tokens(text))
	for i, word := range ts {
		// After "יום", and not after "היום": "היום שני חשודים נעצרו".
		if day := weekdays[word]; day != "" && i > 0 && contains(stems(ts[i-1]), "יום") && !contains(stems(ts[i-1]), "היום") {
			out[day] = true
		}
		if rest, ok := strings.CutPrefix(word, "ב"); ok && (rest == "שלישי" || rest == "רביעי" || rest == "חמישי" || rest == "שישי") {
			out[weekdays[rest]] = true
		}
		for _, stem := range stems(word) {
			switch {
			case stem == "שבת":
				out[weekdays[stem]] = true
			case relativeDays[stem] != "":
				out[relativeDays[stem]] = true
			case stem == "מרס":
				out["מרץ"] = true
			case months[stem]:
				out[stem] = true
			}
		}
	}
	return out
}

// anotherDay says text names a day that than doesn't: in words (days), or a date or a year in digits. A sentence
// with one tells of another day than the one it is compared with, and what it gives is no other value for that
// one's fact: last night's rating is no correction of the night before's.
func anotherDay(text, than string) bool {
	if another(text, than, true) {
		return true
	}
	named := days(than)
	for day := range days(text) {
		if !named[day] {
			return true
		}
	}
	return false
}

// another says text has a number that than doesn't state: in digits (a date or a year aside), or a count in a
// word. A count is compared with than's counts, in digits or in a word ("שלושה" and "3", "שתי" and "שניים"), and
// not with every word of than that a number is written with: "שלושה חשודים נעצרו" is another number than "שני
// חשודים נעצרו ביום שלישי". dates says that of its dates and years instead.
func another(text, than string, dates bool) bool {
	n := newNumbers()
	n.add(than)
	if dates {
		for _, f := range figures(text) {
			if f.date && !n.states(f) {
				return true
			}
		}
		return false
	}
	counted := map[int64]bool{}
	for _, c := range counts(than) {
		counted[c.value] = true
	}
	for _, f := range figures(text) {
		if !f.date && !n.stating(f, counted) {
			return true
		}
	}
	for _, c := range counts(text) {
		if !n.stating(c.figure(), counted) {
			return true
		}
	}
	return false
}

// differ says two sentences give different numbers: each has one the other doesn't state. A date or a year is
// none.
func differ(a, b string) bool { return another(a, b, false) && another(b, a, false) }

// sameTime says neither of two sentences has a date or a year the other doesn't state.
func sameTime(a, b string) bool { return !another(a, b, true) && !another(b, a, true) }

// news is what text's numbers add to the story n is of: number says one of them is no date or year and the story
// lacks it; dated that the only ones the story lacks are dates and years, which make no version (round 4: "the
// law of 14.3.2021", "since 2023").
func (n *numbers) news(text string) (number, dated bool) {
	for _, f := range figures(text) {
		switch {
		case n.has(f):
		case f.date:
			dated = true
		default:
			number = true
		}
	}
	return number, dated && !number
}

// functionWords are words that tell nothing of what a sentence is about: two sentences that differ only in them
// say the same thing ("שלושה חשודים נעצרו עד כה" and "שלושה חשודים נעצרו"). Not "לא" and its like, which turn a
// fact over, and no word that says where, when or more of what.
var functionWords = map[string]bool{
	"את": true, "של": true, "על": true, "עם": true, "כי": true, "גם": true, "הוא": true, "היא": true, "הם": true, "הן": true,
	"זה": true, "זו": true, "זאת": true, "אלה": true, "אלו": true, "הזה": true, "הזו": true, "הזאת": true, "האלה": true,
	"אם": true, "או": true, "כל": true, "בכל": true, "לכל": true, "מכל": true, "עד": true, "אשר": true, "אך": true, "אבל": true,
	"רק": true, "כבר": true, "אף": true, "לפי": true, "כדי": true, "כך": true, "כה": true, "היה": true, "היתה": true,
	"הייתה": true, "היו": true, "יש": true, "לו": true, "לה": true, "להם": true, "שלו": true, "שלה": true, "שלהם": true,
	"אותו": true, "אותה": true, "אותם": true, "בו": true, "בה": true, "בהם": true, "מן": true, "ידי": true, "אכן": true,
	"בסך": true, "סך": true, "הכול": true, "הכל": true, "כולל": true, "בסה״כ": true, "סהכ": true, "בסהכ": true, "כלומר": true,
}

// functionWord says word is one of functionWords, alone or after "ו" or "ש" ("וגם", "שכבר").
func functionWord(word string) bool {
	if functionWords[word] {
		return true
	}
	r := []rune(word)
	return len(r) > 2 && r[0] == 'ו' && functionWords[string(r[1:])] || len(r) > 3 && r[0] == 'ש' && functionWords[string(r[1:])]
}

// content says word tells what a sentence is about: no number, no word a number is written with, no function word.
func content(word string) bool { return !isNumber(word) && !numberWord(word) && !functionWord(word) }

// tally is a number with the word it counts or measures: "3 חשודים", "שלושה חשודים", "2 מיליון שקל".
type tally struct{ value, of string }

// tallies are text's counts and measures with what each is of: in digits (no date, year, hour or age) and in a
// word.
func tallies(text string) []tally {
	var out []tally
	for _, f := range figures(text) {
		if f.measure() && f.of != "" {
			out = append(out, tally{f.text, f.of})
		}
	}
	for _, c := range counts(text) {
		if len(c.of) > 0 {
			out = append(out, tally{c.figure().text, c.of[0]})
		}
	}
	return out
}

// recount is a count text writes in a word that is news to the story, "" when there is none: the story counts
// the same thing with another number and not with this one ("שלושה חשודים" where it has "2 חשודים"; tallied are
// the story's tallies); text counts more of something ("שני נאשמים נוספים"); or it is three or more and the story
// states no such number, in digits or in a count's word (told and counted are the story's numbers and what its
// counts in a word count to). "שני" alone is no news: it is two of anything ("שני הצדדים"), where a number in
// digits is copied from the article.
func recount(text string, told *numbers, counted map[int64]bool, tallied []tally) string {
	_, more := said(text)
	for _, c := range counts(text) {
		value := c.figure().text
		same, other := false, false
		for _, t := range tallied {
			if len(c.of) > 0 && alike(t.of, c.of[0]) {
				same = same || t.value == value
				other = other || t.value != value
			}
		}
		if !same && (other || more || c.value > 2 && !told.stating(c.figure(), counted)) {
			return c.word
		}
	}
	return ""
}

// outcomes are the words of an outcome or a decision: what a court, an authority or an employer did, and what
// became of someone. Each form is written out: Hebrew's passive and its feminine are other words to the word
// likeness (alike).
var outcomes = func() map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.Fields(`
		החליט החליטה החליטו הוחלט אישר אישרה אישרו אושר אושרה אושרו דחה דחתה דחו נדחה נדחתה נדחו
		קבע קבעה קבעו נקבע נקבעה פסק פסקה פסקו נפסק הורה הורתה הורו ביטל ביטלה ביטלו בוטל בוטלה בוטלו
		הטיל הטילה הוטל הוטלה הוטלו אסר אסרה נאסר נאסרה הכריז הכריזה הוכרז הוכרזה
		הרשיע הרשיעה הורשע הורשעה הורשעו זיכה זיכתה זוכה זוכתה זוכו גזר גזרה נגזר נגזרו
		האריך האריכה הוארך שחרר שחררה שוחרר שוחררה שוחררו נעצר נעצרה נעצרו
		הגיש הגישה הגישו הוגש הוגשה הוגשו הואשם הואשמה הואשמו נחתם נחתמה נחתמו
		מינה מינתה מונתה התמנה התמנתה מינוי מינויו מינויה נבחר נבחרה נבחרו
		פיטר פיטרה פוטר פוטרה פוטרו יפוטרו תפטר יפטר מפטרת פיטורי פיטורים פיטוריו
		התפטר התפטרה התפטרו הודח הודחה הושעה הושעתה`) {
		out[word] = true
	}
	return out
}()

// outcome is the word of text that tells an outcome or a decision the story doesn't: one of outcomes, alone or
// after joined letters, that known (the story's words) lacks. "" when there is none.
func outcome(text string, known func(word string) bool) string {
	for _, word := range words(text) {
		for _, stem := range stems(word) {
			if outcomes[stem] && !known(word) {
				return word
			}
		}
	}
	return ""
}

// bodyWords are the words a body's name follows directly, each in the form that stands only before a name
// ("חברת אורן", "עיריית גבעון"): the sign that the word after it is one, as a title is of a person's. Not the ones
// that stand before other words as often ("בנק", "משרד", "ארגון", "קבוצת", "תנועת").
var bodyWords = map[string]bool{
	"חברת": true, "עיריית": true, "מועצת": true, "עמותת": true, "מפלגת": true, "ועדת": true, "אוניברסיטת": true, "סיעת": true,
	"לשכת": true,
}

// newBody is a body (a company, a municipality, a party, a committee) that text names and the story doesn't: one
// of bodyWords, alone or after joined letters, and directly after it a word known lacks. "" when there is none.
func newBody(text string, known func(word string) bool) string {
	ts := nameTokens(text)
	for i := 0; i+1 < len(ts); i++ {
		if ts[i].ends || !ts[i+1].named() || known(ts[i+1].key) {
			continue
		}
		for _, stem := range stems(ts[i].key) {
			if bodyWords[stem] {
				return ts[i].core + " " + ts[i+1].core
			}
		}
	}
	return ""
}

// timeWords are the words that say when and nothing else: a development that is a sentence of the story with one
// of them added tells nothing new.
var timeWords = map[string]bool{"היום": true, "אמש": true, "הבוקר": true, "הערב": true, "הלילה": true, "כעת": true, "עתה": true}

// timeless is text without its time words (alone, or after joined letters: "מהבוקר"); stripped says it had any.
func timeless(text string) (out string, stripped bool) {
	var b strings.Builder
	for _, t := range tokens(text) {
		timed := false
		for _, stem := range stems(t.key) {
			timed = timed || timeWords[stem]
		}
		if timed {
			stripped = true
			continue
		}
		b.WriteString(t.text)
	}
	return b.String(), stripped
}

// titles are the words a person's name follows directly: a title or a rank before a name is the sign that the
// words after it are one. Mostly the abbreviated ones, which stand before a name and little else: not a role that
// takes its field ("שר ה...", "ראש ה...", "סגן ה..."), and not a word that is another word too ("ניצב", "אלוף",
// "מר", "הרב").
var titles = map[string]bool{
	"ח״כ": true, "חה״כ": true, "ד״ר": true, "פרופ׳": true, "פרופסור": true, "עו״ד": true, "רו״ח": true, "גב׳": true,
	"השופט": true, "השופטת": true,
	"רא״ל": true, "תא״ל": true, "אל״ם": true, "סא״ל": true, "רס״ן": true, "סג״ם": true, "רנ״ג": true, "רס״ב": true,
	"רס״ם": true, "רס״ר": true, "רס״ל": true, "סמ״ר": true, "רב״ט": true,
	"תנ״צ": true, "נצ״מ": true, "סנ״צ": true, "רפ״ק": true,
}

// notNames are words that stand where a name does and are none: what the person did or is after a title, and who
// an age in brackets follows when no name does ("תושב העיר (34)").
var notNames = map[string]bool{
	"את": true, "של": true, "על": true, "עם": true, "כי": true, "לא": true, "גם": true, "הוא": true, "היא": true, "זה": true, "אל": true,
	"אם": true, "או": true, "כל": true, "עד": true, "אשר": true, "אך": true, "רק": true, "עוד": true, "כבר": true, "אף": true, "בין": true,
	"לפי": true, "אחרי": true, "לפני": true, "כדי": true, "אמר": true, "אמרה": true, "מסר": true, "מסרה": true, "קבע": true, "קבעה": true,
	"הודיע": true, "הודיעה": true, "ציין": true, "ציינה": true, "טען": true, "טענה": true, "הורה": true, "הורתה": true, "דחה": true,
	"דחתה": true, "קיבל": true, "קיבלה": true, "הוסיף": true, "הוסיפה": true, "כתב": true, "כתבה": true, "בדימוס": true, "לשעבר": true,
	"במילואים": true, "במיל": true, "בית": true, "המשפט": true, "המחוזי": true, "העליון": true, "השלום": true, "הראשי": true, "הראשית": true,
	"תושב": true, "תושבת": true, "גבר": true, "אישה": true, "צעיר": true, "צעירה": true, "נער": true, "נערה": true, "ילד": true,
	"ילדה": true, "נהג": true, "נהגת": true, "בן": true, "בת": true, "כבן": true, "כבת": true, "חייל": true, "חיילת": true, "אזרח": true,
	"אזרחית": true, "פעוט": true, "פעוטה": true, "תינוק": true, "תינוקת": true, "קשיש": true, "קשישה": true, "הולך": true, "רגל": true,
	"רוכב": true, "אופנוע": true, "אופניים": true, "חשוד": true, "חשודה": true, "נאשם": true, "נאשמת": true,
	"גזר": true, "גזרה": true, "הרשיע": true, "הרשיעה": true, "זיכה": true, "זיכתה": true, "האריך": true, "האריכה": true, "שחרר": true,
	"שחררה": true, "פסק": true, "פסקה": true, "החליט": true, "החליטה": true, "ביקש": true, "ביקשה": true, "מתח": true, "מתחה": true,
	"הבהיר": true, "הבהירה": true, "הדגיש": true, "הדגישה": true, "סבר": true, "סברה": true, "נימק": true, "נימקה": true, "הגיש": true,
	"הגישה": true, "עליו": true, "עליה": true, "עליהם": true, "מטעם": true, "החשוד": true, "הנאשם": true, "המשפחה": true, "בכיר": true,
	"בכירה": true, "שלו": true, "שלה": true, "שלהם": true,
}

// nameToken is a word of a text as a name is read from it.
type nameToken struct {
	core string // the word, without the punctuation around it
	key  string // as compared
	ends bool   // punctuation after it ends what it is part of
	// aside says it stands in brackets: "(במיל׳)" between a rank and a name.
	aside bool
}

// asciiMarks writes an abbreviation's marks as the Hebrew ones, as oneLine does for the input.
var asciiMarks = strings.NewReplacer(`"`, "״", "'", "׳")

func nameTokens(text string) []nameToken {
	var out []nameToken
	for _, field := range strings.Fields(text) {
		field = asciiMarks.Replace(field)
		core := strings.TrimRight(strings.TrimLeft(field, "([„“”"), ".,:;!?)]”“")
		// A quote mark that ends a word closes a quotation; inside one it is an abbreviation's ("עו״ד").
		if r := []rune(core); len(r) > 0 && r[len(r)-1] == '״' {
			core = string(r[:len(r)-1])
		}
		t := nameToken{core: core, aside: strings.HasPrefix(field, "(") || strings.HasPrefix(field, "[")}
		if ts := tokens(core); len(ts) > 0 {
			t.key = ts[0].key
		}
		t.ends = !strings.HasSuffix(field, core) || strings.ContainsAny(core, "–—")
		out = append(out, t)
	}
	return out
}

// isTitle says t is a title, alone or after a joined letter or two ("לח״כ", "והשופטת").
func (t nameToken) isTitle() bool {
	r := []rune(t.core)
	for i := 0; i <= 2 && i < len(r); i++ {
		if titles[string(r[i:])] {
			return true
		}
		if !strings.ContainsRune(prefixes, r[i]) {
			break
		}
	}
	return false
}

// named says t can be a word of a name: letters, two at least, and no word that only follows a title.
func (t nameToken) named() bool {
	letters := 0
	for _, r := range t.core {
		switch {
		case unicode.IsLetter(r):
			letters++
		case r == '׳' || r == '״' || r == '-' || r == '־':
		default:
			return false
		}
	}
	return letters >= 2 && !notNames[t.key] && !t.isTitle()
}

var ageInBrackets = regexp.MustCompile(`^\(\d{1,3}\)[.,;:]?$`)

// signed says the two words at i of ts are a name by a sign beside them: a title or a rank directly before them
// (brackets between them aside), or an age in brackets directly after them: "(34)". "בן 34" is no sign: it follows
// what happened to someone as often as a name ("נפצע קשה, בן 34").
func signed(ts []nameToken, fields []string, i int) bool {
	for j := i - 1; j >= 0; j-- {
		if ts[j].aside {
			continue
		}
		if ts[j].isTitle() && !ts[j].ends {
			return true
		}
		break
	}
	return i+2 < len(ts) && !ts[i+1].ends && ageInBrackets.MatchString(fields[i+2])
}

// newName is a person's full name that text gives and the story doesn't: two words in a row, neither of which
// known has, with a sign that they are a name (signed), in text itself or where one of evidence (the articles the
// sentence cites, as the request showed them) has the same two words. "" when there is none. It is narrow on
// purpose: Hebrew marks no names, and two new words in a row are what most new facts have.
func newName(text string, known func(word string) bool, evidence ...string) string {
	ts := nameTokens(text)
	fields := strings.Fields(text)
	type shown struct {
		ts     []nameToken
		fields []string
	}
	var articles []shown
	for i := 0; i+1 < len(ts); i++ {
		first, second := ts[i], ts[i+1]
		if first.ends || !first.named() || !second.named() || known(first.key) || known(second.key) {
			continue
		}
		name := first.core + " " + second.core
		if signed(ts, fields, i) {
			return name
		}
		if articles == nil {
			articles = []shown{}
			for _, e := range evidence {
				articles = append(articles, shown{nameTokens(e), strings.Fields(e)})
			}
		}
		for _, article := range articles {
			for j := 0; j+1 < len(article.ts); j++ {
				if !article.ts[j].ends && alike(article.ts[j].key, first.key) && alike(article.ts[j+1].key, second.key) && signed(article.ts, article.fields, j) {
					return name
				}
			}
		}
	}
	return ""
}
