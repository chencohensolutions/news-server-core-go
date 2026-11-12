package resolver

import (
	"context"
	"encoding/json"

	"github.com/chencohensolutions/news-server-core-go/graph/model"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

// storyVersions are the kept versions of the stories ids, oldest first, by id.
func (r *Resolver) storyVersions(ctx context.Context, ids []string) (map[string][]summary.StorySummary, error) {
	kept, err := r.Store.StoryVersions(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]summary.StorySummary, len(kept))
	for id, versions := range kept {
		for _, v := range versions {
			var s summary.StorySummary
			if json.Unmarshal(v.Summary, &s) != nil {
				continue
			}
			s.ID, s.WrittenAt = id, v.WrittenAt
			out[id] = append(out[id], s)
		}
	}
	return out, nil
}

// versioned is s for the API with its place among versions (the story's, oldest first) and what it changed from
// the version before it.
func versioned(s summary.StorySummary, versions []summary.StorySummary, current bool, headlines map[string]store.Headline) *model.StorySummary {
	out := model.StorySummaryOf(s, headlines)
	out.Current = current
	var before *summary.StorySummary
	for i := range versions {
		if versions[i].WrittenAt.UnixMilli() < s.WrittenAt.UnixMilli() {
			out.Version++
			before = &versions[i]
		}
	}
	out.Version++ // itself, kept or not
	out.Versions = max(len(versions), out.Version)
	if before != nil {
		out.Changes = model.StoryChangesOf(summary.ChangesFrom(*before, s), headlines)
	}
	return out
}
