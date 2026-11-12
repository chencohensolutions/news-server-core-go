package newsserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/backup"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

func TestTheIncrementalCommandsSetTheShadowAndReplayDry(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	config := auth.AppConfig{DatabaseFile: filepath.Join(tmp, "news.db"), ArchiveDir: filepath.Join(tmp, "archive"), SourcesDir: filepath.Join(tmp, "sources")}
	var out bytes.Buffer
	// Off until it is turned on; "on" is refused.
	if err := incrementalCommand(config, nil, &out); err != nil || !strings.Contains(out.String(), "incremental: off") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if err := incrementalCommand(config, []string{"on"}, &out); err == nil {
		t.Fatal("on is not built")
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
		if err := db.SaveHeadlines(ctx, []scrape.ArticleSummary{first, second}, at); err != nil {
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
