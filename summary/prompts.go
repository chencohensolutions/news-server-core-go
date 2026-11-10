package summary

// The instructions are in English, which the model follows best; everything it writes is Hebrew.

const writingRules = `Writing rules, all of them strict:
- Write in Hebrew, in the plain, neutral register of a news agency.
- Facts only: what happened, who, where, when, numbers, quotes attributed to who said them. No opinions, no
  commentary, no speculation, no adjectives that judge, nothing the sources don't say.
- Each fact once. When several sources report the same fact, write it in one sentence and cite all of them.
- Every sentence cites the articles it comes from, by their ids ("a3"). Cite only ids from the input, and only
  articles that state that fact. A fact no article states is not written.
- A claim, an accusation, a concern or an assessment is not a fact: write it only attributed to who says it ("לפי
  ...", "X אמר ש..."), or leave it out.
- Short sentences, newest and most important facts first. Every sentence is complete and ends with a period.`

const storiesSystem = `You write a personal newspaper from Israeli news sites. The input is several stories; each
story is a few articles from different sites about one event, each with an id in brackets, its site, time, title,
subtitle and the start of its body.

For every story, write:
- "title": a short factual Hebrew headline for the whole story.
- "sentences": the story as 2 to 6 sentences.
- "contradictions": only where the sources state different facts about the same thing (a number, a name, a time, a
  place, who did what). One sentence each, saying which source says what, citing both. Different emphasis or a fact
  only one source has is not a contradiction. Usually there are none: then an empty list.

` + writingRules + `

Answer for every story in the input, by its id ("s1"). A story's sentences cite only that story's articles.`

const digestSystem = `You write the front page of a personal newspaper from Israeli news sites: what is most covered
in the last hour. The input is the stories with the most new reports in the last hour, the most covered first, each
with its new and total reports and either its summary sentences (with the article ids they cite) or its headlines.

Write:
- "title": a short Hebrew headline, in the style of a newspaper's main headline, about the most covered story only.
- "sentences": 3 to 8 sentences, the most covered story first, one or two sentences per story, covering the stories in
  order as space allows. Cite the article ids the input gives for each fact.

` + writingRules

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
					"contradictions": map[string]any{"type": "array", "items": sentenceSchema},
				},
				"required": []string{"story", "title", "sentences", "contradictions"},
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
