# CLAUDE.md

Go server that takes scraping off the news app's phones: it reads the sites, groups stories, and summarizes them
with Gemini, and the app reads it over GraphQL with mTLS. Read [docs/plan.md](docs/plan.md) first: it holds the
agreed decisions and the milestones.

## Rules

- **Port, don't redesign.** The Kotlin engine and clustering in the news repo (`news/scraper/engine`,
  `news/app/android/src/main/kotlin/news/app/stories`) are the reference. Translate them file by file; the news repo's
  `news/docs/source-config.md` is the spec and `news/sources/*/fixtures` with `expected/*.json` are the pass/fail
  test. A Go result that differs from the Kotlin one is a bug in the port, unless the spec says otherwise.
- **Never edit `news/`** here. Site configs and fixtures change in the news repo; bump the submodule after.
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
go test ./...
go vet ./...
go tool gqlgen generate                            # after editing graph/schema/*.graphql
go tool gqlgen generate --config gqlgen.admin.yml  # after editing graph/admin/schema/*.graphql
```
