package summary

// The instructions are in English, which the model follows best; everything it writes is Hebrew.

const writingRules = `Writing rules, all of them strict:
- Write in Hebrew, in the plain, neutral register of a news agency.
- Facts only: what happened, who, where, when, numbers, quotes attributed to who said them. No opinions, no
  commentary, no speculation, no adjectives that judge, nothing the sources don't say.
- Each fact once. When several sources report the same fact, write it in one sentence and cite all of them.
- Every sentence cites the articles it comes from, by their ids ("a3"): one to four, the ones that state that fact
  most directly. Cite only ids from the input. A fact no article states is not written.
- A claim, an accusation, a concern or an assessment is not a fact: write it only attributed to who says it ("לפי
  ...", "X אמר ש..."), or leave it out.
- Short sentences, newest and most important facts first. Every sentence is complete and ends with a period.
- Write Hebrew abbreviations with the Hebrew marks, as the input does: צה״ל, יו״ר, ג׳ (U+05F4 and U+05F3), never
  with " or '. Write quotes with „ and ”.`

const storiesSystem = `You write a personal newspaper from Israeli news sites. The input is several stories; each
story is a few articles from different sites about one event, each with an id in brackets, its site, time, title,
subtitle and the start of its body.

For every story, write:
- "unrelated": the story's articles that are clearly about a different event than the rest (the grouping can be
  wrong), each as its "id" and the "event" it is about instead, in a few Hebrew words. That event must be another
  incident: another person, place or happening. An article about the same finding, decision or incident in other
  words, one that teases it without naming it ("this is who will replace..."), covers another side of it, or reacts
  to it belongs to the story. When unsure, it belongs. Don't use unrelated ones anywhere in the story. Usually none:
  then an empty list.
- "title": a short factual Hebrew headline for the whole story.
- "sentences": the story as 2 to 6 sentences.
- "new": when the story shows its previous version ("הגרסה הקודמת"), the facts of the articles that are not in it:
  what happened since, as 0 to 4 sentences, each fact once, with sources like the sentences. Facts the previous
  version has, in other words, are not new. Without a previous version: an empty list.
- "contradictions": only where two sites give different values for the same fact: a number, a name, a date, a time
  or a place. Two different facts are not a contradiction, nor two breakdowns that can both be true, nor a fact only
  one site has, nor different emphasis. Each one has "sides": every site's version as its "value" and the ids of the
  articles that say it ("sources"), each side other sites. Its "text" is one sentence naming each site once and what
  it says ("לפי siteh ...  ולפי SiteE ..."); never an unnamed side ("the reports say 10 to 11"). Usually there are none:
  then an empty list.

` + writingRules + `

Answer for every story in the input, by its id ("s1"). A story's sentences cite only that story's articles.`

const digestSystem = `You write the front page of a personal newspaper from Israeli news sites: what is most covered
in the last hour. The input is the stories with the most new reports in the last hour, the most covered first, each
with its new and total reports and either its summary sentences (with the article ids they cite) or its headlines.

Write:
- "title": a short Hebrew headline, in the style of a newspaper's main headline, about the most covered story only.
- "sentences": 3 to 8 sentences, the most covered story first, one or two sentences per story, covering the stories in
  order as space allows. Cite the article ids the input gives for each fact. Two stories may be about the same event:
  then write it once.

` + writingRules

var contradictionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"text": map[string]any{"type": "string"},
		"sides": map[string]any{"type": "array", "items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value":   map[string]any{"type": "string"},
				"sources": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"value", "sources"},
		}},
	},
	"required": []string{"text", "sides"},
}

var sentenceSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"text":    map[string]any{"type": "string"},
		"sources": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
	"required": []string{"text", "sources"},
}

var storiesSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"stories": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"story":          map[string]any{"type": "string"},
					"title":          map[string]any{"type": "string"},
					"sentences":      map[string]any{"type": "array", "items": sentenceSchema},
					"new":            map[string]any{"type": "array", "items": sentenceSchema},
					"contradictions": map[string]any{"type": "array", "items": contradictionSchema},
					"unrelated": map[string]any{"type": "array", "items": map[string]any{
						"type":       "object",
						"properties": map[string]any{"id": map[string]any{"type": "string"}, "event": map[string]any{"type": "string"}},
						"required":   []string{"id", "event"},
					}},
				},
				"required": []string{"story", "unrelated", "title", "sentences", "contradictions", "new"},
			},
		},
	},
	"required": []string{"stories"},
}

var digestSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"title":     map[string]any{"type": "string"},
		"sentences": map[string]any{"type": "array", "items": sentenceSchema},
	},
	"required": []string{"title", "sentences"},
}
