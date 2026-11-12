package summary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	control string // "" for a real step
	target  string // the control's article
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

	Requests int // sent (dry: built)
	Asked    int // stories in them, controls too
	Answered int
	Cut      int // requests whose answer was cut: their whole stories were read
	Stopped  string

	// The real steps answered, by what the incremental update made of them and what the live rewrite did: Live*
	// has new facts by the live rewrite's own "new" list, less what the version before already said.
	Real                             int
	LiveNewVersion, LiveNewQuiet     int // the live rewrite had new facts: a version, or none (a miss)
	LiveQuietVersion, LiveQuietQuiet int // the live rewrite had none
	LiveListed                       int // real steps whose live "new" list wasn't empty, restated or not
	Tripwire                         int // steps sent to be written whole after Gemini found nothing

	Stats MergeStats     // of the real steps
	Kinds map[string]int // the sentences that went into versions, by kind

	RepeatAsked, RepeatPassed     int
	StrangerAsked, StrangerPassed int
	Positions                     []PositionStats

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
}

// read reads the headlines and articles of urls from dir's archive: the headlines first seen from since on, and
// the articles from the day the earliest of those was (an article is read after its headline is seen; the article
// files hold the pages too, and are the slow part).
func (a *replayArchive) read(dir string, since time.Time, urls map[string]bool) error {
	a.headlines, a.articles = map[string]scrape.ArticleSummary{}, map[string]*scrape.Article{}
	var first time.Time
	err := backup.Read(dir, "headlines", since, func(row struct {
		URL       string                `json:"url"`
		Item      scrape.ArticleSummary `json:"item"`
		FirstSeen int64                 `json:"first_seen"`
	}) error {
		if urls[row.URL] {
			a.headlines[row.URL] = row.Item
			if seen := time.UnixMilli(row.FirstSeen); first.IsZero() || seen.Before(first) {
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
	each := min(bodyChars(settings), max(articleChars, storyBodyChars/max(len(reports), 1)))
	var b strings.Builder
	b.WriteString("=== סיפור s1 ===\n")
	for _, r := range reports {
		b.WriteString(w.report(ctx, ids, r, each, seen))
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

// replayItems are the stories the replay asks about: the steps that take a request, in time order, and among
// them the controls, two a request at places that move from request to request (so each place gets its controls).
func (w *Writer) replayItems(ctx context.Context, steps []ReplayStep, arch *replayArchive, opts ReplayOptions, settings Settings, report *ReplayReport) []replayItem {
	var real []replayItem
	for _, s := range steps {
		story := arch.story(s.Next)
		step := Step{Story: story, Prev: s.Prev}
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
		item := replayItem{step: step, plan: p, liveNew: listed, liveGenuine: genuine}
		item.liveIn, item.liveOut = w.liveChars(ctx, story, s.Prev, s.Next, settings)
		real = append(real, item)
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
						step := Step{Story: story, Prev: v, Forced: map[string]bool{address: true}}
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
				step := Step{Story: story, Prev: v}
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
	report := &ReplayReport{Dry: opts.Dry, Rebuild: map[string]int{}, Kinds: map[string]int{}}
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
		if s.Next.WrittenAt.Before(cutoff) {
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
			report.Answered++
			at := &report.Positions[position]
			at.Answered++
			switch item.control {
			case controlRepeat:
				// Nothing may come of an article the story has: no version, and not another event.
				passed := !m.Version && !contains(m.Unrelated, item.target)
				report.RepeatAsked++
				at.Controls++
				if passed {
					report.RepeatPassed++
					at.Passed++
				}
			case controlStranger:
				passed := contains(m.Unrelated, item.target)
				report.StrangerAsked++
				at.Controls++
				if passed {
					report.StrangerPassed++
					at.Passed++
				}
			default:
				report.Real++
				at.Steps++
				if item.liveNew > 0 {
					report.LiveListed++
				}
				switch {
				case item.liveGenuine > 0 && m.Version:
					report.LiveNewVersion++
				case item.liveGenuine > 0:
					report.LiveNewQuiet++
				case m.Version:
					report.LiveQuietVersion++
				default:
					report.LiveQuietQuiet++
				}
				if m.Version == (item.liveGenuine > 0) {
					at.Agree++
				}
				if w.tripwire(ctx, a, m) != "" {
					report.Tripwire++
				}
				report.Stats.add(m.Stats)
				if m.Version {
					updates := m.Story.Updates
					for _, kind := range updates[len(updates)-1].Kinds {
						report.Kinds[kind]++
					}
				}
			}
		}
	}
	return report, nil
}

func share(n, of int) string {
	if of == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", (100*n+of/2)/of)
}

// Print writes the report.
func (r *ReplayReport) Print(out io.Writer) {
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
	verb := "sent"
	if r.Dry {
		verb = "built, none sent (-dry)"
	}
	fmt.Fprintf(out, "%d requests %s: %d stories, the controls among them; %d characters in, %d a story\n", r.Requests, verb, r.Asked, r.CharsIn, r.CharsIn/max(r.Asked, 1))
	if r.Stopped != "" {
		fmt.Fprintf(out, "  %s\n", r.Stopped)
	}
	n := max(r.RealAsked, 1)
	if r.Dry {
		fmt.Fprintf(out, "a real step: %d characters in; its live rewrite: %d characters in (%s of it), %d out\n", r.RealCharsIn/n, r.LiveCharsIn/n,
			share(r.RealCharsIn, r.LiveCharsIn), r.LiveCharsOut/n)
		return
	}
	fmt.Fprintf(out, "  %d stories answered (%s); %d answers were cut and read up to the cut\n", r.Answered, share(r.Answered, r.Asked), r.Cut)
	fmt.Fprintf(out, "\nthe %d real steps answered, against the live rewrite of the same step:\n", r.Real)
	version := r.LiveNewVersion + r.LiveQuietVersion
	fmt.Fprintf(out, "  a new version: %d (%s); no version: %d (%s); of those, %d would be written whole (the tripwire)\n", version, share(version, r.Real),
		r.Real-version, share(r.Real-version, r.Real), r.Tripwire)
	live := r.LiveNewVersion + r.LiveNewQuiet
	fmt.Fprintf(out, "  the live rewrite listed new facts in %d (%s); in %d (%s) they were not the version before's in other words\n", r.LiveListed, share(r.LiveListed, r.Real), live, share(live, r.Real))
	fmt.Fprintf(out, "  live had new facts:  %d a version, %d none: recall %s\n", r.LiveNewVersion, r.LiveNewQuiet, share(r.LiveNewVersion, live))
	fmt.Fprintf(out, "  live had none:       %d a version, %d none\n", r.LiveQuietVersion, r.LiveQuietQuiet)
	s := r.Stats
	fmt.Fprintf(out, "\nsentences Gemini answered to insert or replace: %d\n", s.Asked)
	fmt.Fprintf(out, "  dropped: %d say what the story has (restatements: %s), %d without a new article of the story, %d say what another of the answer does, %d on unknown sentences, %d garbled or cut\n",
		s.Restated, share(s.Restated, s.Asked), s.Unsourced, s.Repeated, s.Unknown, s.Garbled)
	fmt.Fprintf(out, "  left out with no version made: %d; in versions: %d inserted, %d replaced, %d deleted; %d old sentences got a new source\n", s.Unapplied, s.Inserted, s.Replaced, s.Deleted, s.Cited)
	var byKind []string
	for _, kind := range kinds {
		byKind = append(byKind, fmt.Sprintf("%d %s", r.Kinds[kind], kind))
	}
	fmt.Fprintf(out, "  by kind: %s\n", strings.Join(byKind, ", "))
	fmt.Fprintf(out, "\ncontrols:\n")
	fmt.Fprintf(out, "  an article the story has, shown as new, made nothing: %d of %d (%s)\n", r.RepeatPassed, r.RepeatAsked, share(r.RepeatPassed, r.RepeatAsked))
	fmt.Fprintf(out, "  an article of another story was found unrelated:      %d of %d (%s)\n", r.StrangerPassed, r.StrangerAsked, share(r.StrangerPassed, r.StrangerAsked))
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
