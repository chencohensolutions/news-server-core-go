package backup

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
)

func open(t *testing.T, path string) *store.Store {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// dump is every archived table's rows, as JSON, for comparing two databases.
func dump(t *testing.T, db *store.Store) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range append(append([]string{}, whole...), "headlines", "articles", "summaries", "story_records", "digests", "unrelated", "gemini_groups") {
		db.Rows(context.Background(), table, "", 0, 0, 1_000_000, func(row store.Row) error {
			data, _ := json.Marshal(row)
			out[table] = append(out[table], string(data))
			return nil
		})
	}
	return out
}

func TestTheArchiveRestoresTheDatabase(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	db := open(t, filepath.Join(tmp, "news.db"))
	dir := filepath.Join(tmp, "archive")
	day1 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)

	h := scrape.ArticleSummary{SourceID: "siteh", URL: "https://siteh/1", Title: "גשם בצפון"}
	db.SaveHeadlines(ctx, []scrape.ArticleSummary{h}, day1)
	db.SaveArticle(ctx, &scrape.Article{SourceID: "siteh", URL: h.URL, Title: h.Title, Body: []string{"ירד גשם."}}, "<html>page</html>", day1)
	db.SaveSummary(ctx, "k1", json.RawMessage(`{"title":"גשם","id":"s1"}`), day1)
	db.SaveStoryRecord(ctx, "s1", json.RawMessage(`{"title":"גשם","id":"s1"}`), day1)
	db.SaveDigest(ctx, json.RawMessage(`{"title":"השעה"}`), day1)
	db.SaveServerSetting(ctx, "summaries", json.RawMessage(`{"fullArticles":true,"length":"full"}`))
	db.SaveThread(ctx, store.Thread{ID: "t1", Name: "החורף", Summary: "גשם כבד.", StartedAt: day1, UpdatedAt: day1})
	db.LinkStory(ctx, "s1", []string{"t1"})

	result, err := Archive(ctx, db, dir, day1.Add(time.Hour))
	if err != nil || result.Rows["headlines"] != 1 || result.Rows["articles"] != 1 {
		t.Fatalf("%v %v", result, err)
	}
	// The next day: a new version of the story and a new digest are added; what was archived isn't again.
	db.SaveStoryRecord(ctx, "s1", json.RawMessage(`{"title":"גשם וברד","id":"s1"}`), day2)
	db.SaveDigest(ctx, json.RawMessage(`{"title":"היום"}`), day2)
	result, err = Archive(ctx, db, dir, day2.Add(time.Hour))
	if err != nil || result.Rows["headlines"] != 0 || result.Rows["story_records"] != 1 || result.Rows["digests"] != 1 {
		t.Fatalf("second: %v %v", result, err)
	}
	// Every version is kept in the files.
	for _, name := range []string{"story_records/2026-11/2026-11-10.jsonl", "story_records/2026-11/2026-11-11.jsonl", "digests/2026-11/2026-11-10.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Error(err)
		}
	}
	// A server stopped while adding: a line cut short at the end of a file.
	f, _ := os.OpenFile(filepath.Join(dir, "headlines/2026-11/2026-11-10.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"url":"https://cut`)
	f.Close()
	// And a gzip member cut short.
	f, _ = os.OpenFile(filepath.Join(dir, "articles/2026-11/2026-11-10.jsonl.gz"), os.O_APPEND|os.O_WRONLY, 0o644)
	zw := gzip.NewWriter(f)
	zw.Write([]byte(`{"url":"https://cut","sour`))
	zw.Flush()
	f.Close()

	restored := open(t, filepath.Join(tmp, "restored.db"))
	if _, err := Restore(ctx, restored, dir, t.Logf); err != nil {
		t.Fatal(err)
	}
	if want, got := dump(t, db), dump(t, restored); !reflect.DeepEqual(want, got) {
		t.Fatalf("restored\n got %v\nwant %v", got, want)
	}
}
