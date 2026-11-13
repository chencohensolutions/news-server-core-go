package summary

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// Recorded answers of the incremental request.
const (
	// A development and a footnote; and a story the request didn't have.
	shadowNews = `{"stories": [{"story": "s1", "unrelated": [],
		"insert": [{"after": "top", "kind": "development", "text": "נחל הקישון עלה על גדותיו.", "sources": ["a1"]},
			{"after": "s1.1", "kind": "development", "text": "גשם כבד ירד בצפון הבוקר.", "sources": ["a1"]}],
		"replace": [], "delete": [], "cite": [{"sentence": "s1.1", "sources": ["a1"]}], "title": "", "contradictions": []},
		{"story": "s7", "unrelated": [], "insert": [], "replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`
	// Nothing new: the new article states the first sentence's fact.
	shadowQuiet = `{"stories": [{"story": "s1", "unrelated": [], "insert": [], "replace": [], "delete": [],
		"cite": [{"sentence": "s1.2", "sources": ["a1"]}], "title": "", "contradictions": []}]}`
	// From an article older than the story: a detail with a number and background, a deletion and a title.
	shadowOld = `{"stories": [{"story": "s1", "unrelated": [],
		"insert": [{"after": "s1.1", "kind": "detail", "text": "בחיפה ירדו 80 מילימטרים של גשם.", "sources": ["a1"]},
			{"after": "top", "kind": "background", "text": "בחורף שעבר הוצף האזור פעמיים.", "sources": ["a1"]}],
		"replace": [], "delete": ["s1.2"], "cite": [], "title": "80 מילימטרים בחיפה", "contradictions": []}]}`
	// No answer: the story's new article is named nowhere.
	shadowSkipped = `{"stories": [{"story": "s1", "unrelated": [], "insert": [], "replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`
)

// shadowed is setup with the shadow on and the incremental requests answered from answers.
func shadowed(t *testing.T, answers ...string) (*Writer, *fakeGemini, *recorded, []stories.Story) {
	t.Helper()
	w, fake, found := setup(t)
	rec := &recorded{live: w.Gemini, answers: answers}
	w.Gemini = rec
	if err := SaveSettings(context.Background(), w.Store, Settings{FullArticles: true, Length: "short", Incremental: IncrementalShadow}); err != nil {
		t.Fatal(err)
	}
	return w, fake, rec, found
}

// join adds a report to the rain story (found[0]) at the next minute and runs.
func join(t *testing.T, w *Writer, found []stories.Story, r scrape.ArticleSummary, body ...string) Result {
	t.Helper()
	ctx := context.Background()
	at := w.Now().Add(time.Minute)
	w.Now = func() time.Time { return at }
	if err := w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{r}, at); err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 {
		if err := w.Store.SaveArticle(ctx, &scrape.Article{SourceID: r.SourceID, URL: r.URL, Title: r.Title, Body: body}, "", at); err != nil {
			t.Fatal(err)
		}
	}
	found[0].Reports = append(append([]scrape.ArticleSummary{}, found[0].Reports...), r)
	result, err := w.Run(ctx, found)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func shadowOf(t *testing.T, w *Writer, s stories.Story) *Shadow {
	t.Helper()
	data, err := w.Store.Shadow(context.Background(), StoryKey(s))
	if err != nil {
		t.Fatal(err)
	}
	if data == nil {
		return nil
	}
	var out Shadow
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func TestTheShadowUpdatesAStoryBesideItsRewrite(t *testing.T) {
	w, fake, rec, found := shadowed(t, shadowNews, shadowQuiet)
	ctx := context.Background()
	// First versions are written whole: nothing for the shadow.
	if result, err := w.Run(ctx, found); err != nil || result != (Result{Written: 2}) || len(rec.requests) != 0 {
		t.Fatalf("%+v %v, %d shadow requests", result, err, len(rec.requests))
	}
	// A third site reports the rain: the story is written again as ever, and the shadow asks what the article adds.
	if result := join(t, w, found, rainSiteg, news...); result != (Result{Written: 1}) || fake.count() != 2 || len(rec.requests) != 1 {
		t.Fatalf("%+v, %d live and %d shadow requests", result, fake.count(), len(rec.requests))
	}
	prompt := rec.requests[0].Prompt
	for _, want := range []string{"=== סיפור s1 ===", "כותרת הסיפור: גשם כבד בצפון", "s1.1 [מקורות: siteh, SiteE] גשם כבד ירד הבוקר בצפון.\n", "s1.2 [מקורות: SiteE] שני כבישים נסגרו.\n",
		"כתבות שכבר בסיפור:\n- siteh: גשם בצפון\n- SiteE: סערה בצפון", "כתבות חדשות:\n[a1] siteg · פורסמה אחרי שהסיפור נכתב\nכותרת: גשם בגליל\nגוף: " + news[0]} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the shadow's request lacks %q:\n%s", want, prompt)
		}
	}
	// The articles the story has are titles: their bodies are not sent again. Nor are other stories asked about.
	if strings.Contains(prompt, rainBody) || strings.Contains(prompt, "s2") {
		t.Fatalf("the shadow's request:\n%s", prompt)
	}
	// The live story is the rewrite's, as without the shadow.
	saved, err := Summaries(ctx, w.Store)
	if err != nil {
		t.Fatal(err)
	}
	live := saved[StoryKey(found[0])]
	if live.Title != "גשם כבד בצפון" || len(live.Updates) != 2 || live.Updates[1].Sentences[0].Text != "גשם ירד גם בגליל." || len(live.Updates[1].Kinds) != 0 {
		t.Fatalf("live %+v", live)
	}
	if versions, err := w.Store.StoryVersions(ctx, []string{live.ID}); err != nil || len(versions[live.ID]) != 2 {
		t.Fatalf("versions %+v %v", versions, err)
	}
	// The shadow's: the version before with the development on top; the restatement dropped; the footnote added.
	shadow := shadowOf(t, w, found[0])
	if shadow == nil || shadow.Plan != PlanIncremental || !shadow.Version || shadow.Base != BaseLive || shadow.ID != live.ID || shadow.Full != 2 ||
		strings.Join(shadow.New, " ") != rainSiteg.URL || *shadow.Stats != (MergeStats{Asked: 2, Inserted: 1, Restated: 1, Cited: 1}) {
		t.Fatalf("shadow %+v", shadow)
	}
	if got := texts(shadow.Story.Sentences); got != "נחל הקישון עלה על גדותיו. | גשם כבד ירד הבוקר בצפון. | שני כבישים נסגרו." {
		t.Fatalf("shadow story: %s", got)
	}
	if got := shadow.Story.Sentences[1].Sources; strings.Join(got, " ") != rainSiteh.URL+" "+rainSiteE.URL+" "+rainSiteg.URL {
		t.Fatalf("footnotes %v", got)
	}
	if r := shadow.Request; r == nil || r.Stories != 1 || r.Position != 1 || r.TokensIn != 500 || r.TokensOut != 80 || len(shadow.Answer.Insert) != 2 {
		t.Fatalf("request %+v", r)
	}
	// Its request is one of the day's, and its tokens are counted.
	if n, _ := w.Store.GeminiRequests(ctx, Day(now)); n != 3 {
		t.Fatalf("%d requests counted", n)
	}
	// Nothing changed: the story is not asked about again.
	if result, err := w.Run(ctx, found); err != nil || result != (Result{}) || len(rec.requests) != 1 {
		t.Fatalf("%+v %v, %d shadow requests", result, err, len(rec.requests))
	}

	// A fourth report with nothing new. The shadow goes on from its own story, and records the step all the same:
	// no version, the article among the reports and the footnotes.
	if result := join(t, w, found, rainSited); result != (Result{Written: 1}) || len(rec.requests) != 2 {
		t.Fatalf("%+v, %d shadow requests", result, len(rec.requests))
	}
	prompt = rec.requests[1].Prompt
	if !strings.Contains(prompt, "s1.1 [מקורות: siteg] נחל הקישון עלה על גדותיו.\n") || !strings.Contains(prompt, "[a1] sited · פורסמה אחרי שהסיפור נכתב\nכותרת: הצפות בצפון\n") || strings.Contains(prompt, "גוף:") {
		t.Fatalf("the second request:\n%s", prompt)
	}
	quiet := shadowOf(t, w, found[0])
	if quiet == nil || quiet.Version || quiet.Base != BaseShadow || quiet.Full != 2 || *quiet.Stats != (MergeStats{Cited: 1}) ||
		texts(quiet.Story.Sentences) != texts(shadow.Story.Sentences) || !quiet.Story.WrittenAt.Equal(shadow.Story.WrittenAt) ||
		len(quiet.Story.Reports) != 4 || len(quiet.Story.Sentences[1].Sources) != 4 || len(quiet.Story.Updates) != 2 ||
		strings.Join(quiet.Story.HeadlineOnly, " ") != rainSiteE.URL+" "+rainSited.URL {
		t.Fatalf("quiet %+v\n%+v", quiet, quiet.Story)
	}
	// Answered "nothing new", and still not asked again.
	if _, err := w.Run(ctx, found); err != nil || len(rec.requests) != 2 {
		t.Fatalf("%v, %d shadow requests", err, len(rec.requests))
	}
	// The story loses a report no sentence stood on alone: kept by the server, no request.
	found[0].Reports = found[0].Reports[1:]
	w.Now = func() time.Time { return now.Add(time.Hour) }
	if _, err := w.Run(ctx, found); err != nil || len(rec.requests) != 2 {
		t.Fatalf("%v, %d shadow requests", err, len(rec.requests))
	}
	kept := shadowOf(t, w, found[0])
	if kept == nil || kept.Plan != PlanLocal || kept.Version || strings.Join(kept.Gone, " ") != rainSiteh.URL || len(kept.Story.Sentences) != 3 || len(kept.Story.Reports) != 3 {
		t.Fatalf("kept %+v", kept)
	}
	// What the shadow recorded: three steps, one of them a version; the live rewrites had new facts in both asked.
	report, err := ReportShadow(ctx, w.Store, now.Add(-time.Hour))
	if err != nil || report.Steps != 3 || report.Local != 1 || report.Asked != 2 || report.Versions != 1 || report.Quiet != 1 || report.FromShadow != 1 ||
		report.Compared != 2 || report.LiveNewVersion != 1 || report.LiveNewQuiet != 1 {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestTheShadowRecordsAQuietUpdate(t *testing.T) {
	w, _, rec, found := shadowed(t, shadowOld, shadowQuiet)
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	saved, err := Summaries(ctx, w.Store)
	if err != nil {
		t.Fatal(err)
	}
	live := saved[StoryKey(found[0])]
	// A third site's article joins the rain story: the site published it two hours before the story was written.
	preview := rainSiteg
	preview.PublishedAt = scrape.FormatInstant(now.Add(-2 * time.Hour))
	join(t, w, found, preview)
	if want := "[a1] siteg · 2026-11-10T18:00:00Z · פורסמה לפני שהסיפור נכתב\n"; len(rec.requests) != 1 || !strings.Contains(rec.requests[0].Prompt, want) {
		t.Fatalf("the request lacks %q:\n%s", want, rec.requests[0].Prompt)
	}
	// Its detail with a number makes no version, and is in the shadow's story all the same: a quiet update, recorded
	// as one. The story's title, time and updates are the live version's; nothing was deleted, and the background is
	// left out.
	quiet := shadowOf(t, w, found[0])
	if quiet == nil || quiet.Version || quiet.Quiet != 1 || quiet.Story == nil || quiet.Reason != "" || *quiet.Stats != (MergeStats{Asked: 2, Quiet: 1, Old: 1, Unapplied: 1}) {
		t.Fatalf("%+v", quiet)
	}
	if got := texts(quiet.Story.Sentences); got != "גשם כבד ירד הבוקר בצפון. | בחיפה ירדו 80 מילימטרים של גשם. | שני כבישים נסגרו." ||
		strings.Join(quiet.Story.Sentences[1].Sources, " ") != rainSiteg.URL {
		t.Fatalf("the shadow's story: %s", got)
	}
	if quiet.Story.Title != live.Title || !quiet.Story.WrittenAt.Equal(live.WrittenAt) || len(quiet.Story.Updates) != len(live.Updates) || len(quiet.Story.Reports) != 3 {
		t.Fatalf("%+v, live %+v", quiet.Story, live)
	}
	// In the table's JSON a quiet update is told from a version and from nothing new.
	if data, err := w.Store.Shadow(ctx, StoryKey(found[0])); err != nil || !strings.Contains(string(data), `"version":false,"quiet":1,`) {
		t.Fatalf("%s %v", data, err)
	}
	// The shadow's line of the story goes on from the changed text: the next request shows the sentence as the
	// story's own, with its site.
	join(t, w, found, rainSited)
	if want := "s1.2 [מקורות: siteg] בחיפה ירדו 80 מילימטרים של גשם.\ns1.3 [מקורות: SiteE] שני כבישים נסגרו.\n"; len(rec.requests) != 2 || !strings.Contains(rec.requests[1].Prompt, want) {
		t.Fatalf("the second request lacks %q:\n%s", want, rec.requests[1].Prompt)
	}
	next := shadowOf(t, w, found[0])
	if next == nil || next.Version || next.Quiet != 0 || next.Base != BaseShadow || texts(next.Story.Sentences) != texts(quiet.Story.Sentences) || len(next.Story.Sentences[1].Sources) != 2 {
		t.Fatalf("%+v", next)
	}
	// The report tells the two apart: neither made a version, one changed the text.
	report, err := ReportShadow(ctx, w.Store, now.Add(-time.Hour))
	if err != nil || report.Asked != 2 || report.Versions != 0 || report.Quiet != 2 || report.QuietUpdates != 1 || report.Stats.Quiet != 1 || report.Tripwire != 0 {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestTheShadowTakesOnlyWhatTheDaySpares(t *testing.T) {
	w, fake, rec, found := shadowed(t, shadowNews)
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	// A budget that leaves no two requests for each run left in the day: the story is written, the shadow waits.
	w.Budget = 100
	if result := join(t, w, found, rainSiteg); result != (Result{Written: 1}) || fake.count() != 2 || len(rec.requests) != 0 {
		t.Fatalf("%+v, %d live and %d shadow requests", result, fake.count(), len(rec.requests))
	}
	// Past the mark from which only new stories are written: none either.
	w.Budget = 0
	if err := SaveSettings(ctx, w.Store, Settings{FullArticles: true, Length: "short", Incremental: IncrementalShadow, ReserveAfter: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(ctx, found); err != nil || len(rec.requests) != 0 {
		t.Fatalf("%v, %d shadow requests", err, len(rec.requests))
	}
	// The shadow's own share of the day, used.
	if err := SaveSettings(ctx, w.Store, Settings{FullArticles: true, Length: "short", Incremental: IncrementalShadow, ShadowDaily: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.countShadow(ctx, Day(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(ctx, found); err != nil || len(rec.requests) != 0 || shadowOf(t, w, found[0]) != nil {
		t.Fatalf("%v, %d shadow requests", err, len(rec.requests))
	}
	// With room, the story that waited is asked about.
	if err := SaveSettings(ctx, w.Store, Settings{FullArticles: true, Length: "short", Incremental: IncrementalShadow}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(ctx, found); err != nil || len(rec.requests) != 1 || shadowOf(t, w, found[0]) == nil {
		t.Fatalf("%v, %d shadow requests", err, len(rec.requests))
	}
	// Off: the shadow is not run. "on" is not built: off too.
	for _, mode := range []string{IncrementalOff, "", "on"} {
		if err := SaveSettings(ctx, w.Store, Settings{FullArticles: true, Length: "short", Incremental: mode}); err != nil {
			t.Fatal(err)
		}
		if result := join(t, w, found, scrape.ArticleSummary{SourceID: "sited", URL: "https://sited/rain/" + mode, Title: "גשם"}); result != (Result{Written: 1}) || len(rec.requests) != 1 {
			t.Fatalf("%q: %+v, %d shadow requests", mode, result, len(rec.requests))
		}
	}
}

func TestABusyOrCutShadowAnswer(t *testing.T) {
	// Two stories asked about; the answer is cut inside the second.
	cut := shadowNews[:strings.Index(shadowNews, `{"story": "s7"`)] + `{"story": "s2", "unrelated": [], "insert": [{"after": "top", "kind": "deve`
	w, _, rec, found := shadowed(t, "", cut, shadowQuiet)
	rec.errs = []error{gemini.ErrUnavailable, gemini.ErrCut}
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	// Gemini is busy: the shadow tries once, the request is counted (the quota counts it), and the run is none the
	// worse.
	if result := join(t, w, found, rainSiteg); result != (Result{Written: 1}) || len(rec.requests) != 1 || shadowOf(t, w, found[0]) != nil {
		t.Fatalf("%+v, %d shadow requests", result, len(rec.requests))
	}
	if n, _ := w.Store.GeminiRequests(ctx, Day(now)); n != 3 {
		t.Fatalf("%d requests counted", n)
	}
	if sent, _ := w.shadowSent(ctx, Day(now)); sent != 1 {
		t.Fatalf("%d shadow requests counted", sent)
	}
	// The budget story gains a report too. The next run asks about both; the cut answer's whole story is kept, the
	// other waits for the run after.
	at := w.Now().Add(time.Minute)
	w.Now = func() time.Time { return at }
	third := scrape.ArticleSummary{SourceID: "sitee", URL: "https://sitee/budget", Title: "התקציב עבר"}
	if err := w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{third}, at); err != nil {
		t.Fatal(err)
	}
	found[1].Reports = append(found[1].Reports, third)
	if _, err := w.Run(ctx, found); err != nil || len(rec.requests) != 2 {
		t.Fatalf("%v, %d shadow requests", err, len(rec.requests))
	}
	if !strings.Contains(rec.requests[1].Prompt, "=== סיפור s2 ===") {
		t.Fatalf("the request:\n%s", rec.requests[1].Prompt)
	}
	rain, budget := shadowOf(t, w, found[0]), shadowOf(t, w, found[1])
	asked, waiting := rain, found[1]
	if rain == nil {
		asked, waiting = budget, found[0]
	}
	if asked == nil || !asked.Request.Salvaged || asked.Request.Stories != 2 || shadowOf(t, w, waiting) != nil {
		t.Fatalf("rain %+v, budget %+v", rain, budget)
	}
	if sent, _ := w.shadowSent(ctx, Day(now)); sent != 2 {
		t.Fatalf("%d shadow requests counted", sent)
	}
	if _, err := w.Run(ctx, found); err != nil || len(rec.requests) != 3 || shadowOf(t, w, waiting) == nil || strings.Contains(rec.requests[2].Prompt, "s2") {
		t.Fatalf("%v, %d shadow requests", err, len(rec.requests))
	}
}

func TestAStoryGeminiSkippedIsAskedAgain(t *testing.T) {
	w, _, rec, found := shadowed(t, shadowNews, shadowSkipped, shadowQuiet, shadowQuiet, shadowSkipped)
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	// The shadow's own story of the rain: a version with the development on top.
	join(t, w, found, rainSiteg, news...)
	first := shadowOf(t, w, found[0])
	if first == nil || !first.Version || len(rec.requests) != 1 {
		t.Fatalf("%+v, %d shadow requests", first, len(rec.requests))
	}
	// A fourth report, and an answer that names it nowhere. That is not "nothing new": the step is recorded as to
	// be asked again, with the answer, and without a story for the next step to start from.
	join(t, w, found, rainSited)
	skipped := shadowOf(t, w, found[0])
	if len(rec.requests) != 2 || skipped == nil || !skipped.Again || skipped.Skipped != 1 || skipped.Story != nil || skipped.Version || skipped.Answer == nil || skipped.Base != BaseShadow {
		t.Fatalf("%+v, %d shadow requests", skipped, len(rec.requests))
	}
	if report, err := ReportShadow(ctx, w.Store, now.Add(-time.Hour)); err != nil || report.Asked != 2 || report.Versions != 1 || report.Skipped != 1 || report.Quiet != 0 || report.Tripwire != 0 || report.Compared != 1 {
		t.Fatalf("%+v %v", report, err)
	}
	// The next run asks about it again, before the budget story that changed since: the very request of the run
	// before comes first, from the shadow's own story as it was. This time the article is named: a step.
	at := w.Now().Add(time.Minute)
	w.Now = func() time.Time { return at }
	third := scrape.ArticleSummary{SourceID: "sitee", URL: "https://sitee/budget", Title: "התקציב עבר"}
	if err := w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{third}, at); err != nil {
		t.Fatal(err)
	}
	found[1].Reports = append(found[1].Reports, third)
	if _, err := w.Run(ctx, found); err != nil || len(rec.requests) != 3 {
		t.Fatalf("%v, %d shadow requests", err, len(rec.requests))
	}
	if again := rec.requests[2].Prompt; !strings.HasPrefix(again, rec.requests[1].Prompt) || !strings.Contains(again, "=== סיפור s2 ===") {
		t.Fatalf("asked again:\n%s\nafter:\n%s", again, rec.requests[1].Prompt)
	}
	quiet := shadowOf(t, w, found[0])
	if quiet == nil || quiet.Again || quiet.Skipped != 0 || quiet.Version || quiet.Base != BaseShadow || !quiet.BaseAt.Equal(first.At) || quiet.Story == nil ||
		texts(quiet.Story.Sentences) != texts(first.Story.Sentences) || len(quiet.Story.Reports) != 4 || *quiet.Stats != (MergeStats{Cited: 1}) {
		t.Fatalf("%+v", quiet)
	}
	// Answered now, and not asked again; the budget story, which the answer left out, is.
	if _, err := w.Run(ctx, found); err != nil || len(rec.requests) != 4 || strings.Contains(rec.requests[3].Prompt, "גשם") || shadowOf(t, w, found[1]) == nil {
		t.Fatalf("%v, %d shadow requests", err, len(rec.requests))
	}

	report, err := ReportShadow(ctx, w.Store, now.Add(-time.Hour))
	if err != nil || report.Asked != 3 || report.Versions != 1 || report.Quiet != 2 || report.Skipped != 0 || report.GaveUp != 0 {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestAStoryGeminiSkippedTwiceIsLeftToBeWrittenWhole(t *testing.T) {
	w, _, rec, found := shadowed(t, shadowSkipped)
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	join(t, w, found, rainSiteg, news...)
	if again := shadowOf(t, w, found[0]); len(rec.requests) != 1 || again == nil || !again.Again || again.Skipped != 1 || again.Base != BaseLive {
		t.Fatalf("%+v, %d shadow requests", again, len(rec.requests))
	}
	// Asked again, and skipped again: no third time. The story is left to be written whole, which the live rewrite
	// of these reports is: the next step starts from it.
	if _, err := w.Run(ctx, found); err != nil || len(rec.requests) != 2 || rec.requests[1].Prompt != rec.requests[0].Prompt {
		t.Fatalf("%v, %d shadow requests", err, len(rec.requests))
	}
	left := shadowOf(t, w, found[0])
	if left == nil || left.Again || left.Skipped != 2 || left.Story != nil || left.Version || left.Reason != whySkipped {
		t.Fatalf("%+v", left)
	}
	if _, err := w.Run(ctx, found); err != nil || len(rec.requests) != 2 {
		t.Fatalf("%v, %d shadow requests", err, len(rec.requests))
	}
	report, err := ReportShadow(ctx, w.Store, now.Add(-time.Hour))
	if err != nil || report.Asked != 1 || report.Versions != 0 || report.Quiet != 0 || report.Skipped != 0 || report.GaveUp != 1 || report.Tripwire != 0 || report.Rebuild[whySkipped] != 1 ||
		report.Compared != 0 {
		t.Fatalf("%+v %v", report, err)
	}
}
