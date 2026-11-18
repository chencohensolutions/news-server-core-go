package narrative

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"time"
)

// The eval measures the shortlist on the threads Gemini already linked, seeing the full list of threads: for a
// story in a thread, would the shortlist the linking now sends have listed one of its threads? Each story is taken
// as it was when it was new: only the stories before it, and the threads as they were made of them.

// EvalStory is one kept story: its first time, vector and threads.
type EvalStory struct {
	ID      string
	At      time.Time
	Vector  []float32
	Threads []string
}

// EvalResult is one way of shortlisting, over the stories tested.
type EvalResult struct {
	Name   string
	Tested int
	Hits   int
	// Listed is the mean number of threads listed.
	Listed float64
}

func (r EvalResult) Recall() float64 {
	if r.Tested == 0 {
		return 0
	}
	return float64(r.Hits) / float64(r.Tested)
}

// Evaluate tests the latest n stories (oldest first in all) that continue a thread: a thread of theirs had a story
// before them. k is the neighbors each way, recent the latest-updated threads listed too, halfLives the thread
// vectors' weightings to compare (0: every story the same).
func Evaluate(all []EvalStory, n, k, recent int, halfLives []time.Duration) []EvalResult {
	sorted := slices.Clone(all)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	byThread := map[string][]int{}
	for i, s := range sorted {
		for _, t := range s.Threads {
			byThread[t] = append(byThread[t], i)
		}
	}
	// The stories to test: the latest n whose thread had an earlier story.
	var tests []int
	for i := len(sorted) - 1; i >= 0 && len(tests) < n; i-- {
		s := sorted[i]
		if len(s.Vector) == 0 {
			continue
		}
		for _, t := range s.Threads {
			if first := byThread[t][0]; sorted[first].At.Before(s.At) {
				tests = append(tests, i)
				break
			}
		}
	}
	type way struct {
		name     string
		halfLife time.Duration
		threads  bool // nearest threads by their vectors
		stories  bool // threads of the nearest earlier stories
		recent   bool
	}
	var ways []way
	for _, h := range halfLives {
		ways = append(ways, way{name: fmt.Sprintf("threads (half-life %s)", days(h)), halfLife: h, threads: true})
	}
	h := HalfLife
	ways = append(ways,
		way{name: "earlier stories' threads", stories: true},
		way{name: fmt.Sprintf("threads (%s) + stories' threads", days(h)), halfLife: h, threads: true, stories: true},
		way{name: fmt.Sprintf("threads (%s) + stories' threads + %d latest (the linking's)", days(h), recent), halfLife: h, threads: true, stories: true, recent: true},
	)
	results := make([]EvalResult, len(ways))
	for w := range ways {
		results[w].Name = ways[w].name
	}
	for _, i := range tests {
		s := sorted[i]
		// The threads as they were: their stories before s.
		before := map[string][]Member{}
		updated := map[string]time.Time{}
		for t, idx := range byThread {
			for _, j := range idx {
				o := sorted[j]
				if !o.At.Before(s.At) || len(o.Vector) == 0 {
					continue
				}
				before[t] = append(before[t], Member{At: o.At, Vector: o.Vector})
				if o.At.After(updated[t]) {
					updated[t] = o.At
				}
			}
		}
		want := map[string]bool{}
		for _, t := range s.Threads {
			if len(before[t]) > 0 {
				want[t] = true
			}
		}
		// The nearest earlier stories.
		type scored struct {
			i   int
			sim float32
		}
		var near []scored
		for j := range i {
			if len(sorted[j].Vector) > 0 && sorted[j].At.Before(s.At) {
				near = append(near, scored{j, Dot(s.Vector, sorted[j].Vector)})
			}
		}
		sort.Slice(near, func(a, b int) bool { return near[a].sim > near[b].sim })
		near = near[:min(len(near), k)]
		var latest []string
		for t := range before {
			latest = append(latest, t)
		}
		sort.Slice(latest, func(a, b int) bool { return updated[latest[a]].After(updated[latest[b]]) })
		latest = latest[:min(len(latest), recent)]
		nearThreads := map[time.Duration][]string{}
		for w, way := range ways {
			listed := map[string]bool{}
			if way.threads {
				ids, ok := nearThreads[way.halfLife]
				if !ok {
					type th struct {
						id  string
						sim float32
					}
					var list []th
					for t, members := range before {
						if v := ThreadVector(members, updated[t], way.halfLife); v != nil {
							list = append(list, th{t, Dot(s.Vector, v)})
						}
					}
					sort.Slice(list, func(a, b int) bool { return list[a].sim > list[b].sim })
					for _, x := range list[:min(len(list), k)] {
						ids = append(ids, x.id)
					}
					nearThreads[way.halfLife] = ids
				}
				for _, id := range ids {
					listed[id] = true
				}
			}
			if way.stories {
				for _, x := range near {
					for _, t := range sorted[x.i].Threads {
						if len(before[t]) > 0 {
							listed[t] = true
						}
					}
				}
			}
			if way.recent {
				for _, t := range latest {
					listed[t] = true
				}
			}
			hit := false
			for t := range want {
				hit = hit || listed[t]
			}
			results[w].Tested++
			if hit {
				results[w].Hits++
			}
			results[w].Listed += float64(len(listed))
		}
	}
	for w := range results {
		if results[w].Tested > 0 {
			results[w].Listed /= float64(results[w].Tested)
		}
	}
	return results
}

func days(d time.Duration) string {
	if d <= 0 {
		return "none"
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// PrintEval writes results as a table.
func PrintEval(out io.Writer, results []EvalResult) {
	for _, r := range results {
		fmt.Fprintf(out, "%-70s %5.1f%% of %d (%d), %.0f threads listed\n", r.Name, 100*r.Recall(), r.Tested, r.Hits, r.Listed)
	}
}
