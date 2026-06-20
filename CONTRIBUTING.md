# Contributing

Thanks for your interest in improving gentoo-zh-verify-bot! Issues and pull
requests are welcome.

## Building

Requires **Go 1.25+** (the only dependency is [telego](https://github.com/mymmrac/telego)).

```sh
go build ./...
```

## Before opening a PR

The CI runs these three checks — please make sure they pass locally:

```sh
gofmt -l .      # must print nothing (run `gofmt -w .` to fix)
go vet ./...
go build ./...
```

## Code style

- The bot is a single `package main`; one file per area (`verify.go`, `feed.go`,
  `wiki.go`, `bbs.go`, the `pkg*`/`bug`/`news` helpers, etc.). Put new functionality
  in a focused file and reuse the shared helpers (`httpGetJSON`, `httpGetBody`,
  `htmlMessage`, the `Verifier`/`Config` types) rather than duplicating them.
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
