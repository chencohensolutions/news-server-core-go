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

	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// The incremental update of a story (docs/incremental.md). A story that gained articles is not written again from all
// of them: Gemini is shown the version before as numbered sentences and only the new articles, and answers with what
// they change: sentences to insert, replace or delete, and old sentences the new articles state too. The server
// applies that to the version before, so the old text and its footnotes stay exactly as they were, and what was
// inserted or replaced is what's new. Many stories go in one request. For now it runs in shadow only (shadow.go) and
// in the replay (replay.go).

// Settings.Incremental's values.
const (
	IncrementalOff    = "off"
	IncrementalShadow = "shadow"
)

// What an inserted or replaced sentence is.
const (
	Development = "development" // something happened or became known since the story was written
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
		if l, ok := latest[s.ID]; !ok || s.WrittenAt.After(l.WrittenAt) {
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
Each story shows its title, the story so far as numbered sentences (each ends with the sites it cites, in
parentheses), the titles of the articles it was written from, and then its new articles: each with an id in
brackets, its site, time, title, subtitle and its body (the whole article, or its start).

The story so far is written and stays as it is. You answer only what the new articles change in it. Never write a
sentence of the story again, and never reword one.

For every story answer, in this order:
- "story": its id ("s1").
- "unrelated": the new articles that are clearly about a different event than the story (the grouping can be
  wrong), each as its "id" and the "event" it is about instead, in a few Hebrew words. That event must be another
  incident: another person, place or happening. An article about the same finding, decision or incident in other
  words, one that teases it without naming it, covers another side of it, or reacts to it belongs to the story.
  When unsure, it belongs. Don't use unrelated ones anywhere else in the answer. Usually none: then an empty list.
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
- "correction": a fact of the story changed or was wrong.
- "detail": a fact about what the story already tells that it lacks: a name, a number, a place, a time, a quote.
- "background": what was before the event, or context.
- "reaction": what someone said about it.

Most new articles repeat what the story has. Then the story's answer is its id, what it cites, and empty lists:
never invent a difference to have something to answer.

Writing rules, all of them strict:
- Write in Hebrew, in the plain, neutral register of a news agency.
- Facts only: what happened, who, where, when, numbers, quotes attributed to who said them. No opinions, no
  commentary, no speculation, no adjectives that judge, nothing the new articles don't say.
- One fact a sentence, each fact once. When several new articles state it, cite all of them, one article a site.
- Cite only the ids of the story's own new articles. A fact no new article states is not written.
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
}

// bodyChars is how much of an article's body a request shows.
func bodyChars(settings Settings) int {
	if settings.FullArticles {
		return fullArticleChars
	}
	return articleChars
}

// storyText is one story of an incremental request: its title, the version before as numbered sentences with the
// sites each cites, the titles of the articles it has, and the new articles whole (without the paragraphs an
// article it has already gave, word for word).
func (w *Writer) storyText(ctx context.Context, ids *sources, id string, step Step, p Plan, settings Settings) (string, *asked) {
	a := &asked{step: step, plan: p, shown: map[string]string{}, bodies: map[string]string{}}
	site := map[string]string{}
	for _, r := range step.Story.Reports {
		site[r.URL] = w.siteName(r.SourceID)
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
		fmt.Fprintf(&b, "%s.%d %s (%s)\n", id, i+1, oneLine(s.Text), strings.Join(sites, ", "))
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
	added := shown(stories.Story{Reports: p.New})
	each := min(bodyChars(settings), max(articleChars, storyBodyChars/max(len(added), 1)))
	for _, r := range added {
		body := w.body(ctx, r, each, seen)
		b.WriteString(w.reportOf(ids, r, body))
		b.WriteString("\n")
		a.shown[r.URL] = strings.TrimSpace(oneLine(r.Title) + ". " + oneLine(r.Subtitle) + ". " + body)
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
	// too); cut short or garbled.
	Unsourced int `json:"unsourced"`
	Restated  int `json:"restated"`
	Repeated  int `json:"repeated"`
	Unknown   int `json:"unknown"`
	Garbled   int `json:"garbled"`
	// Unapplied are sound sentences left out because none of the answer's makes a version (background, reactions,
	// details without a number).
	Unapplied int `json:"unapplied"`
	// Unanchored are inserted sentences whose place wasn't a sentence of the story: they lead.
	Unanchored int `json:"unanchored"`
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
	s.Unapplied += o.Unapplied
	s.Unanchored += o.Unanchored
}

// Merged is a story's answer applied to the version before.
type Merged struct {
	// Story is the story after the step. Without a version it is the version before, with the new articles among
	// its reports and, where they state a sentence's fact, its footnotes.
	Story StorySummary
	// Version says the answer makes a new version: what was inserted and replaced is its latest update.
	Version bool
	// Unrelated are the new articles Gemini found to be about another event.
	Unrelated []string
	Stats     MergeStats
	// Notes are what became of each operation of the answer, in the order the rules read them: the replaced
	// sentences, the inserted ones, the citations, the deletions.
	Notes []MergeNote
}

// What became of an operation of an answer (MergeNote.Outcome).
const (
	NoteApplied = "applied"  // in the version; of a citation, a footnote the sentence got
	NoteLeftOut = "left out" // sound, and left out because the answer makes no version
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
	// with a number the story lacks.
	MakesVersion bool `json:"makesVersion,omitempty"`
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
	whyNoVersion  = "no version: the answer has no development, no correction, and no detail with a number the story lacks"
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

// sentence is one answered sentence with its footnotes as URLs, among allowed; or why it is dropped: "garbled"
// (empty, an Arabic letter, or cut short) or "unsourced".
func (s *sources) sentence(text string, refs []string, allowed map[string]bool) (Sentence, string) {
	text = strings.TrimSpace(inlineRefs.ReplaceAllString(text, ""))
	if text == "" || arabic.MatchString(text) || !finished(text) {
		return Sentence{}, "garbled"
	}
	var urls []string
	for _, ref := range refs {
		for _, id := range articleRef.FindAllString(ref, -1) {
			if url, ok := s.ids[id]; ok && allowed[url] && !contains(urls, url) && len(urls) < maxFootnotes {
				urls = append(urls, url)
			}
		}
	}
	if len(urls) == 0 {
		return Sentence{}, "unsourced"
	}
	return Sentence{Text: text, Sources: urls}, ""
}

// edit is a sentence that survived the rules: inserted after sentence at (0 leads), or sentence at's new text.
type edit struct {
	sentence Sentence
	kind     string
	at       int
	replace  bool
	note     int // its note, among Merged.Notes
}

// merge applies what Gemini answered for a story (id in the request, "s1") to the version before. The rules:
//   - A new or replaced sentence cites at least one new article of this story, or it is dropped. So is one that only
//     says again what the story has (restates), or what an earlier sentence of the answer does.
//   - An old sentence changes only by being replaced or deleted, by its number; an operation on a number the story
//     doesn't have is dropped. Its text and footnotes are otherwise kept exactly; a citation adds a footnote.
//   - A version is made only when a development or a correction survives, or a detail with a number the story
//     lacks. Otherwise the text stays as it was (background and reactions alone don't make a version, and are left
//     out), and the new articles are only among the story's reports and, where cited, its footnotes.
func merge(a *asked, answer Answer, id string, ids *sources, now time.Time) Merged {
	p := a.plan
	var m Merged
	n := len(p.Kept)
	allowed := map[string]bool{}
	for _, r := range p.New {
		allowed[r.URL] = true
	}
	for _, u := range answer.Unrelated {
		if strings.TrimSpace(u.Event) == "" {
			continue // no other event named: not shown to be about another one
		}
		for _, ref := range articleRef.FindAllString(u.ID, -1) {
			if url := ids.ids[ref]; allowed[url] {
				allowed[url] = false
				m.Unrelated = append(m.Unrelated, url)
			}
		}
	}
	index := indexOf(p.Kept)
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
		s, why := ids.sentence(op.Text, op.Sources, allowed)
		switch {
		case why == "garbled":
			m.Stats.Garbled++
			drop(op, whyGarbled)
		case why == "unsourced":
			m.Stats.Unsourced++
			drop(op, whyUnsourced)
		case index.restates(s.Text):
			m.Stats.Restated++
			why := "has no words"
			if of, _ := index.restated(s.Text); of >= 0 {
				why = fmt.Sprintf("says again what %s.%d does", id, of+1)
			}
			drop(op, why)
		case answered.restates(s.Text):
			m.Stats.Repeated++
			drop(op, whyRepeated)
		default:
			kind := strings.ToLower(strings.TrimSpace(op.Kind))
			if !contains(kinds, kind) {
				kind = Detail
			}
			answered.add(s.Text)
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
	for _, e := range edits {
		if e.kind == Development || e.kind == Correction || e.kind == Detail && index.hasNewNumber(e.sentence.Text) {
			m.Version = true
			m.Notes[e.note].MakesVersion = true
		}
	}
	for _, e := range edits {
		if m.Version {
			m.Notes[e.note].Outcome = NoteApplied
		} else {
			m.Notes[e.note].Outcome, m.Notes[e.note].Why = NoteLeftOut, whyNoVersion
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
		for _, source := range c.Sources {
			for _, ref := range articleRef.FindAllString(source, -1) {
				if url := ids.ids[ref]; allowed[url] && !contains(kept[at-1].Sources, url) && len(kept[at-1].Sources) < maxFootnotes {
					kept[at-1].Sources = append(kept[at-1].Sources, url)
					cited = true
				}
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
	contradictions := ids.newContradictions(answer.Contradictions, id, kept, allowed, a.step.Story)
	if !m.Version {
		m.Stats.Unapplied = len(edits)
		for _, ref := range answer.Delete {
			note(MergeNote{Op: "delete", At: ref, Outcome: NoteLeftOut, Why: whyNoVersion})
		}
		m.Story = render(a.step, p, kept, nil, nil, contradictions, "", unrelated, now)
		m.Story.HeadlineOnly = append(m.Story.HeadlineOnly, headlineOnly(ids, p, allowed)...)
		return m
	}
	deleted := map[int]bool{}
	deletions := map[int]int{} // a deleted sentence's note
	for _, ref := range answer.Delete {
		op := MergeNote{Op: "delete", At: ref}
		at, ok := sentenceNumber(ref, id, n)
		switch {
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
	var sentences, added []Sentence
	var addedKinds []string
	add := func(e edit) {
		sentences = append(sentences, e.sentence)
		added = append(added, e.sentence)
		addedKinds = append(addedKinds, e.kind)
		if e.replace {
			m.Stats.Replaced++
		} else {
			m.Stats.Inserted++
		}
	}
	for at := 0; at <= n; at++ {
		switch {
		case at == 0 || deleted[at]:
		case replaced[at]:
			for _, e := range edits {
				if e.replace && e.at == at {
					add(e)
				}
			}
		default:
			sentences = append(sentences, kept[at-1])
		}
		for _, e := range edits {
			if !e.replace && e.at == at {
				add(e)
			}
		}
	}
	m.Story = render(a.step, p, sentences, added, addedKinds, contradictions, strings.TrimSpace(answer.Title), unrelated, now)
	m.Story.HeadlineOnly = append(m.Story.HeadlineOnly, headlineOnly(ids, p, allowed)...)
	return m
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
func (s *sources) newContradictions(in []answerContradiction, id string, kept []Sentence, allowed map[string]bool, story stories.Story) []Sentence {
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
		ok := len(c.Sides) >= 2 && text != "" && finished(text) && !arabic.MatchString(text)
		fresh := false
		for i, side := range c.Sides {
			var sideURLs []string
			for _, source := range side.Sources {
				if at, known := sentenceNumber(source, id, len(kept)); known && sentenceRef.MatchString(source) {
					sideURLs = append(sideURLs, kept[at-1].Sources...)
					continue
				}
				for _, ref := range articleRef.FindAllString(source, -1) {
					if url := s.ids[ref]; allowed[url] {
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
			s.oneSided++
		}
	}
	return out
}

// tripwire is why a story that Gemini found nothing new in is written again whole all the same: a new article of
// it that is clearly news by its words alone (clearlyNovel, against the story and the articles it has). "" when
// none is. It keeps a miss from freezing: an incremental story is never read whole again otherwise.
func (w *Writer) tripwire(ctx context.Context, a *asked, m Merged) string {
	if m.Version {
		return ""
	}
	var index *wordIndex
	for _, r := range a.plan.New {
		if contains(m.Unrelated, r.URL) {
			continue
		}
		if index == nil {
			index = indexOf(a.plan.Kept)
			for _, old := range a.plan.Old {
				index.addText(oneLine(old.Title))
				index.addText(oneLine(old.Subtitle))
				if article := w.article(ctx, old.URL); article != nil {
					for _, paragraph := range article.Body {
						index.addText(oneLine(paragraph))
					}
				}
			}
		}
		if sentences, novel, numbers := index.novelty(a.shown[r.URL]); clearlyNovel(sentences, novel, numbers) {
			return fmt.Sprintf("Gemini found nothing new, but %d of an article's %d sentences and %d of its numbers are not in the story", novel, sentences, numbers)
		}
	}
	return ""
}
