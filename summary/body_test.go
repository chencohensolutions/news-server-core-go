package summary

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
)

func TestArticleTextDropsWhatIsNotTheArticle(t *testing.T) {
	long := strings.Repeat("הממשלה אישרה הלילה את התקציב לאחר דיון ארוך. ", 3)
	body := []string{
		long,
		"לחצו כאן להצטרפות לקבוצת הווטסאפ שלנו",
		"• הבורסה ירדה היום ב-2%",
		"שר האוצר אמר כי התקציב יעבור בכנסת עד סוף החודש, והאופוזיציה הודיעה שתתנגד.",
		"עוד בנושא:",
		"כותרת של כתבה אחרת לגמרי",
	}
	got := articleText(body, map[string]bool{}, fullArticleChars)
	for _, gone := range []string{"לחצו כאן", "הבורסה", "עוד בנושא", "כתבה אחרת"} {
		if strings.Contains(got, gone) {
			t.Errorf("kept %q: %s", gone, got)
		}
	}
	if !strings.Contains(got, "שר האוצר אמר") {
		t.Errorf("dropped the article's paragraph: %s", got)
	}
}

func TestArticleTextDropsAPaywallStub(t *testing.T) {
	if got := articleText([]string{"למנויים בלבד: הכתבה המלאה זמינה למנויי האתר."}, map[string]bool{}, fullArticleChars); got != "" {
		t.Errorf("got %q, want no body", got)
	}
}

func TestArticleTextSendsSharedParagraphsOnce(t *testing.T) {
	shared := strings.Repeat("ההודעה המלאה של דובר צה״ל על התקיפה בלבנון הלילה. ", 3)
	own := strings.Repeat("כתבנו מצאו כי מדובר בתקיפה השלישית השבוע. ", 6)
	seen := map[string]bool{}
	first := articleText([]string{shared, own}, seen, fullArticleChars)
	second := articleText([]string{shared, strings.ReplaceAll(own, "השלישית", "הרביעית")}, seen, fullArticleChars)
	if !strings.Contains(first, "דובר צה״ל") {
		t.Errorf("first report lacks the shared paragraph: %s", first)
	}
	if strings.Contains(second, "דובר צה״ל") || !strings.Contains(second, "הרביעית") {
		t.Errorf("second report: %s", second)
	}
}

func TestArticleTextCutsAtAParagraph(t *testing.T) {
	var body []string
	for i := 0; i < 20; i++ {
		body = append(body, fmt.Sprintf("עדכון %d: ", i)+strings.Repeat("פסקה ארוכה בבלוג החי של היום. ", 10))
	}
	got := articleText(body, map[string]bool{}, fullArticleChars)
	if n := len([]rune(got)); n > fullArticleChars+2 || n < fullArticleChars/2 {
		t.Errorf("%d characters", n)
	}
	if !strings.HasSuffix(got, ". …") {
		t.Errorf("not cut at a paragraph's end: …%s", string([]rune(got)[len([]rune(got))-20:]))
	}
}

func TestCheckPlacingRefusesALazyAnswer(t *testing.T) {
	var all, alone []placing
	for i := 0; i < 30; i++ {
		all = append(all, placing{ID: fmt.Sprintf("h%d", i+1), Story: "new1"})
		alone = append(alone, placing{ID: fmt.Sprintf("h%d", i+1), Story: "alone"})
	}
	if err := checkPlacing(30, all); err != nil {
		t.Errorf("a full answer: %v", err)
	}
	if err := checkPlacing(100, all); err == nil {
		t.Error("30 of 100 placed: no error")
	}
	if err := checkPlacing(30, alone); err == nil {
		t.Error("all alone: no error")
	}
	if err := checkPlacing(3, alone[:3]); err != nil {
		t.Errorf("3 alone: %v", err)
	}
}

func TestTheLengthSetsTheSentences(t *testing.T) {
	if got := storiesSystem("medium"); !strings.Contains(got, "3 to 10 sentences") || !strings.Contains(got, "0 to 6 sentences") {
		t.Errorf("medium:\n%s", got)
	}
	if got := storiesSystem("full"); !strings.Contains(got, "as many sentences as it takes") || strings.Contains(got, "%!") {
		t.Errorf("full:\n%s", got)
	}
	if lengthOf("nope") != lengths["medium"] {
		t.Error("an unknown length isn't medium")
	}
}

func TestAStoryWrittenLatelyWaitsForItsNextVersion(t *testing.T) {
	w, fake, found := setup(t)
	ctx := context.Background()
	if err := SaveSettings(ctx, w.Store, Settings{FullArticles: true, Length: "short", RewriteMinutes: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(ctx, found); err != nil {
		t.Fatal(err)
	}
	// Another report of a site already in it, 10 minutes later: held.
	more := scrape.ArticleSummary{SourceID: "siteh", URL: "https://siteh/rain2", Title: "עוד גשם"}
	w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{more}, now)
	found[0].Reports = append(found[0].Reports, more)
	w.Now = func() time.Time { return now.Add(10 * time.Minute) }
	result, err := w.Run(ctx, found)
	if err != nil || result.Held != 1 || fake.count() != 1 {
		t.Fatalf("%+v %v, %d requests", result, err, fake.count())
	}
	// A new site at once, when that's set.
	SaveSettings(ctx, w.Store, Settings{FullArticles: true, Length: "short", RewriteMinutes: 30, RewriteOnNewSite: true})
	if result, _ := w.Run(ctx, found); result.Held != 1 {
		t.Fatalf("same site: %+v", result)
	}
	siteg := scrape.ArticleSummary{SourceID: "siteg", URL: "https://siteg/rain", Title: "גשם"}
	w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{siteg}, now)
	found[0].Reports = append(found[0].Reports, siteg)
	if result, _ := w.Run(ctx, found); result.Held != 0 || fake.count() != 2 {
		t.Fatalf("new site: %+v, %d requests", result, fake.count())
	}
	// Past the reserve: new versions wait, whatever the time.
	SaveSettings(ctx, w.Store, Settings{FullArticles: true, Length: "short", ReserveAfter: 2})
	another := scrape.ArticleSummary{SourceID: "sited", URL: "https://sited/rain", Title: "גשם"}
	w.Store.SaveHeadlines(ctx, []scrape.ArticleSummary{another}, now)
	found[0].Reports = append(found[0].Reports, another)
	w.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if result, _ := w.Run(ctx, found); result.Held != 1 || fake.count() != 2 {
		t.Fatalf("reserve: %+v, %d requests", result, fake.count())
	}
}
