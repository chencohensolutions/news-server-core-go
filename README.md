# News server

A Go server that reads the news sites for the [news app](https://github.com/chencohensolutions/news), so the phone
doesn't have to. It will fetch headlines and articles, group them into stories, and every 10 minutes summarize the
last 12 hours with Gemini (free tier only). Phones reach it the way the journal app reaches
[journal-server-go](https://github.com/chencohensolutions/journal-server-go): GraphQL (gqlgen) over mutual TLS, with
a one-time enrollment code or QR code per device.

The plan, milestones and open questions: [docs/plan.md](docs/plan.md).

## What's built

Milestone 1, the skeleton:

- **mTLS and enrollment**, copied from journal-server-go (`enroll/`, `auth/`, `middleware.go`): deny by default, only
  `enrollmentPreview` and `enrollDevice` are `@public`, certificates renew with `renewCertificate`.
- **Admin API** on loopback with a token rewritten on every start, and the commands that call it: `enroll-code`,
  `devices`, `revoke`.
- **The site configs** come from the news repo, a git submodule at `news/`. `sources` lists them.

Milestone 2, the engine port (`scrape/`): the news app's scraper translated file by file. It reads a site's config
(CSS selectors, JSON paths, regex transforms, dates), downloads pages one at a time per site with a gap between them
and a growing pause when a site pushes back, and gives the same output as the app on every saved page of every site
(`news/sources/*/fixtures`). Jsoup's selector and text rules are reimplemented in `scrape/dom`, Java regex through
`regexp2`.

Milestone 3, fetching (`fetch/`, `refresh/`, `store/`): every `refresh_minutes` (10) the server reads every site's
headlines, then the article of each headline it doesn't have yet, newest first, and keeps them in `news.db` (SQLite)
with when each headline was first and last seen; a headline gone from its site for `keep_days` (7) is forgotten. It
reaches the sites with a plain HTTP client.

Milestone 4, stories (`stories/`, `onnx/`): after each read the server groups the stored headlines of the last
`story_days` (7) into stories, as the app does: each headline embedded with `story_model` (bge-m3 by default; the app's
other choice, multilingual-e5-small, works too) by `story_text` (title + ". " + subtitle), the close pairs kept in a
similarity graph, average-link clustering at `story_strictness` (60, the app's slider), and only groups with two sites
or more. The tokenizer, embedder, graph, clustering and ordering are the app's Kotlin translated file by file, with its
tests. Vectors are kept in `news.db`, so only new headlines (or ones whose subtitle arrived) are embedded. The model
runs on ONNX Runtime through purego, as in journal-server-go; `news-server models` downloads both (587 MB for bge-m3).
Without them the server still reads the sites and says why stories are off.

Milestone 5, the API (`graph/schema/news.graphql`): enrolled devices read `headlines` (all kept, or since a time, for
what changed; optionally some sites only), `article` and `articles` (up to 200 at once), and `stories` (the last
grouping, newest first). The fields are the app's `ArticleSummary` and `Article`; `serverInfo.stories` says whether
stories are on.

## Getting started

Requirements: Go 1.26 or newer (gqlgen runs as a `go tool` of the module) and OpenSSL for the certificate scripts.

```bash
git clone --recurse-submodules https://github.com/chencohensolutions/news-server-core-go.git
cd news-server-core-go

scripts/generate-ca.sh
SERVER_NAMES="myhome.example.org" scripts/generate-server-cert.sh   # names and IPs devices will use
cp config.example.json config.json                                   # then edit public_url and users

go run .                        # serves https://0.0.0.0:56399
go run . enroll-code hen        # a one-time code and a QR code for the app, valid 10 minutes
go run . devices                # enrolled devices
go run . revoke <serial>        # lock a device out
go run . refresh                # read every site once now (the server does it every refresh_minutes)
go run . models                 # download ONNX Runtime and the story model (once)
go run . stories                # group the stored headlines now and print the stories
go run . eval-stories           # the story model on the app's 348 labeled pairs, against the app's numbers
```

The site tools need no config.json:

```bash
go run . check news/sources                          # every source's golden fixtures (or one source folder)
go run . extract news/sources/siteh list page.html    # read a saved page with a config, print JSON
go run . fetch news/sources/siteh                     # the live headlines (add an article URL for one article)
```

The port is one above the journal server's (56398) and the admin API is on 127.0.0.1:8082, so both servers can run
on the same PC.

## Development

```bash
go test ./...                    # end to end over real mTLS, plus every site's golden fixtures
go vet ./...
go tool gqlgen generate          # after editing graph/schema/*.graphql
go tool gqlgen generate --config gqlgen.admin.yml
git submodule update --remote news   # the latest site configs
```

## Project layout

```
main.go, serve.go, middleware.go   start-up, routing, identify / gzip / deadlines
admin.go, admincli.go              the loopback admin API and the commands that call it
auth/  enroll/                     config, request identity and @public, enrollment and issuing
sites/                             reads news/sources/<id>/config.json
scrape/                            the engine: configs, extraction, dates, URLs, politeness, downloading
scrape/dom, jsonpath, rx           jsoup-compatible selectors and text, JSON paths, Java-like regex
fetch/                             the HTTP client the engine reads with
refresh/  store/                   reading every site on a schedule; the SQLite database
reading.go                         wires them into the server and the refresh command
stories/  onnx/                    tokenizer, embedder, similarity graph, clustering; ONNX Runtime through purego
bundle/                            downloads ONNX Runtime and the models, checked by sha256
storyjob.go                        groups stories after each read; the models, stories and eval-stories commands
sitecli.go                         check, extract, fetch
graph/                             public schema and resolvers; graph/admin/ for the admin API
news/                              the news repo (submodule): sources/ configs and golden fixtures
scripts/                           CA and server certificates
docs/                              plan.md, authentication.md
```
