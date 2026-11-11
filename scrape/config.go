// Package scrape is the Go port of the news app's scraper engine (the news repo's scraper/engine): site configs
// (config.go), reading pages with them (extract.go), dates and URLs. The spec is the news repo's
// docs/source-config.md, and its golden fixtures are the test: this engine is correct when it gives the same
// output as the Kotlin one on every saved page.
package scrape

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // the configs name zones such as Asia/Jerusalem; Windows has no zone database of its own

	"github.com/chencohensolutions/news-server-core-go/scrape/dom"
	"github.com/chencohensolutions/news-server-core-go/scrape/jsonpath"
	"github.com/chencohensolutions/news-server-core-go/scrape/rx"
)

// SchemaVersion is the config schema this engine reads.
const SchemaVersion = 1

// SourceConfig is sources/<id>/config.json.
type SourceConfig struct {
	SchemaVersion int           `json:"schemaVersion"`
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	BaseURL       string        `json:"baseUrl"`
	Language      string        `json:"language"`
	HTTP          HTTPConfig    `json:"http"`
	List          *ListConfig   `json:"list"`
	Feed          *ListConfig   `json:"feed"`
	Article       ArticleConfig `json:"article"`
	Categories    Categories    `json:"categories"`
}

// WithFeed is this config reading its headlines from Feed instead of List; itself when it has no feed.
func (c *SourceConfig) WithFeed() *SourceConfig {
	if c.Feed == nil {
		return c
	}
	copy := *c
	copy.List = c.Feed
	return &copy
}

type HTTPConfig struct {
	UserAgent string            `json:"userAgent"`
	Headers   map[string]string `json:"headers"`
	// Extra holds the keys the core doesn't know, for the packs' transports to read (fetch.Site.Extra). Unlike the
	// rest of a config, unknown keys here are not errors, since packs define them.
	Extra map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON reads userAgent and headers, and keeps every other key in Extra.
func (h *HTTPConfig) UnmarshalJSON(data []byte) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	*h = HTTPConfig{}
	if raw, ok := keys["userAgent"]; ok {
		if err := json.Unmarshal(raw, &h.UserAgent); err != nil {
			return fmt.Errorf("http.userAgent: %w", err)
		}
		delete(keys, "userAgent")
	}
	if raw, ok := keys["headers"]; ok {
		if err := json.Unmarshal(raw, &h.Headers); err != nil {
			return fmt.Errorf("http.headers: %w", err)
		}
		delete(keys, "headers")
	}
	if len(keys) > 0 {
		h.Extra = keys
	}
	return nil
}

type ListConfig struct {
	URLs        []string      `json:"urls"`
	Sections    []ListSection `json:"sections"`
	Item        *string       `json:"item"`
	JSON        *JSONItems    `json:"json"`
	URL         *FieldSpec    `json:"url"`
	Title       *FieldSpec    `json:"title"`
	Summary     *FieldSpec    `json:"summary"`
	Image       *FieldSpec    `json:"image"`
	PublishedAt *FieldSpec    `json:"publishedAt"`
	Category    *FieldSpec    `json:"category"`
}

type ListSection struct {
	URL      string `json:"url"`
	Category string `json:"category"`
}

type JSONItems struct {
	Selector string `json:"selector"`
	Items    string `json:"items"`
}

type ArticleConfig struct {
	Remove      []string     `json:"remove"`
	Title       *FieldSpec   `json:"title"`
	Subtitle    *FieldSpec   `json:"subtitle"`
	Author      *FieldSpec   `json:"author"`
	PublishedAt *FieldSpec   `json:"publishedAt"`
	Image       *FieldSpec   `json:"image"`
	Category    *FieldSpec   `json:"category"`
	Body        *FieldSpec   `json:"body"`
	Figures     *FigureSpec  `json:"figures"`
	Content     *JSONContent `json:"content"`
	Video       *VideoSpec   `json:"video"`
}

type FigureSpec struct {
	Selector string     `json:"selector"`
	Image    *FieldSpec `json:"image"`
	Caption  *FieldSpec `json:"caption"`
}

type JSONContent struct {
	Selector string `json:"selector"`
	Path     string `json:"path"`
}

type VideoSpec struct {
	Poster   *FieldSpec `json:"poster"`
	Stream   *FieldSpec `json:"stream"`
	Embed    *FieldSpec `json:"embed"`
	Duration *FieldSpec `json:"duration"`
}

const (
	AttrText    = "text"
	AttrOwnText = "ownText"
	AttrHTML    = "html"
)

// FieldSpec is how to read one value.
type FieldSpec struct {
	Selector   *string     `json:"selector"`
	Path       *string     `json:"path"`
	Attr       *string     `json:"attr"`
	Transforms []Transform `json:"transforms"`
	Date       *DateSpec   `json:"date"`
	Fallback   *FieldSpec  `json:"fallback"`
}

func (f *FieldSpec) attr() string {
	if f.Attr == nil {
		return AttrText
	}
	return *f.Attr
}

const (
	FormatISO          = "iso"
	FormatEpochSeconds = "epochSeconds"
	FormatEpochMillis  = "epochMillis"
)

type DateSpec struct {
	Format   string `json:"format"`
	Timezone string `json:"timezone"`
}

func (d *DateSpec) withDefaults() DateSpec {
	out := DateSpec{Format: FormatISO, Timezone: "UTC"}
	if d != nil {
		if d.Format != "" {
			out.Format = d.Format
		}
		if d.Timezone != "" {
			out.Timezone = d.Timezone
		}
	}
	return out
}

// Transform is {"type": "regex", "pattern", "group"} or {"type": "replace", "pattern", "with"}.
type Transform struct {
	Type    string
	Pattern string
	Group   int
	With    string
}

func (t *Transform) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var kind string
	if err := json.Unmarshal(raw["type"], &kind); err != nil || kind == "" {
		return fmt.Errorf("transform needs a type")
	}
	allowed := map[string][]string{"regex": {"type", "pattern", "group"}, "replace": {"type", "pattern", "with"}}[kind]
	if allowed == nil {
		return fmt.Errorf("unknown transform type '%s'", kind)
	}
	for k := range raw {
		if !contains(allowed, k) {
			return fmt.Errorf("unknown key '%s' in a %s transform", k, kind)
		}
	}
	if _, ok := raw["pattern"]; !ok {
		return fmt.Errorf("%s transform needs a pattern", kind)
	}
	t.Type, t.Group = kind, 1
	if err := json.Unmarshal(raw["pattern"], &t.Pattern); err != nil {
		return err
	}
	if g, ok := raw["group"]; ok {
		if err := json.Unmarshal(g, &t.Group); err != nil {
			return err
		}
	}
	if w, ok := raw["with"]; ok {
		if err := json.Unmarshal(w, &t.With); err != nil {
			return err
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Categories maps shared category ids to patterns, in the order written: the first id with a matching pattern wins.
type Categories struct {
	IDs      []string
	Patterns map[string][]string
}

func (c *Categories) UnmarshalJSON(data []byte) error {
	v, err := jsonpath.Parse(string(data))
	if err != nil {
		return err
	}
	if v.Kind != jsonpath.Object {
		return fmt.Errorf("categories must be an object")
	}
	c.Patterns = map[string][]string{}
	for _, id := range v.Keys {
		list := v.Members[id]
		if list.Kind != jsonpath.Array {
			return fmt.Errorf("categories.%s must be a list of patterns", id)
		}
		var patterns []string
		for _, p := range list.Items {
			if p.Kind != jsonpath.String {
				return fmt.Errorf("categories.%s must be a list of patterns", id)
			}
			patterns = append(patterns, p.Literal)
		}
		c.IDs = append(c.IDs, id)
		c.Patterns[id] = patterns
	}
	return nil
}

// CategoryIDs are the shared categories (core/model Categories.kt in the news repo).
var CategoryIDs = []string{"israel", "politics", "security", "world", "economy", "law", "opinion", "sport", "culture",
	"tech", "health", "lifestyle"}

// InvalidConfigError lists what is wrong with a config.
type InvalidConfigError struct{ Errors []string }

func (e *InvalidConfigError) Error() string {
	return "Invalid source config:\n  - " + strings.Join(e.Errors, "\n  - ")
}

// ParseConfig parses and validates a config, as SourceConfigs.parse: unknown keys are errors, since a misspelled
// key would otherwise be ignored.
func ParseConfig(data []byte) (*SourceConfig, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c SourceConfig
	if err := dec.Decode(&c); err != nil {
		return nil, &InvalidConfigError{[]string{err.Error()}}
	}
	if errs := Validate(&c); len(errs) > 0 {
		return nil, &InvalidConfigError{errs}
	}
	return &c, nil
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Validate is SourceConfigs.validate, plus the required keys kotlinx checks while decoding.
func Validate(c *SourceConfig) []string {
	var v validator
	if c.SchemaVersion != SchemaVersion {
		v.add("schemaVersion %d is not supported (expected %d)", c.SchemaVersion, SchemaVersion)
	}
	if !idPattern.MatchString(c.ID) {
		v.add("id '%s' must match [a-z0-9][a-z0-9_-]*", c.ID)
	}
	if c.Name == "" {
		v.add("name is required")
	}
	if c.Language == "" {
		v.add("language is required")
	}
	if !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
		v.add("baseUrl must be an absolute http(s) URL")
	}
	for _, id := range c.Categories.IDs {
		if !contains(CategoryIDs, id) {
			v.add("categories: unknown category '%s' (expected one of %s)", id, strings.Join(CategoryIDs, ", "))
		}
		for _, p := range c.Categories.Patterns[id] {
			if _, err := rx.Compile(p); err != nil {
				v.add("categories.%s: invalid regex '%s'", id, p)
			}
		}
	}
	if c.List == nil {
		v.add("list is required")
	} else {
		v.list("list", c.List)
	}
	if c.Feed != nil {
		v.list("feed", c.Feed)
	}
	a := &c.Article
	for i, s := range a.Remove {
		v.selector(fmt.Sprintf("article.remove[%d]", i), s)
	}
	v.required("article.title", a.Title, false)
	v.optional("article.subtitle", a.Subtitle, false)
	v.optional("article.author", a.Author, false)
	v.optional("article.category", a.Category, false)
	v.optional("article.publishedAt", a.PublishedAt, false)
	v.optional("article.image", a.Image, false)
	v.required("article.body", a.Body, false)
	if f := a.Figures; f != nil {
		if a.Body == nil || a.Body.Selector == nil {
			v.add("article.figures needs article.body to have a selector")
		}
		v.selector("article.figures.selector", f.Selector)
		v.required("article.figures.image", f.Image, false)
		v.optional("article.figures.caption", f.Caption, false)
	}
	if ct := a.Content; ct != nil {
		v.selector("article.content.selector", ct.Selector)
		v.path("article.content.path", ct.Path)
	}
	if vid := a.Video; vid != nil {
		if vid.Stream == nil && vid.Embed == nil {
			v.add("article.video needs stream or embed")
		}
		v.optional("article.video.poster", vid.Poster, false)
		v.optional("article.video.stream", vid.Stream, false)
		v.optional("article.video.embed", vid.Embed, false)
		v.optional("article.video.duration", vid.Duration, false)
	}
	return v.errors
}

type validator struct{ errors []string }

func (v *validator) add(format string, args ...any) {
	v.errors = append(v.errors, fmt.Sprintf(format, args...))
}

func (v *validator) list(name string, l *ListConfig) {
	if len(l.URLs) == 0 && len(l.Sections) == 0 {
		v.add("%s.urls must not be empty", name)
	}
	for i, s := range l.Sections {
		if strings.TrimSpace(s.URL) == "" {
			v.add("%s.sections[%d].url must not be empty", name, i)
		}
	}
	if (l.Item == nil) == (l.JSON == nil) {
		v.add("%s must have exactly one of 'item' and 'json'", name)
	}
	if l.Item != nil {
		v.selector(name+".item", *l.Item)
	}
	if l.JSON != nil {
		v.selector(name+".json.selector", l.JSON.Selector)
		v.path(name+".json.items", l.JSON.Items)
	}
	inJSON := l.JSON != nil
	v.required(name+".url", l.URL, inJSON)
	v.required(name+".title", l.Title, inJSON)
	v.optional(name+".summary", l.Summary, inJSON)
	v.optional(name+".image", l.Image, inJSON)
	v.optional(name+".publishedAt", l.PublishedAt, inJSON)
	v.optional(name+".category", l.Category, inJSON)
}

func (v *validator) required(path string, f *FieldSpec, inJSON bool) {
	if f == nil {
		v.add("%s is required", path)
		return
	}
	v.field(path, f, inJSON)
}

func (v *validator) optional(path string, f *FieldSpec, inJSON bool) {
	if f != nil {
		v.field(path, f, inJSON)
	}
}

func (v *validator) field(path string, f *FieldSpec, inJSON bool) {
	if inJSON {
		if f.Selector != nil || f.attr() != AttrText {
			v.add("%s: use 'path', not 'selector'/'attr', in a JSON list", path)
		}
		if f.Path != nil {
			v.path(path+".path", *f.Path)
		}
	} else {
		if f.Path != nil {
			v.add("%s: 'path' is only valid in a JSON list (list.json)", path)
		}
		if f.Selector != nil {
			v.selector(path+".selector", *f.Selector)
		}
	}
	for i, t := range f.Transforms {
		if _, err := rx.Compile(t.Pattern); err != nil {
			v.add("%s.transforms[%d]: invalid regex '%s'", path, i, t.Pattern)
		}
	}
	if f.Date != nil {
		if _, err := time.LoadLocation(f.Date.withDefaults().Timezone); err != nil {
			v.add("%s.date: unknown timezone '%s'", path, f.Date.Timezone)
		}
	}
	if f.Fallback != nil {
		v.field(path+".fallback", f.Fallback, inJSON)
	}
}

func (v *validator) path(name, p string) {
	if _, err := jsonpath.Compile(p); err != nil {
		v.add("%s: %v", name, err)
	}
}

func (v *validator) selector(name, s string) {
	if _, err := dom.Compile(s); err != nil {
		v.add("%s: invalid CSS selector '%s'", name, s)
	}
}
