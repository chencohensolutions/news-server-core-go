package scrape

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The Kotlin ScraperEngineTest and PolitenessTest, on a real HTTP server that answers for every host, and virtual
// time: a wait moves the clock instead of sleeping.

type virtualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *virtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *virtualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *virtualClock) Since(start time.Time) int64 { return c.Now().Sub(start).Milliseconds() }

func testPoliteness(clock *virtualClock) *Politeness {
	return &Politeness{
		Gap: time.Second, FirstPause: 2 * time.Minute, MaxPause: time.Hour, Now: clock.Now,
		Sleep: func(_ context.Context, d time.Duration) error { clock.Advance(d); return nil },
	}
}

// siteServer serves handler for every host name: requests to http://<any host>/ reach it.
func siteServer(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	address := server.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}}
}

func testConfig(t *testing.T) *SourceConfig {
	t.Helper()
	c, err := ParseConfig([]byte(`{
		"schemaVersion": 1, "id": "t", "name": "T", "baseUrl": "http://t.example", "language": "en",
		"http": {"userAgent": "test-agent", "headers": {"Cookie": "consent=1"}},
		"list": {"urls": ["/a", "/b"], "item": ".i", "url": {"selector": "a", "attr": "href"}, "title": {"selector": "a"}},
		"article": {"title": {"selector": "h1"}, "body": {"selector": "p"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func titles(items []ArticleSummary) string {
	var out []string
	for _, item := range items {
		out = append(out, item.Title)
	}
	return strings.Join(out, ",")
}

func TestFetchLatestMergesPagesAndSendsConfiguredHeaders(t *testing.T) {
	var seen []string
	client := siteServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, "http://"+r.Host+r.URL.Path+" "+r.Header.Get("User-Agent")+" "+r.Header.Get("Cookie"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/a" {
			w.Write([]byte(`<div class="i"><a href="/1">One</a></div><div class="i"><a href="/2">Two</a></div>`))
		} else {
			w.Write([]byte(`<div class="i"><a href="/2">Two again</a></div><div class="i"><a href="/3">Three</a></div>`))
		}
	})
	items, err := NewEngine(client, testPoliteness(&virtualClock{})).FetchLatest(context.Background(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(items); got != "One,Two,Three" {
		t.Fatal(got)
	}
	if got := strings.Join(seen, "|"); got != "http://t.example/a test-agent consent=1|http://t.example/b test-agent consent=1" {
		t.Fatal(got)
	}
}

func TestAnHTTPErrorIsAScrapeError(t *testing.T) {
	client := siteServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	_, err := NewEngine(client, testPoliteness(&virtualClock{})).FetchArticle(context.Background(), testConfig(t), "http://t.example/x")
	var scrapeErr *ScrapeError
	if !errors.As(err, &scrapeErr) {
		t.Fatalf("%v", err)
	}
}

func TestBodiesAreDecodedByTheirCharset(t *testing.T) {
	client := siteServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=windows-1255")
		w.Write([]byte("<h1>\xf9\xec\xe5\xed</h1><p>x</p>")) // "שלום"
	})
	article, err := NewEngine(client, testPoliteness(&virtualClock{})).FetchArticle(context.Background(), testConfig(t), "http://t.example/x")
	if err != nil {
		t.Fatal(err)
	}
	if article.Title != "שלום" {
		t.Fatal(article.Title)
	}
}

func TestOneRequestAtATimePerSiteWithAGapBetween(t *testing.T) {
	clock := &virtualClock{now: time.Unix(0, 0)}
	start := clock.Now()
	var mu sync.Mutex
	starts := map[string][]int64{}
	client := siteServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		starts[r.Host] = append(starts[r.Host], clock.Since(start))
		mu.Unlock()
		w.Write([]byte("<h1>T</h1><p>x</p>"))
	})
	engine := NewEngine(client, testPoliteness(clock))
	var wg sync.WaitGroup
	for _, url := range []string{"http://t.example/1", "http://t.example/2", "http://t.example/3"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := engine.FetchArticle(context.Background(), testConfig(t), url); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// The same site a second apart.
	if got := starts["t.example"]; len(got) != 3 || got[0] != 0 || got[1] != 1000 || got[2] != 2000 {
		t.Fatalf("t.example started at %v", got)
	}
	// Another site at once: it doesn't wait for t.example's gap.
	before := clock.Since(start)
	if _, err := engine.FetchArticle(context.Background(), testConfig(t), "http://other.example/1"); err != nil {
		t.Fatal(err)
	}
	if got := starts["other.example"]; len(got) != 1 || got[0] != before {
		t.Fatalf("other.example started at %v, want %d", got, before)
	}
}

func TestASiteCanHaveItsOwnGap(t *testing.T) {
	clock := &virtualClock{now: time.Unix(0, 0)}
	start := clock.Now()
	var starts []int64
	client := siteServer(t, func(w http.ResponseWriter, r *http.Request) {
		starts = append(starts, clock.Since(start))
		w.Write([]byte("<h1>T</h1><p>x</p>"))
	})
	politeness := testPoliteness(clock)
	// Set for the bare host, applied to its "www." too.
	politeness.SetGaps(map[string]time.Duration{"t.example": 3 * time.Second})
	engine := NewEngine(client, politeness)
	for _, url := range []string{"http://www.t.example/1", "http://www.t.example/2"} {
		if _, err := engine.FetchArticle(context.Background(), testConfig(t), url); err != nil {
			t.Fatal(err)
		}
	}
	if len(starts) != 2 || starts[0] != 0 || starts[1] != 3000 {
		t.Fatalf("started at %v", starts)
	}
}

func TestASiteThatPushesBackIsLeftAloneForAWhileLongerEachTime(t *testing.T) {
	clock := &virtualClock{now: time.Unix(0, 0)}
	status := http.StatusTooManyRequests
	calls := 0
	client := siteServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(status)
		w.Write([]byte("<h1>T</h1><p>x</p>"))
	})
	politeness := testPoliteness(clock)
	engine := NewEngine(client, politeness)
	ctx, c := context.Background(), testConfig(t)
	var scrapeErr *ScrapeError
	if _, err := engine.FetchArticle(ctx, c, "http://t.example/1"); !errors.As(err, &scrapeErr) {
		t.Fatalf("%v", err)
	}
	// Paused: no request reaches the site.
	if _, err := engine.FetchArticle(ctx, c, "http://t.example/2"); !IsPaused(err) {
		t.Fatalf("%v", err)
	}
	if calls != 1 {
		t.Fatalf("%d calls", calls)
	}
	if got := politeness.PausedFor("t.example"); got != 2*time.Minute {
		t.Fatal(got)
	}
	clock.Advance(2 * time.Minute)
	if _, err := engine.FetchArticle(ctx, c, "http://t.example/3"); !errors.As(err, &scrapeErr) {
		t.Fatalf("%v", err)
	}
	// Again: twice as long.
	if got := politeness.PausedFor("t.example"); got != 4*time.Minute {
		t.Fatal(got)
	}
	clock.Advance(4 * time.Minute)
	status = http.StatusOK
	if _, err := engine.FetchArticle(ctx, c, "http://t.example/4"); err != nil {
		t.Fatal(err)
	}
	// A success ends the doubling: the next pushback starts from the first pause.
	politeness.Pushback("t.example", 0)
	if got := politeness.PausedFor("t.example"); got != 2*time.Minute {
		t.Fatal(got)
	}
}

func TestRetryAfterIsHonouredWhenLonger(t *testing.T) {
	client := siteServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	politeness := testPoliteness(&virtualClock{})
	if _, err := NewEngine(client, politeness).FetchArticle(context.Background(), testConfig(t), "http://t.example/1"); err == nil {
		t.Fatal("no error")
	}
	if got := politeness.PausedFor("t.example"); got != 10*time.Minute {
		t.Fatal(got)
	}
}

func TestAnUnchangedHeadlinePageIsNotDownloadedAgain(t *testing.T) {
	bodies := 0
	client := siteServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		bodies++
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(`<div class="i"><a href="/1">One</a></div>`))
	})
	c := testConfig(t)
	c.List.URLs = []string{"/a"}
	engine := NewEngine(client, testPoliteness(&virtualClock{}))
	for range 2 {
		// The second time, asked with its ETag, the site says "not modified": the headlines read last time.
		items, err := engine.FetchLatest(context.Background(), c)
		if err != nil || titles(items) != "One" {
			t.Fatalf("%v %v", titles(items), err)
		}
	}
	if bodies != 1 {
		t.Fatalf("%d bodies", bodies)
	}
}
