package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/graph/resolver"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/sites"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// The news fields over real mTLS, from a real database in the test's folder.

func seed(t *testing.T, env *testEnv) (morning, noon time.Time) {
	t.Helper()
	ctx := context.Background()
	morning = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	noon = morning.Add(4 * time.Hour)
	rain := scrape.ArticleSummary{SourceID: "siteh", URL: "https://siteh/1", Title: "Rain", PublishedAt: "2026-11-10T07:30:00Z"}
	vote := scrape.ArticleSummary{SourceID: "sitee", URL: "https://sitee/1", Title: "Vote", Summary: "The list's summary"}
	if err := env.db.SaveHeadlines(ctx, []scrape.ArticleSummary{rain}, morning); err != nil {
		t.Fatal(err)
	}
	if err := env.db.SaveHeadlines(ctx, []scrape.ArticleSummary{vote}, noon); err != nil {
		t.Fatal(err)
	}
	article := &scrape.Article{SourceID: "siteh", URL: rain.URL, Title: "Rain all day", Subtitle: "In the north",
		Body: []string{"First.", "Second."}, Blocks: []scrape.Block{{Type: "text", Text: "First."}, {Type: "image", URL: "https://img/1.jpg"}},
		Video: &scrape.ArticleVideo{Stream: "https://v/1.m3u8"}}
	if err := env.db.SaveArticle(ctx, article, "<html/>", morning); err != nil {
		t.Fatal(err)
	}
	if err := env.db.UpdateHeadline(ctx, rain.FilledFrom(article)); err != nil {
		t.Fatal(err)
	}
	return morning, noon
}

func mustQuery(t *testing.T, env *testEnv, query string, variables map[string]any) gqlResponse {
	t.Helper()
	cert, _ := env.enrollDevice(t, "hen")
	status, resp := env.query(t, cert, query, variables)
	if status != http.StatusOK || len(resp.Errors) > 0 {
		t.Fatalf("%d %+v", status, resp.Errors)
	}
	return resp
}

func TestTheAppReadsTheHeadlinesAndWhatChanged(t *testing.T) {
	env := newTestEnv(t)
	morning, noon := seed(t, env)
	// The siteh headline as its article filled it in: its title, subtitle and video.
	resp := mustQuery(t, env, `{ headlines { sourceId url title summary subtitle publishedAt video firstSeen lastSeen } }`, nil)
	want := fmt.Sprintf(`[{"sourceId":"sitee","url":"https://sitee/1","title":"Vote","summary":"The list's summary","subtitle":null,"publishedAt":null,"video":false,"firstSeen":%q,"lastSeen":%q},`+
		`{"sourceId":"siteh","url":"https://siteh/1","title":"Rain all day","summary":null,"subtitle":"In the north","publishedAt":"2026-11-10T07:30:00Z","video":true,"firstSeen":%q,"lastSeen":%q}]`,
		noon.Format(time.RFC3339), noon.Format(time.RFC3339), morning.Format(time.RFC3339), morning.Format(time.RFC3339))
	if got := string(resp.Data["headlines"]); got != want {
		t.Fatalf("headlines\n got %s\nwant %s", got, want)
	}
	// Since the morning's: what changed after it.
	resp = mustQuery(t, env, `query($since: Time) { headlines(since: $since) { url } }`, map[string]any{"since": morning.Add(time.Minute)})
	if got := string(resp.Data["headlines"]); got != `[{"url":"https://sitee/1"}]` {
		t.Fatalf("since: %s", got)
	}
	// One site.
	resp = mustQuery(t, env, `{ headlines(sourceIds: ["siteh"]) { url } }`, nil)
	if got := string(resp.Data["headlines"]); got != `[{"url":"https://siteh/1"}]` {
		t.Fatalf("siteh: %s", got)
	}
}

func TestTheAppReadsTheArticlesTheServerHas(t *testing.T) {
	env := newTestEnv(t)
	seed(t, env)
	resp := mustQuery(t, env, `{ article(url: "https://siteh/1") { title subtitle body blocks { type text url caption } video { stream embed } }
		missing: article(url: "https://sitee/1") { title }
		articles(urls: ["https://sitee/1", "https://siteh/1"]) { url } }`, nil)
	want := `{"title":"Rain all day","subtitle":"In the north","body":["First.","Second."],"blocks":[{"type":"text","text":"First.","url":null,"caption":null},{"type":"image","text":null,"url":"https://img/1.jpg","caption":null}],"video":{"stream":"https://v/1.m3u8","embed":null}}`
	if got := string(resp.Data["article"]); got != want {
		t.Fatalf("article\n got %s\nwant %s", got, want)
	}
	if got := string(resp.Data["missing"]); got != "null" {
		t.Fatalf("missing: %s", got)
	}
	if got := string(resp.Data["articles"]); got != `[{"url":"https://siteh/1"}]` {
		t.Fatalf("articles: %s", got)
	}
	// Too many at once: refused with a code.
	urls := make([]string, 201)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://x/%d", i)
	}
	cert, _ := env.enrollDevice(t, "hen")
	_, bad := env.query(t, cert, `query($urls: [String!]!) { articles(urls: $urls) { url } }`, map[string]any{"urls": urls})
	if len(bad.Errors) != 1 || bad.Errors[0].Extensions["code"] != "BAD_REQUEST" {
		t.Fatalf("%+v", bad.Errors)
	}
}

func TestTheAppReadsTheStories(t *testing.T) {
	env := newTestEnv(t)
	morning, _ := seed(t, env)
	// Off: none, and serverInfo says so.
	resp := mustQuery(t, env, `{ stories { groupedAt stories { reports { url } } } serverInfo { stories } }`, nil)
	if got := string(resp.Data["stories"]); got != `{"groupedAt":null,"stories":[]}` {
		t.Fatalf("off: %s", got)
	}
	grouped := morning.Add(5 * time.Hour)
	reports := []scrape.ArticleSummary{
		{SourceID: "siteh", URL: "https://siteh/1", Title: "Rain all day"},
		{SourceID: "gone", URL: "https://gone/1", Title: "Pruned since"},
	}
	env.api.Stories = func() ([]stories.Story, time.Time) { return []stories.Story{{Reports: reports}}, grouped }
	resp = mustQuery(t, env, `{ stories { groupedAt stories { reports { url firstSeen } } } serverInfo { stories } }`, nil)
	want := fmt.Sprintf(`{"groupedAt":%q,"stories":[{"reports":[{"url":"https://siteh/1","firstSeen":%q},{"url":"https://gone/1","firstSeen":%q}]}]}`,
		grouped.Format(time.RFC3339), morning.Format(time.RFC3339), grouped.Format(time.RFC3339))
	if got := string(resp.Data["stories"]); got != want {
		t.Fatalf("stories\n got %s\nwant %s", got, want)
	}
	var info struct{ Stories bool }
	json.Unmarshal(resp.Data["serverInfo"], &info)
	if !info.Stories {
		t.Fatal("serverInfo.stories is false")
	}
}

func TestTheNewsNeedsACertificate(t *testing.T) {
	env := newTestEnv(t)
	for _, q := range []string{`{ headlines { url } }`, `{ article(url: "x") { url } }`, `{ stories { groupedAt } }`} {
		_, resp := env.query(t, nil, q, nil)
		if len(resp.Errors) != 1 || resp.Errors[0].Extensions["code"] != "UNAUTHORIZED" {
			t.Fatalf("%s: %+v", q, resp.Errors)
		}
	}
}

func TestTheAppSetsTheServersSites(t *testing.T) {
	env := newTestEnv(t)
	seed(t, env)
	resp := mustQuery(t, env, `{ sources { id enabled hasFeed feed waitMs } }`, nil)
	if got := string(resp.Data["sources"]); got != `[{"id":"siteh","enabled":true,"hasFeed":false,"feed":false,"waitMs":250}]` {
		t.Fatalf("sources: %s", got)
	}
	// Switched off, with a longer wait: its headlines and stories go at once, and the server keeps the choice.
	resp = mustQuery(t, env, `mutation { setSource(id: "siteh", enabled: false, waitMs: 1000) { id enabled waitMs } }`, nil)
	if got := string(resp.Data["setSource"]); got != `{"id":"siteh","enabled":false,"waitMs":1000}` {
		t.Fatalf("setSource: %s", got)
	}
	resp = mustQuery(t, env, `{ headlines { url } }`, nil)
	if got := string(resp.Data["headlines"]); got != `[{"url":"https://sitee/1"}]` {
		t.Fatalf("headlines: %s", got)
	}
	reopened, err := sites.NewSettings(context.Background(), env.db, env.api.Sites, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get("siteh"); got != (sites.Setting{Enabled: false, WaitMs: 1000}) {
		t.Fatalf("after a restart: %+v", got)
	}
	// What it can't take.
	cert, _ := env.enrollDevice(t, "hen")
	for _, mutation := range []string{
		`mutation { setSource(id: "nope", enabled: true) { id } }`,
		`mutation { setSource(id: "siteh", feed: true) { id } }`,
		`mutation { setSource(id: "siteh", waitMs: 60001) { id } }`,
	} {
		_, resp := env.query(t, cert, mutation, nil)
		if len(resp.Errors) != 1 || resp.Errors[0].Extensions["code"] != resolver.CodeBadRequest {
			t.Fatalf("%s: %+v", mutation, resp.Errors)
		}
	}
}
