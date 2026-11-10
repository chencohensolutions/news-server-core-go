package stories

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestScorePairsAsTheAppsScript(t *testing.T) {
	pairs := []LabeledPair{{Same: true}, {Same: true}, {Same: false}, {Same: false}, {Same: false}}
	s := ScorePairs(pairs, []float32{0.9, 0.6, 0.7, 0.6, 0.1})
	// Positives 0.9 and 0.6 against 0.7, 0.6, 0.1: 3 + (1 + 0.5) of 6.
	if math.Abs(s.AUC-4.5/6) > 1e-9 {
		t.Errorf("AUC %v", s.AUC)
	}
	// At 0.6: 2 of 4, recall 1, F1 2/3; at 0.9 precision 1, recall 0.5, F1 2/3 too, not higher: the lower stays,
	// as score.py, which tries the thresholds from the lowest.
	if math.Abs(s.F1-2.0/3) > 1e-9 || s.Threshold != float64(float32(0.6)) || s.Precision != 0.5 || s.Recall != 1 {
		t.Errorf("%+v", s)
	}
}

// The app's labeled test set is in the news submodule.
func TestTheAppsTestSetLoads(t *testing.T) {
	dir := filepath.Join("..", "news", "tools", "story-model-eval")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no news submodule")
	}
	items, pairs, err := LoadEvalSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	same := 0
	for _, p := range pairs {
		if p.Same {
			same++
		}
	}
	// docs/story-model-comparison.md: 737 headlines, 348 pairs, 83 the same event.
	if len(items) != 737 || len(pairs) != 348 || same != 83 {
		t.Fatalf("%d headlines, %d pairs, %d same", len(items), len(pairs), same)
	}
}
