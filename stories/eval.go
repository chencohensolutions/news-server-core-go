package stories

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/chencohensolutions/news-server-core-go/scrape"
)

// The app's story model test set (the news repo's tools/story-model-eval; story_eval_dir): 737 headlines of one day and 348 cross-site pairs of
// them labeled by hand "same event" or not. Its measurements are in the news repo's docs/story-model-comparison.md,
// so the server's embedder can be checked against the app's numbers on the same pairs.

// LabeledPair is two headlines of the test set (indexes into its items) and whether they report one event.
type LabeledPair struct {
	I, J int
	Same bool
}

// LoadEvalSet reads items.json, pairs.json and labels-*.json from dir.
func LoadEvalSet(dir string) ([]scrape.ArticleSummary, []LabeledPair, error) {
	var items []scrape.ArticleSummary
	if err := readJSON(filepath.Join(dir, "items.json"), &items); err != nil {
		return nil, nil, err
	}
	var pairs []struct{ ID, I, J int }
	if err := readJSON(filepath.Join(dir, "pairs.json"), &pairs); err != nil {
		return nil, nil, err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "labels-*.json"))
	slices.Sort(files)
	labels := map[int]bool{}
	for _, f := range files {
		var m map[string]int
		if err := readJSON(f, &m); err != nil {
			return nil, nil, err
		}
		for k, v := range m {
			id, err := strconv.Atoi(k)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %q is not a pair id", f, k)
			}
			labels[id] = v == 1
		}
	}
	var out []LabeledPair
	for _, p := range pairs {
		same, ok := labels[p.ID]
		if !ok {
			continue
		}
		if p.I < 0 || p.J < 0 || p.I >= len(items) || p.J >= len(items) {
			return nil, nil, fmt.Errorf("pair %d: headline out of range", p.ID)
		}
		out = append(out, LabeledPair{p.I, p.J, same})
	}
	return items, out, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// PairScore is how well a similarity tells the labeled pairs apart, as the test set's score.py measures it.
type PairScore struct {
	Pairs, Same int
	// AUC is the chance a same-event pair scores above a pair that isn't (ties count half).
	AUC float64
	// F1 is the best over thresholds, at Threshold (pairs at or above it called the same event).
	F1, Threshold, Precision, Recall float64
}

// ScorePairs scores similarities[k], the similarity of pairs[k].
func ScorePairs(pairs []LabeledPair, similarities []float32) PairScore {
	score := PairScore{Pairs: len(pairs)}
	var pos, neg []float32
	for k, p := range pairs {
		if p.Same {
			pos = append(pos, similarities[k])
		} else {
			neg = append(neg, similarities[k])
		}
	}
	score.Same = len(pos)
	if len(pos) == 0 || len(neg) == 0 {
		return score
	}
	var above float64
	for _, a := range pos {
		for _, b := range neg {
			switch {
			case a > b:
				above++
			case a == b:
				above += 0.5
			}
		}
	}
	score.AUC = above / float64(len(pos)*len(neg))
	thresholds := slices.Clone(similarities)
	slices.Sort(thresholds)
	thresholds = slices.Compact(thresholds)
	for _, t := range thresholds {
		var called, hit int
		for k, p := range pairs {
			if similarities[k] >= t {
				called++
				if p.Same {
					hit++
				}
			}
		}
		precision := float64(hit) / float64(max(called, 1))
		recall := float64(hit) / float64(len(pos))
		if precision+recall == 0 {
			continue
		}
		if f1 := 2 * precision * recall / (precision + recall); f1 > score.F1 {
			score.F1, score.Threshold, score.Precision, score.Recall = f1, float64(t), precision, recall
		}
	}
	return score
}
