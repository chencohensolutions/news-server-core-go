package summary

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// The incremental update, on (Settings.Incremental "on"): a changed story that has a version is no longer written
// again whole. The plan (incremental.go) sends it one of three ways:
//
//   - no new article: the server keeps the text, and saves the story under its new reports without a version;
//   - an incremental request, eight stories in one: a version when the answer makes one, saved as the whole writing
//     saves its own (summaries, story_records, story_versions); otherwise the version it is, changed in place: its
//     new reports and footnotes, and the sentences that went in quietly;
//   - written whole, by the request it always was (writeStories): when the plan says so, when the tripwire does
//     after an answer, and when Gemini skipped the story twice.
//
// First versions are written whole as ever. The requests are the run's own, under its budget and its pacing: they
// take the place of the whole rewrites, so the shadow's share of the day (ShadowDaily) is not theirs.
//
// Every step is kept in story_shadow too, marked Applied, as the shadow keeps its own: `news-server incremental`
// reports them, and a story Gemini skipped, or one to be written whole, is known by its row in the next run.

// pending is a changed story on its way through the incremental update: its step, the plan and its row.
type pending struct {
	step Step
	plan Plan
	row  Shadow
}

// applied is the row the mode kept of the story key when it was on, or nil. A row of the shadow's is not the story
// readers have, and says nothing of it.
func (w *Writer) applied(ctx context.Context, key string) (*Shadow, error) {
	data, err := w.Store.Shadow(ctx, key)
	if err != nil || data == nil {
		return nil, err
	}
	var row Shadow
	if json.Unmarshal(data, &row) != nil || !row.Applied {
		return nil, nil
	}
	return &row, nil
}

// route plans the next version of story, which has the version prev. A story without a new article is kept by the
// server at once, with no request, and route returns nil. Otherwise it returns the story with its plan:
// PlanIncremental for a request, PlanRebuild to be written whole, which is also what the run before left a story
// at (the tripwire, or skipped twice).
func (w *Writer) route(ctx context.Context, story stories.Story, prev *StorySummary, written map[string]StorySummary, firstSeen map[string]time.Time, settings Settings, now time.Time) (*pending, error) {
	key := StoryKey(story)
	step := Step{Story: story, Prev: *prev, Full: prev.Full, Seen: firstSeen}
	p := plan(step, settings, mergedInto(story, prev.ID, written))
	row := Shadow{ID: prev.ID, Key: key, At: now, Applied: true, Plan: p.Kind, Reason: p.Reason, Base: BaseLive, BaseAt: prev.WrittenAt,
		Gone: p.Gone, Full: fullOf(step)}
	for _, r := range p.New {
		row.New = append(row.New, r.URL)
	}
	if p.Kind == PlanLocal {
		kept := local(step, p)
		if err := w.amend(ctx, step, &kept, now); err != nil {
			return nil, err
		}
		row.Story = &kept
		return nil, w.saveShadow(ctx, row)
	}
	before, err := w.applied(ctx, key)
	if err != nil {
		return nil, err
	}
	switch {
	case p.Kind == PlanRebuild:
		// Recorded once: the story may wait some runs for its request.
		if before == nil || before.Plan != PlanRebuild || before.Reason != p.Reason {
			err = w.saveShadow(ctx, row)
		}
	case before != nil && before.Again:
		// Gemini skipped it: asked again, and the count of its skipped answers goes on.
		row.Skipped, row.Again = before.Skipped, true
	case before != nil && before.Story == nil:
		// An answer sent it to be written whole (the tripwire, or skipped twice), and it waits for that request.
		p.Kind, p.Reason = PlanRebuild, before.Reason
		row = *before
	}
	return &pending{step: step, plan: p, row: row}, err
}

// amend saves story as step's story without a new version: under its reports' key, so that Run finds it written
// and the app has it; and in place of the version it is, in story_versions and, when that version is the story's
// latest, in story_records. The version's time stays, so no version is added and the story keeps its place among
// its threads' stories.
func (w *Writer) amend(ctx context.Context, step Step, story *StorySummary, now time.Time) error {
	story.UpdatedAt, story.Full = now, fullOf(step)
	data, err := json.Marshal(story)
	if err != nil {
		return err
	}
	if err := w.Store.SaveSummary(ctx, StoryKey(step.Story), data, now); err != nil {
		return err
	}
	if err := w.Store.SaveStoryVersion(ctx, story.ID, data, story.WrittenAt); err != nil {
		return err
	}
	// A story that split off may stand on an earlier version than its story's latest: the record keeps the latest.
	record, err := w.Store.StoryRecord(ctx, story.ID)
	if err != nil || record != nil && record.WrittenAt.UnixMilli() > story.WrittenAt.UnixMilli() {
		return err
	}
	return w.Store.SaveStoryRecord(ctx, story.ID, data, story.WrittenAt)
}

// publish saves story as step's story's new version, written at now, as writeStories saves a whole writing.
func (w *Writer) publish(ctx context.Context, step Step, story *StorySummary, now time.Time) error {
	story.Full = fullOf(step)
	data, err := json.Marshal(story)
	if err != nil {
		return err
	}
	if err := w.Store.SaveSummary(ctx, StoryKey(step.Story), data, now); err != nil {
		return err
	}
	if err := w.Store.SaveStoryRecord(ctx, story.ID, data, now); err != nil {
		return err
	}
	return w.Store.SaveStoryVersion(ctx, story.ID, data, now)
}

// updated is what one incremental request did.
type updated struct {
	asked    int // the first stories of the queue, which the request took
	versions int // new versions written
	settled  int // stories saved without a version: nothing new, or sentences that went in quietly
	// whole are the asked stories to be written whole in this run or the next: the tripwire's, and the ones Gemini
	// skipped twice.
	whole []stories.Story
}

// updateStories sends one incremental request with the first of updates that fit it, and applies what comes back
// to the stories readers have. A story the answer left out, or whose part of a cut answer isn't whole, waits for
// the next run, and so does one Gemini skipped (its answer names none of its new articles): that one is asked about
// first then, and after shadowTries such answers it is written whole.
func (w *Writer) updateStories(ctx context.Context, updates []pending, day string, now time.Time, settings Settings) (out updated, err error) {
	steps, plans := make([]Step, len(updates)), make([]Plan, len(updates))
	for i, u := range updates {
		steps[i], plans[i] = u.step, u.plan
	}
	req, sent := w.build(ctx, steps, plans, 0, settings)
	out.asked = sent
	raw, usage, err := w.generate(ctx, day, req.gemini(), 0)
	if err != nil && !errors.Is(err, gemini.ErrCut) {
		return out, err
	}
	if err != nil {
		// A cut answer's whole stories stand, and its tokens were spent.
		if err := w.countTokens(ctx, day, usage); err != nil {
			return out, err
		}
	}
	answers, whole := readAnswers(raw)
	quietly, skipped := 0, 0
	var stats MergeStats
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
		row := updates[position].row
		m := merge(a, answer, id, req.ids, now)
		answer := answer
		row.At, row.Answer, row.Stats, row.Version, row.Again = now, &answer, &m.Stats, false, false
		row.Request = &ShadowRequest{Stories: sent, Position: position + 1, Chars: a.chars, Salvaged: !whole,
			TokensIn: usage.PromptTokens, TokensOut: usage.AnswerTokens, TokensThoughts: usage.ThoughtsTokens}
		stats.add(m.Stats)
		if m.Skipped {
			// No answer for the story: it stays as it is, and is asked about again; or, asked often enough, written
			// whole.
			row.Skipped++
			if row.Again = row.Skipped < shadowTries; row.Again {
				skipped++
			} else {
				row.Reason = whySkipped
				out.whole = append(out.whole, a.step.Story)
			}
		} else if why, _ := w.tripwire(ctx, a, m); why != "" {
			row.Skipped, row.Reason = 0, why
			out.whole = append(out.whole, a.step.Story)
		} else {
			story := m.Story
			// A version is what was written now with what's new; anything else changes the version before in place.
			if m.Version && story.WrittenAt.Equal(now) {
				if err := w.publish(ctx, a.step, &story, now); err != nil {
					return out, err
				}
				row.Version = true
				out.versions++
			} else {
				if err := w.amend(ctx, a.step, &story, now); err != nil {
					return out, err
				}
				if m.Stats.Quiet > 0 {
					quietly++
				}
				out.settled++
			}
			// The grouping moves an article of another event to a closer story, or out, from the next run on.
			for _, url := range m.Unrelated {
				if err := w.Store.SaveUnrelated(ctx, url, story.Reports, now); err != nil {
					return out, err
				}
			}
			row.Skipped, row.Story, row.Quiet = 0, &story, m.Stats.Quiet
		}
		if err := w.saveShadow(ctx, row); err != nil {
			return out, err
		}
	}
	cutShort := ""
	if !whole {
		cutShort = " (the answer was cut: the whole stories of it)"
	}
	w.logf("summaries: incremental: Gemini answered for %d of %d stories%s; new versions: %d (%d of them made by details alone), updated quietly (no version): %d, nothing new: %d, to be written whole: %d, skipped by Gemini and asked again next run: %d; sentences dropped: %d without a new article of the story, %d said already, %d twice, %d on unknown sentences, %d garbled, %d with a number their articles don't state; %d inserted ones made the correction of the sentence they gave another number for; no version from %d developments and corrections for their articles' time; %d sentences went into stories quietly, %d stayed out with no version made",
		sent-len(req.asked), sent, cutShort, out.versions, stats.ByDetail, quietly, out.settled-quietly, len(out.whole), skipped, stats.Unsourced, stats.Restated, stats.Repeated, stats.Unknown, stats.Garbled, stats.Unbacked, stats.Corrected, stats.Old, stats.Quiet, stats.Unapplied)
	return out, nil
}

// queue orders the stories that wait for an incremental request: the ones Gemini skipped first, to be asked again,
// then the most reported, as the whole writings go.
func queue(updates []pending) {
	sort.SliceStable(updates, func(i, j int) bool {
		if a, b := updates[i].row.Again, updates[j].row.Again; a != b {
			return a
		}
		return len(updates[i].step.Story.Reports) > len(updates[j].step.Story.Reports)
	})
}
