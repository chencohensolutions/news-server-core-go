package newsserver

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/sites"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

// The incremental update's commands (docs/incremental.md): `incremental` turns its shadow on and off and reports
// what the shadow recorded, `eval-incremental` replays the archive through it.

// incrementalCommand is the incremental command: with "off" or "shadow" it sets the mode in the database (the
// running server follows it from its next run), and either way it prints the mode and the shadow's last week.
func incrementalCommand(config auth.AppConfig, args []string, out io.Writer) error {
	if len(args) > 1 || len(args) == 1 && args[0] != summary.IncrementalOff && args[0] != summary.IncrementalShadow {
		return fmt.Errorf("usage: news-server incremental [off|shadow] (on is not built yet)")
	}
	db, err := store.Open(config.DatabaseFile)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	settings, err := summary.LoadSettings(ctx, db)
	if err != nil {
		return err
	}
	if len(args) == 1 {
		settings.Incremental = args[0]
		if err := summary.SaveSettings(ctx, db, settings); err != nil {
			return err
		}
	}
	mode := settings.Incremental
	if mode == "" {
		mode = summary.IncrementalOff
	}
	fmt.Fprintf(out, "incremental: %s", mode)
	if mode == summary.IncrementalShadow {
		daily := settings.ShadowDaily
		if daily == 0 {
			daily = summary.DefaultShadowDaily
		}
		fmt.Fprintf(out, " (at most %d requests a day, from what the day's budget has to spare)", daily)
	}
	fmt.Fprintln(out)
	report, err := summary.ReportShadow(ctx, db, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		return err
	}
	printShadowReport(out, report)
	return nil
}

func percent(n, of int) string {
	if of == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", (100*n+of/2)/of)
}

func printShadowReport(out io.Writer, r summary.ShadowReport) {
	if r.Steps == 0 {
		fmt.Fprintln(out, "the shadow recorded nothing in the last 7 days")
		return
	}
	fmt.Fprintf(out, "the shadow's last 7 days: %d changed stories\n", r.Steps)
	fmt.Fprintf(out, "  %d (%s) had no new article: kept by the server, no request\n", r.Local, percent(r.Local, r.Steps))
	rebuilt := 0
	reasons := make([]string, 0, len(r.Rebuild))
	for reason, n := range r.Rebuild {
		rebuilt += n
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	fmt.Fprintf(out, "  %d (%s) would be written whole again:\n", rebuilt, percent(rebuilt, r.Steps))
	for _, reason := range reasons {
		fmt.Fprintf(out, "    %d %s\n", r.Rebuild[reason], reason)
	}
	fmt.Fprintf(out, "  %d (%s) were asked, in about %.0f requests (%.0f tokens in, %.0f out, %.0f thinking):\n", r.Asked, percent(r.Asked, r.Steps),
		r.Requests, r.TokensIn, r.TokensOut, r.TokensThoughts)
	fmt.Fprintf(out, "    %d a new version (%s), %d nothing new, %d the tripwire; %d started from the shadow's own story; %d read from a cut answer\n",
		r.Versions, percent(r.Versions, r.Asked), r.Quiet, r.Tripwire, r.FromShadow, r.Salvaged)
	s := r.Stats
	fmt.Fprintf(out, "    sentences answered: %d; dropped: %d said already (%s), %d without a new article of the story, %d twice, %d on unknown sentences, %d garbled\n",
		s.Asked, s.Restated, percent(s.Restated, s.Asked), s.Unsourced, s.Repeated, s.Unknown, s.Garbled)
	fmt.Fprintf(out, "    left out with no version made: %d; in versions: %d inserted, %d replaced, %d deleted; %d old sentences got a new source\n",
		s.Unapplied, s.Inserted, s.Replaced, s.Deleted, s.Cited)
	live := r.LiveNewVersion + r.LiveNewQuiet
	fmt.Fprintf(out, "  against the live rewrite of the same reports (%d steps):\n", r.Compared)
	fmt.Fprintf(out, "    live had new facts:  %d a version, %d none: recall %s\n", r.LiveNewVersion, r.LiveNewQuiet, percent(r.LiveNewVersion, live))
	fmt.Fprintf(out, "    live had none:       %d a version, %d none\n", r.LiveQuietVersion, r.LiveQuietQuiet)
}

// evalIncremental is the eval-incremental command: the archive's rewrites through the incremental request, on the
// real Gemini key, each request counted in the database's day (summary.Writer.Replay).
func evalIncremental(config auth.AppConfig, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("eval-incremental", flag.ContinueOnError)
	flags.SetOutput(out)
	days := flags.Int("days", 0, "replay the archive's last `N` days (0: all of it)")
	batch := flags.Int("batch", summary.IncrementalPerRequest, "stories in one request")
	maxRequests := flags.Int("max-requests", 10, "the most requests to send; each one is counted in the day's budget (0: as many as it takes)")
	dry := flags.Bool("dry", false, "build the requests and print their sizes; nothing is sent")
	noControls := flags.Bool("no-controls", false, "leave the controls out")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 || *batch < 1 {
		return fmt.Errorf("usage: news-server eval-incremental [-days N] [-batch N] [-max-requests N] [-dry] [-no-controls]")
	}
	if config.ArchiveDir == "-" {
		return fmt.Errorf("the archive is off (archive_dir is \"-\")")
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if *dry && !set["max-requests"] {
		*maxRequests = 0 // building costs nothing
	}
	db, err := store.Open(config.DatabaseFile)
	if err != nil {
		return err
	}
	defer db.Close()
	names := map[string]string{}
	if found, err := sites.Load(config.SourcesDir); err == nil {
		for _, s := range found {
			names[s.ID] = s.Name
		}
	}
	w := &summary.Writer{Store: db, Budget: config.SummaryBudget, SiteName: func(id string) string { return names[id] },
		Log: func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) }}
	if !*dry {
		key, err := gemini.ReadKey(config.GeminiKeyFile)
		if err != nil {
			return fmt.Errorf("no Gemini key (%w): -dry builds the requests without one", err)
		}
		w.Gemini = &gemini.Client{Key: key, Model: config.GeminiModel}
	}
	start := time.Now()
	report, err := w.Replay(context.Background(), summary.ReplayOptions{Archive: config.ArchiveDir, Days: *days, Batch: *batch,
		MaxRequests: *maxRequests, Dry: *dry, NoControls: *noControls})
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	report.Print(out)
	fmt.Fprintf(out, "in %s\n", time.Since(start).Round(time.Second))
	return nil
}
