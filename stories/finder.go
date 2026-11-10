package stories

import (
	"context"
	"fmt"

	"github.com/chencohensolutions/news-server-core-go/scrape"
)

// TextEmbedder turns a text into a unit vector: an Embedder, or a fake in tests.
type TextEmbedder interface {
	Embed(text string) ([]float32, error)
}

// VectorStore keeps the headline vectors between runs: the store package's Store.
type VectorStore interface {
	Vectors(ctx context.Context, space string) (map[string][]float32, error)
	SaveVectors(ctx context.Context, space string, added map[string][]float32, removed []string) error
}

// Finder groups headlines that report the same event by comparing their meaning, by the text Text names (the app's
// StoryFinder, for one model and one text). It keeps each headline's vector in memory and in Store, so a run, or the
// next process, only embeds the new ones; and the close pairs in its graph, so it only compares the new ones with the
// rest and reclusters the pieces they touch, with the same stories as clustering the whole feed. Not safe for
// concurrent use.
type Finder struct {
	embedder  TextEmbedder
	store     VectorStore
	text      StoryText
	space     string
	vectors   map[string][]float32
	graph     *SimilarityGraph
	clusterer ComponentClusterer
	// LastEmbedded is how many headlines the last Find embedded.
	LastEmbedded int
}

// NewFinder finds stories with embedder (model's), keeping vectors in store; the graph keeps pairs from model's
// floor for text, so any strictness on the slider works without comparing again.
func NewFinder(embedder TextEmbedder, store VectorStore, model ModelSpec, text StoryText) *Finder {
	return &Finder{
		embedder: embedder,
		store:    store,
		text:     text,
		space:    model.ID + "/" + text.String(),
		graph:    NewSimilarityGraph(model.Scale(text).Floor(), text.Similarity()),
	}
}

// Text is what the finder groups headlines by.
func (f *Finder) Text() StoryText { return f.text }

// key is what a headline's vector is kept under: its URL and texts, so when its subtitle arrives it is embedded again.
func (f *Finder) key(item scrape.ArticleSummary) string {
	key := item.URL
	for _, t := range f.text.Texts(item) {
		key += "\n" + t
	}
	return key
}

// Find is the stories among items with reports from at least two sites, newest first (NewestFirst). threshold is
// the similarity two headlines need (Scale.Threshold): higher splits stories, lower merges related topics. Vectors
// of headlines no longer in items are forgotten. Stopped by ctx, what is embedded is kept.
func (f *Finder) Find(ctx context.Context, items []scrape.ArticleSummary, threshold float32) ([]Story, error) {
	if f.vectors == nil {
		saved, err := f.store.Vectors(ctx, f.space)
		if err != nil {
			return nil, fmt.Errorf("stories: reading vectors: %w", err)
		}
		f.vectors = saved
	}
	// One report per URL: the first.
	seen := make(map[string]bool, len(items))
	unique := make([]scrape.ArticleSummary, 0, len(items))
	for _, item := range items {
		if !seen[item.URL] {
			seen[item.URL] = true
			unique = append(unique, item)
		}
	}
	items = unique
	keys := make([]string, len(items))
	byKey := make(map[string]scrape.ArticleSummary, len(items))
	current := make(map[string][]float32, len(items))
	for i, item := range items {
		keys[i] = f.key(item)
		byKey[keys[i]] = item
	}
	added := map[string][]float32{}
	var embedErr error
	for i, key := range keys {
		if _, ok := f.vectors[key]; ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			embedErr = err
			break
		}
		var embeddings [][]float32
		for _, text := range f.text.Texts(items[i]) {
			v, err := f.embedder.Embed(text)
			if err != nil {
				embedErr = fmt.Errorf("stories: embedding %s: %w", items[i].URL, err)
				break
			}
			embeddings = append(embeddings, v)
		}
		if embedErr != nil {
			break
		}
		added[key] = f.text.Vector(embeddings)
	}
	for key, v := range added {
		f.vectors[key] = v
	}
	f.LastEmbedded = len(added)
	for _, key := range keys {
		current[key] = f.vectors[key]
	}
	if embedErr != nil {
		// What is done is kept for the next run; nothing is forgotten from a feed not whole.
		if err := f.store.SaveVectors(context.WithoutCancel(ctx), f.space, added, nil); err != nil {
			return nil, err
		}
		return nil, embedErr
	}
	var removed []string
	for key := range f.vectors {
		if _, ok := current[key]; !ok {
			removed = append(removed, key)
		}
	}
	for _, key := range removed {
		delete(f.vectors, key)
	}
	if err := f.store.SaveVectors(ctx, f.space, added, removed); err != nil {
		return nil, fmt.Errorf("stories: saving vectors: %w", err)
	}
	if _, err := f.graph.Update(ctx, current); err != nil {
		return nil, err
	}
	groups, err := f.clusterer.Groups(ctx, keys, current, f.graph, threshold, true)
	if err != nil {
		return nil, err
	}
	// A story is several sites reporting one event: one site's look-alike headlines (a business site's "the stock jumped
	// N% after the report", for different companies) can be close in meaning without being one event.
	var stories []Story
	for _, group := range groups {
		reports := make([]scrape.ArticleSummary, len(group))
		sites := map[string]bool{}
		for i, key := range group {
			reports[i] = byKey[key]
			sites[reports[i].SourceID] = true
		}
		if len(sites) > 1 {
			stories = append(stories, Story{reports})
		}
	}
	return NewestFirst(stories, items), nil
}

// MoveUnrelated takes out of found the reports flags says aren't about the same event as the rest of their story
// (Gemini's: a flagged URL and the URLs it was flagged against) and moves each to the other story it is closest to,
// by its mean similarity to that story's reports, when that is at least threshold, as Find would have grouped it;
// else it is in no story. A story left with one site is no story. Run it on what Find returned, with the same feed.
func (f *Finder) MoveUnrelated(found []Story, feed []scrape.ArticleSummary, flags map[string]map[string]bool, threshold float32) []Story {
	if len(flags) == 0 {
		return found
	}
	var loose []scrape.ArticleSummary
	kept := make([]Story, 0, len(found))
	for _, s := range found {
		var reports []scrape.ArticleSummary
		for _, r := range s.Reports {
			against := flags[r.URL]
			flagged := false
			for _, o := range s.Reports {
				if o.URL != r.URL && against[o.URL] {
					flagged = true
					break
				}
			}
			if flagged {
				loose = append(loose, r)
			} else {
				reports = append(reports, r)
			}
		}
		if len(reports) == len(s.Reports) {
			kept = append(kept, s)
		} else if sites(reports) > 1 {
			kept = append(kept, Story{reports})
		}
	}
	if len(loose) == 0 {
		return found
	}
	similarity := f.text.Similarity()
	moves := make([][]scrape.ArticleSummary, len(kept))
	for _, r := range loose {
		v, ok := f.vectors[f.key(r)]
		if !ok {
			continue
		}
		best, bestSim := -1, threshold
		for i, s := range kept {
			var sum float32
			n, skip := 0, false
			for _, o := range s.Reports {
				if flags[r.URL][o.URL] {
					skip = true
					break
				}
				if w, ok := f.vectors[f.key(o)]; ok {
					sum += similarity(v, w)
					n++
				}
			}
			if skip || n == 0 {
				continue
			}
			if mean := sum / float32(n); mean >= bestSim {
				best, bestSim = i, mean
			}
		}
		if best >= 0 {
			moves[best] = append(moves[best], r)
		}
	}
	for i, added := range moves {
		if len(added) > 0 {
			kept[i] = Story{append(append([]scrape.ArticleSummary{}, kept[i].Reports...), added...)}
		}
	}
	return NewestFirst(kept, feed)
}

func sites(reports []scrape.ArticleSummary) int {
	seen := map[string]bool{}
	for _, r := range reports {
		seen[r.SourceID] = true
	}
	return len(seen)
}
