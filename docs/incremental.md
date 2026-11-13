# Incremental story updates

A story that gains an article is written again from all of its articles today. The incremental update asks Gemini
only what the new articles change in the version before, and the server applies that. It is built to run **in shadow**
beside the whole rewrite, and to be replayed on the archive, before it writes a story anyone reads. Decided Nov 12,
2026.

Code: [summary/incremental.go](../summary/incremental.go) (the request, the rules), [summary/novelty.go](../summary/novelty.go)
(the checks by words), [summary/facts.go](../summary/facts.go) (a sentence's numbers, names and time words),
[summary/shadow.go](../summary/shadow.go), [summary/replay.go](../summary/replay.go),
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

### What the fourth replay found

66 real steps and 22 controls, judged by hand (Nov 13, 2026): of the steps with real news the update made a version
of 80%, 88% with the tripwire, where the live rewrite's own "new" list had 56%; 3 of 23 versions were false; the
controls 21 of 22. What was left:

- **News stated in a new article's title or subtitle was not proposed** (3 or 4 misses: a dismissal in a subtitle,
  an approval in a title, a rift in a subtitle), where the body mostly repeated the story. Hence the instruction to
  read the title and the subtitle first.
- **Another event's fact, and wrong**: an insert stated a fact of a related but separate event the article mentions
  beside the story's, with a count and nationalities no article gives. Hence the instruction on separate events and
  on copying names and numbers, and the check of a sentence's numbers against the articles it cites.
- **False versions**: a detail whose only "new number" was a date or a year, and a development that said an old
  sentence again with "today" added. **Missed versions**: a detail that named a person for the first time, with no
  number. Hence the three changes to the version rule and the restatement check.
- **Quiet updates padded and rewrote**: a story's own article, shown again, added sentences; an older article
  replaced a sentence; several sentences went in at once. Hence a quiet update only inserts, two sentences at most,
  and never from the story's own articles alone.

### What the fifth replay found

All 120 real steps of the sample and 40 controls, judged by hand (Nov 13, 2026): of the steps with real news the
update made a version of 87% (94% of the ones with significant news), where the live rewrite's own "new" list had
69%; 6 of 53 versions were false (11%); the repeat controls 20 of 20; of 20 strangers 16 were found unrelated and 4
were skipped, to be asked again. What was left:

- **Side figures made versions** (5 of the 6 false ones): a detail with a number beside the story's event: a related
  country's figure, another day's, an option's amount, a project's size, one more signer of a bill. Hence a detail
  makes no version at all, and the kinds are told apart by the event's main facts.
- **Official acts were answered as reactions**, which make no version: a letter, an appeal, a decision, an order, a
  leader's formal statement. Hence an official act is a development.
- **Corrections were written as inserts**: another number for a fact the story states, beside the old sentence.
  Hence a correction is always a replacement, and the server makes such an insert one.
- **Inserts with factual errors**, a count of a country's people that no article gives among them. Hence the check
  of such a count written in a word. A wider check was tried and left out: dropping a sentence with two words in a
  row that neither the articles it cites nor the story have, as a made-up name would be. Hebrew inflects and a
  sentence rewords its article (`נעצרו בחשד` where the article has `עצרה` and `שנחשדו`): of 8 sound made-up
  paraphrases it dropped 6. It would cost the recall the round measured.

### What the sixth replay found

The same 120 steps and 40 controls, on round 5's changes (Nov 13, 2026). It was worse than round 5:

- **Gemini skipped 13 of the 120 stories**, against 2, clear news among them (an incident, an appointment approved),
  and 3 of the repeat controls came back empty. Round 5's 6 false versions were gone, 3 of them only because the
  story was skipped.
- **31 of 107 answered steps made a version**, against 53 of 118: 35% of the steps where the live rewrite had new
  facts, against about 69%.
- **37 inserted sentences were called a detail**, 28 of them from an article published after the version before,
  and real developments were among them: more defendants on an indictment, three arrested where the story said two,
  a witness's testimony with a court's ruling, a company's layoffs. A detail made no version in 14 steps where it
  would have, and 31 went in quietly.
- **No correction was made**: the three arrested were inserted as a detail beside the two, and left out.

What was changed, and why:

- **The instructions are round 5's again, with one sentence more.** The request, its schema and the order of its
  keys were the same in both rounds: only the instructions differed, in four places, so the skips come from them.
  Which of the four is not measured: the replay runs on the server's archive with its key, and none was run on each
  alone. Read against the answers, the likeliest is the kinds, rewritten from six lines to nineteen. A development
  became "news of the story's main event" and "a central fact of it", a detail "a fact beside the main event" that
  "a reader who has the story misses no news without", and the model was told that "a detail alone never makes the
  story news again". A new article's fact that was neither the main event's nor beside it (a follow-up, a second
  incident) had no kind left, and with nothing to insert and nothing the story says to cite, the answer named no
  article: a skipped story. The same narrowing is what moved real developments to "detail". The two sentences that
  told it what not to insert (another value for a fact "is not inserted beside it", a correction is "never an
  insert") made no correction at all. So all four are taken back, and what is kept is the one line round 5 needed:
  an official act is a development. Which kind a sentence gets is no longer what the model is told the version rule
  is; the server decides that (below).
- **A detail makes a version by what it adds** (the owner's choice: catch everything, and about one thin version in
  five is acceptable): a number the story lacks, a person or a body it doesn't name, an outcome or a decision it
  doesn't tell, from an article published after the version before. Telling a side figure (another country's,
  another night's, a project's size, an option's amount) from the story's own by its words was worked through and
  left out: the sentence that gives another country's figure and the one that gives the story's new toll have the
  same words but for one, and Hebrew gives no local sign of which word is another place. A check that the sentence
  shares most of its words with the story lets both through or keeps both out.
- **What goes into the story is not what makes a version.** Every sound sentence goes in, a version or not, and as
  many as there are; the version rule only decides whether readers are told.
- **The correction check reads a count in a word as a number, and words that tell nothing as nothing** (below).
- **The headline as a check of recall**: a second tripwire (below).
- **Articles are sent whole**: 12,000 characters at most, where 4,000 cut one article in ten and about 12% of the
  articles' text was never sent. In a whole writing too.

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
  already has gave word for word: up to 12,000 characters each (the first 600 when full articles are off), as in a
  whole writing. A request that would pass its characters takes fewer stories, and no article is cut to make room
  for another story; only a story whose new articles pass 120,000 characters together has its longest ones
  shortened, the longest first. Under an article's first line are its title (`כותרת:`), its subtitle (`משנה:`; the
  site's summary, `תקציר:`, when it has none) and its body (`גוף:`), a line each, and the instructions name the
  labels. An article's first line says, after the site and its time, whether it was
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

`kind` is `development`, `correction`, `detail`, `background` or `reaction`, each told in a line, as in round 5:

- a **development** is something that happened or became known since the story was written, a new stage of the
  event. **An official act is a development too**, also when it is made of words: a letter sent, an appeal or a
  petition filed, a decision, an order, a formal statement or announcement by a leader, a minister or an official
  body (round 5: they were answered as reactions);
- a **correction** is a fact of the story that changed or was wrong;
- a **detail** is a fact about what the story already tells that it lacks: a name, a number, a place, a time, a
  quote. A breakdown of figures the story already sums up is a detail, not a development;
- **background** is what was before the event; a **reaction** is what someone said about it.

The instructions say no more of them than that, and nothing of which kinds make a version: round 6 did (a detail
"never makes the story news again", a correction is "never an insert"), and Gemini skipped stories and called news
a detail. What is wrong with a kind is mended by the server's rules.

Nothing an article published before the story was written reports is a development: a fact of it about the event
itself that the story lacks is a detail, its preview and run-up are background. Gemini never sends a sentence that
stays, so the old text and its footnotes are kept exactly.

A sentence's text is Hebrew only, and holds no id and no site's name: sources go in `sources`, as ids.

Gemini is told to **account for every new article**: each one's id is in `unrelated`, or among the sources of an
`insert`, a `replace` or a `cite`. Before it decides that an article only repeats the story, it reads the article's
title and subtitle first: they state its main news, and what they state that the story lacks is an insert, even
when the body mostly repeats the story. Then it checks the body for what the story lacks: a new stage or decision, a
person named for the first time, a number (a count, an age, an amount, a date), an official response. Each is an
insert; an article with none of them is cited.

**Only the story's event.** The facts of a related but separate event that an article mentions beside it (another
indictment, another company's results, an earlier incident) are not inserted, and its people, numbers and places
are never given to the story's event. Names, numbers and nationalities are copied exactly as the article gives
them: not rounded, added up, converted or guessed, and never taken from another story of the request.

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
- **A sentence with a number that none of the articles it cites states is dropped** (`unbacked`): each number of the
  sentence must be in the text the request showed of those articles (title, subtitle or summary, body), or in the
  story already. It is read loosely, so that a true sentence is not lost: `1,200` and `1200`, a number written in
  words (`שלושה`, `עשרים וחמישה`, `מאתיים`, an ordinal), another scale (`300 אלף` and `300,000`, `מיליון וחצי` and
  `1.5 מיליון`), a part of a time or a date. A date and a year are not checked: Gemini works them out from
  "yesterday". **A count written in a word is checked only when it counts a country's people**, three or more
  (`שלושה איראנים`, `חמישה תיירים סינים`: the nationalities are a list, `nationals`): any other count in a word is as
  often one Gemini rightly works out (three ministers, of three names; three days, of a date) or part of a place's
  name. Names are not checked: Hebrew marks none, and a sentence rewords its article (round 5, above).
- A sentence that only says the story again is dropped (`restates`): one earlier sentence has three quarters of its
  words, by the versions' word likeness, and its other words are in the story too. A number that sentence lacks,
  in digits or in a word, or a word the story lacks (a name, mostly), makes it news. So is a sentence that repeats
  another of the same answer.
  **A sentence is read without its time words too** (`היום`, `אמש`, `הבוקר`, `הערב`, `הלילה`, `כעת`, `עתה`): one
  that is an old sentence with such a word added tells nothing new, whatever its kind.
- An old sentence changes only by `replace` or `delete`, by number. An operation on a number the story doesn't
  have, or another story's, is dropped. An insert whose place is unknown leads. Deleting more than half the story is
  refused.
- **An inserted sentence that gives another number for an old sentence's fact replaces it** (`contradicted`), as
  a correction, whatever Gemini called it: the story must not say 2 and 3. It is the same sentence but for the
  number: the old sentence has every content word of it, by the word likeness; it has three quarters of the old
  one's; each has a number the other doesn't state; and neither a date or a year the other lacks.
  - **A count in a word is a number** (`counts`): `שניים`, `שני`, `שתיים`, `שתי`, `שלושה`, `שלוש`, `שלושת` … `עשרה`,
    `עשר`, and the tens, against each other and against digits (`שלושה` beside `שני` or `2`; `שתי` and `שניים` are
    one number). A count is compared with the other sentence's counts, and not with any word of it that a number is
    written with: `ביום שלישי` states no three and `השני` no two, so a sentence with a day in it can still be
    corrected. `שלוש` and `שלושים` are two numbers, though the word likeness takes them for one word. No count:
    a word beside another number's (`שלוש עשרה`, `עשרים ושלושה`), an hour or a day (`בשעה שבע`, `ביום שני`), `מצד
    שני`, and `שנים`, which is years.
  - **Content words** are a sentence's words without its numbers and without the words that tell nothing
    (`functionWords`: `את`, `של`, `גם`, `כבר`, `עד כה`, `בסך הכול`, …), so `שלושה חשודים נעצרו עד כה` corrects `שני
    חשודים נעצרו`. `לא` is no such word: it turns the fact over.
  - It is narrow on purpose, as the old sentence is lost: one content word more (another place, "killed" for
    "hurt") or "more" (`עוד`, `נוספים`) makes it another fact, and background and reactions stay what they are. A
    sentence that gives the story's count again in other words (`המשטרה עצרה שלושה חשודים`) is therefore no
    replacement: it is inserted beside the old one, and its count makes a version (below).
  - Such a correction makes a version when its article was published after the version before, and goes in with a
    version another sentence makes; otherwise it stays out, and never beside the old sentence: no replacement goes
    in without a version. A `replace` answered as a detail that gives another number than its sentence is a
    correction too.
- **A version is made when one of these survives, with an article published after the version before among its
  sources**:
  - a development or a correction;
  - **a detail that adds what the story lacks** (`MergeNote.News`):
    - a **number** (`number`): in digits, a toll, a sum, a count, an hour. **A date or a year is not one**:
      `14.3.2021`, `12/11`, a day before a month's name (`ב-12 בנובמבר`), and a number from 1900 to 2099 alone
      (unless "about": `כ-2000`). A count in a word is one when it is news to the story (`recount`): the story
      counts the same thing otherwise (`שלושה חשודים` where it has `2 חשודים`), the sentence counts more of
      something (`שני נאשמים נוספים`), or it is three or more and the story states no such number. `שני` alone is
      two of anything (`שני הצדדים`);
    - a **person or a body** (`actor`). The person is a full name: two words in a row, neither of them in the
      story or its title, with a sign that they are a name (`newName`): a title or a rank directly before them
      (`ח״כ`, `ד״ר`, `עו״ד`, `השופטת`, `סא״ל`, …), or an age in brackets directly after them (`(34)`), in the
      sentence itself or where the article it cites has the same two words. The body is a word that stands only
      before a body's name (`חברת`, `עיריית`, `מועצת`, `עמותת`, `מפלגת`, `ועדת`, `אוניברסיטת`, `סיעת`, `לשכת`) and,
      directly after it, a word the story lacks (`newBody`). Both are narrow: two new words in a row are what most
      new facts have, and `בנק`, `משרד` and `ארגון` stand before other words as often;
    - an **outcome or a decision** (`outcome`): a word of what a court, an authority or an employer did, or of
      what became of someone (`הוחלט`, `אישר`, `דחה`, `קבע`, `פסק`, `הורה`, `הורשע`, `זוכה`, `נגזר`, `הוארך`,
      `שוחרר`, `נעצר`, `הוגש`, `מונתה`, `פוטר`, `פיטורים`, `התפטר`, `הושעה`, …, each form written out) that the
      story doesn't have.
  Background, reactions and other details make none. **No side figure is told apart**: a detail with another
  country's figure, a project's size or one more signer of a bill makes a version too (round 5's five false ones).
  It was the owner's choice after round 6: a thin version is the lesser loss, and no local sign told them apart
  (above). `MergeStats.ByDetail` counts the versions that only details made, to read for the thin ones.
- **What goes into the story is not what makes a version.** Every sentence that survived the rules above goes into
  the story's text, with its sources, whatever its kind:
  - **with a version**, all of them, and what was inserted and replaced is the version's update
    (`Update.Sentences`, with `Update.Kinds`);
  - **without one, quietly**: the inserted ones go in, as many as there are (until round 6 two at most, and only
    developments from older articles and details with a number or a name). Nothing else changes: the title,
    `writtenAt` and the updates stay, and nothing is in "what's new". So an article from before the version before
    makes no version and its facts are in the story all the same, and so are background, reactions and plain
    details of any article.
  - **Without a version a sentence is only inserted**: never a replacement, never a correction, and nothing is
    deleted. The story must not change what it tells without a version to show that it did.
  - **Never from the story's own articles alone.** A sentence that cites only articles the version before was
    written from, shown as new again (the replay's control does that), stays out: the story was written from them,
    and they add nothing now.
  - A story fills sooner this way, and one past its sentences is written whole (the design, above).
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
- **The tripwire.** When no version is made, the story's new articles that aren't unrelated are checked by words
  against the story and the articles it has (their titles, subtitles and bodies), and the story is written whole
  when either holds. An incremental story is never read whole again otherwise, and a miss would freeze.
  - **An article that is clearly news**: five of its sentences or more, half of them at least, have no like
    sentence there, and two of its numbers appear nowhere.
  - **The headline** (`headlineNews`): an article published after the version before, from which nothing went
    into the story (no sentence that cites it, with a version or quietly), has a title or a subtitle that names
    what the story lacks. The title and the subtitle state an article's main news, and round 4 missed news that
    stood only there. It names one of: a number that is no date, year, hour or age; a word of an outcome or a
    decision (the same list as above); or **two** content words of four letters or more, the common ones aside
    (who said and how it is presented: `אמר`, `דיווח`, `צפו`, `תיעוד`, `בלעדי`, the time words, …). One plain word is
    not enough: Hebrew inflects, the word likeness knows only joined letters and endings, and a headline in other
    words has one new word more often than not. Each story it flags is a whole writing's request, so it is
    conservative; `headlineWords` in [novelty.go](../summary/novelty.go) is the number to change. Not for an
    article the story was written from, shown again. The replay counts it apart, as the headline tripwire.
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
version was made, how many sentences went in quietly (`quiet`, never with `version`: told from a version and from
nothing new), the counts of what the rules dropped and of what stayed out without a version (`MergeStats`), and the
request's size. The report of `news-server incremental`
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
- the steps without a version where a development or a correction was kept from making one by its articles' time
  (all published before the version before), apart by whether the live rewrite had new facts: the rule's cost, and
  what it saved;
- the **versions that only details made**: no development and no correction, but a detail with a number, a name
  or an outcome the story lacks, from an article published after the version before. Apart the same way: where the
  live rewrite had new facts they are what the rule finds, and where it had none they are the ones to read for a
  thin version (a side figure);
- the **headline tripwire**: of the steps the tripwire would write whole, the ones the headline check sent, apart
  the same way;
- the steps without a version where **sentences went into the story quietly**, apart the same way. They are not
  versions, and are in no recall but the one that counts what Gemini found;
- the sentences Gemini answered, and how many each rule dropped (among them the ones with a number none of their
  articles states, and how many of those were a count of a country's people in a word); the restatement rate, and
  how many of the restatements had only a time word more; the inserted sentences made the correction of the
  sentence they gave another number for; the developments and corrections from older articles; the details that
  add a number, a name or an outcome, how many of them stand on an article published after the version before, how
  many add a name alone or an outcome alone, and the ones that add nothing because their only new numbers are dates
  or years; the sentences in versions, the ones added quietly with no version made, and the ones kept out with no
  version made (from the story's own articles, replacements and corrections), each by kind;
- the sizes: characters and tokens in and out a story, beside the live rewrite's request and answer for the same
  step (in characters, and in tokens at the rate the incremental requests measured);
- three controls that need no labels, two in each request at places that move:
  - an article the story already has, shown as new: no version may come of it (it is older than the story, so the
    version rule holds whatever is written from it; found unrelated, it still fails), and nothing of it goes into
    the story quietly. How many of these answers wrote a sound sentence from the article the story was written
    from is counted, as the padding that rule keeps out;
  - an article of another story (the least alike of a few), shown as new: it must be found unrelated;
  - of each, how many answers named no new article at all: a skipped repeat passes (nothing came of it), a skipped
    stranger fails;
  - both, and the steps' agreement with the live rewrite, by the story's place in the request.

The live rewrite is not the truth: it misses news too, and lists restatements. A step where only the incremental
update found news is worth reading, not counting as an error.

### The cases

`-cases file` writes what the numbers are made of, to read a disagreement and judge who is right: a line of JSON
for every real step answered, for every control answered wrong, and for every control of the first kind whose
answer wrote a sound sentence from the story's own article (`summary.ReplayCase`). The file holds the sites'
headlines and articles as they are, so it is refused inside the archive folder and inside a repository (a folder
with `.git`, or under one): keep it beside them. It is written anew, and added to with `-offset`.

| Field | |
|---|---|
| `n` | the story's number among the ones to ask about, from 1: what `-offset` counts, and the same on every run with the same days, `-batch`, `-sample` and `-seed` |
| `story`, `at` | the story's id, and the step's time: when the live rewrite wrote the next version |
| `kind` | `missed` (the live rewrite had new facts, no version here), `extra` (a version here, none there), `agree-version`, `agree-none`; `skipped` (the answer names none of the story's new articles: `live` tells what was skipped); `control-repeat` and `control-stranger` for a control answered wrong; `control-repeat-reshown` for one answered right (no version, the story as it was) whose answer wrote such a sentence from the article the story already had: the rules kept it out |
| `request`, `position`, `id` | where it was asked: the run's request, its place in it, and its id there (`s3`) |
| `title`, `prevAt`, `prev` | the version before: its title, when it was written, and its sentences as the request numbered them (`n`, `text`, `sites`) |
| `old` | the articles the story had: `site`, `url`, `title` (the request shows no more of them) |
| `new` | the articles shown as new: `id` in the request (`a4`), `site`, `url`, `published`, `when` (`after` or `before` the version before, as the request said it; none when not known), `title`, `subtitle` (or `summary`, the site's, which the request showed when there is no subtitle), and `body` exactly as the request showed it (cleaned, without the paragraphs the story's articles gave, cut to its length; none when only the headline was shown) |
| `target` | a control's article, by its id |
| `answer` | what Gemini answered for the story: `unrelated`, `insert`, `replace`, `delete`, `cite`, `title`, `contradictions` |
| `merge` | what the server made of it: `version` (one was made), `tripwire` and `tripwireWhy` (it would be written whole; `headlineTripwire` when the headline check is why), `unrelated`, `unmentioned` (the new articles the answer names nowhere) and `skipped` (that is all of them), `stats` (the counts of the report), and `notes` |
| `merge.notes` | one for each answered operation: `op`, `at`, `kind`, `text`, `sources`, and its `outcome`: `applied`, `quiet` (in the story without a version), `left out` (sound, and out of the story because no version was made: a replacement or a correction, or a sentence from the story's own articles) or `dropped`, with `why` (says again what `s3.1` does, with a time word more or not; cites no new article of the story; states a number that none of the articles it cites does; garbled or cut short, ...). `text` and `sources` are as Gemini answered them, with what was written into them. `makesVersion` marks a sentence the version rule takes: a development, a correction, or a detail that adds, from an article published after the version before. `adds` marks a detail that adds what the story lacks, and `news` what: `number`, `actor` (`name` is the person or the body) or `outcome`; `dated` a detail that adds nothing because its only new numbers are dates or years. `old` marks a development or a correction whose articles were all published before the version before. `corrects` is the sentence of the story (`s3.2`) an inserted sentence gave another number for: it was made that sentence's replacement, and its `kind` is `correction` |
| `live` | the live rewrite's version of the step (none for a control): `at`, `title`, `new` (the sentences it gave as new, less the ones the version before said already: those are in `restated`), and `sentences`, all of it. A sentence has its `sites` and, in `articles`, the step's new articles among its sources: a "new" sentence without one stands on articles the story had |

To read the misses, for one: `jq -c 'select(.kind == "missed")' file`; the skipped: `select(.kind == "skipped")`;
the sentences the articles' time held back: `select(any(.merge.notes[]; .old))`; the steps where sentences went in
quietly: `select(any(.merge.notes[]; .outcome == "quiet"))`; the sentences dropped for a number:
`select(any(.merge.notes[]; (.why // "") | startswith("states a number")))`; the versions only details made, where
the live rewrite had nothing (the thin ones): `select(.kind == "extra" and .merge.stats.byDetail == 1)`; the
headline tripwire: `select(.merge.headlineTripwire)`; the inserts made corrections:
`select(any(.merge.notes[]; .corrects))`.

## What turning it on still needs

`on` is refused until the shadow and the replay say it is good enough. Then:

- **The run.** First versions keep their own requests and go first; rewrites are packed into incremental requests.
  A story to be written whole goes through today's rewrite, with its new articles marked, and publishes a version
  only when the result has facts the version before lacks.
- **Skipped stories.** The shadow's rule becomes the run's: a story whose answer names none of its new articles
  keeps its version, is asked about again in the next run, and after a second such answer is written whole.
- **Bookkeeping.** A step that makes no version must still be saved in `summaries` under the new `StoryKey` (the
  version before, with the new reports and footnotes and the sentences that went in quietly, its `writtenAt`
  unchanged), or `Run` asks about it every run;
  `previous`, `Latest` and `PruneSummaries` then find it as they find a rewrite. It adds no row to
  `story_versions`, and must not reset the story's place in the thread linking. `similarStory` reads whole
  stories and is not touched.
- **The app.** `Update.Kinds` is in the stored story and not in the GraphQL schema yet. After a quiet update the
  story's text has sentences that are in none of its updates and in no row of `story_versions`: what shows a
  version's changes must take them for old text. Since round 6 every sound sentence goes in so, and there are more
  of them.
- **The flags.** The shadow does not save the unrelated articles for the grouping; the mode must.
