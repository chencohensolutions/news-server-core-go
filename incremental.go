package newsserver

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// geminiBaseURL is where eval-incremental finds Gemini: "" is the Gemini API. A test's recorded answers are served
// from another address.
var geminiBaseURL = ""

// casesFile opens the file the replay writes its cases to: anew, or to add to when the replay goes on from an
// offset. The cases hold the sites' headlines and articles as they are, so a file inside the archive (a restore reads
// that folder, and it is copied whole) or inside a repository (a folder with .git, or any folder under one) is
// refused.
func casesFile(path, archive string, add bool) (*os.File, error) {
	full, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	inArchive, _ := os.Stat(archive)
	for dir := filepath.Dir(full); ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(dir); err == nil && inArchive != nil && os.SameFile(info, inArchive) {
			return nil, fmt.Errorf("-cases %s is inside the archive (%s): the cases are written outside it", path, archive)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return nil, fmt.Errorf("-cases %s is inside a repository (%s): the cases hold the sites' headlines and articles, which stay out of it", path, dir)
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	mode := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if add {
		mode = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	return os.OpenFile(full, mode, 0o600)
}

// evalIncremental is the eval-incremental command: the archive's rewrites through the incremental request, on the
// real Gemini key, each request counted in the database's day (summary.Writer.Replay).
func evalIncremental(config auth.AppConfig, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("eval-incremental", flag.ContinueOnError)
	flags.SetOutput(out)
	days := flags.Int("days", 0, "replay the archive's last `N` days (0: all of it)")
	until := flags.String("until", "", "the last `day` to replay (UTC, 2026-11-10), which -days counts back from: a day that is over gives the same steps on every run (default: the archive's last)")
	batch := flags.Int("batch", summary.IncrementalPerRequest, "stories in one request")
	maxRequests := flags.Int("max-requests", 10, "the most requests to send; each one is counted in the day's budget (0: as many as it takes)")
	sample := flags.Int("sample", 0, "ask about `N` of the steps, spread over all of them in time (0: all, the oldest first)")
	seed := flags.Int64("seed", 1, "which steps -sample picks: the same `seed` picks the same ones")
	offset := flags.Int("offset", 0, "skip the first `N` stories to ask about, controls among them: a replay that stopped prints the offset that goes on from there")
	cases := flags.String("cases", "", "write every real step answered and every control answered wrong to `file`, a line of JSON each (outside the archive and any repository; with -offset it is added to)")
	dry := flags.Bool("dry", false, "build the requests and print their sizes; nothing is sent")
	noControls := flags.Bool("no-controls", false, "leave the controls out")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 || *batch < 1 || *sample < 0 || *offset < 0 {
		return fmt.Errorf("usage: news-server eval-incremental [-days N] [-until day] [-batch N] [-max-requests N] [-sample N] [-seed N] [-offset N] [-cases file] [-dry] [-no-controls]")
	}
	var last time.Time
	if *until != "" {
		day, err := time.Parse(time.DateOnly, *until)
		if err != nil {
			return fmt.Errorf("-until %s is not a day (2026-11-10)", *until)
		}
		last = day
	}
	if *cases != "" && *dry {
		return fmt.Errorf("-cases needs the answers: not with -dry")
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
		w.Gemini = &gemini.Client{Key: key, Model: config.GeminiModel, BaseURL: geminiBaseURL}
	}
	opts := summary.ReplayOptions{Archive: config.ArchiveDir, Days: *days, Until: last, Batch: *batch, MaxRequests: *maxRequests,
		Sample: *sample, Seed: *seed, Offset: *offset, Dry: *dry, NoControls: *noControls}
	if *cases != "" {
		file, err := casesFile(*cases, config.ArchiveDir, *offset > 0)
		if err != nil {
			return err
		}
		defer file.Close()
		opts.Cases = file
	}
	start := time.Now()
	report, err := w.Replay(context.Background(), opts)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	report.Print(out)
	if *cases != "" {
		fmt.Fprintf(out, "%d cases written to %s\n", report.Cases, *cases)
	}
	fmt.Fprintf(out, "in %s\n", time.Since(start).Round(time.Second))
	return nil
}
