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
- **The site configs** come from the news repo, a git submodule at `news/`. `sources` lists them; reading the sites
  comes with milestone 2, the engine port.

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
```

The port is one above the journal server's (56398) and the admin API is on 127.0.0.1:8082, so both servers can run
on the same PC.

## Development

```bash
go test ./...                    # end to end over real mTLS, no mocks
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
graph/                             public schema and resolvers; graph/admin/ for the admin API
news/                              the news repo (submodule): sources/ configs and golden fixtures
scripts/                           CA and server certificates
docs/                              plan.md, authentication.md
```
