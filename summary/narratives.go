package summary

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/chencohensolutions/news-server-core-go/narrative"
	"github.com/chencohensolutions/news-server-core-go/store"
)

const (
	// DefaultThreadCandidates is how many of the nearest threads, and of the nearest earlier stories, each new story
	// brings into its linking request.
	DefaultThreadCandidates = 10
	// recentThreads are always listed too, latest updated first: the affairs going on now.
	recentThreads = 20
	// olderPerStory is the most earlier stories in no thread one new story brings, and olderFloor how close they
	// must be (bge-m3's cosine: same-event headlines are from ~0.66).
	olderPerStory = 3
	olderFloor    = 0.6
)

// narrativeStory is what a kept story's vector is made from.
func narrativeStory(r store.StoryRecord) (narrative.Story, bool) {
	var s StorySummary
	if err := json.Unmarshal(r.Summary, &s); err != nil || s.Title == "" {
		return narrative.Story{}, false
	}
	n := narrative.Story{ID: r.ID, At: r.FirstWritten, Title: s.Title}
	for _, sentence := range s.Sentences {
		n.Sentences = append(n.Sentences, sentence.Text)
	}
	return n, true
}

// catchUpNarratives embeds the kept stories that have no vector, or changed since theirs, and makes the vectors of
// the threads whose stories changed: all of them the first time (the backfill).
func (w *Writer) catchUpNarratives(ctx context.Context) error {
	if w.Narratives == nil {
		return nil
	}
	var all []narrative.Story
	if err := w.Store.StoryRecords(ctx, func(r store.StoryRecord) error {
		if s, ok := narrativeStory(r); ok {
			all = append(all, s)
		}
		return nil
	}); err != nil {
		return err
	}
	embedded, err := w.Narratives.EmbedStories(ctx, all)
	if err != nil {
		return err
	}
	threads, err := w.Store.Threads(ctx, 1<<30)
	if err != nil {
		return err
	}
	members, err := w.Store.ThreadMembers(ctx)
	if err != nil {
		return err
	}
	made, err := w.Narratives.UpdateThreads(ctx, threads, members)
	if err != nil {
		return err
	}
	if embedded > 0 || made > 0 {
		w.logf("narratives: %d stories embedded, %d thread vectors made", embedded, made)
	}
	return nil
}

// CatchUpNarratives is catchUpNarratives, for the narratives command.
func (w *Writer) CatchUpNarratives(ctx context.Context) error { return w.catchUpNarratives(ctx) }

// candidates are what a linking request lists for waiting: the threads near each new story (by the threads'
// vectors, and the threads of the earlier stories near it), the latest updated ones, and the earlier stories near
// a new one that are in no thread, so a story that comes back after weeks starts a thread with its first chapter.
// all says there are no vectors to choose by: every thread is listed, as before.
func (w *Writer) candidates(ctx context.Context, waiting []store.StoryRecord) (threads []store.Thread, older []store.StoryRecord, all bool, err error) {
	k := w.ThreadCandidates
	if k == 0 {
		k = DefaultThreadCandidates
	}
	if w.Narratives == nil || k < 0 {
		return nil, nil, true, nil
	}
	if err := w.catchUpNarratives(ctx); err != nil {
		return nil, nil, false, err
	}
	isWaiting := map[string]bool{}
	for _, r := range waiting {
		isWaiting[r.ID] = true
	}
	chosen := map[string]bool{}
	var threadIDs []string
	addThread := func(id string) {
		if !chosen[id] {
			chosen[id] = true
			threadIDs = append(threadIDs, id)
		}
	}
	olderChosen := map[string]bool{}
	var olderIDs []string
	vectors := 0
	for _, r := range waiting {
		v, err := w.Narratives.Vector(ctx, store.KindStory, r.ID)
		if err != nil {
			return nil, nil, false, err
		}
		if v == nil {
			continue
		}
		vectors++
		near, err := w.Narratives.Nearest(ctx, store.KindThread, v, k, time.Time{})
		if err != nil {
			return nil, nil, false, err
		}
		for _, n := range near {
			addThread(n.Ref)
		}
		stories, err := w.Narratives.Nearest(ctx, store.KindStory, v, k+len(waiting), r.FirstWritten.Add(1))
		if err != nil {
			return nil, nil, false, err
		}
		taken, alone := 0, 0
		for _, n := range stories {
			if isWaiting[n.Ref] || n.Ref == r.ID {
				continue
			}
			if taken++; taken > k {
				break
			}
			in, err := w.Store.StoryThreads(ctx, n.Ref)
			if err != nil {
				return nil, nil, false, err
			}
			for _, t := range in {
				addThread(t.ID)
			}
			if len(in) == 0 && n.Similarity >= olderFloor && alone < olderPerStory {
				alone++
				if !olderChosen[n.Ref] {
					olderChosen[n.Ref] = true
					olderIDs = append(olderIDs, n.Ref)
				}
			}
		}
	}
	if vectors == 0 {
		return nil, nil, true, nil
	}
	recent, err := w.Store.Threads(ctx, recentThreads)
	if err != nil {
		return nil, nil, false, err
	}
	for _, t := range recent {
		addThread(t.ID)
	}
	for _, id := range threadIDs {
		t, err := w.Store.ThreadByID(ctx, id)
		if err != nil {
			return nil, nil, false, err
		}
		if t != nil {
			threads = append(threads, *t)
		}
	}
	// Latest updated first, as the whole list was.
	slices.SortStableFunc(threads, func(a, b store.Thread) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	records, err := w.Store.StoryRecordsByID(ctx, olderIDs)
	if err != nil {
		return nil, nil, false, err
	}
	for _, id := range olderIDs {
		if r, ok := records[id]; ok {
			older = append(older, r)
		}
	}
	slices.SortStableFunc(older, func(a, b store.StoryRecord) int { return a.FirstWritten.Compare(b.FirstWritten) })
	return threads, older, false, nil
}
