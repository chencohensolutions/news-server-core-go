package resolver

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/chencohensolutions/news-server-core-go/graph/model"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

const (
	// maxFollowUpdates is how many updates a followed story shows, the newest.
	maxFollowUpdates = 30
	// followSentences is how many sentences an update shows: a later version's new facts, a new story's start.
	followSentences, newStorySentences = 3, 2
)

// followUpdate is one update on a followed story, before it has its footnotes.
type followUpdate struct {
	At        time.Time
	Story     summary.StorySummary
	NewStory  bool
	Sentences []summary.Sentence
	Kinds     []string
}

// followUpdates are the updates on story followed, followed at since: what each of stories (the followed one and its
// threads' stories) added after since, newest first, at most maxFollowUpdates. A story's first version counts as an
// update only for a story other than the followed one: it joined a thread.
func followUpdates(followed string, since time.Time, stories []summary.StorySummary) []followUpdate {
	var out []followUpdate
	for _, s := range stories {
		updates := s.Updates
		if len(updates) == 0 {
			updates = []summary.Update{{At: s.WrittenAt, Sentences: s.Sentences}}
		}
		for i, u := range updates {
			if !u.At.After(since) || len(u.Sentences) == 0 {
				continue
			}
			first := i == 0
			if first && s.ID == followed {
				continue
			}
			limit := followSentences
			if first {
				limit = newStorySentences
			}
			n := min(limit, len(u.Sentences))
			var kinds []string
			if len(u.Kinds) == len(u.Sentences) {
				kinds = u.Kinds[:n]
			}
			out = append(out, followUpdate{At: u.At, Story: s, NewStory: first, Sentences: u.Sentences[:n], Kinds: kinds})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > maxFollowUpdates {
		out = out[:maxFollowUpdates]
	}
	return out
}

// followedStory is f for the API: its story's latest version, its threads, and the updates since it was followed.
// Nil when the server no longer has the story.
func (r *Resolver) followedStory(ctx context.Context, f store.Follow) (*model.FollowedStory, error) {
	rec, err := r.Store.StoryRecord(ctx, f.Story)
	if err != nil || rec == nil {
		return nil, err
	}
	var own summary.StorySummary
	if json.Unmarshal(rec.Summary, &own) != nil {
		return nil, nil
	}
	own.ID = rec.ID
	threads, err := r.Store.StoryThreads(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	stories := []summary.StorySummary{own}
	seen := map[string]bool{rec.ID: true}
	for _, t := range threads {
		records, err := r.Store.ThreadStories(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		for _, other := range records {
			if seen[other.ID] {
				continue
			}
			seen[other.ID] = true
			var s summary.StorySummary
			if json.Unmarshal(other.Summary, &s) != nil {
				continue
			}
			s.ID = other.ID
			stories = append(stories, s)
		}
	}
	updates := followUpdates(rec.ID, f.FollowedAt, stories)
	var urls []string
	for _, u := range updates {
		for _, s := range u.Sentences {
			urls = append(urls, s.Sources...)
		}
	}
	headlines, err := r.Store.HeadlinesByURL(ctx, urls)
	if err != nil {
		return nil, err
	}
	out := &model.FollowedStory{ID: rec.ID, Title: own.Title, FollowedAt: f.FollowedAt, Threads: model.ThreadsOf(threads),
		Updates: make([]*model.FollowUpdate, len(updates))}
	for i, u := range updates {
		kinds := u.Kinds
		if kinds == nil {
			kinds = []string{}
		}
		out.Updates[i] = &model.FollowUpdate{At: u.At, StoryID: u.Story.ID, StoryTitle: u.Story.Title, NewStory: u.NewStory,
			Sentences: model.SentencesOf(u.Sentences, headlines), Kinds: kinds}
	}
	return out, nil
}
