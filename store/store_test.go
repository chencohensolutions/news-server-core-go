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
