package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
)

func TestPruneForgetsHeadlinesNoLongerSeenWithTheirArticles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "news.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	old := scrape.ArticleSummary{SourceID: "a", URL: "u/old", Title: "Old"}
	fresh := scrape.ArticleSummary{SourceID: "a", URL: "u/new", Title: "New"}
	s.SaveHeadlines(ctx, []scrape.ArticleSummary{old}, day)
	s.SaveArticle(ctx, &scrape.Article{SourceID: "a", URL: "u/old", Title: "Old", Body: []string{"x"}}, "<p>x</p>", day)
	s.SaveHeadlines(ctx, []scrape.ArticleSummary{fresh}, day.Add(8*24*time.Hour))
	if n, err := s.Prune(ctx, day.Add(24*time.Hour)); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if has, _ := s.HasArticle(ctx, "u/old"); has {
		t.Fatal("article kept")
	}
	s.Close()
	// Reopened: the data is in the file.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	all, _ := s.Headlines(ctx, time.Time{})
	if len(all) != 1 || all[0].Title != "New" {
		t.Fatalf("%+v", all)
	}
}

func TestOpeningAnOlderDatabaseKeepsItsStoriesAsTheirFirstVersions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "news.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	s.SaveStoryRecord(ctx, "a", []byte(`{"title":"a2","id":"a"}`), day.Add(time.Hour))
	s.SaveSummary(ctx, "k1", []byte(`{"title":"a1","id":"a"}`), day)
	s.SaveSummary(ctx, "k2", []byte(`{"title":"b"}`), day) // written before ids: its key is its id
	// As a server before versions left it.
	if _, err := s.db.Exec(`DELETE FROM story_versions`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.StoryVersions(ctx, []string{"a", "k2", "none"})
	if err != nil || len(got) != 2 || len(got["a"]) != 2 || string(got["a"][0].Summary) != `{"title":"a1","id":"a"}` ||
		!got["a"][1].WrittenAt.Equal(day.Add(time.Hour)) || len(got["k2"]) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}
