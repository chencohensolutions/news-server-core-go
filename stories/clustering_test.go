package stories

import (
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"testing"
)

// The app's StoryClusteringTest.

func unitOf(v ...float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x * x)
	}
	n := float32(math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

func TestGroupsCloseVectorsAndKeepsInputOrder(t *testing.T) {
	vs := [][]float32{unitOf(1, 0, 0), unitOf(0, 1, 0), unitOf(1, 0.05, 0), unitOf(0, 1, 0.05), unitOf(0, 0, 1)}
	want := [][]int{{0, 2}, {1, 3}, {4}}
	if got := ClusterBySimilarity(vs, 0.9, true, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAverageLinkStopsAChainFromGrowing(t *testing.T) {
	// a~b and b~c are close, a and c are not: average link keeps c out of {a, b}.
	vs := [][]float32{unitOf(1, 0), unitOf(1, 0.33), unitOf(1, 0.75)}
	want := [][]int{{0, 1}, {2}}
	if got := ClusterBySimilarity(vs, 0.9, true, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// randomFeed is groups of noisy vectors around a few random centres, as the app's tests make.
func randomFeed(r *rand.Rand) [][]float32 {
	const dims = 16
	centres := make([][]float32, 1+r.IntN(8))
	for i := range centres {
		centres[i] = make([]float32, dims)
		for d := range centres[i] {
			centres[i][d] = r.Float32()*2 - 1
		}
	}
	vs := make([][]float32, 20+r.IntN(120))
	for i := range vs {
		c := centres[r.IntN(len(centres))]
		spread := r.Float32() * 0.9
		v := make([]float32, dims)
		for d := range v {
			v[d] = c[d] + (r.Float32()*2-1)*spread
		}
		vs[i] = unitOf(v...)
	}
	return vs
}

var thresholds = []float32{0.3, 0.5, 0.7, 0.85, 0.95}

func TestTheChainGivesTheSameGroupsAsMergingTheBestPairEachTime(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 5))
	for round := range 30 {
		vs := randomFeed(r)
		for _, threshold := range thresholds {
			plain := ClusterBySimilarity(vs, threshold, false, nil)
			if got := ClusterBySimilarity(vs, threshold, true, nil); !reflect.DeepEqual(got, plain) {
				t.Fatalf("round %d, threshold %v: chain %v, plain %v", round, threshold, got, plain)
			}
		}
	}
}

func closePairs(vs [][]float32, threshold float32, similarity Similarity) []map[int]float32 {
	close := make([]map[int]float32, len(vs))
	for a := range vs {
		close[a] = map[int]float32{}
		for b := range vs {
			if s := similarity(vs[a], vs[b]); b != a && s >= threshold {
				close[a][b] = s
			}
		}
	}
	return close
}

func TestKeepingOnlyTheClosePairsGivesTheSameGroupsAsTheTableOfEveryPair(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 9))
	for round := range 30 {
		vs := randomFeed(r)
		for _, threshold := range thresholds {
			close := closePairs(vs, threshold, Dot)
			table := ClusterBySimilarity(vs, threshold, true, nil)
			for _, chain := range []bool{true, false} {
				if got := ClusterClosePairs(vs, close, threshold, chain, nil); !reflect.DeepEqual(got, table) {
					t.Fatal(fmt.Sprintf("chain %v, round %d, threshold %v: %v, want %v", chain, round, threshold, got, table))
				}
			}
		}
	}
}
