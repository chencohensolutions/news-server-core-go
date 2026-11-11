package summary

import (
	"fmt"
	"strings"
	"testing"
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
