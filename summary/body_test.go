package summary

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/stories"
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
	for i := 0; i < 60; i++ {
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

// longBody is a made-up body of about chars characters in paragraphs of about 300, no two alike in any body: name
// and the paragraph's number are in each.
func longBody(name string, chars int) []string {
	var body []string
	for i := 0; len([]rune(strings.Join(body, " "))) < chars; i++ {
		body = append(body, strings.TrimSpace(fmt.Sprintf("פסקה %d בכתבה %s: ", i, name)+strings.Repeat("תיאור מפורט של מהלך האירועים כפי שנמסר לכתבים. ", 6)))
	}
	return body
}

func TestTheLongestBodiesAreShortenedFirst(t *testing.T) {
	// 12,000 characters for bodies of 1,000, 5,000 and 9,000: the two shorter ones stay whole, and the longest
	// gets what is left.
	if got := level([]int{9000, 1000, 5000}, 12_000, 600); got != 6000 {
		t.Fatalf("level %d", got)
	}
	// Bodies that fit are not cut, and none is cut below the least.
	if got := level([]int{9000, 1000, 5000}, 15_000, 600); got != math.MaxInt {
		t.Fatalf("level %d", got)
	}
	if got := level([]int{9000, 9000, 9000}, 900, 600); got != 600 {
		t.Fatalf("level %d", got)
	}

	// A story of 15 articles, 139,000 characters together: past storyBodyChars. Cut evenly, each would keep 8,000.
	articles := map[string]*scrape.Article{}
	var reports []scrape.ArticleSummary
	add := func(n, chars int) {
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("%d-%d", chars, i)
			r := scrape.ArticleSummary{SourceID: "site" + name, URL: "https://site.example/" + name}
			reports = append(reports, r)
			articles[r.URL] = &scrape.Article{Body: longBody(name, chars)}
		}
	}
	add(2, 2000)
	add(6, 8500)
	add(7, 12_500)
	w := &Writer{Articles: func(_ context.Context, url string) *scrape.Article { return articles[url] }}
	bodies := w.bodies(context.Background(), reports, bodyChars(DefaultSettings), map[string]bool{})
	total := 0
	for i, r := range reports {
		body, whole := bodies[r.URL], strings.Join(articles[r.URL].Body, " ")
		n := len([]rune(body))
		total += n
		switch {
		case i < 8 && body != whole:
			// The short and the middle ones are whole: only the longest are shortened.
			t.Errorf("article %d: %d of %d characters", i, n, len([]rune(whole)))
		case i >= 8 && (n < 8600 || n > 9300 || !strings.HasSuffix(body, " …") || !strings.HasPrefix(whole, strings.TrimSuffix(body, " …"))):
			t.Errorf("article %d: %d characters", i, n)
		}
	}
	if total > storyBodyChars+2*len(reports) || total < storyBodyChars*9/10 {
		t.Fatalf("%d characters together", total)
	}
	// A story that fits has every article whole, up to the most an article takes.
	bodies = w.bodies(context.Background(), reports[8:10], bodyChars(DefaultSettings), map[string]bool{})
	for _, r := range reports[8:10] {
		if n := len([]rune(bodies[r.URL])); n > fullArticleChars+2 || n < fullArticleChars*9/10 {
			t.Errorf("%d characters", n)
		}
	}
	// With full articles off, the first 600 characters as before.
	if n := len([]rune(w.bodies(context.Background(), reports[:1], bodyChars(Settings{}), map[string]bool{})[reports[0].URL])); n > articleChars+2 {
		t.Fatalf("%d characters", n)
	}
}

func TestAWholeWritingSendsAnArticleWhole(t *testing.T) {
	w, fake, found := setup(t)
	ctx := context.Background()
	// 9,000 characters: until round 6 an article was cut at 4,000.
	body := longBody("הסערה", 9000)
	if err := w.Store.SaveArticle(ctx, &scrape.Article{SourceID: "sitee", URL: rainSiteE.URL, Title: rainSiteE.Title, Body: body}, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(ctx, found[:1]); err != nil {
		t.Fatal(err)
	}
	if prompt := fake.prompts[0]; !strings.Contains(prompt, "גוף: "+strings.Join(body, " ")+"\n") {
		t.Fatalf("the request lacks the whole article:\n%s", prompt)
	}
}

func TestAnIncrementalRequestSendsNewArticlesWholeAndTakesFewerStories(t *testing.T) {
	// Three stories, each with one new article of 9,000 characters and an old one of the same length.
	articles := map[string]*scrape.Article{}
	var steps []Step
	var plans []Plan
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("story%d", i)
		old := scrape.ArticleSummary{SourceID: "siteh", URL: "https://siteh.example/" + name, Title: "כותרת ישנה " + name}
		added := scrape.ArticleSummary{SourceID: "siteg", URL: "https://siteg.example/" + name, Title: "כותרת חדשה " + name}
		articles[old.URL] = &scrape.Article{Body: longBody("ישנה "+name, 9000)}
		articles[added.URL] = &scrape.Article{Body: longBody("חדשה "+name, 9000)}
		prev := StorySummary{ID: name, Title: "סיפור " + name, WrittenAt: now.Add(-time.Hour), Reports: []string{old.URL},
			Sentences: []Sentence{{"משפט ראשון בסיפור.", []string{old.URL}}, {"משפט שני בסיפור.", []string{old.URL}}}}
		step := Step{Story: stories.Story{Reports: []scrape.ArticleSummary{old, added}}, Prev: prev}
		steps = append(steps, step)
		plans = append(plans, plan(step, DefaultSettings, false))
	}
	w := &Writer{Articles: func(_ context.Context, url string) *scrape.Article { return articles[url] }, MaxChars: 20_000}
	req, took := w.build(context.Background(), steps, plans, 0, DefaultSettings)
	// Two stories fit the request's 20,000 characters, and the third waits for the next: no article was cut to
	// make room for it.
	if took != 2 || len(req.order) != 2 || strings.Contains(req.prompt, "story2") {
		t.Fatalf("%d stories: %v", took, req.order)
	}
	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("story%d", i)
		whole := strings.Join(articles["https://siteg.example/"+name].Body, " ")
		// The new article whole, and the old one by its title alone.
		if !strings.Contains(req.prompt, "כותרת: כותרת חדשה "+name+"\nגוף: "+whole+"\n") {
			t.Errorf("%s: the new article is not whole", name)
		}
		if !strings.Contains(req.prompt, "- siteh: כותרת ישנה "+name+"\n") || strings.Contains(req.prompt, "בכתבה ישנה "+name) {
			t.Errorf("%s: the old article", name)
		}
	}
	// A story alone is asked about whatever its size: the request's size takes fewer stories, never none.
	w.MaxChars = 1000
	if req, took = w.build(context.Background(), steps, plans, 0, DefaultSettings); took != 1 || !strings.Contains(req.prompt, strings.Join(articles["https://siteg.example/story0"].Body, " ")) {
		t.Fatalf("%d stories", took)
	}
	// With full articles off, a new article's first 600 characters, as in a whole writing.
	req, _ = w.build(context.Background(), steps[:1], plans[:1], 0, Settings{Length: "medium"})
	if a := req.asked["s1"]; len([]rune(a.bodies["https://siteg.example/story0"])) > articleChars+2 {
		t.Fatalf("%d characters", len([]rune(a.bodies["https://siteg.example/story0"])))
	}
}
