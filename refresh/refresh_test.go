package refresh

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
)

// A real store in a temporary folder and real HTTP sites (one server answering for every host); time is virtual.

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type site struct {
	mu       sync.Mutex
	pages    map[string]string // host+path → body; missing is a 404
	requests []string
}

func (s *site) set(path, body string) {
	s.mu.Lock()
	s.pages[path] = body
	s.mu.Unlock()
}

func (s *site) count(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func setup(t *testing.T) (*Refresher, *site, *clock) {
	t.Helper()
	s := &site{pages: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Host + r.URL.Path
		s.mu.Lock()
		s.requests = append(s.requests, key)
		body, ok := s.pages[key]
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	address := server.Listener.Addr().String()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}}
	c := &clock{now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	politeness := &scrape.Politeness{FirstPause: 2 * time.Minute, MaxPause: time.Hour, Now: c.Now,
		Sleep: func(context.Context, time.Duration) error { return nil }}
	db, err := store.Open(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var sources []*scrape.SourceConfig
	for _, id := range []string{"a", "b"} {
		config, err := scrape.ParseConfig([]byte(`{"schemaVersion": 1, "id": "` + id + `", "name": "` + id + `",
			"baseUrl": "http://` + id + `.example", "language": "he",
			"list": {"urls": ["/"], "item": ".i", "url": {"selector": "a", "attr": "href"}, "title": {"selector": "a"},
				"publishedAt": {"selector": "time"}},
			"article": {"title": {"selector": "h1"}, "subtitle": {"selector": "h2"}, "body": {"selector": "p"}}}`))
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, config)
	}
	r := &Refresher{Engine: scrape.NewEngine(client, politeness), Store: db, Sources: sources, Now: c.Now, Log: t.Logf}
	return r, s, c
}

func headlines(t *testing.T, r *Refresher) map[string]store.Headline {
	t.Helper()
	all, err := r.Store.Headlines(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Headline{}
	for _, h := range all {
		out[h.URL] = h
	}
	return out
}

func TestARunReadsHeadlinesThenTheArticlesItLacks(t *testing.T) {
	r, s, c := setup(t)
	s.set("a.example/", `<div class="i"><a href="/1">One short</a><time>2026-11-10T11:00:00Z</time></div>
		<div class="i"><a href="/2">Two</a><time>2026-11-10T11:30:00Z</time></div>`)
	s.set("a.example/1", `<h1>One, the whole title</h1><h2>The subtitle</h2><p>Text.</p>`)
	s.set("a.example/2", `<h1>Two</h1><p>First paragraph.</p>`)
	s.set("b.example/", `<div class="i"><a href="/9">Nine</a></div>`)
	s.set("b.example/9", `<h1>Nine</h1><p>x</p>`)

	result := r.Run(context.Background())
	if result.Headlines != 3 || result.Articles != 3 || len(result.Failures) != 0 {
		t.Fatalf("%+v", result)
	}
	got := headlines(t, r)
	one := got["http://a.example/1"]
	// The headline as its article has it.
	if one.Title != "One, the whole title" || one.Subtitle != "The subtitle" || got["http://a.example/2"].Subtitle != "First paragraph." {
		t.Fatalf("%+v %+v", one, got["http://a.example/2"])
	}
	// Newest first within a site (the sites are read at once, so only a.example's requests).
	s.mu.Lock()
	var mine []string
	for _, req := range s.requests {
		if strings.HasPrefix(req, "a.example/") {
			mine = append(mine, req)
		}
	}
	order := strings.Join(mine, " ")
	s.mu.Unlock()
	if !strings.Contains(order, "a.example/2 a.example/1") {
		t.Fatal(order)
	}

	c.Advance(10 * time.Minute)
	result = r.Run(context.Background())
	if result.Articles != 0 || s.count("a.example/1") != 1 {
		t.Fatalf("downloaded again: %+v", result)
	}
	again := headlines(t, r)["http://a.example/1"]
	// The next list read doesn't undo what the article filled in; the first sighting stays.
	if again.Title != "One, the whole title" || !again.FirstSeen.Equal(one.FirstSeen) || !again.LastSeen.After(one.LastSeen) {
		t.Fatalf("%+v", again)
	}
}

func TestASiteThatFailsKeepsItsHeadlines(t *testing.T) {
	r, s, c := setup(t)
	s.set("a.example/", `<div class="i"><a href="/1">One</a></div>`)
	s.set("a.example/1", `<h1>One</h1><p>x</p>`)
	r.Run(context.Background())
	s.mu.Lock()
	delete(s.pages, "a.example/")
	s.mu.Unlock()
	c.Advance(10 * time.Minute)
	result := r.Run(context.Background())
	if _, failed := result.Failures["a"]; !failed {
		t.Fatalf("%+v", result)
	}
	if _, kept := headlines(t, r)["http://a.example/1"]; !kept {
		t.Fatal("headline lost")
	}
	runs, _ := r.Store.SiteRuns(context.Background())
	if runs["a"].Error == "" || runs["a"].OKAt.IsZero() {
		t.Fatalf("%+v", runs["a"])
	}
}

func TestAnEmptyArticleIsNotKeptAndWaitsBeforeTheNextTry(t *testing.T) {
	r, s, c := setup(t)
	s.set("a.example/", `<div class="i"><a href="/1">One</a></div>`)
	s.set("a.example/1", `<h1>Sorry</h1>`)
	r.Run(context.Background())
	if has, _ := r.Store.HasArticle(context.Background(), "http://a.example/1"); has {
		t.Fatal("kept a block page")
	}
	// The site pushed back: paused, and the article waits 5 minutes.
	if r.Engine.Politeness.PausedFor("a.example") != 2*time.Minute {
		t.Fatal(r.Engine.Politeness.PausedFor("a.example"))
	}
	c.Advance(3 * time.Minute)
	r.Run(context.Background())
	if n := s.count("a.example/1"); n != 1 {
		t.Fatalf("asked %d times within its wait", n)
	}
	s.set("a.example/1", `<h1>One</h1><p>x</p>`)
	c.Advance(3 * time.Minute)
	if result := r.Run(context.Background()); result.Articles != 1 {
		t.Fatalf("%+v", result)
	}
}

func TestFutureTimesAreDropped(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	items := WithoutFutureTimes([]scrape.ArticleSummary{
		{URL: "1", PublishedAt: "2026-11-10T12:05:00Z"},
		{URL: "2", PublishedAt: "2026-11-10T23:00:00Z"},
	}, now)
	if items[0].PublishedAt == "" || items[1].PublishedAt != "" {
		t.Fatalf("%+v", items)
	}
}

func TestArticleBackoffDoublesUpToSixHours(t *testing.T) {
	for times, want := range map[int]time.Duration{1: 5 * time.Minute, 2: 10 * time.Minute, 3: 20 * time.Minute, 20: 6 * time.Hour} {
		if got := ArticleBackoff(times); got != want {
			t.Errorf("%d: %s, want %s", times, got, want)
		}
	}
}

func TestASiteSwitchedOffIsNotRead(t *testing.T) {
	r, s, _ := setup(t)
	s.set("a.example/", `<div class="i"><a href="/1">One</a></div>`)
	s.set("b.example/", `<div class="i"><a href="/1">Two</a></div>`)
	r.Settings = func(id string) store.SiteSetting { return store.SiteSetting{Enabled: id != "a"} }
	r.Run(context.Background())
	if n := s.count("a.example"); n != 0 {
		t.Fatalf("a was read %d times", n)
	}
	if s.count("b.example/") == 0 {
		t.Fatal("b wasn't read")
	}
}

func TestTheLoopKeepsItsLastRound(t *testing.T) {
	r, s, _ := setup(t)
	s.set("a.example/", `<div class="i"><a href="/1">One</a></div>`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	r.AfterRun = func(context.Context) { close(done) }
	go r.Loop(ctx, time.Hour)
	<-done
	last := r.Last()
	if last.Start.IsZero() || last.Took <= 0 || last.Headlines != 1 || len(last.Failures) != 1 {
		t.Fatalf("%+v", last)
	}
}
