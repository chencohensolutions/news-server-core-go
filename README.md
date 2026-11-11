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
- **The site configs** are folders of `<id>/config.json`, from `sources_dir` and from packs: no sites ship with the
  server, you bring them. The schema is [docs/source-config.md](docs/source-config.md); `sources/_example` shows
  it. `sources` lists them.

Milestone 2, the engine port (`scrape/`): the news app's scraper translated file by file. It reads a site's config
(CSS selectors, JSON paths, regex transforms, dates), downloads pages one at a time per site with a gap between them
and a growing pause when a site pushes back, and gives the same output as the app on every saved page of every site
(each source's `fixtures/`). Jsoup's selector and text rules are reimplemented in `scrape/dom`, Java regex through
`regexp2`.

Milestone 3, fetching (`fetch/`, `refresh/`, `store/`): every `refresh_minutes` (10) the server reads every site's
headlines, then the article of each headline it doesn't have yet, newest first, and keeps them in `news.db` (SQLite)
with when each headline was first and last seen; a headline gone from its site for `keep_days` (7) is forgotten. It
reaches the sites with a plain HTTP client that names itself (`news-server-go (+repo URL)`) unless a site config sets
its own user agent. Packs can change how it reaches sites; see "Packs" below.

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

## Packs

The core reads only what site configs describe, and ships no sites. A pack is a Go module that adds to it from its
`init` function:

- `scrape.RegisterSources(name, fsys)`: the pack's sites, a folder of `<id>/config.json` (usually its `sources/`,
  embedded with `//go:embed sources/*/config.json` and `fs.Sub`). The server reads them beside `sources_dir`; a site
  id in two places is an error.
- `fetch.RegisterTransport(name, wrap)`: an HTTP transport wrapped around the core's, for the requests it handles.
  It gets every site (with the keys of its config's `http` object the core doesn't know, in `Site.Extra`) and its own
  settings from config.json's `"packs": { "<name>": {...} }`.
- `video.Register(name, resolver)`: turns an embedded player into a stream the server can play.

List your packs in `packs.txt` (git-ignored; copy `packs.example.txt`), one a line: a module (`@latest` or a
version) or a folder. Then build:

```bash
./build.ps1                                  # Windows: news-server.exe with every pack in packs.txt
./build.sh                                   # macOS/Linux
./build.ps1 -o C:\path\to\news-server.exe   # written elsewhere, say beside your config.json
```

Run it again after pulling the core or a pack. It runs `newsbuild` (`cmd/newsbuild`), which builds a server with
packs the way `xcaddy` builds Caddy with plugins; `--with <pack>` names packs on the command line instead of
`packs.txt`. `news-server version` lists what the packs added. Without packs: `go build ./cmd/news-server`.

To work on the core and a pack together, put a `go.work` in the folder holding both (`go work init ./news-server-core-go
./my-pack`), so each builds and tests against the other's checkout.

## Getting started

Requirements: Go 1.26 or newer (gqlgen runs as a `go tool` of the module) and OpenSSL for the certificate scripts.

```bash
git clone https://github.com/chencohensolutions/news-server-core-go.git
cd news-server-core-go

scripts/generate-ca.sh
SERVER_NAMES="myhome.example.org" scripts/generate-server-cert.sh   # names and IPs devices will use
cp config.example.json config.json                                   # then edit public_url, users and sources_dir

go run ./cmd/news-server                        # serves https://0.0.0.0:56399
go run ./cmd/news-server enroll-code hen        # a one-time code and a QR code for the app, valid 10 minutes
go run ./cmd/news-server devices                # enrolled devices
go run ./cmd/news-server revoke <serial>        # lock a device out
go run ./cmd/news-server refresh                # read every site once now (the server does it every refresh_minutes)
go run ./cmd/news-server models                 # download ONNX Runtime and the story model (once)
go run ./cmd/news-server stories                # group the stored headlines now and print the stories
go run ./cmd/news-server eval-stories           # the story model on the app's 348 labeled pairs, against the app's numbers
```

The site tools need no config.json:

```bash
go run ./cmd/news-server check sources                               # every source's golden fixtures (or one source folder)
go run ./cmd/news-server extract sources/_example list page.html     # read a saved page with a config, print JSON
go run ./cmd/news-server fetch path/to/sources/<id>                  # the live headlines (add an article URL for one article)
```

The port is one above the journal server's (56398) and the admin API is on 127.0.0.1:8082, so both servers can run
on the same PC.

## Development

```bash
go test ./...                    # end to end over real mTLS, plus the example's golden fixtures
NEWS_SOURCES=../news-sources-xx/sources NEWS_EVAL_SET=../news-sources-xx/eval/story-model-eval go test ./...
                                 # plus a pack's sites and the story test set
go vet ./...
go tool gqlgen generate          # after editing graph/schema/*.graphql
go tool gqlgen generate --config gqlgen.admin.yml
```

## Project layout

```
main.go, serve.go, middleware.go   start-up, routing, identify / gzip / deadlines
admin.go, admincli.go              the loopback admin API and the commands that call it
auth/  enroll/                     config, request identity and @public, enrollment and issuing
sites/                             the sites' configs (sources_dir and the packs') and settings
scrape/                            the engine: configs, extraction, dates, URLs, politeness, downloading
scrape/dom, jsonpath, rx           jsoup-compatible selectors and text, JSON paths, Java-like regex
fetch/  video/                     the HTTP client and video resolvers, with the packs' registries
cmd/news-server, cmd/newsbuild      the plain server; the builder of a server with packs
refresh/  store/                   reading every site on a schedule; the SQLite database
reading.go                         wires them into the server and the refresh command
stories/  onnx/                    tokenizer, embedder, similarity graph, clustering; ONNX Runtime through purego
bundle/                            downloads ONNX Runtime and the models, checked by sha256
storyjob.go                        groups stories after each read; the models, stories and eval-stories commands
sitecli.go                         check, extract, fetch
graph/                             public schema and resolvers; graph/admin/ for the admin API
sources/_example                   the config schema's example, with its golden fixtures
scripts/                           CA and server certificates
docs/                              plan.md, authentication.md
```
