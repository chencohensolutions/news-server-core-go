package scrape

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// The Kotlin HtmlExtractorTest. Configs are written as config.json is, which ParseConfig also validates.

const (
	defaultList    = `{"urls": ["/"], "item": ".i", "url": {"selector": "a", "attr": "href"}, "title": {"selector": "a"}}`
	defaultArticle = `{"title": {"selector": "h1"}, "body": {"selector": "p"}}`
)

func configOf(t *testing.T, list, article, categories string) *SourceConfig {
	t.Helper()
	text := `{"schemaVersion": 1, "id": "t", "name": "T", "baseUrl": "https://t.example", "language": "en",
		"list": ` + list + `, "article": ` + article
	if categories != "" {
		text += `, "categories": ` + categories
	}
	c, err := ParseConfig([]byte(text + "}"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func jsonString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

func mustList(t *testing.T, c *SourceConfig, page, url string) []ArticleSummary {
	t.Helper()
	items, err := ParseList(c, page, url)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func mustArticle(t *testing.T, c *SourceConfig, page string) *Article {
	t.Helper()
	article, err := ParseArticle(c, page, "https://t.example/a")
	if err != nil {
		t.Fatal(err)
	}
	return article
}

func field(items []ArticleSummary, get func(ArticleSummary) string) []string {
	out := []string{}
	for _, item := range items {
		out = append(out, get(item))
	}
	return out
}

func equal(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func urlOf(i ArticleSummary) string { return i.URL }

func TestListSkipsItemsWithoutURLOrTitle(t *testing.T) {
	page := `<div class="i"><a href="/1">One</a></div><div class="i"><span>no link</span></div>
		<div class="i"><a href="/2"> </a></div>`
	equal(t, field(mustList(t, configOf(t, defaultList, defaultArticle, ""), page, "https://t.example/"), urlOf),
		[]string{"https://t.example/1"})
}

func TestDuplicatesKeepFirstPositionAndFillMissingFields(t *testing.T) {
	list := strings.Replace(defaultList, `"urls"`, `"image": {"selector": "img", "attr": "src"}, "summary": {"selector": "p"}, "urls"`, 1)
	page := `<div class="i"><a href="/1">First</a></div><div class="i"><a href="/2">Two</a></div>
		<div class="i"><a href="/1">Again</a><img src="/1.jpg"><p>Sum</p></div>`
	items := mustList(t, configOf(t, list, defaultArticle, ""), page, "https://t.example/")
	equal(t, field(items, urlOf), []string{"https://t.example/1", "https://t.example/2"})
	equal(t, []string{items[0].Title, items[0].ImageURL, items[0].Summary}, []string{"First", "https://t.example/1.jpg", "Sum"})
}

func TestCategoriesMapRawValuesInConfigOrder(t *testing.T) {
	list := strings.Replace(defaultList, `"urls"`,
		`"category": {"selector": "a", "attr": "href", "transforms": [{"type": "regex", "pattern": "^/([^/]+)/"}]}, "urls"`, 1)
	c := configOf(t, list, defaultArticle, `{"tech": ["^money-tech"], "economy": ["^money"]}`)
	page := `<div class="i"><a href="/money-tech/1">A</a></div><div class="i"><a href="/money/2">B</a></div>
		<div class="i"><a href="/food/3">C</a></div>`
	equal(t, field(mustList(t, c, page, "https://t.example/"), func(i ArticleSummary) string { return i.Category }),
		[]string{"tech", "economy", ""})
}

func TestSectionPagesGiveTheirCategoryToItemsWithoutOne(t *testing.T) {
	list := strings.Replace(defaultList, `"urls"`, `"sections": [{"url": "/world", "category": "abroad"}], "urls"`, 1)
	c := configOf(t, list, defaultArticle, `{"world": ["abroad"]}`)
	page := `<div class="i"><a href="/1">A</a></div>`
	equal(t, mustList(t, c, page, "https://t.example/world")[0].Category, "world")
	equal(t, mustList(t, c, page, "https://t.example/")[0].Category, "")
}

func TestASelectorlessFieldReadsTheItemItself(t *testing.T) {
	list := `{"urls": ["/"], "item": "a.i", "url": {"attr": "href"}, "title": {}}`
	items := mustList(t, configOf(t, list, defaultArticle, ""), `<a class="i" href="/x">X</a>`, "https://t.example/")
	equal(t, items[0].Title, "X")
}

func TestTransformsRunInOrderAndARegexMissMeansMissing(t *testing.T) {
	article := `{"title": {"selector": "h1"}, "body": {"selector": "p"},
		"author": {"selector": ".by", "transforms": [{"type": "replace", "pattern": "\\s*\\|.*$"}, {"type": "regex", "pattern": "^By (.+)"}]},
		"subtitle": {"selector": ".sub", "transforms": [{"type": "regex", "pattern": "nomatch"}]}}`
	result := mustArticle(t, configOf(t, defaultList, article, ""), `<h1>T</h1><span class="by">By Noa Cohen | Staff</span><p class="sub">Sub</p>`)
	equal(t, []string{result.Author, result.Subtitle}, []string{"Noa Cohen", ""})
}

func TestTheFallbackIsUsedWhenThePrimaryIsBlank(t *testing.T) {
	article := `{"title": {"selector": "h1"}, "body": {"selector": "p"},
		"image": {"selector": "img", "attr": "data-src", "fallback": {"selector": "img", "attr": "src"}}}`
	result, err := ParseArticle(configOf(t, defaultList, article, ""), `<h1>T</h1><img src="/i.jpg">`, "https://t.example/a/b")
	if err != nil {
		t.Fatal(err)
	}
	equal(t, result.ImageURL, "https://t.example/i.jpg")
}

func TestTheArticleCategoryMapsThePagesSectionName(t *testing.T) {
	article := `{"title": {"selector": "h1"}, "body": {"selector": "p"}, "category": {"selector": "meta[property=article:section]", "attr": "content"}}`
	c := configOf(t, defaultList, article, `{"sport": ["ספורט"]}`)
	section := func(name string) string {
		return mustArticle(t, c, `<meta property="article:section" content="`+name+`"><h1>T</h1><p>x</p>`).Category
	}
	equal(t, section("חדשות ספורט"), "sport")
	equal(t, section("בישול"), "")
	equal(t, mustArticle(t, c, "<h1>T</h1><p>x</p>").Category, "")
	// Read before `remove`: a site's section is often in a script the config drops.
	inScript := `{"title": {"selector": "h1"}, "body": {"selector": "p"}, "remove": ["script"],
		"category": {"selector": "script[type=application/ld+json]", "attr": "html", "transforms": [{"type": "regex", "pattern": "\"articleSection\":\"([^\"]+)\""}]}}`
	ld := `<script type="application/ld+json">{"articleSection":"ספורט"}</script><h1>T</h1><p>x</p>`
	equal(t, mustArticle(t, configOf(t, defaultList, inScript, `{"sport": ["ספורט"]}`), ld).Category, "sport")
}

func TestRemoveRunsBeforeExtraction(t *testing.T) {
	article := `{"title": {"selector": "h1"}, "body": {"selector": "p"}, "remove": [".ad"]}`
	result := mustArticle(t, configOf(t, defaultList, article, ""), `<h1>T</h1><p>keep</p><div class="ad"><p>ad</p></div><p></p>`)
	equal(t, result.Body, []string{"keep"})
}

func TestAJSONListReadsItemsFromEmbeddedJSON(t *testing.T) {
	list := `{"urls": ["/"], "json": {"selector": "script#data", "items": "$.page..post"},
		"url": {"path": "$.link"}, "title": {"path": "$.title", "transforms": [{"type": "replace", "pattern": "^\\s+"}]},
		"image": {"path": "$.images[0].src", "fallback": {"path": "$.cover"}},
		"publishedAt": {"path": "$.time", "date": {"format": "epochSeconds"}}}`
	page := `{"posts":[{"post":{"link":"/a","title":" A","images":[{"src":"/a.jpg"}],"time":0}},` +
		`{"post":{"link":"/b","title":"B","images":"","cover":"/b.jpg"}},{"post":{"link":"/c","images":[]}}]}`
	html := `<div class="card"></div><script id="data" type="application/json">{"page":` + jsonString(page) + `}</script>`
	items := mustList(t, configOf(t, list, defaultArticle, ""), html, "https://t.example/news")
	equal(t, field(items, urlOf), []string{"https://t.example/a", "https://t.example/b"})
	equal(t, field(items, func(i ArticleSummary) string { return i.Title }), []string{"A", "B"})
	equal(t, field(items, func(i ArticleSummary) string { return i.ImageURL }), []string{"https://t.example/a.jpg", "https://t.example/b.jpg"})
	equal(t, items[0].PublishedAt, "1970-01-01T00:00:00Z")
}

func TestAJSONListWithoutTheScriptIsEmpty(t *testing.T) {
	list := `{"urls": ["/"], "json": {"selector": "script#data", "items": "$..post"}, "url": {"path": "$.u"}, "title": {}}`
	equal(t, len(mustList(t, configOf(t, list, defaultArticle, ""), "<p>nothing</p>", "https://t.example/")), 0)
}

func TestFiguresComeBetweenParagraphsAndTheirCaptionsAreNotParagraphs(t *testing.T) {
	article := `{"title": {"selector": "h1"}, "body": {"selector": ".text p"},
		"figures": {"selector": "figure", "image": {"selector": "img", "attr": "src"}, "caption": {"selector": "figcaption"}}}`
	page := `<h1>T</h1><div class="text"><p>One</p><figure><img src="/a.jpg"><img src="/a.jpg">
		<figcaption><p>Cap</p></figcaption></figure><p>Two</p><figure><span>no image</span></figure>
		<figure><img src="/b.jpg"></figure><figure><img src="/b.jpg"></figure><p>Three</p></div>`
	result := mustArticle(t, configOf(t, defaultList, article, ""), page)
	// With figures, body is the blocks' text: the caption's <p> belongs to its figure.
	equal(t, result.Body, []string{"One", "Two", "Three"})
	equal(t, result.Blocks, []Block{
		{Type: "text", Text: "One"},
		{Type: "image", URL: "https://t.example/a.jpg", Caption: "Cap"},
		{Type: "text", Text: "Two"},
		{Type: "image", URL: "https://t.example/b.jpg"},
		{Type: "text", Text: "Three"},
	})
}

func TestContentFromEmbeddedJSONFeedsBodyAndFigures(t *testing.T) {
	article := `{"title": {"selector": "h1"}, "body": {"selector": "p"}, "remove": ["script"],
		"content": {"selector": "script#data", "path": "$.post..content"},
		"figures": {"selector": "widget[type=photo]",
			"image": {"attr": "context", "transforms": [{"type": "regex", "pattern": "\"src\":\"([^\"]+)\""}]},
			"caption": {"attr": "context", "transforms": [{"type": "regex", "pattern": "\"caption\":\"([^\"]+)\""}]}}}`
	body := `<p>One</p><widget type="photo" context='{"src":"/a.jpg","caption":"Cap"}'></widget><p>Two</p>`
	page := `<script id="data" type="application/json">{"post":{"blocks":[{"content":` + jsonString(body) +
		`}]}}</script><h1>T</h1><p>placeholder on the page</p>`
	result := mustArticle(t, configOf(t, defaultList, article, ""), page)
	equal(t, result.Title, "T")
	equal(t, result.Body, []string{"One", "Two"})
	equal(t, result.Blocks[1], Block{Type: "image", URL: "https://t.example/a.jpg", Caption: "Cap"})
}

func TestAVideoIsReadBeforeRemoveAndNeedsAStreamOrEmbed(t *testing.T) {
	ld := `{"@type":"VideoObject","thumbnailUrl":"/p.jpg","contentUrl":"https://cdn.example/v.mp4","duration":"PT1M5S"}`
	script := `"selector": "script[type=application/ld+json]", "attr": "html"`
	article := `{"title": {"selector": "h1"}, "body": {"selector": "p"}, "remove": ["script"], "video": {
		"poster": {` + script + `, "transforms": [{"type": "regex", "pattern": "\"thumbnailUrl\":\"([^\"]+)\""}]},
		"stream": {` + script + `, "transforms": [{"type": "regex", "pattern": "\"contentUrl\":\"([^\"]+\\.mp4)\""}]},
		"duration": {` + script + `, "transforms": [{"type": "regex", "pattern": "\"duration\":\"([^\"]+)\""}]}}}`
	c := configOf(t, defaultList, article, "")
	result := mustArticle(t, c, `<script type="application/ld+json">`+ld+`</script><h1>T</h1><p>x</p>`)
	equal(t, result.Video, &ArticleVideo{Poster: "https://t.example/p.jpg", Stream: "https://cdn.example/v.mp4", Duration: "PT1M5S"})
	equal(t, mustArticle(t, c, "<h1>T</h1><p>x</p>").Video, (*ArticleVideo)(nil))
}

func TestAMissingTitleFails(t *testing.T) {
	_, err := ParseArticle(configOf(t, defaultList, defaultArticle, ""), "<p>body only</p>", "https://t.example/a")
	var scrapeErr *ScrapeError
	if !errors.As(err, &scrapeErr) {
		t.Fatalf("%v", err)
	}
}
