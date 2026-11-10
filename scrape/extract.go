package scrape

import (
	"fmt"
	"strings"

	"github.com/chencohensolutions/news-server-core-go/scrape/dom"
	"github.com/chencohensolutions/news-server-core-go/scrape/jsonpath"
	"github.com/chencohensolutions/news-server-core-go/scrape/rx"
	"golang.org/x/net/html"
)

// ScrapeError means a page didn't fit its config: the config no longer matches the site.
type ScrapeError struct{ Message string }

func (e *ScrapeError) Error() string { return e.Message }

// ParseList reads a headline page (HtmlExtractor.parseList). Items without a url or title are skipped, and
// duplicate urls are merged (MergeDuplicates).
func ParseList(c *SourceConfig, page, pageURL string) ([]ArticleSummary, error) {
	list := c.List
	doc := dom.ParseString(page)
	var section *ListSection
	for i := range list.Sections {
		if ResolveURL(c.BaseURL, list.Sections[i].URL) == pageURL {
			section = &list.Sections[i]
			break
		}
	}
	var items []scope
	switch {
	case list.JSON != nil:
		items = jsonItems(doc, list.JSON)
	case list.Item != nil:
		for _, el := range dom.Select(doc, dom.MustCompile(*list.Item)) {
			items = append(items, htmlScope{el})
		}
	default:
		return nil, &ScrapeError{c.ID + ": list has neither item nor json"}
	}
	var out []ArticleSummary
	for _, item := range items {
		url, ok := evalOne(item, list.URL)
		if !ok {
			continue
		}
		title, ok := evalOne(item, list.Title)
		if !ok {
			continue
		}
		s := ArticleSummary{SourceID: c.ID, URL: ResolveURL(pageURL, url), Title: title}
		if list.Summary != nil {
			s.Summary, _ = evalOne(item, list.Summary)
		}
		if list.Image != nil {
			if img, ok := evalOne(item, list.Image); ok {
				s.ImageURL = ResolveURL(pageURL, img)
			}
		}
		if list.PublishedAt != nil {
			s.PublishedAt = evalDate(item, list.PublishedAt)
		}
		raw, ok := "", false
		if list.Category != nil {
			raw, ok = evalOne(item, list.Category)
		}
		if !ok && section != nil {
			raw, ok = section.Category, true
		}
		if ok {
			s.Category = MapCategory(c, raw)
		}
		out = append(out, s)
	}
	return MergeDuplicates(out), nil
}

// MergeDuplicates keeps the first item per url, in order, and fills its missing optional fields from later
// duplicates.
func MergeDuplicates(items []ArticleSummary) []ArticleSummary {
	index := map[string]int{}
	var out []ArticleSummary
	for _, it := range items {
		i, seen := index[it.URL]
		if !seen {
			index[it.URL] = len(out)
			out = append(out, it)
			continue
		}
		first := &out[i]
		first.Summary = or(first.Summary, it.Summary)
		first.ImageURL = or(first.ImageURL, it.ImageURL)
		first.PublishedAt = or(first.PublishedAt, it.PublishedAt)
		first.Category = or(first.Category, it.Category)
	}
	return out
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ParseArticle reads an article page (HtmlExtractor.parseArticle). A page without a title is an error: the config
// no longer matches.
func ParseArticle(c *SourceConfig, page, url string) (*Article, error) {
	spec := &c.Article
	doc := dom.ParseString(page)
	var video *ArticleVideo
	if spec.Video != nil {
		video = readVideo(spec.Video, htmlScope{doc}, url)
	}
	category := ""
	if spec.Category != nil {
		if raw, ok := evalOne(htmlScope{doc}, spec.Category); ok {
			category = MapCategory(c, raw)
		}
	}
	// Read before `remove`, which usually drops the <script> holding it.
	text := doc
	if spec.Content != nil {
		if h, ok := contentHTML(doc, spec.Content); ok {
			text = dom.ParseString(h)
		}
	}
	for _, s := range spec.Remove {
		sel := dom.MustCompile(s)
		for _, el := range dom.Select(doc, sel) {
			dom.Remove(el)
		}
		if text != doc {
			for _, el := range dom.Select(text, sel) {
				dom.Remove(el)
			}
		}
	}
	sc := htmlScope{doc}
	var blocks []Block
	if spec.Figures != nil {
		blocks = readBlocks(text, spec.Body, spec.Figures, url)
	}
	title, ok := evalOne(sc, spec.Title)
	if !ok {
		sel := ""
		if spec.Title.Selector != nil {
			sel = *spec.Title.Selector
		}
		return nil, &ScrapeError{fmt.Sprintf("%s: no title found at %s (selector '%s')", c.ID, url, sel)}
	}
	a := &Article{SourceID: c.ID, URL: url, Title: title, Category: category, Blocks: blocks, Video: video}
	if spec.Subtitle != nil {
		a.Subtitle, _ = evalOne(sc, spec.Subtitle)
	}
	if spec.Author != nil {
		a.Author, _ = evalOne(sc, spec.Author)
	}
	if spec.PublishedAt != nil {
		a.PublishedAt = evalDate(sc, spec.PublishedAt)
	}
	if spec.Image != nil {
		if img, ok := evalOne(sc, spec.Image); ok {
			a.ImageURL = ResolveURL(url, img)
		}
	}
	if spec.Figures != nil {
		for _, b := range blocks {
			if b.Type == "text" {
				a.Body = append(a.Body, b.Text)
			}
		}
	} else {
		a.Body = evalAll(text, spec.Body)
	}
	return a, nil
}

// readBlocks is paragraphs and photos in document order; a paragraph inside a figure (its caption) belongs to it.
func readBlocks(doc *html.Node, body *FieldSpec, figures *FigureSpec, url string) []Block {
	if body.Selector == nil {
		return nil
	}
	figureSel := dom.MustCompile(figures.Selector)
	isFigure := map[*html.Node]bool{}
	for _, el := range dom.Select(doc, figureSel) {
		isFigure[el] = true
	}
	var out []Block
	for _, el := range dom.Select(doc, dom.MustCompile(*body.Selector+", "+figures.Selector)) {
		switch {
		case isFigure[el]:
			sc := htmlScope{el}
			if img, ok := evalOne(sc, figures.Image); ok {
				b := Block{Type: "image", URL: ResolveURL(url, img)}
				if figures.Caption != nil {
					b.Caption, _ = evalOne(sc, figures.Caption)
				}
				out = append(out, b)
			}
		case insideAny(el, isFigure):
		default:
			if t, ok := transform(body, readAttr(el, body.attr())); ok {
				out = append(out, Block{Type: "text", Text: t})
			}
		}
	}
	// The same picture twice in a row (an image and its zoom copy) shows once.
	var kept []Block
	for i, b := range out {
		if b.Type == "image" && i > 0 && out[i-1].Type == "image" && out[i-1].URL == b.URL {
			continue
		}
		kept = append(kept, b)
	}
	return kept
}

func insideAny(el *html.Node, set map[*html.Node]bool) bool {
	for p := el.Parent; p != nil; p = p.Parent {
		if set[p] {
			return true
		}
	}
	return false
}

func readVideo(spec *VideoSpec, sc scope, url string) *ArticleVideo {
	read := func(f *FieldSpec, resolve bool) string {
		if f == nil {
			return ""
		}
		v, ok := evalOne(sc, f)
		if !ok {
			return ""
		}
		if resolve {
			return ResolveURL(url, v)
		}
		return v
	}
	stream, embed := read(spec.Stream, true), read(spec.Embed, true)
	if stream == "" && embed == "" {
		return nil
	}
	return &ArticleVideo{Poster: read(spec.Poster, true), Stream: stream, Embed: embed, Duration: read(spec.Duration, false)}
}

// MapCategory is the first shared category, in config order, with a pattern found in raw; "" if none matches.
func MapCategory(c *SourceConfig, raw string) string {
	for _, id := range c.Categories.IDs {
		for _, p := range c.Categories.Patterns[id] {
			if re, err := rx.Compile(p); err == nil && re.Find(raw) {
				return id
			}
		}
	}
	return ""
}

func contentHTML(doc *html.Node, content *JSONContent) (string, bool) {
	el := dom.SelectFirst(doc, dom.MustCompile(content.Selector))
	if el == nil {
		return "", false
	}
	root, err := jsonpath.Parse(dom.Data(el))
	if err != nil {
		return "", false
	}
	path, err := jsonpath.Compile(content.Path)
	if err != nil {
		return "", false
	}
	for _, v := range path.Select(root) {
		if v.Kind == jsonpath.String {
			return v.Literal, true
		}
	}
	return "", false
}

// jsonItems: missing or malformed JSON yields no items, like an item selector that matches nothing.
func jsonItems(doc *html.Node, source *JSONItems) []scope {
	el := dom.SelectFirst(doc, dom.MustCompile(source.Selector))
	if el == nil {
		return nil
	}
	root, err := jsonpath.Parse(dom.Data(el))
	if err != nil {
		return nil
	}
	path, err := jsonpath.Compile(source.Items)
	if err != nil {
		return nil
	}
	var out []scope
	for _, v := range path.Select(root) {
		out = append(out, jsonScope{v})
	}
	return out
}

// scope is where a field reads from: an HTML element, or a JSON value in a JSON list.
type scope interface {
	// raw is the selected value before transforms; ok is false if nothing was selected.
	raw(spec *FieldSpec) (string, bool)
}

type htmlScope struct{ el *html.Node }

func (s htmlScope) raw(spec *FieldSpec) (string, bool) {
	target := s.el
	if spec.Selector != nil {
		if target = dom.SelectFirst(s.el, dom.MustCompile(*spec.Selector)); target == nil {
			return "", false
		}
	}
	return readAttr(target, spec.attr()), true
}

type jsonScope struct{ v *jsonpath.Value }

func (s jsonScope) raw(spec *FieldSpec) (string, bool) {
	target := s.v
	if spec.Path != nil {
		path, err := jsonpath.Compile(*spec.Path)
		if err != nil {
			return "", false
		}
		matches := path.Select(s.v)
		if len(matches) == 0 {
			return "", false
		}
		target = matches[0]
	}
	return target.Content()
}

func evalOne(sc scope, spec *FieldSpec) (string, bool) {
	if raw, ok := sc.raw(spec); ok {
		if v, ok := transform(spec, raw); ok {
			return v, true
		}
	}
	if spec.Fallback != nil {
		return evalOne(sc, spec.Fallback)
	}
	return "", false
}

// evalDate parses with each fallback's own date spec; "" when no field in the chain gives a date.
func evalDate(sc scope, spec *FieldSpec) string {
	if raw, ok := sc.raw(spec); ok {
		if v, ok := transform(spec, raw); ok {
			if t, ok := parseDate(v, spec.Date.withDefaults()); ok {
				return FormatInstant(t)
			}
		}
	}
	if spec.Fallback != nil {
		return evalDate(sc, spec.Fallback)
	}
	return ""
}

func evalAll(el *html.Node, spec *FieldSpec) []string {
	targets := []*html.Node{el}
	if spec.Selector != nil {
		targets = dom.Select(el, dom.MustCompile(*spec.Selector))
	}
	var out []string
	for _, t := range targets {
		if v, ok := transform(spec, readAttr(t, spec.attr())); ok {
			out = append(out, v)
		}
	}
	if len(out) == 0 && spec.Fallback != nil {
		return evalAll(el, spec.Fallback)
	}
	return out
}

func readAttr(el *html.Node, attr string) string {
	switch attr {
	case AttrText:
		return dom.Text(el)
	case AttrOwnText:
		return dom.OwnText(el)
	case AttrHTML:
		return dom.HTML(el)
	}
	return dom.Attr(el, attr)
}

// transform runs the field's transforms in order, then trims; ok is false when a transform finds nothing or the
// result is blank.
func transform(spec *FieldSpec, raw string) (string, bool) {
	value := raw
	for _, t := range spec.Transforms {
		re, err := rx.Compile(t.Pattern)
		if err != nil {
			return "", false
		}
		switch t.Type {
		case "regex":
			g, ok := re.Group(value, t.Group)
			if !ok {
				return "", false
			}
			value = g
		case "replace":
			value = re.Replace(value, t.With)
		}
	}
	value = kotlinTrim(value)
	return value, value != ""
}

// kotlinTrim is Kotlin's String.trim(): Unicode whitespace at both ends.
func kotlinTrim(s string) string {
	return strings.TrimFunc(s, isKotlinWhitespace)
}
