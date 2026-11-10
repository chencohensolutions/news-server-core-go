package scrape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html/charset"
)

// DefaultUserAgent is the app's: a phone's Safari, since some sites refuse anything else.
const DefaultUserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 " +
	"(KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"

// pushbackStatuses mean the site wants fewer requests: too many, refused, or its server in trouble.
var pushbackStatuses = map[int]bool{403: true, 429: true, 500: true, 502: true, 503: true, 504: true}

// Engine downloads pages and reads them with ParseList and ParseArticle (the Kotlin ScraperEngine.kt). Every request
// goes through Politeness: one at a time per site, a gap between them, a pause when the site pushes back. Headline
// pages are asked for only if changed since the last time (FetchLatest).
type Engine struct {
	HTTP       *http.Client
	Politeness *Politeness

	mu        sync.Mutex
	listPages map[string]listPage
}

// listPage is a headline page as last read: its validators, and the headlines read from it.
type listPage struct {
	etag, lastModified string
	items              []ArticleSummary
}

// NewEngine is an engine on client (nil: http.DefaultClient) with politeness (nil: NewPoliteness()).
func NewEngine(client *http.Client, politeness *Politeness) *Engine {
	if client == nil {
		client = http.DefaultClient
	}
	if politeness == nil {
		politeness = NewPoliteness()
	}
	return &Engine{HTTP: client, Politeness: politeness, listPages: map[string]listPage{}}
}

// FetchLatest fetches every headline page of c and merges the results, de-duplicated by url. A page the site says is
// unchanged (304, against the ETag or Last-Modified it gave before) gives the headlines read from it last time.
func (e *Engine) FetchLatest(ctx context.Context, c *SourceConfig) ([]ArticleSummary, error) {
	var all []ArticleSummary
	for _, pageURL := range c.ListPageURLs() {
		items, err := e.listPage(ctx, c, pageURL)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return MergeDuplicates(all), nil
}

func (e *Engine) listPage(ctx context.Context, c *SourceConfig, pageURL string) ([]ArticleSummary, error) {
	e.mu.Lock()
	known, isKnown := e.listPages[pageURL]
	e.mu.Unlock()
	var validators *listPage
	if isKnown {
		validators = &known
	}
	response, err := e.get(ctx, c, pageURL, validators)
	if err != nil {
		return nil, err
	}
	if response.status == http.StatusNotModified && isKnown {
		return known.items, nil
	}
	page, err := body(c, pageURL, response)
	if err != nil {
		return nil, err
	}
	items, err := ParseList(c, page, pageURL)
	if err != nil {
		return nil, err
	}
	if response.etag != "" || response.lastModified != "" {
		e.mu.Lock()
		e.listPages[pageURL] = listPage{etag: response.etag, lastModified: response.lastModified, items: items}
		e.mu.Unlock()
	}
	return items, nil
}

// FetchArticle downloads url and reads it as an article of c.
func (e *Engine) FetchArticle(ctx context.Context, c *SourceConfig, url string) (*Article, error) {
	page, err := e.FetchHTML(ctx, c, url)
	if err != nil {
		return nil, err
	}
	return ParseArticle(c, page, url)
}

// FetchHTML downloads url in its site's turn.
func (e *Engine) FetchHTML(ctx context.Context, c *SourceConfig, url string) (string, error) {
	response, err := e.get(ctx, c, url, nil)
	if err != nil {
		return "", err
	}
	return body(c, url, response)
}

// Pushback pauses the site of url as for a 429: it pushed back by other means than its status (a block page read as
// an article).
func (e *Engine) Pushback(url string) { e.Politeness.Pushback(hostOf(url), 0) }

// fetched is a response as the engine keeps it: read whole within the site's turn, so one site's downloads never
// overlap.
type fetched struct {
	status             int
	text               *string
	etag, lastModified string
}

// get GETs url in its site's turn; known makes it conditional. Statuses are checked by body.
func (e *Engine) get(ctx context.Context, c *SourceConfig, url string, known *listPage) (*fetched, error) {
	host := hostOf(url)
	var out *fetched
	err := e.Politeness.Request(ctx, host, func() error {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return &ScrapeError{fmt.Sprintf("%s: request to %s failed: %v", c.ID, url, err)}
		}
		userAgent := c.HTTP.UserAgent
		if userAgent == "" {
			userAgent = DefaultUserAgent
		}
		request.Header.Set("User-Agent", userAgent)
		request.Header.Set("Accept", "text/html,application/xhtml+xml")
		for name, value := range c.HTTP.Headers {
			request.Header.Set(name, value)
		}
		if known != nil && known.etag != "" {
			request.Header.Set("If-None-Match", known.etag)
		}
		if known != nil && known.lastModified != "" {
			request.Header.Set("If-Modified-Since", known.lastModified)
		}
		response, err := e.HTTP.Do(request)
		if err != nil {
			return &ScrapeError{fmt.Sprintf("%s: request to %s failed: %v", c.ID, url, err)}
		}
		defer response.Body.Close()
		switch {
		case pushbackStatuses[response.StatusCode]:
			var retryAfter time.Duration
			if seconds, err := strconv.ParseInt(strings.TrimSpace(response.Header.Get("Retry-After")), 10, 64); err == nil {
				retryAfter = time.Duration(seconds) * time.Second
			}
			e.Politeness.Pushback(host, retryAfter)
		case isSuccess(response.StatusCode) || response.StatusCode == http.StatusNotModified:
			e.Politeness.Success(host)
		}
		out = &fetched{status: response.StatusCode, etag: response.Header.Get("ETag"), lastModified: response.Header.Get("Last-Modified")}
		if isSuccess(response.StatusCode) {
			text, err := readText(response)
			if err != nil {
				return &ScrapeError{fmt.Sprintf("%s: reading %s failed: %v", c.ID, url, err)}
			}
			out.text = &text
		}
		return nil
	})
	return out, err
}

func isSuccess(status int) bool { return status >= 200 && status < 300 }

// readText decodes the body by the charset its Content-Type names, UTF-8 when it names none (as Ktor's bodyAsText).
func readText(response *http.Response) (string, error) {
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	_, params, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if name := params["charset"]; name != "" && !strings.EqualFold(name, "utf-8") {
		if encoding, _ := charset.Lookup(name); encoding != nil {
			decoded, err := encoding.NewDecoder().Bytes(data)
			if err != nil {
				return "", err
			}
			return string(decoded), nil
		}
	}
	return string(data), nil
}

func body(c *SourceConfig, url string, response *fetched) (string, error) {
	if response.text == nil {
		return "", &ScrapeError{fmt.Sprintf("%s: %s returned %d %s", c.ID, url, response.status, http.StatusText(response.status))}
	}
	return *response.text, nil
}

func hostOf(raw string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return raw
}

// IsPaused reports whether err is a request not made because its site is paused.
func IsPaused(err error) bool {
	var paused *SitePausedError
	return errors.As(err, &paused)
}
