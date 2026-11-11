// Package refresh reads every site on a schedule (the app's NewsRepository.latest, ArticleFetcher and
// BackgroundRefresh, on the server): each site's headlines, then the article of every headline the server hasn't
// read yet, newest first, each site one request at a time (scrape.Politeness), and keeps them in the store.
package refresh

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
)

// Refresher is one server's schedule.
type Refresher struct {
	Engine  *scrape.Engine
	Store   *store.Store
	Sources []*scrape.SourceConfig
	// Settings, when set, says per site whether it is read, and from its RSS feed (SourceConfig.WithFeed) instead of
	// its pages; nil reads every site's pages.
	Settings func(id string) store.SiteSetting
	// Keep is how long a headline no longer on its site stays; 0 is a week, below 0 forever.
	Keep time.Duration
	Now  func() time.Time
	Log  func(format string, args ...any)
	// AfterRun, when set, runs after each of Loop's runs: the stories, grouped from what the run stored.
	AfterRun func(ctx context.Context)

	mu   sync.Mutex
	last LastRun
}

// LastRun is Loop's latest read of the sites, for the statistics.
type LastRun struct {
	Start time.Time // zero before the first
	Took  time.Duration
	Result
}

// Last is Loop's latest read of the sites.
func (r *Refresher) Last() LastRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func (r *Refresher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Refresher) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

// Result is what one run did.
type Result struct {
	Headlines int               // read from the sites
	Articles  int               // newly downloaded
	Failures  map[string]string // site id → why its headlines couldn't be read
}

// Run reads every site once: all sites at once, each site's own requests one after another.
func (r *Refresher) Run(ctx context.Context) Result {
	result := Result{Failures: map[string]string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, config := range r.Sources {
		if r.Settings != nil && !r.Settings(config.ID).Enabled {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			headlines, articles, err := r.site(ctx, config)
			mu.Lock()
			defer mu.Unlock()
			result.Headlines += headlines
			result.Articles += articles
			if err != nil {
				result.Failures[config.ID] = err.Error()
			}
		}()
	}
	wg.Wait()
	if keep := r.keep(); keep > 0 {
		if n, err := r.Store.Prune(ctx, r.now().Add(-keep)); err != nil {
			r.logf("refresh: pruning: %v", err)
		} else if n > 0 {
			r.logf("refresh: forgot %d headlines no longer on their sites", n)
		}
	}
	return result
}

func (r *Refresher) keep() time.Duration {
	if r.Keep == 0 {
		return 7 * 24 * time.Hour
	}
	return r.Keep
}

// site reads one site's headlines, then the articles it doesn't have. A site that fails keeps its stored headlines:
// a site down for one run doesn't lose them.
func (r *Refresher) site(ctx context.Context, config *scrape.SourceConfig) (headlines, articles int, err error) {
	read := config
	if r.Settings != nil && r.Settings(config.ID).Feed {
		read = config.WithFeed()
	}
	items, err := r.Engine.FetchLatest(ctx, read)
	now := r.now()
	if err != nil {
		if saveErr := r.Store.SaveSiteRun(ctx, config.ID, 0, err, now); saveErr != nil {
			r.logf("refresh: %s: %v", config.ID, saveErr)
		}
		return 0, 0, err
	}
	items = WithoutFutureTimes(items, now)
	if err := r.Store.SaveHeadlines(ctx, items, now); err != nil {
		return 0, 0, fmt.Errorf("storing headlines: %w", err)
	}
	if err := r.Store.SaveSiteRun(ctx, config.ID, len(items), nil, now); err != nil {
		r.logf("refresh: %s: %v", config.ID, err)
	}
	return len(items), r.articles(ctx, config, items), nil
}

// articles downloads the articles of items the store doesn't have, newest first, and fills each headline from its
// article. It stops at the first request the site's pause refused: the rest would be refused too.
func (r *Refresher) articles(ctx context.Context, config *scrape.SourceConfig, items []scrape.ArticleSummary) int {
	queue := make([]scrape.ArticleSummary, 0, len(items))
	for _, item := range items {
		if has, err := r.Store.HasArticle(ctx, item.URL); err == nil && !has {
			queue = append(queue, item)
		}
	}
	// Newest first; those without a time last, in the site's order.
	sort.SliceStable(queue, func(i, j int) bool {
		a, b := queue[i].PublishedAt, queue[j].PublishedAt
		return a != "" && (b == "" || a > b)
	})
	downloaded := 0
	for _, item := range queue {
		if ctx.Err() != nil {
			break
		}
		article, err := r.article(ctx, config, item.URL)
		if scrape.IsPaused(err) {
			break
		}
		if err != nil || article == nil {
			continue
		}
		downloaded++
		if filled := item.FilledFrom(article); filled != item {
			if err := r.Store.UpdateHeadline(ctx, filled); err != nil {
				r.logf("refresh: %s: %v", item.URL, err)
			}
		}
	}
	return downloaded
}

// article downloads url's article unless an earlier failure still waits its turn (nil, nil then), and records a
// failure for the next try.
func (r *Refresher) article(ctx context.Context, config *scrape.SourceConfig, url string) (*scrape.Article, error) {
	times, next, err := r.Store.Failure(ctx, url)
	if err != nil {
		return nil, err
	}
	now := r.now()
	if times > 0 && now.Before(next) {
		return nil, nil
	}
	page, err := r.Engine.FetchHTML(ctx, config, url)
	var article *scrape.Article
	if err == nil {
		article, err = scrape.ParseArticle(config, page, url)
	}
	if err == nil && !article.HasContent() {
		// A site's error page ("we are sorry", a block) reads as a title with nothing under it: not kept, and the
		// site is left alone a while.
		r.Engine.Pushback(url)
		err = &EmptyArticleError{URL: url}
	}
	if err != nil {
		// A paused site wasn't asked: that isn't the article's failure.
		if !scrape.IsPaused(err) {
			if setErr := r.Store.SetFailure(ctx, url, times+1, now.Add(ArticleBackoff(times+1))); setErr != nil {
				r.logf("refresh: %s: %v", url, setErr)
			}
		}
		return nil, err
	}
	return article, r.Store.SaveArticle(ctx, article, page, now)
}

// Loop runs every interval until ctx ends, the first run at once.
func (r *Refresher) Loop(ctx context.Context, interval time.Duration) {
	for {
		start := time.Now()
		result := r.Run(ctx)
		r.mu.Lock()
		r.last = LastRun{Start: start, Took: time.Since(start), Result: result}
		r.mu.Unlock()
		r.logf("refresh: %d headlines, %d new articles in %s; %d sites failed",
			result.Headlines, result.Articles, time.Since(start).Round(time.Second), len(result.Failures))
		for id, why := range result.Failures {
			r.logf("refresh: %s: %s", id, why)
		}
		if r.AfterRun != nil {
			r.AfterRun(ctx)
		}
		wait := interval - time.Since(start)
		if wait < time.Minute {
			wait = time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// EmptyArticleError is a page with a title and nothing to read: an error or block page, not the article.
type EmptyArticleError struct{ URL string }

func (e *EmptyArticleError) Error() string {
	return "the site returned a page without content: " + e.URL
}

// ArticleBackoff is how long an article waits after failing times in a row: 5 minutes, doubling, at most 6 hours.
func ArticleBackoff(times int) time.Duration {
	shift := min(max(times-1, 0), 10)
	return min(5*time.Minute<<shift, 6*time.Hour)
}

// futureSlack is how far ahead of the server's clock a headline's time may be before it is ignored.
const futureSlack = 10 * time.Minute

// WithoutFutureTimes drops the times later than now by more than a few minutes: a list page's clock gone wrong (ice
// writes the hour after midnight as 12). The article page's time fills in.
func WithoutFutureTimes(items []scrape.ArticleSummary, now time.Time) []scrape.ArticleSummary {
	latest := now.Add(futureSlack)
	out := make([]scrape.ArticleSummary, len(items))
	for i, item := range items {
		if t, err := time.Parse(time.RFC3339Nano, item.PublishedAt); err == nil && t.After(latest) {
			item.PublishedAt = ""
		}
		out[i] = item
	}
	return out
}
