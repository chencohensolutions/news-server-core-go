package scrape

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
