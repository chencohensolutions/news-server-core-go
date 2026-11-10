package stories

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
)

// The app's StoryTextTest (the parts about grouping; the filling from articles is scrape's) and StoryOrderTest.

func textItem(url, summary, subtitle string) scrape.ArticleSummary {
	return scrape.ArticleSummary{SourceID: "s", URL: url, Title: "כותרת " + url, Summary: summary, Subtitle: subtitle}
}

func TestStoriesAreGroupedBySubtitleThenSummaryThenTitle(t *testing.T) {
	for _, c := range []struct {
		item scrape.ArticleSummary
		want string
	}{
		{textItem("a", "תקציר", "כותרת משנה"), "כותרת משנה"},
		{textItem("a", "תקציר", ""), "תקציר"},
		{textItem("a", "", ""), "כותרת a"},
	} {
		if got := storyText(c.item); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func TestEachSettingEmbedsItsOwnText(t *testing.T) {
	full := textItem("a", "תקציר", "משנה")
	for text, want := range map[StoryText][]string{
		Title:              {"כותרת a"},
		Subtitle:           {"משנה"},
		TitleAndSubtitle:   {"כותרת a. משנה"},
		TitleTimesSubtitle: {"כותרת a", "משנה"},
	} {
		if got := text.Texts(full); !reflect.DeepEqual(got, want) {
			t.Errorf("%v: got %q, want %q", text, got, want)
		}
	}
	// Nothing but a title: no ". " after it.
	if got := TitleAndSubtitle.Texts(textItem("b", "", "")); !reflect.DeepEqual(got, []string{"כותרת b"}) {
		t.Errorf("got %q", got)
	}
	// Only the title needs no article.
	if !Title.IsFinal(textItem("b", "", "")) || TitleAndSubtitle.IsFinal(textItem("b", "תקציר", "")) || !TitleAndSubtitle.IsFinal(full) {
		t.Error("IsFinal")
	}
}

func near32(a, b, eps float32) bool { return float32(math.Abs(float64(a-b))) <= eps }

func TestTheProductMultipliesTheTitlesAndTheSubtitlesSimilarities(t *testing.T) {
	text := TitleTimesSubtitle
	a := text.Vector([][]float32{{1, 0}, {0.6, 0.8}})
	b := text.Vector([][]float32{{0.8, 0.6}, {0.6, 0.8}})
	// Titles 0.8 alike, subtitles the same: 0.8 × 1.
	if s := text.Similarity()(a, b); !near32(s, 0.8, 1e-5) {
		t.Errorf("product %v", s)
	}
	// The others compare one vector by its dot product.
	if s := Title.Similarity()([]float32{1, 0}, []float32{0.8, 0.6}); !near32(s, 0.8, 1e-5) {
		t.Errorf("dot %v", s)
	}
	for _, model := range Models {
		for _, text := range StoryTexts {
			s := model.Scale(text)
			if !(s.Strict > s.Balanced && s.Balanced > s.Loose && s.Loose > s.Floor()) {
				t.Errorf("%s %v: %+v", model.ID, text, s)
			}
		}
	}
}

func TestTheSliderPlacesTheOldChoicesAlikeInEveryScale(t *testing.T) {
	for _, model := range Models {
		for _, text := range StoryTexts {
			s := model.Scale(text)
			if !near32(s.Threshold(70), s.Strict, 1e-4) || !near32(s.Threshold(50), s.Balanced, 1e-4) || !near32(s.Threshold(30), s.Loose, 1e-4) {
				t.Errorf("%s %v", model.ID, text)
			}
			// The graph keeps every pair the slider's loosest end can group.
			if !(s.Floor() < s.Threshold(StrictnessMin)) || !(s.Threshold(StrictnessMax) > s.Strict) {
				t.Errorf("%s %v: ends", model.ID, text)
			}
		}
	}
}

func timed(site, url, at string) scrape.ArticleSummary {
	return scrape.ArticleSummary{SourceID: site, URL: url, Title: url, PublishedAt: at}
}

func leads(stories []Story) []string {
	var out []string
	for _, s := range stories {
		out = append(out, s.Lead().URL)
	}
	return out
}

func TestNewestStoryFirstAndUntimedLastInFeedOrder(t *testing.T) {
	old := Story{[]scrape.ArticleSummary{timed("s", "a", "2026-10-30T06:00:00Z"), timed("s", "b", "2026-10-30T07:00:00Z")}}
	recent := Story{[]scrape.ArticleSummary{timed("s", "c", ""), timed("s", "d", "2026-10-30T09:00:00Z")}}
	untimed1 := Story{[]scrape.ArticleSummary{timed("s", "e", ""), timed("s", "f", "")}}
	untimed2 := Story{[]scrape.ArticleSummary{timed("s", "g", ""), timed("s", "h", "")}}
	var feed []scrape.ArticleSummary
	for _, url := range []string{"g", "h", "e", "f"} {
		feed = append(feed, timed("s", url, ""))
	}
	got := leads(NewestFirst([]Story{untimed1, old, untimed2, recent}, feed))
	if want := []string{"d", "b", "g", "e"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTheNewestReportLeadsAndTheRestFollowNewestFirst(t *testing.T) {
	story := Story{[]scrape.ArticleSummary{timed("s", "w", "2026-10-30T05:00:00Z"), timed("s", "x", ""), timed("s", "y", "2026-10-30T06:00:00Z"), timed("s", "z", "2026-10-30T08:00:00Z")}}
	var got []string
	for _, r := range NewestFirst([]Story{story}, nil)[0].Reports {
		got = append(got, r.URL)
	}
	if want := []string{"z", "y", "w", "x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestOneSitesStoriesGoByThatSitesReports(t *testing.T) {
	// Story a: siteh's report is old, but another site reported it since. Story b: siteh's report is newer.
	a := Story{[]scrape.ArticleSummary{timed("sitee", "a1", "2026-10-30T09:00:00Z"), timed("siteh", "a2", "2026-10-30T05:00:00Z")}}
	b := Story{[]scrape.ArticleSummary{timed("siteh", "b1", "2026-10-30T07:00:00Z")}}
	if got := leads(LatestFirst([]Story{b, a}, nil, nil)); !reflect.DeepEqual(got, []string{"a1", "b1"}) {
		t.Errorf("all sites: %v", got)
	}
	siteh := func(s scrape.ArticleSummary) bool { return s.SourceID == "siteh" }
	if got := leads(LatestFirst([]Story{a, b}, nil, siteh)); !reflect.DeepEqual(got, []string{"b1", "a1"}) {
		t.Errorf("siteh: %v", got)
	}
}

func TestStoriesAreGroupedFromTheLastDaysOnly(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	items := []scrape.ArticleSummary{timed("s", "new", "2026-11-02T12:00:00Z"), timed("s", "old", "2026-10-20T12:00:00Z"), timed("s", "untimed", "")}
	var got []string
	for _, item := range WithinDays(items, 7, now) {
		got = append(got, item.URL)
	}
	if !reflect.DeepEqual(got, []string{"new", "untimed"}) {
		t.Errorf("got %v", got)
	}
	if !reflect.DeepEqual(WithinDays(items, 0, now), items) {
		t.Error("0 days keeps all")
	}
}
