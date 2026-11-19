package summary

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/narrative"
	"github.com/chencohensolutions/news-server-core-go/store"
)

// meaningEmbedder puts every text about rain (גשם) in one direction and every other text in one of 60 others.
type meaningEmbedder struct{}

func (meaningEmbedder) Embed(text string) ([]float32, error) {
	v := make([]float32, 64)
	if strings.Contains(text, "גשם") {
		v[0] = 1
		return v, nil
	}
	h := fnv.New32a()
	h.Write([]byte(text))
	v[4+h.Sum32()%60] = 1
	return v, nil
}

func TestALinkingListsOnlyTheThreadsNearTheNewStoriesAndAnEarlierChapter(t *testing.T) {
	w, fake, _ := setup(t)
	ctx := context.Background()
	w.Narratives = narrative.New(w.Store, meaningEmbedder{}, "m")
	w.ThreadsEvery = 24 * time.Hour
	record := func(id, title string, at time.Time, threads ...string) {
		t.Helper()
		data, _ := json.Marshal(StorySummary{ID: id, Title: title, Sentences: []Sentence{{Text: "משפט על " + title + ".", Sources: nil}}})
		if err := w.Store.SaveStoryRecord(ctx, id, data, at); err != nil {
			t.Fatal(err)
		}
		for _, th := range threads {
			if err := w.Store.SaveThread(ctx, store.Thread{ID: th, Name: "חוט " + title, Summary: "עד כה: " + title, StartedAt: at, UpdatedAt: at}); err != nil {
				t.Fatal(err)
			}
		}
		if threads != nil {
			if err := w.Store.LinkStory(ctx, id, threads); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 60 affairs of the last weeks, none about rain.
	for i := range 60 {
		record(fmt.Sprintf("s%d", i), fmt.Sprintf("פרשה מספר %d", i), now.Add(-time.Duration(10*24+i)*time.Hour), fmt.Sprintf("th%d", i))
	}
	// The rain thread, quiet for 100 days: not among the latest, found by meaning.
	record("rain-old", "גשם בדרום", now.Add(-100*24*time.Hour), "weather")
	// A rain story 60 days ago in no thread.
	record("storm-old", "גשם וסערה", now.Add(-60*24*time.Hour))
	if err := w.Store.LinkStory(ctx, "storm-old", nil); err != nil {
		t.Fatal(err)
	}
	record("rain-new", "שוב גשם כבד", now)
	fake.threads = mustJSON(map[string]any{
		"stories": []any{
			map[string]any{"story": "n1", "threads": []string{"גשם החורף"}},
			// The earlier chapter joins the new thread, not a thread no new story is in.
			map[string]any{"story": "o1", "threads": []string{"גשם החורף", "th5"}},
		},
		"newThreads": []any{map[string]any{"name": "גשם החורף", "summary": "גשם כבד שב."}},
		"summaries":  []any{},
		"related":    []any{},
	})
	if err := w.linkThreadsIfDue(ctx, Day(now), now); err != nil {
		t.Fatal(err)
	}
	prompt := fake.prompts[len(fake.prompts)-1]
	listed := strings.Count(prompt, "\n[t")
	if !strings.Contains(prompt, "הקרובים לסיפורים החדשים") || !strings.Contains(prompt, "חוט גשם בדרום") || listed >= 61 || listed < 20 {
		t.Fatalf("%d threads listed:\n%s", listed, prompt)
	}
	if !strings.Contains(prompt, "[o1]") || !strings.Contains(prompt, "גשם וסערה") || strings.Contains(prompt, "[o2]") {
		t.Fatalf("earlier stories:\n%s", prompt)
	}
	for _, id := range []string{"rain-new", "storm-old"} {
		ths, err := w.Store.StoryThreads(ctx, id)
		if err != nil || len(ths) != 1 || ths[0].Name != "גשם החורף" {
			t.Fatalf("%s in %+v, %v", id, ths, err)
		}
		// The thread starts with its first chapter.
		if !ths[0].StartedAt.Equal(now.Add(-60*24*time.Hour)) || !ths[0].UpdatedAt.Equal(now) {
			t.Fatalf("thread span %+v", ths[0])
		}
	}
	// The new thread has its vector, near rain.
	v, _ := w.Narratives.Vector(ctx, store.KindStory, "rain-new")
	if err := w.catchUpNarratives(ctx); err != nil {
		t.Fatal(err)
	}
	near, err := w.Narratives.Nearest(ctx, store.KindThread, v, 2, time.Time{})
	if err != nil || len(near) != 2 || near[0].Similarity < 0.99 || near[1].Similarity < 0.99 {
		t.Fatalf("near %+v, %v", near, err)
	}
}

func TestWithoutCandidatesEveryThreadIsListed(t *testing.T) {
	w, fake, _ := setup(t)
	ctx := context.Background()
	w.Narratives = narrative.New(w.Store, meaningEmbedder{}, "m")
	w.ThreadCandidates = -1
	w.ThreadsEvery = 24 * time.Hour
	data, _ := json.Marshal(StorySummary{ID: "x", Title: "גשם"})
	w.Store.SaveStoryRecord(ctx, "x", data, now)
	if err := w.linkThreadsIfDue(ctx, Day(now), now); err != nil {
		t.Fatal(err)
	}
	if p := fake.prompts[len(fake.prompts)-1]; strings.Contains(p, "הקרובים") {
		t.Fatalf("shortlisted:\n%s", p)
	}
}

func TestTheWeeklyTidyUpMergesThreadsAndTakesOutStrays(t *testing.T) {
	w, fake, _ := setup(t)
	ctx := context.Background()
	w.Narratives = narrative.New(w.Store, meaningEmbedder{}, "m")
	record := func(id, title string, at time.Time, thread string) {
		t.Helper()
		data, _ := json.Marshal(StorySummary{ID: id, Title: title})
		if err := w.Store.SaveStoryRecord(ctx, id, data, at); err != nil {
			t.Fatal(err)
		}
		if th, _ := w.Store.ThreadByID(ctx, thread); th == nil {
			if err := w.Store.SaveThread(ctx, store.Thread{ID: thread, Name: "חוט " + thread, Summary: "עד כה", StartedAt: at, UpdatedAt: at}); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Store.LinkStory(ctx, id, []string{thread}); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	// Two rain threads, one of them bigger; and a rain story in the budget thread.
	record("r1", "גשם בצפון", now.Add(-30*day), "rain")
	record("r2", "גשם בדרום", now.Add(-20*day), "rain")
	record("r3", "שוב גשם", now.Add(-2*day), "rain2")
	// They share a story: half of the smaller one's.
	if err := w.Store.LinkStory(ctx, "r2", []string{"rain", "rain2"}); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		record(fmt.Sprintf("b%d", i), "התקציב", now.Add(-time.Duration(10+i)*day), "budget")
	}
	record("stray", "גשם בירושלים", now.Add(-day), "budget")
	fake.tidy = mustJSON(map[string]any{
		"merges": []any{map[string]any{"candidate": "m1", "same": true, "name": "גשמי החורף", "summary": "גשם ירד בכל הארץ."}},
		"splits": []any{},
		"strays": []any{map[string]any{"candidate": "x1", "belongs": false}},
	})
	if err := w.tidyIfDue(ctx, Day(now), now); err != nil {
		t.Fatal(err)
	}
	prompt := fake.prompts[len(fake.prompts)-1]
	if !strings.Contains(prompt, "{m1}") || !strings.Contains(prompt, "{x1}") || !strings.Contains(prompt, "גשם בירושלים") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	// The bigger thread keeps its id and takes the other's stories; the other's id finds it.
	merged, err := w.Store.ThreadByID(ctx, "rain2")
	if err != nil || merged == nil || merged.ID != "rain" || merged.Name != "גשמי החורף" || merged.Stories != 3 ||
		!merged.StartedAt.Equal(now.Add(-30*day)) || !merged.UpdatedAt.Equal(now.Add(-2*day)) {
		t.Fatalf("merged %+v, %v", merged, err)
	}
	if all, _ := w.Store.Threads(ctx, 10); len(all) != 2 {
		t.Fatalf("threads %+v", all)
	}
	if ths, _ := w.Store.StoryThreads(ctx, "stray"); len(ths) != 0 {
		t.Fatalf("stray still in %+v", ths)
	}
	budget, _ := w.Store.ThreadByID(ctx, "budget")
	if budget.Stories != 4 || !budget.UpdatedAt.Equal(now.Add(-10*day)) {
		t.Fatalf("budget %+v", budget)
	}
	// The merged-away thread's vector is gone.
	if v, _ := w.Narratives.Vector(ctx, store.KindThread, "rain2"); v != nil {
		t.Fatal("rain2 kept its vector")
	}
	// Not again within the week.
	n := fake.count()
	if err := w.tidyIfDue(ctx, Day(now), now.Add(3*day)); err != nil || fake.count() != n {
		t.Fatalf("%v, %d requests", err, fake.count())
	}
}
