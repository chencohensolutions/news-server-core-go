package stories

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
)

// fakeEmbedder gives each text a fixed vector and counts the calls; unknown texts fail.
type fakeEmbedder struct {
	vectors map[string][]float32
	calls   int
	failAt  int // the call that fails, from 1; 0: none
}

func (e *fakeEmbedder) Embed(text string) ([]float32, error) {
	e.calls++
	if e.calls == e.failAt {
		return nil, errors.New("model failed")
	}
	v, ok := e.vectors[text]
	if !ok {
		return nil, errors.New("unknown text " + text)
	}
	return unitOf(v...), nil
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func report(site, url, title, at string) scrape.ArticleSummary {
	return scrape.ArticleSummary{SourceID: site, URL: url, Title: title, PublishedAt: at}
}

var feedVectors = map[string][]float32{
	"rain in the north":         {1, 0, 0},
	"heavy rain up north":       {1, 0.1, 0},
	"stock jumps 5%":            {0, 1, 0},
	"stock jumps 7%":            {0, 1, 0.05},
	"elections called":          {0, 0, 1},
	"heavy rain up north. more": {1, 0.12, 0},
}

func storyURLs(stories []Story) [][]string {
	var out [][]string
	for _, s := range stories {
		var urls []string
		for _, r := range s.Reports {
			urls = append(urls, r.URL)
		}
		out = append(out, urls)
	}
	return out
}

func TestTheFinderGroupsSitesReportsOfOneEvent(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	embedder := &fakeEmbedder{vectors: feedVectors}
	finder := NewFinder(embedder, db, E5Small, TitleAndSubtitle)
	feed := []scrape.ArticleSummary{
		report("siteh", "y1", "rain in the north", "2026-11-10T08:00:00Z"),
		report("sitee", "n1", "heavy rain up north", "2026-11-10T09:00:00Z"),
		// One site's look-alikes are not one event.
		report("biz", "b1", "stock jumps 5%", "2026-11-10T07:00:00Z"),
		report("biz", "b2", "stock jumps 7%", "2026-11-10T07:30:00Z"),
		report("siteh", "y2", "elections called", ""),
		report("siteh", "y1", "rain in the north", "2026-11-10T08:00:00Z"),
	}
	stories, err := finder.Find(ctx, feed, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	// The newest report leads.
	if got, want := storyURLs(stories), [][]string{{"n1", "y1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stories %v, want %v", got, want)
	}
	if embedder.calls != 5 {
		t.Fatalf("embedded %d texts", embedder.calls)
	}
	// Again: nothing to embed.
	if _, err := finder.Find(ctx, feed, 0.9); err != nil || finder.LastEmbedded != 0 || embedder.calls != 5 {
		t.Fatalf("again: err %v, embedded %d", err, finder.LastEmbedded)
	}
	// A new process: the vectors come from the database.
	restarted := NewFinder(embedder, db, E5Small, TitleAndSubtitle)
	if again, err := restarted.Find(ctx, feed, 0.9); err != nil || embedder.calls != 5 || !reflect.DeepEqual(storyURLs(again), storyURLs(stories)) {
		t.Fatalf("restarted: err %v, calls %d, %v", err, embedder.calls, storyURLs(again))
	}
	// Another model or text has its own vectors.
	if other, _ := db.Vectors(ctx, BGEM3.ID+"/"+TitleAndSubtitle.String()); len(other) != 0 {
		t.Fatalf("bge-m3 has %d vectors", len(other))
	}
}

func TestASubtitleThatArrivesIsEmbeddedAndTheOldVectorForgotten(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	embedder := &fakeEmbedder{vectors: feedVectors}
	finder := NewFinder(embedder, db, E5Small, TitleAndSubtitle)
	listed := report("sitee", "n1", "heavy rain up north", "")
	if _, err := finder.Find(ctx, []scrape.ArticleSummary{listed}, 0.9); err != nil {
		t.Fatal(err)
	}
	opened := listed
	opened.Subtitle = "more"
	if _, err := finder.Find(ctx, []scrape.ArticleSummary{opened}, 0.9); err != nil || finder.LastEmbedded != 1 {
		t.Fatalf("err %v, embedded %d", err, finder.LastEmbedded)
	}
	saved, _ := db.Vectors(ctx, finder.space)
	if len(saved) != 1 || saved["n1\nheavy rain up north. more"] == nil {
		t.Fatalf("saved %v", saved)
	}
}

func TestAFailedEmbeddingKeepsWhatWasDone(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	embedder := &fakeEmbedder{vectors: feedVectors, failAt: 3}
	finder := NewFinder(embedder, db, E5Small, Title)
	feed := []scrape.ArticleSummary{
		report("siteh", "y1", "rain in the north", ""),
		report("sitee", "n1", "heavy rain up north", ""),
		report("biz", "b1", "stock jumps 5%", ""),
	}
	if _, err := finder.Find(ctx, feed, 0.9); err == nil {
		t.Fatal("the model's error is returned")
	}
	if saved, _ := db.Vectors(ctx, finder.space); len(saved) != 2 {
		t.Fatalf("saved %d vectors", len(saved))
	}
	embedder.failAt = 0
	if _, err := finder.Find(ctx, feed, 0.9); err != nil || finder.LastEmbedded != 1 {
		t.Fatalf("err %v, embedded %d", err, finder.LastEmbedded)
	}
}

func TestAnUnrelatedReportMovesToTheClosestOtherStory(t *testing.T) {
	ctx := context.Background()
	vectors := map[string][]float32{
		"rain in the north":   {1, 0, 0},
		"heavy rain up north": {1, 0.1, 0},
		"rain and elections":  {0.8, 0, 0.65},
		"elections called":    {0, 0, 1},
		"elections set":       {0, 0.05, 1},
	}
	feed := []scrape.ArticleSummary{
		report("siteh", "y1", "rain in the north", "2026-11-10T08:00:00Z"),
		report("sitee", "n1", "heavy rain up north", "2026-11-10T09:00:00Z"),
		report("siteg", "w1", "rain and elections", "2026-11-10T07:00:00Z"),
		report("siteh", "y2", "elections called", "2026-11-10T06:00:00Z"),
		report("sitee", "n2", "elections set", "2026-11-10T05:00:00Z"),
	}
	flags := map[string]map[string]bool{"w1": {"y1": true, "n1": true}}
	for _, c := range []struct {
		threshold float32
		want      [][]string
	}{
		// Close enough to the elections story: it joins it.
		{0.6, [][]string{{"n1", "y1"}, {"w1", "y2", "n2"}}},
		// Not close enough to any other: in no story.
		{0.7, [][]string{{"n1", "y1"}, {"y2", "n2"}}},
	} {
		finder := NewFinder(&fakeEmbedder{vectors: vectors}, openStore(t), E5Small, TitleAndSubtitle)
		found, err := finder.Find(ctx, feed, c.threshold)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := storyURLs(found), [][]string{{"n1", "y1", "w1"}, {"y2", "n2"}}; !reflect.DeepEqual(got, want) {
			t.Fatalf("at %v grouped %v, want %v", c.threshold, got, want)
		}
		if got := storyURLs(finder.MoveUnrelated(found, feed, flags, c.threshold)); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("at %v moved %v, want %v", c.threshold, got, c.want)
		}
		// Flagged against the story it is in now: it isn't moved back.
		back := map[string]map[string]bool{"w1": {"y1": true, "n1": true, "y2": true}}
		if got := storyURLs(finder.MoveUnrelated(found, feed, back, c.threshold)); !reflect.DeepEqual(got, [][]string{{"n1", "y1"}, {"y2", "n2"}}) {
			t.Fatalf("at %v flagged twice: %v", c.threshold, got)
		}
	}
	// A story left with one site is no story.
	finder := NewFinder(&fakeEmbedder{vectors: vectors}, openStore(t), E5Small, TitleAndSubtitle)
	found, _ := finder.Find(ctx, feed[3:], 0.6)
	if got := finder.MoveUnrelated(found, feed[3:], map[string]map[string]bool{"n2": {"y2": true}}, 0.6); len(got) != 0 {
		t.Fatalf("one site left: %v", storyURLs(got))
	}
}
