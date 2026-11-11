package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/stories"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

// eval-gemini-grouping: whether Gemini groups headlines into stories better than the story model, on the app's
// 348 labeled pairs (news/tools/story-model-eval). One request with all 737 headlines, counted in the day's budget.

const geminiGroupingSystem = `You group Israeli news headlines into stories. The input is headlines from many sites of one day, each
with an id in brackets, its site, time, title and list summary. A story is one specific event or development reported
by several sites: the same happening, decision, statement, incident or announcement, even in different words, from
another side of it, or as a teaser that doesn't name it. Two headlines about the same general subject but different
happenings (two different shootings, two separate Knesset votes, a war's different days) are different stories.

Answer "stories": every group of two headlines or more that report one event, as the list of their ids. A headline is
in at most one story. Leave out headlines that no other headline reports.`

var geminiGroupingSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"stories": map[string]any{"type": "array", "items": map[string]any{
			"type": "array", "items": map[string]any{"type": "string"},
		}},
	},
	"required": []string{"stories"},
}

func evalGeminiGrouping(config auth.AppConfig, out io.Writer) error {
	if err := evalGeminiOneShot(config, out); err != nil {
		fmt.Fprintf(out, "one request: %v\n", err)
	}
	return evalGeminiRounds(config, out)
}

// evalGeminiRounds groups the test set as the server would: the headlines in time order, 100 a round, each round
// placed in the stories so far (summary.Writer.Group on a scratch database), the requests counted in the real one.
func evalGeminiRounds(config auth.AppConfig, out io.Writer) error {
	items, pairs, err := stories.LoadEvalSet(evalSetDir)
	if err != nil {
		return err
	}
	key, err := gemini.ReadKey(config.GeminiKeyFile)
	if err != nil {
		return fmt.Errorf("no Gemini key: %w", err)
	}
	dir, err := os.MkdirTemp("", "eval-gemini")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	scratch, err := store.Open(filepath.Join(dir, "news.db"))
	if err != nil {
		return err
	}
	defer scratch.Close()
	real, err := store.Open(config.DatabaseFile)
	if err != nil {
		return err
	}
	defer real.Close()
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return items[order[a]].PublishedAt < items[order[b]].PublishedAt })
	var last time.Time
	for _, it := range items {
		if t, err := time.Parse(time.RFC3339, it.PublishedAt); err == nil && t.After(last) {
			last = t
		}
	}
	w := &summary.Writer{Gemini: &gemini.Client{Key: key, Model: config.GeminiModel}, Store: scratch, Log: func(f string, a ...any) { fmt.Fprintf(out, f+"\n", a...) }}
	ctx := context.Background()
	start := time.Now()
	rounds := 0
	for shown := 100; ; shown += 100 {
		shown = min(shown, len(order))
		// The feed so far, newest first.
		var feed []scrape.ArticleSummary
		for k := shown - 1; k >= 0; k-- {
			feed = append(feed, items[order[k]])
		}
		before, _ := scratch.GeminiRequests(ctx, summary.Day(last))
		if _, err := w.Group(ctx, feed, last.Add(time.Hour)); err != nil {
			return fmt.Errorf("round %d: %w", rounds+1, err)
		}
		after, _ := scratch.GeminiRequests(ctx, summary.Day(last))
		for range after - before {
			real.CountGeminiRequest(ctx, summary.Day(time.Now()))
		}
		rounds++
		if shown == len(order) {
			break
		}
	}
	urls := make([]string, len(items))
	for i, it := range items {
		urls[i] = it.URL
	}
	placed, err := scratch.GeminiGroups(ctx, urls)
	if err != nil {
		return err
	}
	group := make([]string, len(items))
	for i, it := range items {
		group[i] = placed[it.URL]
	}
	var tp, fp, fn, tn int
	for _, p := range pairs {
		same := group[p.I] != "" && group[p.I] == group[p.J]
		switch {
		case same && p.Same:
			tp++
		case same && !p.Same:
			fp++
		case !same && p.Same:
			fn++
		default:
			tn++
		}
	}
	precision := float64(tp) / float64(max(1, tp+fp))
	recall := float64(tp) / float64(max(1, tp+fn))
	f1 := 2 * precision * recall / max(1e-9, precision+recall)
	fmt.Fprintf(out, "in rounds as the server: %d rounds in %s\n", rounds, time.Since(start).Round(time.Millisecond))
	fmt.Fprintf(out, "%d labeled pairs: F1 %.2f (precision %.2f, recall %.2f; %d right same, %d wrong same, %d missed, %d right apart)\n",
		len(pairs), f1, precision, recall, tp, fp, fn, tn)
	return nil
}

// evalGeminiOneShot is all 737 headlines in one request.
func evalGeminiOneShot(config auth.AppConfig, out io.Writer) error {
	items, pairs, err := stories.LoadEvalSet(evalSetDir)
	if err != nil {
		return fmt.Errorf("the test set (the news submodule's tools/story-model-eval): %w", err)
	}
	key, err := gemini.ReadKey(config.GeminiKeyFile)
	if err != nil {
		return fmt.Errorf("no Gemini key: %w", err)
	}
	db, err := store.Open(config.DatabaseFile)
	if err != nil {
		return err
	}
	defer db.Close()
	var prompt strings.Builder
	for i, item := range items {
		fmt.Fprintf(&prompt, "[h%d] %s", i+1, item.SourceID)
		if item.PublishedAt != "" {
			fmt.Fprintf(&prompt, " · %s", item.PublishedAt)
		}
		fmt.Fprintf(&prompt, "\n%s\n", oneLineText(item.Title))
		if s := oneLineText(item.Summary); s != "" {
			fmt.Fprintf(&prompt, "%s\n", s)
		}
	}
	day := summary.Day(time.Now())
	if err := db.CountGeminiRequest(context.Background(), day); err != nil {
		return err
	}
	client := &gemini.Client{Key: key, Model: config.GeminiModel}
	start := time.Now()
	raw, usage, err := client.Generate(context.Background(), gemini.Request{System: geminiGroupingSystem, Prompt: prompt.String(), Schema: geminiGroupingSchema})
	if err != nil {
		return err
	}
	db.CountGeminiTokens(context.Background(), day, store.Tokens{In: usage.PromptTokens, Cached: usage.CachedTokens, Out: usage.AnswerTokens, Thoughts: usage.ThoughtsTokens})
	var answer struct {
		Stories [][]string `json:"stories"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return fmt.Errorf("reading Gemini's answer: %w", err)
	}
	group := make([]int, len(items))
	for i := range group {
		group[i] = -1
	}
	multi := 0
	for g, members := range answer.Stories {
		n := 0
		for _, ref := range members {
			var i int
			if _, err := fmt.Sscanf(strings.TrimSpace(ref), "h%d", &i); err == nil && i >= 1 && i <= len(items) && group[i-1] < 0 {
				group[i-1] = g
				n++
			}
		}
		if n > 1 {
			multi++
		}
	}
	var tp, fp, fn, tn int
	for _, p := range pairs {
		same := group[p.I] >= 0 && group[p.I] == group[p.J]
		switch {
		case same && p.Same:
			tp++
		case same && !p.Same:
			fp++
		case !same && p.Same:
			fn++
		default:
			tn++
		}
	}
	precision := float64(tp) / float64(max(1, tp+fp))
	recall := float64(tp) / float64(max(1, tp+fn))
	f1 := 2 * precision * recall / max(1e-9, precision+recall)
	fmt.Fprintf(out, "%s: %d headlines in %s, %d tokens in, %d out, %d thinking; %d stories\n", client.Model, len(items),
		time.Since(start).Round(time.Millisecond), usage.PromptTokens, usage.AnswerTokens, usage.ThoughtsTokens, multi)
	fmt.Fprintf(out, "%d labeled pairs: F1 %.2f (precision %.2f, recall %.2f; %d right same, %d wrong same, %d missed, %d right apart)\n",
		len(pairs), f1, precision, recall, tp, fp, fn, tn)
	fmt.Fprintf(out, "story model for comparison (eval-stories): bge-m3's best F1 0.83, picked at the best threshold\n")
	return nil
}

func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }
