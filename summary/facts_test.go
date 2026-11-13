package summary

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// The rules round 4 of the replay added (docs/incremental.md): a number no cited article states, dates and years
// in the version rule, a person the story doesn't name, a development that only adds a time word.

// askedShown is askedOf with the text the request showed of each new article, by URL.
func askedShown(t *testing.T, prev StorySummary, shown map[string]string, reports ...scrape.ArticleSummary) (*asked, *sources) {
	t.Helper()
	a, ids := askedOf(t, prev, reports...)
	a.shown = shown
	return a, ids
}

// oneInsert is a recorded answer about one story with one inserted sentence, citing a1.
func oneInsert(kind, text string) string {
	return fmt.Sprintf(`{"stories": [{"story": "s1", "unrelated": [],
		"insert": [{"after": "s1.1", "kind": %q, "text": %q, "sources": ["a1"]}],
		"replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`, kind, text)
}

// What the request showed of two new articles of the rain story: a1 about the floods in the north, and a2, which
// tells of a rescue in the south beside them: a related event, and another one.
const (
	floodsShown = "הצפות בגליל. בעכו פונו 1,200 תושבים. שלושה נחלים עלו על גדותיהם, ו-300 אלף בתים נותקו מחשמל. הנזק נאמד ב-1.5 מיליון שקל. המשטרה סגרה את כביש 70 בשעה 19:30."
	rescueShown = "חילוץ בדרום. 4 בני אדם, שלושה מהם תיירים מצרפת, חולצו מנחל בדרום."
)

// A recorded answer about the rain story with those two articles. From a1: numbers written another way than the
// article writes them, a date worked out, and the story's own number. Then the other event's facts: a count that no
// article gives, a2's count cited to a1, and the same cited to a2; and a replacement with a number of no article.
const numbersAnswer = `{"stories": [{"story": "s1", "unrelated": [],
	"insert": [{"after": "top", "kind": "development", "text": "בעכו פונו 1200 תושבים מבתיהם.", "sources": ["a1"]},
		{"after": "s1.1", "kind": "detail", "text": "3 נחלים עלו על גדותיהם.", "sources": ["a1"]},
		{"after": "s1.1", "kind": "detail", "text": "300,000 בתים נותקו מחשמל.", "sources": ["a1"]},
		{"after": "s1.1", "kind": "detail", "text": "הנזק נאמד ב-1,500,000 שקל.", "sources": ["a1"]},
		{"after": "s1.2", "kind": "detail", "text": "כביש 70 נחסם ב-12 בנובמבר בשעה 19:30.", "sources": ["a1"]},
		{"after": "s1.2", "kind": "detail", "text": "2 הכבישים שנסגרו נמצאים ליד עכו.", "sources": ["a1"]},
		{"after": "s1.3", "kind": "development", "text": "7 תיירים מגרמניה חולצו מנחל בצפון.", "sources": ["a1"]},
		{"after": "s1.3", "kind": "detail", "text": "4 בני אדם חולצו מהנחל.", "sources": ["a1"]},
		{"after": "s1.3", "kind": "detail", "text": "4 בני אדם חולצו מנחל בדרום.", "sources": ["a1", "a2"]}],
	"replace": [{"sentence": "s1.2", "kind": "correction", "text": "2 כבישים ו-6 צמתים נסגרו בגלל הגשם.", "sources": ["a1"]}],
	"delete": [], "cite": [], "title": "", "contradictions": []}]}`

func TestASentenceWithANumberItsArticlesDontStateIsDropped(t *testing.T) {
	prev := rainStory()
	shown := map[string]string{rainSiteg.URL: floodsShown, rainSited.URL: rescueShown}
	a, ids := askedShown(t, prev, shown, rainSiteh, rainSiteE, rainSiteg, rainSited)
	m := merge(a, answerOf(t, numbersAnswer), "s1", ids, now)
	// The numbers the article states stand, however the sentence writes them; so does the story's own, and a date.
	// The other event's count is dropped with whatever it was pinned on, unless the article that states it is cited.
	want := "בעכו פונו 1200 תושבים מבתיהם. | גשם כבד ירד הבוקר בצפון. | 3 נחלים עלו על גדותיהם. | 300,000 בתים נותקו מחשמל. | הנזק נאמד ב-1,500,000 שקל. | " +
		"2 כבישים נסגרו בגלל הגשם. | כביש 70 נחסם ב-12 בנובמבר בשעה 19:30. | 2 הכבישים שנסגרו נמצאים ליד עכו. | ראש העיר אמר שהכבישים ייפתחו מחר. | 4 בני אדם חולצו מנחל בדרום."
	if got := texts(m.Story.Sentences); !m.Version || got != want {
		t.Fatalf("version %v\n got %s\nwant %s\n%s", m.Version, got, want, strings.Join(notesOf(m), "\n"))
	}
	if m.Stats != (MergeStats{Asked: 10, Inserted: 7, Unbacked: 3, Adds: 5, Since: 5}) {
		t.Fatalf("%+v", m.Stats)
	}
	// Each dropped sentence's note names the number.
	var dropped []string
	for _, n := range m.Notes {
		if n.Outcome == NoteDropped {
			dropped = append(dropped, n.Op+" "+n.At+": "+n.Why)
		}
	}
	wantDropped := []string{"replace s1.2: " + fmt.Sprintf(whyUnbacked, "6"), "insert s1.3: " + fmt.Sprintf(whyUnbacked, "7"), "insert s1.3: " + fmt.Sprintf(whyUnbacked, "4")}
	if strings.Join(dropped, "\n") != strings.Join(wantDropped, "\n") {
		t.Fatalf("dropped\n got %s\nwant %s", strings.Join(dropped, "\n"), strings.Join(wantDropped, "\n"))
	}
	// Without the request's text of an article there is nothing to check a number against: the sentence stands.
	a, ids = askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	if m = merge(a, answerOf(t, oneInsert(Development, "7 תיירים מגרמניה חולצו מנחל בצפון.")), "s1", ids, now); !m.Version || m.Stats.Unbacked != 0 {
		t.Fatalf("%+v", m.Stats)
	}
}

func TestANumberIsStatedHoweverItIsWritten(t *testing.T) {
	for _, c := range []struct {
		article, sentence string
		want              bool
	}{
		{"בעכו פונו 1,200 תושבים.", "פונו 1200 תושבים.", true},
		{"בעכו פונו 1200 תושבים.", "פונו 1,200 תושבים.", true},
		{"בעכו פונו 1,200 תושבים.", "פונו 1,300 תושבים.", false},
		{"שלושה נחלים עלו על גדותיהם.", "3 נחלים עלו.", true},
		{"ושלושת הנחלים עלו על גדותיהם.", "3 נחלים עלו.", true},
		{"שלושה נחלים עלו על גדותיהם.", "4 נחלים עלו.", false},
		{"עשרים וחמישה בתים הוצפו.", "25 בתים הוצפו.", true},
		{"עשרים בתים הוצפו.", "25 בתים הוצפו.", false},
		{"שנים עשר בתים הוצפו.", "12 בתים הוצפו.", true},
		{"מאתיים בתים הוצפו.", "200 בתים הוצפו.", true},
		{"שלוש מאות בתים הוצפו.", "300 בתים הוצפו.", true},
		{"חמשת אלפים בתים נותקו.", "5,000 בתים נותקו.", true},
		{"300 אלף בתים נותקו.", "300,000 בתים נותקו.", true},
		{"300,000 בתים נותקו.", "300 אלף בתים נותקו.", true},
		{"300,000 בתים נותקו.", "200 אלף בתים נותקו.", false},
		{"הנזק נאמד ב-1.5 מיליון שקל.", "הנזק נאמד ב-1,500,000 שקל.", true},
		{"הנזק נאמד ב-1,500,000 שקל.", "הנזק נאמד ב-1.5 מיליון שקל.", true},
		{"הנזק נאמד במיליון וחצי שקל.", "הנזק נאמד ב-1.5 מיליון שקל.", true},
		{"הנזק נאמד בשני מיליון שקל.", "הנזק נאמד ב-2 מיליון שקל.", true},
		{"רבע מיליון בתים נותקו.", "250,000 בתים נותקו.", true},
		{"חצי מיליון בתים נותקו.", "500,000 בתים נותקו.", true},
		{"מאה אלף בתים נותקו.", "100,000 בתים נותקו.", true},
		{"זו ההצפה השלישית השנה.", "זו ההצפה ה-3 השנה.", true},
		{"הנזק נאמד ב-1.5 מיליון שקל.", "הנזק נאמד ב-5 מיליון שקל.", false},
		{"ירדו 12.50 מילימטרים.", "ירדו 12.5 מילימטרים.", true},
		{"ירדו 12 מילימטרים ועוד 5.", "ירדו 12.5 מילימטרים.", false},
		{"הכביש נסגר ב-12/11 בשעה 19:30.", "הכביש נסגר ב-12 בחודש בשעה 19:30.", true},
		{"התוצאה 3:1 נקבעה בשעה 19:30.", "בדקה ה-19 נקבעה התוצאה 1:3.", true},
		{"ירדו 12.11 מילימטרים.", "ירדו 12 מילימטרים.", false},
		{"כ-70 אחוז מהכבישים נסגרו.", "70% מהכבישים נסגרו.", true},
		{"הכבישים נסגרו.", "70% מהכבישים נסגרו.", false},
	} {
		article := newNumbers()
		article.add(c.article)
		got := true
		for _, f := range figures(c.sentence) {
			got = got && article.states(f)
		}
		if got != c.want {
			t.Errorf("%q states the numbers of %q: %v, want %v", c.article, c.sentence, got, c.want)
		}
	}
}

func TestADateOrAYearIsNoNumberADetailAdds(t *testing.T) {
	for text, want := range map[string]string{
		"החוק נחקק ב-14.3.2021.":             "14.3.2021 date",
		"הסערה החלה ב-12 בנובמבר 2025.":      "12 date, 2025 date",
		"הסערה החלה ב-3 למרץ.":               "3 date",
		"הכביש סגור מאז 2023.":               "2023 date",
		"הדיון נקבע ל-7/12.":                 "7/12 date",
		"כ-2000 תושבים פונו מבתיהם.":         "2000",
		"לכ-2000 תושבים אין חשמל.":           "2000",
		"2,023 תושבים פונו מבתיהם.":          "2023",
		"בחיפה ירדו 12 מילימטרים בנובמבר.":   "12",
		"הכבישים נסגרו בשעה 7 בבוקר.":        "7",
		"ב-2023 נסגרו 14 כבישים, 1.5 מהם.":   "2023 date, 14, 1.5",
		"נמדדו 12.50 מילימטרים ו-3,400 קו״ט": "12.5, 3400",
	} {
		var got []string
		for _, f := range figures(text) {
			if f.date {
				got = append(got, f.text+" date")
			} else {
				got = append(got, f.text)
			}
		}
		if strings.Join(got, ", ") != want {
			t.Errorf("figures(%q) = %s, want %s", text, strings.Join(got, ", "), want)
		}
	}
	// A detail whose only numbers the story lacks are a date or a year adds nothing, and is marked: it goes in
	// quietly. One with a count beside them adds it, and so does a number that only looks like a year: from an
	// article published since the story, they make a version.
	for text, adds := range map[string]bool{
		"תקנות הסגירה נקבעו ב-14.3.2021.":            false,
		"הנחל עלה על גדותיו לאחרונה ב-2023.":         false,
		"הסערה החלה ב-12 בנובמבר.":                   false,
		"הסערה החלה ב-12 בנובמבר ופגעה ב-9 יישובים.": true,
		"כ-2000 תושבים נותרו ללא חשמל.":              true,
		"בחיפה ירדו 80 מילימטרים של גשם.":            true,
	} {
		a, ids := askedOf(t, rainStory(), rainSiteh, rainSiteE, rainSiteg)
		m := merge(a, answerOf(t, oneInsert(Detail, text)), "s1", ids, now)
		if m.Version != adds || m.Notes[0].Adds != adds || (m.Stats.Adds == 1) != adds || (m.Stats.Dated == 1) == adds || m.Notes[0].Dated == adds {
			t.Errorf("%q: version %v, adds %v, want %v: %+v, %v", text, m.Version, m.Notes[0].Adds, adds, m.Stats, notesOf(m))
		}
		want := "insert s1.1: quiet (" + whyQuietPlain + ")"
		if adds {
			want = "insert s1.1: applied, makes a version"
		}
		if got := strings.Join(notesOf(m), "; "); got != want {
			t.Errorf("%q: %s", text, got)
		}
	}
	// A year the story has is no number it lacks, whatever stands before it.
	prev := rainStory()
	prev.Sentences[2].Text = "ראש העיר אמר שהכבישים ייפתחו ב-14 בחודש."
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	if m := merge(a, answerOf(t, oneInsert(Detail, "עד 14 בחודש יישארו המקלטים פתוחים.")), "s1", ids, now); m.Version || m.Stats.Dated != 0 || m.Stats.Adds != 0 {
		t.Fatalf("%+v", m.Stats)
	}
}

// A recorded answer about the rain story with one new article: a detail that names a person by a title, and one
// that only names the mayor the story tells of already.
const namedAnswer = `{"stories": [{"story": "s1", "unrelated": [],
	"insert": [{"after": "s1.2", "kind": "detail", "text": "לדברי עו״ד ישראל ישראלי, הכבישים ייפתחו בהדרגה.", "sources": ["a1"]},
		{"after": "s1.3", "kind": "detail", "text": "ראש העיר לוי אמר שהכבישים ייפתחו מחר.", "sources": ["a1"]}],
	"replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`

func TestADetailThatNamesAPersonTheStoryDoesntMakesAVersion(t *testing.T) {
	prev := rainStory()
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	m := merge(a, answerOf(t, namedAnswer), "s1", ids, now)
	// The first detail has no number, and adds the person it names: a version. The other adds nothing, and rides
	// along.
	if !m.Version || m.Stats != (MergeStats{Asked: 2, Inserted: 2, Adds: 1, Since: 1, Named: 1, ByDetail: 1}) || m.Notes[0].Name != "ישראל ישראלי" || !m.Notes[0].Adds || m.Notes[0].News != NewsActor ||
		!m.Notes[0].MakesVersion || m.Notes[1].Adds || m.Notes[1].Name != "" {
		t.Fatalf("%+v %+v", m.Stats, m.Notes)
	}
	// One more word of a name (a surname) is no person the story doesn't mention: no version, as before.
	if m = merge(a, answerOf(t, oneInsert(Detail, "ראש העיר לוי אמר שהכבישים ייפתחו מחר.")), "s1", ids, now); m.Version || m.Stats.Named != 0 {
		t.Fatalf("%+v", m.Stats)
	}
	// Nor is a person the story names already.
	named := rainStory()
	named.Sentences[2].Text = "ראש העיר ישראל ישראלי אמר שהכבישים ייפתחו מחר."
	b, ids := askedOf(t, named, rainSiteh, rainSiteE, rainSiteg)
	if m = merge(b, answerOf(t, oneInsert(Detail, "ד״ר ישראל ישראלי מכהן בתפקיד זה עשור.")), "s1", ids, now); m.Version || m.Stats.Named != 0 {
		t.Fatalf("%+v", m.Stats)
	}
	// A sentence that gives the name without a sign is read against the article it cites: there the same two words
	// follow a rank, or an age in brackets follows them.
	plain := "מנהל המחוז דן אלמוני סייר בכבישים שנסגרו."
	for shown, adds := range map[string]bool{
		"הצפות בגליל. מנהל המחוז, תנ״צ דן אלמוני, סייר בכבישים שנסגרו.":    true,
		"הצפות בגליל. דן אלמוני (52), מנהל המחוז, סייר בכבישים שנסגרו.":    true,
		"הצפות בגליל. לדברי דן אלמוני הראל (52), הכבישים ייסגרו.":          false, // the age follows other words
		"הצפות בגליל. מנהל המחוז דן אלמוני סייר בכבישים שנסגרו.":           false,
		"הצפות בגליל. תנ״צ רון פלוני ומנהל המחוז דן אלמוני סיירו בכבישים.": false,
	} {
		a, ids := askedShown(t, prev, map[string]string{rainSiteg.URL: shown}, rainSiteh, rainSiteE, rainSiteg)
		if m := merge(a, answerOf(t, oneInsert(Detail, plain)), "s1", ids, now); m.Version != adds || m.Notes[0].Adds != adds || (m.Notes[0].Name == "דן אלמוני") != adds {
			t.Errorf("%q: adds %v, want %v: %v", shown, m.Notes[0].Adds, adds, m.Notes[0].Name)
		}
	}
	// From an article older than the story it goes in quietly.
	old := rainSiteg
	old.PublishedAt = scrape.FormatInstant(prev.WrittenAt.Add(-2 * time.Hour))
	a, ids = askedOf(t, prev, rainSiteh, rainSiteE, old)
	if m = merge(a, answerOf(t, oneInsert(Detail, "לדברי עו״ד ישראל ישראלי, הכבישים ייפתחו בהדרגה.")), "s1", ids, now); m.Version || m.Stats != (MergeStats{Asked: 1, Quiet: 1, Adds: 1, Named: 1}) {
		t.Fatalf("%+v", m.Stats)
	}
}

func TestANameIsTwoNewWordsWithASign(t *testing.T) {
	story := indexOf(rainStory().Sentences)
	for text, want := range map[string]string{
		"לדברי עו״ד ישראל ישראלי, הכבישים ייפתחו.": "ישראל ישראלי",
		`לדברי עו"ד ישראל ישראלי, הכבישים ייפתחו.`: "ישראל ישראלי",
		"ההחלטה התקבלה אצל השופטת דנה אלמונית.":    "דנה אלמונית",
		"בהצפה נהרג רס״ן (במיל׳) דן אלמוני.":       "דן אלמוני",
		"בהצפה נהרג דן אלמוני (34) מחיפה.":         "דן אלמוני",
		"בהצפה נהרג דן אלמוני הראל (34) מחיפה.":    "אלמוני הראל",
		"לח״כ ישראל ישראלי נמסר שהכבישים ייפתחו.":  "ישראל ישראלי",
		"הנהג הוא Dan Almoni (41), תייר.":          "Dan Almoni",
		// A title and what its holder did, or is, are no name; nor is who an age follows when no name does.
		"השופטת קבעה כי הכבישים ייסגרו.":            "",
		"השופט גזר עליו עבודות שירות.":              "",
		"עו״ד מטעם העירייה מסר תגובה.":              "",
		"הרב אמר לתושבים להישאר בבתים.":             "",
		"השופט בדימוס הורה לפנות את התושבים.":       "",
		"בהצפה נפצע תושב חיפה (34) באורח קל.":       "",
		"בהצפה נפצע צעיר (19) באורח קל.":            "",
		"הוא נעצר בחשד לגרימת נזק.":                 "",
		"בית החולים קלט ארבעה פצועים.":              "",
		"עו״ד ישראל, שייצג את העירייה, מסר תגובה.":  "", // one word
		"ראש העיר לוי אמר שהכבישים ייפתחו מחר.":     "",
		"עוד שני חשודים נעצרו אמש.":                 "", // "עוד", not "עו״ד"
		"לדברי עו״ד, ישראל ישראלי אינו מעורב בדבר.": "", // the title ends its clause
		"הוא נמצא במצב קשה, בן 34 במותו.":           "",
		"עו״ד ראש העיר מסר תגובה.":                  "", // words the story has
	} {
		if got := newName(text, story.has); got != want {
			t.Errorf("newName(%q) = %q, want %q", text, got, want)
		}
	}
}

// A recorded answer about the rain story with one new article: developments that say a sentence of the story again
// with a time word, and one that tells something new with one.
const timedAnswer = `{"stories": [{"story": "s1", "unrelated": [],
	"insert": [{"after": "top", "kind": "development", "text": "2 כבישים נסגרו היום בגלל הגשם.", "sources": ["a1"]},
		{"after": "top", "kind": "development", "text": "ראש העיר אמר כעת שהכבישים ייפתחו מחר.", "sources": ["a1"]},
		{"after": "top", "kind": "development", "text": "מהלילה 2 כבישים נסגרו בגלל הגשם.", "sources": ["a1"]}],
	"replace": [], "delete": [], "cite": [{"sentence": "s1.2", "sources": ["a1"]}], "title": "הכבישים נסגרו היום", "contradictions": []}]}`

func TestADevelopmentThatOnlyAddsATimeWordSaysTheStoryAgain(t *testing.T) {
	prev := rainStory()
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	m := merge(a, answerOf(t, timedAnswer), "s1", ids, now)
	if m.Version || texts(m.Story.Sentences) != texts(prev.Sentences) || m.Story.Title != prev.Title || m.Stats != (MergeStats{Asked: 3, Restated: 3, Timed: 3, Cited: 1}) {
		t.Fatalf("%+v %s", m.Stats, texts(m.Story.Sentences))
	}
	want := []string{
		"insert top: dropped (" + fmt.Sprintf(whyTimed, "s1", 2) + ")",
		"insert top: dropped (" + fmt.Sprintf(whyTimed, "s1", 3) + ")",
		"insert top: dropped (" + fmt.Sprintf(whyTimed, "s1", 2) + ")",
		"cite s1.2: applied",
	}
	if got := notesOf(m); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("notes\n got %s\nwant %s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for text, version := range map[string]bool{
		// A time word beside something new is a development still: another number, another fact.
		"3 כבישים נסגרו היום בגלל הגשם.":     true,
		"נחל הקישון עלה אמש על גדותיו.":      true,
		"היום נפתחו 2 הכבישים שנסגרו.":       true,
		"2 כבישים נסגרו הערב בגלל הגשם.":     false,
		"עתה 2 כבישים נסגרו בגלל הגשם.":      false,
		"2 כבישים נסגרו ביום שני בגלל הגשם.": true, // a day is no time word: it tells when
	} {
		m := merge(a, answerOf(t, oneInsert(Development, text)), "s1", ids, now)
		if m.Version != version || (m.Stats.Timed == 1) == version {
			t.Errorf("%q: version %v, want %v: %v", text, m.Version, version, notesOf(m))
		}
	}
	// Whatever kind it was given: a detail goes into the story without a version, and must not say the story again.
	if m = merge(a, answerOf(t, oneInsert(Detail, "2 כבישים נסגרו היום בגלל הגשם.")), "s1", ids, now); m.Version || m.Stats != (MergeStats{Asked: 1, Restated: 1, Timed: 1}) {
		t.Fatalf("%+v", m.Stats)
	}
	if got, stripped := timeless("מהבוקר נסגרו הכבישים, והיום ייפתחו."); got != "נסגרו הכבישים, ייפתחו." || !stripped {
		t.Fatalf("%q %v", got, stripped)
	}
	if got, stripped := timeless("בבוקר נסגרו הכבישים ביום שני."); got != "בבוקר נסגרו הכבישים ביום שני." || stripped {
		t.Fatalf("%q %v", got, stripped)
	}
}

// A recorded answer about the rain story with one new article whose news is in its subtitle alone: the insert that
// states it, and one with another count.
const subtitleAnswer = `{"stories": [{"story": "s1", "unrelated": [],
	"insert": [{"after": "top", "kind": "development", "text": "מנהל מחלקת הניקוז בעירייה פוטר.", "sources": ["a1"]},
		{"after": "s1.2", "kind": "detail", "text": "בעכו פונו 30 תושבים מבתיהם.", "sources": ["a1"]},
		{"after": "s1.2", "kind": "detail", "text": "בנהריה פונו 45 תושבים מבתיהם.", "sources": ["a1"]}],
	"replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`

func TestTheRequestShowsANewArticlesTitleAndSubtitleAndSaysToReadThem(t *testing.T) {
	prev := rainStory()
	fired := scrape.ArticleSummary{SourceID: "siteg", URL: "https://siteg/rain", Title: "אחרי ההצפות: מנהל הניקוז הודח", Subtitle: "ראש העיר פיטר את מנהל מחלקת הניקוז; בעכו פונו 30 תושבים"}
	brief := scrape.ArticleSummary{SourceID: "sited", URL: "https://sited/rain", Title: "הצפות בצפון", Summary: "העירייה פתחה 5 מקלטים"}
	step := Step{Story: stories.Story{Reports: []scrape.ArticleSummary{rainSiteh, rainSiteE, fired, brief}}, Prev: prev}
	p := plan(step, DefaultSettings, false)
	w := &Writer{Articles: func(context.Context, string) *scrape.Article { return nil }}
	ids := newSources()
	text, a := w.storyText(context.Background(), ids, "s1", step, p, DefaultSettings)
	// Each new article's title and subtitle have their own labelled lines, the site's summary where it gives no
	// subtitle; the instructions name the labels, and say the main news is there.
	for _, want := range []string{
		"[a1] siteg\nכותרת: אחרי ההצפות: מנהל הניקוז הודח\nמשנה: ראש העיר פיטר את מנהל מחלקת הניקוז; בעכו פונו 30 תושבים\n",
		"[a2] sited\nכותרת: הצפות בצפון\nתקציר: העירייה פתחה 5 מקלטים\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the request lacks %q:\n%s", want, text)
		}
	}
	system := strings.Join(strings.Fields(incrementalSystem), " ")
	for _, want := range []string{
		`then its title ("כותרת:"), its subtitle ("משנה:", or "תקציר:", the site's summary of it) and its body ("גוף:"`,
		`read its title ("כותרת:") and its subtitle ("משנה:" or "תקציר:") first: they state the article's main news.`,
		"When the story so far does not say what they state", "that is an insert, even when the body mostly repeats the story",
		// Round 4's other instructions: a related but separate event, and facts copied as they are.
		"Only this story's event.", "Its facts are not this story's: don't insert them",
		"Copy names, numbers and nationalities exactly as the article gives them", "never take one from another story of the input",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("the instructions lack %q", want)
		}
	}
	// What the request showed of an article is its title and subtitle (or summary) with its body: a number that
	// only the subtitle states is one the article states.
	if a.shown[fired.URL] != "אחרי ההצפות: מנהל הניקוז הודח. ראש העיר פיטר את מנהל מחלקת הניקוז; בעכו פונו 30 תושבים." || !strings.Contains(a.shown[brief.URL], "העירייה פתחה 5 מקלטים") {
		t.Fatalf("shown %q", a.shown)
	}
	m := merge(a, answerOf(t, subtitleAnswer), "s1", ids, now)
	if !m.Version || m.Stats != (MergeStats{Asked: 3, Inserted: 2, Unbacked: 1, Adds: 1, Since: 1}) || m.Story.Sentences[0].Text != "מנהל מחלקת הניקוז בעירייה פוטר." {
		t.Fatalf("%+v %v", m.Stats, notesOf(m))
	}
}
