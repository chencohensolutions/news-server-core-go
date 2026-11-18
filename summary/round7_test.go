package summary

import (
	"fmt"
	"strings"
	"testing"
)

// What round 7 of the replay changed (docs/incremental.md), three checks of what Gemini answered: a count of a
// country's people must be one a cited article gives of that people; no sentence corrects one of another day; and
// an inserted sentence that says again what another of the answer does is dropped, a count read as its number.

// What the request showed of a new article of the rain story: three were rescued, one of them Chinese and two
// Japanese.
const threeRescuedShown = "חילוץ בנחל הקישון. שלושה תיירים חולצו מהנחל, בהם אזרח סיני ושני יפנים. 8 מסוקים השתתפו בחילוץ."

func TestACountOfAPeopleIsOneAnArticleGivesOfThatPeople(t *testing.T) {
	prev := rainStory()
	shown := map[string]string{rainSiteg.URL: threeRescuedShown}
	for text, number := range map[string]string{
		// The article has a three, and it is no three of that people.
		"שלושה בעלי אזרחות סינית חולצו מנחל הקישון.":        "שלושה",
		"שלושה אנשים בעלי אזרחות סינית חולצו מנחל הקישון.":  "שלושה",
		"שלושה סינים חולצו מנחל הקישון.":                    "שלושה",
		"שלושת התיירים הסינים חולצו מנחל הקישון.":           "שלושת",
		"3 תיירים סינים חולצו מנחל הקישון.":                 "3",
		"שלושה אזרחי סין חולצו מנחל הקישון.":                "שלושה",
		"מנחל הקישון חולצו שלוש יפניות.":                    "שלוש",
		"שלושה תיירים חולצו מנחל הקישון.":                   "", // as the article says
		"שלושה תיירים, בהם סיני ושני יפנים, חולצו מהנחל.":   "",
		"שלושה תיירים חולצו מהנחל: אזרח סיני ושני יפנים.":   "",
		"שני יפנים ואזרח סיני חולצו מנחל הקישון.":           "",
		"שלושה תיירים חולצו מהנחל בהם סינים.":               "", // some of the three
		"שלושה מסוקים של הסינים השתתפו בחילוץ בנחל הקישון.": "", // theirs, and worked out of nothing else
		"שלושה תיירים חולצו במצרים מנחל גועש.":              "", // a place
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

	// What gives three of a people: the count beside its word, however the people are called, or as many of them
	// named one by one.
	three := headcount{value: 3, people: "סיני"}
	for text, want := range map[string]bool{
		"שלושה סינים חולצו מהנחל.":                                  true,
		"3 תיירים סינים חולצו מהנחל.":                               true,
		"שלוש תיירות סיניות חולצו מהנחל.":                           true,
		"החילוץ הסתיים. ושלושת הסינים שבו למלון.":                   true,
		"שלושה אזרחים סיניים חולצו מהנחל.":                          true,
		"שלושה בעלי אזרחות סינית חולצו מהנחל.":                      true,
		"שלושה צעירים ממוצא סיני חולצו מהנחל.":                      true,
		"שלושה אזרחי סין חולצו מהנחל.":                              true,
		"שלושה תיירים מסין חולצו מהנחל.":                            true,
		"השלושה, אזרחי סין, חולצו מהנחל.":                           true,
		"מהנחל חולצו סיני בן 30, סיני בן 41 וסינית בת 25.":          true, // one by one
		"שני סינים חולצו בבוקר וסיני נוסף בערב.":                    true, // added up
		"שלושה תיירים חולצו מהנחל, אחד מהם סיני.":                   false,
		"שלושה חולצו מהנחל: סיני ושני יפנים.":                       false,
		"שלושה תיירים חולצו מהנחל בהם סיני ושני יפנים.":             false,
		"שלושה תיירים חולצו מהנחל בהם סינים.":                       false,
		"שלושה חולצו. הסינים שבו למלון.":                            false, // another sentence
		"שלושה מסוקים ושני סינים היו במקום.":                        false, // another number between them
		"חמישה סינים חולצו מהנחל.":                                  false,
		"התייר הסיני חולץ. הסיני נחקר. הדרכון הסיני נמצא בנחל.":     false, // one of them, three times over
		"ב-2023 סינים רבים ביקרו בגליל, וב-3 בנובמבר סינים חולצו.":  false, // a year and a date count nobody
		"הגשם ירד 3.5 שעות, וסינים רבים חולצו ב-19:30 מנחל הקישון.": false,
	} {
		if got := headcounted(three, text); got != want {
			t.Errorf("headcounted(%q) = %v, want %v: %+v", text, got, want, headcounts(text, true))
		}
	}
	// A people's word, by the form it is in.
	for word, want := range map[string]string{
		"סיני": "סיני", "הסינית": "סיני", "סינים": "סיני+", "והסיניות": "סיני+", "סיניים": "סיני+", "אמריקנים": "אמריקאי+",
		"רומנים": "", "מסוקים": "", "סין": "",
	} {
		people, plural := national(word)
		if plural {
			people += "+"
		}
		if people != want {
			t.Errorf("national(%q) = %q, want %q", word, people, want)
		}
	}
}

func TestADayASentenceNamesIsReadFromItsWords(t *testing.T) {
	for _, c := range []struct {
		text, than string
		want       bool
	}{
		{"ביום שלישי ירדו בחיפה 82 מילימטרים.", "ביום שני ירדו בחיפה 81 מילימטרים.", true},
		{"ביום ג׳ ירדו בחיפה 82 מילימטרים.", "ביום שלישי ירדו בחיפה 81 מילימטרים.", false},
		{"ברביעי ירדו בחיפה 82 מילימטרים.", "ביום רביעי ירדו בחיפה 81 מילימטרים.", false},
		{"בשבת ירדו בחיפה 82 מילימטרים.", "ביום שישי ירדו בחיפה 81 מילימטרים.", true},
		{"במוצאי שבת ירדו 82 מילימטרים.", "ביום שבת ירדו 81 מילימטרים.", false},
		{"אתמול ירדו בחיפה 82 מילימטרים.", "בחיפה ירדו 81 מילימטרים.", true},
		{"אמש ירדו בחיפה 82 מילימטרים.", "אתמול ירדו בחיפה 81 מילימטרים.", false}, // last night is yesterday
		{"הבוקר ירדו בחיפה 82 מילימטרים.", "היום ירדו בחיפה 81 מילימטרים.", false},
		{"מחר יירדו בחיפה 82 מילימטרים.", "היום יירדו בחיפה 81 מילימטרים.", true},
		{"ב-13 בנובמבר ירדו 82 מילימטרים.", "ב-12 בנובמבר ירדו 81 מילימטרים.", true},
		{"ב-13/11 ירדו 82 מילימטרים.", "ירדו 81 מילימטרים.", true},
		{"בדצמבר ירדו 82 מילימטרים.", "בנובמבר ירדו 81 מילימטרים.", true},
		{"ב-2023 ירדו 82 מילימטרים.", "ירדו 81 מילימטרים.", true},
		// A day the old sentence names and the new one doesn't is no other day: the new one says nothing of when.
		{"בחיפה ירדו 82 מילימטרים.", "אמש ירדו בחיפה 81 מילימטרים.", false},
		// No day: both, the second, in the evening, nowadays.
		{"בשני המקרים ירדו 82 מילימטרים.", "ירדו 81 מילימטרים.", false},
		{"שני נחלים עלו על גדותיהם.", "היום שלושה נחלים עלו על גדותיהם.", false},
		{"היום שני נחלים עלו על גדותיהם.", "היום שלושה נחלים עלו על גדותיהם.", false},
		{"ביום השני לסערה ירדו 82 מילימטרים.", "ירדו 81 מילימטרים.", false},
		{"בערב ירדו בחיפה 82 מילימטרים.", "ירדו 81 מילימטרים.", false},
		{"כיום יורדים בחיפה 82 מילימטרים.", "ירדו 81 מילימטרים.", false},
	} {
		if got := anotherDay(c.text, c.than); got != c.want {
			t.Errorf("anotherDay(%q, %q) = %v, want %v: %v", c.text, c.than, got, c.want, days(c.text))
		}
	}
}

func TestNoSentenceCorrectsOneOfAnotherDay(t *testing.T) {
	prev := rainStory()
	prev.Sentences[1].Text = "ביום שני ירדו בחיפה 81 מילימטרים של גשם."
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	// The next day's figure, in the old sentence's words but for the day: until round 7 it took that sentence's
	// place, as a correction. It is inserted, and the story has both days.
	m := merge(a, answerOf(t, oneInsert(Detail, "ביום שלישי ירדו בחיפה 82 מילימטרים של גשם.")), "s1", ids, now)
	want := "גשם כבד ירד הבוקר בצפון. | ביום שלישי ירדו בחיפה 82 מילימטרים של גשם. | ביום שני ירדו בחיפה 81 מילימטרים של גשם. | ראש העיר אמר שהכבישים ייפתחו מחר."
	if got := texts(m.Story.Sentences); !m.Version || got != want || m.Stats != (MergeStats{Asked: 1, Inserted: 1, Adds: 1, Since: 1, ByDetail: 1, OtherDay: 1}) {
		t.Fatalf("version %v, %+v\n got %s\nwant %s\n%v", m.Version, m.Stats, got, want, notesOf(m))
	}
	if n := m.Notes[0]; n.Kind != Detail || n.Corrects != "" || !n.OtherDay {
		t.Fatalf("%+v", n)
	}
	// The same day's other figure still corrects it.
	m = merge(a, answerOf(t, oneInsert(Detail, "ביום שני ירדו בחיפה 82 מילימטרים של גשם.")), "s1", ids, now)
	if !m.Version || m.Stats != (MergeStats{Asked: 1, Replaced: 1, Corrected: 1}) || m.Notes[0].OtherDay {
		t.Fatalf("%+v %v", m.Stats, notesOf(m))
	}

	// A replacement Gemini answered as a correction, of another day: the sentence stays, with this one after it as
	// a detail. So does one answered as a detail, which another number would have made a correction.
	for _, kind := range []string{Correction, Detail} {
		m = merge(a, Answer{Story: "s1", Replace: []AnswerReplace{
			{Sentence: "s1.2", Kind: kind, Text: "אתמול ירדו בחיפה 82 מילימטרים של גשם.", Sources: []string{"a1"}},
		}}, "s1", ids, now)
		want = "גשם כבד ירד הבוקר בצפון. | ביום שני ירדו בחיפה 81 מילימטרים של גשם. | אתמול ירדו בחיפה 82 מילימטרים של גשם. | ראש העיר אמר שהכבישים ייפתחו מחר."
		if got := texts(m.Story.Sentences); !m.Version || got != want || m.Stats != (MergeStats{Asked: 1, Inserted: 1, Adds: 1, Since: 1, ByDetail: 1, OtherDay: 1}) {
			t.Fatalf("%s: version %v, %+v\n got %s\nwant %s\n%v", kind, m.Version, m.Stats, got, want, notesOf(m))
		}
		if n := m.Notes[0]; n.Op != "replace" || n.Kind != Detail || !n.OtherDay || n.Outcome != NoteApplied {
			t.Fatalf("%s: %+v", kind, n)
		}
	}
	// One of the same day, or of no day, replaces the sentence as before; and a development is Gemini's to place.
	for text, kind := range map[string]string{
		"ביום שני ירדו בחיפה 82 מילימטרים של גשם.": Correction,
		"בחיפה ירדו 82 מילימטרים של גשם.":          Correction,
		"אתמול ירדו בחיפה 82 מילימטרים של גשם.":    Development,
	} {
		m = merge(a, Answer{Story: "s1", Replace: []AnswerReplace{{Sentence: "s1.2", Kind: kind, Text: text, Sources: []string{"a1"}}}}, "s1", ids, now)
		if !m.Version || m.Stats != (MergeStats{Asked: 1, Replaced: 1}) || len(m.Story.Sentences) != 3 || m.Story.Sentences[1].Text != text {
			t.Errorf("%q: %+v %v", text, m.Stats, notesOf(m))
		}
	}
}

func TestAnInsertThatSaysAgainWhatTheAnswerReplacesWithIsDropped(t *testing.T) {
	prev := crimeStory()
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	replace := []AnswerReplace{{Sentence: "s1.2", Kind: Correction, Text: "3 חשודים נעצרו בחשד למעורבות בפרשה.", Sources: []string{"a1"}}}
	corrected := "כתב אישום הוגש נגד מנהל המפעל בחשד לזיהום הנחל. | 3 חשודים נעצרו בחשד למעורבות בפרשה. | המפעל מעסיק 300 עובדים."
	// The answer replaces the two arrested with three, and inserts the three again, the count in a word: until
	// round 7 the story had both.
	for _, text := range []string{
		"שלושה חשודים נעצרו בחשד למעורבות בפרשה.",
		"ושלושת החשודים נעצרו בחשד למעורבות בפרשה.",
		"שלושה חשודים נעצרו בחשד למעורבות בפרשה במפעל.", // the word more is one the story has
	} {
		m := merge(a, Answer{Story: "s1", Replace: replace, Insert: []AnswerInsert{{After: "s1.2", Kind: Development, Text: text, Sources: []string{"a1"}}}}, "s1", ids, now)
		if got := texts(m.Story.Sentences); !m.Version || got != corrected || m.Stats != (MergeStats{Asked: 2, Replaced: 1, Repeated: 1}) {
			t.Errorf("%q: %+v\n got %s\n%v", text, m.Stats, got, notesOf(m))
		}
		if got, want := notesOf(m)[1], "insert s1.2: dropped ("+whyRepeated+")"; got != want {
			t.Errorf("%q: %s", text, got)
		}
	}
	// What tells more stays: another count, more of them, a word neither the answer nor the story has.
	for _, text := range []string{
		"ארבעה חשודים נעצרו בחשד למעורבות בפרשה.",
		"שלושה חשודים נוספים נעצרו בחשד למעורבות בפרשה.",
		"שלושה חשודים נעצרו בחיפה בחשד למעורבות בפרשה.",
		"שלושת החשודים שוחררו למעצר בית.",
	} {
		m := merge(a, Answer{Story: "s1", Replace: replace, Insert: []AnswerInsert{{After: "s1.2", Kind: Development, Text: text, Sources: []string{"a1"}}}}, "s1", ids, now)
		if !m.Version || m.Stats != (MergeStats{Asked: 2, Replaced: 1, Inserted: 1}) || len(m.Story.Sentences) != 4 {
			t.Errorf("%q: %+v %v", text, m.Stats, notesOf(m))
		}
	}
	// Two inserted sentences with the one count: the first corrects the story's, the second is dropped.
	m := merge(a, Answer{Story: "s1", Insert: []AnswerInsert{
		{After: "s1.2", Kind: Detail, Text: "3 חשודים נעצרו בחשד למעורבות בפרשה.", Sources: []string{"a1"}},
		{After: "s1.3", Kind: Detail, Text: "בסך הכול נעצרו שלושה חשודים בחשד למעורבות בפרשה.", Sources: []string{"a1"}},
	}}, "s1", ids, now)
	if got := texts(m.Story.Sentences); !m.Version || got != corrected || m.Stats != (MergeStats{Asked: 2, Replaced: 1, Corrected: 1, Repeated: 1}) {
		t.Fatalf("%+v\n got %s\n%v", m.Stats, got, notesOf(m))
	}
	// A count as its number, where two sentences are compared.
	for text, want := range map[string]string{
		"שלושה חשודים נעצרו ב-3 בתים.":   "3 חשודים נעצרו 3 בתים",
		"ושלושת החשודים נחקרו ביום שני.": "3 החשודים נחקרו ביום שני",
		"שלוש עשרה מכוניות ו-5 משאיות.":  "שלוש עשרה מכוניות 5 משאיות",
	} {
		if got := numbered(text); got != want {
			t.Errorf("numbered(%q) = %q, want %q", text, got, want)
		}
	}
	if !strings.Contains(whyRepeated, "another sentence of the answer") {
		t.Fatal(whyRepeated)
	}
}
