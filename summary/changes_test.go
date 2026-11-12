package summary

import (
	"testing"
	"time"
)

func TestChangesMarkTheNewWordsAndSentences(t *testing.T) {
	before := StorySummary{Title: "גשם בצפון", WrittenAt: now, Sentences: []Sentence{
		{Text: "גשם כבד ירד הבוקר בצפון."},
		{Text: "שני כבישים נסגרו."},
		{Text: "המשטרה ביקשה לא לצאת לטיולים."},
	}}
	after := StorySummary{Title: "סערה בצפון", WrittenAt: now.Add(time.Hour), Sentences: []Sentence{
		{Text: "גשם כבד ירד הבוקר בצפון."},
		{Text: "שלושה כבישים נסגרו, ובהם כביש 90."},
		{Text: "אדם נפצע קל מעץ שנפל."},
	}}
	c := ChangesFrom(before, after)
	if !c.Since.Equal(now) || c.Title != "גשם בצפון" || len(c.Sentences) != 3 {
		t.Fatalf("%+v", c)
	}
	if s := c.Sentences[0]; s.Kind != Same || len(s.Parts) != 1 || s.Parts[0].Added {
		t.Fatalf("same %+v", s)
	}
	// Punctuation goes with its word.
	want := []Part{{"שלושה", true}, {" כבישים נסגרו, ", false}, {"ובהם כביש 90.", true}}
	if s := c.Sentences[1]; s.Kind != Changed || !equalParts(s.Parts, want) {
		t.Fatalf("changed %+v", s)
	}
	if s := c.Sentences[2]; s.Kind != New || len(s.Parts) != 1 || !s.Parts[0].Added || s.Parts[0].Text != after.Sentences[2].Text {
		t.Fatalf("new %+v", s)
	}
	if len(c.Removed) != 1 || c.Removed[0].Text != "המשטרה ביקשה לא לצאת לטיולים." {
		t.Fatalf("removed %+v", c.Removed)
	}
	// The parts are the whole sentence.
	for i, s := range c.Sentences {
		text := ""
		for _, p := range s.Parts {
			text += p.Text
		}
		if text != after.Sentences[i].Text {
			t.Fatalf("%q, want %q", text, after.Sentences[i].Text)
		}
	}
}

func equalParts(a, b []Part) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSimilarStoriesSayTheSame(t *testing.T) {
	old := []Sentence{{Text: "גשם כבד ירד הבוקר בצפון."}, {Text: "שני כבישים נסגרו."}}
	if !Similar([]Sentence{{Text: "גשם כבד ירד הבוקר בצפון הארץ."}, {Text: "שני כבישים נסגרו בגלל הגשם."}}, old) {
		t.Fatal("the same story")
	}
	if Similar([]Sentence{{Text: "הכנסת אישרה את התקציב."}, {Text: "שני כבישים נסגרו."}, {Text: "האופוזיציה הצביעה נגד."}}, old) {
		t.Fatal("another story")
	}
}
