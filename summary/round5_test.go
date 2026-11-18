package summary

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
)

// The rules round 5 of the replay added (docs/incremental.md): an inserted sentence with another number for an old
// one's fact replaces it, and a count of a country's people that no cited article states (round 7 made it a count
// that none gives of that people). Its instructions on the kinds and its rule that a detail makes no version were
// taken back after round 6: round6_test.go.

func TestASentenceSaysAnotherAgainWithAnotherNumber(t *testing.T) {
	story := []Sentence{
		{Text: "גשם כבד ירד הבוקר בצפון."},
		{Text: "2 כבישים נסגרו בגלל הגשם."},
		{Text: "שני חשודים נעצרו אמש בחשד לגניבת ציוד."},
		{Text: "3 בני אדם נפצעו בתאונה בכביש החוף."},
		{Text: "אדם נהרג בתאונה."},
		{Text: "ב-2019 נסגרו 6 גשרים בגלל שיטפון."},
	}
	index := indexOf(story)
	for text, want := range map[string]int{
		"3 כבישים נסגרו בגלל הגשם.":            2,
		"בגלל הגשם נסגרו 3 כבישים.":            2, // in another order
		"שלושה כבישים נסגרו בגלל הגשם.":        2, // the number in a word
		"שלושה חשודים נעצרו בחשד לגניבת ציוד.": 3, // most of the old sentence's words, and a count for a count
		"2 כבישים נסגרו בגלל הגשם.":            0, // the same number
		"עוד 3 כבישים נסגרו בגלל הגשם.":        0, // three more
		"3 כבישים נוספים נסגרו בגלל הגשם.":     0,
		"3 כבישים נסגרו בגלל הגשם בגליל.":      0, // a word more: another place
		"3 כבישים נחסמו בגלל הגשם.":            0, // another word: maybe another fact
		"3 כבישים נסגרו.":                      0, // half the old sentence: replacing it would lose the rest
		"3 כבישים נסגרו בגלל הגשם ב-2023.":     0, // another year
		"2 בני אדם נהרגו בתאונה בכביש החוף.":   0, // killed, where the old sentence counts the hurt
		"4 גשרים נסגרו בגלל שיטפון.":           0, // the old sentence tells of another year
		"7 נסגרו.": 0, // too little to tell
		"ראש העיר אמר שהכבישים ייפתחו בעוד יומיים.": 0,
	} {
		if got := index.contradicted(text, story); got != want {
			t.Errorf("%q: contradicted %d, want %d", text, got, want)
		}
	}
}

func TestAWordedCountIsReadNarrowly(t *testing.T) {
	for text, want := range map[string]string{
		"שלושה חשודים נעצרו.":              "3",
		"ושלושת הנאשמים שוחררו.":           "3",
		"שני חשודים ועשרים עדים נחקרו.":    "2 20",
		"שלוש עשרה מכוניות נפגעו.":         "", // thirteen
		"עשרים ושלושה בתים הוצפו.":         "", // twenty-three
		"המפגש נקבע לשעה שבע.":             "",
		"הדיון יתקיים ביום שני.":           "",
		"השופט השני פסל את עצמו.":          "",
		"נמצאו 3 אלפים פריטים.":            "",
		"התיק נפתח לפני שנים רבות.":        "",
		"החיפוש נמשך 4 ימים וחמש שעות.":    "5",
		"החיפוש נמשך ארבעה ימים ו-5 שעות.": "4",
	} {
		var got []string
		for _, c := range counts(text) {
			got = append(got, c.figure().text)
		}
		if strings.Join(got, " ") != want {
			t.Errorf("counts(%q) = %v, want %s", text, got, want)
		}
	}
	// A number in a word is a number where two sentences are compared: this one is no restatement, though the
	// story has the word elsewhere.
	index := indexOf([]Sentence{{Text: "שני חשודים נעצרו בחשד לגניבת ציוד."}, {Text: "החיפוש אחריהם נמשך שלושה ימים."}})
	if index.restates("שלושה חשודים נעצרו בחשד לגניבת ציוד.") || !index.restates("חשודים נעצרו בחשד לגניבת ציוד.") {
		t.Fatal("restates")
	}
	if !differ("שלושה חשודים נעצרו.", "2 חשודים נעצרו.") || differ("שלושה חשודים נעצרו.", "3 חשודים נעצרו ב-12 בנובמבר.") || differ("שלושה חשודים נעצרו.", "חשודים נעצרו.") {
		t.Fatal("differ")
	}
	plural := func(word string) bool {
		_, plural := national(word)
		return plural
	}
	if !plural("סינים") || !plural("והאיראניות") || plural("כבישים") || plural("רומנים") || plural("איראני") {
		t.Fatal("national")
	}
}

// A recorded answer about the rain story with one new article: the count of closed roads again, with another
// number, as an inserted detail; and a detail with a number the story lacks.
const correctedAnswer = `{"stories": [{"story": "s1", "unrelated": [],
	"insert": [{"after": "s1.3", "kind": "detail", "text": "3 כבישים נסגרו בגלל הגשם.", "sources": ["a1"]},
		{"after": "s1.3", "kind": "detail", "text": "בחיפה ירדו 80 מילימטרים.", "sources": ["a1"]}],
	"replace": [], "delete": [], "cite": [], "title": "3 כבישים נסגרו בצפון", "contradictions": []}]}`

// The same from an older article (a1), with a development from a later one (a2).
const correctedWithNews = `{"stories": [{"story": "s1", "unrelated": [],
	"insert": [{"after": "s1.3", "kind": "detail", "text": "3 כבישים נסגרו בגלל הגשם.", "sources": ["a1"]},
		{"after": "top", "kind": "development", "text": "נחל הקישון עלה על גדותיו.", "sources": ["a2"]}],
	"replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`

func TestAnInsertedSentenceWithAnotherNumberForAnOldOnesFactReplacesIt(t *testing.T) {
	prev := rainStory()
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	m := merge(a, answerOf(t, correctedAnswer), "s1", ids, now)
	// The story said 2 roads were closed, and the new article says 3: the inserted sentence takes the old one's
	// place, where it stood, and doesn't stand beside it after the third. It is a correction, so it makes a version,
	// though Gemini called it a detail; the other detail rides along.
	want := "גשם כבד ירד הבוקר בצפון. | 3 כבישים נסגרו בגלל הגשם. | ראש העיר אמר שהכבישים ייפתחו מחר. | בחיפה ירדו 80 מילימטרים."
	if got := texts(m.Story.Sentences); !m.Version || got != want || m.Story.Title != "3 כבישים נסגרו בצפון" {
		t.Fatalf("version %v\n got %s\nwant %s\n%v", m.Version, got, want, notesOf(m))
	}
	if m.Stats != (MergeStats{Asked: 2, Replaced: 1, Inserted: 1, Corrected: 1, Adds: 1, Since: 1}) {
		t.Fatalf("%+v", m.Stats)
	}
	// The replacement stands on the new article alone, and the note says what it corrects.
	if n := m.Notes[0]; n.Op != "insert" || n.Kind != Correction || n.Corrects != "s1.2" || !n.MakesVersion || n.Outcome != NoteApplied || m.Notes[1].Corrects != "" ||
		strings.Join(m.Story.Sentences[1].Sources, " ") != rainSiteg.URL {
		t.Fatalf("%+v", m.Notes)
	}
	if last := m.Story.Updates[len(m.Story.Updates)-1]; strings.Join(last.Kinds, " ") != "correction detail" || texts(last.Sentences) != "3 כבישים נסגרו בגלל הגשם. | בחיפה ירדו 80 מילימטרים." {
		t.Fatalf("%+v", last)
	}

	// From an article published before the story was written it makes no version, and then it is no quiet update
	// either: it stays out, and the story never says 2 and 3. The other detail goes in quietly.
	before := rainSiteg
	before.PublishedAt = scrape.FormatInstant(prev.WrittenAt.Add(-time.Hour))
	a, ids = askedOf(t, prev, rainSiteh, rainSiteE, before)
	m = merge(a, answerOf(t, correctedAnswer), "s1", ids, now)
	want = "גשם כבד ירד הבוקר בצפון. | 2 כבישים נסגרו בגלל הגשם. | ראש העיר אמר שהכבישים ייפתחו מחר. | בחיפה ירדו 80 מילימטרים."
	if got := texts(m.Story.Sentences); m.Version || got != want || m.Story.Title != prev.Title {
		t.Fatalf("version %v\n got %s\nwant %s", m.Version, got, want)
	}
	if m.Stats != (MergeStats{Asked: 2, Quiet: 1, Old: 1, Adds: 1, Corrected: 1, Unapplied: 1, NotInserted: 1}) {
		t.Fatalf("%+v", m.Stats)
	}
	wantNotes := "insert s1.3: left out (" + whyNotInsert + "); insert s1.3: quiet (" + whyQuiet + ")"
	if got := strings.Join(notesOf(m), "; "); got != wantNotes || m.Notes[0].Corrects != "s1.2" || !m.Notes[0].Old {
		t.Fatalf("notes\n got %s\nwant %s", got, wantNotes)
	}

	// With a version that a later article's development makes, the older article's number replaces the sentence.
	after := rainSited
	after.PublishedAt = scrape.FormatInstant(prev.WrittenAt.Add(time.Minute))
	a, ids = askedOf(t, prev, rainSiteh, rainSiteE, before, after)
	m = merge(a, answerOf(t, correctedWithNews), "s1", ids, now)
	want = "נחל הקישון עלה על גדותיו. | גשם כבד ירד הבוקר בצפון. | 3 כבישים נסגרו בגלל הגשם. | ראש העיר אמר שהכבישים ייפתחו מחר."
	if got := texts(m.Story.Sentences); !m.Version || got != want || m.Stats != (MergeStats{Asked: 2, Replaced: 1, Inserted: 1, Old: 1, Corrected: 1}) || m.Notes[0].MakesVersion {
		t.Fatalf("version %v, %+v\n got %s\nwant %s", m.Version, m.Stats, got, want)
	}

	a, ids = askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	// Background and a reaction stay what they are: they tell of another time, or of what someone said.
	for _, kind := range []string{Background, Reaction} {
		if m = merge(a, answerOf(t, oneInsert(kind, "3 כבישים נסגרו בגלל הגשם.")), "s1", ids, now); m.Version || m.Stats.Corrected != 0 || m.Notes[0].Kind != kind {
			t.Fatalf("%s: %+v %v", kind, m.Stats, notesOf(m))
		}
	}
	// A sentence the answer replaces already is not replaced twice: the inserted one stays inserted.
	twice := answerOf(t, oneInsert(Detail, "3 כבישים נסגרו בגלל הגשם."))
	twice.Replace = []AnswerReplace{{Sentence: "s1.2", Kind: "correction", Text: "4 כבישים נסגרו בגלל הגשם.", Sources: []string{"a1"}}}
	if m = merge(a, twice, "s1", ids, now); !m.Version || m.Stats != (MergeStats{Asked: 2, Replaced: 1, Inserted: 1, Adds: 1, Since: 1}) {
		t.Fatalf("%+v %v", m.Stats, notesOf(m))
	}
	// More roads are another fact: inserted beside the old sentence, a detail, whose number makes a version.
	if m = merge(a, answerOf(t, oneInsert(Detail, "3 כבישים נוספים נסגרו בגלל הגשם.")), "s1", ids, now); !m.Version || m.Stats != (MergeStats{Asked: 1, Inserted: 1, Adds: 1, Since: 1, ByDetail: 1}) ||
		len(m.Story.Sentences) != 4 {
		t.Fatalf("%+v %v", m.Stats, notesOf(m))
	}
}

func TestAReplacementWithAnotherNumberIsACorrectionWhateverItIsCalled(t *testing.T) {
	prev := rainStory()
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	replace := func(text string) Answer {
		return Answer{Story: "s1", Replace: []AnswerReplace{{Sentence: "s1.2", Kind: "detail", Text: text, Sources: []string{"a1"}}}}
	}
	// A replacement never goes in without a version: 3 for 2 is a correction, and makes one.
	m := merge(a, replace("3 כבישים נסגרו בגלל הגשם ליד עכו."), "s1", ids, now)
	if !m.Version || m.Stats != (MergeStats{Asked: 1, Replaced: 1}) || m.Notes[0].Kind != Correction || m.Story.Sentences[1].Text != "3 כבישים נסגרו בגלל הגשם ליד עכו." {
		t.Fatalf("%+v %v", m.Stats, notesOf(m))
	}
	// With the same number it is the detail it was called: no version, and the sentence stays.
	m = merge(a, replace("2 כבישים נסגרו בגלל הגשם ליד עכו."), "s1", ids, now)
	if m.Version || m.Stats != (MergeStats{Asked: 1, Unapplied: 1, NotInserted: 1}) || m.Notes[0].Kind != Detail || texts(m.Story.Sentences) != texts(prev.Sentences) {
		t.Fatalf("%+v %v", m.Stats, notesOf(m))
	}
}

// What the request showed of a new article of the rain story about a rescue.
const rescuedShown = "חילוץ בנחל הקישון. כוחות ההצלה חילצו מהנחל שני תיירים סינים וארבעה ישראלים. 8 מסוקים השתתפו בחילוץ."

func TestACountOfACountrysPeopleThatNoCitedArticleStatesIsDropped(t *testing.T) {
	prev := rainStory()
	shown := map[string]string{rainSiteg.URL: rescuedShown}
	for text, number := range map[string]string{
		"שלושה סינים חולצו מנחל הקישון.":        "שלושה", // the article counts two
		"חמישה תיירים סינים חולצו מנחל הקישון.": "חמישה",
		"מנחל הקישון חולצו שבע ישראליות.":       "שבע",
		"ארבעה ישראלים חולצו מנחל הקישון.":      "", // as the article says
		"4 ישראלים חולצו מנחל הקישון.":          "",
		"שני סינים חולצו מנחל הקישון.":          "",
		"שמונה מסוקים השתתפו בחילוץ.":           "", // the article's 8
		"שישה בני אדם חולצו מנחל הקישון.":       "", // added up, and no country's people: not checked
		"החילוץ בנחל הקישון נמשך חמש שעות.":     "",
	} {
		a, ids := askedShown(t, prev, shown, rainSiteh, rainSiteE, rainSiteg)
		m := merge(a, answerOf(t, oneInsert(Development, text)), "s1", ids, now)
		if number == "" {
			if !m.Version || m.Stats != (MergeStats{Asked: 1, Inserted: 1}) {
				t.Errorf("%q: %+v %v", text, m.Stats, notesOf(m))
			}
			continue
		}
		if want := "insert s1.1: dropped (" + fmt.Sprintf(whyUncounted, number) + ")"; m.Version || m.Stats != (MergeStats{Asked: 1, Unbacked: 1, Worded: 1}) || notesOf(m)[0] != want {
			t.Errorf("%q: %+v %v", text, m.Stats, notesOf(m))
		}
	}
	// A count of the people that the story has already is no count of no article; the same number of something
	// else is none of that people (round 7: round7_test.go).
	prev.Sentences[1].Text = "3 כבישים נסגרו בגלל הגשם."
	a, ids := askedShown(t, prev, shown, rainSiteh, rainSiteE, rainSiteg)
	if m := merge(a, answerOf(t, oneInsert(Development, "שלושה סינים חולצו מנחל הקישון.")), "s1", ids, now); m.Version || m.Stats.Worded != 1 {
		t.Fatalf("%+v", m.Stats)
	}
	prev.Sentences[1].Text = "3 תיירים סינים נתקעו בנחל הקישון."
	a, ids = askedShown(t, prev, shown, rainSiteh, rainSiteE, rainSiteg)
	if m := merge(a, answerOf(t, oneInsert(Development, "שלושה סינים חולצו מנחל הקישון.")), "s1", ids, now); !m.Version || m.Stats.Unbacked != 0 {
		t.Fatalf("%+v", m.Stats)
	}
}
