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
regenerate them as an unrelated cleanup. Run only the package-qualified generator for the
format being changed:

```sh
UPDATE_STATE_COMPAT_FIXTURES=1 go test ./internal/verify -run '^TestStateCompatGenerateFixtures$'
UPDATE_STATE_COMPAT_FIXTURES=1 go test ./internal/feed -run '^TestStateCompatGenerateFeedFixtures$'
UPDATE_STATE_COMPAT_FIXTURES=1 go test ./internal/moderate -run '^TestGenerateWarningFixture$'
```

Review every fixture diff, then run the full test gate. `settings.json` fixtures have no generator;
update them deliberately by hand when the store compatibility tests require it.

See the [documentation index](docs/README.md) for architecture, operations, and flow-specific
guides.

## Adding a lookup command

1. Implement the handler in `internal/lookup` and add its route to
   `(*Service).handlerRoutes` in `internal/bot/bot.go`.
2. Insert the route name at the exact matching position in the `want` ordering list in
   `internal/bot/bot_test.go`; handler order is a tested first-match contract.
3. Add the command to the appropriate Telegram menus in `internal/bot/commands.go`.
4. Add typed catalogue fields and values in every locale for the command menu, `/help`, usage,
   errors, and results.
5. Update the public command tables in `README.md`, `README.zh-CN.md`, and any command table in
   `docs/` that covers the changed surface.
6. Add package-level behavior tests and update menu, handler-order, and localization tests that
   cover the observable command.

## Adding a panel setting

1. Add and validate the `config.Config` source value, then project it into a provenance-aware
   baseline in `internal/store/baseline.go`.
2. Add the effective setting and sparse override to `internal/store/settings.go`. Preserve
   optimistic revision checks, validate candidates before publishing, and omit an override when
   it equals the baseline.
3. Extend the callback grammar in `internal/panel/codec.go`. Use the existing compact field/value
   encoding and keep encoded callback data within Telegram's 64-byte limit.
4. Add rendering and callback dispatch in `internal/panel/settings_panel.go`; add ForceReply or
   chat-picker input dispatch in `internal/panel/settings_input.go` when the value cannot be
   selected directly.
5. Add typed catalogue entries and every locale value.
6. Cover config normalization, baseline provenance, sparse persistence and revision conflicts,
   codec validation, rendering, input dispatch, authorization, and settings integration as
   applicable.

## Localisation

- Put every user-visible string in the typed catalogue under `internal/i18n/`, with one JSON
  file per subsystem and locale.
- To add a key, add its typed `Text` or `Format` field and the matching JSON key in every
  locale. Adding a locale also requires every selection path listed in
  `internal/i18n/README.md`; a `Lang` constant and `localeDefinitions` entry alone are insufficient.
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
- Write all repository code comments in English. The localization invariants reject Chinese
  production literals, including comments; do not discover this rule only after the test fails.
- Make config values configurable (with a sensible default in `LoadConfig`) instead of
  hard-coding them.

## Commits

- Group changes by topic — one commit per logical change, not one big mixed commit.
- Write a clear, imperative subject line (e.g. `feat: …`, `fix: …`, `docs: …`).

## Secrets

Never commit secrets. The bot token (`BOT_TOKEN`) and optional `GITHUB_TOKEN` come
from the environment; `bot.env` and `config.json` are git-ignored. See
[SECURITY.md](SECURITY.md) for how to report a vulnerability.
