package summary

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// recorded answers Generate with recorded answers: the incremental requests with answers, one a request in order
// (the last again after it), and every other request through live (the stories' recorded answers). It keeps the
// incremental requests.
type recorded struct {
	mu       sync.Mutex
	live     Generator
	answers  []string
	errs     []error // the incremental requests' errors, in order; nil answers
	requests []gemini.Request
}

func (r *recorded) Generate(ctx context.Context, req gemini.Request) (json.RawMessage, gemini.Usage, error) {
	if req.System != incrementalSystem {
		return r.live.Generate(ctx, req)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.requests)
	r.requests = append(r.requests, req)
	usage := gemini.Usage{PromptTokens: 500, AnswerTokens: 80, ThoughtsTokens: 10}
	if n < len(r.errs) && r.errs[n] != nil {
		if r.errs[n] == gemini.ErrCut {
			return json.RawMessage(r.answers[min(n, len(r.answers)-1)]), usage, r.errs[n]
		}
		return nil, gemini.Usage{}, r.errs[n]
	}
	return json.RawMessage(r.answers[min(n, len(r.answers)-1)]), usage, nil
}

var (
	rainSiteg = scrape.ArticleSummary{SourceID: "siteg", URL: "https://siteg/rain", Title: "גשם בגליל"}
	rainSited = scrape.ArticleSummary{SourceID: "sited", URL: "https://sited/rain", Title: "הצפות בצפון"}
)

// rainStory is a written story of two reports and three sentences.
func rainStory() StorySummary {
	return StorySummary{ID: "rain", Title: "גשם כבד בצפון", WrittenAt: now.Add(-time.Hour),
		Sentences: []Sentence{
			{"גשם כבד ירד הבוקר בצפון.", []string{rainSiteh.URL, rainSiteE.URL}},
			{"2 כבישים נסגרו בגלל הגשם.", []string{rainSiteE.URL}},
			{"ראש העיר אמר שהכבישים ייפתחו מחר.", []string{rainSiteh.URL}},
		},
		Contradictions: []Sentence{},
		Reports:        []string{rainSiteh.URL, rainSiteE.URL},
		Updates:        []Update{{At: now.Add(-time.Hour), Sentences: []Sentence{{"גשם כבד ירד הבוקר בצפון.", []string{rainSiteh.URL, rainSiteE.URL}}}}},
	}
}

// askedOf is prev's story with reports as a request asks about it: s1, its new articles a1, a2, … in order.
func askedOf(t *testing.T, prev StorySummary, reports ...scrape.ArticleSummary) (*asked, *sources) {
	t.Helper()
	step := Step{Story: stories.Story{Reports: reports}, Prev: prev}
	p := plan(step, DefaultSettings, false)
	if p.Kind != PlanIncremental {
		t.Fatalf("plan %+v", p)
	}
	ids := newSources()
	for _, r := range p.New {
		ids.id(r.URL)
	}
	return &asked{step: step, plan: p, shown: map[string]string{}}, ids
}

func texts(sentences []Sentence) string {
	var out []string
	for _, s := range sentences {
		out = append(out, s.Text)
	}
	return strings.Join(out, " | ")
}

func TestAnUpdateKeepsTheOldTextAndAddsWhatIsNew(t *testing.T) {
	prev := rainStory()
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg, rainSited)
	m := merge(a, Answer{Story: "s1",
		Insert: []AnswerInsert{
			{After: "top", Kind: "development", Text: "נחל הקישון עלה על גדותיו [a1].", Sources: []string{"a1", "a2"}},
			{After: "s1.2", Kind: "reaction", Text: "המשטרה קראה לנהגים להימנע מנסיעה.", Sources: []string{"[a2]"}},
		},
		Cite:  []AnswerCite{{Sentence: "s1.1", Sources: []string{"a1"}}, {Sentence: "s1.1", Sources: []string{"a1"}}},
		Title: "הקישון עלה על גדותיו",
	}, "s1", ids, now)
	if !m.Version {
		t.Fatal("a development makes a version")
	}
	want := "נחל הקישון עלה על גדותיו. | גשם כבד ירד הבוקר בצפון. | 2 כבישים נסגרו בגלל הגשם. | המשטרה קראה לנהגים להימנע מנסיעה. | ראש העיר אמר שהכבישים ייפתחו מחר."
	if got := texts(m.Story.Sentences); got != want {
		t.Fatalf("sentences\n got %s\nwant %s", got, want)
	}
	// The old sentences are the version before's, to the letter; the cited one has the new article among its
	// footnotes, once.
	if got := m.Story.Sentences[1]; strings.Join(got.Sources, " ") != rainSiteh.URL+" "+rainSiteE.URL+" "+rainSiteg.URL {
		t.Fatalf("footnotes %v", got.Sources)
	}
	if got := m.Story.Sentences[2]; got.Text != prev.Sentences[1].Text || strings.Join(got.Sources, " ") != rainSiteE.URL {
		t.Fatalf("%+v", got)
	}
	// What's new is what was inserted, with its kinds; the story keeps its id and its earlier updates.
	last := m.Story.Updates[len(m.Story.Updates)-1]
	if len(m.Story.Updates) != 2 || !last.At.Equal(now) || texts(last.Sentences) != "נחל הקישון עלה על גדותיו. | המשטרה קראה לנהגים להימנע מנסיעה." ||
		strings.Join(last.Kinds, " ") != "development reaction" || strings.Join(last.Sentences[0].Sources, " ") != rainSiteg.URL+" "+rainSited.URL {
		t.Fatalf("updates %+v", m.Story.Updates)
	}
	if m.Story.ID != "rain" || m.Story.Title != "הקישון עלה על גדותיו" || !m.Story.WrittenAt.Equal(now) || len(m.Story.Reports) != 4 {
		t.Fatalf("%+v", m.Story)
	}
	if m.Stats != (MergeStats{Asked: 2, Inserted: 2, Cited: 1}) {
		t.Fatalf("%+v", m.Stats)
	}
}

func TestAnAnswerWithoutNewsMakesNoVersion(t *testing.T) {
	prev := rainStory()
	for name, answer := range map[string]Answer{
		"nothing":        {Story: "s1"},
		"only citations": {Cite: []AnswerCite{{Sentence: "s1.1", Sources: []string{"a1"}}}},
		// Background and a reaction alone are no news; nor a detail without a number.
		"background and a reaction": {Title: "כותרת אחרת", Delete: []string{"s1.3"}, Insert: []AnswerInsert{
			{After: "top", Kind: "background", Text: "בשנה שעברה הוצף האזור פעמיים.", Sources: []string{"a1"}},
			{After: "top", Kind: "reaction", Text: "המשטרה קראה לנהגים להימנע מנסיעה.", Sources: []string{"a1"}},
			{After: "s1.1", Kind: "detail", Text: "הגשם החל לפנות בוקר.", Sources: []string{"a1"}}}},
		// A sentence must cite a new article of this story: not an old one, not one the request doesn't have.
		"no new source": {Insert: []AnswerInsert{
			{After: "top", Kind: "development", Text: "נחל הקישון עלה על גדותיו.", Sources: []string{"a7"}},
			{After: "top", Kind: "development", Text: "נחל הקישון עלה על גדותיו.", Sources: []string{}}}},
		// What the story says already, in other words, is not new: the old sentence stays.
		"restatements": {Insert: []AnswerInsert{
			{After: "top", Kind: "development", Text: "בצפון ירד הבוקר גשם כבד.", Sources: []string{"a1"}}},
			Replace: []AnswerReplace{{Sentence: "s1.3", Kind: "correction", Text: "ראש העיר אמר שמחר ייפתחו הכבישים.", Sources: []string{"a1"}}}},
		// Sentence numbers the story doesn't have, another story's too; and a sentence cut short.
		"unknown sentences": {Replace: []AnswerReplace{
			{Sentence: "s1.9", Kind: "correction", Text: "5 כבישים נסגרו בגלל הגשם.", Sources: []string{"a1"}},
			{Sentence: "s2.1", Kind: "correction", Text: "5 כבישים נסגרו בגלל הגשם.", Sources: []string{"a1"}}},
			Insert: []AnswerInsert{{After: "top", Kind: "development", Text: "נחל הקישון עלה על", Sources: []string{"a1"}}}},
	} {
		a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
		m := merge(a, answer, "s1", ids, now)
		if m.Version {
			t.Errorf("%s: a version: %+v", name, m.Story.Updates)
		}
		// The text, title and time are the version before's; the new article is among the story's reports.
		if texts(m.Story.Sentences) != texts(prev.Sentences) || m.Story.Title != prev.Title || !m.Story.WrittenAt.Equal(prev.WrittenAt) ||
			len(m.Story.Updates) != 1 || len(m.Story.Reports) != 3 {
			t.Errorf("%s: %+v", name, m.Story)
		}
		want := map[string]MergeStats{
			"nothing":                   {},
			"only citations":            {Cited: 1},
			"background and a reaction": {Asked: 3, Unapplied: 3},
			"no new source":             {Asked: 2, Unsourced: 2},
			"restatements":              {Asked: 2, Restated: 2},
			"unknown sentences":         {Asked: 3, Unknown: 2, Garbled: 1},
		}[name]
		if m.Stats != want {
			t.Errorf("%s: %+v, want %+v", name, m.Stats, want)
		}
		if name == "only citations" && len(m.Story.Sentences[0].Sources) != 3 {
			t.Errorf("the cited sentence's footnotes: %v", m.Story.Sentences[0].Sources)
		}
	}
}

func TestACorrectionReplacesASentence(t *testing.T) {
	prev := rainStory()
	a, ids := askedOf(t, prev, rainSiteh, rainSiteE, rainSiteg)
	m := merge(a, Answer{Story: "s1",
		Replace: []AnswerReplace{
			// A changed number is news, however alike the rest is; so is a name the story didn't have.
			{Sentence: "s1.2", Kind: "correction", Text: "3 כבישים נסגרו בגלל הגשם.", Sources: []string{"a1"}},
			{Sentence: "2", Kind: "correction", Text: "4 כבישים נסגרו בגלל הגשם.", Sources: []string{"a1"}},
		},
		Insert: []AnswerInsert{
			{After: "s1.3", Kind: "detail", Text: "ראש העיר לוי אמר שהכבישים ייפתחו מחר.", Sources: []string{"a1"}},
			// The same again in the one answer is dropped.
			{After: "top", Kind: "detail", Text: "ראש העיר לוי אמר שהכבישים ייפתחו מחר.", Sources: []string{"a1"}},
		},
		// A sentence replaced isn't deleted too; an unknown one isn't there to delete.
		Delete: []string{"s1.2", "s1.1", "s1.8"},
	}, "s1", ids, now)
	want := "3 כבישים נסגרו בגלל הגשם. | ראש העיר אמר שהכבישים ייפתחו מחר. | ראש העיר לוי אמר שהכבישים ייפתחו מחר."
	if got := texts(m.Story.Sentences); !m.Version || got != want {
		t.Fatalf("version %v\n got %s\nwant %s", m.Version, got, want)
	}
	// The replaced sentence stands on the new article alone: the old ones stated the old number.
	if got := m.Story.Sentences[0].Sources; len(got) != 1 || got[0] != rainSiteg.URL {
		t.Fatalf("sources %v", got)
	}
	if m.Stats != (MergeStats{Asked: 4, Replaced: 1, Inserted: 1, Deleted: 1, Repeated: 2, Unknown: 1}) {
		t.Fatalf("%+v", m.Stats)
	}
	if kinds := m.Story.Updates[len(m.Story.Updates)-1].Kinds; strings.Join(kinds, " ") != "correction detail" {
		t.Fatalf("kinds %v", kinds)
	}
}

func TestADetailWithANewNumberMakesAVersion(t *testing.T) {
	a, ids := askedOf(t, rainStory(), rainSiteh, rainSiteE, rainSiteg)
	m := merge(a, Answer{Insert: []AnswerInsert{{After: "s1.1", Kind: "detail", Text: "בחיפה ירדו 80 מילימטרים.", Sources: []string{"a1"}}}}, "s1", ids, now)
	if !m.Version || m.Stats.Inserted != 1 {
		t.Fatalf("%+v", m)
	}
	// An insert whose place is no sentence of the story leads.
	m = merge(a, Answer{Insert: []AnswerInsert{{After: "s4.1", Kind: "development", Text: "נחל הקישון עלה על גדותיו.", Sources: []string{"a1"}}}}, "s1", ids, now)
	if !m.Version || m.Stats.Unanchored != 1 || m.Story.Sentences[0].Text != "נחל הקישון עלה על גדותיו." {
		t.Fatalf("%+v", m)
	}
}

func TestAnUnrelatedNewArticleIsNoSource(t *testing.T) {
	a, ids := askedOf(t, rainStory(), rainSiteh, rainSiteE, rainSiteg, rainSited)
	m := merge(a, Answer{
		Unrelated: []answerUnrelated{{ID: "a1", Event: "שריפה בדרום"}, {ID: "a2", Event: ""}},
		Insert: []AnswerInsert{
			{After: "top", Kind: "development", Text: "שריפה פרצה בדרום.", Sources: []string{"a1"}},
			{After: "top", Kind: "development", Text: "נחל הקישון עלה על גדותיו.", Sources: []string{"a1", "a2"}}},
		Cite: []AnswerCite{{Sentence: "s1.1", Sources: []string{"a1"}}},
	}, "s1", ids, now)
	// An article about another event is out of the story; one with no event named stays.
	if strings.Join(m.Unrelated, " ") != rainSiteg.URL || strings.Join(m.Story.Unrelated, " ") != rainSiteg.URL ||
		strings.Join(m.Story.Reports, " ") != rainSiteh.URL+" "+rainSiteE.URL+" "+rainSited.URL {
		t.Fatalf("%+v", m.Story)
	}
	if !m.Version || m.Stats != (MergeStats{Asked: 2, Unsourced: 1, Inserted: 1}) || strings.Join(m.Story.Sentences[0].Sources, " ") != rainSited.URL {
		t.Fatalf("%+v %+v", m.Stats, m.Story.Sentences[0])
	}
}

func TestANewArticleContradictsTheStory(t *testing.T) {
	a, ids := askedOf(t, rainStory(), rainSiteh, rainSiteE, rainSiteg)
	side := func(value string, sources ...string) struct {
		Value   string   `json:"value"`
		Sources []string `json:"sources"`
	} {
		return struct {
			Value   string   `json:"value"`
			Sources []string `json:"sources"`
		}{value, sources}
	}
	contradiction := func(text string, sides ...struct {
		Value   string   `json:"value"`
		Sources []string `json:"sources"`
	}) answerContradiction {
		return answerContradiction{Text: text, Sides: sides}
	}
	m := merge(a, Answer{Contradictions: []answerContradiction{
		contradiction("לפי SiteE נסגרו 2 כבישים, ולפי siteg 3.", side("2", "s1.2"), side("3", "a1")),
		// No new article on any side; and both sides one value.
		contradiction("לפי siteh ולפי SiteE.", side("2", "s1.2"), side("3", "s1.3")),
		contradiction("לפי SiteE ולפי siteg נסגרו 2 כבישים.", side("2", "s1.2"), side("2", "a1")),
	}}, "s1", ids, now)
	if m.Version || len(m.Story.Contradictions) != 1 || strings.Join(m.Story.Contradictions[0].Sources, " ") != rainSiteE.URL+" "+rainSiteg.URL {
		t.Fatalf("%+v", m.Story.Contradictions)
	}
}

func TestASentenceThatSaysTheStoryAgainIsARestatement(t *testing.T) {
	index := indexOf(rainStory().Sentences)
	for text, want := range map[string]bool{
		"בצפון ירד הבוקר גשם כבד.":                true,  // the same words
		"גשם כבד ירד בצפון.":                      true,  // fewer of them
		"2 כבישים נסגרו בגלל הגשם הכבד.":          true,  // and a word the story has elsewhere
		"3 כבישים נסגרו בגלל הגשם.":               false, // a changed number
		"ראש העיר לוי אמר שהכבישים ייפתחו מחר.":   false, // a name the story lacks
		"נחל הקישון עלה על גדותיו.":               false,
		"100 כבישים נסגרו בגלל הגשם.":             false,
		"ראש העירייה אמר שהכבישים ייפתחו מחר.":    true, // an inflection
		"ראש העיר אמר שהכבישים ייפתחו בעוד 1000.": false,
	} {
		if got := index.restates(text); got != want {
			t.Errorf("%q: restates %v, want %v", text, got, want)
		}
	}
	// Numbers are compared whole: 100 is not 1000.
	if sameWord("100", "1000") || !sameWord("כביש", "והכבישים") {
		t.Fatal("sameWord")
	}
}

func TestAChangedStoryIsUpdatedKeptOrWrittenWhole(t *testing.T) {
	prev := rainStory()
	story := func(reports ...scrape.ArticleSummary) Step {
		return Step{Story: stories.Story{Reports: reports}, Prev: prev}
	}
	more := func(n int) []scrape.ArticleSummary {
		out := []scrape.ArticleSummary{rainSiteh, rainSiteE}
		for i := range n {
			out = append(out, scrape.ArticleSummary{SourceID: "siteg", URL: "https://siteg/rain/" + string(rune('a'+i))})
		}
		return out
	}
	long := prev
	for range 20 {
		long.Sentences = append(long.Sentences, prev.Sentences[0])
	}
	// A story that stood on one site, mostly.
	thin := prev
	thin.Sentences = []Sentence{prev.Sentences[2], prev.Sentences[1], prev.Sentences[2]}
	for name, c := range map[string]struct {
		step   Step
		merged bool
		kind   string
		reason string
	}{
		"a new article":         {story(rainSiteh, rainSiteE, rainSiteg), false, PlanIncremental, ""},
		"only regrouped":        {Step{Story: stories.Story{Reports: []scrape.ArticleSummary{rainSiteE, rainSiteh}}, Prev: StorySummary{Sentences: prev.Sentences, Reports: append([]string{rainSited.URL}, prev.Reports...)}}, false, PlanLocal, ""},
		"a source left":         {story(rainSiteE, rainSiteg), false, PlanRebuild, "a sentence lost its sources"},
		"its sources left":      {Step{Story: stories.Story{Reports: []scrape.ArticleSummary{rainSiteE}}, Prev: thin}, false, PlanRebuild, "its sources left the story"},
		"merged":                {story(rainSiteh, rainSiteE, rainSiteg), true, PlanRebuild, "two written stories became one"},
		"doubled":               {story(more(3)...), false, PlanRebuild, "twice the articles of its last whole writing"},
		"grown, but not double": {Step{Story: stories.Story{Reports: more(3)}, Prev: prev, Full: 4}, false, PlanIncremental, ""},
		"too long":              {Step{Story: stories.Story{Reports: more(1)}, Prev: long}, false, PlanRebuild, "past its sentences"},
	} {
		p := plan(c.step, DefaultSettings, c.merged)
		if p.Kind != c.kind || p.Reason != c.reason {
			t.Errorf("%s: %s %q, want %s %q", name, p.Kind, p.Reason, c.kind, c.reason)
		}
	}
	// A story that lost a source no sentence stood on alone keeps its text, without that footnote.
	p := plan(story(rainSiteE), DefaultSettings, false)
	if p.Dropped != 1 || len(p.Kept) != 2 || strings.Join(p.Gone, " ") != rainSiteh.URL || len(p.Kept[0].Sources) != 1 {
		t.Fatalf("%+v", p)
	}
	only := prev
	only.Sentences = prev.Sentences[:2]
	step := Step{Story: stories.Story{Reports: []scrape.ArticleSummary{rainSiteE}}, Prev: only}
	if p := plan(step, DefaultSettings, false); p.Kind != PlanLocal {
		t.Fatalf("%+v", p)
	} else if kept := local(step, p); texts(kept.Sentences) != texts(only.Sentences) || strings.Join(kept.Sentences[0].Sources, " ") != rainSiteE.URL ||
		strings.Join(kept.Reports, " ") != rainSiteE.URL || !kept.WrittenAt.Equal(prev.WrittenAt) {
		t.Fatalf("%+v", kept)
	}
	// Another written story whose reports are in this one: the two became one.
	written := map[string]StorySummary{
		"k1": {ID: "rain", Reports: []string{rainSiteh.URL, rainSiteE.URL}},
		"k2": {ID: "flood", Reports: []string{rainSiteg.URL, rainSited.URL}},
		"k3": {Reports: []string{rainSiteh.URL, rainSiteE.URL}}, // from before ids: maybe this story's own
	}
	both := stories.Story{Reports: []scrape.ArticleSummary{rainSiteh, rainSiteE, rainSiteg, rainSited}}
	if !mergedInto(both, "rain", written) || mergedInto(stories.Story{Reports: both.Reports[:3]}, "rain", written) {
		t.Fatal("mergedInto")
	}
}

func TestTheSchemaGoesOutInItsOrder(t *testing.T) {
	// As the client sends it: a Go map of the schema would go out with its keys sorted.
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"candidates": [{"content": {"parts": [{"text": "{\"stories\": [{\"story\": \"s1\", \"title\": \"\"}, {\"story\": \"s2\", \"ti"}]}, "finishReason": "MAX_TOKENS"}]}`))
	}))
	defer server.Close()
	client := &gemini.Client{Key: "test-key", BaseURL: server.URL}
	raw, _, err := client.Generate(context.Background(), gemini.Request{System: incrementalSystem, Prompt: "x", Schema: incrementalSchema})
	at := -1
	for _, key := range []string{`"responseJsonSchema":`, `"story":`, `"unrelated":`, `"insert":`, `"replace":`, `"delete":`, `"cite":`, `"title":`, `"contradictions":`} {
		next := strings.Index(string(body), key)
		if next <= at {
			t.Fatalf("%s is out of order in %s", key, body)
		}
		at = next
	}
	if strings.Contains(string(body), "propertyOrdering") {
		t.Fatal("propertyOrdering is Gemini 2.0's: not sent")
	}
	// An answer that stopped early comes back with its error, and its whole stories are read.
	if !errors.Is(err, gemini.ErrCut) || err.Error() != "gemini: the answer stopped early (MAX_TOKENS)" {
		t.Fatalf("%v", err)
	}
	if answers, whole := readAnswers(raw); whole || len(answers) != 1 || answers[0].Story != "s1" {
		t.Fatalf("%+v %v", answers, whole)
	}
	// The whole writing's instructions tell Gemini not to stand on the version before: these are apart from them.
	if strings.Contains(incrementalSystem, "Don't base the story on the previous version") || !strings.Contains(incrementalSystem, "When unsure, it belongs") {
		t.Fatal("the instructions")
	}
}

func TestACutAnswerGivesItsWholeStories(t *testing.T) {
	whole := `{"stories": [{"story": "s1", "unrelated": [], "insert": [], "replace": [], "delete": [], "cite": [], "title": "", "contradictions": []},
		{"story": "s2", "unrelated": [], "insert": [{"after": "top", "kind": "development", "text": "נחל הקישון עלה על גדותיו.", "sources": ["a2"]}], "replace": [], "delete": [], "cite": [], "title": "", "contradictions": []}]}`
	answers, ok := readAnswers([]byte(whole))
	if !ok || len(answers) != 2 || answers[1].Insert[0].Kind != "development" {
		t.Fatalf("%+v %v", answers, ok)
	}
	// Cut in the second story: the first stands.
	answers, ok = readAnswers([]byte(whole[:strings.Index(whole, "נחל")+10]))
	if ok || len(answers) != 1 || answers[0].Story != "s1" {
		t.Fatalf("%+v %v", answers, ok)
	}
	if answers, ok := readAnswers([]byte(`{"error": "x"`)); ok || len(answers) != 0 {
		t.Fatalf("%+v %v", answers, ok)
	}
}

// news is an article's body that is clearly news to the rain story: sentences and numbers it doesn't have.
var news = []string{
	"מפלס הכנרת עלה ב-14 סנטימטרים במהלך הלילה האחרון.",
	"רשות המים דיווחה כי 37 תחנות מדידה רשמו שיא עונתי.",
	"בקיבוץ דן פונו 120 תושבים מבתיהם לאחר הצפה.",
	"כוחות הצלה חילצו שלושה מטיילים שנתקעו בנחל עמוד.",
	"שירות המטאורולוגי צופה כי הסערה תימשך עד יום חמישי.",
	"חברת החשמל מסרה כי 5400 בתים נותרו ללא חשמל באזור.",
}

func TestAStoryGeminiFoundNothingInIsDoubted(t *testing.T) {
	w, _, _ := setup(t)
	ctx := context.Background()
	if err := w.Store.SaveArticle(ctx, &scrape.Article{SourceID: "siteg", URL: rainSiteg.URL, Title: rainSiteg.Title, Body: news}, "", now); err != nil {
		t.Fatal(err)
	}
	step := Step{Story: stories.Story{Reports: []scrape.ArticleSummary{rainSiteh, rainSiteE, rainSiteg}}, Prev: rainStory()}
	p := plan(step, DefaultSettings, false)
	ids := newSources()
	_, a := w.storyText(ctx, ids, "s1", step, p, DefaultSettings)
	quiet := merge(a, Answer{Story: "s1"}, "s1", ids, now)
	if why := w.tripwire(ctx, a, quiet); !strings.Contains(why, "6 of an article's 6 sentences") {
		t.Fatalf("the tripwire: %q", why)
	}
	// Not when Gemini found news in it, nor when it found the article to be about another event.
	if why := w.tripwire(ctx, a, merge(a, Answer{Insert: []AnswerInsert{{After: "top", Kind: "development", Text: "מפלס הכנרת עלה.", Sources: []string{"a1"}}}}, "s1", ids, now)); why != "" {
		t.Fatal(why)
	}
	if why := w.tripwire(ctx, a, merge(a, Answer{Unrelated: []answerUnrelated{{ID: "a1", Event: "מפלס הכנרת"}}}, "s1", ids, now)); why != "" {
		t.Fatal(why)
	}
	// Nor for an article that says what the story's articles said: the rain article again, at another address.
	again := scrape.ArticleSummary{SourceID: "siteg", URL: "https://siteg/again", Title: rainSiteh.Title}
	if err := w.Store.SaveArticle(ctx, &scrape.Article{SourceID: "siteg", URL: again.URL, Body: []string{"גשם כבד ירד הבוקר.", "שני כבישים נסגרו.", "עוד משפט קצר על הגשם הכבד שירד הבוקר בצפון הארץ ועל שני הכבישים שנסגרו בגללו, ועוד מילים כדי שהגוף לא יהיה קצר מדי ויישלח: גשם כבד ירד הבוקר, שני כבישים נסגרו, גשם כבד ירד הבוקר, שני כבישים נסגרו, גשם כבד ירד הבוקר."}}, "", now); err != nil {
		t.Fatal(err)
	}
	step.Story.Reports[2] = again
	p = plan(step, DefaultSettings, false)
	ids = newSources()
	_, a = w.storyText(ctx, ids, "s1", step, p, DefaultSettings)
	if why := w.tripwire(ctx, a, merge(a, Answer{Story: "s1"}, "s1", ids, now)); why != "" {
		t.Fatal(why)
	}
}
