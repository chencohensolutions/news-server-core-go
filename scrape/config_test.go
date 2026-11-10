package scrape

import (
	"errors"
	"strings"
	"testing"
)

// The Kotlin SourceConfigsTest.

func configJSON(listItem, extra, tz string) string {
	return `{
	  "schemaVersion": 1, "id": "ok-site", "name": "Ok", "baseUrl": "https://ok.example", "language": "he",
	  "list": { "urls": ["/"], "item": "` + listItem + `", "url": { "selector": "a", "attr": "href" }, "title": { "selector": "a" } },
	  "article": {
	    "title": { "selector": "h1" ` + extra + ` },
	    "publishedAt": { "selector": "time", "date": { "format": "dd.MM.yy", "timezone": "` + tz + `" } },
	    "body": { "selector": "p", "transforms": [{ "type": "regex", "pattern": "(.*)" }] }
	  }
	}`
}

var okJSON = configJSON(".i", "", "UTC")

// configErrors is what ParseConfig reported about text; it fails when text parses.
func configErrors(t *testing.T, text string) []string {
	t.Helper()
	_, err := ParseConfig([]byte(text))
	var invalid *InvalidConfigError
	if !errors.As(err, &invalid) {
		t.Fatalf("parsed, or failed otherwise: %v", err)
	}
	return invalid.Errors
}

func anyError(errs []string, match func(string) bool) bool {
	for _, e := range errs {
		if match(e) {
			return true
		}
	}
	return false
}

func startsWith(prefix string) func(string) bool {
	return func(s string) bool { return strings.HasPrefix(s, prefix) }
}

func TestAValidConfigParses(t *testing.T) {
	c, err := ParseConfig([]byte(okJSON))
	if err != nil || c.ID != "ok-site" {
		t.Fatal(c, err)
	}
}

func TestAnUnknownKeyIsRejected(t *testing.T) {
	configErrors(t, configJSON(".i", `, "selectr": "h2"`, "UTC"))
}

func TestABadSelectorAndTimezoneAreReported(t *testing.T) {
	errs := configErrors(t, configJSON("div[", "", "Mars/Base"))
	if !anyError(errs, startsWith("list.item")) || !anyError(errs, func(s string) bool { return strings.Contains(s, "timezone") }) {
		t.Fatal(errs)
	}
}

func TestAFeedIsAnAlternativeListValidatedLikeIt(t *testing.T) {
	feed := `"feed": { "urls": ["/rss"], "item": "item", "url": { "selector": "link" }, "title": { "selector": "title" } },`
	c, err := ParseConfig([]byte(strings.Replace(okJSON, `"article":`, feed+` "article":`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	equal(t, c.List.URLs, []string{"/"})
	equal(t, c.WithFeed().List.URLs, []string{"/rss"})
	// Without a feed, reading it reads the list.
	plain, _ := ParseConfig([]byte(okJSON))
	equal(t, plain.WithFeed().List.URLs, []string{"/"})
	broken := strings.Replace(feed, `"item": "item"`, `"item": "item["`, 1)
	if errs := configErrors(t, strings.Replace(okJSON, `"article":`, broken+` "article":`, 1)); !anyError(errs, startsWith("feed.item")) {
		t.Fatal(errs)
	}
}

func TestAListNeedsExactlyOneOfItemAndJSON(t *testing.T) {
	both := strings.Replace(okJSON, `"item": ".i",`, `"item": ".i", "json": { "selector": "script", "items": "$..post" },`, 1)
	if errs := configErrors(t, both); !anyError(errs, func(s string) bool { return strings.Contains(s, "exactly one") }) {
		t.Fatal(errs)
	}
}

func TestJSONListFieldsUsePathsOnly(t *testing.T) {
	text := strings.Replace(okJSON,
		`"item": ".i", "url": { "selector": "a", "attr": "href" }, "title": { "selector": "a" }`,
		`"json": { "selector": "script", "items": "$..post" }, "url": { "path": "$.link" }, `+
			`"title": { "selector": "a" }, "image": { "path": "nope" }`, 1)
	errs := configErrors(t, text)
	if !anyError(errs, func(s string) bool { return strings.HasPrefix(s, "list.title") && strings.Contains(s, "'path'") }) ||
		!anyError(errs, startsWith("list.image.path")) || anyError(errs, startsWith("list.url")) {
		t.Fatal(errs)
	}
}

func TestAPathOutsideAJSONListIsRejected(t *testing.T) {
	if errs := configErrors(t, configJSON(".i", `, "path": "$.t"`, "UTC")); !anyError(errs, startsWith("article.title")) {
		t.Fatal(errs)
	}
}

func TestAnUnknownCategoryIDIsRejected(t *testing.T) {
	text := strings.TrimSuffix(strings.TrimSpace(okJSON), "}") + `, "categories": { "money": ["x"], "economy": ["("] } }`
	errs := configErrors(t, text)
	if !anyError(errs, func(s string) bool { return strings.Contains(s, "unknown category 'money'") }) ||
		!anyError(errs, startsWith("categories.economy")) {
		t.Fatal(errs)
	}
}
