package scrape

import "strings"

// ArticleSummary is one entry on a site's headline page (core/model Article.kt). Absent optional values are
// omitted from JSON, as the Kotlin model writes them.
type ArticleSummary struct {
	SourceID    string `json:"sourceId"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Summary     string `json:"summary,omitempty"`
	ImageURL    string `json:"imageUrl,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	Category    string `json:"category,omitempty"`
	// Filled in from the article page (FilledFrom); list pages never set them. Stories are grouped by Subtitle.
	Subtitle string `json:"subtitle,omitempty"`
	Video    bool   `json:"video,omitempty"`
}

// Article is a fully read article page.
type Article struct {
	SourceID    string        `json:"sourceId"`
	URL         string        `json:"url"`
	Title       string        `json:"title"`
	Subtitle    string        `json:"subtitle,omitempty"`
	Author      string        `json:"author,omitempty"`
	PublishedAt string        `json:"publishedAt,omitempty"`
	ImageURL    string        `json:"imageUrl,omitempty"`
	Category    string        `json:"category,omitempty"`
	Body        []string      `json:"body,omitempty"`
	Blocks      []Block       `json:"blocks,omitempty"`
	Video       *ArticleVideo `json:"video,omitempty"`
}

// Block is a paragraph ({"type": "text", "text"}) or a photo ({"type": "image", "url", "caption"?}).
type Block struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	URL     string `json:"url,omitempty"`
	Caption string `json:"caption,omitempty"`
}

// ArticleVideo is the article's main video; Stream or Embed is set.
type ArticleVideo struct {
	Poster   string `json:"poster,omitempty"`
	Stream   string `json:"stream,omitempty"`
	Embed    string `json:"embed,omitempty"`
	Duration string `json:"duration,omitempty"`
}

// HasContent is whether an article page gave anything to read: text, photos, a video or at least a subtitle (a quiz
// has only that). A title alone is a site's error or block page.
func (a *Article) HasContent() bool {
	return len(a.Body) > 0 || len(a.Blocks) > 0 || a.Video != nil || strings.TrimSpace(a.Subtitle) != ""
}

// FilledFrom is this headline as its article has it (the app's ArticleFetcher.filledFrom): the article's own title,
// main picture, time, category and video wherever it gives them, and its subtitle (else its first paragraph) when the
// headline has none. A list shortens titles and drops names, often has no time, a thumbnail, no subtitle.
func (s ArticleSummary) FilledFrom(a *Article) ArticleSummary {
	if s.Subtitle == "" {
		if strings.TrimSpace(a.Subtitle) != "" {
			s.Subtitle = a.Subtitle
		} else {
			for _, p := range a.Body {
				if strings.TrimSpace(p) != "" {
					s.Subtitle = p
					break
				}
			}
		}
	}
	if strings.TrimSpace(a.Title) != "" {
		s.Title = a.Title
	}
	if a.ImageURL != "" {
		s.ImageURL = a.ImageURL
	}
	if a.Category != "" {
		s.Category = a.Category
	}
	s.Video = s.Video || a.Video != nil
	if a.PublishedAt != "" {
		s.PublishedAt = a.PublishedAt
	}
	return s
}
