// Package summary writes the personal newspaper with Gemini: a short Hebrew article for each story that changed in
// the last hours, and a digest of what is most covered in the last hour. Every sentence carries footnotes, the
// articles it comes from; facts only, each fact once. One request for the stories and one for the digest per run,
// under a daily request budget.
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
	DefaultMaxChars    = 90_000 // of article text in one request: about 35,000 tokens
	maxStories         = 12     // stories in one request: a long answer is where the model cuts sentences short
	articleChars       = 600    // of each article's body
	maxReports         = 8      // of a story's reports in the prompt, one per site first
	maxFootnotes       = 5      // of one sentence: more can't all state it
	digestStories      = 8
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
	WrittenAt      time.Time  `json:"writtenAt"`
}

// Digest is the front page: what is most covered in the last hour.
type Digest struct {
	Title     string     `json:"title"`
	Sentences []Sentence `json:"sentences"`
	WrittenAt time.Time  `json:"writtenAt"`
	// Stories are the keys of the stories it was written from: the same ones next run means no new digest.
	Stories []string `json:"stories"`
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
	// Every is how often a run starts (refresh_minutes); 0 is 10 minutes. Extra requests keep the budget's share of
	// the runs left in the day.
	Every time.Duration
	// SiteName is a source id's display name, for the prompt.
	SiteName func(id string) string
	Now      func() time.Time
	Log      func(format string, args ...any)
	// Sleep waits between tries when Gemini is busy; nil waits for real.
	Sleep func(ctx context.Context, d time.Duration) error

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
	Digest  bool
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
	stored, err := w.Store.Headlines(ctx, time.Time{})
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
	var changed []stories.Story
	for _, s := range window {
		if _, ok := saved[StoryKey(s)]; !ok {
			changed = append(changed, s)
		}
	}
	// The most reported first.
	sort.SliceStable(changed, func(i, j int) bool { return len(changed[i].Reports) > len(changed[j].Reports) })

	day := Day(now)
	used, err := w.Store.GeminiRequests(ctx, day)
	if err != nil {
		return result, err
	}
	left := orDefault(w.Budget, DefaultBudget) - used
	// Story requests while stories wait: the first whenever two requests are left (the digest goes first when the
	// budget is short), more only while the day's budget keeps two for each run left until Google's day ends.
	budget := orDefault(w.Budget, DefaultBudget)
	for sent := 0; len(changed) > 0 && sent < orDefault(w.MaxRequests, DefaultMaxRequests); sent++ {
		used, err := w.Store.GeminiRequests(ctx, day)
		if err != nil {
			return result, err
		}
		left = budget - used
		if left < 2 || sent > 0 && left-1 <= 2*w.runsLeft(now) {
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
	if saved, err = w.Store.Summaries(ctx); err != nil {
		return result, err
	}
	if used, err = w.Store.GeminiRequests(ctx, day); err != nil {
		return result, err
	}
	left = budget - used
	if left >= 1 {
		wrote, err := w.writeDigest(ctx, window, saved, firstSeen, day, now)
		result.Digest = wrote
		if err != nil {
			return result, err
		}
	} else {
		w.logf("summaries: the day's %d Gemini requests are used", orDefault(w.Budget, DefaultBudget))
	}
	keep := map[string]bool{}
	for _, s := range found {
		keep[StoryKey(s)] = true
	}
	return result, w.Store.PruneSummaries(ctx, keep, since)
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
	for try := 0; ; try++ {
		if err := w.Store.CountGeminiRequest(ctx, day); err != nil {
			return nil, err
		}
		start := time.Now()
		answer, usage, err := w.Gemini.Generate(ctx, req)
		if errors.Is(err, gemini.ErrUnavailable) && try < len(busyWaits) {
			w.logf("summaries: %v; trying again in %s", err, busyWaits[try])
			if err := w.sleep(ctx, busyWaits[try]); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := w.Store.CountGeminiTokens(ctx, day, store.Tokens{In: usage.PromptTokens, Cached: usage.CachedTokens, Out: usage.AnswerTokens, Thoughts: usage.ThoughtsTokens}); err != nil {
			return nil, err
		}
		w.logf("summaries: Gemini answered in %s, %d tokens in (%d cached), %d out, %d thinking", time.Since(start).Round(time.Millisecond),
			usage.PromptTokens, usage.CachedTokens, usage.AnswerTokens, usage.ThoughtsTokens)
		return answer, nil
	}
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
	cut, unsourced, mended int
	mendedEnd              string // the end of a sentence cut short, for the log
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
	for _, r := range s.Reports {
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

// report is one article as the prompt shows it: its id, site, time, title, subtitle and the start of its body.
func (w *Writer) report(ctx context.Context, ids *sources, r scrape.ArticleSummary) string {
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
	if article, err := w.Store.Article(ctx, r.URL); err == nil && article != nil && len(article.Body) > 0 {
		fmt.Fprintf(&b, "גוף: %s\n", cut(oneLine(strings.Join(article.Body, " ")), articleChars))
	}
	return b.String()
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
	var prompt strings.Builder
	limit := orDefault(w.MaxChars, DefaultMaxChars)
	for i, s := range changed {
		var b strings.Builder
		id := fmt.Sprintf("s%d", i+1)
		fmt.Fprintf(&b, "=== סיפור %s ===\n", id)
		for _, r := range shown(s) {
			b.WriteString(w.report(ctx, ids, r))
			b.WriteString("\n")
		}
		if len(byID) == maxStories || len(byID) > 0 && len([]rune(prompt.String()))+len([]rune(b.String())) > limit {
			break
		}
		prompt.WriteString(b.String())
		byID[id] = s
	}
	asked = len(byID)
	raw, err := w.send(ctx, day, gemini.Request{System: storiesSystem, Prompt: prompt.String(), Schema: storiesSchema})
	if err != nil {
		return 0, asked, err
	}
	var answer struct {
		Stories []struct {
			Story          string           `json:"story"`
			Title          string           `json:"title"`
			Sentences      []answerSentence `json:"sentences"`
			Contradictions []answerSentence `json:"contradictions"`
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
		summary := StorySummary{Title: strings.TrimSpace(a.Title), Sentences: ids.sentences(a.Sentences, own),
			Contradictions: ids.sentences(a.Contradictions, own), WrittenAt: now}
		if len(summary.Sentences) == 0 {
			continue
		}
		data, err := json.Marshal(summary)
		if err != nil {
			return written, asked, err
		}
		if err := w.Store.SaveSummary(ctx, StoryKey(s), data, now); err != nil {
			return written, asked, err
		}
		delete(byID, storyID(a.Story))
		written++
	}
	cutShort := fmt.Sprint(ids.mended)
	if ids.mended > 0 {
		cutShort += fmt.Sprintf(" (one ended %q)", ids.mendedEnd)
	}
	w.logf("summaries: Gemini answered for %d of %d stories (%d not asked); %d written; sentences dropped: %d garbled, %d without a source of the story, %s cut short",
		len(answer.Stories), asked, unknown, written, ids.cut, ids.unsourced, cutShort)
	if written == 0 {
		w.logf("summaries: nothing written; the answer starts: %s", cut(string(raw), 600))
	}
	return written, asked, nil
}

// writeDigest writes the front page from the stories with the most reports first seen in the last hour, unless
// they are the ones the latest digest was written from. It says whether it wrote one.
func (w *Writer) writeDigest(ctx context.Context, window []stories.Story, saved map[string]json.RawMessage,
	firstSeen map[string]time.Time, day string, now time.Time) (bool, error) {
	hourAgo := now.Add(-time.Hour)
	type hot struct {
		story stories.Story
		fresh int
	}
	var hots []hot
	for _, s := range window {
		fresh := 0
		for _, r := range s.Reports {
			if firstSeen[r.URL].After(hourAgo) {
				fresh++
			}
		}
		if fresh > 0 {
			hots = append(hots, hot{s, fresh})
		}
	}
	if len(hots) == 0 {
		return false, nil
	}
	sort.SliceStable(hots, func(i, j int) bool {
		if hots[i].fresh != hots[j].fresh {
			return hots[i].fresh > hots[j].fresh
		}
		return len(hots[i].story.Reports) > len(hots[j].story.Reports)
	})
	hots = hots[:min(len(hots), digestStories)]
	keys := make([]string, len(hots))
	for i, h := range hots {
		keys[i] = StoryKey(h.story)
	}
	if latest, err := LatestDigest(ctx, w.Store); err != nil {
		return false, err
	} else if latest != nil && strings.Join(latest.Stories, ",") == strings.Join(keys, ",") {
		return false, nil
	}

	ids := newSources()
	var prompt strings.Builder
	for i, h := range hots {
		fmt.Fprintf(&prompt, "=== סיפור %d: %d דיווחים בשעה האחרונה, %d בסך הכול ===\n", i+1, h.fresh, len(h.story.Reports))
		var summary StorySummary
		if data, ok := saved[keys[i]]; ok && json.Unmarshal(data, &summary) == nil {
			fmt.Fprintf(&prompt, "כותרת: %s\n", summary.Title)
			for _, s := range append(summary.Sentences, summary.Contradictions...) {
				var refs []string
				for _, url := range s.Sources {
					refs = append(refs, ids.id(url))
				}
				fmt.Fprintf(&prompt, "- %s [%s]\n", s.Text, strings.Join(refs, ","))
			}
		} else {
			for _, r := range h.story.Reports {
				fmt.Fprintf(&prompt, "- [%s] %s: %s", ids.id(r.URL), w.siteName(r.SourceID), oneLine(r.Title))
				if r.Subtitle != "" {
					fmt.Fprintf(&prompt, ". %s", oneLine(r.Subtitle))
				}
				prompt.WriteString("\n")
			}
		}
		prompt.WriteString("\n")
	}
	raw, err := w.send(ctx, day, gemini.Request{System: digestSystem, Prompt: prompt.String(), Schema: digestSchema})
	if err != nil {
		return false, err
	}
	var answer struct {
		Title     string           `json:"title"`
		Sentences []answerSentence `json:"sentences"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return false, fmt.Errorf("summaries: reading Gemini's digest: %w", err)
	}
	digest := Digest{Title: strings.TrimSpace(answer.Title), Sentences: ids.sentences(answer.Sentences, nil), WrittenAt: now, Stories: keys}
	if len(digest.Sentences) == 0 {
		return false, errors.New("summaries: Gemini's digest has no sentence with a source")
	}
	data, err := json.Marshal(digest)
	if err != nil {
		return false, err
	}
	return true, w.Store.SaveDigest(ctx, data, now)
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

// LatestDigest is the latest digest, or nil.
func LatestDigest(ctx context.Context, db *store.Store) (*Digest, error) {
	data, err := db.Digest(ctx)
	if err != nil || data == nil {
		return nil, err
	}
	var d Digest
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
