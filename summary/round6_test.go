package summary

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// What round 6 of the replay changed (docs/incremental.md): the instructions are round 5's again, with the official
// act; a detail that adds a number, a name or an outcome makes a version; a count in a word is read as a number
// where two sentences are compared; every sound sentence goes into the story, a version or not; and a headline that
// names what the story lacks, of an article nothing was taken from, sends the story to be written whole.

func TestTheInstructionsAreRound5sWithTheOfficialAct(t *testing.T) {
	system := strings.Join(strings.Fields(incrementalSystem), " ")
	for _, want := range []string{
		`"development": something that happened or became known since the story was written; a new stage of the event.`,
		`An official act is a "development" too, also when it is made of words: a letter sent, an appeal or a petition filed, a decision, an order, a formal statement or announcement by a leader, a minister or an official body.`,
		`"correction": a fact of the story changed or was wrong.`,
		`"detail": a fact about what the story already tells that it lacks: a name, a number, a place, a time, a quote.`,
		`"reaction": what someone said about it.`,
		`in other words too, is not inserted: it goes in "cite".`,
		"(someone dismissed or appointed, a decision approved, a rift, a result), that is an insert",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("the instructions lack %q", want)
		}
	}
	// What round 6 had and Gemini answered with skipped stories and with news called a detail: kinds told apart by
	// the story's "main event", the version rule told to the model, and what not to insert.
	for _, gone := range []string{"main event", "central fact", "misses no news", "never makes the story news", "is not inserted beside it", "never an \"insert\" beside it", `Its kind is "correction" or "development"`} {
		if strings.Contains(system, gone) {
			t.Errorf("the instructions have %q", gone)
		}
	}
}

// crimeStory is a written story of two reports and three sentences, with a count in a word.
func crimeStory() StorySummary {
	s := rainStory()
	s.Title = "כתב אישום בפרשת זיהום הנחל"
	s.Sentences[0].Text = "כתב אישום הוגש נגד מנהל המפעל בחשד לזיהום הנחל."
	s.Sentences[1].Text = "שני חשודים נעצרו בחשד למעורבות בפרשה."
	s.Sentences[2].Text = "המפעל מעסיק 300 עובדים."
	return s
}

func TestACountInAWordIsANumberWhereSentencesAreCompared(t *testing.T) {
	// Each word a count is written with, by what it counts to.
	for value, forms := range map[int64]string{
		2: "שניים שני שתיים שתי", 3: "שלושה שלוש שלושת", 4: "ארבעה ארבע ארבעת", 5: "חמישה חמש חמשת", 6: "שישה שש ששת",
		7: "שבעה שבע שבעת", 8: "שמונה שמונת", 9: "תשעה תשע תשעת", 10: "עשרה עשר עשרת",
	} {
		for _, word := range strings.Fields(forms) {
			text := "במקום נמצאו " + word + " פריטים."
			if got := counts(text); len(got) != 1 || got[0].value != value || cardinal(word) != value || cardinal("ו"+word) != value && len([]rune(word)) > 2 {
				t.Errorf("%q: %+v, cardinal %d", text, got, cardinal(word))
			}
			// The same count in digits is no other number, and any other is.
			if differ(text, fmt.Sprintf("במקום נמצאו %d פריטים.", value)) || !differ(text, fmt.Sprintf("במקום נמצאו %d פריטים.", value+1)) {
				t.Errorf("%q: differ", text)
			}
		}
	}
	// Two forms of one count are one number; a count and its tens are two, though the words are alike.
	if differ("שתי נשים נעצרו.", "שניים נעצרו.") || !differ("שלוש נשים נעצרו.", "שלושים נשים נעצרו.") || sameWord("שלוש", "שלושים") || !sameWord("שלוש", "שלושה") || !sameWord("ושני", "שתי") {
		t.Fatal("the counts' words")
	}
	// A word that is a number's elsewhere states no count: the day, the second, the other hand.
	for _, pair := range [][2]string{
		{"שלושה חשודים נעצרו ביום שלישי.", "שני חשודים נעצרו ביום שלישי."},
		{"שלושה חשודים נעצרו ביום שני.", "שני חשודים נעצרו ביום שני."},
		{"שלושה שופטים דנו בתיק, והשני פסל את עצמו.", "ארבעה שופטים דנו בתיק, והשני פסל את עצמו."},
	} {
		if !differ(pair[0], pair[1]) {
			t.Errorf("%q and %q: no other number", pair[0], pair[1])
		}
	}
	if got := counts("מצד שני, התושבים תומכים במפעל."); len(got) != 0 {
		t.Fatalf("%+v", got)
	}

	story := crimeStory().Sentences
	story = append(story, Sentence{Text: "שלושים דירות הוצפו בעיר."})
	index := indexOf(story)
	for text, want := range map[string]int{
		"שלושה חשודים נעצרו בחשד למעורבות בפרשה.":          2,
		"3 חשודים נעצרו בחשד למעורבות בפרשה.":              2,
		"שלושה נעצרו בחשד למעורבות בפרשה.":                 2,
		"שלושה חשודים נעצרו עד כה בחשד למעורבות בפרשה.":    2, // words that tell nothing
		"בסך הכול נעצרו שלושה חשודים בחשד למעורבות בפרשה.": 2,
		"גם שלושת החשודים נעצרו כבר בחשד למעורבות בפרשה.":  2,
		"שלוש דירות הוצפו בעיר.":                           4, // three, where the story says thirty
		"שני חשודים נעצרו בחשד למעורבות בפרשה.":            0, // the same count
		"שניים נעצרו בחשד למעורבות בפרשה.":                 0,
		"2 חשודים נעצרו בחשד למעורבות בפרשה.":              0,
		"שלושה חשודים לא נעצרו בחשד למעורבות בפרשה.":       0, // "not" turns the fact over
		"שלושה חשודים נוספים נעצרו בחשד למעורבות בפרשה.":   0, // three more
		"שלושה חשודים שוחררו בחשד למעורבות בפרשה.":         0, // another word: another fact
		"שלושה חשודים נעצרו בחיפה בחשד למעורבות בפרשה.":    0, // another place
		"שלושה חשודים נעצרו.":                              0, // too little of the old sentence
		"שלושה חשודים נעצרו ב-2019 בחשד למעורבות בפרשה.":   0, // another year
	} {
		if got := index.contradicted(text, story); got != want {
			t.Errorf("%q: contradicted %d, want %d", text, got, want)
		}
	}
	// The count in a word where the story has thirty is no restatement of it.
	if index.restates("שלוש דירות הוצפו בעיר.") || !index.restates("שלושים דירות הוצפו בעיר.") {
		t.Fatal("restates")
	}
}

func TestADetailWithAnotherCountInAWordCorrectsTheSentence(t *testing.T) {
	prev := crimeStory()
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	// The story says two were arrested, and a new article three. Gemini answered it as a detail, inserted beside
	// the old sentence (round 6): it takes that sentence's place, a correction, and makes a version.
	m := merge(a, answerOf(t, oneInsert(Detail, "שלושה חשודים נעצרו עד כה בחשד למעורבות בפרשה.")), "s1", ids, now)
	want := "כתב אישום הוגש נגד מנהל המפעל בחשד לזיהום הנחל. | שלושה חשודים נעצרו עד כה בחשד למעורבות בפרשה. | המפעל מעסיק 300 עובדים."
	if got := texts(m.Story.Sentences); !m.Version || got != want || m.Stats != (MergeStats{Asked: 1, Replaced: 1, Corrected: 1}) {
		t.Fatalf("version %v, %+v\n got %s\nwant %s\n%v", m.Version, m.Stats, got, want, notesOf(m))
	}
	if n := m.Notes[0]; n.Kind != Correction || n.Corrects != "s1.2" || !n.MakesVersion || n.Outcome != NoteApplied {
		t.Fatalf("%+v", n)
	}
	// In other words it is no replacement (the old sentence would be lost), and its count is still news: the story
	// counts the suspects otherwise. A version, with the sentence beside the old one.
	m = merge(a, answerOf(t, oneInsert(Detail, "המשטרה עצרה שלושה חשודים.")), "s1", ids, now)
	if !m.Version || m.Stats != (MergeStats{Asked: 1, Inserted: 1, Adds: 1, Since: 1, ByDetail: 1}) || m.Notes[0].News != NewsNumber || len(m.Story.Sentences) != 4 {
		t.Fatalf("%+v %v", m.Stats, notesOf(m))
	}
	// From an article older than the story the correction makes no version, and never goes in without one.
	old := rainSiteg
	old.PublishedAt = scrape.FormatInstant(prev.WrittenAt.Add(-time.Hour))
	a, ids = askedOf(t, prev, rainSiteh, rainSiteE, old)
	m = merge(a, answerOf(t, oneInsert(Detail, "שלושה חשודים נעצרו עד כה בחשד למעורבות בפרשה.")), "s1", ids, now)
	if m.Version || texts(m.Story.Sentences) != texts(prev.Sentences) || m.Stats != (MergeStats{Asked: 1, Corrected: 1, Old: 1, Unapplied: 1, NotInserted: 1}) {
		t.Fatalf("%+v %v", m.Stats, notesOf(m))
	}
}

func TestADetailMakesAVersionByWhatItAddsToTheStory(t *testing.T) {
	prev := crimeStory()
	for text, news := range map[string]string{
		// A number the story lacks: in digits, or a count in a word that is news to it.
		"החברה תפטר 120 עובדים.":                    NewsNumber,
		"בית המשפט הורה למדינה להשיב בתוך 45 ימים.": NewsNumber,
		"לכתב האישום צורפו שני נאשמים נוספים.":      NewsNumber, // two more
		"שלושה שרים ביקרו במפעל.":                   NewsNumber, // three, and the story states no three
		// A person or a body the story doesn't name.
		"עו״ד דנה אלמוג מייצגת את מנהל המפעל.": NewsActor,
		"חברת אורן רכשה את המפעל.":             NewsActor,
		// An outcome or a decision the story doesn't tell.
		"מנהל המפעל הושעה מתפקידו.":      NewsOutcome,
		"בית המשפט דחה את בקשת המנהל.":   NewsOutcome,
		"העובדים חוששים מפיטורים במפעל.": NewsOutcome,
		// None of them: no version, and in the story quietly.
		"המפעל פועל באזור התעשייה.":             "",
		"המפעל הוקם ב-1998.":                    "", // a year
		"מצד שני, התושבים תומכים במפעל.":        "",
		"שני הצדדים נפגשו במפעל.":               "", // two of anything
		"המפעל מעסיק 300 עובדים באזור התעשייה.": "", // the story's own number
		"כתב האישום הוגש לבית המשפט בחיפה.":     "", // an outcome the story tells already
	} {
		a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
		m := merge(a, answerOf(t, oneInsert(Detail, text)), "s1", ids, now)
		n := m.Notes[0]
		if n.News != news || m.Version != (news != "") || n.Adds != (news != "") || n.MakesVersion != (news != "") || len(m.Story.Sentences) != 4 {
			t.Errorf("%q: news %q, want %q; version %v: %v", text, n.News, news, m.Version, notesOf(m))
		}
		if news == "" && (n.Outcome != NoteQuiet || m.Stats.Quiet != 1 || m.Story.Title != prev.Title || len(m.Story.Updates) != len(prev.Updates)) {
			t.Errorf("%q: %+v %v", text, m.Stats, notesOf(m))
		}
		if news != "" && (m.Stats.ByDetail != 1 || m.Stats.Since != 1) {
			t.Errorf("%q: %+v", text, m.Stats)
		}
	}
	// From an article published before the story was written it makes no version, whatever it adds: it goes in
	// quietly.
	old := rainSiteg
	old.PublishedAt = scrape.FormatInstant(prev.WrittenAt.Add(-time.Hour))
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, old)
	for _, text := range []string{"החברה תפטר 120 עובדים.", "חברת אורן רכשה את המפעל.", "מנהל המפעל הושעה מתפקידו."} {
		m := merge(a, answerOf(t, oneInsert(Detail, text)), "s1", ids, now)
		if n := m.Notes[0]; m.Version || !n.Adds || n.News == "" || n.MakesVersion || n.Outcome != NoteQuiet || n.Why != whyQuiet || m.Stats.Since != 0 || m.Stats.Quiet != 1 {
			t.Errorf("%q: %+v %v", text, m.Stats, notesOf(m))
		}
	}
	// A development and a correction make a version as they are; background and a reaction never do.
	a, ids = askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	for kind, version := range map[string]bool{Development: true, Correction: true, Background: false, Reaction: false} {
		if m := merge(a, answerOf(t, oneInsert(kind, "מנהל המפעל הושעה מתפקידו.")), "s1", ids, now); m.Version != version || m.Stats.ByDetail != 0 || len(m.Story.Sentences) != 4 {
			t.Errorf("%s: version %v: %v", kind, m.Version, notesOf(m))
		}
	}
}

func TestAHeadlineThatNamesWhatTheStoryLacksSendsItToBeWrittenWhole(t *testing.T) {
	prev := rainStory()
	w := &Writer{Articles: func(context.Context, string) *scrape.Article { return nil }}
	ctx := context.Background()
	since := func(title, subtitle string) scrape.ArticleSummary {
		r := rainSiteg
		r.Title, r.Subtitle, r.PublishedAt = title, subtitle, scrape.FormatInstant(prev.WrittenAt.Add(20*time.Minute))
		return r
	}
	cited := Answer{Story: "s1", Cite: []AnswerCite{{Sentence: "s1.1", Sources: []string{"a1"}}}}
	for _, c := range []struct {
		title, subtitle, what string
	}{
		// A number, an outcome's word, or two words the story and its articles lack.
		{"30 תושבים פונו מבתיהם בצפון", "", "the number 30"},
		{"גשם כבד בצפון", "ראש העיר התפטר", "התפטר"},
		{"גשם כבד בצפון", "הנחל עלה על גדותיו, הרכבת הושבתה", "הנחל, גדותיו"},
		// Nothing the story lacks; one word in other words; an hour, a date and the story's own number; words any
		// headline has.
		{"גשם כבד בצפון: הכבישים נסגרו", "", ""},
		{"גשם כבד בצפון: הכבישים נחסמו", "", ""},
		{"הגשם בצפון החל בשעה 4 ב-12 בנובמבר", "2 כבישים נסגרו", ""},
		{"צפו: תיעוד בלעדי של הגשם בצפון", "ראש העיר אמר אתמול שהכבישים ייפתחו שוב", ""},
	} {
		a, ids := askedOf(t, prev, rainSiteh, rainSiteE, since(c.title, c.subtitle))
		why, headline := w.tripwire(ctx, a, merge(a, cited, "s1", ids, now))
		if headline != (c.what != "") || c.what != "" && why != whyHeadline+c.what || c.what == "" && why != "" {
			t.Errorf("%q, %q: %q, headline %v", c.title, c.subtitle, why, headline)
		}
	}

	news := since("גשם כבד בצפון", "ראש העיר התפטר")
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, news)
	flagged := func(a *asked, ids *sources, answer Answer) bool {
		t.Helper()
		_, headline := w.tripwire(ctx, a, merge(a, answer, "s1", ids, now))
		return headline
	}
	// An answer that names no article at all is one that added nothing from it.
	if !flagged(a, ids, Answer{Story: "s1"}) {
		t.Fatal("a skipped story")
	}
	// Not when the answer put a sentence of the article into the story, with a version or quietly; nor when it
	// found the article to be about another event.
	for name, answer := range map[string]Answer{
		"a version": {Insert: []AnswerInsert{{After: "top", Kind: "development", Text: "ראש העיר התפטר מתפקידו.", Sources: []string{"a1"}}}},
		"quietly":   {Insert: []AnswerInsert{{After: "top", Kind: "reaction", Text: "התושבים מחו נגד העירייה.", Sources: []string{"a1"}}}},
		"unrelated": {Unrelated: []answerUnrelated{{ID: "a1", Event: "התפטרות ראש העיר"}}},
	} {
		if flagged(a, ids, answer) {
			t.Errorf("%s: flagged", name)
		}
	}
	// A sentence the rules dropped put nothing into the story.
	if !flagged(a, ids, Answer{Insert: []AnswerInsert{{After: "top", Kind: "detail", Text: "גשם כבד ירד הבוקר בצפון.", Sources: []string{"a1"}}}}) {
		t.Fatal("a dropped sentence")
	}
	// Not for an article published before the story was written, and not for one the story was written from, shown
	// again; an article whose time isn't known counts as published since, as in the version rule.
	before := news
	before.PublishedAt = scrape.FormatInstant(prev.WrittenAt.Add(-time.Hour))
	if a, ids := askedOf(t, prev, rainSiteh, rainSiteE, before); flagged(a, ids, cited) {
		t.Fatal("an article older than the story")
	}
	undated := news
	undated.PublishedAt = ""
	if a, ids := askedOf(t, prev, rainSiteh, rainSiteE, undated); !flagged(a, ids, cited) {
		t.Fatal("an article of no known time")
	}
	own := rainSiteE
	own.Title, own.Subtitle = news.Title, news.Subtitle
	step := Step{Story: stories.Story{Reports: []scrape.ArticleSummary{rainSiteh, own}}, Prev: prev, Forced: map[string]bool{own.URL: true}}
	p := plan(step, DefaultSettings, false)
	ids = newSources()
	ids.id(own.URL)
	if a := (&asked{step: step, plan: p, shown: map[string]string{}}); flagged(a, ids, cited) {
		t.Fatal("the story's own article")
	}
	// What the story's articles said is no news to it, though no sentence of the story says it.
	w.Articles = func(_ context.Context, url string) *scrape.Article {
		if url == rainSiteE.URL {
			return &scrape.Article{Body: []string{"ראש העיר התפטר אמש מתפקידו."}}
		}
		return nil
	}
	if flagged(a, ids, cited) {
		t.Fatal("a word of an article the story has")
	}
}

// council adds a story to the replay's archive whose third article has its news in the title: the mayor resigned.
func council(a *archiveMaker) {
	reports := []scrape.ArticleSummary{a.report("siteh", "council", "ישיבת המועצה", "מועצת העיר התכנסה הערב לישיבה.", filler("הישיבה")), a.report("sitee", "council", "המועצה התכנסה"),
		a.reportAt(a.t0.Add(100*time.Minute), "siteg", "council", "ראש העיר התפטר בישיבת המועצה", "מועצת העיר התכנסה הערב לישיבה.", filler("ההתפטרות"))}
	text := []Sentence{{"מועצת העיר התכנסה הערב לישיבה.", []string{reports[0].URL, reports[1].URL}}, {"על סדר היום עמד תקציב העירייה.", []string{reports[1].URL}}}
	a.version("council1", "council", a.t0.Add(50*time.Minute), text, nil, reports[:2]...)
	a.version("council2", "council", a.t0.Add(110*time.Minute), text, []Sentence{{"ראש העיר התפטר מתפקידו.", []string{reports[2].URL}}}, reports...)
}

func TestTheReplayCountsTheHeadlineTripwire(t *testing.T) {
	dir, db := replayArchiveOf(t, council)
	// The council story's new article is only cited: its body says the story again, and its title has the news.
	answer := strings.TrimSuffix(replayAnswer, "]}") + `,
	{"story": "s4", "unrelated": [], "insert": [], "replace": [], "delete": [], "cite": [{"sentence": "s4.1", "sources": ["a4"]}], "title": "", "contradictions": []}]}`
	var cases bytes.Buffer
	w := &Writer{Gemini: &recorded{answers: []string{answer}}, Store: db, Now: func() time.Time { return now }, Log: t.Logf}
	report, err := w.Replay(context.Background(), ReplayOptions{Archive: dir, NoControls: true, Cases: &cases})
	if err != nil || report.Real != 4 || report.LiveNewQuiet != 2 || report.Tripwire != 1 || report.Headline != 1 || report.MissedHeadline != 1 || report.QuietHeadline != 0 || report.MissedTripwire != 1 {
		t.Fatalf("%v %+v", err, report)
	}
	var log bytes.Buffer
	report.Print(&log)
	if want := "headline tripwire (of the tripwire's: nothing of an article published after the version before went into the story, and its title or subtitle names what the story lacks): 1; 1 where live had new facts, 0 where it had none"; !strings.Contains(log.String(), want) {
		t.Fatalf("the report lacks %q:\n%s", want, log.String())
	}
	got := casesOf(t, cases.Bytes())
	if c := got[len(got)-1]; c.Story != "council" || c.Kind != CaseMissed || !c.Merge.Tripwire || !c.Merge.Headline || c.Merge.TripwireWhy != whyHeadline+"התפטר" ||
		!strings.Contains(cases.String(), `"headlineTripwire":true`) {
		t.Fatalf("%+v", c.Merge)
	}
	for _, c := range got[:len(got)-1] {
		if c.Merge.Headline {
			t.Fatalf("%s: %+v", c.Story, c.Merge)
		}
	}
}
