package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
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
//
// The articles that joined were first seen after the first versions were written, and the sites give them no time.
func replayArchiveOf(t *testing.T, more ...func(a *archiveMaker)) (dir string, db *store.Store) {
	t.Helper()
	a := newArchiveMaker(t, now.Add(-3*time.Hour))
	t0, report, version := a.t0, a.report, a.version
	joined := func(site, subject, title string, body ...string) scrape.ArticleSummary {
		return a.reportAt(t0.Add(50*time.Minute), site, subject, title, body...)
	}
	rain := []scrape.ArticleSummary{report("siteh", "rain", "גשם בצפון", "גשם כבד ירד הבוקר בצפון.", filler("הגשם")), report("sitee", "rain", "סערה בצפון"),
		joined("siteg", "rain", "הקישון עלה על גדותיו", "נחל הקישון עלה על גדותיו.", filler("הקישון"))}
	rainText := []Sentence{{"גשם כבד ירד הבוקר בצפון.", []string{rain[0].URL, rain[1].URL}}, {"2 כבישים נסגרו בגלל הגשם.", []string{rain[1].URL}}}
	version("rain1", "rain", t0, rainText, nil, rain[:2]...)
	version("rain2", "rain", t0.Add(60*time.Minute), rainText, []Sentence{{"נחל הקישון עלה על גדותיו.", []string{rain[2].URL}}}, rain...)

	budget := []scrape.ArticleSummary{report("sited", "budget", "התקציב אושר", "הכנסת אישרה את התקציב בקריאה שלישית.", filler("התקציב")), report("siteg", "budget", "הכנסת אישרה את התקציב"),
		joined("sitee", "budget", "התקציב עבר", "התקציב עבר בכנסת.", filler("ההצבעה"))}
	budgetText := []Sentence{{"הכנסת אישרה את התקציב בקריאה שלישית.", []string{budget[0].URL, budget[1].URL}}, {"61 חברי כנסת תמכו בתקציב.", []string{budget[0].URL}}}
	version("budget1", "budget", t0.Add(10*time.Minute), budgetText, nil, budget[:2]...)
	version("budget2", "budget", t0.Add(70*time.Minute), budgetText, []Sentence{{"בקריאה שלישית אישרה הכנסת את התקציב.", []string{budget[2].URL}}}, budget...)

	strike := []scrape.ArticleSummary{report("siteh", "strike", "שביתה בנמלים", "עובדי הנמלים פתחו בשביתה.", filler("השביתה")), report("sitee", "strike", "הנמלים שובתים"), report("sited", "strike", "שביתה")}
	strikeText := []Sentence{{"עובדי הנמלים פתחו בשביתה.", []string{strike[0].URL, strike[1].URL}}, {"השביתה החלה בשעה 6 בבוקר.", []string{strike[0].URL}}}
	version("strike1", "strike", t0.Add(20*time.Minute), strikeText, nil, strike...)
	version("strike2", "strike", t0.Add(80*time.Minute), strikeText, nil, strike[:2]...)

	fire := []scrape.ArticleSummary{report("siteh", "fire", "שריפה בדרום", "שריפה פרצה בדרום.", filler("השריפה")), report("sitee", "fire", "שריפה גדולה"),
		joined("siteg", "fire", "השריפה כובתה", "השריפה בדרום כובתה לאחר 5 שעות.", filler("הכיבוי"))}
	fireText := []Sentence{{"שריפה פרצה בדרום.", []string{fire[0].URL, fire[1].URL}}, {"תושבים פונו מבתיהם.", []string{fire[1].URL}}}
	version("fire1", "", t0.Add(30*time.Minute), fireText, nil, fire[:2]...)
	version("fire2", "", t0.Add(90*time.Minute), append([]Sentence{{"השריפה כובתה לאחר 5 שעות.", []string{fire[2].URL}}}, fireText...), nil, fire...)

	vote := []scrape.ArticleSummary{report("siteh", "vote", "הצבעה במועצה"), report("sitee", "vote", "המועצה הצביעה"), joined("sited", "vote", "ההצבעה נדחתה")}
	voteText := []Sentence{{"מועצת העיר הצביעה על התוכנית.", []string{vote[0].URL}}, {"7 חברים תמכו בתוכנית.", []string{vote[1].URL}}}
	version("vote1", "vote", t0.Add(40*time.Minute), voteText, nil, vote[:2]...)
	version("vote2", "vote", t0.Add(100*time.Minute), voteText[:1], nil, vote[0], vote[2])

	for _, add := range more {
		add(a)
	}
	return a.archive(), a.db
}

// archiveMaker saves reports and story versions as a server did, and archives them for the replay.
type archiveMaker struct {
	t   *testing.T
	tmp string
	db  *store.Store
	// t0 is when the reports that follow are first seen, and the stories that follow first written.
	t0 time.Time
	// published is the site's time of the reports that follow; "" for none.
	published string
}

func newArchiveMaker(t *testing.T, t0 time.Time) *archiveMaker {
	t.Helper()
	a := &archiveMaker{t: t, tmp: t.TempDir(), t0: t0}
	db, err := store.Open(filepath.Join(a.tmp, "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a.db = db
	return a
}

// report saves a site's headline about subject, and with a body its article.
func (a *archiveMaker) report(site, subject, title string, body ...string) scrape.ArticleSummary {
	return a.reportAt(a.t0, site, subject, title, body...)
}

// reportAt is report for an article first seen at at.
func (a *archiveMaker) reportAt(at time.Time, site, subject, title string, body ...string) scrape.ArticleSummary {
	ctx := context.Background()
	r := scrape.ArticleSummary{SourceID: site, URL: "https://" + site + "/" + subject, Title: title, PublishedAt: a.published}
	if err := a.db.SaveHeadlines(ctx, []scrape.ArticleSummary{r}, at); err != nil {
		a.t.Fatal(err)
	}
	if len(body) > 0 {
		if err := a.db.SaveArticle(ctx, &scrape.Article{SourceID: site, URL: r.URL, Title: title, Body: body}, "<html>page</html>", at); err != nil {
			a.t.Fatal(err)
		}
	}
	return r
}

// version saves a story's version written at at from reports, under key and, from ids on, its id; added are the
// sentences the rewrite gave as new.
func (a *archiveMaker) version(key, id string, at time.Time, sentences, added []Sentence, reports ...scrape.ArticleSummary) {
	ctx := context.Background()
	s := StorySummary{Title: "כותרת " + key, Sentences: sentences, Contradictions: []Sentence{}, WrittenAt: at, ID: id}
	for _, r := range reports {
		s.Reports = append(s.Reports, r.URL)
	}
	if id != "" {
		s.Updates = []Update{{At: a.t0, Sentences: sentences[:1]}}
		if len(added) > 0 {
			s.Updates = append(s.Updates, Update{At: at, Sentences: added})
		}
	}
	data, err := json.Marshal(s)
	if err != nil {
		a.t.Fatal(err)
	}
	if err := a.db.SaveSummary(ctx, key, data, at); err != nil {
		a.t.Fatal(err)
	}
	if id != "" {
		// A version is in story_versions too: the replay reads it once.
		if err := a.db.SaveStoryVersion(ctx, id, data, at); err != nil {
			a.t.Fatal(err)
		}
	}
}

// archive archives what was saved, and returns the archive's folder.
func (a *archiveMaker) archive() string {
	dir := filepath.Join(a.tmp, "archive")
	if _, err := backup.Archive(context.Background(), a.db, dir, now); err != nil {
		a.t.Fatal(err)
	}
	return dir
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
	for _, want := range []string{"s1.2 [מקורות: sitee] 2 כבישים נסגרו בגלל הגשם.\n", "כתבות שכבר בסיפור:\n- siteh: גשם בצפון\n- sitee: סערה בצפון\nכתבות חדשות:\n[a1] siteg · פורסמה אחרי שהסיפור נכתב\nכותרת: הקישון עלה על גדותיו\nגוף: נחל הקישון עלה על גדותיו.",
		"=== סיפור s2 ===\nכותרת הסיפור: כותרת budget1", "=== סיפור s3 ===\nכותרת הסיפור: כותרת fire1", "[a3] siteg · פורסמה אחרי שהסיפור נכתב\nכותרת: השריפה כובתה"} {
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
	if report.Stats != (MergeStats{Asked: 3, Inserted: 1, Cited: 2, Restated: 1, Quiet: 1}) || report.Kinds[Development] != 1 {
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
	for _, want := range []string{"10 story versions in the archive, 5 of them rewrites", "live had new facts:  1 a version, 1 none, 0 skipped by Gemini: recall 50% without the skipped, 50% with them as misses", "restatements: 33%",
		"the real steps' 3 new articles: 3 (100%) published after the version before, 0 (0%) before it, 0 of no known time"} {
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
// unrelated; the budget step (s4), whose new article is named nowhere. The second: the fire step (s1), its article
// named nowhere either; a story shown one of its own articles (s2), and a development written from it all the same;
// a story shown another's article (s3), not found unrelated: named nowhere.
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
	var cases bytes.Buffer
	w := &Writer{Gemini: rec, Store: db, Now: func() time.Time { return now }, Log: t.Logf}
	report, err := w.Replay(context.Background(), ReplayOptions{Archive: dir, Batch: 4, Cases: &cases})
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
	if !strings.Contains(first, "s1.2 [מקורות: siteh] השביתה החלה בשעה 6 בבוקר.\nכתבות שכבר בסיפור:\n- sitee: הנמלים שובתים\nכתבות חדשות:\n[a1] siteh · פורסמה לפני שהסיפור נכתב\nכותרת: שביתה בנמלים\nגוף: עובדי הנמלים פתחו בשביתה.") {
		t.Fatalf("the first request:\n%s", first)
	}
	// And an article of another story, after a story's own.
	if !strings.Contains(first, "- sited: ההצבעה נדחתה\nכתבות חדשות:\n[a3] siteh · פורסמה לפני שהסיפור נכתב\nכותרת: גשם בצפון") || !strings.Contains(second, "=== סיפור s1 ===\nכותרת הסיפור: כותרת fire1") {
		t.Fatalf("the requests:\n%s\n%s", first, second)
	}
	// No version comes of a story's own article, whatever was written from it: it was published before the story
	// was written, so its development makes none. Nor does it go into the story quietly: a story's own article,
	// shown again, adds nothing. It is counted: the padding that rule keeps out. The stranger that was named
	// nowhere is not found.
	if report.RepeatAsked != 2 || report.RepeatPassed != 2 || report.RepeatSkipped != 0 || report.RepeatReshown != 1 || report.StrangerAsked != 2 || report.StrangerPassed != 1 ||
		report.StrangerSkipped != 1 {
		t.Fatalf("%+v", report)
	}
	// That control is among the cases, to be read: it passed, and its sentence is marked with the rule.
	var padded *ReplayCase
	for _, c := range casesOf(t, cases.Bytes()) {
		if c.Kind == CaseRepeatReshown {
			padded = &c
		}
	}
	if padded == nil || padded.N != 6 || padded.Merge.Version || padded.Merge.Stats.Quiet != 0 || padded.Merge.Stats.Reshown != 1 || padded.Merge.Notes[0].Outcome != NoteLeftOut ||
		padded.Merge.Notes[0].Why != whyReshown || !padded.Merge.Notes[0].Old || padded.Merge.Notes[0].Kind != Development ||
		padded.New[0].When != "before" || report.Cases != 5 {
		t.Fatalf("%+v, %d cases", padded, report.Cases)
	}
	// The controls stay out of the steps' measures. Of the three steps, two were skipped by Gemini: they are
	// neither a version nor "nothing new", and are counted apart, the one whose live rewrite had new facts (the
	// fire) among them.
	if report.LiveNewVersion != 1 || report.LiveNewQuiet != 0 || report.LiveQuietQuiet != 0 || report.Stats != (MergeStats{Asked: 1, Inserted: 1}) ||
		report.GeminiSkipped != 2 || report.SkippedNews != 1 || report.SkippedTripwire != 0 || report.Unmentioned != 2 || report.Tripwire != 0 {
		t.Fatalf("%+v", report)
	}
	want := []PositionStats{
		{Asked: 2, Answered: 2, Controls: 1, Passed: 1},
		{Asked: 2, Answered: 2, Steps: 1, Agree: 1, Controls: 1, Passed: 1},
		{Asked: 2, Answered: 2, Controls: 2, Passed: 1},
		{Asked: 1, Answered: 1},
	}
	for i, p := range report.Positions {
		if p != want[i] {
			t.Fatalf("place %d: %+v, want %+v", i+1, p, want[i])
		}
	}
	var log bytes.Buffer
	report.Print(&log)
	for _, want := range []string{"an article the story has, shown as new, made no version: 2 of 2 (100%); 0 of the answers named no new article (skipped: nothing came of them); sentences written from it alone were kept out of the story in 1",
		"an article of another story was found unrelated:      1 of 2 (50%); 1 of the answers named no new article", "    3: 2 of 2; 1 of 2; 0 of 0",
		`skipped by Gemini (the answer names none of the story's new articles; not "nothing new": asked again): 2 (67%); the live rewrite had new facts in 1 of them`,
		"of the 1 not skipped, a new version: 1 (100%); no version: 0 (0%)",
		"live had new facts:  1 a version, 0 none, 1 skipped by Gemini: recall 100% without the skipped, 50% with them as misses",
		"the tripwire would write 0 of the 1 skipped whole"} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("the report lacks %q:\n%s", want, log.String())
		}
	}
}

// casesOf are the lines of a cases file.
func casesOf(t *testing.T, data []byte) []ReplayCase {
	t.Helper()
	var out []ReplayCase
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var c ReplayCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("%v in %s", err, line)
		}
		out = append(out, c)
	}
	return out
}

// lake adds a sixth story to the replay's archive: a third site joined with an article that is clearly news by its
// words alone (news), and the rewrite gave a new fact.
func lake(a *archiveMaker) {
	reports := []scrape.ArticleSummary{a.report("siteh", "lake", "מפלס הכנרת נמדד", "מפלס הכנרת נמדד הבוקר.", filler("המדידה")), a.report("sitee", "lake", "מדידה בכנרת"),
		a.reportAt(a.t0.Add(100*time.Minute), "siteg", "lake", "הכנרת עלתה", news...)}
	text := []Sentence{{"מפלס הכנרת נמדד הבוקר.", []string{reports[0].URL, reports[1].URL}}, {"המדידה נערכה בתחנה שבצפון האגם.", []string{reports[1].URL}}}
	a.version("lake1", "lake", a.t0.Add(50*time.Minute), text, nil, reports[:2]...)
	a.version("lake2", "lake", a.t0.Add(110*time.Minute), text, []Sentence{{"מפלס הכנרת עלה ב-14 סנטימטרים.", []string{reports[2].URL}}}, reports...)
}

// The replay's answer (replayAnswer) with a fourth story, the lake's, whose new article Gemini named nowhere.
var lakeAnswer = strings.TrimSuffix(replayAnswer, "]}") + `,
	{"story": "s4", "unrelated": [], "insert": [], "replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`

func TestTheReplayExplainsItsSteps(t *testing.T) {
	dir, db := replayArchiveOf(t, lake)
	rec := &recorded{answers: []string{lakeAnswer}}
	var cases bytes.Buffer
	w := &Writer{Gemini: rec, Store: db, Now: func() time.Time { return now }, Log: t.Logf}
	report, err := w.Replay(context.Background(), ReplayOptions{Archive: dir, NoControls: true, Cases: &cases})
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	report.Print(&log)
	t.Log(log.String())
	// The live rewrite had new facts in three of the four steps, and the update made a version of one (the rain).
	// In the fire story Gemini answered a sound sentence, background, that made no version and went in quietly: a
	// miss. The
	// lake story it skipped: counted apart, and recall is given without it and with it as a miss. The tripwire would
	// write it whole.
	if report.Real != 4 || report.LiveNewVersion != 1 || report.LiveNewQuiet != 1 || report.LiveQuietQuiet != 1 || report.Tripwire != 0 ||
		report.MissedFound != 1 || report.MissedTripwire != 0 || report.MissedBoth != 0 || report.MissedDropped != 0 || report.QuietFound != 0 || report.QuietTripwire != 0 ||
		report.GeminiSkipped != 1 || report.SkippedNews != 1 || report.SkippedTripwire != 1 || report.Unmentioned != 1 || report.ArticlesAfter != 4 || report.ArticlesBefore != 0 {
		t.Fatalf("%+v", report)
	}
	if report.QuietKinds[Background] != 1 || len(report.QuietKinds) != 1 || len(report.LeftOutKinds) != 0 || report.Kinds[Development] != 1 || report.Cases != 4 {
		t.Fatalf("%+v", report)
	}
	for _, want := range []string{
		"the archive's days up to 2026-11-10 (UTC)",
		`skipped by Gemini (the answer names none of the story's new articles; not "nothing new": asked again): 1 (25%); the live rewrite had new facts in 1 of them`,
		"live had new facts:  1 a version, 1 none, 1 skipped by Gemini: recall 50% without the skipped, 33% with them as misses",
		"the tripwire would write 1 of the 1 skipped whole",
		"the 1 without a version, by what Gemini answered: sound sentences that made no version in 1; only sentences the other rules dropped in 0; no sentence in 0",
		"the tripwire would write 0 of the 1 whole (0 of them with sound sentences that made no version); neither in 0",
		"recall counting the sound sentences that made no version (Gemini found facts, a version or not): 100%",
		"recall counting the tripwire's whole writings (a version, or written whole): 50%; counting both: 100%",
		"the 1 without a version: sound sentences that made no version in 0; the tripwire would write 0 whole (0 of them with sound sentences that made no version)",
		"added quietly with no version made, by kind: 0 development, 0 correction, 0 detail, 1 background, 0 reaction",
	} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("the report lacks %q:\n%s", want, log.String())
		}
	}

	// The cases: a line for each step answered, with what was sent and what came of it.
	got := casesOf(t, cases.Bytes())
	var kinds []string
	for _, c := range got {
		kinds = append(kinds, c.Story+" "+c.Kind)
	}
	if strings.Join(kinds, ", ") != "rain agree-version, budget agree-none, fire1 missed, lake skipped" {
		t.Fatalf("cases: %s", strings.Join(kinds, ", "))
	}
	prompt := rec.requests[0].Prompt
	rain := got[0]
	if rain.N != 1 || rain.Request != 1 || rain.Position != 1 || rain.ID != "s1" || rain.Title != "כותרת rain1" || !rain.At.Equal(now.Add(-2*time.Hour)) || !rain.PrevAt.Equal(now.Add(-3*time.Hour)) {
		t.Fatalf("%+v", rain)
	}
	// The version before as the request numbered it, the articles it had by their titles, and the new article as
	// the request showed it: its body is the request's, to the letter.
	if len(rain.Prev) != 2 || rain.Prev[1].N != "s1.2" || rain.Prev[1].Text != "2 כבישים נסגרו בגלל הגשם." || strings.Join(rain.Prev[0].Sites, ",") != "siteh,sitee" ||
		!strings.Contains(prompt, rain.Prev[1].N+" [מקורות: sitee] "+rain.Prev[1].Text+"\n") {
		t.Fatalf("%+v", rain.Prev)
	}
	if len(rain.Old) != 2 || rain.Old[0].Title != "גשם בצפון" || rain.Old[0].Body != "" || rain.Old[0].ID != "" {
		t.Fatalf("%+v", rain.Old)
	}
	if len(rain.New) != 1 || rain.New[0].ID != "a1" || rain.New[0].Site != "siteg" || rain.New[0].URL != "https://siteg/rain" || rain.New[0].Title != "הקישון עלה על גדותיו" ||
		!strings.HasPrefix(rain.New[0].Body, "נחל הקישון עלה על גדותיו.") || !strings.Contains(prompt, "[a1] siteg · פורסמה אחרי שהסיפור נכתב\nכותרת: הקישון עלה על גדותיו\nגוף: "+rain.New[0].Body+"\n") || rain.New[0].When != "after" {
		t.Fatalf("%+v", rain.New)
	}
	// The answer as it came, and what the rules made of it.
	if len(rain.Answer.Insert) != 1 || rain.Answer.Insert[0].Kind != "development" || len(rain.Answer.Cite) != 1 || !rain.Merge.Version || rain.Merge.Tripwire ||
		strings.Join(notesOf(Merged{Notes: rain.Merge.Notes}), "; ") != "insert top: applied, makes a version; cite s1.1: applied" || rain.Merge.Stats != (MergeStats{Asked: 1, Inserted: 1, Cited: 1}) {
		t.Fatalf("%+v %+v", rain.Answer, rain.Merge)
	}
	// The live rewrite of the same step: what it gave as new, with the new article it stands on, and all of it.
	if rain.Live == nil || len(rain.Live.New) != 1 || rain.Live.New[0].Text != "נחל הקישון עלה על גדותיו." || strings.Join(rain.Live.New[0].Articles, ",") != "a1" ||
		strings.Join(rain.Live.New[0].Sites, ",") != "siteg" || len(rain.Live.Restated) != 0 || len(rain.Live.Sentences) != 2 || rain.Live.Title != "כותרת rain2" {
		t.Fatalf("%+v", rain.Live)
	}
	// What the live rewrite gave as new and the version before said already is apart, as the measure has it.
	budget := got[1]
	if len(budget.Live.New) != 0 || len(budget.Live.Restated) != 1 || budget.Live.Restated[0].Text != "בקריאה שלישית אישרה הכנסת את התקציב." || budget.Merge.Version ||
		strings.Join(notesOf(Merged{Notes: budget.Merge.Notes}), "; ") != "cite s2.1: applied" {
		t.Fatalf("%+v %+v", budget.Live, budget.Merge)
	}
	// A miss, by the rules: one sentence said the story again, and the other was sound and made no version: it
	// went into the story quietly.
	fire := got[2]
	wantNotes := "insert top: dropped (says again what s3.1 does); insert top: quiet (" + whyQuietPlain + ")"
	if fire.ID != "s3" || fire.Merge.Version || fire.Merge.Tripwire || strings.Join(notesOf(Merged{Notes: fire.Merge.Notes}), "; ") != wantNotes ||
		fire.Merge.Notes[1].Kind != Background || fire.Merge.Notes[1].Text != "באזור היו שריפות גם בקיץ שעבר." || fire.Merge.Stats.Quiet != 1 || fire.Merge.Stats.Unapplied != 0 ||
		len(fire.Live.New) != 1 || fire.Live.New[0].Text != "השריפה כובתה לאחר 5 שעות." {
		t.Fatalf("%+v %+v", fire.Merge, fire.Live)
	}
	// The story Gemini skipped: its article is named nowhere, and is news by its words.
	lake := got[3]
	if lake.Merge.Version || !lake.Merge.Skipped || strings.Join(lake.Merge.Unmentioned, ",") != "a4" || !lake.Merge.Tripwire ||
		!strings.Contains(lake.Merge.TripwireWhy, "6 of an article's 6 sentences") || len(lake.Merge.Notes) != 0 ||
		!strings.Contains(cases.String(), `"notes":[]`) || len(lake.Live.New) != 1 || !strings.Contains(lake.New[0].Body, news[5]) {
		t.Fatalf("%+v %+v", lake.Merge, lake.Live)
	}
	if rain.Merge.Skipped || fire.Merge.Skipped || len(fire.Merge.Unmentioned) != 0 {
		t.Fatalf("%+v %+v", rain.Merge, fire.Merge)
	}

	// When every sentence Gemini answered is dropped by the other rules, the miss is counted apart: here the lake's
	// only sentence is cut short. It names the article, so the story was not skipped: a miss the tripwire catches.
	rec = &recorded{answers: []string{strings.Replace(lakeAnswer, `{"story": "s4", "unrelated": [], "insert": []`,
		`{"story": "s4", "unrelated": [], "insert": [{"after": "top", "kind": "development", "text": "מפלס הכנרת עלה ב", "sources": ["a4"]}]`, 1)}}
	w.Gemini = rec
	cases.Reset()
	report, err = w.Replay(context.Background(), ReplayOptions{Archive: dir, NoControls: true, Cases: &cases})
	if err != nil || report.MissedDropped != 1 || report.MissedFound != 1 || report.MissedTripwire != 1 || report.Tripwire != 1 || report.Stats.Garbled != 1 || report.GeminiSkipped != 0 ||
		report.LiveNewQuiet != 2 {
		t.Fatalf("%v %+v", err, report)
	}
	if got := casesOf(t, cases.Bytes()); got[3].Kind != CaseMissed || strings.Join(notesOf(Merged{Notes: got[3].Merge.Notes}), "; ") != "insert top: dropped ("+whyGarbled+")" || !got[3].Merge.Tripwire {
		t.Fatalf("%+v", got[3].Merge)
	}
}

// festival adds a story to the replay's archive whose third article is a preview: the site published it before the
// story's first version was written, and it joined the story after it. The rewrite gave a fact of it as new.
func festival(a *archiveMaker) {
	reports := []scrape.ArticleSummary{a.report("siteh", "festival", "הפסטיבל נפתח", "פסטיבל הסרטים נפתח הערב בעיר.", filler("הפתיחה")), a.report("sitee", "festival", "ערב הפתיחה של הפסטיבל")}
	a.published = scrape.FormatInstant(a.t0.Add(-time.Hour))
	reports = append(reports, a.reportAt(a.t0.Add(100*time.Minute), "siteg", "festival", "לקראת הפסטיבל", "בפסטיבל יוקרנו 40 סרטים.", filler("התוכנית")))
	a.published = ""
	text := []Sentence{{"פסטיבל הסרטים נפתח הערב בעיר.", []string{reports[0].URL, reports[1].URL}}, {"ראש העיר נשא דברים בטקס הפתיחה.", []string{reports[1].URL}}}
	a.version("festival1", "festival", a.t0.Add(50*time.Minute), text, nil, reports[:2]...)
	a.version("festival2", "festival", a.t0.Add(110*time.Minute), text, []Sentence{{"בפסטיבל יוקרנו 40 סרטים.", []string{reports[2].URL}}}, reports...)
}

// The replay's answer (replayAnswer) with a fourth story, the festival's: a detail with a number from the preview.
var festivalAnswer = strings.TrimSuffix(replayAnswer, "]}") + `,
	{"story": "s4", "unrelated": [], "insert": [{"after": "s4.1", "kind": "detail", "text": "בפסטיבל יוקרנו 40 סרטים.", "sources": ["a4"]}],
		"replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`

func TestTheReplayTakesNoNewsFromAnArticleOlderThanTheStory(t *testing.T) {
	dir, db := replayArchiveOf(t, festival)
	rec := &recorded{answers: []string{festivalAnswer}}
	var cases bytes.Buffer
	w := &Writer{Gemini: rec, Store: db, Now: func() time.Time { return now }, Log: t.Logf}
	report, err := w.Replay(context.Background(), ReplayOptions{Archive: dir, NoControls: true, Cases: &cases})
	if err != nil {
		t.Fatal(err)
	}
	// The request says the preview is older than the story. Its detail with a number makes no version: it is
	// counted among the details that add a number, and not among the ones from an article published since the
	// story. It goes into the story quietly, and that is counted apart, by its kind.
	if want := "[a4] siteg · 2026-11-10T16:00:00Z · פורסמה לפני שהסיפור נכתב\nכותרת: לקראת הפסטיבל\n"; !strings.Contains(rec.requests[0].Prompt, want) {
		t.Fatalf("the request lacks %q:\n%s", want, rec.requests[0].Prompt)
	}
	if report.Real != 4 || report.LiveNewVersion != 1 || report.LiveNewQuiet != 2 || report.MissedOld != 0 || report.QuietOld != 0 || report.Stats.Old != 0 || report.MissedFound != 2 ||
		report.Stats.Adds != 1 || report.Stats.Since != 0 || report.NewsByDetail != 0 || report.ExtraByDetail != 0 || report.ArticlesAfter != 3 || report.ArticlesBefore != 1 || report.ArticlesUndated != 0 || report.GeminiSkipped != 0 {
		t.Fatalf("%+v", report)
	}
	if report.Stats.Quiet != 2 || report.Stats.Unapplied != 0 || report.MissedUpdated != 2 || report.QuietUpdated != 0 || report.QuietKinds[Detail] != 1 || report.QuietKinds[Background] != 1 ||
		len(report.QuietKinds) != 2 || len(report.LeftOutKinds) != 0 || report.Kinds[Detail] != 0 {
		t.Fatalf("%+v", report)
	}
	var log bytes.Buffer
	report.Print(&log)
	for _, want := range []string{
		"the real steps' 4 new articles: 3 (75%) published after the version before, 1 (25%) before it, 0 of no known time",
		"(every article it cites was published before the version before) in 0 steps without one: 0 where live had new facts, 0 where it had none",
		"no development or correction): 0; 0 where live had new facts, 0 where it had none (read those for a thin version)",
		"headline tripwire (of the tripwire's:",
		"inserted sentences that gave another number for a sentence of the story, and were made its correction: 0",
		"developments and corrections from articles published before the version before (they make no version): 0",
		"details with a number, a person or a body, or an outcome the story lacks: 1, 0 of them from an article published after the version before (each makes a version)",
		"with no version made: 2 went into the story quietly; 0 stayed out: 0 from articles the story was written from, shown again, and 0 replacements and corrections",
		"sentences went into the story quietly (no version, nothing in what's new) in 2 steps: 2 where live had new facts, 0 where it had none",
		"added quietly with no version made, by kind: 0 development, 0 correction, 1 detail, 1 background, 0 reaction",
		"0 garbled or cut, 0 with a number none of the articles they cite states (0 of them a count of a country's people in a word)",
		"of those details, for a person or a body they name and no number: 0; for an outcome alone: 0; details that add none because the only numbers the story lacks in them are dates or years: 0",
		"kept out of the story with no version made, by kind: 0 development, 0 correction, 0 detail, 0 background, 0 reaction",
	} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("the report lacks %q:\n%s", want, log.String())
		}
	}
	got := casesOf(t, cases.Bytes())
	preview := got[len(got)-1]
	if preview.Story != "festival" || preview.Kind != CaseMissed || preview.Merge.Version || preview.New[0].When != "before" || preview.New[0].Published != "2026-11-10T16:00:00Z" ||
		strings.Join(notesOf(Merged{Notes: preview.Merge.Notes}), "; ") != "insert s4.1: quiet ("+whyQuiet+")" || preview.Merge.Notes[0].Old || !preview.Merge.Notes[0].Adds ||
		got[0].New[0].When != "after" {
		t.Fatalf("%+v", preview)
	}
	// The case marks the sentence: its outcome is "quiet", and the step's count has it.
	if preview.Merge.Notes[0].Outcome != NoteQuiet || preview.Merge.Stats.Quiet != 1 || !strings.Contains(cases.String(), `"outcome":"quiet","why":"`+whyQuiet+`","adds":true,"news":"number"`) {
		t.Fatalf("%+v", preview.Merge)
	}

	// The same fact answered as a development: the articles' time keeps it from making a version, and that is
	// counted as such a step, where the live rewrite had new facts. It goes in quietly all the same.
	w.Gemini = &recorded{answers: []string{strings.Replace(festivalAnswer, `{"after": "s4.1", "kind": "detail"`, `{"after": "s4.1", "kind": "development"`, 1)}}
	if report, err = w.Replay(context.Background(), ReplayOptions{Archive: dir, NoControls: true}); err != nil || report.MissedOld != 1 || report.QuietOld != 0 || report.Stats.Old != 1 ||
		report.Stats.Adds != 0 || report.MissedUpdated != 2 || report.QuietKinds[Development] != 1 || report.LiveNewVersion != 1 {
		t.Fatalf("%v %+v", err, report)
	}
	log.Reset()
	report.Print(&log)
	if want := "(every article it cites was published before the version before) in 1 steps without one: 1 where live had new facts, 0 where it had none"; !strings.Contains(log.String(), want) {
		t.Fatalf("the report lacks %q:\n%s", want, log.String())
	}
}

func TestTheReplayCountsTheVersionsOnlyDetailsMade(t *testing.T) {
	dir, db := replayArchiveOf(t)
	// The fire's new article, first seen after the story was written, is answered with a detail: how long the fire
	// burned. Its number makes a version, by the detail alone, and the step is counted: the live rewrite gave the
	// same fact as new, so it is no thin version.
	answer := strings.Replace(replayAnswer, `{"after": "top", "kind": "development", "text": "בדרום פרצה שריפה.", "sources": ["a3"]}`,
		`{"after": "s3.1", "kind": "detail", "text": "השריפה כובתה לאחר 5 שעות.", "sources": ["a3"]}`, 1)
	var cases bytes.Buffer
	w := &Writer{Gemini: &recorded{answers: []string{answer}}, Store: db, Now: func() time.Time { return now }, Log: t.Logf}
	report, err := w.Replay(context.Background(), ReplayOptions{Archive: dir, NoControls: true, Cases: &cases})
	if err != nil || report.Real != 3 || report.LiveNewVersion != 2 || report.LiveNewQuiet != 0 || report.NewsByDetail != 1 || report.ExtraByDetail != 0 || report.MissedOld != 0 ||
		report.MissedUpdated != 0 || report.Stats.Adds != 1 || report.Stats.Since != 1 || report.Stats.ByDetail != 1 || report.Stats.Quiet != 0 || report.Kinds[Detail] != 1 {
		t.Fatalf("%v %+v", err, report)
	}
	var log bytes.Buffer
	report.Print(&log)
	for _, want := range []string{
		"no development or correction): 1; 1 where live had new facts, 0 where it had none (read those for a thin version)",
		"details with a number, a person or a body, or an outcome the story lacks: 1, 1 of them from an article published after the version before (each makes a version)",
	} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("the report lacks %q:\n%s", want, log.String())
		}
	}
	fire := casesOf(t, cases.Bytes())[2]
	if fire.Kind != CaseAgreeVersion || !fire.Merge.Version || !fire.Merge.Notes[0].Adds || fire.Merge.Notes[0].News != NewsNumber || !fire.Merge.Notes[0].MakesVersion ||
		fire.Merge.Notes[0].Outcome != NoteApplied || fire.New[0].When != "after" {
		t.Fatalf("%+v", fire)
	}
}

func TestTheReplaysCasesHoldTheControlsAnsweredWrong(t *testing.T) {
	dir, db := replayArchiveOf(t)
	// The controls' answers, with the second request's own article (s2) found to be about another event.
	controlAnswers := []string{controlAnswers[0], strings.Replace(controlAnswers[1], `{"story": "s2", "unrelated": []`, `{"story": "s2", "unrelated": [{"id": "a2", "event": "הצבעה אחרת"}]`, 1)}
	rec := &recorded{answers: controlAnswers}
	var cases bytes.Buffer
	w := &Writer{Gemini: rec, Store: db, Now: func() time.Time { return now }, Log: t.Logf}
	report, err := w.Replay(context.Background(), ReplayOptions{Archive: dir, Batch: 4, Cases: &cases})
	if err != nil {
		t.Fatal(err)
	}
	// The three steps, two of them skipped by Gemini, and of the four controls the two answered wrong: a story
	// shown its own article, found to be about another event; a story shown another's article, not found unrelated.
	got := casesOf(t, cases.Bytes())
	var kinds []string
	for _, c := range got {
		kinds = append(kinds, fmt.Sprintf("%d %s", c.N, c.Kind))
	}
	if strings.Join(kinds, ", ") != "2 agree-version, 4 skipped, 5 skipped, 6 control-repeat, 7 control-stranger" || report.Cases != 5 || report.Selected != 7 {
		t.Fatalf("cases: %s; %+v", strings.Join(kinds, ", "), report)
	}
	// A skipped step has the live rewrite of it, to see what was skipped.
	if skipped := got[2]; !skipped.Merge.Skipped || strings.Join(skipped.Merge.Unmentioned, ",") != "a1" || skipped.Live == nil || len(skipped.Live.New) != 1 || skipped.Merge.Version {
		t.Fatalf("%+v", skipped)
	}
	repeat, stranger := got[3], got[4]
	if repeat.Live != nil || repeat.Request != 2 || repeat.Position != 2 || repeat.Target != "a2" || len(repeat.New) != 1 || repeat.New[0].ID != "a2" || repeat.New[0].When != "before" ||
		repeat.Merge.Version || strings.Join(repeat.Merge.Unrelated, ",") != "a2" || repeat.Merge.Skipped ||
		repeat.Merge.Notes[0].Text != "שר האוצר בירך על אישור התקציב." || repeat.Merge.Notes[0].Outcome != NoteDropped {
		t.Fatalf("%+v", repeat)
	}
	if stranger.Live != nil || stranger.Target != "a3" || stranger.New[0].ID != "a3" || stranger.Merge.Version || len(stranger.Merge.Unrelated) != 0 || !stranger.Merge.Skipped {
		t.Fatalf("%+v", stranger)
	}

	// From an offset the replay goes on where one that stopped left off: the second request, as it was.
	second := rec.requests[1].Prompt
	rec = &recorded{answers: controlAnswers[1:]}
	w.Gemini = rec
	cases.Reset()
	report, err = w.Replay(context.Background(), ReplayOptions{Archive: dir, Batch: 4, Offset: 4, Cases: &cases})
	if err != nil || len(rec.requests) != 1 || rec.requests[0].Prompt != second || report.Skipped != 4 || report.Asked != 3 || report.Next != 0 {
		t.Fatalf("%v, %d requests, %+v", err, len(rec.requests), report)
	}
	kinds = nil
	for _, c := range casesOf(t, cases.Bytes()) {
		kinds = append(kinds, fmt.Sprintf("%d %s", c.N, c.Kind))
	}
	if strings.Join(kinds, ", ") != "5 skipped, 6 control-repeat, 7 control-stranger" {
		t.Fatalf("cases: %s", strings.Join(kinds, ", "))
	}
	// And a replay that stopped says the offset to go on from.
	rec = &recorded{answers: controlAnswers}
	w.Gemini = rec
	report, err = w.Replay(context.Background(), ReplayOptions{Archive: dir, Batch: 4, MaxRequests: 1})
	var log bytes.Buffer
	report.Print(&log)
	if err != nil || report.Next != 4 || !strings.Contains(log.String(), "-offset 4\n") {
		t.Fatalf("%v %+v\n%s", err, report, log.String())
	}
}

// spreadArchiveOf makes an archive of twelve stories over three days, four a day: each has a version in the
// morning and, an hour later, one a third site joined. Their titles are "כותרת n0a" and "כותרת n0b" to "n11b".
func spreadArchiveOf(t *testing.T) (dir string, db *store.Store) {
	t.Helper()
	a := newArchiveMaker(t, now)
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("n%d", i)
		a.t0 = time.Date(2026, 11, 8+i/4, 8, 10*(i%4), 0, 0, time.UTC)
		reports := []scrape.ArticleSummary{a.report("siteh", name, "ידיעה ראשונה "+name, "דבר ראשון נמסר על "+name+".", filler("הידיעה "+name)), a.report("sitee", name, "ידיעה שנייה "+name),
			a.report("siteg", name, "ידיעה שלישית "+name, "דבר נוסף נמסר על "+name+".", filler("ההמשך של "+name))}
		text := []Sentence{{"דבר ראשון נמסר על " + name + ".", []string{reports[0].URL, reports[1].URL}}, {"פרט שני נמסר על " + name + ".", []string{reports[1].URL}}}
		a.version(name+"a", name, a.t0, text, nil, reports[:2]...)
		a.version(name+"b", name, a.t0.Add(time.Hour), text, nil, reports...)
	}
	return a.archive(), a.db
}

var askedStory = regexp.MustCompile(`כותרת הסיפור: כותרת n(\d+)a`)

func TestTheReplaySamplesItsStepsOverTheDays(t *testing.T) {
	dir, db := spreadArchiveOf(t)
	ctx := context.Background()
	// asked are the stories a replay asked about, by their numbers.
	asked := func(opts ReplayOptions) (string, *ReplayReport) {
		t.Helper()
		rec := &recorded{answers: []string{`{"stories": []}`}}
		w := &Writer{Gemini: rec, Store: db, Now: func() time.Time { return now }, Log: func(string, ...any) {}}
		opts.Archive, opts.NoControls = dir, true
		report, err := w.Replay(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		var numbers []string
		for _, request := range rec.requests {
			for _, m := range askedStory.FindAllStringSubmatch(request.Prompt, -1) {
				numbers = append(numbers, m[1])
			}
		}
		return strings.Join(numbers, " "), report
	}
	// Without a sample: every step, the oldest first.
	all, report := asked(ReplayOptions{Batch: 12})
	if all != "0 1 2 3 4 5 6 7 8 9 10 11" || report.Eligible != 12 || report.Sampled != 0 || report.Last.Format(time.DateOnly) != "2026-11-10" {
		t.Fatalf("%s %+v", all, report)
	}
	// A sample of four is one of each quarter of them, so of every day; the same seed gives the same four, on every
	// run, and another seed others.
	first, report := asked(ReplayOptions{Sample: 4, Seed: 1})
	numbers := strings.Fields(first)
	if len(numbers) != 4 || report.Sampled != 4 || report.Eligible != 12 || report.Asked != 4 {
		t.Fatalf("%s %+v", first, report)
	}
	for i, number := range numbers {
		if n, _ := strconv.Atoi(number); n/3 != i {
			t.Fatalf("the sample %s is not one of each quarter", first)
		}
	}
	if again, _ := asked(ReplayOptions{Sample: 4, Seed: 1}); again != first {
		t.Fatalf("the same seed: %s, then %s", first, again)
	}
	if other, _ := asked(ReplayOptions{Sample: 4, Seed: 2}); other == first || len(strings.Fields(other)) != 4 {
		t.Fatalf("another seed: %s, then %s", first, other)
	}
	// A sample of all the steps or more is all of them.
	if got, report := asked(ReplayOptions{Sample: 20, Seed: 1, Batch: 12}); got != all || report.Sampled != 0 {
		t.Fatalf("%s %+v", got, report)
	}
	// An offset skips the sample's first stories; a replay that stopped tells where to go on.
	rest, report := asked(ReplayOptions{Sample: 4, Seed: 1, Offset: 1, Batch: 2, MaxRequests: 1})
	if rest != strings.Join(numbers[1:3], " ") || report.Skipped != 1 || report.Selected != 4 || report.Next != 3 {
		t.Fatalf("%s of %s, %+v", rest, first, report)
	}
	if rest, report := asked(ReplayOptions{Sample: 4, Seed: 1, Offset: 3}); rest != numbers[3] || report.Next != 0 {
		t.Fatalf("%s of %s, %+v", rest, first, report)
	}
	if rest, report := asked(ReplayOptions{Sample: 4, Seed: 1, Offset: 9}); rest != "" || report.Skipped != 4 || report.Requests != 0 {
		t.Fatalf("%s %+v", rest, report)
	}
	// Up to a day: its steps and the days' before it, whatever the archive got since. Days counts back from it.
	until := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	if got, report := asked(ReplayOptions{Until: until, Batch: 12}); got != "0 1 2 3 4 5 6 7" || report.Steps != 8 || report.Last.Format(time.DateOnly) != "2026-11-09" {
		t.Fatalf("%s %+v", got, report)
	}
	if got, report := asked(ReplayOptions{Until: until, Days: 1}); got != "4 5 6 7" || report.Steps != 4 {
		t.Fatalf("%s %+v", got, report)
	}
	sampled, report := asked(ReplayOptions{Until: until, Days: 2, Sample: 2, Seed: 1})
	if numbers := strings.Fields(sampled); len(numbers) != 2 || numbers[0] > "3" || numbers[1] < "4" || report.Eligible != 8 {
		t.Fatalf("%s %+v", sampled, report)
	}
	// A day after the archive's last is its last.
	if got, report := asked(ReplayOptions{Until: until.Add(72 * time.Hour), Days: 1}); got != "8 9 10 11" || report.Last.Format(time.DateOnly) != "2026-11-10" {
		t.Fatalf("%s %+v", got, report)
	}
	var log bytes.Buffer
	_, report = asked(ReplayOptions{Sample: 4, Seed: 1, Offset: 1})
	report.Print(&log)
	for _, want := range []string{"asked about: 4 of them, one of each of 4 equal runs of them in time (-sample, -seed)", "the first 1 of the 4 stories to ask about, controls among them, were skipped (-offset)"} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("the report lacks %q:\n%s", want, log.String())
		}
	}
}
