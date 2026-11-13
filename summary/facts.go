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
}

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
func wordAfter(rest string) string {
	// The next words are near: no need to split a whole article after each of its numbers.
	if len(rest) > 80 {
		rest = strings.ToValidUTF8(rest[:80], "")
	}
	for i, field := range strings.Fields(rest) {
		if i == 2 {
			break
		}
		if ts := tokens(field); len(ts) > 0 && ts[0].key != "" {
			return ts[0].key
		}
		if strings.ContainsAny(field, ".,;:!?") {
			break
		}
	}
	return ""
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
		next := wordAfter(text[at[1]:])
		f := figure{text: plainNumber(raw), scale: scales[next]}
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

// states says the text states f, however it writes it: the same number; a part of a time, a date or a score of
// the text's ("19" of "19:30"), or such a one whose parts the text has; in words ("שלושה" for 3, "חצי" for a half); or at another scale ("300 אלף" for 300,000,
// "1.5 מיליון" for 1,500,000, and the other way).
func (n *numbers) states(f figure) bool {
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
	if isWhole && n.written(whole) {
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
