package scrape

import (
	"testing"
	"time"
)

// The Kotlin UrlsTest and DatesTest.

func TestResolveURL(t *testing.T) {
	base := "https://site.example/news/world/story?id=1#top"
	for _, c := range []struct{ base, ref, want string }{
		{base, " https://x.example/a ", "https://x.example/a"},
		{base, "//cdn.example/i.jpg", "https://cdn.example/i.jpg"},
		{base, "/a/b", "https://site.example/a/b"},
		{base, "other", "https://site.example/news/world/other"},
		{base, "../tech/./x", "https://site.example/news/tech/x"},
		{base, "?id=2", "https://site.example/news/world/story?id=2"},
		{"https://site.example/x", "../../a", "https://site.example/a"},
		{base, "/a?b=../c", "https://site.example/a?b=../c"},
	} {
		if got := ResolveURL(c.base, c.ref); got != c.want {
			t.Errorf("ResolveURL(%q, %q) = %q, want %q", c.base, c.ref, got, c.want)
		}
	}
}

func TestParseDate(t *testing.T) {
	const jerusalem = "Asia/Jerusalem"
	for _, c := range []struct {
		raw  string
		spec DateSpec
		want string // "" when no date
	}{
		{"2026-10-26T09:15:00+02:00", DateSpec{}, "2026-10-26T07:15:00Z"},
		{"2026-07-01T09:00", DateSpec{Timezone: jerusalem}, "2026-07-01T06:00:00Z"},
		{"Updated: 26.10.26 09:15", DateSpec{Format: "dd.MM.yy HH:mm", Timezone: jerusalem}, "2026-10-26T07:15:00Z"},
		{"2026/3/5", DateSpec{Format: "yyyy/M/d"}, "2026-03-05T00:00:00Z"},
		{"1800000000", DateSpec{Format: FormatEpochSeconds}, time.Unix(1_800_000_000, 0).UTC().Format(time.RFC3339)},
		{"yesterday", DateSpec{Format: "dd.MM.yy HH:mm"}, ""},
		{"31.02.26", DateSpec{Format: "dd.MM.yy"}, ""},
	} {
		got, ok := parseDate(c.raw, (&c.spec).withDefaults())
		var gotText string
		if ok {
			gotText = got.UTC().Format(time.RFC3339)
		}
		if gotText != c.want {
			t.Errorf("parseDate(%q, %+v) = %q, want %q", c.raw, c.spec, gotText, c.want)
		}
	}
}
