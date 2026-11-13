package summary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/chencohensolutions/news-server-core-go/backup"
	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// The replay of the incremental update on the archive (`news-server eval-incremental`, docs/incremental.md): the
// steps the server really took, each a story's version and the articles that joined before its next one, sent
// through the incremental request, and what came of them set against what the whole rewrite did. No labels: the
// measures are the live rewrite's own "new" facts, and three controls that have a known answer.

// ReplayOptions are the replay's.
type ReplayOptions struct {
	Archive string // the archive folder (archive_dir)
	Days    int    // the steps of the archive's last Days days; 0 is all of them
	Batch   int    // stories a request; 0 is IncrementalPerRequest
	// MaxRequests is the most requests it sends (or, dry, builds); 0 is as many as the steps take.
	MaxRequests int
	// Dry builds the requests and reports their sizes; nothing is sent.
	Dry bool
	// NoControls leaves the controls out: every story of a request is a real step.
	NoControls bool
	// Until is the last day (UTC) whose steps are replayed, and the day Days counts back from; zero, or a day after
	// the archive's last, is the archive's last. A day that is over gives the same steps on every run.
	Until time.Time
	// Sample is how many of the steps that take a request are asked, spread over all of them in time: one of each
	// of Sample equal runs of them, picked by Seed (the same seed picks the same steps). 0 is all of them.
	Sample int
	Seed   int64
	// Offset is how many stories of the selection, controls among them, are skipped: a replay that stopped is
	// gone on with from the story it stopped at (ReplayReport.Next), under the same other options.
	Offset int
	// Cases gets a line of JSON for every real step answered, every control answered wrong, and every repeat control
	// whose answer wrote a sound sentence from the story's own article (ReplayCase).
	Cases io.Writer
}

// The controls: stories whose right answer is known without a label.
const (
	controlRepeat   = "repeat"   // an article the story already has, shown as new: nothing may come of it
	controlStranger = "stranger" // an article of another story, shown as new: it must be found unrelated
)

// ReplayStep is one rewrite the archive holds: a story's version and the one before it.
type ReplayStep struct {
	Prev, Next StorySummary
}

// replayItem is one story of a replay request: a real step, or a control.
type replayItem struct {
	step    Step
	plan    Plan
	control string       // "" for a real step
	target  string       // the control's article
	n       int          // its number in the selection, from 1: what Offset counts
	next    StorySummary // of a real step: the version the live rewrite made
	// Of a real step: how many sentences the live rewrite gave as new, and how many of them the version before
	// didn't already say (restates); and the sizes of the live request and answer, in characters.
	liveNew, liveGenuine int
	liveIn, liveOut      int
}

// PositionStats are the answers by a story's place in its request.
type PositionStats struct {
	Asked    int // stories at this place
	Answered int
	// Steps are the real steps answered there, Agree the ones whose outcome (a version or none) is the live
	// rewrite's (new facts or none).
	Steps, Agree int
	// Controls are the controls answered there, Passed the ones answered right.
	Controls, Passed int
}

// ReplayReport is what a replay found.
type ReplayReport struct {
	Dry      bool
	Versions int // story versions read from the archive
	Steps    int // rewrites among them: a version with one before it
	// How the steps would be made: without a request (no new article), written whole (by reason), or asked.
	Local    int
	Rebuild  map[string]int
	Eligible int
	// LocalNews and RebuildNews are the steps of those two whose live rewrite had new facts: news the server's
	// keeping would miss (the old articles had it), and whole writings that would make a version.
	LocalNews, RebuildNews int
	// Missing are the reports of the steps the archive has no article of: shown by their titles only.
	Missing int

	// Last is the last day (UTC) replayed. Sampled are the steps asked about of the Eligible when a sample was
	// taken (0 when not), Selected the stories the replay would ask about in all (controls too), Skipped the ones
	// Offset left out.
	Last                       time.Time
	Sampled, Selected, Skipped int

	Requests int // sent (dry: built)
	Asked    int // stories in them, controls too
	Answered int
	Cut      int // requests whose answer was cut: their whole stories were read
	Stopped  string
	// Next is the Offset that goes on from where a replay stopped; 0 when it asked about everything.
	Next int

	// The real steps answered, by what the incremental update made of them and what the live rewrite did: Live*
	// has new facts by the live rewrite's own "new" list, less what the version before already said.
	Real                             int
	LiveNewVersion, LiveNewQuiet     int // the live rewrite had new facts: a version, or none (a miss)
	LiveQuietVersion, LiveQuietQuiet int // the live rewrite had none
	LiveListed                       int // real steps whose live "new" list wasn't empty, restated or not
	Tripwire                         int // steps sent to be written whole after an answer that made no version
	// Headline are the ones of Tripwire that the headline check sent: nothing of an article published after the
	// version before went into the story, and its title or subtitle names what the story lacks. MissedHeadline and
	// QuietHeadline are the same apart: of the steps where the live rewrite had new facts, and where it had none.
	Headline, MissedHeadline, QuietHeadline int
	// The steps without a version, apart by whether the live rewrite had new facts (Missed, of LiveNewQuiet) or
	// none (Quiet, of LiveQuietQuiet): Found are the ones where Gemini answered sound sentences that made no version
	// (left out, or added quietly), Tripwire the ones sent to be written whole, Both the ones that are both. MissedDropped
	// are the missed steps where Gemini answered sentences and the other rules dropped every one (in the rest of
	// the missed it answered none).
	MissedFound, MissedTripwire, MissedBoth int
	MissedDropped                           int
	QuietFound, QuietTripwire, QuietBoth    int
	// GeminiSkipped are the real steps whose answer names none of the story's new articles (Merged.Skipped): no
	// answer, so they are in none of the counts above. SkippedNews are the ones whose live rewrite had new facts,
	// and SkippedTripwire the ones of those the tripwire would write whole.
	GeminiSkipped, SkippedNews, SkippedTripwire int
	// MissedOld and QuietOld are the steps without a version where a development or a correction was kept from
	// making one because its articles were all published before the version before (MergeStats.Old): of the steps
	// where the live rewrite had new facts, and of the ones where it had none.
	MissedOld, QuietOld int
	// NewsByDetail and ExtraByDetail are the versions that only details made (MergeStats.ByDetail): a detail with
	// a number, a name or an outcome the story lacks, from an article published after the version before, and no
	// development or correction. Where the live rewrite had new facts (NewsByDetail) they are what the rule won
	// back since round 6, and where it had none (ExtraByDetail) the ones to read for a thin version.
	NewsByDetail, ExtraByDetail int
	// MissedUpdated and QuietUpdated are the steps without a version whose story changed all the same
	// (MergeStats.Quiet): sentences went into the text quietly, with no version and nothing in what's new.
	MissedUpdated, QuietUpdated int
	// The new articles the requests showed for the real steps, by when they were published against the version
	// before (Step.fresh): after it, before it, or not known. Unmentioned are the ones, of the steps answered, that
	// their story's answer names nowhere.
	ArticlesAfter, ArticlesBefore, ArticlesUndated int
	Unmentioned                                    int

	Stats MergeStats     // of the real steps
	Kinds map[string]int // the sentences that went into versions, by kind
	// LeftOutKinds are the sound sentences kept out of the story with no version made, by kind; QuietKinds the ones
	// that went into a story quietly, with no version made, by kind.
	LeftOutKinds map[string]int
	QuietKinds   map[string]int
	Cases        int // lines written to ReplayOptions.Cases

	RepeatAsked, RepeatPassed     int
	StrangerAsked, StrangerPassed int
	// The controls whose answer names none of the story's new articles: of the repeats (nothing came of those, so
	// they pass), and of the strangers (not found unrelated: they fail).
	RepeatSkipped, StrangerSkipped int
	// RepeatReshown are the repeats that passed whose answer wrote a sound sentence from the article the story was
	// written from: no version (the article is older than the story), and not in the story either (nothing goes in
	// quietly from a story's own article, MergeStats.Reshown). It is the padding that rule keeps out.
	RepeatReshown int
	Positions     []PositionStats

	// Sizes: the requests' characters (instructions and text) and tokens; the answers'; and, for the real steps
	// asked, their part of the requests and the live rewrite's request and answer, in characters.
	CharsIn, CharsOut                   int
	TokensIn, TokensOut, TokensThoughts int
	RealAsked                           int
	RealCharsIn                         int
	LiveCharsIn, LiveCharsOut           int
}

type versionRow struct {
	Key       string          `json:"key"`
	ID        string          `json:"id"`
	WrittenAt int64           `json:"written_at"`
	Summary   json.RawMessage `json:"summary"`
}

// ArchiveVersions are the story versions the archive in dir holds from since on, oldest first: the rows of
// summaries/ and story_versions/, each version once. A version written before ids has its key for one.
func ArchiveVersions(dir string, since time.Time) ([]StorySummary, error) {
	var out []StorySummary
	seen := map[string]bool{}
	for _, table := range []string{"story_versions", "summaries"} {
		err := backup.Read(dir, table, since, func(row versionRow) error {
			var s StorySummary
			if json.Unmarshal(row.Summary, &s) != nil || len(s.Sentences) == 0 {
				return nil
			}
			if s.WrittenAt.IsZero() {
				s.WrittenAt = time.UnixMilli(row.WrittenAt).UTC()
			}
			if s.ID == "" {
				s.ID = row.ID
			}
			if s.ID == "" {
				s.ID = row.Key
			}
			// One version is in both tables, and the grouping may have saved it under two keys.
			key := fmt.Sprintf("%s@%d", s.ID, s.WrittenAt.UnixMilli())
			if s.ID == "" || seen[key] {
				return nil
			}
			seen[key] = true
			out = append(out, s)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].WrittenAt.Before(out[j].WrittenAt) })
	return out, nil
}

// ReplaySteps are the rewrites among versions (oldest first): each version that has one before it, with that one.
// The version before is the latest with its id; for a version written before ids, the latest version of the story
// that shares the most reports with it, at least half its own, as the server found it then (previous).
func ReplaySteps(versions []StorySummary) []ReplayStep {
	var steps []ReplayStep
	heads := map[string]StorySummary{} // each story's latest version so far, by the story's id
	chain := map[string]string{}       // a version's id → its story's
	for _, v := range versions {
		id, ok := chain[v.ID]
		if !ok {
			story := stories.Story{}
			for url := range seenOf(v) {
				story.Reports = append(story.Reports, scrape.ArticleSummary{URL: url})
			}
			if prev := previous(story, heads); prev != nil {
				id, ok = prev.ID, true
			}
		}
		if !ok {
			chain[v.ID], heads[v.ID] = v.ID, v
			continue
		}
		steps = append(steps, ReplayStep{Prev: heads[id], Next: v})
		chain[v.ID] = id
		v.ID = id
		heads[id] = v
	}
	return steps
}

// replayArchive is what the replay reads of the archive: the versions' articles and headlines.
type replayArchive struct {
	headlines map[string]scrape.ArticleSummary
	articles  map[string]*scrape.Article
	seen      map[string]time.Time // when the server first saw each headline
}

// read reads the headlines and articles of urls from dir's archive: the headlines first seen from since on, and
// the articles from the day the earliest of those was (an article is read after its headline is seen; the article
// files hold the pages too, and are the slow part).
func (a *replayArchive) read(dir string, since time.Time, urls map[string]bool) error {
	a.headlines, a.articles, a.seen = map[string]scrape.ArticleSummary{}, map[string]*scrape.Article{}, map[string]time.Time{}
	var first time.Time
	err := backup.Read(dir, "headlines", since, func(row struct {
		URL       string                `json:"url"`
		Item      scrape.ArticleSummary `json:"item"`
		FirstSeen int64                 `json:"first_seen"`
	}) error {
		if urls[row.URL] {
			a.headlines[row.URL] = row.Item
			seen := time.UnixMilli(row.FirstSeen)
			if earlier, ok := a.seen[row.URL]; row.FirstSeen > 0 && (!ok || seen.Before(earlier)) {
				a.seen[row.URL] = seen
			}
			if first.IsZero() || seen.Before(first) {
				first = seen
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(a.headlines) == len(urls) && first.After(since) {
		since = first
	}
	// Only the wanted articles are read out of a line.
	return backup.Read(dir, "articles", since, func(row struct {
		URL     string          `json:"url"`
		Article json.RawMessage `json:"article"`
	}) error {
		if !urls[row.URL] {
			return nil
		}
		var article scrape.Article
		if json.Unmarshal(row.Article, &article) == nil {
			a.articles[row.URL] = &article
		}
		return nil
	})
}

// report is url's headline: the archive's, or what its article gives, or the address alone.
func (a *replayArchive) report(address string) scrape.ArticleSummary {
	if h, ok := a.headlines[address]; ok {
		return h
	}
	if article := a.articles[address]; article != nil {
		return scrape.ArticleSummary{SourceID: article.SourceID, URL: address, Title: article.Title, Subtitle: article.Subtitle, PublishedAt: article.PublishedAt}
	}
	site := address
	if u, err := url.Parse(address); err == nil && u.Host != "" {
		site = u.Host
	}
	return scrape.ArticleSummary{SourceID: site, URL: address}
}

// story is the story a version was written from: its reports, then the ones it found unrelated.
func (a *replayArchive) story(v StorySummary) stories.Story {
	var s stories.Story
	for _, address := range append(append([]string{}, reportsOf(v)...), v.Unrelated...) {
		s.Reports = append(s.Reports, a.report(address))
	}
	return s
}

// hasBody says the archive has address's article with a body the prompt shows.
func (a *replayArchive) hasBody(address string) bool {
	article := a.articles[address]
	return article != nil && articleText(article.Body, map[string]bool{}, fullArticleChars) != ""
}

// liveChars are the sizes of the live rewrite of a step, in characters: its part of the request (every report
// with its body, the version before, and its share of the instructions), as writeStories builds it, and what it
// answered (the story and its new facts).
func (w *Writer) liveChars(ctx context.Context, story stories.Story, prev, next StorySummary, settings Settings) (in, out int) {
	ids := newSources()
	seen := map[string]bool{}
	reports := shown(story)
	bodies := w.bodies(ctx, reports, bodyChars(settings), seen)
	var b strings.Builder
	b.WriteString("=== סיפור s1 ===\n")
	for _, r := range reports {
		b.WriteString(w.reportOf(ids, r, bodies[r.URL]))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "הגרסה הקודמת (%s):\n", israelTime(prev.WrittenAt))
	for _, s := range prev.Sentences {
		fmt.Fprintf(&b, "- %s\n", oneLine(s.Text))
	}
	// An answered sentence is its text and about 40 characters of JSON and footnotes around it.
	const around = 40
	out = len([]rune(next.Title)) + around
	for _, list := range [][]Sentence{next.Sentences, liveNew(prev, next), next.Contradictions} {
		for _, s := range list {
			out += len([]rune(s.Text)) + around
		}
	}
	// And its share of the instructions, which a request sends once for its stories.
	return len([]rune(b.String())) + len([]rune(storiesSystem(settings.Length)))/lengthOf(settings.Length).perRequest, out
}

// sampleOf are n of items, spread over them: one of each of n equal runs of them, picked by seed, in their order.
// The same items, n and seed give the same ones (PCG's sequence is fixed).
func sampleOf(items []replayItem, n int, seed int64) []replayItem {
	random := rand.New(rand.NewPCG(uint64(seed), 0))
	out := make([]replayItem, 0, n)
	for i := 0; i < n; i++ {
		from, to := i*len(items)/n, (i+1)*len(items)/n
		out = append(out, items[from+random.IntN(to-from)])
	}
	return out
}

// replayItems are the stories the replay asks about: the steps that take a request, in time order, and among
// them the controls, two a request at places that move from request to request (so each place gets its controls).
func (w *Writer) replayItems(ctx context.Context, steps []ReplayStep, arch *replayArchive, opts ReplayOptions, settings Settings, report *ReplayReport) []replayItem {
	var real []replayItem
	for _, s := range steps {
		story := arch.story(s.Next)
		step := Step{Story: story, Prev: s.Prev, Seen: arch.seen}
		p := plan(step, settings, false)
		listed, genuine := newFacts(s.Prev, s.Next)
		switch p.Kind {
		case PlanLocal:
			report.Local++
			if genuine > 0 {
				report.LocalNews++
			}
			continue
		case PlanRebuild:
			report.Rebuild[p.Reason]++
			if genuine > 0 {
				report.RebuildNews++
			}
			continue
		}
		report.Eligible++
		for _, r := range p.New {
			if arch.articles[r.URL] == nil {
				report.Missing++
			}
		}
		item := replayItem{step: step, plan: p, liveNew: listed, liveGenuine: genuine, next: s.Next}
		item.liveIn, item.liveOut = w.liveChars(ctx, story, s.Prev, s.Next, settings)
		real = append(real, item)
	}
	if opts.Sample > 0 && opts.Sample < len(real) {
		real = sampleOf(real, opts.Sample, opts.Seed)
		report.Sampled = len(real)
	}
	batch := orDefault(opts.Batch, IncrementalPerRequest)
	if opts.NoControls || batch < 3 || len(steps) < 2 {
		return real
	}
	var out []replayItem
	// The controls' stories are spread over the steps: every stride-th, a stride that doesn't divide their number.
	stride := 1
	for _, prime := range []int{7, 11, 13, 17} {
		if len(steps)%prime != 0 {
			stride = prime
			break
		}
	}
	made := 0
	control := func(kind string) (replayItem, bool) {
		// On from the last control's story, until one has what the control takes.
		for try := 0; try < len(steps); try++ {
			made++
			v := steps[(made*stride)%len(steps)].Next
			story := arch.story(v)
			switch kind {
			case controlRepeat:
				for _, s := range v.Sentences {
					for _, address := range s.Sources {
						if len(story.Reports) < 2 || !arch.hasBody(address) {
							continue
						}
						step := Step{Story: story, Prev: v, Forced: map[string]bool{address: true}, Seen: arch.seen}
						if p := plan(step, settings, false); p.Kind == PlanIncremental {
							return replayItem{step: step, plan: p, control: kind, target: address}, true
						}
					}
				}
			case controlStranger:
				// Of a few other stories' leading reports, the one least like this story.
				index := indexOf(v.Sentences)
				mine := seenOf(v)
				best, bestAt := "", 2.0
				for k := 1; k <= 12 && k < len(steps); k++ {
					other := steps[(made*stride+k)%len(steps)].Next
					if other.ID == v.ID || len(other.Reports) == 0 || mine[other.Reports[0]] || !arch.hasBody(other.Reports[0]) {
						continue
					}
					text := other.Title
					for _, s := range other.Sentences {
						text += " " + s.Text
					}
					if at := index.coverage(words(text)); at < bestAt {
						best, bestAt = other.Reports[0], at
					}
				}
				if best == "" {
					continue
				}
				story.Reports = append(story.Reports, arch.report(best))
				step := Step{Story: story, Prev: v, Seen: arch.seen}
				if p := plan(step, settings, false); p.Kind == PlanIncremental && len(p.New) == 1 {
					return replayItem{step: step, plan: p, control: kind, target: best}, true
				}
			}
		}
		return replayItem{}, false
	}
	for request := 0; len(real) > 0; request++ {
		places := map[int]string{request % batch: controlRepeat, (request + batch/2) % batch: controlStranger}
		for place := 0; place < batch; place++ {
			if kind := places[place]; kind != "" {
				if item, ok := control(kind); ok {
					out = append(out, item)
					continue
				}
			}
			if len(real) > 0 {
				out, real = append(out, real[0]), real[1:]
			}
		}
	}
	return out
}

// Replay replays the archive's rewrites through the incremental request and reports what came of it. w's Gemini
// answers and its Store counts each request in the day's budget (the real database's, so the replay and the server
// share the day); the articles come from the archive (it sets w.Articles). It stops, with what it has, at
// MaxRequests, at the day's budget, or when Gemini refuses.
func (w *Writer) Replay(ctx context.Context, opts ReplayOptions) (*ReplayReport, error) {
	report := &ReplayReport{Dry: opts.Dry, Rebuild: map[string]int{}, Kinds: map[string]int{}, LeftOutKinds: map[string]int{}, QuietKinds: map[string]int{}}
	settings, err := LoadSettings(ctx, w.Store)
	if err != nil {
		return report, err
	}
	last, ok := backup.LastDay(opts.Archive, "summaries")
	if other, has := backup.LastDay(opts.Archive, "story_versions"); has && other.After(last) {
		last, ok = other, true
	}
	if !ok {
		return report, fmt.Errorf("%s has no summaries or story_versions: is it the archive folder?", opts.Archive)
	}
	if !opts.Dry && w.Gemini == nil {
		return report, errors.New("no Gemini to ask: only a dry replay runs without one")
	}
	var end time.Time // steps from it on are left out; zero when the archive's last day is the last
	if until := opts.Until.UTC().Truncate(24 * time.Hour); !opts.Until.IsZero() && until.Before(last) {
		last, end = until, until.Add(24*time.Hour)
	}
	report.Last = last
	var cutoff, since time.Time
	if opts.Days > 0 {
		cutoff = last.Add(24*time.Hour - time.Duration(opts.Days)*24*time.Hour)
		since = cutoff.Add(-2 * 24 * time.Hour) // the versions before the first steps'
	}
	versions, err := ArchiveVersions(opts.Archive, since)
	if err != nil {
		return report, err
	}
	report.Versions = len(versions)
	var steps []ReplayStep
	urls := map[string]bool{}
	for _, s := range ReplaySteps(versions) {
		if s.Next.WrittenAt.Before(cutoff) || !end.IsZero() && !s.Next.WrittenAt.Before(end) {
			continue
		}
		steps = append(steps, s)
		for _, v := range []StorySummary{s.Prev, s.Next} {
			for address := range seenOf(v) {
				urls[address] = true
			}
		}
	}
	report.Steps = len(steps)
	arch := &replayArchive{}
	if !since.IsZero() {
		since = since.Add(-stories.MaxDays * 24 * time.Hour) // a story's reports can be that much older than its versions
	}
	w.logf("replay: %d story versions, %d of them rewrites; reading their %d reports from the archive", len(versions), len(steps), len(urls))
	if err := arch.read(opts.Archive, since, urls); err != nil {
		return report, err
	}
	w.logf("replay: %d headlines and %d articles read", len(arch.headlines), len(arch.articles))
	w.Articles = func(_ context.Context, address string) *scrape.Article { return arch.articles[address] }
	items := w.replayItems(ctx, steps, arch, opts, settings, report)
	for i := range items {
		items[i].n = i + 1
	}
	report.Selected = len(items)
	report.Skipped = min(max(opts.Offset, 0), len(items))
	items = items[report.Skipped:]
	var cases *json.Encoder
	if opts.Cases != nil {
		cases = json.NewEncoder(opts.Cases)
		cases.SetEscapeHTML(false)
	}
	batch := orDefault(opts.Batch, IncrementalPerRequest)
	report.Positions = make([]PositionStats, batch)
	system := len([]rune(incrementalSystem))
	day := Day(w.now())
	for len(items) > 0 {
		if opts.MaxRequests > 0 && report.Requests >= opts.MaxRequests {
			report.Stopped = fmt.Sprintf("%d stories were not asked: -max-requests is %d", len(items), opts.MaxRequests)
			break
		}
		stepsLeft := make([]Step, len(items))
		plans := make([]Plan, len(items))
		for i, item := range items {
			stepsLeft[i], plans[i] = item.step, item.plan
		}
		req, taken := w.build(ctx, stepsLeft, plans, batch, settings)
		sent := items[:taken]
		if !opts.Dry {
			used, err := w.Store.GeminiRequests(ctx, day)
			if err != nil {
				return report, err
			}
			if used >= orDefault(w.Budget, DefaultBudget) {
				report.Stopped = fmt.Sprintf("%d stories were not asked: the day's %d Gemini requests are used", len(items), used)
				break
			}
		}
		var raw json.RawMessage
		var usage gemini.Usage
		if !opts.Dry {
			var err error
			raw, usage, err = w.generate(ctx, day, req.gemini(), len(busyWaits))
			if errors.Is(err, gemini.ErrCut) {
				if err := w.countTokens(ctx, day, usage); err != nil {
					return report, err
				}
				report.Cut++
			} else if err != nil {
				report.Stopped = fmt.Sprintf("%d stories were not asked: %v", len(items), err)
				break
			}
		}
		items = items[taken:]
		report.Requests++
		report.Asked += taken
		report.CharsIn += system + len([]rune(req.prompt))
		for i, id := range req.order {
			report.Positions[i].Asked++
			if sent[i].control == "" {
				report.RealAsked++
				report.RealCharsIn += req.asked[id].chars + system/taken
				report.LiveCharsIn += sent[i].liveIn
				report.LiveCharsOut += sent[i].liveOut
				for _, r := range req.asked[id].sent {
					switch fresh, known := sent[i].step.fresh(r); {
					case !known:
						report.ArticlesUndated++
					case fresh:
						report.ArticlesAfter++
					default:
						report.ArticlesBefore++
					}
				}
			}
		}
		if opts.Dry {
			w.logf("request %d: %d stories, %d characters (%d of them the instructions), %d new articles", report.Requests, taken,
				system+len([]rune(req.prompt)), system, len(req.ids.ids))
			continue
		}
		report.TokensIn += usage.PromptTokens
		report.TokensOut += usage.AnswerTokens
		report.TokensThoughts += usage.ThoughtsTokens
		report.CharsOut += len([]rune(string(raw)))
		answers, _ := readAnswers(raw)
		for _, answer := range answers {
			id := storyID(answer.Story)
			a, ok := req.asked[id]
			if !ok {
				continue
			}
			delete(req.asked, id)
			position := 0
			for i, other := range req.order {
				if other == id {
					position = i
				}
			}
			item := sent[position]
			m := merge(a, answer, id, req.ids, w.now())
			tripwire, headline := w.tripwire(ctx, a, m)
			report.Answered++
			at := &report.Positions[position]
			at.Answered++
			kind := ""
			switch item.control {
			case controlRepeat:
				// Nothing may come of an article the story has: no version, and not another event.
				passed := !m.Version && !contains(m.Unrelated, item.target)
				report.RepeatAsked++
				at.Controls++
				if m.Skipped {
					report.RepeatSkipped++
				}
				switch {
				case !passed:
					kind = CaseRepeat
				case m.Stats.Reshown > 0:
					report.RepeatPassed++
					at.Passed++
					report.RepeatReshown++
					kind = CaseRepeatReshown
				default:
					report.RepeatPassed++
					at.Passed++
				}
			case controlStranger:
				passed := contains(m.Unrelated, item.target)
				report.StrangerAsked++
				at.Controls++
				if m.Skipped {
					report.StrangerSkipped++
				}
				if passed {
					report.StrangerPassed++
					at.Passed++
				} else {
					kind = CaseStranger
				}
			default:
				report.Real++
				if item.liveNew > 0 {
					report.LiveListed++
				}
				report.Unmentioned += len(m.Unmentioned)
				if m.Skipped {
					// No answer: neither a version nor "nothing new". The story would be asked about again.
					report.GeminiSkipped++
					kind = CaseSkipped
					if item.liveGenuine > 0 {
						report.SkippedNews++
						if tripwire != "" {
							report.SkippedTripwire++
						}
					}
					break
				}
				at.Steps++
				// Without a version: Gemini may have answered sound sentences all the same (found: left out, or
				// added quietly), and the story may be sent to be written whole (the tripwire).
				found := !m.Version && m.Stats.Unapplied+m.Stats.Quiet > 0
				count := func(foundIn, tripwireIn, bothIn *int) {
					if found {
						*foundIn++
					}
					if tripwire != "" {
						*tripwireIn++
					}
					if found && tripwire != "" {
						*bothIn++
					}
				}
				switch {
				case item.liveGenuine > 0 && m.Version:
					report.LiveNewVersion++
					report.NewsByDetail += m.Stats.ByDetail
					kind = CaseAgreeVersion
				case item.liveGenuine > 0:
					report.LiveNewQuiet++
					kind = CaseMissed
					count(&report.MissedFound, &report.MissedTripwire, &report.MissedBoth)
					if !found && m.Stats.Asked > 0 {
						report.MissedDropped++
					}
					if m.Stats.Old > 0 {
						report.MissedOld++
					}
					if headline {
						report.MissedHeadline++
					}
					if m.Stats.Quiet > 0 {
						report.MissedUpdated++
					}
				case m.Version:
					report.LiveQuietVersion++
					report.ExtraByDetail += m.Stats.ByDetail
					kind = CaseExtra
				default:
					report.LiveQuietQuiet++
					kind = CaseAgreeNone
					count(&report.QuietFound, &report.QuietTripwire, &report.QuietBoth)
					if m.Stats.Old > 0 {
						report.QuietOld++
					}
					if headline {
						report.QuietHeadline++
					}
					if m.Stats.Quiet > 0 {
						report.QuietUpdated++
					}
				}
				if m.Version == (item.liveGenuine > 0) {
					at.Agree++
				}
				if tripwire != "" {
					report.Tripwire++
				}
				if headline {
					report.Headline++
				}
				report.Stats.add(m.Stats)
				if m.Version {
					updates := m.Story.Updates
					for _, added := range updates[len(updates)-1].Kinds {
						report.Kinds[added]++
					}
				}
				for _, note := range m.Notes {
					switch {
					case note.Text == "":
					case note.Outcome == NoteLeftOut:
						report.LeftOutKinds[note.Kind]++
					case note.Outcome == NoteQuiet:
						report.QuietKinds[note.Kind]++
					}
				}
			}
			if cases != nil && kind != "" {
				c := w.replayCase(item, a, id, req.ids, answer, m, tripwire, arch)
				c.Kind, c.Request, c.Position = kind, report.Requests, position+1
				if err := cases.Encode(c); err != nil {
					return report, fmt.Errorf("the cases: %w", err)
				}
				report.Cases++
			}
		}
	}
	if report.Stopped != "" {
		report.Next = report.Skipped + report.Asked
	}
	return report, nil
}

// The kinds of a case: how a real step's outcome stands against the live rewrite of the same step, or the
// control that was answered wrong.
const (
	CaseMissed       = "missed"           // the live rewrite had new facts, the incremental update made no version
	CaseExtra        = "extra"            // the incremental update made a version, the live rewrite had no new facts
	CaseAgreeVersion = "agree-version"    // both had news
	CaseAgreeNone    = "agree-none"       // neither had
	CaseSkipped      = "skipped"          // the answer names none of the story's new articles: no answer to compare
	CaseRepeat       = "control-repeat"   // an article the story has, shown as new, made a version or was found unrelated
	CaseStranger     = "control-stranger" // an article of another story was not found unrelated
	// CaseRepeatReshown is a control answered right (no version, and the story as it was) all the same: the answer
	// wrote a sound sentence from the article the story has, and the rules kept it out. It is
	// written to be read for what Gemini makes of an article it was already given.
	CaseRepeatReshown = "control-repeat-reshown"
)

// ReplayCase is one story of the replay with all that was sent and answered: a line of the cases file. A sentence
// that went into the story without a version (quietly) has the outcome "quiet" among Merge.Notes. It is for
// reading a disagreement and judging who is right, so it holds the sites' headlines and articles as they are:
// the file is kept out of the archive and out of any repository.
type ReplayCase struct {
	// N is the story's number in the replay's selection, from 1: the same story has it on every run with the same
	// options, whatever -offset is.
	N     int    `json:"n"`
	Story string `json:"story"` // the story's id
	// At is the step's time: when the live rewrite wrote the next version (of a control: the version it was shown).
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	// Request and Position are where it was asked: the run's request, and its place in it, both from 1. ID is its
	// id there ("s3"): the sentence numbers start with it.
	Request  int    `json:"request"`
	Position int    `json:"position"`
	ID       string `json:"id"`
	Title    string `json:"title"` // the version before's
	// PrevAt is when the version before was written, Prev its sentences as the request numbered them.
	PrevAt time.Time      `json:"prevAt"`
	Prev   []CaseSentence `json:"prev"`
	// Old are the articles the story had (the request shows their titles), New the ones shown as new.
	Old []CaseArticle `json:"old"`
	New []CaseArticle `json:"new"`
	// Target is the control's article, by its id in the request.
	Target string `json:"target,omitempty"`
	// Answer is what Gemini answered for the story, as it was read.
	Answer Answer    `json:"answer"`
	Merge  CaseMerge `json:"merge"`
	// Live is the live rewrite of the same step; nil for a control.
	Live *CaseLive `json:"live,omitempty"`
}

// CaseSentence is a sentence of a story in a case.
type CaseSentence struct {
	N    string `json:"n,omitempty"` // its number in the request ("s3.2")
	Text string `json:"text"`
	// Sites are the sites it cites: as the request showed them for the version before's, the sites' ids otherwise.
	Sites []string `json:"sites,omitempty"`
	// Articles are the step's new articles among its sources, by their ids in the request: a live sentence without
	// one stands on articles the story had already.
	Articles []string `json:"articles,omitempty"`
}

// CaseArticle is an article of a story in a case. Body is the body exactly as the request showed it: without the
// paragraphs the story's articles gave already, up to the request's length for it; "" when it showed the headline
// alone. An article the story had has no id and no body: the request shows its title only.
type CaseArticle struct {
	ID        string `json:"id,omitempty"` // in the request ("a4")
	Site      string `json:"site"`         // the site's id
	URL       string `json:"url"`
	Published string `json:"published,omitempty"`
	// When is what the request said of a new article's time against the version before: "after", "before", or
	// none when it isn't known (Step.fresh).
	When     string `json:"when,omitempty"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	// Summary is the site's summary of a new article, which the request showed in the subtitle's place when the
	// article has none.
	Summary string `json:"summary,omitempty"`
	Body    string `json:"body,omitempty"`
}

// CaseMerge is what the server made of the answer.
type CaseMerge struct {
	Version bool `json:"version"` // a new version was made
	// Tripwire says the story would be sent to be written whole after an answer that made no version, and
	// TripwireWhy why.
	Tripwire    bool   `json:"tripwire"`
	TripwireWhy string `json:"tripwireWhy,omitempty"`
	// Headline says the tripwire's reason is the headline check.
	Headline bool `json:"headlineTripwire,omitempty"`
	// Unrelated are the new articles found to be about another event, by their ids in the request.
	Unrelated []string `json:"unrelated,omitempty"`
	// Unmentioned are the new articles the answer names nowhere, by their ids in the request; Skipped says that is
	// all of them (of a control too, whatever its kind).
	Unmentioned []string `json:"unmentioned,omitempty"`
	Skipped     bool     `json:"skipped,omitempty"`
	// Notes are what became of each operation of the answer, and why.
	Notes []MergeNote `json:"notes"`
	Stats MergeStats  `json:"stats"`
}

// CaseLive is the live rewrite's version of a step.
type CaseLive struct {
	At    time.Time `json:"at"`
	Title string    `json:"title"`
	// New are the sentences it gave as new that the version before didn't already say, Restated the ones it gave
	// as new that the version before did say (the same check as the incremental update's own sentences get).
	New      []CaseSentence `json:"new"`
	Restated []CaseSentence `json:"restated,omitempty"`
	// Sentences are the whole version.
	Sentences []CaseSentence `json:"sentences"`
}

// replayCase is the case of item, asked as a under id in a request with ids and answered answer, which made m.
func (w *Writer) replayCase(item replayItem, a *asked, id string, ids *sources, answer Answer, m Merged, tripwire string, arch *replayArchive) ReplayCase {
	prev := a.step.Prev
	c := ReplayCase{N: item.n, Story: prev.ID, At: prev.WrittenAt, ID: id, Title: prev.Title, PrevAt: prev.WrittenAt, Prev: []CaseSentence{},
		Old: []CaseArticle{}, New: []CaseArticle{}, Target: ids.urls[item.target], Answer: answer}
	site := map[string]string{}
	for _, r := range a.step.Story.Reports {
		site[r.URL] = w.siteName(r.SourceID)
	}
	for i, s := range a.plan.Kept {
		sentence := CaseSentence{N: fmt.Sprintf("%s.%d", id, i+1), Text: s.Text}
		for _, address := range s.Sources {
			if name := site[address]; name != "" && !contains(sentence.Sites, name) {
				sentence.Sites = append(sentence.Sites, name)
			}
		}
		c.Prev = append(c.Prev, sentence)
	}
	article := func(r scrape.ArticleSummary) CaseArticle {
		return CaseArticle{Site: r.SourceID, URL: r.URL, Published: r.PublishedAt, Title: r.Title, Subtitle: r.Subtitle}
	}
	for _, r := range shown(stories.Story{Reports: a.plan.Old}) {
		c.Old = append(c.Old, article(r))
	}
	fresh := map[string]bool{}
	for _, r := range a.sent {
		fresh[r.URL] = true
		shown := article(r)
		shown.ID, shown.Body = ids.urls[r.URL], a.bodies[r.URL]
		if r.Subtitle == "" {
			shown.Summary = r.Summary
		}
		switch fresh, known := a.step.fresh(r); {
		case !known:
		case fresh:
			shown.When = "after"
		default:
			shown.When = "before"
		}
		c.New = append(c.New, shown)
	}
	c.Merge = CaseMerge{Version: m.Version, Tripwire: tripwire != "", TripwireWhy: tripwire, Headline: strings.HasPrefix(tripwire, whyHeadline), Notes: m.Notes, Stats: m.Stats, Skipped: m.Skipped}
	if c.Merge.Notes == nil {
		c.Merge.Notes = []MergeNote{}
	}
	for _, address := range m.Unmentioned {
		c.Merge.Unmentioned = append(c.Merge.Unmentioned, ids.urls[address])
	}
	for _, address := range m.Unrelated {
		c.Merge.Unrelated = append(c.Merge.Unrelated, ids.urls[address])
	}
	if item.control != "" {
		return c
	}
	next := item.next
	c.At = next.WrittenAt
	sentences := func(in []Sentence) []CaseSentence {
		out := []CaseSentence{}
		for _, s := range in {
			sentence := CaseSentence{Text: s.Text}
			for _, address := range s.Sources {
				if name := arch.report(address).SourceID; !contains(sentence.Sites, name) {
					sentence.Sites = append(sentence.Sites, name)
				}
				if fresh[address] {
					sentence.Articles = append(sentence.Articles, ids.urls[address])
				}
			}
			out = append(out, sentence)
		}
		return out
	}
	var added, restated []Sentence
	index := indexOf(prev.Sentences)
	for _, s := range liveNew(prev, next) {
		if index.restates(s.Text) {
			restated = append(restated, s)
		} else {
			added = append(added, s)
		}
	}
	c.Live = &CaseLive{At: next.WrittenAt, Title: next.Title, New: sentences(added), Sentences: sentences(next.Sentences)}
	if len(restated) > 0 {
		c.Live.Restated = sentences(restated)
	}
	return c
}

func share(n, of int) string {
	if of == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", (100*n+of/2)/of)
}

// Print writes the report.
func (r *ReplayReport) Print(out io.Writer) {
	if !r.Last.IsZero() {
		fmt.Fprintf(out, "the archive's days up to %s (UTC)\n", r.Last.Format(time.DateOnly))
	}
	fmt.Fprintf(out, "%d story versions in the archive, %d of them rewrites (a version with one before it)\n", r.Versions, r.Steps)
	fmt.Fprintf(out, "  %d (%s) have no new article: kept by the server, no request (the live rewrite had new facts in %d of them)\n", r.Local, share(r.Local, r.Steps), r.LocalNews)
	rebuilt := 0
	for _, n := range r.Rebuild {
		rebuilt += n
	}
	fmt.Fprintf(out, "  %d (%s) are written whole again (the live rewrite had new facts in %d of them: those make a version):\n", rebuilt, share(rebuilt, r.Steps), r.RebuildNews)
	reasons := make([]string, 0, len(r.Rebuild))
	for reason := range r.Rebuild {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		fmt.Fprintf(out, "    %d %s\n", r.Rebuild[reason], reason)
	}
	fmt.Fprintf(out, "  %d (%s) take an incremental request; the archive lacks the article of %d of their new reports\n", r.Eligible, share(r.Eligible, r.Steps), r.Missing)
	if r.Sampled > 0 {
		fmt.Fprintf(out, "    asked about: %d of them, one of each of %d equal runs of them in time (-sample, -seed)\n", r.Sampled, r.Sampled)
	}
	if r.Skipped > 0 {
		fmt.Fprintf(out, "    the first %d of the %d stories to ask about, controls among them, were skipped (-offset)\n", r.Skipped, r.Selected)
	}
	verb := "sent"
	if r.Dry {
		verb = "built, none sent (-dry)"
	}
	fmt.Fprintf(out, "%d requests %s: %d stories, the controls among them; %d characters in, %d a story\n", r.Requests, verb, r.Asked, r.CharsIn, r.CharsIn/max(r.Asked, 1))
	if r.Stopped != "" {
		fmt.Fprintf(out, "  %s\n", r.Stopped)
	}
	if r.Next > 0 && !r.Dry {
		fmt.Fprintf(out, "  to go on from there, on the same days and with the same -batch, -sample and -seed: -offset %d\n", r.Next)
	}
	n := max(r.RealAsked, 1)
	articles := r.ArticlesAfter + r.ArticlesBefore + r.ArticlesUndated
	if !r.Dry {
		fmt.Fprintf(out, "  %d stories answered (%s); %d answers were cut and read up to the cut\n", r.Answered, share(r.Answered, r.Asked), r.Cut)
	}
	fmt.Fprintf(out, "the real steps' %d new articles: %d (%s) published after the version before, %d (%s) before it, %d of no known time\n", articles,
		r.ArticlesAfter, share(r.ArticlesAfter, articles), r.ArticlesBefore, share(r.ArticlesBefore, articles), r.ArticlesUndated)
	if r.Dry {
		fmt.Fprintf(out, "a real step: %d characters in; its live rewrite: %d characters in (%s of it), %d out\n", r.RealCharsIn/n, r.LiveCharsIn/n,
			share(r.RealCharsIn, r.LiveCharsIn), r.LiveCharsOut/n)
		return
	}
	fmt.Fprintf(out, "\nthe %d real steps answered, against the live rewrite of the same step:\n", r.Real)
	fmt.Fprintf(out, "  skipped by Gemini (the answer names none of the story's new articles; not \"nothing new\": asked again): %d (%s); the live rewrite had new facts in %d of them\n",
		r.GeminiSkipped, share(r.GeminiSkipped, r.Real), r.SkippedNews)
	fmt.Fprintf(out, "  new articles their story's answer names nowhere, the skipped steps' among them: %d\n", r.Unmentioned)
	version := r.LiveNewVersion + r.LiveQuietVersion
	read := r.Real - r.GeminiSkipped
	fmt.Fprintf(out, "  of the %d not skipped, a new version: %d (%s); no version: %d (%s); of those, %d would be written whole (the tripwire)\n", read, version, share(version, read),
		read-version, share(read-version, read), r.Tripwire)
	fmt.Fprintf(out, "  headline tripwire (of the tripwire's: nothing of an article published after the version before went into the story, and its title or subtitle names what the story lacks): %d; %d where live had new facts, %d where it had none\n",
		r.Headline, r.MissedHeadline, r.QuietHeadline)
	live := r.LiveNewVersion + r.LiveNewQuiet
	fmt.Fprintf(out, "  the live rewrite listed new facts in %d (%s); in %d (%s) they were not the version before's in other words\n", r.LiveListed, share(r.LiveListed, r.Real),
		live+r.SkippedNews, share(live+r.SkippedNews, r.Real))
	fmt.Fprintf(out, "  live had new facts:  %d a version, %d none, %d skipped by Gemini: recall %s without the skipped, %s with them as misses\n", r.LiveNewVersion, r.LiveNewQuiet, r.SkippedNews,
		share(r.LiveNewVersion, live), share(r.LiveNewVersion, live+r.SkippedNews))
	if r.SkippedNews > 0 {
		fmt.Fprintf(out, "    the tripwire would write %d of the %d skipped whole\n", r.SkippedTripwire, r.SkippedNews)
	}
	if r.LiveNewQuiet > 0 {
		either := r.MissedFound + r.MissedTripwire - r.MissedBoth
		fmt.Fprintf(out, "    the %d without a version, by what Gemini answered: sound sentences that made no version in %d; only sentences the other rules dropped in %d; no sentence in %d\n",
			r.LiveNewQuiet, r.MissedFound, r.MissedDropped, r.LiveNewQuiet-r.MissedFound-r.MissedDropped)
		fmt.Fprintf(out, "    the tripwire would write %d of the %d whole (%d of them with sound sentences that made no version); neither in %d\n", r.MissedTripwire, r.LiveNewQuiet, r.MissedBoth, r.LiveNewQuiet-either)
		fmt.Fprintf(out, "    recall counting the sound sentences that made no version (Gemini found facts, a version or not): %s\n", share(r.LiveNewVersion+r.MissedFound, live))
		fmt.Fprintf(out, "    recall counting the tripwire's whole writings (a version, or written whole): %s; counting both: %s (all three without the skipped)\n",
			share(r.LiveNewVersion+r.MissedTripwire, live), share(r.LiveNewVersion+either, live))
	}
	fmt.Fprintf(out, "  live had none:       %d a version, %d none\n", r.LiveQuietVersion, r.LiveQuietQuiet)
	if r.LiveQuietQuiet > 0 {
		fmt.Fprintf(out, "    the %d without a version: sound sentences that made no version in %d; the tripwire would write %d whole (%d of them with sound sentences that made no version)\n",
			r.LiveQuietQuiet, r.QuietFound, r.QuietTripwire, r.QuietBoth)
	}
	fmt.Fprintf(out, "  the articles' time kept a development or a correction from making a version (every article it cites was published before the version before) in %d steps without one: %d where live had new facts, %d where it had none\n",
		r.MissedOld+r.QuietOld, r.MissedOld, r.QuietOld)
	fmt.Fprintf(out, "  versions that only details made (a number, a name or an outcome the story lacks, from an article published after the version before; no development or correction): %d; %d where live had new facts, %d where it had none (read those for a thin version)\n",
		r.NewsByDetail+r.ExtraByDetail, r.NewsByDetail, r.ExtraByDetail)
	fmt.Fprintf(out, "  sentences went into the story quietly (no version, nothing in what's new) in %d steps: %d where live had new facts, %d where it had none\n",
		r.MissedUpdated+r.QuietUpdated, r.MissedUpdated, r.QuietUpdated)
	s := r.Stats
	fmt.Fprintf(out, "\nsentences Gemini answered to insert or replace: %d\n", s.Asked)
	fmt.Fprintf(out, "  dropped: %d say what the story has (restatements: %s; %d of them a development with only a time word more), %d without a new article of the story, %d say what another of the answer does, %d on unknown sentences, %d garbled or cut, %d with a number none of the articles they cite states (%d of them a count of a country's people in a word)\n",
		s.Restated, share(s.Restated, s.Asked), s.Timed, s.Unsourced, s.Repeated, s.Unknown, s.Garbled, s.Unbacked, s.Worded)
	fmt.Fprintf(out, "  in versions: %d inserted, %d replaced, %d deleted; %d old sentences got a new source\n", s.Inserted, s.Replaced, s.Deleted, s.Cited)
	fmt.Fprintf(out, "  with no version made: %d went into the story quietly; %d stayed out: %d from articles the story was written from, shown again, and %d replacements and corrections\n",
		s.Quiet, s.Unapplied, s.Reshown, s.NotInserted)
	fmt.Fprintf(out, "  inserted sentences that gave another number for a sentence of the story, and were made its correction: %d\n", s.Corrected)
	fmt.Fprintf(out, "  developments and corrections from articles published before the version before (they make no version): %d\n", s.Old)
	fmt.Fprintf(out, "  details with a number, a person or a body, or an outcome the story lacks: %d, %d of them from an article published after the version before (each makes a version)\n", s.Adds, s.Since)
	fmt.Fprintf(out, "  of those details, for a person or a body they name and no number: %d; for an outcome alone: %d; details that add none because the only numbers the story lacks in them are dates or years: %d\n", s.Named, s.Decided, s.Dated)
	var byKind, leftOut, quiet []string
	for _, kind := range kinds {
		byKind = append(byKind, fmt.Sprintf("%d %s", r.Kinds[kind], kind))
		leftOut = append(leftOut, fmt.Sprintf("%d %s", r.LeftOutKinds[kind], kind))
		quiet = append(quiet, fmt.Sprintf("%d %s", r.QuietKinds[kind], kind))
	}
	fmt.Fprintf(out, "  in versions, by kind: %s\n", strings.Join(byKind, ", "))
	fmt.Fprintf(out, "  added quietly with no version made, by kind: %s\n", strings.Join(quiet, ", "))
	fmt.Fprintf(out, "  kept out of the story with no version made, by kind: %s\n", strings.Join(leftOut, ", "))
	fmt.Fprintf(out, "\ncontrols:\n")
	fmt.Fprintf(out, "  an article the story has, shown as new, made no version: %d of %d (%s); %d of the answers named no new article (skipped: nothing came of them); sentences written from it alone were kept out of the story in %d\n",
		r.RepeatPassed, r.RepeatAsked, share(r.RepeatPassed, r.RepeatAsked), r.RepeatSkipped, r.RepeatReshown)
	fmt.Fprintf(out, "  an article of another story was found unrelated:      %d of %d (%s); %d of the answers named no new article (skipped: not found)\n",
		r.StrangerPassed, r.StrangerAsked, share(r.StrangerPassed, r.StrangerAsked), r.StrangerSkipped)
	fmt.Fprintf(out, "  by place in the request (answered; controls right; real steps that agree with the live rewrite):\n")
	for i, p := range r.Positions {
		if p.Asked > 0 {
			fmt.Fprintf(out, "    %d: %d of %d; %d of %d; %d of %d\n", i+1, p.Answered, p.Asked, p.Passed, p.Controls, p.Agree, p.Steps)
		}
	}
	fmt.Fprintf(out, "\nsizes, a story (the live rewrite's tokens are its characters at the incremental requests' tokens a character):\n")
	perIn, perOut := 0.0, 0.0
	if r.CharsIn > 0 {
		perIn = float64(r.TokensIn) / float64(r.CharsIn)
	}
	if r.CharsOut > 0 {
		perOut = float64(r.TokensOut) / float64(r.CharsOut)
	}
	fmt.Fprintf(out, "  incremental: %d characters in (%.0f tokens), %.0f tokens out (and %.0f of thinking)\n", r.RealCharsIn/n, perIn*float64(r.RealCharsIn)/float64(n),
		float64(r.TokensOut)/float64(max(r.Answered, 1)), float64(r.TokensThoughts)/float64(max(r.Answered, 1)))
	fmt.Fprintf(out, "  live:        %d characters in (%.0f tokens), %d characters out (%.0f tokens)\n", r.LiveCharsIn/n, perIn*float64(r.LiveCharsIn)/float64(n),
		r.LiveCharsOut/n, perOut*float64(r.LiveCharsOut)/float64(n))
	fmt.Fprintf(out, "  all requests: %d tokens in, %d out, %d thinking\n", r.TokensIn, r.TokensOut, r.TokensThoughts)
}
