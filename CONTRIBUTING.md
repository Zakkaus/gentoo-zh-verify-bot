# Contributing

Thanks for your interest in improving gentoo-zh-verify-bot! Issues and pull
requests are welcome.

## Building

Requires **Go 1.25+** (the only dependency is [telego](https://github.com/mymmrac/telego)).

```sh
go build ./...
```

## Before opening a PR

The CI runs these checks — please make sure they pass locally:

```sh
gofmt -l .      # must print nothing (run `gofmt -w .` to fix)
go vet ./...
go build ./...
go test -race ./...
```

## Project layout

The bot is a **single `package main`** with one file per area, all in the repo root —
that's idiomatic Go for a self-contained binary, and Go requires a package's files to live
in one directory. The files are named after what they do, so the flat list reads like a
table of contents:

- **Core:** `main.go`, `config.go`, `verify.go`, `quiz.go`, `dm.go`
- **Moderation:** `moderate.go`, `warn.go`, `antispam.go`, `admin.go`, `commands.go`
- **Helpers (one per command):** `pkg.go` `use.go` `bug.go` `news.go` `wiki.go` `bbs.go`
  `pkgs.go` `arm.go` `armpkgs.go`, plus `feed.go` (auto-feed), `releaseinfo.go`, `http.go`
  (shared HTTP layer). Each `*_test.go` sits next to the file it tests.

We deliberately do **not** split into `internal/...` sub-packages: at this size (~5k lines)
that would force exporting every shared helper (`httpGetJSON`, `htmlMessage`, `Verifier`,
`Config`, …) and add package boilerplate without making anything clearer. Revisit only if
the bot grows toward being reused as a library.

## Code style

- Put new functionality in a focused, command-named file and reuse the shared helpers
  (`httpGetJSON`, `httpGetBody`, `htmlMessage`, the `Verifier`/`Config` types) rather than
  duplicating them.
- Keep it simple and readable; match the surrounding style. `gofmt` decides formatting.
- User-facing strings are Simplified Chinese (this bot targets the Gentoo zh community).
- Make config values configurable (with a sensible default in `LoadConfig`) instead of
  hard-coding them.

## Commits

- Group changes by topic — one commit per logical change, not one big mixed commit.
- Write a clear, imperative subject line (e.g. `feat: …`, `fix: …`, `docs: …`).

## Secrets

Never commit secrets. The bot token (`BOT_TOKEN`) and optional `GITHUB_TOKEN` come
from the environment; `bot.env` and `config.json` are git-ignored. See
[SECURITY.md](SECURITY.md) for how to report a vulnerability.
