# CLAUDE.md

Go server that takes scraping off the news app's phones: it reads the sites, groups stories, and summarizes them
with Gemini, and the app reads it over GraphQL with mTLS. Read [docs/plan.md](docs/plan.md) first: it holds the
agreed decisions and the milestones.

## Rules

- **The core is source-agnostic** (it is going public): no site-specific code, an honest
  user agent. That code lives in packs (`fetch.RegisterTransport`, `video.Register`, built with
  `cmd/newsbuild`); the owner's are private.
- **Port, don't redesign.** The Kotlin engine and clustering in the news app repo (private: `scraper/engine`,
  `app/android/src/main/kotlin/news/app/stories`) are the reference. Translate them file by file;
  [docs/source-config.md](docs/source-config.md) is the spec, and the golden fixtures (`sources/_example` here, the
  sites' in a pack's `sources/` through `NEWS_SOURCES`) are the pass/fail test. A Go result that differs from the
  Kotlin one is a bug in the port, unless the spec says otherwise.
- **No site configs, fixtures or real headlines in this repo**: they live in packs (`news-sources-xx/sources` and
  `eval/story-model-eval`). Only `sources/_example` stays.
- **Gemini: free tier only** (decided Nov 8, 2026). Model `gemini-3.5-flash-lite`. Never add a paid key or
  fallback. One batched request per 10-minute run plus one digest request; a daily request budget under the free
  limit; back off on 429.
- **Plumbing follows journal-server-go** (`../journal-server-go` if checked out beside this repo): gqlgen
  schema-first, mTLS deny by default (`@public` for the enrollment fields only), the loopback admin API, typed errors
  in `extensions.code`, no cgo, Windows-safe file names. The owner runs it on a Windows PC.
- Tests use real temporary folders and a real mTLS server (`newTestEnv` in `serve_test.go`), not mocks. The Gemini
  client is the exception: tests use recorded answers.

## Commands

```bash
go run ./cmd/news-server                            # the server without packs
./build.ps1                                        # a build with the packs in packs.txt
go test ./...
NEWS_SOURCES=../news-sources-xx/sources go test ./scrape  # every site's golden fixtures
go vet ./...
go tool gqlgen generate                            # after editing graph/schema/*.graphql
go tool gqlgen generate --config gqlgen.admin.yml  # after editing graph/admin/schema/*.graphql
```
