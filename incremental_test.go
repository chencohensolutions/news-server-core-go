package newsserver

import (
	"bytes"
	"context"
	"encoding/json"
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
