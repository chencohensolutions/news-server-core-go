package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// The incremental update of a story (docs/incremental.md). A story that gained articles is not written again from all
// of them: Gemini is shown the version before as numbered sentences and only the new articles, and answers with what
// they change: sentences to insert, replace or delete, and old sentences the new articles state too. The server
// applies that to the version before, so the old text and its footnotes stay exactly as they were, and what was
// inserted or replaced is what's new. Many stories go in one request. It runs in shadow (shadow.go), in the replay
// (replay.go), and, on, in place of the whole rewrite (on.go).

// Settings.Incremental's values.
const (
	IncrementalOff    = "off"
	IncrementalShadow = "shadow" // beside the whole rewrite, kept in story_shadow (shadow.go)
	IncrementalOn     = "on"     // in place of the whole rewrite of a story that has a version (on.go)
)

// What an inserted or replaced sentence is.
const (
	Development = "development" // something happened or became known since the story was written; an official act
	Correction  = "correction"  // a fact of the story changed or was wrong
	Detail      = "detail"      // a fact about what the story tells already
	Background  = "background"  // what was before the event
	Reaction    = "reaction"    // what someone said about it
)

var kinds = []string{Development, Correction, Detail, Background, Reaction}

// How a changed story gets its next version.
const (
	PlanLocal       = "local"       // no new article: the server keeps the text, without what lost its sources
	PlanIncremental = "incremental" // new articles: Gemini is asked what they change
	PlanRebuild     = "rebuild"     // written again whole, as before
)

const (
	// IncrementalPerRequest is how many stories one incremental request takes, to start with: the replay measures
	// whether the later ones in a request are answered as well as the first.
	IncrementalPerRequest = 8
	// incrementalOutChars is the answer one request is expected to write at most: a longer one is where the model
	// cuts sentences short. An answer is expected to be storyOutChars a story and articleOutChars a new article.
	incrementalOutChars = 12_000
	storyOutChars       = 200
	articleOutChars     = 500
	// minKept is the fewest sentences a story keeps when its sources leave: fewer, or less than half, and it is
	// written again whole.
	minKept = 2
	// growthMin is the fewest articles a story gains before having doubled sends it to be written whole: a story of
	// two that became four is still small.
	growthMin = 3
	// fullSentenceCap is the most sentences a story of the "full" length holds before it is written again whole;
	// the other lengths' is one and a half times their most.
	fullSentenceCap = 30
)

// Step is a story's next version as an update of the one before it.
type Step struct {
	Story stories.Story // the story as it is now
	Prev  StorySummary  // the version before, with its ID
	// Full is how many articles the story had when it was last written whole.
	Full int
	// Forced are articles of the version before shown as new all the same: the replay's control.
	Forced map[string]bool
	// Seen is when the server first saw each report, by URL: the time of an article whose site gives none. nil when
	// it isn't known.
	Seen map[string]time.Time
}

// seenLate is how long before a version an article may have been published and still be after it, when the server
// first saw it only after that version: the version was written from the reading of the sites before it, which such
// an article was not in yet. It is the time between two runs.
const seenLate = 10 * time.Minute

// fresh says r was published after the version before was written; known is false when that can't be told, and
// such an article is taken as published after it (the rule that keeps old articles from making a version must not
// keep news from making one). The time is the site's for the article (PublishedAt); when it gives none, when the
// server first saw it (Seen); with neither, or a version before without a time, it is not known. An article
// published up to seenLate before the version that the server first saw after it is after it too.
func (s Step) fresh(r scrape.ArticleSummary) (fresh, known bool) {
	written := s.Prev.WrittenAt
	if written.IsZero() {
		return false, false
	}
	seen, wasSeen := s.Seen[r.URL]
	published, err := time.Parse(time.RFC3339, r.PublishedAt)
	if err != nil {
		return wasSeen && seen.After(written), wasSeen
	}
	return published.After(written) || wasSeen && seen.After(written) && published.After(written.Add(-seenLate)), true
}

// When a new article was published, as the request says it on the article's first line. The instructions
// (incrementalSystem) quote both.
const (
	publishedBefore = "פורסמה לפני שהסיפור נכתב"
	publishedAfter  = "פורסמה אחרי שהסיפור נכתב"
)

// published is what the request says of when r was published; "" when it isn't known.
func (s Step) published(r scrape.ArticleSummary) string {
	switch fresh, known := s.fresh(r); {
	case !known:
		return ""
	case fresh:
		return publishedAfter
	}
	return publishedBefore
}

// Plan is how a step is made, and what it is made from.
type Plan struct {
	Kind   string // PlanLocal, PlanIncremental or PlanRebuild
	Reason string // why it is written again whole
	// New are the story's reports the version before hasn't seen, Old the ones it has, Gone its reports that left
	// the story.
	New, Old []scrape.ArticleSummary
	Gone     []string
	// Kept are the version's sentences that still have a source in the story, with those sources: sentence n of
	// the request is Kept[n-1]. Dropped is how many lost every source.
	Kept    []Sentence
	Dropped int
}

// seenOf are the articles a version was written with: its reports and the ones it found unrelated.
func seenOf(s StorySummary) map[string]bool {
	seen := map[string]bool{}
	for _, url := range reportsOf(s) {
		seen[url] = true
	}
	for _, url := range s.Unrelated {
		seen[url] = true
	}
	return seen
}

// sentenceCap is the most sentences a story holds before it is written again whole.
func sentenceCap(settings Settings) int {
	if l := lengthOf(settings.Length); l.max > 0 {
		return l.max * 3 / 2
	}
	return fullSentenceCap
}

// plan decides how step's story gets its next version. merged says the story now holds another written story too.
// A story is written again whole when the update would stand on a text that no longer fits it: a sentence lost its
// last source, two written stories became one, the story has twice the articles it was last written whole from, or
// it grew past its sentences.
func plan(step Step, settings Settings, merged bool) Plan {
	var p Plan
	seen := seenOf(step.Prev)
	here := map[string]bool{}
	for _, r := range step.Story.Reports {
		here[r.URL] = true
		if seen[r.URL] && !step.Forced[r.URL] {
			p.Old = append(p.Old, r)
		} else {
			p.New = append(p.New, r)
		}
	}
	for _, url := range reportsOf(step.Prev) {
		if !here[url] {
			p.Gone = append(p.Gone, url)
		}
	}
	for _, s := range step.Prev.Sentences {
		var sources []string
		for _, url := range s.Sources {
			if here[url] {
				sources = append(sources, url)
			}
		}
		if len(sources) == 0 {
			p.Dropped++
			continue
		}
		p.Kept = append(p.Kept, Sentence{Text: s.Text, Sources: sources})
	}
	full := step.Full
	if full == 0 {
		full = len(seen)
	}
	switch {
	case len(p.New) == 0 && (len(p.Kept) < minKept || 2*p.Dropped > len(step.Prev.Sentences)):
		p.Kind, p.Reason = PlanRebuild, "its sources left the story"
	case len(p.New) == 0:
		p.Kind = PlanLocal
	case merged:
		p.Kind, p.Reason = PlanRebuild, "two written stories became one"
	case p.Dropped > 0:
		p.Kind, p.Reason = PlanRebuild, "a sentence lost its sources"
	case len(step.Story.Reports) >= 2*full && len(step.Story.Reports)-full >= growthMin:
		p.Kind, p.Reason = PlanRebuild, "twice the articles of its last whole writing"
	case len(p.Kept) > sentenceCap(settings):
		p.Kind, p.Reason = PlanRebuild, "past its sentences"
	default:
		p.Kind = PlanIncremental
	}
	return p
}

// mergedInto says story holds another written story than id too: one whose latest version has half of its reports
// in it, two at least.
func mergedInto(story stories.Story, id string, written map[string]StorySummary) bool {
	here := map[string]bool{}
	for _, r := range story.Reports {
		here[r.URL] = true
	}
	latest := map[string]StorySummary{}
	for _, s := range written {
		// One written before ids has only its key: an earlier version of this very story, as likely as not.
		if s.ID == "" || s.ID == id {
			continue
		}
		if l, ok := latest[s.ID]; !ok || s.newer(l) {
			latest[s.ID] = s
		}
	}
	for _, s := range latest {
		reports := reportsOf(s)
		shared := 0
		for _, url := range reports {
			if here[url] {
				shared++
			}
		}
		if shared >= 2 && 2*shared >= len(reports) {
			return true
		}
	}
	return false
}

// local is the story of a step without new articles: the version before, without the sentences and footnotes that
// left with their articles. No version: nothing was added.
func local(step Step, p Plan) StorySummary {
	return render(step, p, p.Kept, nil, nil, nil, "", nil, time.Time{})
}

// render is the story a step makes: sentences, with the version before's title, id and updates. With added
// (what's new, and each one's kind) it is a version written at now, under title when that isn't "".
func render(step Step, p Plan, sentences, added []Sentence, addedKinds []string, contradictions []Sentence, title string, unrelated []string, now time.Time) StorySummary {
	prev := step.Prev
	out := StorySummary{Title: prev.Title, Sentences: sentences, WrittenAt: prev.WrittenAt, ID: prev.ID, Written: prev.Written}
	if out.Sentences == nil {
		out.Sentences = []Sentence{}
	}
	gone := map[string]bool{}
	for _, url := range unrelated {
		gone[url] = true
	}
	here := map[string]bool{}
	for _, r := range step.Story.Reports {
		here[r.URL] = true
		if !gone[r.URL] && !contains(prev.Unrelated, r.URL) {
			out.Reports = append(out.Reports, r.URL)
		}
	}
	for _, url := range prev.Unrelated {
		if here[url] {
			out.Unrelated = append(out.Unrelated, url)
		}
	}
	out.Unrelated = append(out.Unrelated, unrelated...)
	for _, url := range prev.HeadlineOnly {
		if here[url] {
			out.HeadlineOnly = append(out.HeadlineOnly, url)
		}
	}
	// A contradiction stays while two of its sources are in the story.
	out.Contradictions = []Sentence{}
	for _, c := range prev.Contradictions {
		var sources []string
		for _, url := range c.Sources {
			if here[url] {
				sources = append(sources, url)
			}
		}
		if len(sources) >= 2 {
			out.Contradictions = append(out.Contradictions, Sentence{Text: c.Text, Sources: sources})
		}
	}
	out.Contradictions = append(out.Contradictions, contradictions...)
	out.Updates = append([]Update{}, prev.Updates...)
	if len(added) > 0 {
		out.WrittenAt = now
		if title != "" {
			out.Title = title
		}
		out.Updates = unfolded(&prev, sentences, added, now)
		out.Updates[len(out.Updates)-1].Kinds = addedKinds
	}
	return out
}

// The instructions are apart from the whole writing's (storiesInstructions): that one tells Gemini not to base the
// story on the version before, and this one that the version before stays.
const incrementalSystem = `You keep the stories of a personal newspaper from Israeli news sites up to date. The input is several stories.
Each story shows its title, the story so far as numbered sentences (after a sentence's number, in brackets, are
the sites it cites: "[מקורות: ...]" is a note for you, and no part of the sentence), the titles of the articles it
was written from, and then its new articles: each with an id in brackets, its site, time, whether it was published
before or after the story so far was written ("פורסמה לפני שהסיפור נכתב" or "פורסמה אחרי שהסיפור נכתב"; nothing
when that isn't known), then its title ("כותרת:"), its subtitle ("משנה:", or "תקציר:", the site's summary of it)
and its body ("גוף:", the whole article or its start).

The story so far is written and stays as it is. You answer only what the new articles change in it. Never write a
sentence of the story again, and never reword one.

For every story answer, in this order:
- "story": its id ("s1").
- "unrelated": the new articles that are clearly about a different event than the story (the grouping can be
  wrong), each as its "id" and the "event" it is about instead, in a few Hebrew words. That event must be another
  incident: another person, place or happening. An article that shares no person, no place and no event with the
  story is unrelated, even though the grouping put it here. An article about the same finding, decision or incident
  in other words, one that teases it without naming it, covers another side of it, or reacts to it belongs to the
  story. Only when an article shares a person, a place or an event with the story and you are unsure, it belongs.
  Don't use unrelated ones anywhere else in the answer. Usually none: then an empty list.
- "insert": a sentence for each fact the new articles state and the story so far does not. Each has "after": the
  number of the sentence it follows ("s1.3"), or "top" for what happened since the story was written, which leads;
  its "kind"; its "text"; and "sources": the ids of the new articles that state it. A fact the story already has,
  in other words too, is not inserted: it goes in "cite".
- "replace": a sentence of the story that a new article corrects or brings up to date (a toll that rose, a name now
  published, a decision that was reversed): its "sentence" number, its "kind", the whole new "text", and "sources":
  the ids of the new articles that state it. Not for rewording, and not for adding another fact: insert that.
- "delete": the numbers of the sentences a new article shows to be no longer true and nothing replaces. Usually none.
- "cite": the sentences of the story whose fact a new article states too: the "sentence" number and "sources", the
  ids of the new articles that state it. So an article that only repeats the story adds sources, not sentences.
- "title": a new short factual Hebrew headline only when what was inserted or replaced makes the title wrong or
  stale; otherwise "" (the title stays).
- "contradictions": only where a new article gives a different value than the story, or than another new article,
  for the same fact: a number, a name, a date, a time or a place. Each one has "sides": every version as its "value"
  and its "sources": the ids of the new articles that say it, or the number of the story's sentence that does. Its
  "text" is one sentence naming each site once and what it says. Usually none: then an empty list.

A sentence's "kind" is one of:
- "development": something that happened or became known since the story was written; a new stage of the event.
  An official act is a "development" too, also when it is made of words: a letter sent, an appeal or a petition
  filed, a decision, an order, a formal statement or announcement by a leader, a minister or an official body.
- "correction": a fact of the story changed or was wrong.
- "detail": a fact about what the story already tells that it lacks: a name, a number, a place, a time, a quote.
  A breakdown of figures the story already sums up is a detail, not a development.
- "background": what was before the event, or context: a preview of the event, the run-up to it.
- "reaction": what someone said about it.
An article published before the story was written reports nothing that happened since: nothing from it is a
"development". A fact of it about the event itself that the story lacks (a name, a number, a sum, an offer made) is
a "detail"; the rest of it is "background".

Account for every new article: the id of each one appears in "unrelated", or among the "sources" of an "insert", a
"replace" or a "cite". Before you decide that an article only repeats the story, read its title ("כותרת:") and its
subtitle ("משנה:" or "תקציר:") first: they state the article's main news. When the story so far does not say what
they state (someone dismissed or appointed, a decision approved, a rift, a result), that is an insert, even when
the body mostly repeats the story. Then check the body for what the story lacks: a new stage or decision, a person
named for the first time, a number (a count, an age, an amount, a date), an official response. Each such fact is an
insert. An article that has none of them repeats the story: cite the sentences it states. Never invent a difference
to have something to answer.

Only this story's event. An article often mentions a related but separate event beside it: another indictment,
another company's results, an earlier incident, a similar case elsewhere. Its facts are not this story's: don't
insert them, and never give this story's event the people, numbers or places of the other one.

Writing rules, all of them strict:
- Write Hebrew only, in the plain, neutral register of a news agency.
- Never write ids or site names in a sentence's "text": sources go only in "sources", as the new articles' ids
  ("a1"), never as a site's name. No "(a1)", no "[מקורות: ...]" and no site in parentheses after a sentence. Only a
  contradiction's "text" names sites.
- Facts only: what happened, who, where, when, numbers, quotes attributed to who said them. No opinions, no
  commentary, no speculation, no adjectives that judge, nothing the new articles don't say.
- One fact a sentence, each fact once. When several new articles state it, cite all of them, one article a site.
- Cite only the ids of the story's own new articles. A fact no new article states is not written.
- Copy names, numbers and nationalities exactly as the article gives them: the same count, the same sum, the same
  spelling of a name. Never round, add up, convert or guess one, and never take one from another story of the input.
- A claim, an accusation, a concern or an assessment is not a fact: write it only attributed to who says it ("לפי
  ...", "X אמר ש..."), or leave it out.
- Short sentences. Every sentence is complete, stands on its own, and ends with a period.
- Write Hebrew abbreviations with the Hebrew marks, as the input does: צה״ל, יו״ר, ג׳ (U+05F4 and U+05F3), never
  with " or '. Write quotes with „ and ”.

Answer for every story in the input, by its id. A story's answer uses only that story's sentence numbers and new
articles.`

// incrementalSchema is the answer's shape, written out so its keys go to Gemini in this order: Gemini writes an
// answer's keys in the schema's order (documented for structured output from Gemini 2.5 on; "propertyOrdering" is
// only for 2.0, so it isn't sent). The story's id comes first and the unrelated articles before anything is written
// from them; a Go map would send the keys sorted, with "unrelated" last.
var incrementalSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "stories": {"type": "array", "items": {
      "type": "object",
      "properties": {
        "story": {"type": "string"},
        "unrelated": {"type": "array", "items": {
          "type": "object",
          "properties": {"id": {"type": "string"}, "event": {"type": "string"}},
          "required": ["id", "event"]
        }},
        "insert": {"type": "array", "items": {
          "type": "object",
          "properties": {
            "after": {"type": "string"},
            "kind": {"type": "string", "enum": ["development", "correction", "detail", "background", "reaction"]},
            "text": {"type": "string"},
            "sources": {"type": "array", "items": {"type": "string"}}
          },
          "required": ["after", "kind", "text", "sources"]
        }},
        "replace": {"type": "array", "items": {
          "type": "object",
          "properties": {
            "sentence": {"type": "string"},
            "kind": {"type": "string", "enum": ["development", "correction", "detail", "background", "reaction"]},
            "text": {"type": "string"},
            "sources": {"type": "array", "items": {"type": "string"}}
          },
          "required": ["sentence", "kind", "text", "sources"]
        }},
        "delete": {"type": "array", "items": {"type": "string"}},
        "cite": {"type": "array", "items": {
          "type": "object",
          "properties": {
            "sentence": {"type": "string"},
            "sources": {"type": "array", "items": {"type": "string"}}
          },
          "required": ["sentence", "sources"]
        }},
        "title": {"type": "string"},
        "contradictions": {"type": "array", "items": {
          "type": "object",
          "properties": {
            "text": {"type": "string"},
            "sides": {"type": "array", "items": {
              "type": "object",
              "properties": {
                "value": {"type": "string"},
                "sources": {"type": "array", "items": {"type": "string"}}
              },
              "required": ["value", "sources"]
            }}
          },
          "required": ["text", "sides"]
        }}
      },
      "required": ["story", "unrelated", "insert", "replace", "delete", "cite", "title", "contradictions"]
    }}
  },
  "required": ["stories"]
}`)

// Answer is what Gemini answers for one story of an incremental request: the operations on the version before.
type Answer struct {
	Story          string                `json:"story"`
	Unrelated      []answerUnrelated     `json:"unrelated"`
	Insert         []AnswerInsert        `json:"insert"`
	Replace        []AnswerReplace       `json:"replace"`
	Delete         []string              `json:"delete"`
	Cite           []AnswerCite          `json:"cite"`
	Title          string                `json:"title"`
	Contradictions []answerContradiction `json:"contradictions"`
}

// AnswerInsert is a new sentence after sentence After ("s1.3"), or at the top ("top").
type AnswerInsert struct {
	After   string   `json:"after"`
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	Sources []string `json:"sources"`
}

// AnswerReplace is a sentence's new text.
type AnswerReplace struct {
	Sentence string   `json:"sentence"`
	Kind     string   `json:"kind"`
	Text     string   `json:"text"`
	Sources  []string `json:"sources"`
}

// AnswerCite are new articles that state a sentence's fact.
type AnswerCite struct {
	Sentence string   `json:"sentence"`
	Sources  []string `json:"sources"`
}

// readAnswers are the stories of an incremental answer. Of one that was cut or isn't whole JSON, the stories that
// are whole (whole is false): the answer goes story by story, so what came before the cut stands.
func readAnswers(raw []byte) (answers []Answer, whole bool) {
	var answer struct {
		Stories []Answer `json:"stories"`
	}
	if json.Unmarshal(raw, &answer) == nil {
		return answer.Stories, true
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for _, want := range []any{json.Delim('{'), "stories", json.Delim('[')} {
		if token, err := dec.Token(); err != nil || token != want {
			return nil, false
		}
	}
	for dec.More() {
		var a Answer
		if dec.Decode(&a) != nil {
			break
		}
		answers = append(answers, a)
	}
	return answers, false
}

// request is one incremental request: its text, and the stories in it by their id there ("s1").
type request struct {
	prompt string
	ids    *sources
	asked  map[string]*asked
	order  []string // the ids, as the request lists them
}

// asked is one story of a request.
type asked struct {
	step  Step
	plan  Plan
	chars int // of the request's text
	// shown is each new article's text as the request showed it, by URL.
	shown map[string]string
	// sent are the new articles the request showed, in its order, and bodies each one's body as it showed it ("" for
	// one shown by its headline alone), by URL: for the replay's cases.
	sent   []scrape.ArticleSummary
	bodies map[string]string
	// names are the names the request showed for the story's sites, by the sites' ids.
	names map[string]string
}

// sourcesLabel marks the sites an old sentence cites in the request: "s1.2 [מקורות: site, site] the sentence". They
// stand before the sentence, labelled, and not after it in parentheses as they once did: Gemini wrote new sentences
// that ended the same way ("... (site)").
const sourcesLabel = "מקורות"

// storyText is one story of an incremental request: its title, the version before as numbered sentences with the
// sites each cites, the titles of the articles it has, and the new articles whole (without the paragraphs an
// article it has already gave, word for word).
func (w *Writer) storyText(ctx context.Context, ids *sources, id string, step Step, p Plan, settings Settings) (string, *asked) {
	a := &asked{step: step, plan: p, shown: map[string]string{}, bodies: map[string]string{}, names: map[string]string{}}
	site := map[string]string{}
	for _, r := range step.Story.Reports {
		site[r.URL] = w.siteName(r.SourceID)
		a.names[r.SourceID] = site[r.URL]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "=== סיפור %s ===\n", id)
	fmt.Fprintf(&b, "כותרת הסיפור: %s\n", oneLine(step.Prev.Title))
	fmt.Fprintf(&b, "הסיפור עד כה (נכתב %s):\n", israelTime(step.Prev.WrittenAt))
	for i, s := range p.Kept {
		var sites []string
		for _, url := range s.Sources {
			if name := site[url]; name != "" && !contains(sites, name) {
				sites = append(sites, name)
			}
		}
		fmt.Fprintf(&b, "%s.%d [%s: %s] %s\n", id, i+1, sourcesLabel, strings.Join(sites, ", "), oneLine(s.Text))
	}
	seen := map[string]bool{}
	if old := shown(stories.Story{Reports: p.Old}); len(old) > 0 {
		b.WriteString("כתבות שכבר בסיפור:\n")
		for _, r := range old {
			fmt.Fprintf(&b, "- %s", w.siteName(r.SourceID))
			if r.PublishedAt != "" {
				fmt.Fprintf(&b, " · %s", r.PublishedAt)
			}
			fmt.Fprintf(&b, ": %s\n", oneLine(r.Title))
		}
	}
	for _, r := range p.Old {
		if article := w.article(ctx, r.URL); article != nil {
			for _, paragraph := range article.Body {
				if paragraph = oneLine(paragraph); len([]rune(paragraph)) >= sharedChars {
					seen[paragraph] = true
				}
			}
		}
	}
	b.WriteString("כתבות חדשות:\n")
	// Each new article whole: a request that would pass its size takes fewer stories (build), and no article is
	// cut to make room for another story.
	added := shown(stories.Story{Reports: p.New})
	bodies := w.bodies(ctx, added, bodyChars(settings), seen)
	for _, r := range added {
		body := bodies[r.URL]
		b.WriteString(w.reportOf(ids, r, body, step.published(r)))
		b.WriteString("\n")
		// As reportOf shows it: the subtitle, or the site's summary when it has none.
		under := r.Subtitle
		if under == "" {
			under = r.Summary
		}
		a.shown[r.URL] = strings.TrimSpace(oneLine(r.Title) + ". " + oneLine(under) + ". " + body)
		a.bodies[r.URL] = body
	}
	a.sent = added
	a.chars = len([]rune(b.String()))
	return b.String(), a
}

// outChars is the answer a story of a request is expected to take.
func outChars(p Plan) int { return storyOutChars + articleOutChars*len(p.New) }

// build makes one request of the first steps that fit it: up to per stories (0 is IncrementalPerRequest), MaxChars
// of text and incrementalOutChars of expected answer; the first always. It returns how many steps it took.
func (w *Writer) build(ctx context.Context, steps []Step, plans []Plan, per int, settings Settings) (*request, int) {
	req := &request{ids: newSources(), asked: map[string]*asked{}}
	limit := orDefault(w.MaxChars, DefaultMaxChars)
	per = orDefault(per, IncrementalPerRequest)
	var prompt strings.Builder
	chars, out := 0, 0
	for i, step := range steps {
		if i == per || i > 0 && out+outChars(plans[i]) > incrementalOutChars {
			break
		}
		// The ids of a story that doesn't fit are not taken: built on a copy.
		ids := &sources{ids: map[string]string{}, urls: map[string]string{}, headlineOnly: map[string]bool{}}
		for id, url := range req.ids.ids {
			ids.ids[id], ids.urls[url] = url, id
		}
		for url := range req.ids.headlineOnly {
			ids.headlineOnly[url] = true
		}
		id := fmt.Sprintf("s%d", i+1)
		text, a := w.storyText(ctx, ids, id, step, plans[i], settings)
		if i > 0 && chars+a.chars > limit {
			break
		}
		prompt.WriteString(text)
		chars += a.chars
		out += outChars(plans[i])
		req.ids = ids
		req.asked[id] = a
		req.order = append(req.order, id)
	}
	req.prompt = prompt.String()
	return req, len(req.order)
}

// gemini is the request as Gemini takes it.
func (r *request) gemini() gemini.Request {
	return gemini.Request{System: incrementalSystem, Prompt: r.prompt, Schema: incrementalSchema}
}

// MergeStats are what the server did with one story's answer.
type MergeStats struct {
	// Asked are the sentences Gemini answered to insert or replace.
	Asked int `json:"asked"`
	// What went into the story (when a version is made), or its footnotes: sentences inserted, replaced, deleted,
	// and old sentences that got a new article as a source.
	Inserted int `json:"inserted"`
	Replaced int `json:"replaced"`
	Deleted  int `json:"deleted"`
	Cited    int `json:"cited"`
	// What was dropped, and why: no new article of the story cited; says again what the story has; says what
	// another sentence of the answer does; on a sentence number the story doesn't have (a deletion or a citation
	// too); cut short or garbled; states a number that none of the articles it cites does (Unbacked).
	Unsourced int `json:"unsourced"`
	Restated  int `json:"restated"`
	Repeated  int `json:"repeated"`
	Unknown   int `json:"unknown"`
	Garbled   int `json:"garbled"`
	Unbacked  int `json:"unbacked"`
	// Worded are the ones of Unbacked whose number is a count of a country's people, in a word or in digits, that
	// no article they cite gives of that people ("שלושה סינים" where it tells of a Chinese tourist and two Japanese).
	Worded int `json:"worded"`
	// Timed are the ones of Restated that say a sentence of the story again with a time word more ("היום", "אמש").
	Timed int `json:"timed"`
	// Unapplied are sound sentences that stayed out of the story because the answer makes no version: Reshown and
	// NotInserted.
	Unapplied int `json:"unapplied"`
	// Quiet are sound sentences that went into the story without a version: every inserted one of an answer that
	// makes none, whatever its kind, but the ones of Reshown and NotInserted.
	Quiet int `json:"quiet"`
	// What goes into no story without a version, among Unapplied: a sentence that stands only on articles the
	// story was written from, shown again (Reshown); a replacement or a correction (NotInserted).
	Reshown     int `json:"reshown"`
	NotInserted int `json:"notInserted"`
	// Adds are sound details that add what makes a detail news: a number the story lacks, a person or a body it
	// doesn't name, an outcome or a decision it doesn't tell. Since are the ones of them with an article published
	// after the version before among their sources: each makes a version. The others go in quietly, or with a
	// version another sentence makes.
	Adds  int `json:"adds"`
	Since int `json:"since"`
	// Of Adds: Named are the ones for a person or a body they name, and no number; Decided the ones for an outcome
	// alone. Dated are details that are not among Adds, though they have a number the story lacks: it is a date
	// or a year.
	Named   int `json:"named"`
	Decided int `json:"decided"`
	Dated   int `json:"dated"`
	// ByDetail is 1 for a version that only details made: no development and no correction of the answer did.
	// They are the versions to read for a thin one.
	ByDetail int `json:"byDetail"`
	// Corrected are inserted sentences that said a sentence of the story again with another number: each became
	// that sentence's replacement, a correction, which is in the story only with a version (NotInserted has the
	// ones that stayed out).
	Corrected int `json:"corrected"`
	// OtherDay are sentences that were made no correction because they name a day the story's sentence doesn't
	// ("אתמול", "ביום שלישי", a date): inserted ones that otherwise said that sentence again with another number,
	// and replacements answered as a correction, each inserted after its sentence as a detail.
	OtherDay int `json:"otherDay"`
	// Unanchored are inserted sentences whose place wasn't a sentence of the story: they lead.
	Unanchored int `json:"unanchored"`
	// Old are sound developments and corrections all of whose articles were published before the version
	// before: they make none. They go in with the version when another sentence makes one, and the inserted
	// ones quietly (Quiet) when none does.
	Old int `json:"old"`
}

// add adds o's counts to s's.
func (s *MergeStats) add(o MergeStats) {
	s.Asked += o.Asked
	s.Inserted += o.Inserted
	s.Replaced += o.Replaced
	s.Deleted += o.Deleted
	s.Cited += o.Cited
	s.Unsourced += o.Unsourced
	s.Restated += o.Restated
	s.Repeated += o.Repeated
	s.Unknown += o.Unknown
	s.Garbled += o.Garbled
	s.Unbacked += o.Unbacked
	s.Worded += o.Worded
	s.Timed += o.Timed
	s.Unapplied += o.Unapplied
	s.Quiet += o.Quiet
	s.Reshown += o.Reshown
	s.NotInserted += o.NotInserted
	s.Adds += o.Adds
	s.Since += o.Since
	s.Named += o.Named
	s.Decided += o.Decided
	s.ByDetail += o.ByDetail
	s.Dated += o.Dated
	s.Corrected += o.Corrected
	s.OtherDay += o.OtherDay
	s.Unanchored += o.Unanchored
	s.Old += o.Old
}

// Merged is a story's answer applied to the version before.
type Merged struct {
	// Story is the story after the step. Without a version it is the version before, with the new articles among
	// its reports and, where they state a sentence's fact, its footnotes; and with the sentences that went in
	// quietly (Stats.Quiet), which change the text and nothing else: not the title, the time or the updates.
	Story StorySummary
	// Version says the answer makes a new version: what was inserted and replaced is its latest update.
	Version bool
	// Unrelated are the new articles Gemini found to be about another event.
	Unrelated []string
	// Unmentioned are the new articles the request showed that the answer names nowhere: not as unrelated, and not
	// among the sources of an inserted or replaced sentence, a citation or a contradiction. Skipped says that is
	// every one of them: Gemini skipped the story, which is not an answer that it has nothing new.
	Unmentioned []string
	Skipped     bool
	// Used are the new articles a sentence that went into the story cites, with a version or quietly: the ones
	// the answer added something from.
	Used  []string
	Stats MergeStats
	// Notes are what became of each operation of the answer, in the order the rules read them: the replaced
	// sentences, the inserted ones, the citations, the deletions.
	Notes []MergeNote
}

// What became of an operation of an answer (MergeNote.Outcome).
const (
	NoteApplied = "applied"  // in the version; of a citation, a footnote the sentence got
	NoteQuiet   = "quiet"    // in the story without a version
	NoteLeftOut = "left out" // sound, and out of the story because the answer makes no version: Why says which rule
	NoteDropped = "dropped"  // dropped by a rule: Why says which
)

// MergeNote is what the server did with one operation of a story's answer: the replay's cases show them, so a
// sentence that was dropped can be read beside the rule that dropped it.
type MergeNote struct {
	Op string `json:"op"` // "insert", "replace", "delete" or "cite"
	// At is the sentence the operation named, as answered: "s1.3", or "top" of an inserted one.
	At   string `json:"at,omitempty"`
	Kind string `json:"kind,omitempty"` // of an inserted or replaced sentence: as answered, or as read when it is sound
	Text string `json:"text,omitempty"`
	// Sources are the ids of the articles it cited, as answered.
	Sources []string `json:"sources,omitempty"`
	Outcome string   `json:"outcome"`
	// Why it was dropped or left out, in words.
	Why string `json:"why,omitempty"`
	// MakesVersion says a sound sentence is one the version rule takes: a development, a correction, or a detail
	// that adds what makes one news (Adds), with an article published after the version before among its sources.
	MakesVersion bool `json:"makesVersion,omitempty"`
	// Adds says a sound detail adds what makes a detail news, and News what: NewsNumber, NewsActor or NewsOutcome.
	// Name is the person or the body, when that is it. From an article published after the version before it
	// makes a version (MakesVersion); from older ones it goes in quietly. Dated says a detail is not one that adds
	// though it has a number the story lacks: a date or a year.
	Adds  bool   `json:"adds,omitempty"`
	News  string `json:"news,omitempty"`
	Name  string `json:"name,omitempty"`
	Dated bool   `json:"dated,omitempty"`
	// Old says a sound development or correction cites only articles published before the version before: it
	// makes no version.
	Old bool `json:"old,omitempty"`
	// Corrects is the sentence of the story ("s1.2") an inserted sentence said again with another number: it was
	// made that sentence's replacement, a correction (its Kind), and no longer stands beside it.
	Corrects string `json:"corrects,omitempty"`
	// OtherDay says the sentence names a day the story's sentence doesn't, and so corrects nothing: an inserted one
	// that otherwise said that sentence again with another number stays inserted, and a replacement answered as a
	// correction is inserted after its sentence, as a detail (its Kind).
	OtherDay bool `json:"otherDay,omitempty"`
}

// What the rules say of an operation they drop (MergeNote.Why).
const (
	whyGarbled    = "garbled or cut short"
	whyUnsourced  = "cites no new article of the story"
	whyRepeated   = "says what another sentence of the answer does"
	whyUnknown    = "names a sentence the story doesn't have"
	whyReplaced   = "the sentence is replaced"
	whyHalf       = "the answer deletes more than half the story"
	whyNoFootnote = "adds no footnote: no new article of the story the sentence doesn't cite already"
	whyNoVersion  = "no version: the answer has no development, no correction, and no detail with a number, a name or an outcome the story lacks, from an article published since the story was written"
	whyQuiet      = "in the story without a version: every article it cites was published before the story was written"
	whyQuietPlain = "in the story without a version: background, a reaction, and a detail without a number, a name or an outcome the story lacks make none"
	// whyHeadline starts the tripwire's reason when the headline check is why (the article's new words follow).
	whyHeadline  = "the headline of an article published since the story was written names what the story lacks, and nothing of the article went into the story: "
	whyReshown   = "no version, and not in the story: every article it cites is one the story was written from, shown again"
	whyNotInsert = "no version, and not in the story: without a version a sentence is only inserted, and never a correction"
	whyUnbacked  = "states a number (%s) that none of the articles it cites does"
	whyUncounted = "counts a country's people (%s) as none of the articles it cites does"
	whyTimed     = "says again what %s.%d does, with a time word more"
)

// What makes a detail news to its story (MergeNote.News): from an article published after the version before, a
// detail with one of them makes a version.
const (
	NewsNumber  = "number"  // a number the story lacks (no date or year), or a count in a word it counts otherwise
	NewsActor   = "actor"   // a person or a body the story doesn't name
	NewsOutcome = "outcome" // an outcome or a decision the story doesn't tell
)

var sentenceRef = regexp.MustCompile(`\bs(\d+)\.(\d+)\b`)

// sentenceNumber is the sentence of story id ("s2") that ref names ("s2.3", or "3"), from 1 to n.
func sentenceNumber(ref, id string, n int) (int, bool) {
	number := strings.TrimSpace(ref)
	if m := sentenceRef.FindStringSubmatch(ref); m != nil {
		if "s"+m[1] != id {
			return 0, false // another story's sentence
		}
		number = m[2]
	}
	i, err := strconv.Atoi(number)
	return i, err == nil && i >= 1 && i <= n
}

// cited reads what Gemini wrote of a story's sources: the new articles by their ids, and what it wrote where an id
// belongs, or where nothing does (round 3 of the replay: "(a1)" and "(site)" at a sentence's end, and a site's name
// among a sentence's sources).
type cited struct {
	ids *sources
	// sites are the story's sites as Gemini may write them: each one's id and its name in the request, in lower
	// case.
	sites map[string]bool
	// only is the one new article of the story a site has in the request, by the same; a site with several, or
	// with none, has none here.
	only map[string]string
}

// siteKey is a site's id or name as it is looked up: without what surrounds it, in lower case.
func siteKey(name string) string {
	return strings.ToLower(strings.Trim(name, " \t\"'[](){}״׳„”“"))
}

// cited is what reads the sources of a's answer, asked in a request with ids.
func (a *asked) cited(ids *sources) *cited {
	c := &cited{ids: ids, sites: map[string]bool{}, only: map[string]string{}}
	keys := func(site string) []string {
		out := []string{siteKey(site)}
		if name := siteKey(a.names[site]); name != "" && name != out[0] {
			out = append(out, name)
		}
		return out
	}
	for _, r := range a.step.Story.Reports {
		for _, key := range keys(r.SourceID) {
			c.sites[key] = true
		}
	}
	shown := map[string]int{}
	for _, r := range a.plan.New {
		if _, asked := ids.urls[r.URL]; !asked {
			continue // not in the request
		}
		for _, key := range keys(r.SourceID) {
			shown[key]++
			c.only[key] = r.URL
		}
	}
	for key, n := range shown {
		if n != 1 {
			delete(c.only, key)
		}
	}
	return c
}

// refSplit parts the sources Gemini wrote in one string.
var refSplit = regexp.MustCompile(`[,،;]`)

// urls are the articles refs name: by their ids ("a3", "[a1, a2]"), and, where Gemini wrote a site's id or name in
// an id's place, the story's one new article of that site. A site with several new articles names none of them.
func (c *cited) urls(refs ...string) []string {
	var out []string
	add := func(url string) {
		if url != "" && !contains(out, url) {
			out = append(out, url)
		}
	}
	for _, ref := range refs {
		for _, id := range articleRef.FindAllString(ref, -1) {
			add(c.ids.ids[id])
		}
		for _, name := range refSplit.Split(ref, -1) {
			add(c.only[siteKey(name)])
		}
	}
	return out
}

var (
	// aside is what Gemini wrote in brackets in a sentence, with the space before it: "(a1)", "[a1, a3]", "(site)".
	aside = regexp.MustCompile(`\s*[\[(（]([^\[\]()（）]*)[\])）]`)
	// asideLabel is the request's label of an old sentence's sites, copied into such brackets.
	asideLabel = regexp.MustCompile(`^\s*(?:` + sourcesLabel + `|מקור)\s*:`)
	// refID is an article's id, or a sentence's number, alone.
	refID = regexp.MustCompile(`^(?i:a\d+|s\d+(\.\d+)?)$`)
)

// clean is an answered sentence without the sources Gemini wrote into its text: brackets that hold nothing but
// articles' ids, sentence numbers and the story's own sites, by id or name ("... נסגרו (a1).", "... נסגרו. (site)",
// "[מקורות: site, a2]"). Any other brackets are the sentence's own and stay ("(בן 34)").
func (c *cited) clean(text string) string {
	return strings.TrimSpace(aside.ReplaceAllStringFunc(text, func(found string) string {
		inside := asideLabel.ReplaceAllString(aside.FindStringSubmatch(found)[1], "")
		if strings.TrimSpace(inside) == "" {
			return found
		}
		for _, part := range refSplit.Split(inside, -1) {
			if part = strings.TrimSpace(part); !refID.MatchString(part) && !c.sites[siteKey(part)] {
				return found
			}
		}
		return ""
	}))
}

// garbled says text has a word Gemini garbled, or is not Hebrew: a word with a Hebrew letter and an Arabic one
// (the model slips an Arabic letter into a Hebrew word), or a text with as many Arabic letters as Hebrew ones. An
// Arabic word that stands apart in a Hebrew sentence (a quoted word, a name as it is written) is no garble: round 3
// of the replay lost a fact to one. Narrower than the whole writing's check (arabic), which drops a sentence with
// any Arabic letter.
func garbled(text string) bool {
	hebrew, other := 0, 0     // letters in all
	inHebrew, inOther := 0, 0 // letters of the word being read
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Arabic, r) && unicode.IsLetter(r):
			other++
			inOther++
		case unicode.Is(unicode.Hebrew, r) && unicode.IsLetter(r):
			hebrew++
			inHebrew++
		case unicode.IsLetter(r) || unicode.IsMark(r):
			// A letter of another script, or a point: the word goes on.
		default:
			inHebrew, inOther = 0, 0
		}
		if inHebrew > 0 && inOther > 0 {
			return true
		}
	}
	return other > 0 && other >= hebrew
}

// sentence is one answered sentence, without the sources written into its text, with its footnotes as URLs, among
// allowed; or why it is dropped: "garbled" (empty, garbled, or cut short) or "unsourced".
func (c *cited) sentence(text string, refs []string, allowed map[string]bool) (Sentence, string) {
	text = c.clean(text)
	if text == "" || garbled(text) || !finished(text) {
		return Sentence{}, "garbled"
	}
	var urls []string
	for _, url := range c.urls(refs...) {
		if allowed[url] && len(urls) < maxFootnotes {
			urls = append(urls, url)
		}
	}
	if len(urls) == 0 {
		return Sentence{}, "unsourced"
	}
	return Sentence{Text: text, Sources: urls}, ""
}

// unbacked is a number of s that neither the articles s cites state, as the request showed them, nor the story
// (told): a count or a sum of another event, or of no article at all. "" when there is none, and when the request's
// text of one of the articles isn't known. A date or a year is no such number: Gemini works them out ("yesterday").
// people says it is a count of a country's people, three and more (headcounts: "שלושה איראנים", "3 בעלי אזרחות
// איראנית"), that none of them gives of that people (headcounted): the same count beside the people's word, or as
// many of them named one by one. That an article has the number is not enough there: one that tells of three
// rescued, a Chinese tourist and two Japanese, states no three Chinese (round 7 of the replay). Only of such a
// people, as any other count in a word is as often one Gemini rightly works out (three ministers, of three names;
// three days, of a date) or part of a place's name; and three and more, as "שני" is too much else.
func (a *asked) unbacked(s Sentence, told *numbers) (number string, people bool) {
	cited := newNumbers()
	var texts []string
	for _, url := range s.Sources {
		shown, ok := a.shown[url]
		if !ok {
			return "", false
		}
		cited.add(shown)
		texts = append(texts, shown)
	}
	for _, f := range figures(s.Text) {
		if !f.date && !cited.states(f) && !told.states(f) {
			return f.text, false
		}
	}
	story := false
	for _, h := range headcounts(s.Text, false) {
		if h.value <= 2 {
			continue
		}
		if !story {
			story = true
			texts = append(texts, a.step.Prev.Title)
			for _, kept := range a.plan.Kept {
				texts = append(texts, kept.Text)
			}
		}
		if !headcounted(h, texts...) {
			return h.word, true
		}
	}
	return "", false
}

// edit is a sentence that survived the rules: inserted after sentence at (0 leads), or sentence at's new text.
type edit struct {
	sentence Sentence
	kind     string
	at       int
	replace  bool
	note     int  // its note, among Merged.Notes
	quiet    bool // it goes into the story though the answer makes no version
}

// merge applies what Gemini answered for a story (id in the request, "s1") to the version before. The rules:
//   - A new or replaced sentence cites at least one new article of this story, or it is dropped. So is one that only
//     says again what the story has (restates; also when all it adds is a time word), or what an
//     earlier sentence of the answer does (a count read as its number, and an inserted one read with the story's
//     words too: "three men were arrested" beside the answer's own "3 were arrested"), and one with a number that
//     neither the articles it cites nor the story state, or a count of a country's people that none of them gives
//     of that people (unbacked).
//   - An old sentence changes only by being replaced or deleted, by its number; an operation on a number the story
//     doesn't have is dropped. Its text and footnotes are otherwise kept exactly; a citation adds a footnote.
//   - An inserted sentence that says a sentence of the story again with another number (contradicted) takes that
//     sentence's place, as a correction: the story must not say both. A replacement answered as a detail that
//     gives another number than the sentence it replaces (differ) is a correction too.
//   - No sentence corrects one of another day: an inserted one that names a day the story's sentence doesn't
//     (anotherDay: "yesterday", a day of the week, a date) stays inserted, and a replacement that is a correction
//     and names one is inserted after its sentence, as a detail. Last night's figure is no correction of the night
//     before's.
//   - A version is made when a development or a correction survives, or a detail that adds what makes one news: a
//     number the story lacks (no date or year; a count in a word as recount reads it), a person or a body it doesn't name (newName, newBody), an outcome or a decision it doesn't tell
//     (outcome); and one of that sentence's articles was published after the version before was written
//     (Step.fresh; an article whose time isn't known counts as one). Round 5 of the replay had every detail with
//     a number or a name make one, and side figures made thin versions; round 6 had none make one, and lost real
//     news that Gemini calls a detail. Telling a side figure from the story's own by its words was not robust, and
//     a thin version is the lesser loss.
//   - What goes into the story is not what makes a version. Every sentence that survived the rules goes in: with
//     the version when one is made, and quietly when none is, whatever its kind: the story's text gains the
//     sentence with its sources, and nothing else changes, not the title, the time or the updates. Without a
//     version a sentence is only inserted: no replacement and no correction (the story must not change what it
//     tells without a version to show that it did), nothing is deleted, and nothing goes in from the articles the
//     story was written from alone, shown again.
//   - An answer that names none of the new articles is no answer (Skipped): the story is to be asked about again.
//
// What Gemini wrote of sources is read by cited: ids and sites written into a sentence's text are taken out of it,
// and a site written where an id belongs is its one new article.
func merge(a *asked, answer Answer, id string, ids *sources, now time.Time) Merged {
	p := a.plan
	var m Merged
	n := len(p.Kept)
	refs := a.cited(ids)
	allowed := map[string]bool{}
	// old are the new articles known to be published before the version before was written.
	old := map[string]bool{}
	for _, r := range p.New {
		allowed[r.URL] = true
		if fresh, known := a.step.fresh(r); known && !fresh {
			old[r.URL] = true
		}
	}
	m.Unmentioned = refs.unmentioned(answer, p)
	m.Skipped = len(m.Unmentioned) > 0 && len(m.Unmentioned) == len(ids.asked(p))
	for _, u := range answer.Unrelated {
		if strings.TrimSpace(u.Event) == "" {
			continue // no other event named: not shown to be about another one
		}
		for _, url := range refs.urls(u.ID) {
			if allowed[url] {
				allowed[url] = false
				m.Unrelated = append(m.Unrelated, url)
			}
		}
	}
	index := indexOf(p.Kept)
	// The story's numbers, and its words with its title's: what a sentence adds is told against them.
	told := newNumbers()
	told.add(a.step.Prev.Title)
	titled := newWordIndex()
	titled.add(a.step.Prev.Title)
	for _, s := range p.Kept {
		told.add(s.Text)
	}
	known := func(word string) bool { return index.has(word) || titled.has(word) }
	answered := newWordIndex()
	var edits []edit
	replaced := map[int]bool{}
	// note keeps what became of an operation; drop is a dropped one's.
	note := func(n MergeNote) int {
		m.Notes = append(m.Notes, n)
		return len(m.Notes) - 1
	}
	drop := func(n MergeNote, why string) {
		n.Outcome, n.Why = NoteDropped, why
		note(n)
	}
	accept := func(op MergeNote, at int, replace bool) {
		s, why := refs.sentence(op.Text, op.Sources, allowed)
		kind := strings.ToLower(strings.TrimSpace(op.Kind))
		if !contains(kinds, kind) {
			kind = Detail
		}
		// A sentence is checked without its time words too: "the roads were closed today" tells nothing that "the
		// roads were closed" didn't, whatever kind it was given (until round 6 only a development was read so:
		// nothing else went into a story without a version).
		timed, unbacked, worded, again := -1, "", false, false
		if why == "" {
			unbacked, worded = a.unbacked(s, told)
			// What another sentence of the answer says, a count read as its number: "שלושה נעצרו" after "3 נעצרו".
			// An inserted one is read as by someone who has the story too: beside the answer's sentence that
			// replaces "2 נעצרו" with "3 נעצרו", one that says the three again with words of the story, or with
			// words that tell nothing ("בסך הכול"), tells nothing.
			if plain := numbered(s.Text); replace {
				again = answered.restates(plain)
			} else {
				_, again = answered.restatedTo(plain, func(word string) bool {
					return answered.has(word) || index.has(word) || functionWord(word)
				})
			}
			if text, stripped := timeless(s.Text); stripped && !index.restates(s.Text) {
				if of, ok := index.restated(text); ok {
					timed = max(of, 0)
				}
			}
		}
		switch {
		case why == "garbled":
			m.Stats.Garbled++
			drop(op, whyGarbled)
		case why == "unsourced":
			m.Stats.Unsourced++
			drop(op, whyUnsourced)
		case unbacked != "":
			m.Stats.Unbacked++
			if worded {
				m.Stats.Worded++
				drop(op, fmt.Sprintf(whyUncounted, unbacked))
				break
			}
			drop(op, fmt.Sprintf(whyUnbacked, unbacked))
		case index.restates(s.Text):
			m.Stats.Restated++
			why := "has no words"
			if of, _ := index.restated(s.Text); of >= 0 {
				why = fmt.Sprintf("says again what %s.%d does", id, of+1)
			}
			drop(op, why)
		case timed >= 0:
			m.Stats.Restated++
			m.Stats.Timed++
			drop(op, fmt.Sprintf(whyTimed, id, timed+1))
		case again:
			m.Stats.Repeated++
			drop(op, whyRepeated)
		default:
			// A replacement that gives another number than its sentence corrects it, whatever Gemini called it:
			// as a detail it would make no version, and never go in.
			if replace && kind == Detail && differ(s.Text, p.Kept[at-1].Text) {
				kind = Correction
			}
			// Unless it names a day its sentence doesn't: another day's figure corrects nothing, and the sentence
			// stays, with this one after it as a detail.
			if replace && kind == Correction && anotherDay(s.Text, p.Kept[at-1].Text) {
				replace, kind = false, Detail
				op.OtherDay = true
				m.Stats.OtherDay++
			}
			answered.add(numbered(s.Text))
			op.Kind = kind
			edits = append(edits, edit{sentence: s, kind: kind, at: at, replace: replace, note: note(op)})
			if replace {
				replaced[at] = true
			}
		}
	}
	for _, r := range answer.Replace {
		m.Stats.Asked++
		op := MergeNote{Op: "replace", At: r.Sentence, Kind: r.Kind, Text: r.Text, Sources: r.Sources}
		at, ok := sentenceNumber(r.Sentence, id, n)
		switch {
		case !ok:
			m.Stats.Unknown++
			drop(op, whyUnknown)
		case replaced[at]:
			m.Stats.Repeated++
			drop(op, "another sentence of the answer replaces the same one")
		default:
			accept(op, at, true)
		}
	}
	for _, in := range answer.Insert {
		m.Stats.Asked++
		at, ok := sentenceNumber(in.After, id, n)
		if !ok {
			at = 0
			if after := strings.ToLower(strings.TrimSpace(in.After)); after != "top" && after != "" && after != "0" {
				m.Stats.Unanchored++
			}
		}
		accept(MergeNote{Op: "insert", At: in.After, Kind: in.Kind, Text: in.Text, Sources: in.Sources}, at, false)
	}
	// An inserted sentence that says a sentence of the story again with another number ("3 were arrested" beside
	// "2 were arrested") would leave the story saying both: it takes that sentence's place, as a correction. So it
	// makes a version when its article was published since the story was written, goes in with a version another
	// sentence makes, and otherwise stays out: no replacement is ever quiet. Background and a reaction tell of
	// another time or of what someone said, and stay what they are.
	for i := range edits {
		e := &edits[i]
		if e.replace || e.kind == Background || e.kind == Reaction {
			continue
		}
		at := index.contradicted(e.sentence.Text, p.Kept)
		switch {
		case at > 0 && !replaced[at]:
			e.replace, e.at, e.kind = true, at, Correction
			replaced[at] = true
			m.Stats.Corrected++
			m.Notes[e.note].Kind, m.Notes[e.note].Corrects = Correction, fmt.Sprintf("%s.%d", id, at)
		case at == 0 && !m.Notes[e.note].OtherDay:
			// Only the day it names keeps it from being a sentence's correction: it stays inserted.
			if other := index.contradicting(e.sentence.Text, p.Kept, true); other > 0 && !replaced[other] {
				m.Notes[e.note].OtherDay = true
				m.Stats.OtherDay++
			}
		}
	}
	// What the story counts, to tell a count in a word that is news to it (recount).
	var tallied []tally
	counted := map[int64]bool{}
	for _, sentence := range p.Kept {
		tallied = append(tallied, tallies(sentence.Text)...)
		for _, c := range counts(sentence.Text) {
			counted[c.value] = true
		}
	}
	byOther := false // a development or a correction makes the version
	for _, e := range edits {
		note := &m.Notes[e.note]
		// News is what an article published since the story was written tells: a sentence that stands only on
		// older ones (a preview that joined the story late, a source of the story shown again) makes no version.
		since := false
		for _, url := range e.sentence.Sources {
			since = since || !old[url]
		}
		switch e.kind {
		case Development, Correction:
			if since {
				m.Version = true
				note.MakesVersion = true
				byOther = true
			} else {
				m.Stats.Old++
				note.Old = true
			}
		case Detail:
			// A detail is news by what it adds that the story lacks: a number, a date or a year aside; a person or
			// a body it names; an outcome or a decision.
			text := e.sentence.Text
			number, dated := told.news(text)
			if !number {
				number = recount(text, told, counted, tallied) != ""
			}
			var shown []string
			for _, url := range e.sentence.Sources {
				shown = append(shown, a.shown[url])
			}
			name := newName(text, known, shown...)
			if name == "" {
				name = newBody(text, known)
			}
			switch {
			case number:
				note.News = NewsNumber
			case name != "":
				note.News, note.Name = NewsActor, name
				m.Stats.Named++
			case outcome(text, known) != "":
				note.News = NewsOutcome
				m.Stats.Decided++
			case dated:
				m.Stats.Dated++
				note.Dated = true
			}
			if note.Adds = note.News != ""; !note.Adds {
				break
			}
			m.Stats.Adds++
			if since {
				m.Stats.Since++
				m.Version = true
				note.MakesVersion = true
			}
		}
	}
	if m.Version && !byOther {
		m.Stats.ByDetail = 1
	}
	// Without a version every sound sentence goes into the story all the same, quietly: inserted ones only and no
	// correction (the story must not change what it tells without a version to show that it did), and none that
	// stands only on articles the story was written from (shown again, they add nothing).
	own := seenOf(a.step.Prev)
	reshown := func(sources []string) bool {
		for _, url := range sources {
			if !own[url] {
				return false
			}
		}
		return true
	}
	replacing := map[int]bool{} // the sentences whose replacement goes in
	for i, e := range edits {
		note := &m.Notes[e.note]
		leave := func(why string, count *int) {
			note.Outcome, note.Why = NoteLeftOut, why
			m.Stats.Unapplied++
			*count++
		}
		switch {
		case m.Version:
			note.Outcome = NoteApplied
			if e.replace {
				replacing[e.at] = true
			}
		case e.replace || e.kind == Correction:
			leave(whyNotInsert, &m.Stats.NotInserted)
		case reshown(e.sentence.Sources):
			leave(whyReshown, &m.Stats.Reshown)
		default:
			// The articles' time is what kept a development, or a detail that adds, from making a version.
			note.Outcome, note.Why = NoteQuiet, whyQuietPlain
			if note.Old || note.Adds {
				note.Why = whyQuiet
			}
			edits[i].quiet = true
		}
	}
	// Footnotes: a new article that states an old sentence's fact.
	kept := make([]Sentence, n)
	for i, s := range p.Kept {
		kept[i] = Sentence{Text: s.Text, Sources: append([]string{}, s.Sources...)}
	}
	for _, c := range answer.Cite {
		op := MergeNote{Op: "cite", At: c.Sentence, Sources: c.Sources}
		at, ok := sentenceNumber(c.Sentence, id, n)
		if !ok {
			m.Stats.Unknown++
			drop(op, whyUnknown)
			continue
		}
		cited := false
		for _, url := range refs.urls(c.Sources...) {
			if allowed[url] && !contains(kept[at-1].Sources, url) && len(kept[at-1].Sources) < maxFootnotes {
				kept[at-1].Sources = append(kept[at-1].Sources, url)
				cited = true
			}
		}
		if cited {
			m.Stats.Cited++
			op.Outcome = NoteApplied
			note(op)
		} else {
			drop(op, whyNoFootnote)
		}
	}
	var unrelated []string
	unrelated = append(unrelated, m.Unrelated...)
	contradictions := refs.newContradictions(answer.Contradictions, id, kept, allowed, a.step.Story)
	// Sentences are deleted only with a version.
	deleted := map[int]bool{}
	deletions := map[int]int{} // a deleted sentence's note
	for _, ref := range answer.Delete {
		op := MergeNote{Op: "delete", At: ref}
		at, ok := sentenceNumber(ref, id, n)
		switch {
		case !m.Version:
			op.Outcome, op.Why = NoteLeftOut, whyNoVersion
			note(op)
		case !ok:
			m.Stats.Unknown++
			drop(op, whyUnknown)
		case replaced[at]:
			drop(op, whyReplaced)
		case !deleted[at]:
			deleted[at] = true
			op.Outcome = NoteApplied
			deletions[at] = note(op)
		}
	}
	// Deleting half the story is no update of it: the sentences stay.
	if 2*len(deleted) > n {
		m.Stats.Unknown += len(deleted)
		deleted = map[int]bool{}
		for _, i := range deletions {
			m.Notes[i].Outcome, m.Notes[i].Why = NoteDropped, whyHalf
		}
	}
	m.Stats.Deleted = len(deleted)
	// The story: the version before with what goes in, every surviving sentence with a version and the quiet ones
	// without. Only a version has what's new, and a new title.
	var sentences, added []Sentence
	var addedKinds []string
	add := func(e edit) {
		sentences = append(sentences, e.sentence)
		for _, url := range e.sentence.Sources {
			if !contains(m.Used, url) {
				m.Used = append(m.Used, url)
			}
		}
		switch {
		case !m.Version:
			m.Stats.Quiet++
			return
		case e.replace:
			m.Stats.Replaced++
		default:
			m.Stats.Inserted++
		}
		added = append(added, e.sentence)
		addedKinds = append(addedKinds, e.kind)
	}
	for at := 0; at <= n; at++ {
		switch {
		case at == 0 || deleted[at]:
		case replacing[at]:
			for _, e := range edits {
				if e.replace && e.at == at {
					add(e)
				}
			}
		default:
			sentences = append(sentences, kept[at-1])
		}
		for _, e := range edits {
			if !e.replace && e.at == at && (m.Version || e.quiet) {
				add(e)
			}
		}
	}
	title := ""
	if m.Version {
		title = strings.TrimSpace(answer.Title)
	}
	m.Story = render(a.step, p, sentences, added, addedKinds, contradictions, title, unrelated, now)
	m.Story.HeadlineOnly = append(m.Story.HeadlineOnly, headlineOnly(ids, p, allowed)...)
	return m
}

// asked are the new articles of p the request showed: the ones with an id in it.
func (s *sources) asked(p Plan) []string {
	var out []string
	for _, r := range p.New {
		if _, ok := s.urls[r.URL]; ok {
			out = append(out, r.URL)
		}
	}
	return out
}

// unmentioned are the new articles of p the request showed that answer names nowhere: not as unrelated (with or
// without the other event), and not among the sources of an inserted or replaced sentence, a citation or a side of
// a contradiction, whatever the rules then make of it.
func (c *cited) unmentioned(answer Answer, p Plan) []string {
	named := map[string]bool{}
	name := func(refs ...string) {
		for _, url := range c.urls(refs...) {
			named[url] = true
		}
	}
	for _, u := range answer.Unrelated {
		name(u.ID)
	}
	for _, in := range answer.Insert {
		name(in.Sources...)
	}
	for _, r := range answer.Replace {
		name(r.Sources...)
	}
	for _, c := range answer.Cite {
		name(c.Sources...)
	}
	for _, c := range answer.Contradictions {
		for _, side := range c.Sides {
			name(side.Sources...)
		}
	}
	var out []string
	for _, url := range c.ids.asked(p) {
		if !named[url] {
			out = append(out, url)
		}
	}
	return out
}

// headlineOnly are the story's new articles the request showed without a body.
func headlineOnly(ids *sources, p Plan, allowed map[string]bool) []string {
	var out []string
	for _, r := range p.New {
		if allowed[r.URL] && ids.headlineOnly[r.URL] {
			out = append(out, r.URL)
		}
	}
	return out
}

// newContradictions are the answered contradictions that hold: two sides or more, each with its own value and its
// own sites, one of them a new article. A side cites new articles by id, or the story's sentence that says it, whose
// sources are then its own.
func (refs *cited) newContradictions(in []answerContradiction, id string, kept []Sentence, allowed map[string]bool, story stories.Story) []Sentence {
	siteOf := map[string]string{}
	for _, r := range story.Reports {
		siteOf[r.URL] = r.SourceID
	}
	var out []Sentence
	for _, c := range in {
		text := strings.TrimSpace(inlineRefs.ReplaceAllString(c.Text, ""))
		sites := map[string]int{}
		values := map[string]bool{}
		var urls []string
		ok := len(c.Sides) >= 2 && text != "" && finished(text) && !garbled(text)
		fresh := false
		for i, side := range c.Sides {
			var sideURLs []string
			for _, source := range side.Sources {
				if at, known := sentenceNumber(source, id, len(kept)); known && sentenceRef.MatchString(source) {
					sideURLs = append(sideURLs, kept[at-1].Sources...)
					continue
				}
				for _, url := range refs.urls(source) {
					if allowed[url] {
						sideURLs = append(sideURLs, url)
						fresh = true
					}
				}
			}
			for _, url := range sideURLs {
				site := siteOf[url]
				if j, seen := sites[site]; seen && j != i {
					ok = false // one site on two sides is no contradiction between sites
				}
				sites[site] = i
				if !contains(urls, url) {
					urls = append(urls, url)
				}
			}
			value := strings.TrimSpace(side.Value)
			if len(sideURLs) == 0 || value == "" || values[value] {
				ok = false
			}
			values[value] = true
		}
		if ok && fresh {
			out = append(out, Sentence{Text: text, Sources: urls})
		} else {
			refs.ids.oneSided++
		}
	}
	return out
}

// tripwire is why a story whose answer made no version is written again whole all the same; "" when it isn't. It
// keeps a miss from freezing: an incremental story is never read whole again otherwise. Two checks, by words
// alone, against the story and the articles it has:
//   - a new article that is clearly news (clearlyNovel);
//   - the headline (headline is then true): a new article published after the version before, from which the
//     answer put nothing into the story (Merged.Used), whose title or subtitle names what the story lacks
//     (headlineNews). The title and the subtitle state an article's main news, and round 4 of the replay missed
//     news that stood only there.
func (w *Writer) tripwire(ctx context.Context, a *asked, m Merged) (why string, headline bool) {
	if m.Version {
		return "", false
	}
	// The story's words and numbers, with its title's and its articles': read once, when a new article is checked.
	var index *wordIndex
	var told *numbers
	read := func() {
		if index != nil {
			return
		}
		index, told = indexOf(a.plan.Kept), newNumbers()
		add := func(text string) {
			index.addText(text)
			told.add(text)
		}
		for _, s := range a.plan.Kept {
			told.add(s.Text)
		}
		add(oneLine(a.step.Prev.Title))
		for _, old := range a.plan.Old {
			add(oneLine(old.Title))
			add(oneLine(old.Subtitle))
			if article := w.article(ctx, old.URL); article != nil {
				for _, paragraph := range article.Body {
					add(oneLine(paragraph))
				}
			}
		}
	}
	for _, r := range a.plan.New {
		if contains(m.Unrelated, r.URL) {
			continue
		}
		read()
		if sentences, novel, numbers := index.novelty(a.shown[r.URL]); clearlyNovel(sentences, novel, numbers) {
			return fmt.Sprintf("Gemini found nothing new, but %d of an article's %d sentences and %d of its numbers are not in the story", novel, sentences, numbers), false
		}
	}
	own := seenOf(a.step.Prev)
	for _, r := range a.plan.New {
		// Not an article the story was written from, shown again: its headline is the story's.
		if contains(m.Unrelated, r.URL) || contains(m.Used, r.URL) || own[r.URL] {
			continue
		}
		if fresh, known := a.step.fresh(r); known && !fresh {
			continue
		}
		read()
		if what := headlineNews(oneLine(r.Title)+". "+oneLine(r.Subtitle), index, told); what != "" {
			return whyHeadline + what, true
		}
	}
	return "", false
}
