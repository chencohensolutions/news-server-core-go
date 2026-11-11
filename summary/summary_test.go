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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// fakeGemini answers generateContent with the recorded answers in testdata: by the instructions, the threads',
// the grouping's, or the stories'. It keeps every request.
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
		if strings.Contains(string(system), "threads of a personal newspaper") {
			name = "threads-answer.json"
		}
		if strings.Contains(string(system), "group Israeli news headlines into stories") {
			name = "grouping-answer.json"
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
	article := &scrape.Article{SourceID: "siteh", URL: rainSiteh.URL, Title: rainSiteh.Title, Body: []string{"גשם כבד ירד הבוקר.", "שני כבישים נסגרו.", rainBody}}
	if err := db.SaveArticle(ctx, article, "<html/>", now); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGemini{}
	w := &Writer{Gemini: fake.serve(t), Store: db, Now: func() time.Time { return now }, Log: t.Logf,
		Sleep: func(context.Context, time.Duration) error { return nil }, ThreadsEvery: -1,
		SiteName: func(id string) string { return map[string]string{"siteh": "siteh", "sitee": "SiteE"}[id] }}
	// Short stories: 12 a request, which the tests of the requests count on.
	if err := SaveSettings(context.Background(), db, Settings{FullArticles: true, Length: "short"}); err != nil {
		t.Fatal(err)
	}
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
	if result != (Result{Written: 2}) || fake.count() != 1 {
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
	for _, want := range []string{"סיפור s1", "[a1] siteh", "גוף: גשם כבד ירד הבוקר. שני כבישים נסגרו. " + rainBody, "[a2] SiteE", "סיפור s2", "[a3] sited"} {
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
		Contradictions: []Sentence{{"לפי siteh נסגרו שני כבישים, ולפי SiteE שלושה.", []string{rainSiteh.URL, rainSiteE.URL}}},
		Reports:        []string{rainSiteh.URL, rainSiteE.URL}, ID: StoryKey(found[0]),
		Written: &Settings{FullArticles: true, Length: "short"}}
	// A new story unfolds from its first version.
	want.Updates = []Update{{At: now, Sentences: want.Sentences}}
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
	if n, _ := w.Store.GeminiRequests(ctx, Day(now)); n != 1 {
		t.Fatalf("%d requests counted", n)
	}
	// The answer's tokens, by the free tier's day; and the run, for the statistics.
	days, err := w.Store.GeminiDays(ctx, 7)
	if err != nil || len(days) != 1 || days[0].Tokens != (store.Tokens{In: 1200, Cached: 800, Out: 300, Thoughts: 40}) {
		t.Fatalf("%+v %v", days, err)
	}
	if status := w.Status(); status.At != now || status.Written != 2 || status.Error != "" {
		t.Fatalf("%+v", status)
	}
}

func TestOnlyChangedStoriesAreSentAgain(t *testing.T) {
	w, fake, found := setup(t)
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	// Nothing changed: no request.
	if result, err := w.Run(ctx, found); err != nil || result != (Result{}) || fake.count() != 1 {
		t.Fatalf("%+v %v, %d requests", result, err, fake.count())
	}
	// A third site reports the rain: that story only.
	third := scrape.ArticleSummary{SourceID: "siteg", URL: "https://siteg/rain", Title: "גשם"}
	if err := w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{third}, now); err != nil {
		t.Fatal(err)
	}
	found[0].Reports = append(found[0].Reports, third)
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	if fake.count() != 2 || strings.Contains(fake.prompts[1], "s2") || !strings.Contains(fake.prompts[1], "https://siteg/rain") && !strings.Contains(fake.prompts[1], "[a3] siteg") {
		t.Fatalf("%d requests; second:\n%s", fake.count(), fake.prompts[1])
	}
	// It is a later version of the rain story: Gemini sees the previous one, and what it adds unfolds the story.
	if !strings.Contains(fake.prompts[1], "הגרסה הקודמת") || !strings.Contains(fake.prompts[1], "- גשם כבד ירד הבוקר בצפון.") {
		t.Fatalf("no previous version:\n%s", fake.prompts[1])
	}
	saved, err := Summaries(ctx, w.Store)
	if err != nil {
		t.Fatal(err)
	}
	rain := saved[StoryKey(found[0])]
	if len(rain.Updates) != 2 || len(rain.Updates[0].Sentences) != 2 || len(rain.Updates[1].Sentences) != 1 ||
		rain.Updates[1].Sentences[0].Text != "גשם ירד גם בגליל." || rain.Updates[1].Sentences[0].Sources[0] != third.URL {
		t.Fatalf("updates %+v", rain.Updates)
	}
	// The same story: it keeps its id.
	if want := StoryKey(stories.Story{Reports: found[0].Reports[:2]}); rain.ID != want {
		t.Fatalf("id %s, want %s", rain.ID, want)
	}
}

func TestTheDaysLastRequestGoesToStories(t *testing.T) {
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
	if result != (Result{Written: 2}) || fake.count() != 1 {
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
	if err != nil || result.Written != 2 || fake.count() != 2 {
		t.Fatalf("%+v %v, %d requests", result, err, fake.count())
	}
	// Still busy after every wait: the run ends, the next one tries again.
	w2, fake2, found2 := setup(t)
	fake2.busy = 10
	if _, err := w2.Run(context.Background(), found2); !errors.Is(err, gemini.ErrUnavailable) || fake2.count() != 3 {
		t.Fatalf("%v, %d requests", err, fake2.count())
	}
}

func TestABigStoryShowsItsReportsOnePerSiteFirst(t *testing.T) {
	var s stories.Story
	for i := range 50 {
		s.Reports = append(s.Reports, scrape.ArticleSummary{SourceID: fmt.Sprintf("site%d", i%3), URL: fmt.Sprintf("https://x/%d", i)})
	}
	got := shown(s)
	if len(got) != maxReports || got[0].SourceID != "site0" || got[1].SourceID != "site1" || got[2].SourceID != "site2" || got[3].URL != "https://x/3" {
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
	if err != nil || fake.count() != 3 || result != (Result{Written: 6, Waiting: 24}) {
		// 12, 12 and 6 stories asked; the recorded answer writes s1 and s2 of each.
		t.Fatalf("%+v %v, %d requests", result, err, fake.count())
	}
	// A tight budget: one story request.
	w2, fake2, _ := setup(t)
	w2.Budget = 146
	if _, err := w2.Run(ctx, thirty(w2)); err != nil || fake2.count() != 1 {
		t.Fatalf("%v, %d requests", err, fake2.count())
	}
}

func TestAbbreviationsUseHebrewMarks(t *testing.T) {
	if got := oneLine(`דובר צה"ל ויו”ר הכנסת   אמרו לג'ו "שלום"`); got != `דובר צה״ל ויו״ר הכנסת אמרו לג׳ו "שלום"` {
		t.Fatal(got)
	}
}

func TestStoriesAreLinkedIntoThreadsOnceADay(t *testing.T) {
	w, fake, found := setup(t)
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	// Two stories written and kept for good, under their own ids.
	waiting, err := w.Store.UnlinkedStories(ctx, 10)
	if err != nil || len(waiting) != 2 {
		t.Fatalf("%d waiting, %v", len(waiting), err)
	}
	w.ThreadsEvery = 24 * time.Hour
	if err := w.linkThreadsIfDue(ctx, Day(now), now); err != nil {
		t.Fatal(err)
	}
	if fake.count() != 2 || !strings.Contains(fake.prompts[1], "(אין עדיין)") || !strings.Contains(fake.prompts[1], "[n2]") {
		t.Fatalf("%d requests; linking:\n%s", fake.count(), fake.prompts[len(fake.prompts)-1])
	}
	threads, err := w.Store.Threads(ctx, 10)
	if err != nil || len(threads) != 2 {
		t.Fatalf("threads %+v, %v", threads, err)
	}
	// Many to many: the weather thread has both stories, the budget thread one (n99 isn't in the input), and the
	// unnamed one is none; threads that don't exist yet (t1, t9) are dropped.
	counts := map[string]int{}
	for _, th := range threads {
		counts[th.Name] = th.Stories
		if !th.StartedAt.Equal(now) || !th.UpdatedAt.Equal(now) || th.Summary == "" {
			t.Fatalf("thread %+v", th)
		}
	}
	if counts["מזג האוויר בחורף"] != 2 || counts["התקציב"] != 1 {
		t.Fatalf("counts %v", counts)
	}
	// Related both ways; t7 doesn't exist.
	for _, th := range threads {
		rel, err := w.Store.RelatedThreads(ctx, th.ID)
		if err != nil || len(rel) != 1 || rel[0].ID == th.ID {
			t.Fatalf("%s related %+v, %v", th.Name, rel, err)
		}
	}
	if left, _ := w.Store.UnlinkedStories(ctx, 10); len(left) != 0 {
		t.Fatalf("%d still waiting", len(left))
	}
	in := 0
	for _, r := range waiting {
		ths, err := w.Store.StoryThreads(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		in += len(ths)
	}
	if in != 3 {
		t.Fatalf("%d story-thread links", in)
	}
	// Not again within the day.
	if err := w.linkThreadsIfDue(ctx, Day(now), now.Add(time.Hour)); err != nil || fake.count() != 2 {
		t.Fatalf("%v, %d requests", err, fake.count())
	}
	// A day later a new story is offered with the threads so far, and links to the latest updated (t1).
	later := now.Add(25 * time.Hour)
	data, _ := json.Marshal(StorySummary{ID: "x", Title: "עוד גשם", Sentences: []Sentence{{"ירד גשם.", []string{rainSiteh.URL}}}})
	if err := w.Store.SaveStoryRecord(ctx, "x", data, later); err != nil {
		t.Fatal(err)
	}
	if err := w.linkThreadsIfDue(ctx, Day(later), later); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.prompts[2], "[t1]") || !strings.Contains(fake.prompts[2], "[t2]") || !strings.Contains(fake.prompts[2], "עוד גשם") {
		t.Fatalf("second linking:\n%s", fake.prompts[2])
	}
	ths, err := w.Store.StoryThreads(ctx, "x")
	if err != nil || len(ths) == 0 {
		t.Fatalf("x in %+v, %v", ths, err)
	}
	// A new thread named as an existing one is that one: still two threads.
	if all, _ := w.Store.Threads(ctx, 10); len(all) != 2 {
		t.Fatalf("threads after %+v", all)
	}
	updated := false
	for _, th := range ths {
		updated = updated || th.Summary == "הסיפור עד כה, מעודכן." && th.UpdatedAt.Equal(later)
	}
	if !updated {
		t.Fatalf("t1 after %+v", ths)
	}
}

func TestGeminiGroupsTheNewHeadlinesOnce(t *testing.T) {
	w, fake, _ := setup(t)
	ctx := context.Background()
	feed := []scrape.ArticleSummary{rainSiteh, rainSiteE, budgetSited, budgetSiteg, alone}
	found, err := w.Group(ctx, feed, now)
	if err != nil {
		t.Fatal(err)
	}
	// Two new stories of two sites each; alone in none; h77 isn't in the input.
	if len(found) != 2 || fake.count() != 1 || !strings.Contains(fake.prompts[0], "(אין עדיין)") {
		t.Fatalf("%d stories, %d requests:\n%s", len(found), fake.count(), fake.prompts[0])
	}
	urls := func(s stories.Story) string {
		var out []string
		for _, r := range s.Reports {
			out = append(out, r.URL)
		}
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	got := []string{urls(found[0]), urls(found[1])}
	sort.Strings(got)
	if got[0] != budgetSited.URL+" "+budgetSiteg.URL || got[1] != rainSiteE.URL+" "+rainSiteh.URL {
		t.Fatalf("stories %v", got)
	}
	// Placed headlines aren't sent again.
	again, err := w.Group(ctx, feed, now)
	if err != nil || len(again) != 2 || fake.count() != 1 {
		t.Fatalf("again: %d stories, %v, %d requests", len(again), err, fake.count())
	}
	// A new headline is offered with the stories so far, and the answer's merge of s1 and s2 makes them one.
	third := scrape.ArticleSummary{SourceID: "siteg", URL: "https://siteg/rain", Title: "גשם"}
	merged, err := w.Group(ctx, append([]scrape.ArticleSummary{third}, feed...), now)
	if err != nil || fake.count() != 2 || !strings.Contains(fake.prompts[1], "[s1]") || !strings.Contains(fake.prompts[1], "[s2]") {
		t.Fatalf("%v, %d requests:\n%s", err, fake.count(), fake.prompts[len(fake.prompts)-1])
	}
	if len(merged) != 1 || len(merged[0].Reports) != 4 {
		t.Fatalf("after the merge %d stories", len(merged))
	}
	// Out of budget: an error, so the model's grouping stands in.
	w.Budget = 1
	if _, err := w.Group(ctx, append([]scrape.ArticleSummary{{SourceID: "sited", URL: "https://sited/new", Title: "x"}}, feed...), now); !errors.Is(err, ErrNoBudget) {
		t.Fatalf("no budget: %v", err)
	}
}

// rainBody makes the test article's body longer than a paywall stub.
var rainBody = strings.TrimSpace(strings.Repeat("המשטרה ביקשה מהנהגים להימנע מנסיעה באזור עד שהמים ייסוגו. ", 4))
