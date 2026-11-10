package model

import (
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

// The news types of graph/schema/news.graphql, made from what the server stored (bound in gqlgen.yml).

type Headline struct {
	SourceID    string
	URL         string
	Title       string
	Summary     *string
	ImageURL    *string
	PublishedAt *time.Time
	Category    *string
	Subtitle    *string
	Video       bool
	FirstSeen   time.Time
	LastSeen    time.Time
}

type Article struct {
	SourceID    string
	URL         string
	Title       string
	Subtitle    *string
	Author      *string
	PublishedAt *time.Time
	ImageURL    *string
	Category    *string
	Body        []string
	Blocks      []*ArticleBlock
	Video       *ArticleVideo
}

type ArticleBlock struct {
	Type    string
	Text    *string
	URL     *string
	Caption *string
}

type ArticleVideo struct {
	Poster   *string
	Stream   *string
	Embed    *string
	Duration *string
}

type StoryList struct {
	GroupedAt *time.Time
	Stories   []*Story
}

type Story struct {
	Reports []*Headline
	Summary *StorySummary
}

// optional is s, nil when empty.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// timeOf is an RFC 3339 time, nil when empty or unreadable.
func timeOf(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return &t
}

// HeadlineOf is item as the API shows it, seen from firstSeen to lastSeen.
func HeadlineOf(item scrape.ArticleSummary, firstSeen, lastSeen time.Time) *Headline {
	return &Headline{
		SourceID:    item.SourceID,
		URL:         item.URL,
		Title:       item.Title,
		Summary:     optional(item.Summary),
		ImageURL:    optional(item.ImageURL),
		PublishedAt: timeOf(item.PublishedAt),
		Category:    optional(item.Category),
		Subtitle:    optional(item.Subtitle),
		Video:       item.Video,
		FirstSeen:   firstSeen,
		LastSeen:    lastSeen,
	}
}

// StoredHeadline is h as the API shows it.
func StoredHeadline(h store.Headline) *Headline {
	return HeadlineOf(h.ArticleSummary, h.FirstSeen, h.LastSeen)
}

// ArticleOf is a as the API shows it.
func ArticleOf(a *scrape.Article) *Article {
	out := &Article{
		SourceID:    a.SourceID,
		URL:         a.URL,
		Title:       a.Title,
		Subtitle:    optional(a.Subtitle),
		Author:      optional(a.Author),
		PublishedAt: timeOf(a.PublishedAt),
		ImageURL:    optional(a.ImageURL),
		Category:    optional(a.Category),
		Body:        a.Body,
		Blocks:      []*ArticleBlock{},
	}
	if out.Body == nil {
		out.Body = []string{}
	}
	for _, b := range a.Blocks {
		out.Blocks = append(out.Blocks, &ArticleBlock{Type: b.Type, Text: optional(b.Text), URL: optional(b.URL), Caption: optional(b.Caption)})
	}
	if v := a.Video; v != nil {
		out.Video = &ArticleVideo{Poster: optional(v.Poster), Stream: optional(v.Stream), Embed: optional(v.Embed), Duration: optional(v.Duration)}
	}
	return out
}

// SentencesOf are sentences with their footnotes' sites and titles from headlines, by URL.
func SentencesOf(sentences []summary.Sentence, headlines map[string]store.Headline) []*Sentence {
	out := make([]*Sentence, 0, len(sentences))
	for _, s := range sentences {
		sentence := &Sentence{Text: s.Text, Footnotes: make([]*Footnote, 0, len(s.Sources))}
		for _, url := range s.Sources {
			note := &Footnote{URL: url}
			if h, ok := headlines[url]; ok {
				note.SourceID, note.Title = optional(h.SourceID), optional(h.Title)
			}
			sentence.Footnotes = append(sentence.Footnotes, note)
		}
		out = append(out, sentence)
	}
	return out
}

// StorySummaryOf is s for the API.
func StorySummaryOf(s summary.StorySummary, headlines map[string]store.Headline) *StorySummary {
	return &StorySummary{Title: s.Title, Sentences: SentencesOf(s.Sentences, headlines),
		Contradictions: SentencesOf(s.Contradictions, headlines), WrittenAt: s.WrittenAt}
}
