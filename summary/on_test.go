package summary

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// Recorded answers of the incremental request: for the rain story of three sentences (after shadowNews), and for
// the budget story.
const (
	// From an article older than the story: a detail with a number, which makes no version, and a title, which no
	// version means is not taken.
	onQuiet = `{"stories": [{"story": "s1", "unrelated": [],
		"insert": [{"after": "s1.2", "kind": "detail", "text": "בחיפה ירדו 80 מילימטרים של גשם.", "sources": ["a1"]}],
		"replace": [], "delete": ["s1.3"], "cite": [], "title": "80 מילימטרים בחיפה", "contradictions": []}]}`
	// Nothing new: the new article states the first sentence's fact.
	onNothing = `{"stories": [{"story": "s1", "unrelated": [], "insert": [], "replace": [], "delete": [],
		"cite": [{"sentence": "s1.1", "sources": ["a1"]}], "title": "", "contradictions": []}]}`
)

// turnedOn is setup with the incremental update on and its requests answered from answers.
func turnedOn(t *testing.T, answers ...string) (*Writer, *fakeGemini, *recorded, []stories.Story) {
	t.Helper()
	w, fake, found := setup(t)
	rec := &recorded{live: w.Gemini, answers: answers}
	w.Gemini = rec
	if err := SaveSettings(context.Background(), w.Store, Settings{FullArticles: true, Length: "short", Incremental: IncrementalOn}); err != nil {
		t.Fatal(err)
	}
	return w, fake, rec, found
}

// savedOf is the story saved under s's reports.
func savedOf(t *testing.T, w *Writer, s stories.Story) StorySummary {
	t.Helper()
	saved, err := Summaries(context.Background(), w.Store)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := saved[StoryKey(s)]
	if !ok {
		t.Fatalf("nothing is saved under the story's reports")
	}
	return out
}

// versionsOf are the kept versions of story id, oldest first.
func versionsOf(t *testing.T, w *Writer, id string) []StorySummary {
	t.Helper()
	kept, err := w.Store.StoryVersions(context.Background(), []string{id})
	if err != nil {
		t.Fatal(err)
	}
	var out []StorySummary
	for _, v := range kept[id] {
		var s StorySummary
		if err := json.Unmarshal(v.Summary, &s); err != nil {
			t.Fatal(err)
		}
		if !s.WrittenAt.Equal(v.WrittenAt) {
			t.Fatalf("a version kept at %s says it was written at %s", v.WrittenAt, s.WrittenAt)
		}
		out = append(out, s)
	}
	return out
}

func TestOnAStorysNextVersionIsAnUpdateOfTheOneBefore(t *testing.T) {
	w, fake, rec, found := turnedOn(t, shadowNews, onQuiet, onNothing)
	ctx := context.Background()
	// First versions are written whole, as ever.
	if result, err := w.Run(ctx, found); err != nil || result != (Result{Written: 2}) || fake.count() != 1 || len(rec.requests) != 0 {
		t.Fatalf("%+v %v, %d whole and %d incremental requests", result, err, fake.count(), len(rec.requests))
	}
	first := savedOf(t, w, found[0])

	// A third site reports the rain, with news: one incremental request, and no whole rewrite. The answer's
	// development makes a version, saved as a whole writing saves its own.
	if result := join(t, w, found, rainSiteg, news...); result != (Result{Written: 1}) || fake.count() != 1 || len(rec.requests) != 1 {
		t.Fatalf("%+v, %d whole and %d incremental requests", result, fake.count(), len(rec.requests))
	}
	at := w.Now()
	second := savedOf(t, w, found[0])
	if second.ID != first.ID || !second.WrittenAt.Equal(at) || !second.UpdatedAt.IsZero() || second.Full != 2 || second.Title != first.Title ||
		texts(second.Sentences) != "נחל הקישון עלה על גדותיו. | גשם כבד ירד הבוקר בצפון. | שני כבישים נסגרו." ||
		strings.Join(second.Sentences[0].Sources, " ") != rainSiteg.URL || strings.Join(second.Sentences[1].Sources, " ") != rainSiteh.URL+" "+rainSiteE.URL+" "+rainSiteg.URL ||
		strings.Join(second.Reports, " ") != rainSiteh.URL+" "+rainSiteE.URL+" "+rainSiteg.URL {
		t.Fatalf("%+v", second)
	}
	// What's new is the development, with its kind; the first version's sentences are the update before it.
	if len(second.Updates) != 2 || !second.Updates[1].At.Equal(at) || texts(second.Updates[1].Sentences) != "נחל הקישון עלה על גדותיו." ||
		strings.Join(second.Updates[1].Kinds, " ") != Development || texts(second.Updates[0].Sentences) != texts(first.Sentences) {
		t.Fatalf("updates %+v", second.Updates)
	}
	if versions := versionsOf(t, w, first.ID); len(versions) != 2 || texts(versions[1].Sentences) != texts(second.Sentences) {
		t.Fatalf("versions %+v", versions)
	}
	if record, err := w.Store.StoryRecord(ctx, first.ID); err != nil || record == nil || !record.WrittenAt.Equal(at) || !record.FirstWritten.Equal(now) {
		t.Fatalf("record %+v %v", record, err)
	}
	// The step is recorded as the shadow records its own, marked as made in the story itself.
	if row := shadowOf(t, w, found[0]); row == nil || !row.Applied || !row.Version || row.Plan != PlanIncremental || row.Base != BaseLive || row.Request == nil || row.Request.Stories != 1 {
		t.Fatalf("row %+v", row)
	}
	// The request is one of the day's, and the shadow's share of the day is not counted.
	if n, _ := w.Store.GeminiRequests(ctx, Day(now)); n != 2 {
		t.Fatalf("%d requests counted", n)
	}
	if sent, _ := w.shadowSent(ctx, Day(now)); sent != 0 {
		t.Fatalf("%d shadow requests counted", sent)
	}
	// Written under its reports: not asked about again.
	if result, err := w.Run(ctx, found); err != nil || result != (Result{}) || len(rec.requests) != 1 {
		t.Fatalf("%+v %v, %d incremental requests", result, err, len(rec.requests))
	}

	// A fourth article, published before the story's version: its detail goes into the text quietly. No version:
	// the newest one is changed in place and keeps its time, its title and its updates, and nothing is deleted.
	preview := rainSited
	preview.PublishedAt = scrape.FormatInstant(now.Add(-2 * time.Hour))
	if result := join(t, w, found, preview, "מאז הלילה ירדו בחיפה 80 מילימטרים של גשם.", filler("הגשם")); result != (Result{}) || fake.count() != 1 || len(rec.requests) != 2 {
		t.Fatalf("%+v, %d whole and %d incremental requests", result, fake.count(), len(rec.requests))
	}
	if want := "s1.1 [מקורות: siteg] נחל הקישון עלה על גדותיו.\n"; !strings.Contains(rec.requests[1].Prompt, want) || !strings.Contains(rec.requests[1].Prompt, "פורסמה לפני שהסיפור נכתב") {
		t.Fatalf("the request lacks %q:\n%s", want, rec.requests[1].Prompt)
	}
	quiet := savedOf(t, w, found[0])
	if !quiet.WrittenAt.Equal(second.WrittenAt) || !quiet.UpdatedAt.Equal(w.Now()) || quiet.Title != second.Title || quiet.Full != 2 || len(quiet.Reports) != 4 ||
		texts(quiet.Sentences) != "נחל הקישון עלה על גדותיו. | גשם כבד ירד הבוקר בצפון. | בחיפה ירדו 80 מילימטרים של גשם. | שני כבישים נסגרו." ||
		strings.Join(quiet.Sentences[2].Sources, " ") != rainSited.URL {
		t.Fatalf("%+v", quiet)
	}
	if len(quiet.Updates) != 2 || texts(quiet.Updates[1].Sentences) != texts(second.Updates[1].Sentences) {
		t.Fatalf("updates %+v", quiet.Updates)
	}
	if versions := versionsOf(t, w, first.ID); len(versions) != 2 || texts(versions[1].Sentences) != texts(quiet.Sentences) || !versions[1].WrittenAt.Equal(second.WrittenAt) ||
		texts(versions[0].Sentences) != texts(first.Sentences) {
		t.Fatalf("versions %+v", versions)
	}
	// The story's record is its latest version still, at that version's time: its place among its threads' stories.
	if record, err := w.Store.StoryRecord(ctx, first.ID); err != nil || !record.WrittenAt.Equal(second.WrittenAt) || !strings.Contains(string(record.Summary), "80 מילימטרים") {
		t.Fatalf("record %+v %v", record, err)
	}
	if row := shadowOf(t, w, found[0]); row == nil || !row.Applied || row.Version || row.Quiet != 1 || row.Story == nil {
		t.Fatalf("row %+v", row)
	}

	// The budget story gains an article with nothing new: the story has it among its reports and its footnotes, and
	// is otherwise as it was.
	budget := savedOf(t, w, found[1])
	third := scrape.ArticleSummary{SourceID: "sitee", URL: "https://sitee/budget", Title: "התקציב עבר"}
	at = w.Now().Add(time.Minute)
	w.Now = func() time.Time { return at }
	if err := w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{third}, at); err != nil {
		t.Fatal(err)
	}
	found[1].Reports = append(found[1].Reports, third)
	if result, err := w.Run(ctx, found); err != nil || result != (Result{}) || fake.count() != 1 || len(rec.requests) != 3 {
		t.Fatalf("%+v %v, %d whole and %d incremental requests", result, err, fake.count(), len(rec.requests))
	}
	same := savedOf(t, w, found[1])
	if same.ID != budget.ID || !same.WrittenAt.Equal(budget.WrittenAt) || !same.UpdatedAt.Equal(at) || texts(same.Sentences) != texts(budget.Sentences) ||
		strings.Join(same.Reports, " ") != budgetSited.URL+" "+third.URL || strings.Join(same.Unrelated, " ") != budgetSiteg.URL ||
		strings.Join(same.Sentences[0].Sources, " ") != budgetSited.URL+" "+third.URL || len(same.Updates) != 1 || len(versionsOf(t, w, budget.ID)) != 1 {
		t.Fatalf("%+v", same)
	}
	if row := shadowOf(t, w, found[1]); row == nil || !row.Applied || row.Version || row.Quiet != 0 || *row.Stats != (MergeStats{Cited: 1}) {
		t.Fatalf("row %+v", row)
	}
	// Answered "nothing new" is answered: not asked again.
	if result, err := w.Run(ctx, found); err != nil || result != (Result{}) || len(rec.requests) != 3 {
		t.Fatalf("%+v %v, %d incremental requests", result, err, len(rec.requests))
	}

	// The story loses a report no sentence stood on alone: kept by the server, with no request, under its new
	// reports and without that footnote.
	found[0].Reports = found[0].Reports[1:]
	at = now.Add(time.Hour)
	w.Now = func() time.Time { return at }
	if result, err := w.Run(ctx, found); err != nil || result != (Result{}) || fake.count() != 1 || len(rec.requests) != 3 {
		t.Fatalf("%+v %v, %d whole and %d incremental requests", result, err, fake.count(), len(rec.requests))
	}
	kept := savedOf(t, w, found[0])
	if !kept.WrittenAt.Equal(second.WrittenAt) || !kept.UpdatedAt.Equal(at) || texts(kept.Sentences) != texts(quiet.Sentences) || len(kept.Reports) != 3 ||
		contains(kept.Sentences[1].Sources, rainSiteh.URL) || len(versionsOf(t, w, first.ID)) != 2 {
		t.Fatalf("%+v", kept)
	}
	if row := shadowOf(t, w, found[0]); row == nil || !row.Applied || row.Plan != PlanLocal || strings.Join(row.Gone, " ") != rainSiteh.URL {
		t.Fatalf("row %+v", row)
	}
	// Two saved stories share as many reports with the next one and were written at the same time: the one changed
	// last is the version before.
	saved, err := Summaries(ctx, w.Store)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if prev := previous(stories.Story{Reports: found[0].Reports[:2]}, saved); prev == nil || !prev.UpdatedAt.Equal(at) {
			t.Fatalf("the version before: %+v", prev)
		}
	}
	// What was recorded: four steps, all made in the stories; none is compared with a live rewrite.
	report, err := ReportShadow(ctx, w.Store, now.Add(-time.Hour))
	if err != nil || report.Steps != 4 || report.Applied != 4 || report.Local != 1 || report.Asked != 3 || report.Versions != 1 || report.Quiet != 2 || report.QuietUpdates != 1 || report.Compared != 0 {
		t.Fatalf("%+v %v", report, err)
	}

	// Off again, with nothing to migrate: the next change is written whole, as a version after the ones before.
	if err := SaveSettings(ctx, w.Store, Settings{FullArticles: true, Length: "short"}); err != nil {
		t.Fatal(err)
	}
	if result := join(t, w, found, scrape.ArticleSummary{SourceID: "sitef", URL: "https://sitef/rain/2", Title: "גשם"}); result != (Result{Written: 1}) || fake.count() != 2 || len(rec.requests) != 3 {
		t.Fatalf("%+v, %d whole and %d incremental requests", result, fake.count(), len(rec.requests))
	}
	whole := savedOf(t, w, found[0])
	if whole.ID != first.ID || !whole.WrittenAt.Equal(w.Now()) || !whole.UpdatedAt.IsZero() || whole.Full != 0 || len(whole.Updates) != 3 || len(versionsOf(t, w, first.ID)) != 3 {
		t.Fatalf("%+v", whole)
	}
	if !strings.Contains(fake.prompts[1], "- בחיפה ירדו 80 מילימטרים של גשם.\n") {
		t.Fatalf("the whole rewrite's version before:\n%s", fake.prompts[1])
	}
}

func TestOnAStoryGeminiSkippedIsAskedAgainThenWrittenWhole(t *testing.T) {
	w, fake, rec, found := turnedOn(t, shadowSkipped)
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	first := savedOf(t, w, found[0])
	// The answer names the new article nowhere: the story stays as it is, not written under its new reports, and
	// waits to be asked about again.
	if result := join(t, w, found, rainSiteg, news...); result != (Result{Waiting: 1}) || fake.count() != 1 || len(rec.requests) != 1 {
		t.Fatalf("%+v, %d whole and %d incremental requests", result, fake.count(), len(rec.requests))
	}
	saved, err := Summaries(ctx, w.Store)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := saved[StoryKey(found[0])]; ok || len(versionsOf(t, w, first.ID)) != 1 {
		t.Fatal("a skipped story was saved")
	}
	if row := shadowOf(t, w, found[0]); row == nil || !row.Applied || !row.Again || row.Skipped != 1 || row.Story != nil {
		t.Fatalf("row %+v", row)
	}
	// Skipped again: no third time. It is written whole, in the same run while it has a request.
	if result, err := w.Run(ctx, found); err != nil || result != (Result{Written: 1}) || fake.count() != 2 || len(rec.requests) != 2 || rec.requests[1].Prompt != rec.requests[0].Prompt {
		t.Fatalf("%+v %v, %d whole and %d incremental requests", result, err, fake.count(), len(rec.requests))
	}
	if row := shadowOf(t, w, found[0]); row == nil || row.Again || row.Skipped != 2 || row.Reason != whySkipped || row.Story != nil {
		t.Fatalf("row %+v", row)
	}
	if whole := savedOf(t, w, found[0]); whole.ID != first.ID || len(versionsOf(t, w, first.ID)) != 2 || whole.Full != 0 {
		t.Fatalf("%+v", whole)
	}
}

func TestOnTheTripwireSendsAStoryToBeWrittenWhole(t *testing.T) {
	w, fake, rec, found := turnedOn(t, shadowQuiet)
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	// One request a run, and Gemini finds nothing new in an article that is clearly news: nothing is saved of the
	// answer, and the story waits for its whole writing.
	w.MaxRequests = 1
	if result := join(t, w, found, rainSiteg, news...); result != (Result{Waiting: 1}) || fake.count() != 1 || len(rec.requests) != 1 {
		t.Fatalf("%+v, %d whole and %d incremental requests", result, fake.count(), len(rec.requests))
	}
	if row := shadowOf(t, w, found[0]); row == nil || !row.Applied || row.Story != nil || row.Again || !strings.Contains(row.Reason, "6 of an article's 6 sentences") {
		t.Fatalf("row %+v", row)
	}
	// The next run writes it whole, and doesn't ask about it again.
	if result, err := w.Run(ctx, found); err != nil || result != (Result{Written: 1}) || fake.count() != 2 || len(rec.requests) != 1 {
		t.Fatalf("%+v %v, %d whole and %d incremental requests", result, err, fake.count(), len(rec.requests))
	}
	if whole := savedOf(t, w, found[0]); len(whole.Updates) != 2 || len(whole.Reports) != 3 {
		t.Fatalf("%+v", whole)
	}
}

func TestOnAStoryThatDoubledIsWrittenWhole(t *testing.T) {
	w, fake, rec, found := turnedOn(t, shadowNews)
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	first := savedOf(t, w, found[0])
	// Three more articles at once, where the story was written from two: no update stands on that text. The whole
	// rewrite's request, and none of the update's.
	found[0].Reports = append(append([]scrape.ArticleSummary{}, found[0].Reports...), rainSiteg, rainSited)
	if result := join(t, w, found, scrape.ArticleSummary{SourceID: "sitef", URL: "https://sitef/rain", Title: "גשם"}); result != (Result{Written: 1}) || fake.count() != 2 || len(rec.requests) != 0 {
		t.Fatalf("%+v, %d whole and %d incremental requests", result, fake.count(), len(rec.requests))
	}
	if row := shadowOf(t, w, found[0]); row == nil || !row.Applied || row.Plan != PlanRebuild || row.Reason != "twice the articles of its last whole writing" || row.Story != nil {
		t.Fatalf("row %+v", row)
	}
	if whole := savedOf(t, w, found[0]); whole.ID != first.ID || whole.Full != 0 || len(versionsOf(t, w, first.ID)) != 2 {
		t.Fatalf("%+v", whole)
	}
}

func TestOnTheUpdatesAreTheRunsOwnRequests(t *testing.T) {
	cut := shadowNews[:strings.Index(shadowNews, `{"story": "s7"`)] + `{"story": "s2", "unrelated": [], "insert": [{"after": "top", "kind": "deve`
	w, fake, rec, found := turnedOn(t, "", cut, shadowQuiet)
	rec.errs = []error{gemini.ErrUnavailable, gemini.ErrCut}
	ctx := context.Background()
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	// The day's budget is used: the story waits, and nothing is sent.
	w.Budget = 1
	if result := join(t, w, found, rainSiteg); result != (Result{Waiting: 1}) || len(rec.requests) != 0 {
		t.Fatalf("%+v, %d incremental requests", result, len(rec.requests))
	}
	// Past the mark from which only new stories are written: held, as a whole rewrite is.
	w.Budget = 0
	if err := SaveSettings(ctx, w.Store, Settings{FullArticles: true, Length: "short", Incremental: IncrementalOn, ReserveAfter: 1}); err != nil {
		t.Fatal(err)
	}
	if result, err := w.Run(ctx, found); err != nil || result != (Result{Held: 1}) || len(rec.requests) != 0 {
		t.Fatalf("%+v %v, %d incremental requests", result, err, len(rec.requests))
	}
	// The shadow's share of the day is not the update's: used up, and the story is asked about all the same. A busy
	// Gemini ends the run, its request counted, and the story waits for the next.
	if err := SaveSettings(ctx, w.Store, Settings{FullArticles: true, Length: "short", Incremental: IncrementalOn, ShadowDaily: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.countShadow(ctx, Day(now)); err != nil {
		t.Fatal(err)
	}
	if result, err := w.Run(ctx, found); !errors.Is(err, gemini.ErrUnavailable) || result != (Result{Waiting: 1}) || len(rec.requests) != 1 {
		t.Fatalf("%+v %v, %d incremental requests", result, err, len(rec.requests))
	}
	if n, _ := w.Store.GeminiRequests(ctx, Day(now)); n != 2 {
		t.Fatalf("%d requests counted", n)
	}
	// The budget story gains a report too: both go in one request. The answer is cut inside the second story: the
	// whole one is a version, the other waits for the next run, which asks about it alone.
	at := w.Now().Add(time.Minute)
	w.Now = func() time.Time { return at }
	third := scrape.ArticleSummary{SourceID: "sitee", URL: "https://sitee/budget", Title: "התקציב עבר"}
	if err := w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{third}, at); err != nil {
		t.Fatal(err)
	}
	found[1].Reports = append(found[1].Reports, third)
	result, err := w.Run(ctx, found)
	if err != nil || result != (Result{Written: 1, Waiting: 1}) || len(rec.requests) != 2 || !strings.Contains(rec.requests[1].Prompt, "=== סיפור s2 ===") || fake.count() != 1 {
		t.Fatalf("%+v %v, %d whole and %d incremental requests", result, err, fake.count(), len(rec.requests))
	}
	if result, err := w.Run(ctx, found); err != nil || result != (Result{}) || len(rec.requests) != 3 || strings.Contains(rec.requests[2].Prompt, "s2") {
		t.Fatalf("%+v %v, %d incremental requests", result, err, len(rec.requests))
	}
	rows := 0
	for _, s := range found[:2] {
		if row := shadowOf(t, w, s); row != nil && row.Applied && row.Story != nil {
			rows++
		}
	}
	if rows != 2 {
		t.Fatalf("%d of the two stories were updated", rows)
	}
}
