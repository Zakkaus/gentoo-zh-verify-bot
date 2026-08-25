# Contributing

Thanks for your interest in improving gentoo-zh-verify-bot! Issues and pull
requests are welcome.

## Building

Requires **Go 1.26.7+** (per `go.mod`) and uses [telego v1.11.2](https://github.com/mymmrac/telego).

```sh
go build ./...
```

## Before opening a PR

The CI runs these checks — please make sure they pass locally (the release workflow runs the same
gate before publishing binaries):

```sh
gofmt -l .      # must print nothing (run `gofmt -w .` to fix)
go vet ./...
go build ./...
go test -race ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 -exclude=G304,G703,G706 ./...   # excluded classes: see SECURITY.md
```

## Project layout

The bot remains a **single `package main`** in the repository root. Files are grouped by
responsibility:

- **Verification and configuration:** `main.go`, `config.go`, `verify.go`, `kernel.go`,
  `quiz.go`, `dm.go`, `i18n.go`, `settings.go`, `agents.go`, `verifyfail.go`
- **Moderation:** `moderate.go`, `mute.go`, `bantime.go`, `warn.go`, `antispam.go`,
  `admin.go`, `commands.go`
- **Lookups and feeds:** `pkg.go`, `use.go`, `bug.go`, `news.go`, `wiki.go`, `bbs.go`,
  `pkgs.go`, `arm.go`, `armpkgs.go`, `feed.go`, `releaseinfo.go`, `http.go`

Tests sit beside the code they cover. `state_compat_test.go` and `testdata/state/` define
the persisted-format compatibility contract. A persisted-format change must preserve
intentional backward compatibility and update the affected fixtures deliberately. Never
regenerate them as an unrelated cleanup. When a format change requires new fixtures, run
`UPDATE_STATE_COMPAT_FIXTURES=1 go test -run TestStateCompatGenerateFixtures`, review every
fixture diff, then run the full test gate.

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
