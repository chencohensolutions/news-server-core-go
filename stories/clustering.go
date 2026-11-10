package stories

import (
	"math"
	"slices"
)

// Similarity is how alike two vectors are.
type Similarity func(a, b []float32) float32

// Dot is the dot product: the cosine similarity of unit vectors. The explicit float32 conversions keep Go from
// fusing the multiply and the add, so the sums are the app's to the last bit on every CPU.
func Dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += float32(a[i] * b[i])
	}
	return s
}

// ClusterBySimilarity is average-link agglomerative clustering over unit vectors (the app's clusterBySimilarity):
// the groups you get by repeatedly merging the two groups whose mean pairwise similarity is highest, until no pair
// is above threshold. Found with the nearest-neighbour chain (chain), which merges the same pairs in O(n²), or by
// rescanning all pairs after each merge (O(n³)). It keeps a table of every pair; ClusterClosePairs doesn't.
// It returns groups of indexes into vectors, each in input order, groups in the order of their first member.
func ClusterBySimilarity(vectors [][]float32, threshold float32, chain bool, similarity Similarity) [][]int {
	if similarity == nil {
		similarity = Dot
	}
	if chain {
		return chainAverageLink(vectors, threshold, similarity)
	}
	return plainAverageLink(vectors, threshold, similarity)
}

func pairTable(vectors [][]float32, similarity Similarity) [][]float32 {
	n := len(vectors)
	sim := make([][]float32, n)
	for a := range sim {
		sim[a] = make([]float32, n)
		for b := range sim[a] {
			if a != b {
				sim[a][b] = similarity(vectors[a], vectors[b])
			}
		}
	}
	return sim
}

func singletons(n int) [][]int {
	members := make([][]int, n)
	for i := range members {
		members[i] = []int{i}
	}
	return members
}

func groupsOf(members [][]int, alive func(int) bool) [][]int {
	var out [][]int
	for i, m := range members {
		if alive(i) {
			g := slices.Clone(m)
			slices.Sort(g)
			out = append(out, g)
		}
	}
	slices.SortStableFunc(out, func(a, b []int) int { return a[0] - b[0] })
	return out
}

func mergedSimilarity(sizeK, simK, sizeG, simG float32) float32 {
	return float32(float32(sizeK*simK)+float32(sizeG*simG)) / float32(sizeK+sizeG)
}

func chainAverageLink(vectors [][]float32, threshold float32, similarity Similarity) [][]int {
	n := len(vectors)
	if n == 0 {
		return nil
	}
	// sim[a][b] is the average similarity between groups a and b (Lance-Williams update on merge).
	sim := pairTable(vectors, similarity)
	members := singletons(n)
	// Groups that may still merge; a group whose best neighbour is not above the threshold leaves for good: merging
	// other groups can only average its similarities down (average link is reducible).
	open := make([]bool, n)
	for i := range open {
		open[i] = true
	}
	openCount := n
	var chain []int
	for openCount > 1 {
		if len(chain) == 0 {
			chain = append(chain, slices.Index(open, true))
		}
		a := chain[len(chain)-1]
		b := -1
		best := float32(math.Inf(-1))
		for c := 0; c < n; c++ {
			if !open[c] || c == a {
				continue
			}
			// On a tie, the group before a in the chain wins, so the chain always ends.
			if sim[a][c] > best || (sim[a][c] == best && len(chain) > 1 && c == chain[len(chain)-2]) {
				best, b = sim[a][c], c
			}
		}
		if best <= threshold {
			open[a] = false
			openCount--
			chain = chain[:len(chain)-1]
			continue
		}
		if len(chain) > 1 && b == chain[len(chain)-2] {
			chain = chain[:len(chain)-2]
			keep, gone := min(a, b), max(a, b)
			sizeK, sizeG := float32(len(members[keep])), float32(len(members[gone]))
			for c := 0; c < n; c++ {
				if !open[c] || c == keep || c == gone {
					continue
				}
				merged := mergedSimilarity(sizeK, sim[keep][c], sizeG, sim[gone][c])
				sim[keep][c], sim[c][keep] = merged, merged
			}
			members[keep] = append(members[keep], members[gone]...)
			members[gone] = nil
			open[gone] = false
			openCount--
		} else {
			chain = append(chain, b)
		}
	}
	return groupsOf(members, func(i int) bool { return len(members[i]) > 0 })
}

func plainAverageLink(vectors [][]float32, threshold float32, similarity Similarity) [][]int {
	n := len(vectors)
	if n == 0 {
		return nil
	}
	sim := pairTable(vectors, similarity)
	members := singletons(n)
	alive := make([]bool, n)
	for i := range alive {
		alive[i] = true
	}
	for {
		best, bestA, bestB := threshold, -1, -1
		for a := 0; a < n; a++ {
			if !alive[a] {
				continue
			}
			for b := a + 1; b < n; b++ {
				if alive[b] && sim[a][b] > best {
					best, bestA, bestB = sim[a][b], a, b
				}
			}
		}
		if bestA < 0 {
			break
		}
		sizeA, sizeB := float32(len(members[bestA])), float32(len(members[bestB]))
		for c := 0; c < n; c++ {
			if !alive[c] || c == bestA || c == bestB {
				continue
			}
			merged := mergedSimilarity(sizeA, sim[bestA][c], sizeB, sim[bestB][c])
			sim[bestA][c], sim[c][bestA] = merged, merged
		}
		members[bestA] = append(members[bestA], members[bestB]...)
		alive[bestB] = false
	}
	return groupsOf(members, func(i int) bool { return alive[i] })
}

// ClusterClosePairs is ClusterBySimilarity's groups without its table of every pair (the app's clusterClosePairs).
// Two groups can merge only if some pair between them is at or above threshold, so only such groups' similarities
// are kept, starting from close (each index's indexes at or above threshold, with their similarity). A merged
// group's similarity to a group only one of its halves had such a pair with is the mean over their members,
// computed when the merge needs it. Memory grows with the close pairs, not the square of the headlines.
func ClusterClosePairs(vectors [][]float32, close []map[int]float32, threshold float32, chain bool, similarity Similarity) [][]int {
	if similarity == nil {
		similarity = Dot
	}
	n := len(vectors)
	if n == 0 {
		return nil
	}
	sim := make([]map[int]float32, n)
	for a := range sim {
		sim[a] = map[int]float32{}
		for b, s := range close[a] {
			if b != a {
				sim[a][b] = s
			}
		}
	}
	members := singletons(n)
	open := make([]bool, n)
	for i := range open {
		open[i] = true
	}
	mean := func(a, b int) float32 {
		var sum float64
		for _, x := range members[a] {
			for _, y := range members[b] {
				sum += float64(similarity(vectors[x], vectors[y]))
			}
		}
		return float32(sum / float64(len(members[a])*len(members[b])))
	}
	merge := func(keep, gone int) {
		sizeK, sizeG := float32(len(members[keep])), float32(len(members[gone]))
		others := map[int]bool{}
		for c := range sim[keep] {
			others[c] = true
		}
		for c := range sim[gone] {
			others[c] = true
		}
		delete(others, keep)
		delete(others, gone)
		for c := range others {
			delete(sim[c], gone)
			if !open[c] {
				delete(sim[keep], c)
				delete(sim[c], keep)
				continue
			}
			k, ok := sim[keep][c]
			if !ok {
				k = mean(keep, c)
			}
			g, ok := sim[gone][c]
			if !ok {
				g = mean(gone, c)
			}
			merged := mergedSimilarity(sizeK, k, sizeG, g)
			sim[keep][c], sim[c][keep] = merged, merged
		}
		delete(sim[keep], gone)
		sim[gone] = map[int]float32{}
		members[keep] = append(members[keep], members[gone]...)
		members[gone] = nil
		open[gone] = false
	}
	if chain {
		openCount, first := n, 0
		var list []int
		for openCount > 1 {
			if len(list) == 0 {
				for !open[first] {
					first++
				}
				list = append(list, first)
			}
			a := list[len(list)-1]
			before := -1
			if len(list) > 1 {
				before = list[len(list)-2]
			}
			// As ClusterBySimilarity: the first group with the highest similarity, or the one before a in the chain.
			b := -1
			best := float32(math.Inf(-1))
			for c, s := range sim[a] {
				if !open[c] {
					continue
				}
				if s > best || (s == best && (c == before || (b != before && c < b))) {
					best, b = s, c
				}
			}
			if best <= threshold {
				open[a] = false
				openCount--
				list = list[:len(list)-1]
				continue
			}
			if b == before {
				list = list[:len(list)-2]
				merge(min(a, b), max(a, b))
				openCount--
			} else {
				list = append(list, b)
			}
		}
	} else {
		for {
			best, bestA, bestB := threshold, -1, -1
			for a := 0; a < n; a++ {
				if !open[a] {
					continue
				}
				for b, s := range sim[a] {
					if b <= a || !open[b] {
						continue
					}
					if s > best || (s == best && bestA == a && b < bestB) {
						best, bestA, bestB = s, a, b
					}
				}
			}
			if bestA < 0 {
				break
			}
			merge(bestA, bestB)
		}
	}
	return groupsOf(members, func(i int) bool { return len(members[i]) > 0 })
}
