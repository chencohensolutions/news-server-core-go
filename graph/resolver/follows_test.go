package resolver

import (
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/summary"
)

func TestFollowUpdatesAreWhatCameAfterFollowing(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 19, h, 0, 0, 0, time.UTC) }
	say := func(texts ...string) []summary.Sentence {
		var out []summary.Sentence
		for _, s := range texts {
			out = append(out, summary.Sentence{Text: s})
		}
		return out
	}
	followed := summary.StorySummary{ID: "f", Title: "הסערה", Updates: []summary.Update{
		{At: at(8), Sentences: say("א", "ב")},
		{At: at(11), Sentences: say("ג", "ד", "ה", "ו"), Kinds: []string{"development", "detail", "detail", "reaction"}},
	}}
	joined := summary.StorySummary{ID: "j", Title: "אחרי הסערה", Updates: []summary.Update{
		{At: at(12), Sentences: say("ז", "ח", "ט")},
		{At: at(13), Sentences: say("י")},
	}}
	before := summary.StorySummary{ID: "b", Title: "לפני", WrittenAt: at(9), Sentences: say("כ")}

	got := followUpdates("f", at(10), []summary.StorySummary{followed, joined, before})
	type row struct {
		story string
		isNew bool
		texts string
		kinds int
	}
	var rows []row
	for _, u := range got {
		texts := ""
		for _, s := range u.Sentences {
			texts += s.Text
		}
		rows = append(rows, row{u.Story.ID, u.NewStory, texts, len(u.Kinds)})
	}
	want := []row{{"j", false, "י", 0}, {"j", true, "זח", 0}, {"f", false, "גדה", 3}}
	if len(rows) != len(want) {
		t.Fatalf("updates: %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("update %d: %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestAFollowedStoryOwnFirstVersionIsNoUpdate(t *testing.T) {
	at := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	s := summary.StorySummary{ID: "f", WrittenAt: at, Sentences: []summary.Sentence{{Text: "א"}}}
	if got := followUpdates("f", at.Add(-time.Hour), []summary.StorySummary{s}); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}
