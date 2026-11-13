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

### What the second replay found

36 real steps and 10 controls, every disagreement with the live rewrite read against the articles (Nov 12, 2026):

- Of 13 steps with real news the update made a version of 9 (69%), 10 with the tripwire; the live rewrite's own
  "new" list had 5 of them. One of 10 versions was false, and so were two of five "repeat" controls.
- **Gemini skips a story** (4 misses): it cites only, or answers nothing at all for it: no article is named
  anywhere. Hence the instruction to account for every new article, and the answer that names none is no answer
  (below).
- **Old material read as news** (3 false versions): an article published before the version before (a preview of
  the event, or the very articles the story was written from, in the control) gave a "detail" with numbers or a
  mislabelled "development". Hence the articles' time in the request and in the version rule.
- **An article of another story** was not found unrelated in two controls: an empty answer instead. Hence the
  sharper rule for `unrelated`.
- A story's place in the request made no difference.

### What the third replay found

A slice whose new articles were all published before the version before, after the articles' time went into the
version rule (Nov 13, 2026): no false version, the controls 4 of 4, no skipped answer. Two new losses:

- **Facts of older articles were lost**: real details (a sum paid, a name, an offer made) that the time rule kept
  from making a version never entered the story either. Hence the quiet update (below).
- **Sound sentences were dropped as garbled**: Gemini wrote sources into a sentence's text, after its period
  (`... (a1)`, `(site)`), as the request showed the old sentences' sites, and once wrote a site where an id belongs;
  and a sentence that quoted an Arabic word was taken for a garbled one. Hence the old sentences' sites before the
  sentence, the reading of what Gemini writes for sources, and the narrower check for garble (below).

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

- its title, and the version before as numbered sentences scoped to the story (`s3.4`). The names of the sites a
  sentence still cites stand before it, labelled: `s3.4 [מקורות: site, site] the sentence.` They were once after
  it in parentheses, and Gemini ended its own sentences the same way;
- the titles of the articles it already has, not their bodies;
- the new articles whole, as the rewrite cleans them (`a1`, `a2`, …), without the paragraphs an article the story
  already has gave word for word. An article's first line says, after the site and its time, whether it was
  published before or after the story was written: `[a2] site · 2026-11-10T19:20:00Z · פורסמה אחרי שהסיפור נכתב`,
  or `פורסמה לפני שהסיפור נכתב`; nothing when that isn't known (see the version rule).

The instructions are their own ([incrementalSystem](../summary/incremental.go)): the rewrite's tell Gemini not to
base the story on the version before, and here the version before is what stays.

The answer, for each story, in this order:

| Key | What |
|---|---|
| `story` | its id |
| `unrelated` | new articles about another event: `id` and the `event`. One that shares no person, place or event with the story is unrelated, though the grouping put it there; one that shares them belongs when unsure |
| `insert` | `after` (`top` or a sentence number), `kind`, `text`, `sources` (new articles' ids) |
| `replace` | `sentence`, `kind`, `text`, `sources` |
| `delete` | sentence numbers |
| `cite` | `sentence` and the new articles that state its fact too |
| `title` | a new title, or `""` for none |
| `contradictions` | sides citing new articles or a sentence of the story |

`kind` is `development`, `correction`, `detail`, `background` or `reaction`. Nothing an article published before the
story was written reports is a development: a fact of it about the event itself that the story lacks is a detail
(what a quiet update takes, below), its preview and run-up are background. A breakdown of figures the story already
sums up is a detail, not a development. Gemini never sends a sentence that stays, so the old text and its footnotes
are kept exactly.

A sentence's text is Hebrew only, and holds no id and no site's name: sources go in `sources`, as ids.

Gemini is told to **account for every new article**: each one's id is in `unrelated`, or among the sources of an
`insert`, a `replace` or a `cite`. Before it decides that an article only repeats the story, it checks the article
for what the story lacks: a new stage or decision, a person named for the first time, a number (a count, an age, an
amount, a date), an official response. Each is an insert; an article with none of them is cited.

**Key order.** Gemini writes an answer's keys in the order of the schema's `properties` ("the model will produce
outputs in the same order as the keys in the schema",
[structured output](https://ai.google.dev/gemini-api/docs/generate-content/structured-output); Gemini 2.0 needed an
explicit `propertyOrdering`, 2.5 and later don't). The rewrite's schema is a Go map, which goes out with its keys
sorted: that is why it decides `unrelated` last. The incremental schema is written out as JSON
(`json.RawMessage`) in the order above. `propertyOrdering` is not sent: the page lists it only for 2.0 and it is not
among the JSON Schema keywords the API documents for `responseJsonSchema`.

### The rules the server applies (`merge`)

- **What Gemini wrote for sources is read leniently** (`cited`). Brackets in a sentence's text that hold nothing
  but articles' ids, sentence numbers and the story's own sites, by id or by name, are taken out of it: `(a1)`,
  `[a1, a3]`, `(site)`, `[מקורות: site]`, inside the sentence or after its period. Other brackets are the
  sentence's own and stay (`(בן 34)`, a site the story doesn't have). A site written where an id belongs (among a
  sentence's sources, a citation's, an unrelated article's, a contradiction's side) is that site's new article in
  the story, when the request showed exactly one; a site with several names none of them.
- **A garbled sentence is dropped**: one with a word that has a Hebrew letter and an Arabic one (the model slips an
  Arabic letter into a Hebrew word), one with as many Arabic letters as Hebrew ones, an empty one, and one that
  doesn't end as a sentence (cut short). An Arabic word that stands apart (a quoted word, a name as it is written)
  is no garble. The whole rewrite's check is wider, and was left as it is: there any Arabic letter drops the
  sentence. It is what readers get today, and one replay is not enough to loosen it.
- A new or replaced sentence must cite a new article of its own story, or it is dropped. An article found unrelated
  is no source.
- A sentence that only says the story again is dropped (`restates`): one earlier sentence has three quarters of its
  words, by the versions' word likeness, and its other words are in the story too. A number that sentence lacks, or
  a word the story lacks (a name, mostly), makes it news. So is a sentence that repeats another of the same answer.
- An old sentence changes only by `replace` or `delete`, by number. An operation on a number the story doesn't
  have, or another story's, is dropped. An insert whose place is unknown leads. Deleting more than half the story is
  refused.
- **A version is made only when a development or a correction survives, or a detail with a number the story lacks**
  (a toll, a sum, a date), **and one of that sentence's articles was published after the version before was
  written.** Background, reactions and other details alone make none, and are then left out. When a version is
  made, every surviving sentence goes in, the ones from older articles too, and what was inserted and replaced is
  the version's update (`Update.Sentences`, with `Update.Kinds`).
- **A quiet update.** When no version is made, a sentence of a kind that makes one (a development, a correction, a
  detail with a number the story lacks) that only its articles' time kept from it, every one of them published
  before the version before, goes into the story all the same: inserted or replaced, with its sources. It makes no
  version and nothing in "what's new": the title, `writtenAt` and the updates stay, and nothing is deleted (a
  deletion is only ever part of a version).
  - It takes exactly what would have made a version had the article been new, and no more. A detail still needs its
    number, and background and reactions never go in quietly: an article that joins late would otherwise pad the
    story with what a new article may not add either. The cost: a name without a number that Gemini calls a detail
    stays out, as it does from a new article when no version is made.
  - A replacement goes in quietly only when one of its articles is known to be as late as every source of the
    sentence it replaces (the site's time, or the server's first sight). An older article must not undo what a later
    one told, and without a version nothing would show that it did. Otherwise it is left out.
- **When an article was published** (`Step.fresh`) is the site's time for it (`publishedAt`) against the version
  before's `writtenAt`. A site that gives no time: when the server first saw the article. With neither, it is not
  known, and the article counts as published after: the rule keeps old articles from making a version, and must
  not keep news from one. One more case counts as after: an article published in the ten minutes before the
  version that the server first saw only after it. The version was written from the reading of the sites before
  it, which that article was not in yet.
- **An answer that names none of the story's new articles is no answer** (`Merged.Skipped`): not as unrelated, and
  not among the sources of an insert, a replace, a cite or a contradiction, whatever the other rules make of those.
  It is not "nothing new": the story is asked about again (see the shadow), and the replay counts it apart.
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
version was made, how many sentences went in quietly (`quiet`, never with `version`: a quiet update, told from a
version and from nothing new), the counts of what the rules dropped, and the request's size. The report of `news-server incremental`
counts the steps without a version, and how many of them were updated quietly.

A story Gemini skipped (its answer names none of the new articles) is the exception. Its row is kept with the
answer, `skipped` 1 and `again`: it is no step, so the shadow's line of the story stays where it was, and the next
run asks about the story again, first in its request, before the stories that changed since. Skipped a second time,
it is left to be written whole (`again` off, the reason in `reason`), as the mode would do; it is not asked a third
time. The archive keeps the table
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
news-server eval-incremental -days 2 -until 2026-11-10 -sample 120 -seed 1 -max-requests 20 -cases ../replay-cases.jsonl
                                                   120 steps spread over two days that are over, each written out to read
```

| Flag | |
|---|---|
| `-days N` | the archive's last N days (0: all of it) |
| `-batch N` | stories a request (8) |
| `-max-requests N` | the most requests (10; 0: as many as it takes). Each is counted in the database's day, as the server's are, and it stops at `summary_budget` |
| `-until day` | the last day replayed (UTC, `2026-11-10`), which `-days` counts back from; the archive's last when left out. The archive's last day still grows, so only a day that is over gives the same steps on every run |
| `-sample N` | ask about N of the steps that take a request, spread over all of them in time: they are cut into N equal runs, oldest first, and one of each is asked. Without it: all of them, the oldest first, so `-max-requests` stops in the first hours |
| `-seed N` | which one of each run `-sample` picks (1). The same archive days, sample and seed pick the same steps |
| `-offset N` | skip the first N stories to ask about, controls among them. A replay that stopped (at `-max-requests`, the day's budget, a refusal) prints the offset that goes on from there; the days, `-batch`, `-sample` and `-seed` must be the same |
| `-cases file` | write the cases to `file` (below). Not with `-dry` |
| `-dry` | nothing is sent |
| `-no-controls` | only real steps |

It reads `summaries/` and `story_versions/` (a version once), `headlines/` and `articles/`. A version written before
ids is joined to its story by its reports, as the server did then. It reports:

- how many rewrites need no request, how many are written whole and why, and how many are asked;
- how many of the real steps' new articles were published after the version before, before it, or at no known
  time (dry too: it shows how much the version rule's time has to stand on);
- the steps **skipped by Gemini**: the answer names none of the story's new articles. They are neither a version
  nor "nothing new", and are in none of the counts that follow; and how many new articles the answers named
  nowhere in all;
- of the other steps answered: a version or none, against the live rewrite of the same step. The live rewrite "had
  new facts" when its own "new" list has a sentence the version before didn't already say (the same check as
  above). **Recall** is the share of those steps where the incremental update made a version. It is given twice:
  without the skipped steps, and with the skipped steps whose live rewrite had new facts counted as misses (and how
  many of those the tripwire would write whole);
- the steps where the live rewrite had new facts and no version was made, by what Gemini answered: sound sentences
  that made no version (left out, or added quietly), only sentences the other rules dropped, or no sentence
  (the model did). And how many of them the tripwire would send to be written whole. So recall is given three more
  times: counting the steps with sound sentences that made no version ("Gemini found facts", a version or not),
  counting the tripwire's whole writings (a whole writing of a step whose live rewrite had new facts makes a
  version), and counting both. The same two counts are given for the steps where the live rewrite had nothing:
  there a whole writing is a request spent for no version;
- the steps without a version where a sentence of a kind that makes one was kept from it by its articles' time
  (all published before the version before), apart by whether the live rewrite had new facts: the rule's cost, and
  what it saved;
- the **quiet updates**: the steps without a version where such sentences went into the story, apart the same way.
  They are not versions, and are in no recall but the one that counts what Gemini found;
- the sentences Gemini answered, and how many each rule dropped; the restatement rate; the ones in versions, the
  ones added quietly with no version made, and the ones left out with no version made, each by kind;
- the sizes: characters and tokens in and out a story, beside the live rewrite's request and answer for the same
  step (in characters, and in tokens at the rate the incremental requests measured);
- three controls that need no labels, two in each request at places that move:
  - an article the story already has, shown as new: no version may come of it (it is older than the story, so the
    version rule holds whatever is written from it; found unrelated, it still fails). A quiet update may: how many
    of these stories took a sentence from the article they were written from is counted, as the padding a quiet
    update does;
  - an article of another story (the least alike of a few), shown as new: it must be found unrelated;
  - of each, how many answers named no new article at all: a skipped repeat passes (nothing came of it), a skipped
    stranger fails;
  - both, and the steps' agreement with the live rewrite, by the story's place in the request.

The live rewrite is not the truth: it misses news too, and lists restatements. A step where only the incremental
update found news is worth reading, not counting as an error.

### The cases

`-cases file` writes what the numbers are made of, to read a disagreement and judge who is right: a line of JSON
for every real step answered, for every control answered wrong, and for every control of the first kind that a
quiet update changed (`summary.ReplayCase`). The file holds the sites'
headlines and articles as they are, so it is refused inside the archive folder and inside a repository (a folder
with `.git`, or under one): keep it beside them. It is written anew, and added to with `-offset`.

| Field | |
|---|---|
| `n` | the story's number among the ones to ask about, from 1: what `-offset` counts, and the same on every run with the same days, `-batch`, `-sample` and `-seed` |
| `story`, `at` | the story's id, and the step's time: when the live rewrite wrote the next version |
| `kind` | `missed` (the live rewrite had new facts, no version here), `extra` (a version here, none there), `agree-version`, `agree-none`; `skipped` (the answer names none of the story's new articles: `live` tells what was skipped); `control-repeat` and `control-stranger` for a control answered wrong; `control-repeat-quiet` for one answered right (no version) whose story took a sentence quietly from the article it already had |
| `request`, `position`, `id` | where it was asked: the run's request, its place in it, and its id there (`s3`) |
| `title`, `prevAt`, `prev` | the version before: its title, when it was written, and its sentences as the request numbered them (`n`, `text`, `sites`) |
| `old` | the articles the story had: `site`, `url`, `title` (the request shows no more of them) |
| `new` | the articles shown as new: `id` in the request (`a4`), `site`, `url`, `published`, `when` (`after` or `before` the version before, as the request said it; none when not known), `title`, `subtitle`, and `body` exactly as the request showed it (cleaned, without the paragraphs the story's articles gave, cut to its length; none when only the headline was shown) |
| `target` | a control's article, by its id |
| `answer` | what Gemini answered for the story: `unrelated`, `insert`, `replace`, `delete`, `cite`, `title`, `contradictions` |
| `merge` | what the server made of it: `version` (one was made), `tripwire` and `tripwireWhy` (it would be written whole), `unrelated`, `unmentioned` (the new articles the answer names nowhere) and `skipped` (that is all of them), `stats` (the counts of the report), and `notes` |
| `merge.notes` | one for each answered operation: `op`, `at`, `kind`, `text`, `sources`, and its `outcome`: `applied`, `quiet` (in the story without a version: a quiet update), `left out` (sound, and the answer makes no version) or `dropped`, with `why` (says again what `s3.1` does, cites no new article of the story, garbled or cut short, ...). `text` and `sources` are as Gemini answered them, with what was written into them. `makesVersion` marks a sentence the version rule takes, `old` one of such a kind whose articles were all published before the version before: `quiet` when no version was made, unless it is a replacement older than its sentence |
| `live` | the live rewrite's version of the step (none for a control): `at`, `title`, `new` (the sentences it gave as new, less the ones the version before said already: those are in `restated`), and `sentences`, all of it. A sentence has its `sites` and, in `articles`, the step's new articles among its sources: a "new" sentence without one stands on articles the story had |

To read the misses, for one: `jq -c 'select(.kind == "missed")' file`; the skipped: `select(.kind == "skipped")`;
the sentences the articles' time held back: `select(any(.merge.notes[]; .old))`; the quiet updates:
`select(any(.merge.notes[]; .outcome == "quiet"))`.

## What turning it on still needs

`on` is refused until the shadow and the replay say it is good enough. Then:

- **The run.** First versions keep their own requests and go first; rewrites are packed into incremental requests.
  A story to be written whole goes through today's rewrite, with its new articles marked, and publishes a version
  only when the result has facts the version before lacks.
- **Skipped stories.** The shadow's rule becomes the run's: a story whose answer names none of its new articles
  keeps its version, is asked about again in the next run, and after a second such answer is written whole.
- **Bookkeeping.** A step that makes no version must still be saved in `summaries` under the new `StoryKey` (the
  version before, with the new reports and footnotes and the sentences of a quiet update, its `writtenAt`
  unchanged), or `Run` asks about it every run;
  `previous`, `Latest` and `PruneSummaries` then find it as they find a rewrite. It adds no row to
  `story_versions`, and must not reset the story's place in the thread linking. `similarStory` reads whole
  stories and is not touched.
- **The app.** `Update.Kinds` is in the stored story and not in the GraphQL schema yet. After a quiet update the
  story's text has sentences that are in none of its updates and in no row of `story_versions`: what shows a
  version's changes must take them for old text.
- **The flags.** The shadow does not save the unrelated articles for the grouping; the mode must.
