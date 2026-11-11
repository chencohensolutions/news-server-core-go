package summary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// Grouping by Gemini (the app's grouping choice): each round, Gemini places the new headlines in the stories so far,
// starts new ones, and merges two that are one; what it placed is kept, so a headline is sent once. The story
// model's grouping is the fallback when Gemini can't answer.

const (
	// groupHours is how recent a headline must be to be placed, and a story to be offered.
	groupHours = 48
	// groupBatch is the most new headlines one request places; groupRequests the most requests a round sends.
	groupBatch    = 100 // as in the test that measured it (eval-gemini-grouping); at 150 the first live answer left most out
	groupRequests = 3
	// groupStories is the most stories a request offers, the latest first; groupTitles the titles shown of each.
	groupStories = 1500
	groupTitles  = 3
)

// ErrNoBudget is the day's Gemini requests used up: the model's grouping stands in.
var ErrNoBudget = errors.New("no Gemini request left today")

// Group is feed's stories as Gemini grouped them, placing the headlines it hasn't yet. Stories are of two sites or
// more, newest first.
func (w *Writer) Group(ctx context.Context, feed []scrape.ArticleSummary, now time.Time) ([]stories.Story, error) {
	urls := make([]string, len(feed))
	for i, item := range feed {
		urls[i] = item.URL
	}
	placed, err := w.Store.GeminiGroups(ctx, urls)
	if err != nil {
		return nil, err
	}
	since := now.Add(-groupHours * time.Hour)
	day := Day(now)
	for sent := 0; sent < groupRequests; sent++ {
		var waiting []scrape.ArticleSummary
		for _, item := range feed {
			if _, ok := placed[item.URL]; !ok && recent(item, since) {
				waiting = append(waiting, item)
			}
		}
		if len(waiting) == 0 {
			break
		}
		used, err := w.Store.GeminiRequests(ctx, day)
		if err != nil {
			return nil, err
		}
		if orDefault(w.Budget, DefaultBudget)-used < 2 {
			if sent == 0 {
				return nil, ErrNoBudget
			}
			break
		}
		if err := w.placeHeadlines(ctx, day, feed, waiting[:min(len(waiting), groupBatch)], placed, since, now); err != nil {
			if sent == 0 {
				return nil, err
			}
			w.logf("grouping: %v; the rest wait", err)
			break
		}
	}
	byStory := map[string][]scrape.ArticleSummary{}
	var order []string
	for _, item := range feed {
		id, ok := placed[item.URL]
		if !ok {
			continue
		}
		if _, seen := byStory[id]; !seen {
			order = append(order, id)
		}
		byStory[id] = append(byStory[id], item)
	}
	var out []stories.Story
	for _, id := range order {
		reports := byStory[id]
		sites := map[string]bool{}
		for _, r := range reports {
			sites[r.SourceID] = true
		}
		if len(sites) > 1 {
			out = append(out, stories.Story{Reports: reports})
		}
	}
	return stories.NewestFirst(out, feed), nil
}

// recent says item was published since since; one without a time counts as recent.
func recent(item scrape.ArticleSummary, since time.Time) bool {
	t, err := time.Parse(time.RFC3339, item.PublishedAt)
	return err != nil || !t.Before(since)
}

// placeHeadlines sends one request placing waiting in the stories of placed (updated with the answer).
func (w *Writer) placeHeadlines(ctx context.Context, day string, feed, waiting []scrape.ArticleSummary, placed map[string]string, since, now time.Time) error {
	// The stories so far with a recent headline, the latest first (feed is newest first), and their titles.
	members := map[string][]scrape.ArticleSummary{}
	var order []string
	for _, item := range feed {
		id, ok := placed[item.URL]
		if !ok {
			continue
		}
		if _, seen := members[id]; !seen {
			if !recent(item, since) || len(order) == groupStories {
				continue
			}
			order = append(order, id)
		}
		members[id] = append(members[id], item)
	}
	var prompt strings.Builder
	prompt.WriteString("=== סיפורים קיימים ===\n")
	if len(order) == 0 {
		prompt.WriteString("(אין עדיין)\n")
	}
	storyOf := map[string]string{}
	for i, id := range order {
		ref := fmt.Sprintf("s%d", i+1)
		storyOf[ref] = id
		var titles []string
		for _, r := range members[id][:min(len(members[id]), groupTitles)] {
			titles = append(titles, w.siteName(r.SourceID)+": "+oneLine(r.Title))
		}
		fmt.Fprintf(&prompt, "[%s] %s\n", ref, strings.Join(titles, " | "))
	}
	prompt.WriteString("\n=== כותרות חדשות ===\n")
	headlineOf := map[string]scrape.ArticleSummary{}
	for i, item := range waiting {
		ref := fmt.Sprintf("h%d", i+1)
		headlineOf[ref] = item
		fmt.Fprintf(&prompt, "[%s] %s", ref, w.siteName(item.SourceID))
		if item.PublishedAt != "" {
			fmt.Fprintf(&prompt, " · %s", item.PublishedAt)
		}
		fmt.Fprintf(&prompt, "\n%s\n", oneLine(item.Title))
		if sub := oneLine(item.Subtitle); sub != "" {
			fmt.Fprintf(&prompt, "%s\n", sub)
		} else if sum := oneLine(item.Summary); sum != "" {
			fmt.Fprintf(&prompt, "%s\n", sum)
		}
	}
	raw, err := w.send(ctx, day, gemini.Request{System: groupingSystem, Prompt: prompt.String(), Schema: groupingSchema})
	if err != nil {
		return err
	}
	var answer struct {
		Headlines []placing `json:"headlines"`
		Merge     []struct {
			Stories []string `json:"stories"`
		} `json:"merge"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return fmt.Errorf("reading Gemini's answer: %w", err)
	}
	if err := checkPlacing(len(waiting), answer.Headlines); err != nil {
		return err
	}
	fresh := map[string]string{} // Gemini's label for a new story → its id
	news := map[string]string{}
	joined, started, alone := 0, 0, 0
	for _, a := range answer.Headlines {
		item, ok := headlineOf[strings.TrimSpace(a.ID)]
		if !ok {
			continue
		}
		if _, done := news[item.URL]; done {
			continue
		}
		ref := strings.TrimSpace(a.Story)
		switch id, ok := storyOf[ref]; {
		case ok:
			news[item.URL] = id
			joined++
		case strings.HasPrefix(ref, "new"):
			if fresh[ref] == "" {
				fresh[ref] = newGroupID()
				started++
			}
			news[item.URL] = fresh[ref]
		default:
			news[item.URL] = newGroupID()
			alone++
		}
	}
	merged := map[string]string{}
	for _, m := range answer.Merge {
		var into string
		for _, ref := range m.Stories {
			id, ok := storyOf[strings.TrimSpace(ref)]
			if !ok {
				continue
			}
			for merged[id] != "" {
				id = merged[id]
			}
			if into == "" {
				into = id
			} else if id != into {
				merged[id] = into
			}
		}
	}
	if err := w.Store.PlaceInGeminiGroups(ctx, news, merged, now); err != nil {
		return err
	}
	for url, id := range news {
		placed[url] = id
	}
	for url, id := range placed {
		for merged[id] != "" {
			id = merged[id]
		}
		placed[url] = id
	}
	w.logf("grouping: %d new headlines, %d placed: %d joined stories, %d started %d stories, %d alone; %d stories merged",
		len(waiting), len(news), joined, len(news)-joined-alone, started, alone, len(merged))
	return nil
}

// checkPlacing refuses a lazy answer: one that leaves out most of the headlines, or puts every one of many alone
// (the first live answer placed 52 of 150, all alone). Nothing of it is kept, so the headlines are sent again.
func checkPlacing(asked int, answered []placing) error {
	if len(answered)*2 < asked {
		return fmt.Errorf("Gemini placed %d of %d headlines", len(answered), asked)
	}
	alone := 0
	for _, a := range answered {
		if strings.TrimSpace(a.Story) == "alone" {
			alone++
		}
	}
	if asked >= 20 && alone == len(answered) {
		return fmt.Errorf("Gemini put all %d headlines alone", asked)
	}
	return nil
}

// placing is one headline's place in Gemini's answer.
type placing struct {
	ID    string `json:"id"`
	Story string `json:"story"`
}

func newGroupID() string { return "g" + newThreadID()[2:] }
