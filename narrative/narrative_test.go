package narrative

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/store"
)

// fakeEmbedder gives each text its vector from vectors, and counts the calls.
type fakeEmbedder struct {
	vectors map[string][]float32
	calls   int
}

func (f *fakeEmbedder) Embed(text string) ([]float32, error) {
	f.calls++
	if v, ok := f.vectors[text]; ok {
		return Normalize(append([]float32{}, v...)), nil
	}
	return Normalize([]float32{0, 0, 0, 1}), nil
}

var day = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func TestAStoryIsItsTitleAndFirstTwoSentences(t *testing.T) {
	s := Story{Title: "גשם בצפון", Sentences: []string{"ירד גשם כבד.", " כבישים נסגרו. ", "שלישי לא נכנס."}}
	if got := s.Text(); got != "גשם בצפון. ירד גשם כבד. כבישים נסגרו." {
		t.Fatalf("%q", got)
	}
	if got := (Story{Title: "רק כותרת"}).Text(); got != "רק כותרת" {
		t.Fatalf("%q", got)
	}
}

func TestAThreadPointsToItsRecentStories(t *testing.T) {
	old := Member{At: day, Vector: []float32{1, 0}}
	recent := Member{At: day.Add(90 * 24 * time.Hour), Vector: []float32{0, 1}}
	v := ThreadVector([]Member{old, recent}, recent.At, HalfLife)
	if v[1] < 0.99 || v[0] > 0.15 {
		t.Fatalf("weighted %v", v)
	}
	// With no half-life every story counts the same.
	v = ThreadVector([]Member{old, recent}, recent.At, 0)
	if d := v[0] - v[1]; d > 1e-6 || d < -1e-6 {
		t.Fatalf("plain %v", v)
	}
	if ThreadVector(nil, day, HalfLife) != nil {
		t.Fatal("a thread without stories has a vector")
	}
}

func TestStoriesAreEmbeddedOnceUntilTheirTextChanges(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fake := &fakeEmbedder{vectors: map[string][]float32{"גשם": {1, 0, 0, 0}, "תקציב": {0, 1, 0, 0}, "גשם. עוד": {1, 0.1, 0, 0}}}
	ix := New(db, fake, "m")
	stories := []Story{{ID: "a", At: day, Title: "גשם"}, {ID: "b", At: day, Title: "תקציב"}, {ID: "c", At: day}}
	if n, err := ix.EmbedStories(ctx, stories); err != nil || n != 2 || fake.calls != 2 {
		t.Fatalf("%d embedded, %d calls, %v", n, fake.calls, err)
	}
	if n, _ := ix.EmbedStories(ctx, stories); n != 0 || fake.calls != 2 {
		t.Fatalf("again: %d, %d calls", n, fake.calls)
	}
	stories[0].Sentences = []string{"עוד"}
	if n, _ := ix.EmbedStories(ctx, stories); n != 1 || fake.calls != 3 {
		t.Fatalf("changed: %d, %d calls", n, fake.calls)
	}
	// A thread's vector is made once, and again when it gets a story.
	th := store.Thread{ID: "t", UpdatedAt: day}
	members := map[string][]string{"t": {"a", "b"}}
	if n, err := ix.UpdateThreads(ctx, []store.Thread{th}, members); err != nil || n != 1 {
		t.Fatalf("%d threads, %v", n, err)
	}
	if n, _ := ix.UpdateThreads(ctx, []store.Thread{th}, members); n != 0 {
		t.Fatalf("again %d", n)
	}
	v, _ := ix.Vector(ctx, store.KindStory, "a")
	near, err := ix.Nearest(ctx, store.KindThread, v, 3, time.Time{})
	if err != nil || len(near) != 1 || near[0].Ref != "t" {
		t.Fatalf("near %+v, %v", near, err)
	}
}

func TestTheEvalCountsAThreadListedAsAHit(t *testing.T) {
	at := func(d int) time.Time { return day.Add(time.Duration(d) * 24 * time.Hour) }
	all := []EvalStory{
		{ID: "w1", At: at(0), Vector: Normalize([]float32{1, 0, 0}), Threads: []string{"weather"}},
		{ID: "b1", At: at(1), Vector: Normalize([]float32{0, 1, 0}), Threads: []string{"budget"}},
		{ID: "x", At: at(2), Vector: Normalize([]float32{0, 0, 1})},
		{ID: "w2", At: at(30), Vector: Normalize([]float32{1, 0.1, 0}), Threads: []string{"weather"}},
		// Its thread is far in meaning: missed by a shortlist of one, listed as one of the latest.
		{ID: "b2", At: at(40), Vector: Normalize([]float32{1, 0, 0.2}), Threads: []string{"budget"}},
	}
	results := Evaluate(all, 10, 1, 2, []time.Duration{0})
	byName := map[string]EvalResult{}
	for _, r := range results {
		byName[r.Name] = r
	}
	plain := byName["threads (half-life none)"]
	if plain.Tested != 2 || plain.Hits != 1 || plain.Listed != 1 {
		t.Fatalf("threads %+v", plain)
	}
	if all := results[len(results)-1]; all.Hits != 2 || all.Listed != 2 {
		t.Fatalf("with the latest %+v", all)
	}
}
