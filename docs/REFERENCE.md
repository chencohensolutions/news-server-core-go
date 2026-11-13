# News server: reference

Commands, tests, milestones and packs. Moved out of the [README](../README.md). Run commands from the repository
root.

## Commands

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
go run ./cmd/news-server summarize              # group the stories and summarize what changed with Gemini now
go run ./cmd/news-server eval-stories           # the story model on the 348 labeled pairs, against the app's numbers
go run ./cmd/news-server eval-gemini-grouping   # Gemini's grouping on the same pairs
go run ./cmd/news-server incremental on         # the incremental story update: on, shadow (beside the rewrite) or off; alone: what it recorded
go run ./cmd/news-server eval-incremental -dry  # replay the archive's rewrites through it (docs/incremental.md)
go run ./cmd/news-server archive                # bring the archive folder up to date now
go run ./cmd/news-server restore                # make a new database from the archive folder
go run ./cmd/news-server help                   # every command
```

The site tools need no config.json, and work on the example source in this repository:

```bash
go run ./cmd/news-server check sources                               # every source's golden fixtures (or one source folder)
go run ./cmd/news-server extract sources/_example list page.html     # read a saved page with a config, print JSON
go run ./cmd/news-server fetch path/to/sources/<id>                  # the live headlines (add an article URL for one article)
```

The server ships no sites. Without your own site configs it starts, enrolls devices and answers the API, with nothing
to read. Without the ONNX models it still reads the sites and says why stories are off. Summaries need a Gemini API
key in the file named by `gemini_key_file`.

The port is one above the journal server's (56398) and the admin API is on 127.0.0.1:8082, so both servers can run
on the same PC.

## Tests

```bash
go test ./...                    # end to end over real mTLS, plus the example's golden fixtures
go vet ./...
NEWS_SOURCES=../news-sources-xx/sources NEWS_EVAL_SET=../news-sources-xx/eval/story-model-eval go test ./...
                                 # plus a pack's sites and the story test set (private pack)
go tool gqlgen generate          # after editing graph/schema/*.graphql
go tool gqlgen generate --config gqlgen.admin.yml
```

What the tests cover:

- **Port parity:** every source's saved page against its `expected/*.json`; the app's tokenizer, graph and
  clustering tests on the Go port; the embedder on real ONNX Runtime with a hand-written model.
- **Server behavior:** enrollment, revocation, roles, and every API area through a real mTLS server
  (`newTestEnv` in [serve_test.go](../serve_test.go)); the scheduler on a fake clock; the scraper's waits on a virtual
  clock.
- **LLM code:** summaries, the request budget, the 429 and 503 paths, and thread linking, on recorded Gemini
  answers.

There is no CI workflow in the repository yet; tests are run locally.

## Milestones

Active, started Nov 9, 2026. Milestones from [docs/plan.md](plan.md):

| # | Milestone | Status |
|---|---|---|
| 1 | Skeleton: mTLS, enrollment, admin API | done |
| 2 | Scraper engine port, golden fixtures | done |
| 3 | Scheduled fetching into SQLite | done |
| 4 | Stories: embeddings and clustering, eval command | done |
| 5 | GraphQL API for the app | server part done |
| 6 | Gemini summaries and threads | server part built; the summary-quality eval (judge model, frozen windows) is not built yet |
| 7 | Run as a service for a week before the phone stops scraping | not started |

Also built since the plan was written: users and roles, a per-user archive of hidden sites and reads, video relay,
a plain-file archive with restore, router port mapping, and DHT discovery. Not done: CI, the summary eval, and the
app-side screens for summaries.

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
