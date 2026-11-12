package backup

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
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

func TestTheArchiveKeepsRowsOfOneMillisecondPastABatch(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	db := open(t, filepath.Join(tmp, "news.db"))
	at := time.Date(2026, 11, 10, 22, 55, 0, 0, time.UTC)
	// A read's headlines are saved at one time; more of them than a batch.
	var items []scrape.ArticleSummary
	for i := range batch + 500 {
		items = append(items, scrape.ArticleSummary{SourceID: "sitec", URL: fmt.Sprintf("https://sitec/%d", i), Title: "כותרת"})
	}
	if err := db.SaveHeadlines(ctx, items, at); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "archive")
	if _, err := Archive(ctx, db, dir, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	restored := open(t, filepath.Join(tmp, "restored.db"))
	result, err := Restore(ctx, restored, dir, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(dump(t, restored)["headlines"]); got != len(items) {
		t.Fatalf("%d of %d headlines restored (%d lines)", got, len(items), result.Rows["headlines"])
	}
}

func TestTheArchiveKeepsTheIncrementalShadow(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	db := open(t, filepath.Join(tmp, "news.db"))
	dir := filepath.Join(tmp, "archive")
	day1 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	db.SaveShadow(ctx, "k1", "s1", json.RawMessage(`{"id":"s1","key":"k1","plan":"incremental","version":true}`), day1)
	db.SaveShadow(ctx, "k2", "s1", json.RawMessage(`{"id":"s1","key":"k2","plan":"local","version":false}`), day1.Add(24*time.Hour))
	result, err := Archive(ctx, db, dir, day1.Add(48*time.Hour))
	if err != nil || result.Rows["story_shadow"] != 2 {
		t.Fatalf("%v %v", result, err)
	}
	// By the day written, like the versions; and read back without a database, as the replay reads the archive.
	if _, err := os.Stat(filepath.Join(dir, "story_shadow/2026-11/2026-11-11.jsonl")); err != nil {
		t.Fatal(err)
	}
	var keys []string
	if err := Read(dir, "story_shadow", day1.Add(24*time.Hour), func(row struct {
		Key    string          `json:"key"`
		Shadow json.RawMessage `json:"shadow"`
	}) error {
		keys = append(keys, row.Key+" "+string(row.Shadow))
		return nil
	}); err != nil || len(keys) != 1 || keys[0] != `k2 {"id":"s1","key":"k2","plan":"local","version":false}` {
		t.Fatalf("%v %v", keys, err)
	}
	if last, ok := LastDay(dir, "story_shadow"); !ok || last.Format(time.DateOnly) != "2026-11-11" {
		t.Fatalf("%v %v", last, ok)
	}
	restored := open(t, filepath.Join(tmp, "restored.db"))
	if got, err := Restore(ctx, restored, dir, t.Logf); err != nil || got.Rows["story_shadow"] != 2 {
		t.Fatalf("%v %v", got, err)
	}
	rows := func(db *store.Store) (out []string) {
		db.Rows(ctx, "story_shadow", "", 0, 0, 100, func(row store.Row) error {
			data, _ := json.Marshal(row)
			out = append(out, string(data))
			return nil
		})
		return out
	}
	if want, got := rows(db), rows(restored); len(got) != 2 || !reflect.DeepEqual(want, got) {
		t.Fatalf("restored\n got %v\nwant %v", got, want)
	}
	// The database's own rows grow old and go; the archive's stay.
	if err := db.PruneShadows(ctx, day1.Add(time.Hour)); err != nil || len(rows(db)) != 1 {
		t.Fatalf("%v %v", rows(db), err)
	}
	if refs, err := db.ShadowRefs(ctx, day1); err != nil || len(refs) != 1 || refs[0].Key != "k2" || refs[0].ID != "s1" {
		t.Fatalf("%+v %v", refs, err)
	}
}
