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
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/stories"
	"github.com/chencohensolutions/news-server-core-go/summary"
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
	for _, q := range []string{`{ headlines { url } }`, `{ article(url: "x") { url } }`, `{ stories { groupedAt } }`, `{ digest { title } }`} {
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

func TestTheAppReadsTheGeneratedArticles(t *testing.T) {
	env := newTestEnv(t)
	_, noon := seed(t, env)
	ctx := context.Background()
	resp := mustQuery(t, env, `{ digest { title } serverInfo { summaries } }`, nil)
	if got := string(resp.Data["digest"]); got != "null" {
		t.Fatalf("before the first: %s", got)
	}
	story := stories.Story{Reports: []scrape.ArticleSummary{
		{SourceID: "siteh", URL: "https://siteh/1", Title: "Rain all day"},
		{SourceID: "sitee", URL: "https://sitee/1", Title: "Vote"},
	}}
	written := summary.StorySummary{Title: "גשם", WrittenAt: noon,
		Sentences:      []summary.Sentence{{Text: "ירד גשם.", Sources: []string{"https://siteh/1", "https://gone/1"}}},
		Contradictions: []summary.Sentence{}}
	data, _ := json.Marshal(written)
	if err := env.db.SaveSummary(ctx, summary.StoryKey(story), data, noon); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(summary.Digest{Title: "השעה", WrittenAt: noon, Sentences: []summary.Sentence{{Text: "הצבעה.", Sources: []string{"https://sitee/1"}}}})
	if err := env.db.SaveDigest(ctx, data, noon); err != nil {
		t.Fatal(err)
	}
	env.api.Stories = func() ([]stories.Story, time.Time) { return []stories.Story{story}, noon }
	env.api.Summaries = true
	resp = mustQuery(t, env, `{
		stories { stories { summary { title sentences { text footnotes { url sourceId title video } } contradictions { text } writtenAt updates { at sentences { text } } } } }
		digest { title sentences { text footnotes { url sourceId } } writtenAt }
		serverInfo { summaries } }`, nil)
	want := fmt.Sprintf(`{"stories":[{"summary":{"title":"גשם","sentences":[{"text":"ירד גשם.","footnotes":[`+
		`{"url":"https://siteh/1","sourceId":"siteh","title":"Rain all day","video":true},{"url":"https://gone/1","sourceId":null,"title":null,"video":false}]}],`+
		`"contradictions":[],"writtenAt":%q,"updates":[{"at":%[1]q,"sentences":[{"text":"ירד גשם."}]}]}}]}`, noon.Format(time.RFC3339))
	if got := string(resp.Data["stories"]); got != want {
		t.Fatalf("stories\n got %s\nwant %s", got, want)
	}
	want = fmt.Sprintf(`{"title":"השעה","sentences":[{"text":"הצבעה.","footnotes":[{"url":"https://sitee/1","sourceId":"sitee"}]}],"writtenAt":%q}`, noon.Format(time.RFC3339))
	if got := string(resp.Data["digest"]); got != want {
		t.Fatalf("digest\n got %s\nwant %s", got, want)
	}
	if got := string(resp.Data["serverInfo"]); got != `{"summaries":true}` {
		t.Fatalf("serverInfo %s", got)
	}
}

func TestTheAppReadsTheStatistics(t *testing.T) {
	env := newTestEnv(t)
	_, noon := seed(t, env)
	ctx := context.Background()
	if err := env.db.SaveSiteRun(ctx, "siteh", 12, nil, noon); err != nil {
		t.Fatal(err)
	}
	resp := mustQuery(t, env, `{ stats { headlines articles stories groupedAt summaries gemini { model }
		sites { sourceId enabled lastRunAt lastOkAt headlines error } } }`, nil)
	want := fmt.Sprintf(`{"headlines":2,"articles":1,"stories":0,"groupedAt":null,"summaries":0,"gemini":null,`+
		`"sites":[{"sourceId":"siteh","enabled":true,"lastRunAt":%q,"lastOkAt":%q,"headlines":12,"error":null}]}`,
		noon.Format(time.RFC3339), noon.Format(time.RFC3339))
	if got := string(resp.Data["stats"]); got != want {
		t.Fatalf("off\n got %s\nwant %s", got, want)
	}
	day := summary.Day(time.Now())
	env.db.CountGeminiRequest(ctx, day)
	env.db.CountGeminiTokens(ctx, day, store.Tokens{In: 15000, Cached: 4000, Out: 300, Thoughts: 50})
	env.api.Gemini = &resolver.GeminiInfo{Model: "gemini-3.5-flash-lite", Budget: 400,
		Status: func() summary.Status { return summary.Status{At: noon, Result: summary.Result{Written: 3, Waiting: 9}} }}
	resp = mustQuery(t, env, `{ stats { gemini { model budget today days { day requests tokensIn tokensCached tokensOut tokensThinking }
		lastRun { at written waiting digest error } } } }`, nil)
	want = fmt.Sprintf(`{"gemini":{"model":"gemini-3.5-flash-lite","budget":400,"today":%q,`+
		`"days":[{"day":%q,"requests":1,"tokensIn":15000,"tokensCached":4000,"tokensOut":300,"tokensThinking":50}],`+
		`"lastRun":{"at":%q,"written":3,"waiting":9,"digest":false,"error":null}}}`, day, day, noon.Format(time.RFC3339))
	if got := string(resp.Data["stats"]); got != want {
		t.Fatalf("on\n got %s\nwant %s", got, want)
	}
}

// anyText embeds every text as one vector: the stories are anything, what counts here is that they are grouped.
type anyText struct{}

func (anyText) Embed(string) ([]float32, error) { return []float32{1, 0}, nil }
func (anyText) Close()                          {}

func TestTheAppSetsTheServersGrouping(t *testing.T) {
	env := newTestEnv(t)
	seed(t, env)
	// Stories off: no grouping, and nothing to set.
	resp := mustQuery(t, env, `{ grouping { model } }`, nil)
	if got := string(resp.Data["grouping"]); got != `null` {
		t.Fatalf("off: %s", got)
	}
	cert, _ := env.enrollDevice(t, "hen")
	if _, resp := env.query(t, cert, `mutation { setGrouping(strictness: 50) { model } }`, nil); len(resp.Errors) != 1 || resp.Errors[0].Extensions["code"] != resolver.CodeBadRequest {
		t.Fatalf("off: %+v", resp.Errors)
	}
	config := stories.Grouping{Text: stories.TitleAndSubtitle, Strictness: 60, Days: 7}
	job := &storyJob{db: env.db, embedder: anyText{}, model: stories.BGEM3, grouping: config}
	env.api.Grouping, env.api.StoryModel = job, stories.BGEM3.ID
	resp = mustQuery(t, env, `{ grouping { method geminiAvailable model text strictness days threshold } }`, nil)
	if got := string(resp.Data["grouping"]); got != `{"method":"MODEL","geminiAvailable":false,"model":"bge-m3","text":"TITLE_AND_SUBTITLE","strictness":60,"days":7,"threshold":0.6625}` {
		t.Fatalf("grouping: %s", got)
	}
	// The app's slider and text: kept, and the stories grouped again at once.
	resp = mustQuery(t, env, `mutation { setGrouping(strictness: 30, text: TITLE) { text strictness days } }`, nil)
	if got := string(resp.Data["setGrouping"]); got != `{"text":"TITLE","strictness":30,"days":7}` {
		t.Fatalf("setGrouping: %s", got)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, at := job.Stories(); !at.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not grouped again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.finder.Text() != stories.Title {
		t.Fatalf("grouped by %s", job.finder.Text())
	}
	// After a restart, the app's choice, not config.json's.
	if got, err := savedGrouping(env.db, config); err != nil || got.Text != stories.Title || got.Strictness != 30 || got.Days != 7 {
		t.Fatalf("after a restart: %+v, %v", got, err)
	}
	// What it can't take.
	for _, mutation := range []string{
		`mutation { setGrouping(strictness: 95) { days } }`,
		`mutation { setGrouping(days: -1) { days } }`,
		`mutation { setGrouping(days: 31) { days } }`,
		// No Gemini key in the test.
		`mutation { setGrouping(method: GEMINI) { days } }`,
	} {
		_, resp := env.query(t, cert, mutation, nil)
		if len(resp.Errors) != 1 || resp.Errors[0].Extensions["code"] != resolver.CodeBadRequest {
			t.Fatalf("%s: %+v", mutation, resp.Errors)
		}
	}
}

func TestTheAppReadsTheThreads(t *testing.T) {
	env := newTestEnv(t)
	seed(t, env)
	ctx := context.Background()
	noon := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	story := stories.Story{Reports: []scrape.ArticleSummary{
		{SourceID: "siteh", URL: "https://siteh/1", Title: "Rain all day"},
		{SourceID: "sitee", URL: "https://sitee/1", Title: "Vote"},
	}}
	// Written before ids: its key is its id.
	data, _ := json.Marshal(summary.StorySummary{Title: "גשם", WrittenAt: noon,
		Sentences: []summary.Sentence{{Text: "ירד גשם.", Sources: []string{"https://siteh/1"}}}})
	id := summary.StoryKey(story)
	if err := env.db.SaveSummary(ctx, id, data, noon); err != nil {
		t.Fatal(err)
	}
	if err := env.db.SaveStoryRecord(ctx, id, data, noon); err != nil {
		t.Fatal(err)
	}
	for _, th := range []store.Thread{
		{ID: "th1", Name: "החורף", Summary: "גשם בצפון.", StartedAt: noon, UpdatedAt: noon},
		{ID: "th2", Name: "הכבישים", Summary: "כבישים נסגרו.", StartedAt: noon, UpdatedAt: noon.Add(time.Hour)},
	} {
		if err := env.db.SaveThread(ctx, th); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.db.LinkStory(ctx, id, []string{"th1", "th2"}); err != nil {
		t.Fatal(err)
	}
	if err := env.db.RelateThreads(ctx, "th1", "th2"); err != nil {
		t.Fatal(err)
	}
	env.api.Stories = func() ([]stories.Story, time.Time) { return []stories.Story{story}, noon }
	resp := mustQuery(t, env, `{
		stories { stories { summary { threads { id name storyCount } } } }
		threads { id }
		thread(id: "th1") { name summary startedAt stories { title sentences { text footnotes { title } } firstWrittenAt firstReportAt } related { name } }
		none: thread(id: "nope") { id } }`, nil)
	if got := string(resp.Data["stories"]); got != `{"stories":[{"summary":{"threads":[{"id":"th2","name":"הכבישים","storyCount":1},{"id":"th1","name":"החורף","storyCount":1}]}}]}` {
		t.Fatalf("stories %s", got)
	}
	if got := string(resp.Data["threads"]); got != `[{"id":"th2"},{"id":"th1"}]` {
		t.Fatalf("threads %s", got)
	}
	want := fmt.Sprintf(`{"name":"החורף","summary":"גשם בצפון.","startedAt":%q,"stories":[{"title":"גשם","sentences":[{"text":"ירד גשם.","footnotes":[{"title":"Rain all day"}]}],"firstWrittenAt":%[1]q,"firstReportAt":"2026-11-10T07:30:00Z"}],"related":[{"name":"הכבישים"}]}`, noon.Format(time.RFC3339))
	if got := string(resp.Data["thread"]); got != want {
		t.Fatalf("thread\n got %s\nwant %s", got, want)
	}
	if got := string(resp.Data["none"]); got != "null" {
		t.Fatalf("none %s", got)
	}
}

func TestTheAppChangesTheSummarySettings(t *testing.T) {
	env := newTestEnv(t)
	resp := mustQuery(t, env, `{ summarySettings { fullArticles } }`, nil)
	if got := string(resp.Data["summarySettings"]); got != "null" {
		t.Fatalf("summaries off: %s", got)
	}
	cert, _ := env.enrollDevice(t, "hen")
	_, resp = env.query(t, cert, `mutation { setSummarySettings(fullArticles: false) { fullArticles } }`, nil)
	if len(resp.Errors) != 1 || resp.Errors[0].Extensions["code"] != resolver.CodeBadRequest {
		t.Fatalf("summaries off: %+v", resp.Errors)
	}
	env.api.Summaries = true
	resp = mustQuery(t, env, `{ summarySettings { fullArticles } }`, nil)
	resp = mustQuery(t, env, `{ summarySettings { fullArticles length } }`, nil)
	if got := string(resp.Data["summarySettings"]); got != `{"fullArticles":true,"length":"MEDIUM"}` {
		t.Fatalf("default: %s", got)
	}
	resp = mustQuery(t, env, `mutation { setSummarySettings(fullArticles: false, length: FULL) { fullArticles length } }`, nil)
	if got := string(resp.Data["setSummarySettings"]); got != `{"fullArticles":false,"length":"FULL"}` {
		t.Fatalf("set: %s %+v", got, resp.Errors)
	}
	settings, err := summary.LoadSettings(context.Background(), env.db)
	if err != nil || settings.FullArticles || settings.Length != "full" {
		t.Fatalf("saved: %+v %v", settings, err)
	}
}
