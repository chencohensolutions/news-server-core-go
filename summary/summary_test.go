package summary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// fakeGemini answers generateContent with the recorded answers in testdata: the digest's for the front page's
// instructions, the stories' otherwise. It keeps every request.
type fakeGemini struct {
	mu       sync.Mutex
	requests []map[string]any
	prompts  []string
	status   int
	busy     int // answer 503 this many times first
}

func (f *fakeGemini) serve(t *testing.T) *gemini.Client {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "test-key" || !strings.HasSuffix(r.URL.Path, "/models/"+gemini.DefaultModel+":generateContent") {
			t.Errorf("request %s with key %q", r.URL.Path, r.Header.Get("x-goog-api-key"))
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.requests = append(f.requests, body)
		prompt := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
		f.prompts = append(f.prompts, prompt)
		status := f.status
		if f.busy > 0 {
			f.busy--
			status = http.StatusServiceUnavailable
		}
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			w.Write([]byte(`{"error": {"message": "Resource has been exhausted"}}`))
			return
		}
		system, _ := json.Marshal(body["systemInstruction"])
		name := "stories-answer.json"
		if strings.Contains(string(system), "front page") {
			name = "digest-answer.json"
		}
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Error(err)
		}
		w.Write(data)
	}))
	t.Cleanup(server.Close)
	return &gemini.Client{Key: "test-key", BaseURL: server.URL}
}

func (f *fakeGemini) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

var now = time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)

var (
	rainSiteh   = scrape.ArticleSummary{SourceID: "siteh", URL: "https://siteh/rain", Title: "גשם בצפון", Subtitle: "שני כבישים נסגרו"}
	rainSiteE   = scrape.ArticleSummary{SourceID: "sitee", URL: "https://sitee/rain", Title: "סערה בצפון"}
	budgetSited = scrape.ArticleSummary{SourceID: "sited", URL: "https://sited/budget", Title: "התקציב אושר"}
	budgetSiteg = scrape.ArticleSummary{SourceID: "siteg", URL: "https://siteg/budget", Title: "הכנסת אישרה את התקציב"}
	alone       = scrape.ArticleSummary{SourceID: "siteh", URL: "https://siteh/alone", Title: "ידיעה בודדת"}
	old         = scrape.ArticleSummary{SourceID: "siteh", URL: "https://siteh/old", Title: "ישן"}
	oldToo      = scrape.ArticleSummary{SourceID: "sitee", URL: "https://sitee/old", Title: "ישן גם"}
)

func setup(t *testing.T) (*Writer, *fakeGemini, []stories.Story) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	save := func(at time.Time, items ...scrape.ArticleSummary) {
		if err := db.SaveHeadlines(ctx, items, at); err != nil {
			t.Fatal(err)
		}
	}
	save(now.Add(-30*time.Minute), rainSiteh, rainSiteE, alone)
	save(now.Add(-5*time.Hour), budgetSited, budgetSiteg)
	save(now.Add(-13*time.Hour), old, oldToo)
	article := &scrape.Article{SourceID: "siteh", URL: rainSiteh.URL, Title: rainSiteh.Title, Body: []string{"גשם כבד ירד הבוקר.", "שני כבישים נסגרו."}}
	if err := db.SaveArticle(ctx, article, "<html/>", now); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGemini{}
	w := &Writer{Gemini: fake.serve(t), Store: db, Now: func() time.Time { return now }, Log: t.Logf,
		Sleep:    func(context.Context, time.Duration) error { return nil },
		SiteName: func(id string) string { return map[string]string{"siteh": "siteh", "sitee": "SiteE"}[id] }}
	found := []stories.Story{
		{Reports: []scrape.ArticleSummary{rainSiteh, rainSiteE}},
		{Reports: []scrape.ArticleSummary{budgetSited, budgetSiteg}},
		{Reports: []scrape.ArticleSummary{alone}},
		{Reports: []scrape.ArticleSummary{old, oldToo}},
	}
	return w, fake, found
}

func TestChangedStoriesAreSummarizedWithTheirSources(t *testing.T) {
	w, fake, found := setup(t)
	ctx := context.Background()
	result, err := w.Run(ctx, found)
	if err != nil {
		t.Fatal(err)
	}
	if result != (Result{Written: 2, Digest: true}) || fake.count() != 2 {
		t.Fatalf("%+v, %d requests", result, fake.count())
	}
	// The request: structured output, thinking low; the stories of the last 12 hours with two reports or more, the
	// most reported first; each article with its id, site and the start of its body.
	config := fake.requests[0]["generationConfig"].(map[string]any)
	if config["responseMimeType"] != "application/json" || config["responseJsonSchema"] == nil ||
		config["thinkingConfig"].(map[string]any)["thinkingLevel"] != "low" {
		t.Fatalf("generationConfig %v", config)
	}
	prompt := fake.prompts[0]
	for _, want := range []string{"סיפור s1", "[a1] siteh", "גוף: גשם כבד ירד הבוקר. שני כבישים נסגרו.", "[a2] SiteE", "סיפור s2", "[a3] sited"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{alone.Title, old.Title} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt has %q:\n%s", unwanted, prompt)
		}
	}
	// Footnotes are URLs; a sentence whose footnote isn't in the request is dropped, and so is a story that wasn't.
	saved, err := Summaries(ctx, w.Store)
	if err != nil {
		t.Fatal(err)
	}
	rain := saved[StoryKey(found[0])]
	want := StorySummary{Title: "גשם כבד בצפון", WrittenAt: now,
		Sentences:      []Sentence{{"גשם כבד ירד הבוקר בצפון.", []string{rainSiteh.URL, rainSiteE.URL}}, {"שני כבישים נסגרו.", []string{rainSiteE.URL}}},
		Contradictions: []Sentence{{"לפי siteh נסגרו שני כבישים, ולפי SiteE שלושה.", []string{rainSiteh.URL, rainSiteE.URL}}}}
	if got, _ := json.Marshal(rain); string(got) != mustJSON(want) {
		t.Fatalf("rain\n got %s\nwant %s", got, mustJSON(want))
	}
	if len(saved) != 2 {
		t.Fatalf("%d summaries", len(saved))
	}
	// A story's sentences cite only its own articles: the rain article's id in the budget story is dropped.
	// And not an article Gemini found unrelated: siteg's is listed, and gone from the footnotes.
	if got := saved[StoryKey(found[1])]; len(got.Sentences) != 1 || strings.Join(got.Sentences[0].Sources, " ") != budgetSited.URL ||
		strings.Join(got.Unrelated, " ") != budgetSiteg.URL {
		t.Fatalf("budget %+v", got)
	}
	// And kept for the grouping, against the rest of its story.
	if flags, err := w.Store.Unrelated(ctx, time.Time{}); err != nil || len(flags) != 1 || len(flags[budgetSiteg.URL]) != 1 || !flags[budgetSiteg.URL][budgetSited.URL] {
		t.Fatalf("flags %v, %v", flags, err)
	}
	// The digest: the stories with reports in the last hour, from their summaries; its thoughts are not the answer.
	if !strings.Contains(fake.prompts[1], "- גשם כבד ירד הבוקר בצפון. [a1,a2]") || strings.Contains(fake.prompts[1], "התקציב") {
		t.Fatalf("digest prompt:\n%s", fake.prompts[1])
	}
	digest, err := LatestDigest(ctx, w.Store)
	if err != nil || digest == nil {
		t.Fatal(digest, err)
	}
	if digest.Title != "גשם בצפון והתקציב בכנסת" || len(digest.Sentences) != 1 || digest.Sentences[0].Sources[1] != rainSiteE.URL {
		// a3 wasn't in the digest's request: that sentence is dropped.
		t.Fatalf("%+v", digest)
	}
	if n, _ := w.Store.GeminiRequests(ctx, Day(now)); n != 2 {
		t.Fatalf("%d requests counted", n)
	}
	// Both answers' tokens, by the free tier's day; and the run, for the statistics.
	days, err := w.Store.GeminiDays(ctx, 7)
	if err != nil || len(days) != 1 || days[0].Tokens != (store.Tokens{In: 1700, Cached: 800, Out: 380, Thoughts: 40}) {
		t.Fatalf("%+v %v", days, err)
	}
	if status := w.Status(); status.At != now || status.Written != 2 || !status.Digest || status.Error != "" {
		t.Fatalf("%+v", status)
	}
}

func TestOnlyChangedStoriesAreSentAgain(t *testing.T) {
	w, fake, found := setup(t)
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	// Nothing changed: no request, not even the digest.
	if result, err := w.Run(ctx, found); err != nil || result != (Result{}) || fake.count() != 2 {
		t.Fatalf("%+v %v, %d requests", result, err, fake.count())
	}
	// A third site reports the rain: that story only, and a new digest.
	third := scrape.ArticleSummary{SourceID: "siteg", URL: "https://siteg/rain", Title: "גשם"}
	if err := w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{third}, now); err != nil {
		t.Fatal(err)
	}
	found[0].Reports = append(found[0].Reports, third)
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	if fake.count() != 4 || strings.Contains(fake.prompts[2], "s2") || !strings.Contains(fake.prompts[2], "https://siteg/rain") && !strings.Contains(fake.prompts[2], "[a3] siteg") {
		t.Fatalf("%d requests; third:\n%s", fake.count(), fake.prompts[2])
	}
}

func TestTheDigestGoesFirstWhenTheBudgetIsShort(t *testing.T) {
	w, fake, found := setup(t)
	ctx := context.Background()
	w.Budget = 3
	for range 2 {
		w.Store.CountGeminiRequest(ctx, Day(now))
	}
	result, err := w.Run(ctx, found)
	if err != nil {
		t.Fatal(err)
	}
	if result != (Result{Waiting: 2, Digest: true}) || fake.count() != 1 {
		t.Fatalf("%+v, %d requests", result, fake.count())
	}
	// None left: nothing is sent.
	if _, err := w.Run(ctx, found); err != nil || fake.count() != 1 {
		t.Fatalf("%v, %d requests", err, fake.count())
	}
}

func TestA429EndsTheRun(t *testing.T) {
	w, fake, found := setup(t)
	fake.status = http.StatusTooManyRequests
	_, err := w.Run(context.Background(), found)
	if !errors.Is(err, gemini.ErrRateLimited) || fake.count() != 1 {
		t.Fatalf("%v, %d requests", err, fake.count())
	}
	if n, _ := w.Store.GeminiRequests(context.Background(), Day(now)); n != 1 {
		t.Fatalf("a refused request still counts: %d", n)
	}
}

func TestTheFreeTiersDayIsPacific(t *testing.T) {
	if got := Day(time.Date(2026, 11, 11, 5, 0, 0, 0, time.UTC)); got != "2026-11-10" {
		t.Fatal(got)
	}
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func TestOneRequestTakesAtMostTwelveStories(t *testing.T) {
	w, fake, _ := setup(t)
	ctx := context.Background()
	var many []stories.Story
	for i := range 15 {
		a := scrape.ArticleSummary{SourceID: "siteh", URL: fmt.Sprintf("https://siteh/%d", i), Title: "א"}
		b := scrape.ArticleSummary{SourceID: "sitee", URL: fmt.Sprintf("https://sitee/%d", i), Title: "ב"}
		if err := w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{a, b}, now); err != nil {
			t.Fatal(err)
		}
		many = append(many, stories.Story{Reports: []scrape.ArticleSummary{a, b}})
	}
	if _, err := w.Run(ctx, many); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(fake.prompts[0], "=== סיפור s"); n != 12 {
		t.Fatalf("%d stories in one request", n)
	}
}

func TestASentenceEndsLikeOne(t *testing.T) {
	for text, want := range map[string]bool{"ירד גשם.": true, "האם ירד גשם?": true, `הוא אמר "די."`: true, "בעקבות הסערה הורה יו": false, "": false} {
		if finished(text) != want {
			t.Errorf("%q: %v", text, !want)
		}
	}
}

func TestABusyGeminiIsTriedAgain(t *testing.T) {
	w, fake, found := setup(t)
	fake.busy = 1
	result, err := w.Run(context.Background(), found)
	if err != nil || result.Written != 2 || fake.count() != 3 {
		t.Fatalf("%+v %v, %d requests", result, err, fake.count())
	}
	// Still busy after every wait: the run ends, the next one tries again.
	w2, fake2, found2 := setup(t)
	fake2.busy = 10
	if _, err := w2.Run(context.Background(), found2); !errors.Is(err, gemini.ErrUnavailable) || fake2.count() != 3 {
		t.Fatalf("%v, %d requests", err, fake2.count())
	}
}

func TestABigStoryShowsEightReportsOnePerSiteFirst(t *testing.T) {
	var s stories.Story
	for i := range 12 {
		s.Reports = append(s.Reports, scrape.ArticleSummary{SourceID: fmt.Sprintf("site%d", i%3), URL: fmt.Sprintf("https://x/%d", i)})
	}
	got := shown(s)
	if len(got) != 8 || got[0].SourceID != "site0" || got[1].SourceID != "site1" || got[2].SourceID != "site2" || got[3].URL != "https://x/3" {
		t.Fatalf("%+v", got)
	}
}

func TestFootnotesAreCappedAndGarbledSentencesDropped(t *testing.T) {
	ids := newSources()
	var refs []string
	for i := range 7 {
		refs = append(refs, ids.id(fmt.Sprintf("https://x/%d", i)))
	}
	got := ids.sentences([]answerSentence{{Text: "עובדה.", Sources: refs}, {Text: "מסوוג.", Sources: refs}}, nil)
	if len(got) != 1 || len(got[0].Sources) != maxFootnotes || ids.cut != 1 {
		t.Fatalf("%+v, %d cut", got, ids.cut)
	}
}

func TestARunSendsMoreRequestsWhileTheBudgetHasRoom(t *testing.T) {
	ctx := context.Background()
	thirty := func(w *Writer) []stories.Story {
		var many []stories.Story
		for i := range 30 {
			a := scrape.ArticleSummary{SourceID: "siteh", URL: fmt.Sprintf("https://siteh/%d", i), Title: "א"}
			b := scrape.ArticleSummary{SourceID: "sitee", URL: fmt.Sprintf("https://sitee/%d", i), Title: "ב"}
			if err := w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{a, b}, now); err != nil {
				t.Fatal(err)
			}
			many = append(many, stories.Story{Reports: []scrape.ArticleSummary{a, b}})
		}
		return many
	}
	// Noon in Pacific time: 72 runs left in the day, 144 requests kept for them; 400 leaves room for all 3.
	w, fake, _ := setup(t)
	result, err := w.Run(ctx, thirty(w))
	if err != nil || fake.count() != 4 || result != (Result{Written: 6, Waiting: 24, Digest: true}) {
		// 12, 12 and 6 stories asked; the recorded answer writes s1 and s2 of each.
		t.Fatalf("%+v %v, %d requests", result, err, fake.count())
	}
	// A tight budget: one story request, then the digest.
	w2, fake2, _ := setup(t)
	w2.Budget = 146
	if _, err := w2.Run(ctx, thirty(w2)); err != nil || fake2.count() != 2 {
		t.Fatalf("%v, %d requests", err, fake2.count())
	}
}

func TestAbbreviationsUseHebrewMarks(t *testing.T) {
	if got := oneLine(`דובר צה"ל ויו”ר הכנסת   אמרו לג'ו "שלום"`); got != `דובר צה״ל ויו״ר הכנסת אמרו לג׳ו "שלום"` {
		t.Fatal(got)
	}
}
