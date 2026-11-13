package newsserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/backup"
	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/stories"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

func TestTheIncrementalCommandsSetTheShadowAndReplayDry(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	config := auth.AppConfig{DatabaseFile: filepath.Join(tmp, "news.db"), ArchiveDir: filepath.Join(tmp, "archive"), SourcesDir: filepath.Join(tmp, "sources")}
	var out bytes.Buffer
	// Off until it is turned on; a word that is no mode is refused.
	if err := incrementalCommand(config, nil, &out); err != nil || !strings.Contains(out.String(), "incremental: off") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if err := incrementalCommand(config, []string{"later"}, &out); err == nil {
		t.Fatal("later is no mode")
	}
	// On: the stories' next versions are updates.
	out.Reset()
	if err := incrementalCommand(config, []string{"on"}, &out); err != nil || !strings.Contains(out.String(), "incremental: on (a story that has a version is updated from its new articles, 8 stories a request") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	out.Reset()
	if err := incrementalCommand(config, []string{"shadow"}, &out); err != nil || !strings.Contains(out.String(), "incremental: shadow (at most 48 requests a day") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	db, err := store.Open(config.DatabaseFile)
	if err != nil {
		t.Fatal(err)
	}
	// The setting is the summaries' in the database, beside the ones the app changes, which stay.
	settings, err := summary.LoadSettings(ctx, db)
	if err != nil || settings.Incremental != summary.IncrementalShadow || settings.Length != summary.DefaultSettings.Length || settings.RewriteMinutes != summary.DefaultSettings.RewriteMinutes {
		t.Fatalf("%+v %v", settings, err)
	}
	// An archive of one story with two versions: the replay finds the rewrite and builds its request, dry.
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	first := scrape.ArticleSummary{SourceID: "siteh", URL: "https://siteh/rain", Title: "גשם בצפון"}
	second := scrape.ArticleSummary{SourceID: "sitee", URL: "https://sitee/rain", Title: "סערה בצפון"}
	if err := db.SaveHeadlines(ctx, []scrape.ArticleSummary{first, second}, at); err != nil {
		t.Fatal(err)
	}
	sentences := []summary.Sentence{{Text: "גשם כבד ירד הבוקר בצפון.", Sources: []string{first.URL}}}
	for i, reports := range [][]string{{first.URL}, {first.URL, second.URL}} {
		data, _ := json.Marshal(summary.StorySummary{Title: "גשם כבד בצפון", Sentences: sentences, WrittenAt: at.Add(time.Duration(i) * time.Hour), ID: "rain", Reports: reports})
		if err := db.SaveStoryVersion(ctx, "rain", data, at.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := backup.Archive(ctx, db, config.ArchiveDir, at.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	out.Reset()
	if err := evalIncremental(config, []string{"-dry", "-days", "1"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{"request 1: 1 stories", "2 story versions in the archive, 1 of them rewrites", "1 requests built, none sent (-dry)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the replay lacks %q:\n%s", want, out.String())
		}
	}
	// Without -dry it needs the Gemini key; and it takes no other arguments.
	config.GeminiKeyFile = filepath.Join(tmp, "gemini.key")
	if err := evalIncremental(config, nil, &out); err == nil || !strings.Contains(err.Error(), "no Gemini key") {
		t.Fatalf("%v", err)
	}
	if err := evalIncremental(config, []string{"now"}, &out); err == nil {
		t.Fatal("an argument that is no flag")
	}
	out.Reset()
	if err := incrementalCommand(config, []string{"off"}, &out); err != nil || !strings.Contains(out.String(), "incremental: off") || !strings.Contains(out.String(), "the shadow recorded nothing") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

// A recorded answer for a request of one story: a development from its new article.
const replayAnswer = `{"stories": [{"story": "s1", "unrelated": [], "insert": [{"after": "top", "kind": "development", "text": "הנחל עלה על גדותיו.", "sources": ["a1"]}],
	"replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`

func TestTheReplayWritesItsCasesToAFile(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	config := auth.AppConfig{DatabaseFile: filepath.Join(tmp, "news.db"), ArchiveDir: filepath.Join(tmp, "archive"), SourcesDir: filepath.Join(tmp, "sources"),
		GeminiKeyFile: filepath.Join(tmp, "gemini.key")}
	if err := os.WriteFile(config.GeminiKeyFile, []byte("test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Gemini is a server with the recorded answer.
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		json.NewEncoder(w).Encode(map[string]any{
			"candidates":    []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": replayAnswer}}}, "finishReason": "STOP"}},
			"usageMetadata": map[string]any{"promptTokenCount": 500, "candidatesTokenCount": 80},
		})
	}))
	defer server.Close()
	geminiBaseURL = server.URL
	defer func() { geminiBaseURL = "" }()
	// An archive of two stories, one a day: each has a version, and an hour later one a second site joined.
	db, err := store.Open(config.DatabaseFile)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"rain", "fire"} {
		at := time.Date(2026, 11, 9+i, 9, 0, 0, 0, time.UTC)
		first := scrape.ArticleSummary{SourceID: "siteh", URL: "https://siteh/" + name, Title: "ידיעה ראשונה " + name}
		second := scrape.ArticleSummary{SourceID: "sitee", URL: "https://sitee/" + name, Title: "ידיעה שנייה " + name}
		// The second site's article is first seen after the first version was written: it can be news to it.
		if err := db.SaveHeadlines(ctx, []scrape.ArticleSummary{first}, at); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveHeadlines(ctx, []scrape.ArticleSummary{second}, at.Add(30*time.Minute)); err != nil {
			t.Fatal(err)
		}
		body := []string{"הנחל עלה על גדותיו.", "הכתבה המלאה כוללת פרטים רבים שנמסרו לכתבים במהלך היום, ובהם תגובות של גורמים רשמיים, נתונים שפורסמו לציבור, ותיאור של מהלך האירועים כפי שנמסר מפי עדים שהיו במקום בזמן האירוע ולאחריו, לצד הערכות של גורמי המקצוע להמשך."}
		if err := db.SaveArticle(ctx, &scrape.Article{SourceID: "sitee", URL: second.URL, Title: second.Title, Body: body}, "<html>page</html>", at); err != nil {
			t.Fatal(err)
		}
		sentences := []summary.Sentence{{Text: "דבר ראשון נמסר על " + name + ".", Sources: []string{first.URL}}}
		for v, reports := range [][]string{{first.URL}, {first.URL, second.URL}} {
			written := at.Add(time.Duration(v) * time.Hour)
			data, _ := json.Marshal(summary.StorySummary{Title: "כותרת " + name, Sentences: sentences, WrittenAt: written, ID: name, Reports: reports})
			if err := db.SaveStoryVersion(ctx, name, data, written); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := backup.Archive(ctx, db, config.ArchiveDir, time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	path := filepath.Join(tmp, "cases.jsonl")
	// cases are the file's lines.
	cases := func() []summary.ReplayCase {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var out []summary.ReplayCase
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var c summary.ReplayCase
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				t.Fatalf("%v in %s", err, line)
			}
			out = append(out, c)
		}
		return out
	}
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := evalIncremental(config, append([]string{"-no-controls", "-batch", "1", "-cases", path}, args...), &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		return out.String()
	}
	// A sample of one of the two days' steps, up to a day: one request, and its case in the file.
	out := run("-days", "2", "-until", "2026-11-10", "-sample", "1", "-seed", "1")
	for _, want := range []string{"the archive's days up to 2026-11-10 (UTC)", "asked about: 1 of them, one of each of 1 equal runs", "1 requests sent: 1 stories", "1 cases written to " + path} {
		if !strings.Contains(out, want) {
			t.Fatalf("the replay lacks %q:\n%s", want, out)
		}
	}
	got := cases()
	// The live rewrite gave nothing as new, and the update made a version: a case to read.
	if requests != 1 || len(got) != 1 || got[0].Kind != summary.CaseExtra || !got[0].Merge.Version || len(got[0].New) != 1 || got[0].New[0].ID != "a1" || got[0].New[0].Site != "sitee" ||
		!strings.HasPrefix(got[0].New[0].Body, "הנחל עלה על גדותיו.") || len(got[0].Prev) != 1 || got[0].Prev[0].N != "s1.1" || got[0].Live == nil || len(got[0].Live.New) != 0 || len(got[0].Live.Sentences) != 1 {
		t.Fatalf("%d requests, %+v", requests, got)
	}
	// Another seed can pick the other step; the same seed picks the same one.
	picked := map[string]bool{}
	for _, seed := range []string{"1", "1", "2", "3", "4", "5", "6", "7", "8"} {
		run("-sample", "1", "-seed", seed)
		if again := cases(); seed == "1" && again[0].Story != got[0].Story {
			t.Fatalf("seed 1 picked %s, then %s", got[0].Story, again[0].Story)
		} else {
			picked[again[0].Story] = true
		}
	}
	if len(picked) != 2 {
		t.Fatalf("eight seeds picked only %v", picked)
	}
	// A replay that stopped tells the offset to go on from; from it, the file is added to.
	out = run("-max-requests", "1")
	if got = cases(); !strings.Contains(out, "-offset 1\n") || len(got) != 1 || got[0].N != 1 || got[0].Story != "rain" || got[0].At.Format(time.DateOnly) != "2026-11-09" {
		t.Fatalf("%+v\n%s", got, out)
	}
	out = run("-offset", "1")
	if got = cases(); !strings.Contains(out, "the first 1 of the 2 stories to ask about") || len(got) != 2 || got[1].N != 2 || got[1].Story != "fire" {
		t.Fatalf("%+v\n%s", got, out)
	}
	// Up to the first day: its step alone.
	out = run("-until", "2026-11-09")
	if got = cases(); !strings.Contains(out, "the archive's days up to 2026-11-09 (UTC)") || len(got) != 1 || got[0].Story != "rain" {
		t.Fatalf("%+v\n%s", got, out)
	}
	// The cases hold what the sites wrote: not into the archive, nor into a repository.
	requests = 0
	if err := os.MkdirAll(filepath.Join(tmp, "work", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(config.ArchiveDir, "cases.jsonl"):         "inside the archive",
		filepath.Join(config.ArchiveDir, "cases", "one.jsonl"):  "inside the archive",
		filepath.Join(tmp, "work", "cases.jsonl"):               "inside a repository",
		filepath.Join(tmp, "work", "eval", "out", "cases.json"): "inside a repository",
	} {
		var out bytes.Buffer
		if err := evalIncremental(config, []string{"-cases", path}, &out); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("-cases %s: %v", path, err)
		}
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("%s was written", path)
		}
	}
	// Nor with -dry, which has no answers; and the other flags are checked.
	for _, args := range [][]string{{"-dry", "-cases", path}, {"-until", "yesterday"}, {"-sample", "-1"}, {"-offset", "-1"}} {
		var out bytes.Buffer
		if err := evalIncremental(config, args, &out); err == nil {
			t.Fatalf("%v was taken", args)
		}
	}
	if requests != 0 {
		t.Fatalf("%d requests were sent", requests)
	}
}

// recordedGemini answers the summary job with recorded answers: a whole writing's request with whole, and the
// incremental requests with updates, one each in order. It keeps the incremental requests.
type recordedGemini struct {
	whole   string
	updates []string
	wholes  int
	asked   []gemini.Request
}

func (g *recordedGemini) Generate(_ context.Context, req gemini.Request) (json.RawMessage, gemini.Usage, error) {
	usage := gemini.Usage{PromptTokens: 500, AnswerTokens: 80}
	// Only the incremental request has the story so far as numbered sentences, and its new articles under them.
	if !strings.Contains(req.Prompt, "כתבות חדשות:") {
		g.wholes++
		return json.RawMessage(g.whole), usage, nil
	}
	g.asked = append(g.asked, req)
	if len(g.asked) > len(g.updates) {
		return nil, gemini.Usage{}, fmt.Errorf("no recorded answer for incremental request %d", len(g.asked))
	}
	return json.RawMessage(g.updates[len(g.asked)-1]), usage, nil
}

// Recorded answers: a story's first version, and three updates of it.
const (
	wholeAnswer = `{"stories": [{"story": "s1", "title": "גשם כבד בצפון", "new": [],
		"sentences": [{"text": "גשם כבד ירד הבוקר בצפון.", "sources": ["a1", "a2"]}, {"text": "שני כבישים נסגרו.", "sources": ["a2", "a3"]}],
		"contradictions": [], "unrelated": []}]}`
	// A development from an article published since, a footnote, and a title: a version.
	versionAnswer = `{"stories": [{"story": "s1", "unrelated": [],
		"insert": [{"after": "top", "kind": "development", "text": "נחל עלה על גדותיו והציף שכונה.", "sources": ["a1"]}],
		"replace": [], "delete": [], "cite": [{"sentence": "s1.1", "sources": ["a1"]}], "title": "הצפות בצפון", "contradictions": []}]}`
	// A detail from an article published before the version: into the text, quietly.
	quietAnswer = `{"stories": [{"story": "s1", "unrelated": [],
		"insert": [{"after": "s1.2", "kind": "detail", "text": "בעיר הסמוכה ירדו 80 מילימטרים של גשם.", "sources": ["a1"]}],
		"replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`
	// Nothing new: the article states a sentence's fact.
	nothingAnswer = `{"stories": [{"story": "s1", "unrelated": [], "insert": [], "replace": [], "delete": [],
		"cite": [{"sentence": "s1.4", "sources": ["a1"]}], "title": "", "contradictions": []}]}`
)

// appSummary is a story's generated article as the app asks for it (its ServerStorySummary fragment), with the
// updates.
const appSummary = `title sentences { text footnotes { url sourceId title video publishedAt } }
	contradictions { text footnotes { url sourceId title video publishedAt } } unrelated reports headlineOnly writtenAt
	writtenWith { fullArticles length } id version versions current
	changes { since title sentences { kind parts { text added } } removed { text footnotes { url sourceId title video publishedAt } } }
	updates { at sentences { text footnotes { url sourceId title video publishedAt } } }`

type appSentence struct {
	Text      string `json:"text"`
	Footnotes []struct {
		URL      string  `json:"url"`
		SourceID *string `json:"sourceId"`
		Title    *string `json:"title"`
	} `json:"footnotes"`
}

type appStory struct {
	Title          string        `json:"title"`
	Sentences      []appSentence `json:"sentences"`
	Contradictions []appSentence `json:"contradictions"`
	Unrelated      []string      `json:"unrelated"`
	Reports        []string      `json:"reports"`
	WrittenAt      time.Time     `json:"writtenAt"`
	ID             string        `json:"id"`
	Version        int           `json:"version"`
	Versions       int           `json:"versions"`
	Current        bool          `json:"current"`
	Changes        *struct {
		Since     time.Time `json:"since"`
		Title     string    `json:"title"`
		Sentences []struct {
			Kind  string `json:"kind"`
			Parts []struct {
				Text  string `json:"text"`
				Added bool   `json:"added"`
			} `json:"parts"`
		} `json:"sentences"`
		Removed []appSentence `json:"removed"`
	} `json:"changes"`
	Updates []struct {
		At        time.Time     `json:"at"`
		Sentences []appSentence `json:"sentences"`
	} `json:"updates"`
}

func (s appStory) text() string {
	var out []string
	for _, sentence := range s.Sentences {
		out = append(out, sentence.Text)
	}
	return strings.Join(out, " | ")
}

func (s appStory) kinds() string {
	if s.Changes == nil {
		return ""
	}
	var out []string
	for _, c := range s.Changes.Sentences {
		out = append(out, c.Kind)
	}
	return strings.Join(out, " ")
}

// The writer with the incremental update on, on recorded answers, read as the app reads it: a version the update
// makes, a quiet update and an answer with nothing new, then off again.
func TestTheAppReadsAStoryTheIncrementalUpdateKeeps(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.api.Summaries = true
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	report := func(site, title string) scrape.ArticleSummary {
		return scrape.ArticleSummary{SourceID: site, URL: "https://" + site + "/rain", Title: title}
	}
	story := stories.Story{Reports: []scrape.ArticleSummary{report("siteh", "גשם בצפון"), report("sitee", "סערה בצפון"), report("sited", "כבישים נסגרו בצפון"), report("siteg", "גשם כבד")}}
	if err := env.db.SaveHeadlines(ctx, story.Reports, at); err != nil {
		t.Fatal(err)
	}
	if err := summary.SaveSettings(ctx, env.db, summary.Settings{FullArticles: true, Length: "medium", Incremental: summary.IncrementalOn}); err != nil {
		t.Fatal(err)
	}
	recorded := &recordedGemini{whole: wholeAnswer, updates: []string{versionAnswer, quietAnswer, nothingAnswer}}
	writer := &summary.Writer{Gemini: recorded, Store: env.db, Now: func() time.Time { return at }, Log: t.Logf, ThreadsEvery: -1}
	env.api.Stories = func() ([]stories.Story, time.Time) { return []stories.Story{story}, at }
	// run adds reports to the story a quarter of an hour on, runs the writer and reads the story as the app does: in
	// the stories, and its versions, newest first.
	run := func(want summary.Result, added ...scrape.ArticleSummary) (appStory, []appStory) {
		t.Helper()
		if len(added) > 0 {
			at = at.Add(15 * time.Minute)
			if err := env.db.SaveHeadlines(ctx, added, at); err != nil {
				t.Fatal(err)
			}
			story.Reports = append(append([]scrape.ArticleSummary{}, story.Reports...), added...)
		}
		if result, err := writer.Run(ctx, []stories.Story{story}); err != nil || result != want {
			t.Fatalf("%+v %v", result, err)
		}
		resp := mustQuery(t, env, `{ stories { stories { summary { `+appSummary+` } } } }`, nil)
		var list struct {
			Stories []struct {
				Summary *appStory `json:"summary"`
			} `json:"stories"`
		}
		if err := json.Unmarshal(resp.Data["stories"], &list); err != nil || len(list.Stories) != 1 || list.Stories[0].Summary == nil {
			t.Fatalf("%v: %s", err, resp.Data["stories"])
		}
		shown := *list.Stories[0].Summary
		resp = mustQuery(t, env, `query($id: ID!) { storyVersions(id: $id) { `+appSummary+` } }`, map[string]any{"id": shown.ID})
		var versions []appStory
		if err := json.Unmarshal(resp.Data["storyVersions"], &versions); err != nil {
			t.Fatalf("%v: %s", err, resp.Data["storyVersions"])
		}
		// What the app counts on, of the story and of each version: every sentence has footnotes, each an article
		// the server has the headline of; a change for each sentence; no empty update; the newest version first,
		// and the story is it.
		reports := map[string]bool{}
		for _, r := range story.Reports {
			reports[r.URL] = true
		}
		for _, s := range append([]appStory{shown}, versions...) {
			var all []appSentence
			all = append(append(all, s.Sentences...), s.Contradictions...)
			if len(s.Sentences) == 0 || len(s.Updates) == 0 || s.Title == "" || s.Changes != nil && len(s.Changes.Sentences) != len(s.Sentences) {
				t.Fatalf("%+v", s)
			}
			for _, u := range s.Updates {
				if len(u.Sentences) == 0 || u.At.After(s.WrittenAt) {
					t.Fatalf("an update of %+v", s)
				}
				all = append(all, u.Sentences...)
			}
			for _, sentence := range all {
				if len(sentence.Footnotes) == 0 || len(sentence.Footnotes) > 5 {
					t.Fatalf("%q has %d footnotes", sentence.Text, len(sentence.Footnotes))
				}
				for _, f := range sentence.Footnotes {
					if f.SourceID == nil || f.Title == nil || !reports[f.URL] {
						t.Fatalf("%q cites %+v", sentence.Text, f)
					}
				}
			}
		}
		if len(versions) != shown.Versions || shown.Version != shown.Versions || !shown.Current || versions[0].text() != shown.text() || !versions[0].WrittenAt.Equal(shown.WrittenAt) ||
			len(shown.Reports) != len(story.Reports) {
			t.Fatalf("the story %+v\nits versions %+v", shown, versions)
		}
		for i, v := range versions {
			if v.Version != len(versions)-i || (v.Changes == nil) != (v.Version == 1) {
				t.Fatalf("version %d of %d: %+v", i, len(versions), v)
			}
		}
		return shown, versions
	}

	// The first version is written whole.
	first, _ := run(summary.Result{Written: 1})
	written := at
	if first.Version != 1 || first.Title != "גשם כבד בצפון" || first.text() != "גשם כבד ירד הבוקר בצפון. | שני כבישים נסגרו." || !first.WrittenAt.Equal(written) || len(first.Updates) != 1 || recorded.wholes != 1 {
		t.Fatalf("%+v", first)
	}

	// An article with news: a version, by one incremental request. The app shows it as any version: its number, the
	// new title with the one before, the new sentence marked, and it is the latest update.
	second, versions := run(summary.Result{Written: 1}, report("sitef", "נחל עלה על גדותיו"))
	updated := at
	if second.Version != 2 || second.ID != first.ID || second.Title != "הצפות בצפון" || !second.WrittenAt.Equal(updated) ||
		second.text() != "נחל עלה על גדותיו והציף שכונה. | גשם כבד ירד הבוקר בצפון. | שני כבישים נסגרו." || second.kinds() != "NEW SAME SAME" ||
		second.Changes.Title != first.Title || !second.Changes.Since.Equal(written) || len(second.Changes.Removed) != 0 || len(second.Sentences[1].Footnotes) != 3 {
		t.Fatalf("%+v", second)
	}
	if len(second.Updates) != 2 || !second.Updates[1].At.Equal(updated) || len(second.Updates[1].Sentences) != 1 || second.Updates[1].Sentences[0].Text != second.Sentences[0].Text ||
		versions[1].text() != first.text() || recorded.wholes != 1 || len(recorded.asked) != 1 {
		t.Fatalf("updates %+v, versions %+v", second.Updates, versions)
	}

	// An article published before that version, with a detail: the sentence goes into the newest version's text, and
	// no version is added: its number, its time, its title and its updates stay. The app marks the sentence as new
	// since the version before, which it is.
	earlier := report("sitei", "80 מילימטרים של גשם ירדו בעיר הסמוכה")
	earlier.PublishedAt = scrape.FormatInstant(written.Add(-time.Hour))
	quiet, versions := run(summary.Result{}, earlier)
	if quiet.Version != 2 || quiet.Title != second.Title || !quiet.WrittenAt.Equal(updated) || quiet.kinds() != "NEW SAME NEW SAME" ||
		quiet.text() != "נחל עלה על גדותיו והציף שכונה. | גשם כבד ירד הבוקר בצפון. | בעיר הסמוכה ירדו 80 מילימטרים של גשם. | שני כבישים נסגרו." ||
		len(quiet.Updates) != 2 || len(quiet.Updates[1].Sentences) != 1 || versions[1].text() != first.text() || len(recorded.asked) != 2 {
		t.Fatalf("%+v", quiet)
	}

	// An article with nothing new: one more report and a footnote, and nothing else.
	same, _ := run(summary.Result{}, report("sitej", "כבישים נסגרו בגלל הגשם"))
	if same.Version != 2 || !same.WrittenAt.Equal(updated) || same.text() != quiet.text() || same.kinds() != quiet.kinds() || len(same.Updates) != 2 ||
		len(same.Sentences[3].Footnotes) != len(quiet.Sentences[3].Footnotes)+1 || recorded.wholes != 1 || len(recorded.asked) != 3 {
		t.Fatalf("%+v", same)
	}
	// Nothing changed: nothing is asked, and the app reads the same.
	if again, _ := run(summary.Result{}); again.text() != same.text() || len(recorded.asked) != 3 {
		t.Fatalf("%+v", again)
	}

	// Off again: the next article makes a whole rewrite, the third version, with nothing to migrate.
	if err := summary.SaveSettings(ctx, env.db, summary.Settings{FullArticles: true, Length: "medium"}); err != nil {
		t.Fatal(err)
	}
	third, versions := run(summary.Result{Written: 1}, report("sitek", "גשם"))
	if third.Version != 3 || third.ID != first.ID || third.text() != first.text() || !third.Changes.Since.Equal(updated) || third.Changes.Title != second.Title ||
		versions[1].text() != same.text() || recorded.wholes != 2 || len(recorded.asked) != 3 {
		t.Fatalf("%+v", third)
	}
}
