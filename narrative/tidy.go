package narrative

import (
	"sort"
	"time"

	"github.com/chencohensolutions/news-server-core-go/stories"
)

// The weekly tidy-up finds, by the vectors alone, what may be wrong with the threads: two threads that are one
// affair (merge), one thread that is two (split), and a story far from its thread (stray). Gemini decides on each.

const (
	// MergeSimilarity is how close two threads' vectors must be to be offered for a merge; MergeShared the share
	// of the smaller thread's stories also in the other that offers it too (at MergeSharedSimilarity at least).
	MergeSimilarity       = 0.85
	MergeShared           = 1.0 / 3
	MergeSharedSimilarity = 0.6
	// SplitSimilarity is the average-link similarity a thread's stories are grouped at; a split needs two groups
	// of SplitGroup stories or more, and a thread SplitStories.
	SplitSimilarity = 0.55
	SplitGroup      = 3
	SplitStories    = 6
	// StraySimilarity is how far from its thread's vector a story is a stray, in a thread of StrayThread stories.
	StraySimilarity = 0.45
	StrayThread     = 4

	maxMerges = 8
	maxSplits = 4
	maxStrays = 12
)

// TidyStory is one of a thread's stories.
type TidyStory struct {
	ID     string
	At     time.Time
	Vector []float32
}

// TidyThread is a thread with its vector and stories.
type TidyThread struct {
	ID      string
	Vector  []float32
	Stories []TidyStory
}

// MergeCandidate is two threads that may be one.
type MergeCandidate struct {
	A, B       string
	Similarity float32
	Shared     int
}

// SplitCandidate is a thread whose stories fall in groups (story ids, each oldest first; the biggest first).
type SplitCandidate struct {
	Thread string
	Groups [][]string
}

// StrayCandidate is a story far from its thread.
type StrayCandidate struct {
	Thread, Story string
	Similarity    float32
}

// TidyCandidates are the tidy-up's questions, the likeliest first.
type TidyCandidates struct {
	Merges []MergeCandidate
	Splits []SplitCandidate
	Strays []StrayCandidate
}

func (c TidyCandidates) Empty() bool { return len(c.Merges)+len(c.Splits)+len(c.Strays) == 0 }

// FindTidyCandidates finds what may be wrong with threads.
func FindTidyCandidates(threads []TidyThread) TidyCandidates {
	var c TidyCandidates
	members := make([]map[string]bool, len(threads))
	for i, t := range threads {
		members[i] = map[string]bool{}
		for _, s := range t.Stories {
			members[i][s.ID] = true
		}
	}
	for i := range threads {
		for j := i + 1; j < len(threads); j++ {
			a, b := threads[i], threads[j]
			if len(a.Vector) == 0 || len(a.Vector) != len(b.Vector) {
				continue
			}
			sim := Dot(a.Vector, b.Vector)
			shared := 0
			for id := range members[i] {
				if members[j][id] {
					shared++
				}
			}
			smaller := min(len(a.Stories), len(b.Stories))
			if sim >= MergeSimilarity || smaller > 0 && float64(shared) >= MergeShared*float64(smaller) && sim >= MergeSharedSimilarity {
				c.Merges = append(c.Merges, MergeCandidate{A: a.ID, B: b.ID, Similarity: sim, Shared: shared})
			}
		}
	}
	sort.SliceStable(c.Merges, func(i, j int) bool { return c.Merges[i].Similarity > c.Merges[j].Similarity })
	c.Merges = c.Merges[:min(len(c.Merges), maxMerges)]

	for _, t := range threads {
		var list []TidyStory
		for _, s := range t.Stories {
			if len(s.Vector) > 0 {
				list = append(list, s)
			}
		}
		if len(list) >= SplitStories {
			if groups := splitGroups(list); len(groups) > 1 {
				c.Splits = append(c.Splits, SplitCandidate{Thread: t.ID, Groups: groups})
			}
		}
		if len(list) >= StrayThread && len(t.Vector) > 0 {
			for _, s := range list {
				if sim := Dot(s.Vector, t.Vector); sim < StraySimilarity {
					c.Strays = append(c.Strays, StrayCandidate{Thread: t.ID, Story: s.ID, Similarity: sim})
				}
			}
		}
	}
	// The most balanced splits first.
	sort.SliceStable(c.Splits, func(i, j int) bool {
		return len(c.Splits[i].Groups[1]) > len(c.Splits[j].Groups[1])
	})
	c.Splits = c.Splits[:min(len(c.Splits), maxSplits)]
	sort.SliceStable(c.Strays, func(i, j int) bool { return c.Strays[i].Similarity < c.Strays[j].Similarity })
	c.Strays = c.Strays[:min(len(c.Strays), maxStrays)]
	return c
}

// splitGroups are list's stories in groups of SplitGroup or more, the small groups' stories with the group they are
// closest to; nil when there aren't two.
func splitGroups(list []TidyStory) [][]string {
	vectors := make([][]float32, len(list))
	for i, s := range list {
		vectors[i] = s.Vector
	}
	var big, small [][]int
	for _, g := range stories.ClusterBySimilarity(vectors, SplitSimilarity, true, nil) {
		if len(g) >= SplitGroup {
			big = append(big, g)
		} else {
			small = append(small, g)
		}
	}
	if len(big) < 2 {
		return nil
	}
	for _, g := range small {
		for _, i := range g {
			best, bestSim := 0, float32(-2)
			for b, members := range big {
				var sum float32
				for _, j := range members {
					sum += Dot(vectors[i], vectors[j])
				}
				if mean := sum / float32(len(members)); mean > bestSim {
					best, bestSim = b, mean
				}
			}
			big[best] = append(big[best], i)
		}
	}
	sort.SliceStable(big, func(a, b int) bool { return len(big[a]) > len(big[b]) })
	out := make([][]string, len(big))
	for g, members := range big {
		sort.Slice(members, func(a, b int) bool { return list[members[a]].At.Before(list[members[b]].At) })
		for _, i := range members {
			out[g] = append(out[g], list[i].ID)
		}
	}
	return out
}
