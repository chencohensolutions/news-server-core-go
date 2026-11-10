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
	"github.com/chencohensolutions/news-server-core-go/store"
)

// openRefresher sets up reading the sites: their configs, the engine on the fetch client, and the database.
func openRefresher(config auth.AppConfig) (*refresh.Refresher, error) {
	dirs, err := scrape.SourceDirs(config.SourcesDir)
	if err != nil {
		return nil, fmt.Errorf("sites: %w", err)
	}
	var sources []*scrape.SourceConfig
	gaps := map[string]time.Duration{}
	for _, dir := range dirs {
		if strings.HasPrefix(filepath.Base(dir), "_") {
			continue
		}
		c, err := scrape.LoadConfig(dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		sources = append(sources, c)
		host := fetch.HostOf(c.BaseURL)
		if ms, ok := config.SiteWaitsMs[c.ID]; ok {
			gaps[host] = time.Duration(ms) * time.Millisecond
		}
	}
	db, err := store.Open(config.DatabaseFile)
	if err != nil {
		return nil, err
	}
	client := fetch.NewClient(fetch.Options{EngineDefault: scrape.DefaultUserAgent})
	engine := scrape.NewEngine(client, nil)
	engine.Politeness.SetGaps(gaps)
	feeds := map[string]bool{}
	for _, id := range config.FeedSources {
		feeds[id] = true
	}
	return &refresh.Refresher{Engine: engine, Store: db, Sources: sources, Feeds: feeds,
		Keep: time.Duration(config.KeepDays) * 24 * time.Hour}, nil
}

// startReading reads every site every refresh_minutes, in the background.
func startReading(config auth.AppConfig) {
	if config.RefreshMinutes < 0 {
		log.Printf("Reading the sites is off (refresh_minutes is %d)", config.RefreshMinutes)
		return
	}
	refresher, err := openRefresher(config)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Reading %d sites every %d minutes into %s", len(refresher.Sources), config.RefreshMinutes, config.DatabaseFile)
	go refresher.Loop(context.Background(), time.Duration(config.RefreshMinutes)*time.Minute)
}

// refreshOnce is the refresh command: one read of every site, then what it found.
func refreshOnce(config auth.AppConfig, out io.Writer) error {
	refresher, err := openRefresher(config)
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
