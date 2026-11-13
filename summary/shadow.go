package summary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// The incremental update in shadow (Settings.Incremental "shadow"): the stories are written whole as before, and
// after them a run also makes the next version of some changed stories as an update, in the requests the day has
// to spare, and keeps what came of it in story_shadow. Nothing reads that table for the app.
//
// The shadow keeps its own line of each story: a story's next step starts from the shadow's own story when it has
// one, so sentences pile up and drift as they would with the mode on, and from the live version when it has none or
// the step before sent it to be written whole (the live version is that whole writing).

const (
	// DefaultShadowDaily is the most requests a day the shadow sends: at eight stories each, about every new version
	// of a day.
	DefaultShadowDaily = 48
	// shadowKeep is how long the database keeps a shadow row; the archive keeps it for good.
	shadowKeep = 7 * 24 * time.Hour
	// shadowSentKey is where the day's count of shadow requests is kept.
	shadowSentKey = "incremental_shadow_sent"
	// shadowTries is how many times the shadow asks about a story whose answers name none of its new articles,
	// before it is left to be written whole.
	shadowTries = 2
	// whySkipped is why a story Gemini skipped that often is written whole.
	whySkipped = "Gemini skipped it twice: its answers named none of the story's new articles"
)

// What a shadow step started from.
const (
	BaseLive   = "live"   // the live version before
	BaseShadow = "shadow" // the shadow's own story
)

// Shadow is what the incremental update made of one changed story.
type Shadow struct {
	ID  string    `json:"id"`  // the story's id
	Key string    `json:"key"` // the story's reports, hashed (StoryKey)
	At  time.Time `json:"at"`
	// Plan is how the step was made (PlanLocal, PlanIncremental, PlanRebuild), Reason why the story is to be
	// written whole: the plan's, or the tripwire's after Gemini answered.
	Plan   string `json:"plan"`
	Reason string `json:"reason,omitempty"`
	// Base is what the step started from (BaseLive or BaseShadow), written at BaseAt.
	Base   string    `json:"base"`
	BaseAt time.Time `json:"baseAt"`
	// New are the articles shown as new, Gone the ones that left the story.
	New  []string `json:"new,omitempty"`
	Gone []string `json:"gone,omitempty"`
	// Answer is what Gemini answered for the story, as it came.
	Answer *Answer `json:"answer,omitempty"`
	// Story is the story after the step: the next step starts from it. nil when it is to be written whole.
	Story *StorySummary `json:"story,omitempty"`
	// Version says the step made a new version: the story's latest update is what's new.
	Version bool `json:"version"`
	// Skipped is how many answers in a row named none of the story's new articles (Merged.Skipped). Such an answer
	// is no step: with Again the story is asked about again in the next run, and until then the row doesn't count
	// as the story's (the next step starts where this one did). After shadowTries of them it is left to be written
	// whole (Reason says so, and Again is off).
	Skipped int  `json:"skipped,omitempty"`
	Again   bool `json:"again,omitempty"`
	// Full is how many articles the story had when it was last written whole.
	Full    int            `json:"full"`
	Stats   *MergeStats    `json:"stats,omitempty"`
	Request *ShadowRequest `json:"request,omitempty"`
}

// ShadowRequest is the request a shadow step was answered in.
type ShadowRequest struct {
	Stories  int  `json:"stories"`            // the stories it asked about
	Position int  `json:"position"`           // this one's place among them, from 1
	Chars    int  `json:"chars"`              // of this story in the request
	Salvaged bool `json:"salvaged,omitempty"` // read from an answer that was cut
	// The whole request's tokens.
	TokensIn       int `json:"tokensIn"`
	TokensOut      int `json:"tokensOut"`
	TokensThoughts int `json:"tokensThoughts"`
}

// keyOf is the StoryKey of the story a version was written from: its reports and the ones it found unrelated.
func keyOf(s StorySummary) string {
	urls := append(append([]string{}, reportsOf(s)...), s.Unrelated...)
	sort.Strings(urls)
	sum := sha256.Sum256([]byte(strings.Join(urls, "\n")))
	return hex.EncodeToString(sum[:12])
}

// fullOf is how many articles step's story had when it was last written whole.
func fullOf(step Step) int {
	if step.Full > 0 {
		return step.Full
	}
	return len(seenOf(step.Prev))
}

// shadowSent is how many requests the shadow sent on day.
func (w *Writer) shadowSent(ctx context.Context, day string) (int, error) {
	data, ok, err := w.Store.ServerSetting(ctx, shadowSentKey)
	if err != nil || !ok {
		return 0, err
	}
	var sent struct {
		Day      string `json:"day"`
		Requests int    `json:"requests"`
	}
	if json.Unmarshal(data, &sent) != nil || sent.Day != day {
		return 0, nil
	}
	return sent.Requests, nil
}

func (w *Writer) countShadow(ctx context.Context, day string) error {
	sent, err := w.shadowSent(ctx, day)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(map[string]any{"day": day, "requests": sent + 1})
	return w.Store.SaveServerSetting(ctx, shadowSentKey, data)
}

// spare says the day has a request for the shadow: the budget keeps two for each run left in the day after it (the
// rule of a run's extra story requests), the day isn't past the mark from which only new stories are written
// (ReserveAfter), and the shadow hasn't sent its day's share.
func (w *Writer) spare(ctx context.Context, day string, now time.Time, settings Settings) (bool, error) {
	used, err := w.Store.GeminiRequests(ctx, day)
	if err != nil {
		return false, err
	}
	if orDefault(w.Budget, DefaultBudget)-used-1 <= 2*w.runsLeft(now) || settings.ReserveAfter > 0 && used >= settings.ReserveAfter {
		return false, nil
	}
	sent, err := w.shadowSent(ctx, day)
	return sent < orDefault(settings.ShadowDaily, DefaultShadowDaily), err
}

func (w *Writer) saveShadow(ctx context.Context, s Shadow) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return w.Store.SaveShadow(ctx, s.Key, s.ID, data, s.At)
}

// shadowSafely runs shadow, and only logs what goes wrong in it, a panic too: the shadow is an experiment beside the
// run, and the run's stories are written by then.
func (w *Writer) shadowSafely(ctx context.Context, window []stories.Story, firstSeen map[string]time.Time, day string, now time.Time, settings Settings) {
	defer func() {
		if r := recover(); r != nil {
			w.logf("incremental shadow: stopped: %v", r)
		}
	}()
	if err := w.shadow(ctx, window, firstSeen, day, now, settings); err != nil {
		w.logf("incremental shadow: %v", err)
	}
}

// shadow makes the next version of window's changed stories as incremental updates, in shadow: the ones without
// a new article and the ones to be written whole are only recorded; of the others, the freshest that fit go in one
// request, when the day has one to spare, tried once. A story is recorded under its StoryKey whatever came of it
// (nothing new too), so it is asked once; one that Gemini didn't answer waits for the next run. So does one whose
// answer names none of its new articles: that is recorded as to be asked again, first in the next request, and
// the story's line doesn't move; after shadowTries such answers it is left to be written whole.
func (w *Writer) shadow(ctx context.Context, window []stories.Story, firstSeen map[string]time.Time, day string, now time.Time, settings Settings) error {
	if err := w.Store.PruneShadows(ctx, now.Add(-shadowKeep)); err != nil {
		return err
	}
	refs, err := w.Store.ShadowRefs(ctx, now.Add(-shadowKeep))
	if err != nil {
		return err
	}
	done := map[string]bool{}
	again := map[string]bool{} // skipped by Gemini: to be asked again
	latest := map[string]store.ShadowRef{}
	for _, r := range refs {
		if r.Again {
			again[r.Key] = true
			continue
		}
		done[r.Key] = true
		latest[r.ID] = r
	}
	written, err := Summaries(ctx, w.Store)
	if err != nil {
		return err
	}
	// The stories to ask about again first, then the ones that changed last: the shadow follows the news, and what
	// it has no request for grows old.
	order := append([]stories.Story{}, window...)
	sort.SliceStable(order, func(i, j int) bool {
		if a, b := again[StoryKey(order[i])], again[StoryKey(order[j])]; a != b {
			return a
		}
		return newestSeen(order[i], firstSeen).After(newestSeen(order[j], firstSeen))
	})
	var steps []Step
	var plans []Plan
	var rows []Shadow
	recorded := 0
	for _, s := range order {
		key := StoryKey(s)
		if done[key] {
			continue
		}
		// The live version before this one: the closest written story other than the one of these very reports.
		own, had := written[key]
		delete(written, key)
		prev := previous(s, written)
		merged := prev != nil && mergedInto(s, prev.ID, written)
		if had {
			written[key] = own
		}
		if prev == nil {
			continue // a first version: written whole
		}
		step := Step{Story: s, Prev: *prev, Seen: firstSeen}
		row := Shadow{ID: prev.ID, Key: key, At: now, Base: BaseLive, BaseAt: prev.WrittenAt}
		if again[key] {
			data, err := w.Store.Shadow(ctx, key)
			if err != nil {
				return err
			}
			var before Shadow
			if data != nil && json.Unmarshal(data, &before) == nil {
				row.Skipped = before.Skipped
			}
		}
		if ref, ok := latest[prev.ID]; ok {
			data, err := w.Store.Shadow(ctx, ref.Key)
			if err != nil {
				return err
			}
			var before Shadow
			if data != nil && json.Unmarshal(data, &before) == nil && before.Story != nil {
				step.Prev, step.Full = *before.Story, before.Full
				step.Prev.ID = prev.ID
				row.Base, row.BaseAt = BaseShadow, before.At
			}
		}
		p := plan(step, settings, merged)
		row.Plan, row.Reason, row.Gone, row.Full = p.Kind, p.Reason, p.Gone, fullOf(step)
		for _, r := range p.New {
			row.New = append(row.New, r.URL)
		}
		switch p.Kind {
		case PlanLocal:
			story := local(step, p)
			row.Story = &story
		case PlanRebuild:
			// The live writing of these reports is the whole writing: the next step starts from the live version.
		default:
			steps, plans, rows = append(steps, step), append(plans, p), append(rows, row)
			continue
		}
		row.Skipped = 0 // no request: nothing of the answers that skipped it stands
		if err := w.saveShadow(ctx, row); err != nil {
			return err
		}
		recorded++
	}
	if recorded > 0 {
		w.logf("incremental shadow: %d stories recorded without a request (no new article, or to be written whole)", recorded)
	}
	if len(steps) == 0 {
		return nil
	}
	if ok, err := w.spare(ctx, day, now, settings); err != nil || !ok {
		return err
	}
	req, sent := w.build(ctx, steps, plans, 0, settings)
	raw, usage, err := w.generate(ctx, day, req.gemini(), 0)
	// Counted busy or not: the free tier's quota counts a busy answer (503) too.
	if err := w.countShadow(ctx, day); err != nil {
		return err
	}
	if errors.Is(err, gemini.ErrUnavailable) {
		// Busy: not tried again in this run. The live stories' tries come first.
		w.logf("incremental shadow: %v; %d stories wait", err, len(steps))
		return nil
	}
	if err != nil && !errors.Is(err, gemini.ErrCut) {
		return err
	}
	if err != nil {
		if err := w.countTokens(ctx, day, usage); err != nil {
			return err
		}
	}
	answers, whole := readAnswers(raw)
	versions, quiet, rebuilt, skipped := 0, 0, 0, 0
	var dropped MergeStats
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
		row := rows[position]
		m := merge(a, answer, id, req.ids, now)
		answer := answer
		row.Answer, row.Stats, row.Version = &answer, &m.Stats, m.Version
		row.Request = &ShadowRequest{Stories: sent, Position: position + 1, Chars: a.chars, Salvaged: !whole,
			TokensIn: usage.PromptTokens, TokensOut: usage.AnswerTokens, TokensThoughts: usage.ThoughtsTokens}
		if m.Skipped {
			// No answer for the story: its line stays where it was, and it is asked about again; or, asked often
			// enough, left to be written whole.
			row.Skipped++
			if row.Again = row.Skipped < shadowTries; row.Again {
				skipped++
			} else {
				row.Reason = whySkipped
				rebuilt++
			}
		} else if why := w.tripwire(ctx, a, m); why != "" {
			row.Skipped = 0
			row.Reason = why
			rebuilt++
		} else {
			row.Skipped = 0
			row.Story = &m.Story
			if m.Version {
				versions++
			} else {
				quiet++
			}
		}
		dropped.add(m.Stats)
		if err := w.saveShadow(ctx, row); err != nil {
			return err
		}
	}
	cutShort := ""
	if !whole {
		cutShort = " (the answer was cut: the whole stories of it)"
	}
	w.logf("incremental shadow: Gemini answered for %d of %d stories%s, %d more wait for a request; new versions: %d, nothing new: %d, to be written whole: %d, skipped by Gemini and asked again next run: %d; sentences dropped: %d without a new article of the story, %d said already, %d twice, %d on unknown sentences, %d garbled; %d kept from making a version by their articles' time",
		sent-len(req.asked), sent, cutShort, len(steps)-sent, versions, quiet, rebuilt, skipped, dropped.Unsourced, dropped.Restated, dropped.Repeated, dropped.Unknown, dropped.Garbled, dropped.Old)
	return nil
}

// liveNew are the sentences the live rewrite gave as new in next: its latest update when that is next's own, or,
// for a version from before updates, the sentences with nothing like them in prev.
func liveNew(prev, next StorySummary) []Sentence {
	if n := len(next.Updates); n > 0 {
		if n > 1 && next.Updates[n-1].At.Equal(next.WrittenAt) {
			return next.Updates[n-1].Sentences
		}
		return nil
	}
	var out []Sentence
	for i, c := range ChangesFrom(prev, next).Sentences {
		if c.Kind == New {
			out = append(out, next.Sentences[i])
		}
	}
	return out
}

// newFacts are how many sentences the live rewrite gave as new in next, and how many of them prev didn't already
// say in other words (restates): the rewrite's own measure of whether the step had news.
func newFacts(prev, next StorySummary) (listed, genuine int) {
	index := indexOf(prev.Sentences)
	for _, sentence := range liveNew(prev, next) {
		listed++
		if !index.restates(sentence.Text) {
			genuine++
		}
	}
	return listed, genuine
}

// ShadowReport is what the shadow recorded over a time, and how its steps compare with the live rewrites of the same
// stories.
type ShadowReport struct {
	Steps int // changed stories recorded
	// How they were made: without a request (no new article), sent to be written whole (by reason, the tripwire's
	// too), or asked.
	Local   int
	Rebuild map[string]int
	Asked   int
	// Of the asked: a new version, nothing new, or sent to be written whole after the answer (the tripwire).
	Versions, Quiet, Tripwire int
	// Skipped are the asked whose answer named none of their new articles and that wait to be asked again (or were
	// never asked again: the story changed first); GaveUp the ones left to be written whole after shadowTries such
	// answers. Neither is compared with the live rewrite.
	Skipped, GaveUp int
	// FromShadow are the asked steps that started from the shadow's own story, not a live version.
	FromShadow int
	Salvaged   int // read from an answer that was cut
	Stats      MergeStats
	// Requests and their tokens, about: each step carries its request's, shared among its stories.
	Requests                            float64
	TokensIn, TokensOut, TokensThoughts float64
	// The asked steps whose story the live rewrite wrote from the same reports, by what each made of it: Live* has
	// new facts by the live rewrite's own list, less what its version before already said.
	Compared                         int
	LiveNewVersion, LiveNewQuiet     int
	LiveQuietVersion, LiveQuietQuiet int
}

// ReportShadow reads the shadow rows written since since.
func ReportShadow(ctx context.Context, db *store.Store, since time.Time) (ShadowReport, error) {
	report := ShadowReport{Rebuild: map[string]int{}}
	var asked []Shadow
	var ids []string
	err := db.Shadows(ctx, since, func(_ store.ShadowRef, data json.RawMessage) error {
		var s Shadow
		if json.Unmarshal(data, &s) != nil {
			return nil
		}
		report.Steps++
		switch {
		case s.Plan == PlanLocal:
			report.Local++
		case s.Plan == PlanRebuild:
			report.Rebuild[s.Reason]++
		default:
			report.Asked++
			switch {
			case s.Again:
				report.Skipped++
			case s.Story == nil && s.Skipped > 0:
				report.GaveUp++
				report.Rebuild[whySkipped]++
			case s.Story == nil:
				report.Tripwire++
				report.Rebuild["Gemini found nothing new in an article that is clearly news (the tripwire)"]++
			case s.Version:
				report.Versions++
			default:
				report.Quiet++
			}
			if s.Base == BaseShadow {
				report.FromShadow++
			}
			if s.Stats != nil {
				report.Stats.add(*s.Stats)
			}
			if r := s.Request; r != nil && r.Stories > 0 {
				n := float64(r.Stories)
				report.Requests += 1 / n
				report.TokensIn += float64(r.TokensIn) / n
				report.TokensOut += float64(r.TokensOut) / n
				report.TokensThoughts += float64(r.TokensThoughts) / n
				if r.Salvaged {
					report.Salvaged++
				}
			}
			if s.Skipped == 0 {
				asked = append(asked, s)
				ids = append(ids, s.ID)
			}
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	versions, err := db.StoryVersions(ctx, ids)
	if err != nil {
		return report, err
	}
	for _, s := range asked {
		var before *StorySummary
		for _, v := range versions[s.ID] {
			var live StorySummary
			if json.Unmarshal(v.Summary, &live) != nil {
				continue
			}
			if before != nil && keyOf(live) == s.Key {
				_, genuine := newFacts(*before, live)
				report.Compared++
				switch {
				case genuine > 0 && s.Version:
					report.LiveNewVersion++
				case genuine > 0:
					report.LiveNewQuiet++
				case s.Version:
					report.LiveQuietVersion++
				default:
					report.LiveQuietQuiet++
				}
				break
			}
			before = &live
		}
	}
	return report, nil
}
