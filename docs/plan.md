# Plan

The full plan, with the reasoning and sources, is the
[News server in Go plan](https://claude.ai/artifact/3NdgTnVgtQLsXf2Zw9cp1d) (Nov 8, 2026). This page keeps the
decisions and milestones next to the code.

## Decisions

- **Port the Kotlin, don't redesign it.** Only the engine (~600 lines: `HtmlExtractor`, `JsonPath`, `Dates`, `Urls`,
  `Politeness`, `ScraperEngine`, config parsing) and the story clustering (~1,400 lines: `UnigramTokenizer`,
  `TextEmbedder`, `SimilarityGraph`, `StoryClustering`, `StoryFinder`) are rewritten. The 33 site configs and their
  golden fixtures carry over unchanged (first through a `news/` submodule; since Nov 11, 2026 in the private
  `news-sources-xx` pack).
- **CSS with goquery + cascadia.** The configs use `:has`, `:not` and `:nth-of-type`, which cascadia supports. None of
  the 123 `regex` fields uses lookarounds or backreferences, so Go's RE2 reads them all.
- **Packs** (Nov 11, 2026): the core goes public and stays source-agnostic: a plain client that names itself, no
  site-specific code. Packs (Go modules, built in with `cmd/newsbuild`, as xcaddy does for Caddy) register HTTP
  transports (`fetch.RegisterTransport`) and video resolvers (`video.Register`). Site-specific code lives in packs. The server relays a pack-resolved video to the app
  (`videoStream`, then `/video/…`: HLS playlists rewritten to come back through the server, MP4 with Range), so the
  app talks to no site; a video no resolver knows plays its embed in the app's web view.
- **Users and roles** (Nov 11, 2026): many people on one server, each an admin or a user (`users.json`,
  [authentication.md](authentication.md)). Admins change what the server does for everyone (`@admin` fields) and
  invite people from the app; users keep only their own choices (hidden sites, reads) on the server.
- **Stories:** the same average-link clustering as the app, with `bge-m3` by default (too big for the phone), on ONNX
  Runtime through purego like the journal server.
- **Summaries: Gemini free tier only**, `gemini-3.5-flash-lite`. Every 10 minutes, one request with every story that
  changed in the last 12 hours, and one for the 12-hour digest built from the story summaries: 288 requests a day,
  under the free tier's ~500. A daily request budget (~400, configurable), 429s back off to the next run, the digest
  goes first when the budget is short. Structured output (a JSON schema), thinking low. (Nov 11, 2026: hen dropped
  the digest and its screen; the requests go to the stories and the daily thread linking.)
- **Incremental story updates, in shadow first** (Nov 12, 2026): a story that gains an article is to be updated
  from its version before and the new articles only, many stories a request, instead of written again from all its
  articles: about a third of the rewrites add nothing, and they are most of the input tokens. Built as a shadow
  beside the rewrite (`incremental: shadow`, kept in `story_shadow`, never shown) and a replay of the archive
  (`news-server eval-incremental`); turning it on waits for their numbers. See [incremental.md](incremental.md).
- **The app connects like the journal app:** gqlgen schema-first GraphQL, Apollo Kotlin in the app with the schema
  copied by a Gradle task, mTLS with enrollment codes and QR codes.

## Milestones

1. **Skeleton** (done, PR #1): the repo, `news/` as a submodule, mTLS, enrollment and the admin API from the journal
   server, `serverInfo` and `sources`.
2. **Engine port** (done): configs, extraction and politeness in Go (`scrape/`), passing every golden fixture and the
   Kotlin unit tests; `news-server check`, `extract` and `fetch`.
3. **Fetching** (done): every site every 10 minutes, SQLite storage (`modernc.org/sqlite`, no cgo); `news-server refresh`.
4. **Stories** (done): the app's tokenizer, embedder, similarity graph, average-link clustering and story order in Go
   (`stories/`), with the app's unit tests; vectors kept in SQLite, stories grouped after every read; `news-server
   models`, `stories` and `eval-stories` (the app's 348 labeled pairs, scored as its `score.py` does, next to its
   numbers).
5. **API and app:** `headlines` (with `since`, which is the changes), `article`, `articles` and `stories` (server
   part done; `digest` comes with milestone 6); Apollo in the news app, the server as a setting with the phone's
   scraper as fallback.
6. **Summaries** (server part in progress): after each grouping, one Gemini request with the stories that changed in
   the last 12 hours and one for the digest of the last hour (`summary/`, `gemini/`), every sentence with its footnotes,
   under a daily budget; `story.summary` and `digest` in the API; `news-server summarize`; tests on recorded
   answers. Still to do: the eval; the summary in the app (UI lab mock first).
7. **Run it** on the PC as a service for a week before the phone stops scraping.

## Harness

1. **Port parity:** `go test` (with `NEWS_SOURCES`) runs every source's `fixtures` page against its `expected/*.json`. Stories: the
   app's clustering and graph tests run on the Go port (the chain against the plain method, close pairs against the
   full table, components against clustering everything); the embedder is checked on real ONNX Runtime with a
   hand-written model, and end to end by `news-server eval-stories` on the app's labeled test set
   (`story_eval_dir`, the pack's `eval/story-model-eval`), whose AUC and best F1 must match the app's (bge-m3: 0.950 and 0.83).
2. **Server behavior:** a fake clock for the scheduler (10-minute runs, the 12-hour edge, overlapping runs skipped,
   only changed stories re-summarized); a local test site with 403, 429 and 304; a fake Gemini with recorded answers;
   an end-to-end mTLS test (`newTestEnv`).
3. **Incremental updates:** the merge rules, the shadow and the replay on recorded answers and a temporary archive;
   on the real archive, `news-server eval-incremental`: against the live rewrites' own new facts, with controls that
   need no labels ([incremental.md](incremental.md#the-replay)).
4. **Summary eval:** `news-server eval` on a few frozen 12-hour windows in `testdata/`: Hebrew, one paragraph, valid
   JSON, faithful to the sources (a judge model with a fixed rubric), covers each site's main fact. On the free tier
   too, so a small set, optionally in a second Google project.

## Open questions

- Is one of the per-story summary and the 12-hour digest enough, or both?
- A separate program (this one), or a second service inside the journal server?
- Only the owner's devices, or other people too?
- New-article notifications from the server (push), or from the phone polling?
- Does the app switch in milestone 5 cover iOS too?
