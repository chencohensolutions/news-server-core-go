package stories

import (
	"context"
	"fmt"
	"slices"
)

// SimilarityGraph is the pairs of headlines whose vectors are close (similarity at least Floor), kept up to date one
// headline at a time, so a refresh doesn't compare every pair again (the app's SimilarityGraph, in memory only: the
// server runs for days, and after a restart one pass over a few thousand headlines takes a moment).
//
// Why it is enough: average-link clustering only merges two groups whose mean similarity is above the threshold,
// which needs at least one pair above it. So no story spans two Components of the pairs at or above the threshold,
// and clustering each component alone gives the same stories as clustering every headline together, for any
// threshold of at least Floor. Not safe for concurrent use.
type SimilarityGraph struct {
	Floor      float32
	Similarity Similarity
	// LastAdded is how many keys Update or Add compared with the rest in its last call.
	LastAdded int

	ids     map[string]int
	keyOf   []string
	present []bool
	rows    []map[int]float32
	freeIDs []int
}

// NewSimilarityGraph is an empty graph; similarity nil means Dot.
func NewSimilarityGraph(floor float32, similarity Similarity) *SimilarityGraph {
	if similarity == nil {
		similarity = Dot
	}
	return &SimilarityGraph{Floor: floor, Similarity: similarity, ids: map[string]int{}}
}

// Len is how many keys the graph holds.
func (g *SimilarityGraph) Len() int { return len(g.ids) }

// ForEachClose calls action with each of key's close keys and their similarity.
func (g *SimilarityGraph) ForEachClose(key string, action func(other string, similarity float32)) {
	id, ok := g.ids[key]
	if !ok {
		return
	}
	for other, s := range g.rows[id] {
		action(g.keyOf[other], s)
	}
}

func (g *SimilarityGraph) newID(key string) int {
	var id int
	if len(g.freeIDs) > 0 {
		id, g.freeIDs = g.freeIDs[0], g.freeIDs[1:]
		g.keyOf[id], g.present[id], g.rows[id] = key, true, map[int]float32{}
	} else {
		id = len(g.keyOf)
		g.keyOf = append(g.keyOf, key)
		g.present = append(g.present, true)
		g.rows = append(g.rows, map[int]float32{})
	}
	g.ids[key] = id
	return id
}

func (g *SimilarityGraph) remove(key string) {
	id, ok := g.ids[key]
	if !ok {
		return
	}
	delete(g.ids, key)
	for other := range g.rows[id] {
		delete(g.rows[other], id)
	}
	g.keyOf[id], g.present[id], g.rows[id] = "", false, nil
	g.freeIDs = append(g.freeIDs, id)
}

// Update makes the graph hold exactly vectors' keys: drops the others with their pairs, and compares each new key
// with every key present. It returns whether anything changed.
func (g *SimilarityGraph) Update(ctx context.Context, vectors map[string][]float32) (bool, error) {
	var gone []string
	for key := range g.ids {
		if _, ok := vectors[key]; !ok {
			gone = append(gone, key)
		}
	}
	for _, key := range gone {
		g.remove(key)
	}
	keys := make([]string, 0, len(vectors))
	for key := range vectors {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	added, err := g.Add(ctx, keys, vectors)
	return len(gone) > 0 || added > 0, err
}

// Add compares each of keys not in the graph yet (whose vector vectors has) with every key present and adds it,
// dropping none. It returns how many it added. Stopped by ctx, the keys added so far are whole.
func (g *SimilarityGraph) Add(ctx context.Context, keys []string, vectors map[string][]float32) (int, error) {
	var added []string
	for _, key := range keys {
		if _, in := g.ids[key]; in {
			continue
		}
		if _, ok := vectors[key]; ok {
			added = append(added, key)
		}
	}
	for i, key := range added {
		if err := ctx.Err(); err != nil {
			g.LastAdded = i
			return i, fmt.Errorf("stopped after %d of %d: %w", i, len(added), err)
		}
		vector := vectors[key]
		id := g.newID(key)
		for otherID, other := range g.keyOf {
			if otherID == id || !g.present[otherID] {
				continue
			}
			otherVector, ok := vectors[other]
			if !ok {
				continue
			}
			if s := g.Similarity(vector, otherVector); s >= g.Floor {
				g.rows[id][otherID] = s
				g.rows[otherID][id] = s
			}
		}
	}
	g.LastAdded = len(added)
	return len(added), nil
}

// Components is the connected pieces of the pairs at or above threshold among keys, those of two keys or more, each
// in the order of keys, in the order of their first key. threshold must be at least Floor.
func (g *SimilarityGraph) Components(keys []string, threshold float32) ([][]string, error) {
	if threshold < g.Floor {
		return nil, fmt.Errorf("threshold %v is below the graph's floor %v", threshold, g.Floor)
	}
	position := make(map[string]int, len(keys))
	for i, key := range keys {
		position[key] = i
	}
	seen := map[string]bool{}
	var out [][]string
	for _, start := range keys {
		if seen[start] {
			continue
		}
		seen[start] = true
		members := []string{}
		queue := []string{start}
		for len(queue) > 0 {
			key := queue[0]
			queue = queue[1:]
			members = append(members, key)
			g.ForEachClose(key, func(other string, s float32) {
				if _, in := position[other]; s >= threshold && in && !seen[other] {
					seen[other] = true
					queue = append(queue, other)
				}
			})
		}
		if len(members) > 1 {
			slices.SortFunc(members, func(a, b string) int { return position[a] - position[b] })
			out = append(out, members)
		}
	}
	return out, nil
}

type componentID struct {
	keys      string // the component's keys, joined with a NUL
	threshold float32
}

// ComponentClusterer finds stories one component of a SimilarityGraph at a time, with ClusterClosePairs on each:
// the same groups as clustering all keys together. It remembers each component's groups, so a later call
// reclusters only the components that changed (a headline joined or left, or the threshold moved).
type ComponentClusterer struct {
	previous map[componentID][][]string
	// LastReclustered is how many components the last Groups call clustered again rather than reused.
	LastReclustered int
}

// Groups is the groups of two keys or more among keys, each in the order of keys.
func (c *ComponentClusterer) Groups(ctx context.Context, keys []string, vectors map[string][]float32, graph *SimilarityGraph, threshold float32, chain bool) ([][]string, error) {
	named := func(groups [][]int, names []string) [][]string {
		var out [][]string
		for _, group := range groups {
			if len(group) < 2 {
				continue
			}
			g := make([]string, len(group))
			for i, at := range group {
				g[i] = names[at]
			}
			out = append(out, g)
		}
		return out
	}
	if threshold < graph.Floor {
		// Pairs below the floor aren't in the graph: cluster everything.
		c.previous, c.LastReclustered = nil, 1
		vs := make([][]float32, len(keys))
		for i, key := range keys {
			vs[i] = vectors[key]
		}
		return named(ClusterBySimilarity(vs, threshold, chain, graph.Similarity), keys), nil
	}
	components, err := graph.Components(keys, threshold)
	if err != nil {
		return nil, err
	}
	next := map[componentID][][]string{}
	reclustered := 0
	var out [][]string
	for _, component := range components {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("stopped while clustering: %w", err)
		}
		id := componentID{joinKeys(component), threshold}
		groups, ok := c.previous[id]
		if !ok {
			reclustered++
			position := make(map[string]int, len(component))
			for i, key := range component {
				position[key] = i
			}
			close := make([]map[int]float32, len(component))
			vs := make([][]float32, len(component))
			for i, key := range component {
				close[i] = map[int]float32{}
				graph.ForEachClose(key, func(other string, s float32) {
					if at, in := position[other]; s >= threshold && in {
						close[i][at] = s
					}
				})
				vs[i] = vectors[key]
			}
			groups = named(ClusterClosePairs(vs, close, threshold, chain, graph.Similarity), component)
		}
		next[id] = groups
		out = append(out, groups...)
	}
	c.previous, c.LastReclustered = next, reclustered
	return out, nil
}

func joinKeys(keys []string) string {
	n := 0
	for _, k := range keys {
		n += len(k) + 1
	}
	b := make([]byte, 0, n)
	for _, k := range keys {
		b = append(b, k...)
		b = append(b, 0)
	}
	return string(b)
}
