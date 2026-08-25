# Contributing

Thanks for your interest in improving gentoo-zh-verify-bot! Issues and pull
requests are welcome.

## Building

Requires **Go 1.26.7+** (per `go.mod`) and uses [telego v1.11.2](https://github.com/mymmrac/telego).

```sh
go build ./cmd/gentoo-zh-verify-bot
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

The executable entry point and process assembly live in `cmd/gentoo-zh-verify-bot`. Runtime
responsibilities are split across focused internal packages:

- `internal/bot`: handler ordering, command menus, and private-message routing
- `internal/config`: configuration loading, normalization, and validation
- `internal/feed`: Bugzilla and news polling
- `internal/i18n`: typed catalogues and locale files
- `internal/lookup`: package, bug, news, wiki, and distribution lookups
- `internal/moderate`: moderation policy, commands, and warning state
- `internal/panel`: administration commands and the settings panel
- `internal/store`: persisted settings and atomic JSON state helpers
- `internal/tg`: shared Telegram transport and authorization mechanics
- `internal/verify`: join-verification state and challenge flows

Tests sit beside the code they cover. `state_compat_test.go` and `testdata/state/` define
the persisted-format compatibility contract. A persisted-format change must preserve
intentional backward compatibility and update the affected fixtures deliberately. Never
regenerate them as an unrelated cleanup. When a format change requires new fixtures, run
`UPDATE_STATE_COMPAT_FIXTURES=1 go test -run TestStateCompatGenerateFixtures`, review every
fixture diff, then run the full test gate.

See the [documentation index](docs/README.md) for architecture, operations, and flow-specific
guides.

## Localisation

- Put every user-visible string in the typed catalogue under `internal/i18n/`, with one JSON
  file per subsystem and locale.
- To add a key, add its typed `Text` or `Format` field and the matching JSON key in every
  locale. To add a locale, provide all subsystem files and register its `Lang` and
  `localeDefinitions` entry.
- `TestProductionCodeContainsNoChineseStringLiterals` rejects Chinese literals outside the
  catalogue. `TestLocaleFilesLoad` rejects missing files, malformed JSON, unknown keys, and
  invalid value shapes. The other `internal/i18n` tests enforce completeness, placeholder
  parity, terminology, English Gentoo terms, and script consistency.
- Write Traditional Chinese natively; never derive it by converting Simplified Chinese.

See [`internal/i18n/README.md`](internal/i18n/README.md) for the catalogue layout and complete
translation workflow.

## Code style

- Put new functionality in the package that owns its policy. Keep
  `cmd/gentoo-zh-verify-bot` focused on process assembly and registration lifecycle, and reuse
  existing package services and transport or storage helpers instead of duplicating them.
- Keep it simple and readable; match the surrounding style. `gofmt` decides formatting.
- Keep user-visible text in the localisation catalogue; do not hard-code it in production.
- Make config values configurable (with a sensible default in `LoadConfig`) instead of
  hard-coding them.

## Commits

- Group changes by topic — one commit per logical change, not one big mixed commit.
- Write a clear, imperative subject line (e.g. `feat: …`, `fix: …`, `docs: …`).

## Secrets

Never commit secrets. The bot token (`BOT_TOKEN`) and optional `GITHUB_TOKEN` come
from the environment; `bot.env` and `config.json` are git-ignored. See
[SECURITY.md](SECURITY.md) for how to report a vulnerability.
