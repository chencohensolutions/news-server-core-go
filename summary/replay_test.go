package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/backup"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
)

// filler makes a body long enough to be sent: a made-up paragraph about subject.
func filler(subject string) string {
	return "הכתבה המלאה על " + subject + " כוללת פרטים רבים שנמסרו לכתבים במהלך היום, ובהם תגובות של גורמים רשמיים, נתונים שפורסמו לציבור, ותיאור של מהלך האירועים כפי שנמסר מפי עדים שהיו במקום בזמן האירוע ולאחריו."
}

// replayArchiveOf makes an archive of five stories with two versions each, as a server wrote them:
//
//	rain    a third site joined, and the rewrite gave a new fact
//	budget  a third site joined, and the rewrite's "new" only said the story again
//	strike  a report left that no sentence stood on: no new article
//	fire    written before ids (only summaries rows, by key); a third site joined
//	vote    a report left that a sentence stood on, and another joined
func replayArchiveOf(t *testing.T) (dir string, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	db, err := store.Open(filepath.Join(tmp, "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	t0 := now.Add(-3 * time.Hour)
	report := func(site, subject, title string, body ...string) scrape.ArticleSummary {
		r := scrape.ArticleSummary{SourceID: site, URL: "https://" + site + "/" + subject, Title: title}
		if err := db.SaveHeadlines(ctx, []scrape.ArticleSummary{r}, t0); err != nil {
			t.Fatal(err)
		}
		if len(body) > 0 {
			if err := db.SaveArticle(ctx, &scrape.Article{SourceID: site, URL: r.URL, Title: title, Body: body}, "<html>page</html>", t0); err != nil {
				t.Fatal(err)
			}
		}
		return r
	}
	version := func(key, id string, at time.Time, sentences, added []Sentence, reports ...scrape.ArticleSummary) {
		s := StorySummary{Title: "כותרת " + key, Sentences: sentences, Contradictions: []Sentence{}, WrittenAt: at, ID: id}
		for _, r := range reports {
			s.Reports = append(s.Reports, r.URL)
		}
		if id != "" {
			s.Updates = []Update{{At: t0, Sentences: sentences[:1]}}
			if len(added) > 0 {
				s.Updates = append(s.Updates, Update{At: at, Sentences: added})
			}
		}
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveSummary(ctx, key, data, at); err != nil {
			t.Fatal(err)
		}
		if id != "" {
			// A version is in story_versions too: the replay reads it once.
			if err := db.SaveStoryVersion(ctx, id, data, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	rain := []scrape.ArticleSummary{report("siteh", "rain", "גשם בצפון", "גשם כבד ירד הבוקר בצפון.", filler("הגשם")), report("sitee", "rain", "סערה בצפון"),
		report("siteg", "rain", "הקישון עלה על גדותיו", "נחל הקישון עלה על גדותיו.", filler("הקישון"))}
	rainText := []Sentence{{"גשם כבד ירד הבוקר בצפון.", []string{rain[0].URL, rain[1].URL}}, {"2 כבישים נסגרו בגלל הגשם.", []string{rain[1].URL}}}
	version("rain1", "rain", t0, rainText, nil, rain[:2]...)
	version("rain2", "rain", t0.Add(60*time.Minute), rainText, []Sentence{{"נחל הקישון עלה על גדותיו.", []string{rain[2].URL}}}, rain...)

	budget := []scrape.ArticleSummary{report("sited", "budget", "התקציב אושר", "הכנסת אישרה את התקציב בקריאה שלישית.", filler("התקציב")), report("siteg", "budget", "הכנסת אישרה את התקציב"),
		report("sitee", "budget", "התקציב עבר", "התקציב עבר בכנסת.", filler("ההצבעה"))}
	budgetText := []Sentence{{"הכנסת אישרה את התקציב בקריאה שלישית.", []string{budget[0].URL, budget[1].URL}}, {"61 חברי כנסת תמכו בתקציב.", []string{budget[0].URL}}}
	version("budget1", "budget", t0.Add(10*time.Minute), budgetText, nil, budget[:2]...)
	version("budget2", "budget", t0.Add(70*time.Minute), budgetText, []Sentence{{"בקריאה שלישית אישרה הכנסת את התקציב.", []string{budget[2].URL}}}, budget...)

	strike := []scrape.ArticleSummary{report("siteh", "strike", "שביתה בנמלים", "עובדי הנמלים פתחו בשביתה.", filler("השביתה")), report("sitee", "strike", "הנמלים שובתים"), report("sited", "strike", "שביתה")}
	strikeText := []Sentence{{"עובדי הנמלים פתחו בשביתה.", []string{strike[0].URL, strike[1].URL}}, {"השביתה החלה בשעה 6 בבוקר.", []string{strike[0].URL}}}
	version("strike1", "strike", t0.Add(20*time.Minute), strikeText, nil, strike...)
	version("strike2", "strike", t0.Add(80*time.Minute), strikeText, nil, strike[:2]...)

	fire := []scrape.ArticleSummary{report("siteh", "fire", "שריפה בדרום", "שריפה פרצה בדרום.", filler("השריפה")), report("sitee", "fire", "שריפה גדולה"),
		report("siteg", "fire", "השריפה כובתה", "השריפה בדרום כובתה לאחר 5 שעות.", filler("הכיבוי"))}
	fireText := []Sentence{{"שריפה פרצה בדרום.", []string{fire[0].URL, fire[1].URL}}, {"תושבים פונו מבתיהם.", []string{fire[1].URL}}}
	version("fire1", "", t0.Add(30*time.Minute), fireText, nil, fire[:2]...)
	version("fire2", "", t0.Add(90*time.Minute), append([]Sentence{{"השריפה כובתה לאחר 5 שעות.", []string{fire[2].URL}}}, fireText...), nil, fire...)

	vote := []scrape.ArticleSummary{report("siteh", "vote", "הצבעה במועצה"), report("sitee", "vote", "המועצה הצביעה"), report("sited", "vote", "ההצבעה נדחתה")}
	voteText := []Sentence{{"מועצת העיר הצביעה על התוכנית.", []string{vote[0].URL}}, {"7 חברים תמכו בתוכנית.", []string{vote[1].URL}}}
	version("vote1", "vote", t0.Add(40*time.Minute), voteText, nil, vote[:2]...)
	version("vote2", "vote", t0.Add(100*time.Minute), voteText[:1], nil, vote[0], vote[2])

	dir = filepath.Join(tmp, "archive")
	if _, err := backup.Archive(ctx, db, dir, now); err != nil {
		t.Fatal(err)
	}
	return dir, db
}

func TestTheReplayFindsTheArchivesRewrites(t *testing.T) {
	dir, _ := replayArchiveOf(t)
	versions, err := ArchiveVersions(dir, time.Time{})
	if err != nil || len(versions) != 10 {
		t.Fatalf("%d versions, %v", len(versions), err)
	}
	steps := ReplaySteps(versions)
	var got []string
	for _, s := range steps {
		got = append(got, s.Prev.Title+" → "+s.Next.Title)
	}
	// Each story's second version with its first, in the order written; the story from before ids by its reports.
	want := "כותרת rain1 → כותרת rain2, כותרת budget1 → כותרת budget2, כותרת strike1 → כותרת strike2, כותרת fire1 → כותרת fire2, כותרת vote1 → כותרת vote2"
	if strings.Join(got, ", ") != want {
		t.Fatalf("steps\n got %s\nwant %s", strings.Join(got, ", "), want)
	}
	// What the live rewrite gave as new: its list; for a version from before lists, the sentences with nothing
	// like them before.
	if added := liveNew(steps[0].Prev, steps[0].Next); len(added) != 1 || added[0].Text != "נחל הקישון עלה על גדותיו." {
		t.Fatalf("%+v", added)
	}
	if added := liveNew(steps[3].Prev, steps[3].Next); len(added) != 1 || added[0].Text != "השריפה כובתה לאחר 5 שעות." {
		t.Fatalf("%+v", added)
	}
	if added := liveNew(steps[2].Prev, steps[2].Next); len(added) != 0 {
		t.Fatalf("%+v", added)
	}
	// Only the days asked for.
	if versions, err := ArchiveVersions(dir, now.Add(24*time.Hour)); err != nil || len(versions) != 0 {
		t.Fatalf("%d versions, %v", len(versions), err)
	}
}

// Recorded answers for the replay's request of the three steps: news in the rain story; nothing in the budget
// story; in the fire story a sentence the story has already.
const replayAnswer = `{"stories": [
	{"story": "s1", "unrelated": [], "insert": [{"after": "top", "kind": "development", "text": "נחל הקישון עלה על גדותיו.", "sources": ["a1"]}],
		"replace": [], "delete": [], "cite": [{"sentence": "s1.1", "sources": ["a1"]}], "title": "", "contradictions": []},
	{"story": "s2", "unrelated": [], "insert": [], "replace": [], "delete": [], "cite": [{"sentence": "s2.1", "sources": ["a2"]}], "title": "", "contradictions": []},
	{"story": "s3", "unrelated": [], "insert": [{"after": "top", "kind": "development", "text": "בדרום פרצה שריפה.", "sources": ["a3"]},
		{"after": "top", "kind": "background", "text": "באזור היו שריפות גם בקיץ שעבר.", "sources": ["a3"]}],
		"replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`

func TestTheReplayAsksAboutTheArchivesSteps(t *testing.T) {
	dir, db := replayArchiveOf(t)
	ctx := context.Background()
	rec := &recorded{answers: []string{replayAnswer}}
	var log bytes.Buffer
	writer := func() *Writer {
		return &Writer{Gemini: rec, Store: db, Now: func() time.Time { return now }, Sleep: func(context.Context, time.Duration) error { return nil },
			Log: func(format string, args ...any) { t.Logf(format, args...) }}
	}
	// Dry: the requests are built and sized, none is sent or counted.
	report, err := writer().Replay(ctx, ReplayOptions{Archive: dir, Dry: true, NoControls: true})
	if err != nil || len(rec.requests) != 0 || report.Requests != 1 || report.Asked != 3 || report.CharsIn == 0 || report.LiveCharsIn <= report.RealCharsIn-len([]rune(incrementalSystem)) {
		t.Fatalf("%+v %v, %d requests", report, err, len(rec.requests))
	}
	if n, _ := db.GeminiRequests(ctx, Day(now)); n != 0 {
		t.Fatalf("%d requests counted", n)
	}
	report, err = writer().Replay(ctx, ReplayOptions{Archive: dir, NoControls: true})
	if err != nil {
		t.Fatal(err)
	}
	report.Print(&log)
	t.Log(log.String())
	// Five rewrites: one kept by the server, one to be written whole, three asked about in one request.
	if report.Versions != 10 || report.Steps != 5 || report.Local != 1 || report.Rebuild["a sentence lost its sources"] != 1 || report.Eligible != 3 ||
		report.Requests != 1 || report.Asked != 3 || report.Answered != 3 || report.Missing != 0 {
		t.Fatalf("%+v", report)
	}
	prompt := rec.requests[0].Prompt
	for _, want := range []string{"s1.2 2 כבישים נסגרו בגלל הגשם. (sitee)", "כתבות שכבר בסיפור:\n- siteh: גשם בצפון\n- sitee: סערה בצפון\nכתבות חדשות:\n[a1] siteg\nכותרת: הקישון עלה על גדותיו\nגוף: נחל הקישון עלה על גדותיו.",
		"=== סיפור s2 ===\nכותרת הסיפור: כותרת budget1", "=== סיפור s3 ===\nכותרת הסיפור: כותרת fire1", "[a3] siteg\nכותרת: השריפה כובתה"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the request lacks %q:\n%s", want, prompt)
		}
	}
	// The articles the stories have are not sent again.
	if strings.Contains(prompt, filler("הגשם")) || strings.Contains(prompt, "strike") {
		t.Fatalf("the request:\n%s", prompt)
	}
	// Against the live rewrites: the rain story's news is found; the budget story's "new" was a restatement, and
	// nothing comes of it here either; the fire story's news is missed (the answer only said the story again).
	if report.Real != 3 || report.LiveNewVersion != 1 || report.LiveNewQuiet != 1 || report.LiveQuietQuiet != 1 || report.LiveQuietVersion != 0 || report.LiveListed != 3 {
		t.Fatalf("%+v", report)
	}
	if report.Stats != (MergeStats{Asked: 3, Inserted: 1, Cited: 2, Restated: 1, Unapplied: 1}) || report.Kinds[Development] != 1 {
		t.Fatalf("%+v %v", report.Stats, report.Kinds)
	}
	if report.TokensIn != 500 || report.TokensOut != 80 || report.Positions[0] != (PositionStats{Asked: 1, Answered: 1, Steps: 1, Agree: 1}) ||
		report.Positions[2] != (PositionStats{Asked: 1, Answered: 1, Steps: 1}) {
		t.Fatalf("%+v", report)
	}
	// The request is one of the day's, in the database the replay was given.
	if n, _ := db.GeminiRequests(ctx, Day(now)); n != 1 {
		t.Fatalf("%d requests counted", n)
	}
	for _, want := range []string{"10 story versions in the archive, 5 of them rewrites", "live had new facts:  1 a version, 1 none: recall 50%", "restatements: 33%"} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("the report lacks %q:\n%s", want, log.String())
		}
	}
	// No request past the day's budget, nor past -max-requests.
	w := writer()
	w.Budget = 1
	if report, err := w.Replay(ctx, ReplayOptions{Archive: dir, NoControls: true}); err != nil || report.Requests != 0 || !strings.Contains(report.Stopped, "the day's 1 Gemini requests are used") {
		t.Fatalf("%+v %v", report, err)
	}
	if report, err := writer().Replay(ctx, ReplayOptions{Archive: dir, NoControls: true, Batch: 2, MaxRequests: 1}); err != nil || report.Requests != 1 || report.Asked != 2 || !strings.Contains(report.Stopped, "1 stories were not asked") {
		t.Fatalf("%+v %v", report, err)
	}
	// Only the last day's steps: the archive's are all of one day.
	if report, err := writer().Replay(ctx, ReplayOptions{Archive: dir, Dry: true, Days: 1}); err != nil || report.Steps != 5 {
		t.Fatalf("%+v %v", report, err)
	}
	if _, err := writer().Replay(ctx, ReplayOptions{Archive: t.TempDir(), Dry: true}); err == nil {
		t.Fatal("an empty folder is no archive")
	}
}

// Recorded answers for the replay's two requests with controls. The first: a story shown one of its own articles
// as new (s1), where nothing is found; the rain step (s2); a story shown the rain story's article (s3), found
// unrelated; the budget step (s4). The second: the fire step (s1); a story shown one of its own articles (s2), and a
// sentence written from it all the same; a story shown another's article (s3), not found unrelated.
var controlAnswers = []string{`{"stories": [
	{"story": "s1", "unrelated": [], "insert": [], "replace": [], "delete": [], "cite": [{"sentence": "s1.1", "sources": ["a1"]}], "title": "", "contradictions": []},
	{"story": "s2", "unrelated": [], "insert": [{"after": "top", "kind": "development", "text": "נחל הקישון עלה על גדותיו.", "sources": ["a2"]}], "replace": [], "delete": [], "cite": [], "title": "", "contradictions": []},
	{"story": "s3", "unrelated": [{"id": "a3", "event": "גשם בצפון"}], "insert": [], "replace": [], "delete": [], "cite": [], "title": "", "contradictions": []},
	{"story": "s4", "unrelated": [], "insert": [], "replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`,
	`{"stories": [
	{"story": "s1", "unrelated": [], "insert": [], "replace": [], "delete": [], "cite": [], "title": "", "contradictions": []},
	{"story": "s2", "unrelated": [], "insert": [{"after": "top", "kind": "development", "text": "שר האוצר בירך על אישור התקציב.", "sources": ["a2"]}], "replace": [], "delete": [], "cite": [], "title": "", "contradictions": []},
	{"story": "s3", "unrelated": [], "insert": [], "replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`}

func TestTheReplaysControls(t *testing.T) {
	dir, db := replayArchiveOf(t)
	rec := &recorded{answers: controlAnswers}
	w := &Writer{Gemini: rec, Store: db, Now: func() time.Time { return now }, Log: t.Logf}
	report, err := w.Replay(context.Background(), ReplayOptions{Archive: dir, Batch: 4})
	if err != nil {
		t.Fatal(err)
	}
	// Two controls a request, at places that move: 1 and 3 of the first request, 2 and 3 of the second (which has
	// one step left).
	if len(rec.requests) != 2 || report.Asked != 7 || report.Answered != 7 || report.Real != 3 {
		t.Fatalf("%d requests, %+v", len(rec.requests), report)
	}
	first, second := rec.requests[0].Prompt, rec.requests[1].Prompt
	// An article the story has, shown as new with its body, while the story's sentences still cite its site.
	if !strings.Contains(first, "s1.2 השביתה החלה בשעה 6 בבוקר. (siteh)\nכתבות שכבר בסיפור:\n- sitee: הנמלים שובתים\nכתבות חדשות:\n[a1] siteh\nכותרת: שביתה בנמלים\nגוף: עובדי הנמלים פתחו בשביתה.") {
		t.Fatalf("the first request:\n%s", first)
	}
	// And an article of another story, after a story's own.
	if !strings.Contains(first, "- sited: ההצבעה נדחתה\nכתבות חדשות:\n[a3] siteh\nכותרת: גשם בצפון") || !strings.Contains(second, "=== סיפור s1 ===\nכותרת הסיפור: כותרת fire1") {
		t.Fatalf("the requests:\n%s\n%s", first, second)
	}
	if report.RepeatAsked != 2 || report.RepeatPassed != 1 || report.StrangerAsked != 2 || report.StrangerPassed != 1 {
		t.Fatalf("%+v", report)
	}
	// The controls stay out of the steps' measures.
	if report.LiveNewVersion != 1 || report.LiveNewQuiet != 1 || report.LiveQuietQuiet != 1 || report.Stats != (MergeStats{Asked: 1, Inserted: 1}) {
		t.Fatalf("%+v", report)
	}
	want := []PositionStats{
		{Asked: 2, Answered: 2, Steps: 1, Controls: 1, Passed: 1},
		{Asked: 2, Answered: 2, Steps: 1, Agree: 1, Controls: 1},
		{Asked: 2, Answered: 2, Controls: 2, Passed: 1},
		{Asked: 1, Answered: 1, Steps: 1, Agree: 1},
	}
	for i, p := range report.Positions {
		if p != want[i] {
			t.Fatalf("place %d: %+v, want %+v", i+1, p, want[i])
		}
	}
	var log bytes.Buffer
	report.Print(&log)
	for _, want := range []string{"an article the story has, shown as new, made nothing: 1 of 2 (50%)", "an article of another story was found unrelated:      1 of 2 (50%)", "    3: 2 of 2; 1 of 2; 0 of 0"} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("the report lacks %q:\n%s", want, log.String())
		}
	}
}
