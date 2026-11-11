package summary

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/chencohensolutions/news-server-core-go/store"
)

// Article bodies in the prompt. What the sites' bodies hold (measured on the Mac's saved articles, Nov 2026): a
// median of about 2,000 characters; a paywall stub of about 120 at some sites; promotion and "more on
// this" lines at the end of some; one site's "•" bullets about other news; live blogs of 10,000 and more.
const (
	fullArticleChars = 4000 // of a body when full articles are on: a live blog would fill the request
	minBodyChars     = 200  // a body shorter than this is a paywall stub or a caption: the title and subtitle say more
	sharedChars      = 80   // a paragraph this long that an earlier report of the story has word for word is sent once
	promoChars       = 160  // a promotion line is short; a paragraph longer than this is the article's
)

// endMarkers start the part of a body after the article: what follows is links and other stories.
var endMarkers = []string{"עוד בנושא", "קראו עוד", "כתבות נוספות", "פורסמה במקור", "הכתבה פורסמה במקור", "פורסם לראשונה"}

// promoMarkers are a promotion line's words: a short paragraph with one is dropped.
var promoMarkers = []string{"לחצו כאן", "הצטרפו ל", "להאזנה", "האזינו", "הירשמו", "להורדת האפליקציה", "עקבו אחרינו", "לערוץ הטלגרם", "לקבוצת הווטסאפ"}

// articleText is the body as the prompt shows it: without the lines that aren't the article, without paragraphs an
// earlier report of the same story already sent (seen), and cut to max characters at a paragraph's end (at a word's
// end when the first paragraph is longer). Empty when what is left is a paywall stub.
func articleText(body []string, seen map[string]bool, max int) string {
	var kept []string
	for _, p := range body {
		p = oneLine(p)
		if p == "" {
			continue
		}
		if startsWithAny(p, endMarkers) {
			break
		}
		if strings.HasPrefix(p, "•") || len([]rune(p)) <= promoChars && containsAny(p, promoMarkers) {
			continue
		}
		if len([]rune(p)) >= sharedChars {
			if seen[p] {
				continue
			}
			seen[p] = true
		}
		kept = append(kept, p)
	}
	text := strings.Join(kept, " ")
	if len([]rune(text)) < minBodyChars {
		return ""
	}
	if len([]rune(text)) <= max {
		return text
	}
	// At the last paragraph's end that fits, unless that leaves less than half.
	var b strings.Builder
	for _, p := range kept {
		if b.Len() > 0 && len([]rune(b.String()))+1+len([]rune(p)) > max {
			break
		}
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(p)
	}
	if n := len([]rune(b.String())); n <= max && n >= max/2 {
		return b.String() + " …"
	}
	return cut(text, max)
}

func startsWithAny(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func containsAny(s string, parts []string) bool {
	for _, p := range parts {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// Settings are the summaries' settings that the app changes.
type Settings struct {
	// FullArticles sends each report's whole body (cleaned, up to 4,000 characters) instead of its first 600.
	FullArticles bool `json:"fullArticles"`
	// Length is how long a story is written: "short", "medium", "long" or "full" (lengths).
	Length string `json:"length"`
}

// DefaultSettings are the settings before the app changes them.
var DefaultSettings = Settings{FullArticles: true, Length: "medium"}

// storyLength is a story's sentences at a length: from min to max, and up to newMax new ones in an update; perRequest
// stories in one request, fewer for longer stories, since a long answer is where the model cuts sentences short.
type storyLength struct{ min, max, newMax, perRequest int }

var lengths = map[string]storyLength{
	"short":  {2, 6, 4, maxStories},
	"medium": {3, 10, 6, 8},
	"long":   {5, 15, 8, 5},
	// full has no most: as many sentences as the facts take. Few stories a request, so the answer isn't cut.
	"full": {2, 0, 0, 4},
}

// Lengths are the lengths Settings.Length takes, shortest first.
var Lengths = []string{"short", "medium", "long", "full"}

// lengthOf is length's sentences; an unknown one is medium.
func lengthOf(length string) storyLength {
	if l, ok := lengths[length]; ok {
		return l
	}
	return lengths["medium"]
}

const settingsKey = "summaries"

// LoadSettings is the settings the app saved, or DefaultSettings.
func LoadSettings(ctx context.Context, db *store.Store) (Settings, error) {
	settings := DefaultSettings
	data, ok, err := db.ServerSetting(ctx, settingsKey)
	if err != nil || !ok {
		return settings, err
	}
	err = json.Unmarshal(data, &settings)
	if _, ok := lengths[settings.Length]; !ok {
		settings.Length = DefaultSettings.Length
	}
	return settings, err
}

// SaveSettings keeps the settings: the next round's summaries follow them.
func SaveSettings(ctx context.Context, db *store.Store, settings Settings) error {
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return db.SaveServerSetting(ctx, settingsKey, data)
}
