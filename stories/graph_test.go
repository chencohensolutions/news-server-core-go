package stories

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The app's SimilarityGraphTest (without the file: the server keeps the graph in memory).

// graphFeed is stories groups of near-identical unit vectors around random centres, plus noise loose vectors,
// shuffled.
func graphFeed(seed uint64, stories, perStory, noise, dims int) map[string][]float32 {
	r := rand.New(rand.NewPCG(seed, seed))
	randomVector := func() []float32 {
		v := make([]float32, dims)
		for i := range v {
			v[i] = r.Float32()*2 - 1
		}
		return unitOf(v...)
	}
	var vectors [][]float32
	for range stories {
		centre := randomVector()
		for range 1 + r.IntN(perStory) {
			// Spread so some members fall just above, some just below the thresholds.
			spread := 0.15 + r.Float32()*0.35
			v := make([]float32, dims)
			for i := range v {
				v[i] = centre[i] + (r.Float32()*2-1)*spread
			}
			vectors = append(vectors, unitOf(v...))
		}
	}
	for range noise {
		vectors = append(vectors, randomVector())
	}
	r.Shuffle(len(vectors), func(i, j int) { vectors[i], vectors[j] = vectors[j], vectors[i] })
	out := make(map[string][]float32, len(vectors))
	for i, v := range vectors {
		out[fmt.Sprintf("h%d", i)] = v
	}
	return out
}

func feedKeys(vectors map[string][]float32) []string {
	keys := make([]string, 0, len(vectors))
	for k := range vectors {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, cmpNatural)
	return keys
}

func cmpNatural(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

// asSet is groups as a sorted list of joined groups, to compare without order.
func asSet(groups [][]string) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = strings.Join(g, ",")
	}
	slices.Sort(out)
	return out
}

func everything(keys []string, vectors map[string][]float32, threshold float32, similarity Similarity) []string {
	vs := make([][]float32, len(keys))
	for i, k := range keys {
		vs[i] = vectors[k]
	}
	var out [][]string
	for _, g := range ClusterBySimilarity(vs, threshold, true, similarity) {
		if len(g) > 1 {
			names := make([]string, len(g))
			for i, at := range g {
				names[i] = keys[at]
			}
			out = append(out, names)
		}
	}
	return asSet(out)
}

func mustGroups(t *testing.T, c *ComponentClusterer, keys []string, vectors map[string][]float32, g *SimilarityGraph, threshold float32) []string {
	t.Helper()
	groups, err := c.Groups(context.Background(), keys, vectors, g, threshold, true)
	if err != nil {
		t.Fatal(err)
	}
	return asSet(groups)
}

func builtGraph(t *testing.T, floor float32, similarity Similarity, vectors map[string][]float32) *SimilarityGraph {
	t.Helper()
	g := NewSimilarityGraph(floor, similarity)
	if _, err := g.Update(context.Background(), vectors); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestComponentsGiveTheSameStoriesAsClusteringEverything(t *testing.T) {
	for seed := uint64(1); seed <= 5; seed++ {
		vectors := graphFeed(seed, 40, 4, 200, 48)
		keys := feedKeys(vectors)
		graph := builtGraph(t, 0.5, nil, vectors)
		for _, threshold := range []float32{0.5, 0.6, 0.7, 0.8, 0.9} {
			got := mustGroups(t, &ComponentClusterer{}, keys, vectors, graph, threshold)
			if want := everything(keys, vectors, threshold, Dot); !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %d, threshold %v: %v, want %v", seed, threshold, got, want)
			}
			if threshold == 0.5 && len(got) == 0 {
				t.Fatalf("seed %d, threshold %v: no stories to compare", seed, threshold)
			}
		}
	}
}

func TestComponentsGiveTheSameStoriesWithTheProductOfTwoSimilarities(t *testing.T) {
	text := TitleTimesSubtitle
	for seed := uint64(1); seed <= 3; seed++ {
		titles := graphFeed(seed, 40, 4, 200, 48)
		subtitles := graphFeed(seed+100, 40, 4, 200, 48)
		subtitleKeys := feedKeys(subtitles)
		keys := feedKeys(titles)
		vectors := map[string][]float32{}
		for i, k := range keys {
			vectors[k] = text.Vector([][]float32{titles[k], subtitles[subtitleKeys[i%len(subtitleKeys)]]})
		}
		graph := builtGraph(t, 0.2, text.Similarity(), vectors)
		for _, threshold := range []float32{0.2, 0.3, 0.4, 0.5} {
			got := mustGroups(t, &ComponentClusterer{}, keys, vectors, graph, threshold)
			if want := everything(keys, vectors, threshold, text.Similarity()); !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %d, threshold %v: %v, want %v", seed, threshold, got, want)
			}
		}
	}
}

func TestAddingHeadlinesAsTheyAreEmbeddedGivesTheSameStoriesAsOnePass(t *testing.T) {
	ctx := context.Background()
	vectors := graphFeed(7, 40, 4, 200, 48)
	keys := feedKeys(vectors)
	onePass := builtGraph(t, 0.5, nil, vectors)
	// Bit by bit, each piece compared with what is there, then the feed made whole.
	piecewise := NewSimilarityGraph(0.5, nil)
	for piece := range slices.Chunk(keys, 37) {
		if _, err := piecewise.Add(ctx, piece, vectors); err != nil {
			t.Fatal(err)
		}
	}
	if changed, _ := piecewise.Update(ctx, vectors); changed {
		t.Fatal("nothing left to compare")
	}
	for _, threshold := range []float32{0.6, 0.75} {
		a := mustGroups(t, &ComponentClusterer{}, keys, vectors, onePass, threshold)
		b := mustGroups(t, &ComponentClusterer{}, keys, vectors, piecewise, threshold)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("threshold %v: %v, piecewise %v", threshold, a, b)
		}
	}
}

func TestAddingAndRemovingHeadlinesMatchesAFreshGraph(t *testing.T) {
	ctx := context.Background()
	all := graphFeed(11, 40, 4, 200, 48)
	keys := feedKeys(all)
	graph := NewSimilarityGraph(0.5, nil)
	clusterer := &ComponentClusterer{}
	of := func(present []string) map[string][]float32 {
		m := map[string][]float32{}
		for _, k := range present {
			m[k] = all[k]
		}
		return m
	}
	// Arrive in batches, as refreshes do, with some headlines leaving the feed along the way.
	present := slices.Clone(keys[:100])
	graph.Update(ctx, of(present))
	mustGroups(t, clusterer, present, all, graph, 0.7)
	for batch := range slices.Chunk(keys[100:], 60) {
		present = append(slices.Clone(present[15:]), batch...)
		graph.Update(ctx, of(present))
		if graph.LastAdded != len(batch) {
			t.Fatalf("added %d, want %d", graph.LastAdded, len(batch))
		}
		if got, want := mustGroups(t, clusterer, present, all, graph, 0.7), everything(present, all, 0.7, Dot); !reflect.DeepEqual(got, want) {
			t.Fatalf("%v, want %v", got, want)
		}
	}
	if graph.Len() != len(present) {
		t.Fatalf("graph holds %d, want %d", graph.Len(), len(present))
	}
	// Nothing new: every component is reused.
	graph.Update(ctx, of(present))
	mustGroups(t, clusterer, present, all, graph, 0.7)
	if graph.LastAdded != 0 || clusterer.LastReclustered != 0 {
		t.Fatalf("added %d, reclustered %d", graph.LastAdded, clusterer.LastReclustered)
	}
}

func TestAStoppedUpdateKeepsWhatItCompared(t *testing.T) {
	vectors := graphFeed(3, 10, 3, 20, 16)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	graph := NewSimilarityGraph(0.5, nil)
	if _, err := graph.Update(ctx, vectors); err == nil {
		t.Fatal("a cancelled update returns its error")
	}
	if graph.Len() != 0 {
		t.Fatalf("holds %d", graph.Len())
	}
	if changed, err := graph.Update(context.Background(), vectors); !changed || err != nil || graph.Len() != len(vectors) {
		t.Fatalf("changed %v, err %v, holds %d", changed, err, graph.Len())
	}
}

func TestTimingForAFeedOfAppSize(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	ctx := context.Background()
	vectors := graphFeed(21, 600, 5, 1800, 384)
	keys := feedKeys(vectors)
	newOne := keys[len(keys)-1]
	without := map[string][]float32{}
	for k, v := range vectors {
		if k != newOne {
			without[k] = v
		}
	}
	graph := NewSimilarityGraph(0.85, nil)
	clusterer := &ComponentClusterer{}
	start := time.Now()
	graph.Update(ctx, without)
	clusterer.Groups(ctx, keys[:len(keys)-1], vectors, graph, 0.88, true)
	build := time.Since(start)
	start = time.Now()
	graph.Update(ctx, vectors)
	clusterer.Groups(ctx, keys, vectors, graph, 0.88, true)
	oneMore := time.Since(start)
	t.Logf("%d headlines: graph from scratch %v, one new headline %v", len(keys), build, oneMore)
}
