package summary

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/narrative"
	"github.com/chencohensolutions/news-server-core-go/store"
)

// The weekly tidy-up of the threads: the vectors find two threads that may be one affair, a thread that may be two,
// and stories far from their thread (narrative.FindTidyCandidates); one Gemini request decides on them and names
// what changes. A merged thread's id still finds the thread it is part of now (thread_aliases).

const (
	// DefaultTidyEvery is how often the threads are tidied.
	DefaultTidyEvery = 7 * 24 * time.Hour
	tidiedAtKey      = "threads_tidied_at"
	// tidyTitles is the most story titles a candidate thread shows, the latest.
	tidyTitles = 8
)

// tidyIfDue tidies the threads when TidyEvery has passed since the last tidy-up and the day's budget has a request
// left.
func (w *Writer) tidyIfDue(ctx context.Context, day string, now time.Time) error {
	every := w.TidyEvery
	if every == 0 {
		every = DefaultTidyEvery
	}
	if every < 0 || w.Narratives == nil {
		return nil
	}
	if data, ok, err := w.Store.ServerSetting(ctx, tidiedAtKey); err != nil {
		return err
	} else if ok {
		var last time.Time
		if json.Unmarshal(data, &last) == nil && now.Sub(last) < every {
			return nil
		}
	}
	used, err := w.Store.GeminiRequests(ctx, day)
	if err != nil {
		return err
	}
	if orDefault(w.Budget, DefaultBudget)-used < 1 {
		return nil
	}
	if _, err := w.Tidy(ctx, day, false); err != nil {
		return err
	}
	data, _ := json.Marshal(now)
	return w.Store.SaveServerSetting(ctx, tidiedAtKey, data)
}

// TidyResult is what a tidy-up found and did.
type TidyResult struct {
	Candidates narrative.TidyCandidates
	Merged     int
	Split      int
	Removed    int
}

// tidyThreads are the threads with their vectors and their stories', for the candidates.
func (w *Writer) tidyThreads(ctx context.Context) ([]narrative.TidyThread, map[string]store.Thread, error) {
	if err := w.catchUpNarratives(ctx); err != nil {
		return nil, nil, err
	}
	threads, err := w.Store.Threads(ctx, 1<<30)
	if err != nil {
		return nil, nil, err
	}
	members, err := w.Store.ThreadMembers(ctx)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]store.Thread{}
	var out []narrative.TidyThread
	for _, t := range threads {
		byID[t.ID] = t
		tv, err := w.Narratives.Vector(ctx, store.KindThread, t.ID)
		if err != nil {
			return nil, nil, err
		}
		vectors, err := w.Store.NarrativeVectors(ctx, w.Narratives.Space, store.KindStory, members[t.ID])
		if err != nil {
			return nil, nil, err
		}
		tt := narrative.TidyThread{ID: t.ID, Vector: tv}
		for _, id := range members[t.ID] {
			v := vectors[id]
			tt.Stories = append(tt.Stories, narrative.TidyStory{ID: id, At: v.At, Vector: v.Vector})
		}
		out = append(out, tt)
	}
	return out, byID, nil
}

// Tidy finds the candidates and, unless dry, asks Gemini about them and applies its answer.
func (w *Writer) Tidy(ctx context.Context, day string, dry bool) (TidyResult, error) {
	var result TidyResult
	if w.Narratives == nil {
		return result, nil
	}
	threads, byID, err := w.tidyThreads(ctx)
	if err != nil {
		return result, err
	}
	c := narrative.FindTidyCandidates(threads)
	result.Candidates = c
	if dry || c.Empty() {
		return result, nil
	}
	titles := map[string]string{}
	title := func(id string) string {
		if t, ok := titles[id]; ok {
			return t
		}
		t := ""
		if r, err := w.Store.StoryRecord(ctx, id); err == nil && r != nil {
			var s StorySummary
			if json.Unmarshal(r.Summary, &s) == nil {
				t = israelDate(r.FirstWritten) + " · " + oneLine(s.Title)
			}
		}
		titles[id] = t
		return t
	}
	storiesOf := map[string][]string{}
	for _, t := range threads {
		for _, s := range t.Stories {
			storiesOf[t.ID] = append(storiesOf[t.ID], s.ID)
		}
	}
	describe := func(p *strings.Builder, ref string, t store.Thread) {
		fmt.Fprintf(p, "[%s] %s (%d סיפורים, %s–%s): %s\n", ref, oneLine(t.Name), t.Stories, israelDate(t.StartedAt),
			israelDate(t.UpdatedAt), oneLine(t.Summary))
		ids := storiesOf[t.ID]
		for _, id := range ids[max(0, len(ids)-tidyTitles):] {
			if s := title(id); s != "" {
				fmt.Fprintf(p, "  - %s\n", s)
			}
		}
	}
	var prompt strings.Builder
	mergeOf := map[string]narrative.MergeCandidate{}
	if len(c.Merges) > 0 {
		prompt.WriteString("=== אולי חוט אחד ===\n")
	}
	for i, m := range c.Merges {
		ref := fmt.Sprintf("m%d", i+1)
		mergeOf[ref] = m
		fmt.Fprintf(&prompt, "{%s}\n", ref)
		describe(&prompt, ref+"a", byID[m.A])
		describe(&prompt, ref+"b", byID[m.B])
	}
	splitOf := map[string]narrative.SplitCandidate{}
	if len(c.Splits) > 0 {
		prompt.WriteString("\n=== אולי שני חוטים או יותר ===\n")
	}
	for i, s := range c.Splits {
		ref := fmt.Sprintf("s%d", i+1)
		splitOf[ref] = s
		t := byID[s.Thread]
		fmt.Fprintf(&prompt, "{%s} %s: %s\n", ref, oneLine(t.Name), oneLine(t.Summary))
		for g, group := range s.Groups {
			fmt.Fprintf(&prompt, "  [%sg%d]\n", ref, g+1)
			for _, id := range group[max(0, len(group)-tidyTitles):] {
				if s := title(id); s != "" {
					fmt.Fprintf(&prompt, "  - %s\n", s)
				}
			}
		}
	}
	strayOf := map[string]narrative.StrayCandidate{}
	if len(c.Strays) > 0 {
		prompt.WriteString("\n=== סיפורים שאולי אינם שייכים לחוט שלהם ===\n")
	}
	for i, s := range c.Strays {
		ref := fmt.Sprintf("x%d", i+1)
		strayOf[ref] = s
		fmt.Fprintf(&prompt, "{%s} החוט %s: %s\n  הסיפור: %s\n", ref, oneLine(byID[s.Thread].Name), oneLine(byID[s.Thread].Summary), title(s.Story))
	}
	raw, err := w.send(ctx, day, gemini.Request{System: tidySystem, Prompt: prompt.String(), Schema: tidySchema})
	if err != nil {
		return result, err
	}
	var answer struct {
		Merges []struct {
			Candidate string `json:"candidate"`
			Same      bool   `json:"same"`
			Name      string `json:"name"`
			Summary   string `json:"summary"`
		} `json:"merges"`
		Splits []struct {
			Candidate string `json:"candidate"`
			Separate  bool   `json:"separate"`
			Parts     []struct {
				Group   string `json:"group"`
				Name    string `json:"name"`
				Summary string `json:"summary"`
			} `json:"parts"`
		} `json:"splits"`
		Strays []struct {
			Candidate string `json:"candidate"`
			Belongs   bool   `json:"belongs"`
		} `json:"strays"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return result, fmt.Errorf("reading Gemini's answer: %w", err)
	}
	gone := map[string]string{} // merged away, into
	resolve := func(id string) string {
		for gone[id] != "" {
			id = gone[id]
		}
		return id
	}
	for _, a := range answer.Merges {
		m, ok := mergeOf[strings.TrimSpace(a.Candidate)]
		name, sum := strings.TrimSpace(a.Name), strings.TrimSpace(a.Summary)
		if !ok || !a.Same || name == "" || sum == "" {
			continue
		}
		keep, drop := resolve(m.A), resolve(m.B)
		if keep == drop {
			continue
		}
		// The bigger thread keeps its id; of two the same size, the older.
		k, d := byID[keep], byID[drop]
		if d.Stories > k.Stories || d.Stories == k.Stories && d.StartedAt.Before(k.StartedAt) {
			keep, drop = drop, keep
		}
		if err := w.Store.MergeThreads(ctx, keep, drop, name, sum); err != nil {
			return result, err
		}
		if err := w.Store.DeleteNarrative(ctx, w.Narratives.Space, store.KindThread, drop); err != nil {
			return result, err
		}
		gone[drop] = keep
		result.Merged++
	}
	for _, a := range answer.Splits {
		s, ok := splitOf[strings.TrimSpace(a.Candidate)]
		if !ok || !a.Separate || gone[s.Thread] != "" {
			continue
		}
		ref := strings.TrimSpace(a.Candidate)
		parts := map[int][2]string{}
		for _, p := range a.Parts {
			var g int
			if _, err := fmt.Sscanf(strings.TrimPrefix(strings.TrimSpace(p.Group), ref), "g%d", &g); err != nil || g < 1 || g > len(s.Groups) {
				continue
			}
			if name, sum := strings.TrimSpace(p.Name), strings.TrimSpace(p.Summary); name != "" && sum != "" {
				parts[g-1] = [2]string{name, sum}
			}
		}
		// Every group needs its name; the first keeps the thread's id.
		if len(parts) != len(s.Groups) {
			continue
		}
		if err := w.Store.RenameThread(ctx, s.Thread, parts[0][0], parts[0][1]); err != nil {
			return result, err
		}
		for g := 1; g < len(s.Groups); g++ {
			t := store.Thread{ID: newThreadID(), Name: parts[g][0], Summary: parts[g][1], StartedAt: byID[s.Thread].StartedAt, UpdatedAt: byID[s.Thread].UpdatedAt}
			if err := w.Store.SaveThread(ctx, t); err != nil {
				return result, err
			}
			if err := w.Store.MoveStories(ctx, s.Thread, t.ID, s.Groups[g]); err != nil {
				return result, err
			}
			if err := w.Store.RelateThreads(ctx, s.Thread, t.ID); err != nil {
				return result, err
			}
		}
		result.Split++
	}
	for _, a := range answer.Strays {
		s, ok := strayOf[strings.TrimSpace(a.Candidate)]
		if !ok || a.Belongs || gone[s.Thread] != "" {
			continue
		}
		if err := w.Store.MoveStories(ctx, s.Thread, "", []string{s.Story}); err != nil {
			return result, err
		}
		result.Removed++
	}
	if err := w.catchUpNarratives(ctx); err != nil {
		return result, err
	}
	w.logf("threads tidied: %d merge, %d split and %d stray candidates; %d merged, %d split, %d stories taken out",
		len(c.Merges), len(c.Splits), len(c.Strays), result.Merged, result.Split, result.Removed)
	return result, nil
}
