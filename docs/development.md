# Development, CI, and release

The repository requires Go 1.26.7 and telego v1.11.2. Production code is under `cmd/gentoo-zh-verify-bot` and `internal/*`; it is not a root-level single-package program.

## Build and local execution

**Implementation:** package `main`, `main` in `cmd/gentoo-zh-verify-bot/main.go`; module declaration `go.mod`.

Build every package:

```sh
go build ./...
```

Build the deployable static command:

```sh
CGO_ENABLED=0 go build -trimpath -o gentoo-zh-verify-bot ./cmd/gentoo-zh-verify-bot
```

A plain build reports version `dev`. `./gentoo-zh-verify-bot -version` prints the linked value and exits before requiring `BOT_TOKEN`. Normal execution requires `BOT_TOKEN`; a missing config file is allowed. Runtime setup is in [Deployment](deployment.md).

## Tests and localization contracts

**Implementation:** package `internal/i18n`, `TestProductionCodeContainsNoChineseStringLiterals` and `TestLocaleFilesLoad` in `internal/i18n/invariants_test.go`; package `main`, `main` in `cmd/gentoo-zh-verify-bot/main.go`.

Run the repository suite with the race detector and a cold test count:

```sh
go test -race -count=1 ./...
```

Tests are package-local and include behavior, persistence compatibility, parser fixtures, handler order, and settings integration. The localization tests are part of that command. In particular, production Chinese literals outside `internal/i18n` fail `TestProductionCodeContainsNoChineseStringLiterals`; malformed/missing locale data fails `TestLocaleFilesLoad`. A source change that adds user-visible text must update the typed catalog and every locale rather than embedding text in the handler.

The race run exercises tests with `-race`; it does not make external Telegram, Bugzilla, GitHub, or Gentoo network calls into end-to-end verification unless a test explicitly supplies such integration. Most service tests use fake transports.

## CI gate

**Implementation:** workflow `.github/workflows/ci.yml`; package `internal/i18n`, `TestProductionCodeContainsNoChineseStringLiterals` in `internal/i18n/invariants_test.go` as one contract reached by the test step.

CI runs for pull requests and pushes to `main`, in this order:

1. `gofmt -l .` must print no files;
2. `go vet ./...`;
3. Staticcheck v0.8.1;
4. `go build ./...`;
5. `go test -race ./...`;
6. Govulncheck v1.7.0;
7. Gosec v2.28.0, excluding only `G304`, `G703`, and `G706` for the documented operator-controlled paths and journald log inputs.

The tools are invoked with pinned module versions through `go run`. Local acceptance should reproduce the commands in `.github/workflows/ci.yml`; do not infer a narrower gate from one package’s tests.

## Persisted-format compatibility

**Implementation:** package `internal/verify`, `TestStateCompatGenerateFixtures` in `internal/verify/state_compat_test.go`; package `internal/feed`, `TestStateCompatGenerateFeedFixtures` in `internal/feed/state_compat_test.go`; package `internal/moderate`, `TestGenerateWarningFixture` in `internal/moderate/state_test.go`; compatibility tests under `internal/store` and fixtures under `testdata/state/`.

`testdata/state/` is a compatibility contract. A change to any persisted JSON shape must deliberately decide and test backward compatibility, then update the affected fixtures in the same change. Never regenerate fixtures as formatting cleanup or as an incidental side effect.

Current explicit generators are package-specific:

```sh
UPDATE_STATE_COMPAT_FIXTURES=1 go test ./internal/verify -run '^TestStateCompatGenerateFixtures$'
UPDATE_STATE_COMPAT_FIXTURES=1 go test ./internal/feed -run '^TestStateCompatGenerateFeedFixtures$'
UPDATE_STATE_COMPAT_FIXTURES=1 go test ./internal/moderate -run '^TestGenerateWarningFixture$'
```

Use only the generator for the format being changed. `settings.json` fixtures do not have a matching generator in the code read; update those deliberately by hand when the store compatibility tests require it. Review every fixture diff, including field removal and legacy files, then run the full race suite and CI gate. Historical legacy fixtures must not be silently rewritten to the current schema.

## Release and version injection

**Implementation:** workflow `.github/workflows/release.yml`; package `main`, `main` and the `version` variable in `cmd/gentoo-zh-verify-bot/main.go`.

Any pushed tag matching `v*` triggers the release workflow. The trigger is broader than semantic `vX.Y.Z`; tag discipline is therefore an operator/repository policy, not workflow validation.

The release job repeats the complete CI gate before building. It then cross-builds `linux/amd64` and `linux/arm64` with:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=<arch> \
  go build -trimpath -ldflags "-s -w -X main.version=<tag>" \
  -o dist/gentoo-zh-verify-bot-linux-<arch> ./cmd/gentoo-zh-verify-bot
```

The `-X main.version=<tag>` assignment replaces the default `dev` string used by `-version`, startup logs, `/ping`, and release diagnostics. The workflow generates `dist/SHA256SUMS` and publishes both binaries plus the checksum file through the GitHub release action. It does not build other operating systems or architectures.
