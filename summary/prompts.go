package summary

import "fmt"

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

// storiesSystem is the stories' instructions for a length (Settings.Length).
func storiesSystem(length string) string {
	l := lengthOf(length)
	count := fmt.Sprintf("%d to %d sentences: the more sites cover it and the more facts they give, the more\n"+
		"  sentences, up to the most; never filler or a fact twice to reach a count", l.min, l.max)
	news := fmt.Sprintf("0 to %d sentences", l.newMax)
	if l.max == 0 {
		count = "as many sentences as it takes to state every fact the articles give, each fact once; no filler"
		news = "as many sentences as the new facts take"
	}
	return fmt.Sprintf(storiesInstructions, count, news)
}

const storiesInstructions = `You write a personal newspaper from Israeli news sites. The input is several stories; each
story is a few articles from different sites about one event, each with an id in brackets, its site, time, title,
subtitle and its body (the whole article, or its start).

For every story, write:
- "unrelated": the story's articles that are clearly about a different event than the rest (the grouping can be
  wrong), each as its "id" and the "event" it is about instead, in a few Hebrew words. That event must be another
  incident: another person, place or happening. An article about the same finding, decision or incident in other
  words, one that teases it without naming it ("this is who will replace..."), covers another side of it, or reacts
  to it belongs to the story. When unsure, it belongs. Don't use unrelated ones anywhere in the story. Usually none:
  then an empty list.
- "title": a short factual Hebrew headline for the whole story.
- "sentences": the story as %s.
- "new": when the story shows its previous version ("הגרסה הקודמת"), the facts of the articles that are not in it:
  what happened since, as %s, each fact once, with sources like the sentences. Facts the previous
  version has, in other words, are not new. Without a previous version: an empty list.
- "contradictions": only where two sites give different values for the same fact: a number, a name, a date, a time
  or a place. Two different facts are not a contradiction, nor two breakdowns that can both be true, nor a fact only
  one site has, nor different emphasis. Each one has "sides": every site's version as its "value" and the ids of the
  articles that say it ("sources"), each side other sites. Its "text" is one sentence naming each site once and what
  it says ("לפי siteh ...  ולפי SiteE ..."); never an unnamed side ("the reports say 10 to 11"). Usually there are none:
  then an empty list.

` + writingRules + `

Answer for every story in the input, by its id ("s1"). A story's sentences cite only that story's articles.`

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

const threadsSystem = `You keep the threads of a personal newspaper from Israeli news sites: affairs that run over weeks, months
or years, such as a war, a police investigation, a trial, coalition talks, a strike, a scandal or a reform. The input
lists the existing threads (id, name, how many stories and when, and the story so far), then the new stories (id,
date, title and main facts).

Answer:
- "stories": every new story, each with its id and its "threads": the ids of the existing threads it continues and
  the names of new threads it starts or joins. A story can be in several threads (a strike during a war is in both).
  It continues a thread only when it is a development of that same affair, not just the same general subject. An
  empty list only for a one-time event.
- "newThreads": the affairs that start among the new stories: a short Hebrew "name" (2 to 5 words, like
  "פרשת הצוללות" or "המלחמה עם איראן"), exactly as the stories name it, and its "summary" so far. Never a thread that
  an existing one already is.
- "summaries": for every existing thread that got new stories, its id and the story so far, updated with them.
- "related": threads that are separate affairs but bear on each other (a war and the strike during it, an
  investigation and the election it shapes): a thread, by its id or a new thread's name, and the threads related to
  it, the same way. Only new relations that involve a new story's thread; usually few.
Most political, security, legal, economic and social news is part of an ongoing affair: the war in Gaza, each other
front (Iran, Lebanon), the hostages, the October 7 inquiries, the coalition and the elections, an investigation, a
trial, a reform, a strike, relations with a country, US politics, a company's saga. Put each such story in all its
affairs, starting the thread if there is none yet; a thread may begin with one story. A side development, a reaction
or a statement about an affair belongs to it. Don't put a story in an affair it only resembles: a US election
dispute is not the Iran war. Only a clearly one-time event
with nothing before or after it (a road accident, a sports result, a celebrity item) is left out. A summary is 1 to 3 Hebrew
sentences, facts only, the latest developments last; the first time a thread is named it says what the affair is.
Use only ids from the input.`

var threadsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"stories": map[string]any{"type": "array", "items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"story":   map[string]any{"type": "string"},
				"threads": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"story", "threads"},
		}},
		"newThreads": map[string]any{"type": "array", "items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string"},
				"summary": map[string]any{"type": "string"},
			},
			"required": []string{"name", "summary"},
		}},
		"summaries": map[string]any{"type": "array", "items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"thread":  map[string]any{"type": "string"},
				"summary": map[string]any{"type": "string"},
			},
			"required": []string{"thread", "summary"},
		}},
		"related": map[string]any{"type": "array", "items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"thread":  map[string]any{"type": "string"},
				"related": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"thread", "related"},
		}},
	},
	"required": []string{"stories", "newThreads", "summaries", "related"},
}

const groupingSystem = `You group Israeli news headlines into stories for a personal newspaper. A story is one specific event or
development reported by one or more sites: the same happening, decision, statement, incident or announcement, even in
different words, from another side of it, as a reaction to it, or as a teaser that doesn't name it. Headlines about
the same general subject but different happenings (two different shootings, two separate Knesset votes, the war's
different days) are different stories.

The input lists the stories so far (id, and a few of their headlines with their sites), then new headlines (id, site,
time, title and subtitle).

Answer:
- "headlines": every new headline, with its id and its "story": the id of the existing story it reports; or "new1",
  "new2", ... for a story that starts among the new headlines (the same label for all of its headlines); or "alone"
  when nothing else reports it yet.
- "merge": existing stories that turn out to be one story, as lists of their ids; usually none.`

var groupingSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"headlines": map[string]any{"type": "array", "items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":    map[string]any{"type": "string"},
				"story": map[string]any{"type": "string"},
			},
			"required": []string{"id", "story"},
		}},
		"merge": map[string]any{"type": "array", "items": map[string]any{
			"type":       "object",
			"properties": map[string]any{"stories": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
			"required":   []string{"stories"},
		}},
	},
	"required": []string{"headlines", "merge"},
}
