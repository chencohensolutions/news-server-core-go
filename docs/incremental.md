# Incremental story updates

A story that gains an article is written again from all of its articles today. The incremental update asks Gemini
only what the new articles change in the version before, and the server applies that. It is built to run **in shadow**
beside the whole rewrite, and to be replayed on the archive, before it writes a story anyone reads. Decided Nov 12,
2026.

Code: [summary/incremental.go](../summary/incremental.go) (the request, the rules), [summary/novelty.go](../summary/novelty.go)
(the checks by words), [summary/shadow.go](../summary/shadow.go), [summary/replay.go](../summary/replay.go),
[incremental.go](../incremental.go) (the commands).

## Why

Measured on the live archive, Nov 10 to 12, 2026 (`rewriteMinutes` 0):

- About 950 rewrites against 990 first versions in two and a half days. At least 29% of the rewrites add nothing:
  Gemini's "new" list is empty in 37% of them, and only says the version before again in another 29%. A whole rewrite
  also sometimes buries real news.
- Rewrites are 84% of the input tokens, and 78% of a rewrite's input is articles Gemini already read. 53% of the
  rewrites add exactly one article; 9% add none (the grouping only moved reports).
- About half the requests meet a 503, and about 175 stories wait each run.
- A check by words alone of whether a new article has news was replayed and is useless as a gate: the stories it
  would have skipped had news as often as the rest. So Gemini is still asked; it is asked less.

## The design

A changed story with a version before it goes one of three ways (`plan`):

1. **No new article** (reports left, or were regrouped): no request. The server keeps the text, drops the footnotes
   of the articles that left and the sentences left without any, and makes no version. If that leaves fewer than
   two sentences or drops more than half, the story is written whole.
2. **Written whole** (today's rewrite), when an update would stand on a text that no longer fits the story:
   - a sentence lost its last source while new articles joined;
   - two stories that were both written became one;
   - the story has twice the articles it was last written whole from (and at least three more);
   - the story grew past its sentences: one and a half times its length's most (30 for "full");
   - the tripwire, after an answer: see below.
3. **An incremental request** otherwise.

### The request

Many stories in one request: 8 to start with (`IncrementalPerRequest`), fewer when the text passes the request's
characters or the expected answer passes 12,000 (200 a story and 500 a new article). Each story shows:

- its title, and the version before as numbered sentences scoped to the story (`s3.4`), each with the names of the
  sites it still cites;
- the titles of the articles it already has, not their bodies;
- the new articles whole, as the rewrite cleans them (`a1`, `a2`, …), without the paragraphs an article the story
  already has gave word for word.

The instructions are their own ([incrementalSystem](../summary/incremental.go)): the rewrite's tell Gemini not to
base the story on the version before, and here the version before is what stays.

The answer, for each story, in this order:

| Key | What |
|---|---|
| `story` | its id |
| `unrelated` | new articles about another event: `id` and the `event`; when unsure, it belongs |
| `insert` | `after` (`top` or a sentence number), `kind`, `text`, `sources` (new articles' ids) |
| `replace` | `sentence`, `kind`, `text`, `sources` |
| `delete` | sentence numbers |
| `cite` | `sentence` and the new articles that state its fact too |
| `title` | a new title, or `""` for none |
| `contradictions` | sides citing new articles or a sentence of the story |

`kind` is `development`, `correction`, `detail`, `background` or `reaction`. Gemini never sends a sentence that
stays, so the old text and its footnotes are kept exactly.

**Key order.** Gemini writes an answer's keys in the order of the schema's `properties` ("the model will produce
outputs in the same order as the keys in the schema",
[structured output](https://ai.google.dev/gemini-api/docs/generate-content/structured-output); Gemini 2.0 needed an
explicit `propertyOrdering`, 2.5 and later don't). The rewrite's schema is a Go map, which goes out with its keys
sorted: that is why it decides `unrelated` last. The incremental schema is written out as JSON
(`json.RawMessage`) in the order above. `propertyOrdering` is not sent: the page lists it only for 2.0 and it is not
among the JSON Schema keywords the API documents for `responseJsonSchema`.

### The rules the server applies (`merge`)

- A new or replaced sentence must cite a new article of its own story, or it is dropped. An article found unrelated
  is no source.
- A sentence that only says the story again is dropped (`restates`): one earlier sentence has three quarters of its
  words, by the versions' word likeness, and its other words are in the story too. A number that sentence lacks, or
  a word the story lacks (a name, mostly), makes it news. So is a sentence that repeats another of the same answer.
- An old sentence changes only by `replace` or `delete`, by number. An operation on a number the story doesn't
  have, or another story's, is dropped. An insert whose place is unknown leads. Deleting more than half the story is
  refused.
- **A version is made only when a development or a correction survives, or a detail with a number the story lacks**
  (a toll, a sum, a date). Background, reactions and other details alone make none, and are then left out: the text
  stays to the letter. When a version is made, every surviving sentence goes in, and what was inserted and replaced
  is the version's update (`Update.Sentences`, with `Update.Kinds`).
- With or without a version, the new articles join the story's reports, and `cite` adds them to the footnotes of the
  sentences they state (up to five a sentence). The reports are what the story has seen; the footnotes are its
  sources.
- **The tripwire.** When no version is made, each new article that isn't unrelated is checked by words against the
  story and the articles it has: if five of its sentences or more, half of them at least, have no like sentence
  there, and two of its numbers appear nowhere, the story is written whole. An incremental story is never read
  whole again otherwise, and a miss would freeze.
- A cut answer is read up to the cut: the stories that are whole stand, the rest wait.

## Shadow

`incremental` is one of the summaries' settings in the database (`server_settings`, key `summaries`), beside the
ones the app changes. It is not in the GraphQL schema.

| Value | What |
|---|---|
| `off`, or missing | as before |
| `shadow` | the rewrite as before, and the incremental update beside it |
| `on` | not built: treated as off, and the command refuses it |

`shadowDaily` (same place) is the most requests a day the shadow sends: 48 unless set.

On the server (Windows: `news-server.exe`), in its folder, while it runs or not; the running server follows from its
next run:

```
news-server incremental shadow     turn the shadow on
news-server incremental            the mode, and what the shadow recorded in the last 7 days
news-server incremental off        turn it off
```

In shadow a run does everything it did, then (`shadow`):

- finds the stories that have a version before and no shadow row for their reports yet;
- records at once the ones without a new article and the ones to be written whole;
- sends **one** request with up to eight of the others, the latest changed first, only when the day spares it:
  the budget keeps two requests for each run left in the day after it (the rule of a run's extra requests), the day
  is not past `reserveAfter`, and the shadow has not sent `shadowDaily`. It runs after the stories and the threads,
  not at all when the run failed, and a busy Gemini (503) is not tried again in that run.

Every step is kept in `story_shadow` under the story's `StoryKey`, whatever came of it, so a story answered
"nothing new" is not asked again. A row holds the plan, what Gemini answered, the story after the step, whether a
version was made, the counts of what the rules dropped, and the request's size. The archive keeps the table
(`story_shadow/`, restored like the others); the database keeps 7 days. Nothing reads it for the app, and nothing
it does reaches `summaries`, `story_records`, `story_versions`, the threads or the grouping's unrelated flags.

The shadow keeps its own line of each story: a step starts from the shadow's own story when it has one, so sentences
pile up as they would with the mode on, and from the live version when it has none or the step before sent the story
to be written whole (the live version is that whole writing).

## The replay

`news-server eval-incremental` rebuilds from the archive the rewrites the server really made (a version, and the
articles that joined before the next one), sends them through the incremental request, and reports. It needs
`archive_dir`, the database (for the day's count) and, unless `-dry`, the Gemini key.

```
news-server eval-incremental -dry -days 2          build the requests of the last 2 days, print their sizes
news-server eval-incremental -days 2               the same, sent: at most 10 requests
news-server eval-incremental -days 2 -max-requests 30 -batch 8
```

| Flag | |
|---|---|
| `-days N` | the archive's last N days (0: all of it) |
| `-batch N` | stories a request (8) |
| `-max-requests N` | the most requests (10; 0: as many as it takes). Each is counted in the database's day, as the server's are, and it stops at `summary_budget` |
| `-dry` | nothing is sent |
| `-no-controls` | only real steps |

It reads `summaries/` and `story_versions/` (a version once), `headlines/` and `articles/`. A version written before
ids is joined to its story by its reports, as the server did then. It reports:

- how many rewrites need no request, how many are written whole and why, and how many are asked;
- of the steps answered: a version or none, against the live rewrite of the same step. The live rewrite "had new
  facts" when its own "new" list has a sentence the version before didn't already say (the same check as above).
  **Recall** is the share of those steps where the incremental update made a version;
- the sentences Gemini answered, and how many each rule dropped; the restatement rate;
- the sizes: characters and tokens in and out a story, beside the live rewrite's request and answer for the same
  step (in characters, and in tokens at the rate the incremental requests measured);
- three controls that need no labels, two in each request at places that move:
  - an article the story already has, shown as new: nothing may come of it;
  - an article of another story (the least alike of a few), shown as new: it must be found unrelated;
  - both, and the steps' agreement with the live rewrite, by the story's place in the request.

The live rewrite is not the truth: it misses news too, and lists restatements. A step where only the incremental
update found news is worth reading, not counting as an error.

## What turning it on still needs

`on` is refused until the shadow and the replay say it is good enough. Then:

- **The run.** First versions keep their own requests and go first; rewrites are packed into incremental requests.
  A story to be written whole goes through today's rewrite, with its new articles marked, and publishes a version
  only when the result has facts the version before lacks.
- **Bookkeeping.** A step that makes no version must still be saved in `summaries` under the new `StoryKey` (the
  version before, with the new reports and footnotes, its `writtenAt` unchanged), or `Run` asks about it every run;
  `previous`, `Latest` and `PruneSummaries` then find it as they find a rewrite. It adds no row to
  `story_versions`, and must not reset the story's place in the thread linking. `similarStory` reads whole
  stories and is not touched.
- **The app.** `Update.Kinds` is in the stored story and not in the GraphQL schema yet.
- **The flags.** The shadow does not save the unrelated articles for the grouping; the mode must.
