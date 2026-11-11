package summary

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/store"
)

// Threads are affairs that run over months or years (a war, an investigation, coalition talks): stories Gemini
// links, once a day, from the day's new stories and the list of every thread so far. A story can be in several
// threads, and a thread has many stories.

const (
	// DefaultThreadsEvery is how often stories are linked into threads.
	DefaultThreadsEvery = 24 * time.Hour
	// threadStories is the most new stories one linking request takes; threadRequests the most requests a linking.
	threadStories  = 120
	threadRequests = 3
	// threadsShown is the most threads a request lists, latest updated first: years of them fit.
	threadsShown = 3000
	// linkedAtKey is where the latest linking's time is kept.
	linkedAtKey = "threads_linked_at"
)

// linkThreadsIfDue links the stories written since the last linking when ThreadsEvery has passed and the day's
// budget has a request left.
func (w *Writer) linkThreadsIfDue(ctx context.Context, day string, now time.Time) error {
	every := w.ThreadsEvery
	if every == 0 {
		every = DefaultThreadsEvery
	}
	if every < 0 {
		return nil
	}
	if data, ok, err := w.Store.ServerSetting(ctx, linkedAtKey); err != nil {
		return err
	} else if ok {
		var last time.Time
		if json.Unmarshal(data, &last) == nil && now.Sub(last) < every {
			return nil
		}
	}
	if err := w.recordSaved(ctx); err != nil {
		return err
	}
	for sent := 0; sent < threadRequests; sent++ {
		used, err := w.Store.GeminiRequests(ctx, day)
		if err != nil {
			return err
		}
		if orDefault(w.Budget, DefaultBudget)-used < 1 {
			w.logf("threads: no Gemini request left today; linking waits")
			return nil
		}
		more, err := w.linkThreads(ctx, day, now)
		if err != nil {
			return err
		}
		if !more {
			break
		}
	}
	data, _ := json.Marshal(now)
	return w.Store.SaveServerSetting(ctx, linkedAtKey, data)
}

// recordSaved keeps for good the saved summaries written before story records were: each story's latest version
// (one a later saved version shares half its reports with is that story's earlier version).
func (w *Writer) recordSaved(ctx context.Context) error {
	saved, err := Summaries(ctx, w.Store)
	if err != nil {
		return err
	}
	for key, s := range saved {
		id := s.ID
		if id == "" {
			id = key
		}
		if r, err := w.Store.StoryRecord(ctx, id); err != nil {
			return err
		} else if r != nil {
			continue
		}
		if s.ID == "" && supersededIn(s, key, saved) {
			continue
		}
		s.ID = id
		data, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if err := w.Store.SaveStoryRecord(ctx, id, data, s.WrittenAt); err != nil {
			return err
		}
	}
	return nil
}

func supersededIn(s StorySummary, key string, saved map[string]StorySummary) bool {
	mine := reportsOf(s)
	for other, t := range saved {
		if other == key || !t.WrittenAt.After(s.WrittenAt) {
			continue
		}
		theirs := map[string]bool{}
		for _, url := range reportsOf(t) {
			theirs[url] = true
		}
		shared := 0
		for _, url := range mine {
			if theirs[url] {
				shared++
			}
		}
		if shared > 0 && 2*shared >= len(mine) {
			return true
		}
	}
	return false
}

// linkThreads sends one linking request for the oldest stories not yet linked; more says some still wait.
func (w *Writer) linkThreads(ctx context.Context, day string, now time.Time) (more bool, err error) {
	waiting, err := w.Store.UnlinkedStories(ctx, threadStories+1)
	if err != nil || len(waiting) == 0 {
		return false, err
	}
	more = len(waiting) > threadStories
	waiting = waiting[:min(len(waiting), threadStories)]
	threads, err := w.Store.Threads(ctx, threadsShown)
	if err != nil {
		return false, err
	}
	var prompt strings.Builder
	threadOf := map[string]store.Thread{}
	prompt.WriteString("=== חוטים קיימים ===\n")
	if len(threads) == 0 {
		prompt.WriteString("(אין עדיין)\n")
	}
	for i, t := range threads {
		ref := fmt.Sprintf("t%d", i+1)
		threadOf[ref] = t
		fmt.Fprintf(&prompt, "[%s] %s (%d סיפורים, %s–%s): %s\n", ref, oneLine(t.Name), t.Stories,
			israelDate(t.StartedAt), israelDate(t.UpdatedAt), oneLine(t.Summary))
	}
	prompt.WriteString("\n=== סיפורים חדשים ===\n")
	storyOf := map[string]store.StoryRecord{}
	for i, r := range waiting {
		var s StorySummary
		if err := json.Unmarshal(r.Summary, &s); err != nil {
			continue
		}
		ref := fmt.Sprintf("n%d", i+1)
		storyOf[ref] = r
		fmt.Fprintf(&prompt, "[%s] %s · %s\n", ref, israelDate(r.FirstWritten), oneLine(s.Title))
		for _, sentence := range s.Sentences[:min(len(s.Sentences), 3)] {
			fmt.Fprintf(&prompt, "- %s\n", oneLine(sentence.Text))
		}
	}
	raw, err := w.send(ctx, day, gemini.Request{System: threadsSystem, Prompt: prompt.String(), Schema: threadsSchema})
	if err != nil {
		return false, err
	}
	var answer struct {
		Links []struct {
			Story   string   `json:"story"`
			Threads []string `json:"threads"`
		} `json:"links"`
		NewThreads []struct {
			Name    string   `json:"name"`
			Summary string   `json:"summary"`
			Stories []string `json:"stories"`
		} `json:"newThreads"`
		Summaries []struct {
			Thread  string `json:"thread"`
			Summary string `json:"summary"`
		} `json:"summaries"`
		Related []struct {
			Thread  string   `json:"thread"`
			Related []string `json:"related"`
		} `json:"related"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return false, fmt.Errorf("reading Gemini's answer: %w", err)
	}
	// Each story's threads, by their real ids; and each touched thread's span.
	placed := map[string][]string{}
	touched := map[string]*store.Thread{}
	touch := func(t store.Thread, r store.StoryRecord) {
		if touched[t.ID] == nil {
			t := t
			touched[t.ID] = &t
		}
		if t := touched[t.ID]; t.StartedAt.IsZero() || r.FirstWritten.Before(t.StartedAt) {
			t.StartedAt = r.FirstWritten
		}
		if t := touched[t.ID]; r.WrittenAt.After(t.UpdatedAt) {
			t.UpdatedAt = r.WrittenAt
		}
	}
	for _, link := range answer.Links {
		r, ok := storyOf[strings.TrimSpace(link.Story)]
		if !ok {
			continue
		}
		for _, ref := range link.Threads {
			if t, ok := threadOf[strings.TrimSpace(ref)]; ok && !contains(placed[r.ID], t.ID) {
				placed[r.ID] = append(placed[r.ID], t.ID)
				touch(t, r)
			}
		}
	}
	created := 0
	for _, n := range answer.NewThreads {
		name := strings.TrimSpace(n.Name)
		if name == "" {
			continue
		}
		t := store.Thread{ID: newThreadID(), Name: name, Summary: strings.TrimSpace(n.Summary)}
		members := 0
		for _, ref := range n.Stories {
			if r, ok := storyOf[strings.TrimSpace(ref)]; ok && !contains(placed[r.ID], t.ID) {
				placed[r.ID] = append(placed[r.ID], t.ID)
				touch(t, r)
				members++
			}
		}
		if members > 0 {
			created++
			threadOf[name] = t // its summary may come by name
		}
	}
	for _, s := range answer.Summaries {
		ref := strings.TrimSpace(s.Thread)
		t, ok := threadOf[ref]
		if !ok || touched[t.ID] == nil || strings.TrimSpace(s.Summary) == "" {
			continue
		}
		touched[t.ID].Summary = strings.TrimSpace(s.Summary)
	}
	for _, t := range touched {
		if err := w.Store.SaveThread(ctx, *t); err != nil {
			return false, err
		}
	}
	// Related threads: existing ones by id, new ones by name; a new thread with no stories doesn't exist.
	exists := func(t store.Thread) bool {
		_, isNew := touched[t.ID]
		return isNew || t.Stories > 0
	}
	related := 0
	for _, r := range answer.Related {
		a, ok := threadOf[strings.TrimSpace(r.Thread)]
		if !ok || !exists(a) {
			continue
		}
		for _, ref := range r.Related {
			if b, ok := threadOf[strings.TrimSpace(ref)]; ok && exists(b) && a.ID != b.ID {
				if err := w.Store.RelateThreads(ctx, a.ID, b.ID); err != nil {
					return false, err
				}
				related++
			}
		}
	}
	linked := 0
	for _, r := range storyOf {
		if err := w.Store.LinkStory(ctx, r.ID, placed[r.ID]); err != nil {
			return false, err
		}
		if len(placed[r.ID]) > 0 {
			linked++
		}
	}
	w.logf("threads: %d new stories, %d in threads; %d threads new, %d updated; %d related", len(storyOf), linked, created, len(touched)-created, related)
	return more, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func newThreadID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "th" + hex.EncodeToString(b)
}

// israelDate is t's day in Israel, as the prompt shows it.
func israelDate(t time.Time) string {
	if zone, err := time.LoadLocation("Asia/Jerusalem"); err == nil {
		t = t.In(zone)
	}
	return t.Format("02.01.2006")
}
