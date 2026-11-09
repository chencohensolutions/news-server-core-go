package scrape

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
)

// FixtureEntry is one entry of a source's fixtures.json.
type FixtureEntry struct {
	Kind     string `json:"kind"` // list, feed or article
	URL      string `json:"url"`
	HTML     string `json:"html"`
	Expected string `json:"expected"`
}

// FixtureResult is one fixture's outcome; Failure is "" when it passed.
type FixtureResult struct {
	SourceDir string
	Entry     *FixtureEntry
	Failure   string
}

// SourceDirs are the folders under root that hold a config.json, sorted by name.
func SourceDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(root, e.Name(), "config.json")); err == nil && e.IsDir() {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// LoadConfig reads and validates dir/config.json.
func LoadConfig(dir string) (*SourceConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil, err
	}
	return ParseConfig(data)
}

// RunFixtures runs a source folder's golden fixtures (FixtureRunner.run).
func RunFixtures(dir string) []FixtureResult {
	config, err := LoadConfig(dir)
	if err != nil {
		return []FixtureResult{{SourceDir: dir, Failure: err.Error()}}
	}
	data, err := os.ReadFile(filepath.Join(dir, "fixtures.json"))
	if err != nil {
		return []FixtureResult{{SourceDir: dir, Failure: "missing fixtures.json"}}
	}
	var entries []FixtureEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return []FixtureResult{{SourceDir: dir, Failure: "fixtures.json: " + err.Error()}}
	}
	hasList, hasArticle := false, false
	for _, e := range entries {
		hasList = hasList || e.Kind == "list"
		hasArticle = hasArticle || e.Kind == "article"
	}
	if !hasList || !hasArticle {
		return []FixtureResult{{SourceDir: dir, Failure: "fixtures.json needs at least one list and one article fixture"}}
	}
	var out []FixtureResult
	for i := range entries {
		e := &entries[i]
		out = append(out, FixtureResult{SourceDir: dir, Entry: e, Failure: checkFixture(config, dir, e)})
	}
	return out
}

func checkFixture(c *SourceConfig, dir string, e *FixtureEntry) string {
	page, err := os.ReadFile(filepath.Join(dir, e.HTML))
	if err != nil {
		return "missing " + e.HTML
	}
	expectedData, err := os.ReadFile(filepath.Join(dir, e.Expected))
	if err != nil {
		return "missing " + e.Expected
	}
	actual, err := Extract(c, e.Kind, string(page), e.URL)
	if err != nil {
		return "extraction failed: " + err.Error()
	}
	var want, got any
	if err := json.Unmarshal(expectedData, &want); err != nil {
		return e.Expected + ": " + err.Error()
	}
	gotData, _ := json.Marshal(actual)
	json.Unmarshal(gotData, &got)
	if reflect.DeepEqual(want, got) {
		return ""
	}
	return fmt.Sprintf("output differs from %s\n%s", e.Expected, diff(want, got, ""))
}

// Extract reads a page of the given fixture kind: a list ([]ArticleSummary), a feed (the same, read with the
// config's feed) or an article (*Article).
func Extract(c *SourceConfig, kind, page, url string) (any, error) {
	switch kind {
	case "list":
		items, err := ParseList(c, page, url)
		return emptyIfNil(items), err
	case "feed":
		items, err := ParseList(c.WithFeed(), page, url)
		return emptyIfNil(items), err
	case "article":
		return ParseArticle(c, page, url)
	}
	return nil, fmt.Errorf("unknown fixture kind %q", kind)
}

func emptyIfNil(items []ArticleSummary) []ArticleSummary {
	if items == nil {
		return []ArticleSummary{}
	}
	return items
}

// diff describes where two JSON values first differ, path by path, for a readable test failure.
func diff(want, got any, path string) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: want %v\n  got %v", path, short(want), short(got))
		}
		keys := map[string]bool{}
		for k := range w {
			keys[k] = true
		}
		for k := range g {
			keys[k] = true
		}
		var sorted []string
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		var out string
		for _, k := range sorted {
			if !reflect.DeepEqual(w[k], g[k]) {
				out += diff(w[k], g[k], path+"."+k)
			}
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok {
			return fmt.Sprintf("%s: want %v\n  got %v\n", path, short(want), short(got))
		}
		var out string
		for i := 0; i < len(w) || i < len(g); i++ {
			var wi, gi any
			if i < len(w) {
				wi = w[i]
			}
			if i < len(g) {
				gi = g[i]
			}
			if !reflect.DeepEqual(wi, gi) {
				out += diff(wi, gi, fmt.Sprintf("%s[%d]", path, i))
			}
		}
		return out
	}
	return fmt.Sprintf("%s:\n  want %v\n  got  %v\n", path, short(want), short(got))
}

func short(v any) string {
	s := fmt.Sprintf("%v", v)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
