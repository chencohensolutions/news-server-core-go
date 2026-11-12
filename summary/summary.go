// Package summary writes the personal newspaper with Gemini: a Hebrew article for each story that changed in the
// last hours. Every sentence carries footnotes, the articles it comes from; facts only, each fact once. A few requests
// per run, under a daily request budget.
package summary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the free tier's day is Pacific time, also on Windows

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// Defaults for config.json's summary keys.
const (
	DefaultBudget      = 400 // requests a day, under the free tier's limit
	DefaultHours       = 12
	DefaultMinReports  = 2
	DefaultMaxRequests = 4
	DefaultMaxChars    = 90_000  // of article text in one request: about 35,000 tokens
	maxStories         = 12      // stories in one request: a long answer is where the model cuts sentences short
	articleChars       = 600     // of each article's body
	maxReports         = 40      // of a story's reports in the prompt, one per site first: every one but a runaway's
	storyBodyChars     = 120_000 // of a story's bodies together (~45,000 tokens): only a story of 30+ reports is cut
	maxFootnotes       = 5       // of one sentence: more can't all state it
)

// Sentence is one sentence and the URLs of the articles it comes from: its footnotes.
type Sentence struct {
	Text    string   `json:"text"`
	Sources []string `json:"sources"`
}

// StorySummary is a story's generated article.
type StorySummary struct {
	Title          string     `json:"title"`
	Sentences      []Sentence `json:"sentences"`
	Contradictions []Sentence `json:"contradictions"`
	// Unrelated are the URLs of the story's articles Gemini found to be about something else: not used, and not
	// footnotes. The grouping put them in the story by mistake.
	Unrelated []string  `json:"unrelated,omitempty"`
	WrittenAt time.Time `json:"writtenAt"`
	// ID is the story's id, kept by its later versions: its threads find it by it.
	ID string `json:"id,omitempty"`
	// Reports are the URLs of the story's reports it was written from: how the next version finds it.
	Reports []string `json:"reports,omitempty"`
	// Updates are how the story unfolded, oldest first: the first version's sentences, then each later version's new
	// facts, carried from version to version.
	Updates []Update `json:"updates,omitempty"`
	// HeadlineOnly are the reports Gemini read only the title and subtitle of: a paywall stub, or no article.
	HeadlineOnly []string `json:"headlineOnly,omitempty"`
	// Written is the settings it was written with; nil for a story written before they were kept.
	Written *Settings `json:"written,omitempty"`
}

// Update is what a version of a story added, and when it was written.
type Update struct {
	At        time.Time  `json:"at"`
	Sentences []Sentence `json:"sentences"`
	// Kinds are what each sentence is (development, correction, …), when the version is an incremental update's.
	Kinds []string `json:"kinds,omitempty"`
}

// maxUpdates is how many updates a story keeps: the first and the latest.
const maxUpdates = 30

// reportsOf is the URLs a summary was written from; for one written before it kept them, its footnotes.
func reportsOf(s StorySummary) []string {
	if len(s.Reports) > 0 {
		return s.Reports
	}
	seen := map[string]bool{}
	var out []string
	for _, sentence := range s.Sentences {
		for _, url := range sentence.Sources {
			if !seen[url] {
				seen[url] = true
				out = append(out, url)
			}
		}
	}
	return out
}

// previous is the saved summary story is a later version of: the one that shares the most reports with it, at
// least half of its own; nil for a new story. Its ID is set (its key, for one written before IDs).
func previous(story stories.Story, saved map[string]StorySummary) *StorySummary {
	urls := map[string]bool{}
	for _, r := range story.Reports {
		urls[r.URL] = true
	}
	var best *StorySummary
	bestShared := 0
	for key, s := range saved {
		reports := reportsOf(s)
		shared := 0
		for _, url := range reports {
			if urls[url] {
				shared++
			}
		}
		if shared > 0 && 2*shared >= len(reports) && (shared > bestShared || shared == bestShared && best != nil && s.WrittenAt.After(best.WrittenAt)) {
			s := s
			if s.ID == "" {
				s.ID = key
			}
			best, bestShared = &s, shared
		}
	}
	return best
}

// similarStory is the latest version of the saved story whose sentences say what sentences do (Similar), the most
// recently written first; nil when none does.
func similarStory(sentences []Sentence, saved map[string]StorySummary) *StorySummary {
	latest := map[string]StorySummary{}
	for key, s := range saved {
		if s.ID == "" {
			s.ID = key
		}
		if l, ok := latest[s.ID]; !ok || s.WrittenAt.After(l.WrittenAt) {
			latest[s.ID] = s
		}
	}
	var best *StorySummary
	for _, s := range latest {
		if (best == nil || s.WrittenAt.After(best.WrittenAt)) && Similar(sentences, s.Sentences) {
			s := s
			best = &s
		}
	}
	return best
}

// Latest is story's newest written version: the one written from its reports as they are (current), else, while the
// reports that joined it wait for its next version, the one before (not current). ok is false for a story not written.
func Latest(story stories.Story, written map[string]StorySummary) (latest StorySummary, current, ok bool) {
	key := StoryKey(story)
	if s, ok := written[key]; ok {
		if s.ID == "" {
			s.ID = key // written before ids: its key is its id
		}
		return s, true, true
	}
	prev := previous(story, written)
	if prev == nil {
		return StorySummary{}, false, false
	}
	// Its newest version: a split-off story may be closest to an earlier one.
	latest = *prev
	for key, s := range written {
		if s.ID == "" {
			s.ID = key
		}
		if s.ID == latest.ID && s.WrittenAt.After(latest.WrittenAt) {
			latest = s
		}
	}
	return latest, false, true
}

// unfolded is the updates of a story written now with sentences, whose new facts since prev are added.
func unfolded(prev *StorySummary, sentences, added []Sentence, now time.Time) []Update {
	if prev == nil {
		return []Update{{At: now, Sentences: sentences}}
	}
	updates := append([]Update{}, prev.Updates...)
	if len(updates) == 0 {
		updates = []Update{{At: prev.WrittenAt, Sentences: prev.Sentences}}
	}
	if len(added) > 0 {
		updates = append(updates, Update{At: now, Sentences: added})
	}
	if len(updates) > maxUpdates {
		updates = append(updates[:1], updates[len(updates)-maxUpdates+1:]...)
	}
	return updates
}

// Generator is Gemini (gemini.Client), or recorded answers in tests.
type Generator interface {
	Generate(ctx context.Context, req gemini.Request) (json.RawMessage, gemini.Usage, error)
}

// Writer is the summary job.
type Writer struct {
	Gemini     Generator
	Store      *store.Store
	Budget     int // requests a day; 0 is DefaultBudget
	Hours      int // the stories summarized changed in the last Hours; 0 is DefaultHours
	MinReports int // the fewest reports a story needs to be summarized; 0 is DefaultMinReports
	MaxChars   int // article text in one request; 0 is DefaultMaxChars
	// MaxRequests is the most story requests one run sends while stories wait; 0 is DefaultMaxRequests.
	MaxRequests int
	// ThreadsEvery is how often stories are linked into threads; 0 is DefaultThreadsEvery, below 0 never.
	ThreadsEvery time.Duration
	// Every is how often a run starts (refresh_minutes); 0 is 10 minutes. Extra requests keep the budget's share of
	// the runs left in the day.
	Every time.Duration
	// SiteName is a source id's display name, for the prompt.
	SiteName func(id string) string
	Now      func() time.Time
	Log      func(format string, args ...any)
	// Sleep waits between tries when Gemini is busy; nil waits for real.
	Sleep func(ctx context.Context, d time.Duration) error
	// Articles reads an article's text for the prompt; nil reads the Store's. The replay reads the archive's.
	Articles func(ctx context.Context, url string) *scrape.Article

	mu     sync.Mutex
	status Status
}

// busyWaits are the waits before trying a busy Gemini again (503 "high demand").
var busyWaits = []time.Duration{20 * time.Second, time.Minute}

// Status is the latest run, for the statistics.
type Status struct {
	At time.Time // zero before the first run
	Result
	Error string // "" when it went through
}

// Status is the latest run's.
func (w *Writer) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

// Result is what one run did.
type Result struct {
	Written int // story summaries written
	Waiting int // changed stories left for the next run
	Held    int // changed stories written lately, left until RewriteMinutes pass
}

// StoryKey names a story by its reports: a story that gains or loses a report is a new story to summarize.
func StoryKey(s stories.Story) string {
	urls := make([]string, len(s.Reports))
	for i, r := range s.Reports {
		urls[i] = r.URL
	}
	sort.Strings(urls)
	sum := sha256.Sum256([]byte(strings.Join(urls, "\n")))
	return hex.EncodeToString(sum[:12])
}

// Day is the free tier's day of t: its limits reset at midnight Pacific time.
func Day(t time.Time) string {
	pacific, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		pacific = time.UTC
	}
	return t.In(pacific).Format(time.DateOnly)
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Writer) logf(format string, args ...any) {
	if w.Log != nil {
		w.Log(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

func orDefault(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

// Run summarizes found's changed stories, then writes the digest, within the day's budget. A 429 ends the run: the
// next one tries again.
func (w *Writer) Run(ctx context.Context, found []stories.Story) (result Result, err error) {
	defer func() {
		status := Status{At: w.now(), Result: result}
		if err != nil {
			status.Error = err.Error()
		}
		w.mu.Lock()
		w.status = status
		w.mu.Unlock()
	}()
	now := w.now()
	stored, err := w.Store.Headlines(ctx, store.Recent(now, stories.MaxDays))
	if err != nil {
		return result, err
	}
	firstSeen := make(map[string]time.Time, len(stored))
	for _, h := range stored {
		firstSeen[h.URL] = h.FirstSeen
	}
	since := now.Add(-time.Duration(orDefault(w.Hours, DefaultHours)) * time.Hour)
	var window []stories.Story
	for _, s := range found {
		if len(s.Reports) >= orDefault(w.MinReports, DefaultMinReports) && newestSeen(s, firstSeen).After(since) {
			window = append(window, s)
		}
	}
	saved, err := w.Store.Summaries(ctx)
	if err != nil {
		return result, err
	}
	settings, err := LoadSettings(ctx, w.Store)
	if err != nil {
		return result, err
	}
	written, err := Summaries(ctx, w.Store)
	if err != nil {
		return result, err
	}
	gap := time.Duration(settings.RewriteMinutes) * time.Minute
	usedToday, err := w.Store.GeminiRequests(ctx, Day(now))
	if err != nil {
		return result, err
	}
	reserve := settings.ReserveAfter > 0 && usedToday >= settings.ReserveAfter
	var changed []stories.Story
	for _, s := range window {
		if _, ok := saved[StoryKey(s)]; ok {
			continue
		}
		prev := previous(s, written)
		switch {
		case prev == nil:
		case reserve:
			// Past the reserve mark only new stories are written; new versions wait for tomorrow's budget.
			result.Held++
			continue
		case gap > 0 && now.Sub(prev.WrittenAt) < gap && !(settings.RewriteOnNewSite && newSite(s, prev)):
			// A story written lately waits for its next version: the reports joining it meanwhile go in together.
			result.Held++
			continue
		}
		changed = append(changed, s)
	}
	// The most reported first.
	sort.SliceStable(changed, func(i, j int) bool { return len(changed[i].Reports) > len(changed[j].Reports) })

	day := Day(now)
	used, err := w.Store.GeminiRequests(ctx, day)
	if err != nil {
		return result, err
	}
	left := orDefault(w.Budget, DefaultBudget) - used
	// Story requests while stories wait: the first whenever a request is left, more only while the day's budget keeps
	// two for each run left until Google's day ends.
	budget := orDefault(w.Budget, DefaultBudget)
	for sent := 0; len(changed) > 0 && sent < orDefault(w.MaxRequests, DefaultMaxRequests); sent++ {
		used, err := w.Store.GeminiRequests(ctx, day)
		if err != nil {
			return result, err
		}
		left = budget - used
		if left < 1 || sent > 0 && left-1 <= 2*w.runsLeft(now) {
			break
		}
		written, asked, err := w.writeStories(ctx, changed, day, now)
		result.Written += written
		changed = changed[asked:]
		if err != nil {
			result.Waiting = len(changed) + asked - written
			return result, err
		}
		result.Waiting += asked - written
	}
	result.Waiting += len(changed)
	if used, err = w.Store.GeminiRequests(ctx, day); err != nil {
		return result, err
	}
	if budget-used < 1 {
		w.logf("summaries: the day's %d Gemini requests are used", budget)
	}
	if err := w.linkThreadsIfDue(ctx, day, now); err != nil {
		w.logf("threads: %v", err)
	}
	// In shadow, the incremental update of the stories that got a new version, in the requests the day has to
	// spare. What it does never reaches the stories above, and its failure is only logged.
	switch settings.Incremental {
	case "", IncrementalOff:
	case IncrementalShadow:
		w.shadowSafely(ctx, window, firstSeen, day, now, settings)
	default:
		w.logf("summaries: incremental %q is not built yet (off or shadow): off", settings.Incremental)
	}
	keep := map[string]bool{}
	for _, s := range found {
		keep[StoryKey(s)] = true
	}
	return result, w.Store.PruneSummaries(ctx, keep, since)
}

// newSite says s has a report of a site prev wasn't written from.
func newSite(s stories.Story, prev *StorySummary) bool {
	before := map[string]bool{}
	for _, url := range reportsOf(*prev) {
		before[url] = true
	}
	sites := map[string]bool{}
	for _, r := range s.Reports {
		if before[r.URL] {
			sites[r.SourceID] = true
		}
	}
	for _, r := range s.Reports {
		if !sites[r.SourceID] {
			return true
		}
	}
	return false
}

// runsLeft is how many runs start before the free tier's day ends (midnight Pacific time).
func (w *Writer) runsLeft(now time.Time) int {
	pacific, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		pacific = time.UTC
	}
	local := now.In(pacific)
	midnight := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, pacific)
	every := w.Every
	if every <= 0 {
		every = 10 * time.Minute
	}
	return int(midnight.Sub(now) / every)
}

// newestSeen is when the server first saw s's newest report.
func newestSeen(s stories.Story, firstSeen map[string]time.Time) time.Time {
	var newest time.Time
	for _, r := range s.Reports {
		if t := firstSeen[r.URL]; t.After(newest) {
			newest = t
		}
	}
	return newest
}

// send counts the request against the day's budget, then sends it; when Gemini is busy it waits and tries again,
// each try counted.
func (w *Writer) send(ctx context.Context, day string, req gemini.Request) (json.RawMessage, error) {
	answer, _, err := w.generate(ctx, day, req, len(busyWaits))
	if err != nil {
		return nil, err
	}
	return answer, nil
}

// generate is send with the answer's usage, trying a busy Gemini again up to retries times. With an error it
// returns what Gemini answered all the same (the part of a cut answer), and that answer's tokens are not counted.
func (w *Writer) generate(ctx context.Context, day string, req gemini.Request, retries int) (json.RawMessage, gemini.Usage, error) {
	for try := 0; ; try++ {
		if err := w.Store.CountGeminiRequest(ctx, day); err != nil {
			return nil, gemini.Usage{}, err
		}
		start := time.Now()
		answer, usage, err := w.Gemini.Generate(ctx, req)
		if errors.Is(err, gemini.ErrUnavailable) {
			// Busy is Google's failure, not a request of the quota. Counted first all the same: a stop mid-request
			// errs toward the budget.
			if err := w.Store.UncountGeminiRequest(ctx, day); err != nil {
				return nil, gemini.Usage{}, err
			}
		}
		if errors.Is(err, gemini.ErrUnavailable) && try < min(retries, len(busyWaits)) {
			w.logf("summaries: %v; trying again in %s", err, busyWaits[try])
			if err := w.sleep(ctx, busyWaits[try]); err != nil {
				return nil, gemini.Usage{}, err
			}
			continue
		}
		if err != nil {
			return answer, usage, err
		}
		if err := w.countTokens(ctx, day, usage); err != nil {
			return nil, usage, err
		}
		w.logf("summaries: Gemini answered in %s, %d tokens in (%d cached), %d out, %d thinking", time.Since(start).Round(time.Millisecond),
			usage.PromptTokens, usage.CachedTokens, usage.AnswerTokens, usage.ThoughtsTokens)
		return answer, usage, nil
	}
}

func (w *Writer) countTokens(ctx context.Context, day string, usage gemini.Usage) error {
	return w.Store.CountGeminiTokens(ctx, day, store.Tokens{In: usage.PromptTokens, Cached: usage.CachedTokens, Out: usage.AnswerTokens, Thoughts: usage.ThoughtsTokens})
}

func (w *Writer) sleep(ctx context.Context, d time.Duration) error {
	if w.Sleep != nil {
		return w.Sleep(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// sources names articles a1, a2, … in a request, so the answer's footnotes are short and checked.
type sources struct {
	ids  map[string]string // id → URL
	urls map[string]string // URL → id
	// What sentences were dropped, for the log.
	cut, unsourced, mended, oneSided int
	mendedEnd                        string // the end of a sentence cut short, for the log
	// headlineOnly are the URLs sent without a body: a paywall stub, or no article read.
	headlineOnly map[string]bool
}

// inlineRefs are footnotes the model wrote into the text too ("… נסגרו [a1, a3].").
var inlineRefs = regexp.MustCompile(`\s*[\[(]\s*a\d+(\s*[,،;]\s*a\d+)*\s*[\])]`)

// articleRef and storyRef find the ids in what the model wrote: it may write "[a3]" or "a3, a5" or "סיפור s2".
var (
	articleRef = regexp.MustCompile(`\ba\d+\b`)
	storyRef   = regexp.MustCompile(`\bs\d+\b`)
)

// arabic is a letter of the Arabic script: the model sometimes slips one into a Hebrew word.
var arabic = regexp.MustCompile(`\p{Arabic}`)

// shown are the reports of s the prompt shows: one per site first, in the story's order, then the rest, up to
// maxReports. A story of 20 reports would otherwise fill the request alone.
func shown(s stories.Story) []scrape.ArticleSummary {
	var first, rest []scrape.ArticleSummary
	sites := map[string]bool{}
	titles := map[string]bool{}
	for _, r := range s.Reports {
		// The same article under two addresses (a site's sections): once.
		if key := r.SourceID + "\x00" + strings.TrimSpace(r.Title); r.Title != "" {
			if titles[key] {
				continue
			}
			titles[key] = true
		}
		if sites[r.SourceID] {
			rest = append(rest, r)
		} else {
			sites[r.SourceID] = true
			first = append(first, r)
		}
	}
	out := append(first, rest...)
	return out[:min(len(out), maxReports)]
}

// storyID is the story id in what the model wrote for one, or "".
func storyID(s string) string { return storyRef.FindString(s) }

func newSources() *sources {
	return &sources{ids: map[string]string{}, urls: map[string]string{}}
}

func (s *sources) id(url string) string {
	if id, ok := s.urls[url]; ok {
		return id
	}
	id := fmt.Sprintf("a%d", len(s.ids)+1)
	s.ids[id], s.urls[url] = url, id
	return id
}

// sentences turns the answer's sentences into URLs. It drops footnotes that aren't in the request, or not among
// allowed when that is set (a story's own articles), and sentences left without one or not finished (a cut answer).
func (s *sources) sentences(in []answerSentence, allowed map[string]bool) []Sentence {
	out := []Sentence{}
	for _, a := range in {
		text := strings.TrimSpace(inlineRefs.ReplaceAllString(a.Text, ""))
		if text == "" || arabic.MatchString(text) {
			// Empty, or a garbled word: an Arabic letter inside the Hebrew.
			s.cut++
			continue
		}
		if !finished(text) {
			// Cut short: the model wrote an ASCII quote mark (צה"ל) that ended its JSON string. Dropped, not mended.
			s.mended++
			s.mendedEnd = string([]rune(text)[max(0, len([]rune(text))-20):])
			continue
		}
		var urls []string
		seen := map[string]bool{}
		for _, ref := range a.Sources {
			for _, id := range articleRef.FindAllString(ref, -1) {
				if url, ok := s.ids[id]; ok && !seen[url] && (allowed == nil || allowed[url]) && len(urls) < maxFootnotes {
					seen[url] = true
					urls = append(urls, url)
				}
			}
		}
		if len(urls) == 0 {
			s.unsourced++
		} else {
			out = append(out, Sentence{Text: text, Sources: urls})
		}
	}
	return out
}

// finished says text ends a sentence: a cut answer stops mid-word.
func finished(text string) bool {
	text = strings.TrimRight(text, "\"'״׳)”’ ")
	return strings.HasSuffix(text, ".") || strings.HasSuffix(text, "!") || strings.HasSuffix(text, "?") || strings.HasSuffix(text, "…")
}

// contradictions is those of in whose sides are at least two, each with its own sites and its own value: one site
// on both sides, or two sides saying the same, is no contradiction between sources. Each is a sentence citing every
// side.
func (s *sources) contradictions(in []answerContradiction, own map[string]bool, story stories.Story) []answerSentence {
	siteOf := map[string]string{}
	for _, r := range story.Reports {
		siteOf[r.URL] = r.SourceID
	}
	out := []answerSentence{}
	for _, c := range in {
		sites := map[string]int{}
		values := map[string]bool{}
		var refs []string
		ok := len(c.Sides) >= 2
		for i, side := range c.Sides {
			value := strings.TrimSpace(side.Value)
			n := 0
			for _, ref := range side.Sources {
				for _, id := range articleRef.FindAllString(ref, -1) {
					url, known := s.ids[id]
					if !known || !own[url] {
						continue
					}
					site := siteOf[url]
					if j, seen := sites[site]; seen && j != i {
						ok = false
					}
					sites[site] = i
					refs = append(refs, id)
					n++
				}
			}
			if n == 0 || value == "" || values[value] {
				ok = false
			}
			values[value] = true
		}
		if ok {
			out = append(out, answerSentence{Text: c.Text, Sources: refs})
		} else {
			s.oneSided++
		}
	}
	return out
}

// answerContradiction is a fact the sites give differently: each side's value and the articles that say it.
type answerContradiction struct {
	Text  string `json:"text"`
	Sides []struct {
		Value   string   `json:"value"`
		Sources []string `json:"sources"`
	} `json:"sides"`
}

// answerUnrelated is an article Gemini found to be about another event, and that event.
type answerUnrelated struct {
	ID    string `json:"id"`
	Event string `json:"event"`
}

type answerSentence struct {
	Text    string   `json:"text"`
	Sources []string `json:"sources"`
}

func (w *Writer) siteName(id string) string {
	if w.SiteName != nil {
		if name := w.SiteName(id); name != "" {
			return name
		}
	}
	return id
}

// report is one article as the prompt shows it: its id, site, time, title, subtitle and its body (articleText: the
// first bodyChars characters, without what isn't the article or what the story's earlier reports sent).
func (w *Writer) report(ctx context.Context, ids *sources, r scrape.ArticleSummary, bodyChars int, seen map[string]bool) string {
	return w.reportOf(ids, r, w.body(ctx, r, bodyChars, seen))
}

// body is r's body as the prompt shows it (articleText), "" for a paywall stub or an article not read.
func (w *Writer) body(ctx context.Context, r scrape.ArticleSummary, bodyChars int, seen map[string]bool) string {
	if article := w.article(ctx, r.URL); article != nil {
		return articleText(article.Body, seen, bodyChars)
	}
	return ""
}

// reportOf is report with the body given.
func (w *Writer) reportOf(ids *sources, r scrape.ArticleSummary, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s", ids.id(r.URL), w.siteName(r.SourceID))
	if r.PublishedAt != "" {
		fmt.Fprintf(&b, " · %s", r.PublishedAt)
	}
	fmt.Fprintf(&b, "\nכותרת: %s\n", oneLine(r.Title))
	if r.Subtitle != "" {
		fmt.Fprintf(&b, "משנה: %s\n", oneLine(r.Subtitle))
	} else if r.Summary != "" {
		fmt.Fprintf(&b, "תקציר: %s\n", oneLine(r.Summary))
	}
	if body != "" {
		fmt.Fprintf(&b, "גוף: %s\n", body)
	} else {
		if ids.headlineOnly == nil {
			ids.headlineOnly = map[string]bool{}
		}
		ids.headlineOnly[r.URL] = true
	}
	return b.String()
}

// article is the article read from url, or nil.
func (w *Writer) article(ctx context.Context, url string) *scrape.Article {
	if w.Articles != nil {
		return w.Articles(ctx, url)
	}
	if article, err := w.Store.Article(ctx, url); err == nil {
		return article
	}
	return nil
}

// oneLine is s on one line, with Hebrew abbreviations' quote marks as the Hebrew ones: 'צה"ל' becomes 'צה״ל' and
// "ג'" becomes "ג׳". The model copies how the input writes them, and an ASCII " in its JSON answer ends the string:
// the sentence is cut at the abbreviation ("…פעילי ג").
func oneLine(s string) string { return hebrewMarks(strings.Join(strings.Fields(s), " ")) }

var (
	gershayim = regexp.MustCompile(`(\p{Hebrew})["״“”](\p{Hebrew})`)
	geresh    = regexp.MustCompile(`(\p{Hebrew})['׳‘’]`)
)

func hebrewMarks(s string) string {
	s = gershayim.ReplaceAllString(s, "$1״$2")
	return geresh.ReplaceAllString(s, "$1׳")
}

// cut is s's first n characters, at a word's end.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	s = string(r[:n])
	if i := strings.LastIndex(s, " "); i > 0 {
		s = s[:i]
	}
	return s + "…"
}

// writeStories sends the changed stories that fit in MaxChars, the most reported first, and saves what comes back.
// It returns how many stories it wrote and how many it asked for (the first of changed).
func (w *Writer) writeStories(ctx context.Context, changed []stories.Story, day string, now time.Time) (written, asked int, err error) {
	ids := newSources()
	byID := map[string]stories.Story{}
	saved, err := Summaries(ctx, w.Store)
	if err != nil {
		return 0, 0, err
	}
	prevOf := map[string]*StorySummary{}
	var prompt strings.Builder
	limit := orDefault(w.MaxChars, DefaultMaxChars)
	settings, err := LoadSettings(ctx, w.Store)
	if err != nil {
		return 0, 0, err
	}
	bodyChars := articleChars
	if settings.FullArticles {
		bodyChars = fullArticleChars
	}
	for i, s := range changed {
		var b strings.Builder
		seen := map[string]bool{}
		id := fmt.Sprintf("s%d", i+1)
		fmt.Fprintf(&b, "=== סיפור %s ===\n", id)
		reports := shown(s)
		each := min(bodyChars, max(articleChars, storyBodyChars/max(len(reports), 1)))
		for _, r := range reports {
			b.WriteString(w.report(ctx, ids, r, each, seen))
			b.WriteString("\n")
		}
		prev := previous(s, saved)
		if prev != nil {
			fmt.Fprintf(&b, "הגרסה הקודמת (%s):\n", israelTime(prev.WrittenAt))
			for _, sentence := range prev.Sentences {
				fmt.Fprintf(&b, "- %s\n", oneLine(sentence.Text))
			}
			b.WriteString("\n")
		}
		if len(byID) == lengthOf(settings.Length).perRequest || len(byID) > 0 && len([]rune(prompt.String()))+len([]rune(b.String())) > limit {
			break
		}
		prompt.WriteString(b.String())
		byID[id] = s
		prevOf[id] = prev
	}
	asked = len(byID)
	raw, err := w.send(ctx, day, gemini.Request{System: storiesSystem(settings.Length), Prompt: prompt.String(), Schema: storiesSchema})
	if err != nil {
		return 0, asked, err
	}
	var answer struct {
		Stories []struct {
			Story          string                `json:"story"`
			Title          string                `json:"title"`
			Sentences      []answerSentence      `json:"sentences"`
			Contradictions []answerContradiction `json:"contradictions"`
			Unrelated      []answerUnrelated     `json:"unrelated"`
			New            []answerSentence      `json:"new"`
		} `json:"stories"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return 0, asked, fmt.Errorf("summaries: reading Gemini's answer: %w", err)
	}
	unknown := 0
	for _, a := range answer.Stories {
		s, ok := byID[storyID(a.Story)]
		if !ok {
			unknown++
			continue
		}
		own := map[string]bool{}
		for _, r := range s.Reports {
			own[r.URL] = true
		}
		// An article Gemini found to be about something else is no source of this story.
		var unrelated []string
		for _, u := range a.Unrelated {
			if strings.TrimSpace(u.Event) == "" {
				// No other event named: not shown to be about another one.
				continue
			}
			for _, id := range articleRef.FindAllString(u.ID, -1) {
				if url := ids.ids[id]; own[url] {
					own[url] = false
					unrelated = append(unrelated, url)
				}
			}
		}
		summary := StorySummary{Title: strings.TrimSpace(a.Title), Sentences: ids.sentences(a.Sentences, own),
			Contradictions: ids.sentences(ids.contradictions(a.Contradictions, own, s), own), Unrelated: unrelated, WrittenAt: now, Written: &settings}
		if len(summary.Sentences) == 0 {
			continue
		}
		for _, r := range s.Reports {
			if own[r.URL] {
				summary.Reports = append(summary.Reports, r.URL)
				if ids.headlineOnly[r.URL] {
					summary.HeadlineOnly = append(summary.HeadlineOnly, r.URL)
				}
			}
		}
		var added []Sentence
		prev := prevOf[storyID(a.Story)]
		if prev != nil {
			added = ids.sentences(a.New, own)
		} else if like := similarStory(summary.Sentences, saved); like != nil {
			// The grouping made a new story of what an earlier one already tells (a report that joined split it, or
			// it grouped the newer reports apart): it is that story's next version, with what it says that's new.
			prev = like
			for i, c := range ChangesFrom(*like, summary).Sentences {
				if c.Kind == New {
					added = append(added, summary.Sentences[i])
				}
			}
			w.logf("summaries: %q is a new version of %q (%s): it says the same", summary.Title, like.Title, like.ID)
		}
		summary.Updates = unfolded(prev, summary.Sentences, added, now)
		summary.ID = StoryKey(s)
		if prev != nil {
			summary.ID = prev.ID
		}
		data, err := json.Marshal(summary)
		if err != nil {
			return written, asked, err
		}
		if err := w.Store.SaveSummary(ctx, StoryKey(s), data, now); err != nil {
			return written, asked, err
		}
		// Kept for good, for its threads.
		if err := w.Store.SaveStoryRecord(ctx, summary.ID, data, now); err != nil {
			return written, asked, err
		}
		// And every version, for its history.
		if err := w.Store.SaveStoryVersion(ctx, summary.ID, data, now); err != nil {
			return written, asked, err
		}
		// The grouping moves it to a closer story, or out, from the next run on.
		for _, url := range unrelated {
			var others []string
			for _, r := range s.Reports {
				if own[r.URL] {
					others = append(others, r.URL)
				}
			}
			if err := w.Store.SaveUnrelated(ctx, url, others, now); err != nil {
				return written, asked, err
			}
		}
		delete(byID, storyID(a.Story))
		written++
	}
	cutShort := fmt.Sprint(ids.mended)
	if ids.mended > 0 {
		cutShort += fmt.Sprintf(" (one ended %q)", ids.mendedEnd)
	}
	w.logf("summaries: Gemini answered for %d of %d stories (%d not asked); %d written; sentences dropped: %d garbled, %d without a source of the story, %s cut short, %d contradictions not between sites",
		len(answer.Stories), asked, unknown, written, ids.cut, ids.unsourced, cutShort, ids.oneSided)
	if written == 0 {
		w.logf("summaries: nothing written; the answer starts: %s", cut(string(raw), 600))
	}
	return written, asked, nil
}

// Summaries are every stored story summary, by StoryKey.
func Summaries(ctx context.Context, db *store.Store) (map[string]StorySummary, error) {
	saved, err := db.Summaries(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]StorySummary, len(saved))
	for key, data := range saved {
		var s StorySummary
		if json.Unmarshal(data, &s) == nil {
			out[key] = s
		}
	}
	return out, nil
}

// israelTime is t as the prompt shows it: day, month and time in Israel.
func israelTime(t time.Time) string {
	if zone, err := time.LoadLocation("Asia/Jerusalem"); err == nil {
		t = t.In(zone)
	}
	return t.Format("02.01 15:04")
}
