package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/fetch"
	"github.com/chencohensolutions/news-server-core-go/refresh"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/sites"
	"github.com/chencohensolutions/news-server-core-go/store"
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
func startReading(config auth.AppConfig, found []sites.Source) (*store.Store, *sites.Settings, *storyJob) {
	if config.RefreshMinutes < 0 {
		log.Printf("Reading the sites is off (refresh_minutes is %d)", config.RefreshMinutes)
		db, settings, err := openSettings(config, found)
		if err != nil {
			log.Fatal(err)
		}
		return db, settings, nil
	}
	refresher, settings, err := openRefresher(config, found)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Reading %d sites every %d minutes into %s", len(refresher.Sources), config.RefreshMinutes, config.DatabaseFile)
	var job *storyJob
	if config.StoryStrictness < 0 {
		log.Printf("Stories are off (story_strictness is %d)", config.StoryStrictness)
	} else if job, err = openStoryJob(config, refresher.Store); err != nil {
		log.Printf("Stories are off: %v", err)
		job = nil
	} else {
		job.enabled = settings.Enabled
		log.Printf("Grouping stories with %s by %s, strictness %d", config.StoryModel, config.StoryText, config.StoryStrictness)
		refresher.AfterRun = func(ctx context.Context) {
			start := time.Now()
			found, err := job.run(ctx)
			if err != nil {
				log.Printf("stories: %v", err)
				return
			}
			log.Printf("stories: %d, %d headlines embedded, in %s", len(found), job.finder.LastEmbedded, time.Since(start).Round(time.Millisecond))
		}
	}
	go refresher.Loop(context.Background(), time.Duration(config.RefreshMinutes)*time.Minute)
	return refresher.Store, settings, job
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
