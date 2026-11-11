package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/fetch"
	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/graph/model"
	"github.com/chencohensolutions/news-server-core-go/refresh"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/sites"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

// openRefresher sets up reading the sites: their configs, the engine on the fetch client, the database, and the
// sites' settings (found are the sites as the API lists them).
func openRefresher(config auth.AppConfig, found []sites.Source) (*refresh.Refresher, *sites.Settings, error) {
	dirs, err := scrape.SourceDirs(config.SourcesDir)
	if err != nil {
		return nil, nil, fmt.Errorf("sites: %w", err)
	}
	var sources []*scrape.SourceConfig
	for _, dir := range dirs {
		if strings.HasPrefix(filepath.Base(dir), "_") {
			continue
		}
		c, err := scrape.LoadConfig(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", dir, err)
		}
		sources = append(sources, c)
	}
	db, settings, err := openSettings(config, found)
	if err != nil {
		return nil, nil, err
	}
	client := fetch.NewClient(fetch.Options{EngineDefault: scrape.DefaultUserAgent})
	engine := scrape.NewEngine(client, nil)
	setWaits := func(all map[string]sites.Setting) {
		gaps := map[string]time.Duration{}
		for _, c := range sources {
			if set, ok := all[c.ID]; ok {
				gaps[fetch.HostOf(c.BaseURL)] = time.Duration(set.WaitMs) * time.Millisecond
			}
		}
		engine.Politeness.SetGaps(gaps)
	}
	all := map[string]sites.Setting{}
	for _, c := range sources {
		all[c.ID] = settings.Get(c.ID)
	}
	setWaits(all)
	settings.OnChange = setWaits
	return &refresh.Refresher{Engine: engine, Store: db, Sources: sources, Settings: settings.Get,
		Keep: time.Duration(config.KeepDays) * 24 * time.Hour}, settings, nil
}

// openSettings opens the database and the sites' settings in it.
func openSettings(config auth.AppConfig, found []sites.Source) (*store.Store, *sites.Settings, error) {
	db, err := store.Open(config.DatabaseFile)
	if err != nil {
		return nil, nil, err
	}
	settings, err := sites.NewSettings(context.Background(), db, found, config.FeedSources, config.SiteWaitsMs)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, settings, nil
}

// startReading reads every site every refresh_minutes, in the background, and groups the stories after each read.
// It returns the database, the sites' settings, and the stories: nil when they are off or their model isn't there
// (the sites are read either way).
func startReading(config auth.AppConfig, found []sites.Source) (*store.Store, *sites.Settings, *storyJob, *summary.Writer) {
	if config.RefreshMinutes < 0 {
		log.Printf("Reading the sites is off (refresh_minutes is %d)", config.RefreshMinutes)
		db, settings, err := openSettings(config, found)
		if err != nil {
			log.Fatal(err)
		}
		return db, settings, nil, nil
	}
	refresher, settings, err := openRefresher(config, found)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Reading %d sites every %d minutes into %s", len(refresher.Sources), config.RefreshMinutes, config.DatabaseFile)
	var job *storyJob
	var summaries *summary.Writer
	if config.StoryStrictness < 0 {
		log.Printf("Stories are off (story_strictness is %d)", config.StoryStrictness)
	} else if job, err = openStoryJob(config, refresher.Store); err != nil {
		log.Printf("Stories are off: %v", err)
		job = nil
	} else {
		job.enabled = settings.Enabled
		g, _ := job.Grouping()
		log.Printf("Grouping stories with %s by %s, strictness %d, over %d days", config.StoryModel, g.Text, g.Strictness, g.Days)
		writer, err := openSummaries(config, refresher.Store, found)
		if err != nil {
			log.Printf("Summaries are off: %v", err)
		} else {
			job.summaries = true
			job.byGemini = writer.Group
			summaries = writer
			log.Printf("Summarizing stories with %s, at most %d requests a day", orDefault(config.GeminiModel, gemini.DefaultModel), orDefault(config.SummaryBudget, summary.DefaultBudget))
		}
		refresher.AfterRun = func(ctx context.Context) {
			start := time.Now()
			grouped, err := job.run(ctx)
			loop.set(refresher.Last().Start, time.Since(start), 0)
			if err != nil {
				log.Printf("stories: %v", err)
				return
			}
			log.Printf("stories: %d, %d headlines embedded, in %s", len(grouped), job.Embedded(), time.Since(start).Round(time.Millisecond))
			if writer == nil {
				return
			}
			grouping := time.Since(start)
			start = time.Now()
			result, err := writer.Run(ctx, grouped)
			loop.set(refresher.Last().Start, grouping, time.Since(start))
			if err != nil {
				log.Printf("summaries: %v", err)
			}
			log.Printf("summaries: %d written, %d waiting, %d held for their next version, in %s", result.Written, result.Waiting, result.Held, time.Since(start).Round(time.Millisecond))
		}
	}
	if config.ArchiveDir != "-" {
		grouped := refresher.AfterRun
		refresher.AfterRun = func(ctx context.Context) {
			if grouped != nil {
				grouped(ctx)
			}
			archiveNow(ctx, refresher.Store, config.ArchiveDir)
		}
	}
	loop.refresher, loop.every = refresher, config.RefreshMinutes
	go refresher.Loop(context.Background(), time.Duration(config.RefreshMinutes)*time.Minute)
	return refresher.Store, settings, job, summaries
}

// refreshOnce is the refresh command: one read of every site, then what it found.
func refreshOnce(config auth.AppConfig, out io.Writer) error {
	found, err := sites.Load(config.SourcesDir)
	if err != nil {
		return err
	}
	refresher, _, err := openRefresher(config, found)
	if err != nil {
		return err
	}
	defer refresher.Store.Close()
	refresher.Log = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	start := time.Now()
	result := refresher.Run(context.Background())
	fmt.Fprintf(out, "%d headlines, %d new articles in %s\n", result.Headlines, result.Articles, time.Since(start).Round(time.Second))
	for id, why := range result.Failures {
		fmt.Fprintf(out, "FAIL %s: %s\n", id, why)
	}
	return nil
}

func orDefault[T comparable](v, d T) T {
	var zero T
	if v == zero {
		return d
	}
	return v
}

// loop times the server's rounds for the statistics: the refresher's read, then the grouping and the summaries.
var loop loopTimes

type loopTimes struct {
	refresher *refresh.Refresher
	every     int

	mu                  sync.Mutex
	round               time.Time // the read these belong to
	grouping, summaries time.Duration
}

func (l *loopTimes) set(round time.Time, grouping, summaries time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.round, l.grouping, l.summaries = round, grouping, summaries
}

// stats is the latest round, or nil before one or with reading off.
func (l *loopTimes) stats() *model.LoopStats {
	if l.refresher == nil {
		return nil
	}
	last := l.refresher.Last()
	if last.Start.IsZero() {
		return nil
	}
	out := &model.LoopStats{StartedAt: last.Start, EveryMinutes: l.every, ReadSeconds: last.Took.Seconds(),
		Headlines: last.Headlines, NewArticles: last.Articles, FailedSites: len(last.Failures)}
	total := last.Took
	l.mu.Lock()
	if l.round.Equal(last.Start) {
		if l.grouping > 0 {
			g := l.grouping.Seconds()
			out.GroupSeconds = &g
			total += l.grouping
		}
		if l.summaries > 0 {
			s := l.summaries.Seconds()
			out.SummarySeconds = &s
			total += l.summaries
		}
	}
	l.mu.Unlock()
	out.TotalSeconds = total.Seconds()
	return out
}
