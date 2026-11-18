package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func unit(xs ...float32) []float32 {
	var n float32
	for _, x := range xs {
		n += x * x
	}
	out := make([]float32, len(xs))
	for i, x := range xs {
		out[i] = x / sqrt32(n)
	}
	return out
}

func sqrt32(x float32) float32 {
	r := x
	for range 30 {
		r = (r + x/r) / 2
	}
	return r
}

func TestNarrativeVectorsAreSearchedAndReplacedAndOutliveARestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "news.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	save := func(kind, ref string, at time.Time, v []float32) {
		t.Helper()
		if err := s.SaveNarrative(ctx, Narrative{Space: "m/narrative", Kind: kind, Ref: ref, At: at, Hash: ref + "h", Vector: v}); err != nil {
			t.Fatal(err)
		}
	}
	save(KindStory, "rain", day, unit(1, 0, 0, 0))
	save(KindStory, "storm", day.Add(24*time.Hour), unit(1, 0.2, 0, 0))
	save(KindStory, "budget", day.Add(48*time.Hour), unit(0, 0, 1, 0))
	save(KindThread, "weather", day, unit(1, 0.1, 0, 0))
	// Another model's space and another size stay apart.
	if err := s.SaveNarrative(ctx, Narrative{Space: "other", Kind: KindStory, Ref: "rain", At: day, Vector: unit(1, 0, 0, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNarrative(ctx, Narrative{Space: "m/narrative", Kind: KindStory, Ref: "small", At: day, Vector: unit(1, 0)}); err != nil {
		t.Fatal(err)
	}
	near, err := s.NearestNarratives(ctx, "m/narrative", KindStory, unit(1, 0.1, 0, 0), 2, time.Time{})
	if err != nil || len(near) != 2 || near[0].Ref != "rain" && near[0].Ref != "storm" || near[1].Ref == "budget" {
		t.Fatalf("near %+v, %v", near, err)
	}
	if near[0].Similarity < 0.98 || near[0].Similarity > 1.0001 {
		t.Fatalf("similarity %v", near[0].Similarity)
	}
	// Only the ones before a time.
	near, _ = s.NearestNarratives(ctx, "m/narrative", KindStory, unit(1, 0.2, 0, 0), 5, day.Add(time.Hour))
	if len(near) != 1 || near[0].Ref != "rain" || !near[0].At.Equal(day) {
		t.Fatalf("before %+v", near)
	}
	// A story whose text changed is replaced, not doubled.
	save(KindStory, "rain", day, unit(0, 0, 1, 0.1))
	near, _ = s.NearestNarratives(ctx, "m/narrative", KindStory, unit(0, 0, 1, 0), 5, time.Time{})
	if len(near) != 3 || near[0].Ref != "budget" && near[0].Ref != "rain" {
		t.Fatalf("after replacing %+v", near)
	}
	threads, _ := s.NearestNarratives(ctx, "m/narrative", KindThread, unit(0, 0, 1, 0), 5, time.Time{})
	if len(threads) != 1 || threads[0].Ref != "weather" {
		t.Fatalf("threads %+v", threads)
	}
	s.Close()
	// After a restart, and a vacuum, the vectors are there.
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Vacuum(ctx); err != nil {
		t.Fatal(err)
	}
	hashes, err := s.NarrativeHashes(ctx, "m/narrative", KindStory)
	if err != nil || len(hashes) != 4 || hashes["budget"] != "budgeth" {
		t.Fatalf("hashes %v, %v", hashes, err)
	}
	got, err := s.NarrativeVectors(ctx, "m/narrative", KindStory, []string{"storm", "small", "none"})
	if err != nil || len(got) != 2 || len(got["small"].Vector) != 2 || len(got["storm"].Vector) != 4 || !got["storm"].At.Equal(day.Add(24*time.Hour)) {
		t.Fatalf("vectors %v, %v", got, err)
	}
	var order []string
	if err := s.Narratives(ctx, "m/narrative", KindStory, func(n Narrative) error {
		if len(n.Vector) == 4 {
			order = append(order, n.Ref)
		}
		return nil
	}); err != nil || len(order) != 3 || order[0] != "rain" || order[2] != "budget" {
		t.Fatalf("all %v, %v", order, err)
	}
	near, err = s.NearestNarratives(ctx, "m/narrative", KindStory, unit(0, 0, 1, 0), 1, time.Time{})
	if err != nil || len(near) != 1 {
		t.Fatalf("after restart %+v, %v", near, err)
	}
}
