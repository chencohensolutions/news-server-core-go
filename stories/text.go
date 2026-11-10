package stories

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
)

// StoryText is what headlines are compared by when grouping stories (the app's StoryText), with bge-m3's thresholds
// of the grouping choices for it (other models' in ModelSpec.Scale): each scores on its own scale. Measured on 348
// labeled cross-site pairs in the news repo's docs/story-model-comparison.md, where TitleAndSubtitle did best.
//
// TitleTimesSubtitle embeds the title and the subtitle apart and multiplies their two similarities: both must
// agree. Its vector is the two side by side (Vector), and Similarity multiplies the two halves' dot products, so the
// graph and the clustering take it like any other.
type StoryText int

const (
	Title StoryText = iota
	Subtitle
	TitleAndSubtitle
	TitleTimesSubtitle
)

// StoryTexts are all of them, in the app's order.
var StoryTexts = []StoryText{Title, Subtitle, TitleAndSubtitle, TitleTimesSubtitle}

var storyTextNames = map[StoryText]string{
	Title: "title", Subtitle: "subtitle", TitleAndSubtitle: "title_and_subtitle", TitleTimesSubtitle: "title_times_subtitle",
}

func (t StoryText) String() string { return storyTextNames[t] }

// ParseStoryText reads a config name ("title_and_subtitle").
func ParseStoryText(name string) (StoryText, error) {
	for t, n := range storyTextNames {
		if n == name {
			return t, nil
		}
	}
	return 0, fmt.Errorf("unknown story text %q (title, subtitle, title_and_subtitle, title_times_subtitle)", name)
}

// bge-m3's thresholds: strict, balanced, loose.
var bgeScales = map[StoryText]Scale{
	Title:              {0.58, 0.55, 0.52},
	Subtitle:           {0.62, 0.59, 0.56},
	TitleAndSubtitle:   {0.68, 0.645, 0.62},
	TitleTimesSubtitle: {0.35, 0.32, 0.29},
}

var e5Scales = map[StoryText]Scale{
	Title:              {0.898, 0.888, 0.878},
	Subtitle:           {0.898, 0.888, 0.878},
	TitleAndSubtitle:   {0.909, 0.899, 0.889},
	TitleTimesSubtitle: {0.783, 0.773, 0.763},
}

// Texts are the texts embedded for item: one, or the title and the subtitle for TitleTimesSubtitle.
func (t StoryText) Texts(item scrape.ArticleSummary) []string {
	switch t {
	case Title:
		return []string{item.Title}
	case Subtitle:
		return []string{storyText(item)}
	case TitleAndSubtitle:
		if d := details(item); d != "" {
			return []string{item.Title + ". " + d}
		}
		return []string{item.Title}
	default:
		return []string{item.Title, storyText(item)}
	}
}

// IsFinal is whether item's text is all in: a title needs nothing more; the others wait for the article's subtitle.
func (t StoryText) IsFinal(item scrape.ArticleSummary) bool { return t == Title || item.Subtitle != "" }

// Vector is one vector from the embeddings of Texts, in order.
func (t StoryText) Vector(embeddings [][]float32) []float32 {
	if len(embeddings) == 1 {
		return embeddings[0]
	}
	return slices.Concat(embeddings[0], embeddings[1])
}

// Similarity is how alike two Vectors are: their dot product, or for TitleTimesSubtitle the product of the halves'.
func (t StoryText) Similarity() Similarity {
	if t == TitleTimesSubtitle {
		return halvesProduct
	}
	return Dot
}

// The grouping slider's ends and its step.
const (
	StrictnessMin  = 10
	StrictnessMax  = 90
	StrictnessStep = 5
)

// storyText is what a headline is grouped by in Subtitle: its article's subtitle (or first paragraph), else the list
// page's summary, else the title.
func storyText(item scrape.ArticleSummary) string {
	if d := details(item); d != "" {
		return d
	}
	return item.Title
}

func details(item scrape.ArticleSummary) string {
	if item.Subtitle != "" {
		return item.Subtitle
	}
	return item.Summary
}

func halvesProduct(a, b []float32) float32 {
	half := len(a) / 2
	var first, second float32
	for i := 0; i < half; i++ {
		first += float32(a[i] * b[i])
	}
	for i := half; i < len(a); i++ {
		second += float32(a[i] * b[i])
	}
	return first * second
}

// Scale is the similarities of the grouping choices, strictest first, in one model's and one text's scale.
type Scale struct{ Strict, Balanced, Loose float32 }

// Scale is the thresholds of the grouping choices for text in this model's scale.
func (m ModelSpec) Scale(text StoryText) Scale {
	if m.ID == E5Small.ID {
		return e5Scales[text]
	}
	return bgeScales[text]
}

// Threshold is the similarity two headlines need at strictness (10 to 90, the app's grouping slider): 50 is
// Balanced, 70 Strict, 30 Loose, and each 20 one more such step (the step on each side of Balanced its own).
func (s Scale) Threshold(strictness int) float32 {
	step := s.Balanced - s.Loose
	if strictness >= 50 {
		step = s.Strict - s.Balanced
	}
	return s.Balanced + float32(strictness-50)/20*step
}

// Floor is the similarity from which pairs are kept in the similarity graph: a little below the slider's loosest end.
func (s Scale) Floor() float32 { return s.Threshold(StrictnessMin) - 0.01 }

// Story is several sites' reports of one event; the first, Lead, is shown as the story's main headline.
type Story struct {
	Reports []scrape.ArticleSummary
}

func (s Story) Lead() scrape.ArticleSummary { return s.Reports[0] }

// Latest is when the story was last reported: its newest report's time; false when no report has one.
func (s Story) Latest() (time.Time, bool) { return latestOf(s.Reports, nil) }

func publishedAt(item scrape.ArticleSummary) (time.Time, bool) {
	if item.PublishedAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, item.PublishedAt)
	return t, err == nil
}

func latestOf(reports []scrape.ArticleSummary, counts func(scrape.ArticleSummary) bool) (time.Time, bool) {
	var latest time.Time
	found := false
	for _, r := range reports {
		if counts != nil && !counts(r) {
			continue
		}
		if t, ok := publishedAt(r); ok && (!found || t.After(latest)) {
			latest, found = t, true
		}
	}
	return latest, found
}

func positions(feed []scrape.ArticleSummary) func(url string) int {
	position := make(map[string]int, len(feed))
	for i, item := range feed {
		position[item.URL] = i
	}
	return func(url string) int {
		if p, ok := position[url]; ok {
			return p
		}
		return int(^uint(0) >> 1)
	}
}

// byTimeThenPosition orders newest first, untimed last, ties by position.
func byTimeThenPosition(ta time.Time, oka bool, pa int, tb time.Time, okb bool, pb int) int {
	switch {
	case oka && okb:
		if c := tb.Compare(ta); c != 0 {
			return c
		}
	case oka:
		return -1
	case okb:
		return 1
	}
	return cmp.Compare(pa, pb)
}

// NewestFirst orders newest first, inside each story and across them: a story's reports go newest first, so its
// lead is the newest report, and stories go by that time; untimed reports and stories come last, in feed order.
func NewestFirst(stories []Story, feed []scrape.ArticleSummary) []Story {
	position := positions(feed)
	sorted := make([]Story, len(stories))
	for i, s := range stories {
		reports := slices.Clone(s.Reports)
		slices.SortStableFunc(reports, func(a, b scrape.ArticleSummary) int {
			ta, oka := publishedAt(a)
			tb, okb := publishedAt(b)
			return byTimeThenPosition(ta, oka, position(a.URL), tb, okb, position(b.URL))
		})
		sorted[i] = Story{reports}
	}
	return LatestFirst(sorted, feed, nil)
}

// LatestFirst orders stories by their newest report, newest first (untimed last, in feed order); their reports as
// they are. Only the reports counts lets through (nil: all) set a story's time: a story shown for one site goes by
// that site's report.
func LatestFirst(stories []Story, feed []scrape.ArticleSummary, counts func(scrape.ArticleSummary) bool) []Story {
	position := positions(feed)
	out := slices.Clone(stories)
	slices.SortStableFunc(out, func(a, b Story) int {
		ta, oka := latestOf(a.Reports, counts)
		tb, okb := latestOf(b.Reports, counts)
		return byTimeThenPosition(ta, oka, position(a.Lead().URL), tb, okb, position(b.Lead().URL))
	})
	return out
}

// WithinDays is those of items published in the last days days, the ones stories are grouped from; 0: all. One
// without a time stays: it is in the feed now, and its age is unknown.
func WithinDays(items []scrape.ArticleSummary, days int, now time.Time) []scrape.ArticleSummary {
	if days <= 0 {
		return items
	}
	var out []scrape.ArticleSummary
	for _, item := range items {
		if t, ok := publishedAt(item); !ok || now.Sub(t) <= time.Duration(days)*24*time.Hour {
			out = append(out, item)
		}
	}
	return out
}
