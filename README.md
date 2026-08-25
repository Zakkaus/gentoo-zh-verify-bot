# gentoo-zh-verify-bot

English | [简体中文](README.zh-CN.md)

`gentoo-zh-verify-bot` is a Telegram join-request verification bot for Gentoo Chinese community groups that need to filter bulk spam applications. It keeps each request pending until the applicant passes or fails verification. It also provides group moderation, Gentoo and Linux lookups, and optional Bugzilla and news feeds.

## Fit and operating footprint

The bot works with Telegram groups and supergroups that use join requests. It must be a group administrator with **Invite Users**, **Ban Users**, and **Delete Messages** permissions. If a required-channel gate is enabled, the bot must also be an administrator of that channel. BotFather privacy mode only needs to be disabled for `/bc`.

Releases are static Linux binaries for `amd64` and `arm64`. With Telegram's hosted Bot API and the default data sources, the bot requires outbound HTTPS only; it needs no database, reverse proxy, or inbound port. The supplied systemd unit keeps durable state in a private directory and sets `MemoryMax=512M`. The `512M` value is a safety limit, not a claim about resident memory use.

## Verification flow

Each join request is handled independently for its group. By default, the bot first tries to send the challenge in a private message. If Telegram definitively rejects private delivery, the bot posts a group prompt whose `verify_<groupID>` deep link opens only the pending request for that group. A transport error can occur after Telegram has accepted a message, so the bot suppresses the group copy when delivery is uncertain. Administrators can disable DM-first delivery per group in `/settings`.

| Mode | Applicant action | Behavior |
| --- | --- | --- |
| `kernel` (default) | Type the version of the Linux kernel currently running; use `uname -r` to obtain it | Up to 3 typed attempts. An applicant without a Linux device must first state that and provide the current minute, then answer a short question without seeing the accepted answers. |
| `quiz` | Select the correct shuffled option from the question bank | Uses the group's `questions`; an empty bank falls back to `kernel`. |
| `mixed` | Complete a randomly selected `kernel` or `quiz` challenge | Selected independently for every application; an empty question bank uses `kernel`. |

The `kernel` and short-answer prompts include an applicant-specific LLM delegation tripwire. Exact compliance with the hidden instruction fails verification. A self-declared model name is recorded only as an aggregate tally, so this is a deterrent rather than a security boundary.

A group can exempt confirmed members of trusted groups or require applicants to join a channel before approval. A wrong answer or timeout declines the request and starts a cooldown. By default, 3 failures within the 6-hour counting window trigger an automatic ban. Applicant messages use Simplified Chinese, Traditional Chinese, or English according to Telegram `language_code`; group and administrator messages use the group's `lang`.

## Installation

`BOT_TOKEN` is the only required startup configuration. Installing a prebuilt release does not require Go. Building from source requires Go 1.26.7 or later.

### Use a release binary

[Releases](https://github.com/Zakkaus/gentoo-zh-verify-bot/releases) contain the `amd64` and `arm64` binaries and `SHA256SUMS`, but not the systemd unit. Change `arch` to the target architecture and fetch the binary and unit from the same tag:

```sh
version=v3.12.0
arch=amd64
release_url="https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/download/${version}"
curl --fail --location --remote-name "${release_url}/gentoo-zh-verify-bot-linux-${arch}"
curl --fail --location --remote-name "${release_url}/SHA256SUMS"
sha256sum --ignore-missing --strict --check SHA256SUMS
mv "gentoo-zh-verify-bot-linux-${arch}" gentoo-zh-verify-bot
curl --fail --location \
  "https://raw.githubusercontent.com/Zakkaus/gentoo-zh-verify-bot/${version}/deploy/gentoo-zh-verify-bot.service" \
  --output gentoo-zh-verify-bot.service
```

### Build from source

```sh
CGO_ENABLED=0 go build -trimpath -o gentoo-zh-verify-bot ./cmd/gentoo-zh-verify-bot
cp deploy/gentoo-zh-verify-bot.service .
```

### Install and start the systemd service

Install the binary and unit. Create an empty environment file with mode `0600`, then use an editor to add `BOT_TOKEN=<your-token>`:

```sh
sudo install -Dm755 gentoo-zh-verify-bot /usr/local/bin/gentoo-zh-verify-bot
sudo install -Dm600 /dev/null /etc/gentoo-zh-verify-bot/bot.env
sudoedit /etc/gentoo-zh-verify-bot/bot.env
sudo install -Dm644 gentoo-zh-verify-bot.service /etc/systemd/system/gentoo-zh-verify-bot.service
sudo systemctl daemon-reload
sudo systemctl enable --now gentoo-zh-verify-bot
```

## First start and group registration

On first start, the service writes a private, one-use owner claim link to the journal. The link expires after 10 minutes by default. Until it is claimed, anyone who can read the journal could become the owner. Set `owner_claim_user_id` to restrict the link to one Telegram user.

```sh
sudo journalctl -u gentoo-zh-verify-bot
```

After claiming ownership, add the bot to a group and promote it to administrator. The bot registers an owner-authorized group and stores the registration in `settings.json`. Run `/settings` in the group to review its verification mode, question banks, and required channel.

For delegated registration, the owner sends `/enroll` in a private chat and gives the resulting one-use group link to an administrator of that group. The link remains valid for 10 minutes. The bot automatically leaves unknown groups that have neither owner authorization nor a valid enrollment link.

The owner can send `/unregister <group-id>` in a private chat. This command accepts runtime-registered groups only. It removes the registration and that group's runtime overrides, then attempts to leave the group. Removing the bot directly does not clear its registration.

## Configuration

`config.json` is optional. If it is absent, the bot starts with no preconfigured groups and waits for runtime registration. [`config.example.json`](config.example.json) provides a configuration example. File values form the startup baseline; sparse runtime overrides in `settings.json` take precedence over the file, and file values take precedence over built-in defaults. Changes to `config.json` require a service restart.

Administrators normally need only these application environment variables:

| Variable | Purpose |
| --- | --- |
| `BOT_TOKEN` | Required Telegram bot token; no default. |
| `GITHUB_TOKEN` | Optional; uses an authenticated API allowance for GitHub overlay lookups. |
| `TELEGRAM_API_URL` | Optional; selects a self-hosted Telegram Bot API server. |

A group administrator runs `/settings` in the group and then uses the private settings panel to change:

- verification enablement, DM-first delivery, `kernel`, `quiz`, or `mixed` mode, applicant-name hiding, ban duration, lookup cleanup policy, and `lang`;
- sender-channel whitelist, trusted groups, and known chats;
- verification timeout, maximum failures, and retry cooldown;
- the `questions` quiz bank, a custom `fallback_questions` short-answer bank, the built-in short questions, and the required channel and invite link.

The panel can add, edit, and delete quiz questions and custom short questions. Built-in short questions can only be selected or restored, not edited directly. `lang` accepts `zh`, `zh-Hant`, and `en`. Bot-wide settings are rich output controlled by `/rich` and the panel's `private_query_per_min`; only administrators of the effective control group can change them. Set `control_group_id` to select that group explicitly. When it is unset, the settings store uses the first effective group.

Feed destinations, overlays, the news source, `stats_timezone`, and `user_agent` remain `config.json`-only settings. See the [Simplified Chinese documentation index](docs/zh-CN/README.md) and [English documentation index](docs/README.md) for detailed flows.

## Commands

| Scope | Commands | Purpose |
| --- | --- | --- |
| Registered group or private chat | `/help`, `/ping`, `/stats` | Show help, runtime state, and statistics. These commands do not consume the private lookup allowance. |
| Registered group or private chat | `/pkg`, `/use`, `/arm` | Query Gentoo packages, USE flags, and `arm64` keyword status. |
| Registered group or private chat | `/bug`, `/news`, `/wiki`, `/bbs` | Query Gentoo Bugzilla, Gentoo news, Gentoo Wiki, ArchWiki, and Arch Linux CN. |
| Registered group or private chat | `/pkgs`, `/distro`, `/armpkgs` | Compare package versions or `arm64` support across distributions; `/distro` is an alias for `/pkgs`. |
| Administrator in a registered group | `/mute [duration]`, `/unmute`, `/ban`, `/sb`, `/warn`, `/clearwarn` | Reply to the target message, then mute, ban, purge, or warn that user. |
| Administrator in a registered group | `/start`, `/stop`, `/settings`, `/bantime`, `/bc`, `/spoiler`, `/vmode`, `/autodel` | Change verification, moderation, and message policy for the current group; runtime values are written to `settings.json`. |
| Administrator in the control group | `/rich` | Change bot-wide rich output for `/pkg` and `/use`. |
| Owner in a private chat | `/enroll`, `/unregister <group-id>` | Issue a group enrollment link or remove a runtime-registered group. |

External lookups in private chats are limited per user to `private_query_per_min` requests per minute; registered groups are unlimited. `/start` also carries the owner-claim, group-enrollment, verification, and settings-panel deep links; each link type is accepted only in its corresponding private-chat or group scope.

## State, restarts, and outages

The supplied unit uses `StateDirectory=gentoo-zh-verify-bot` to create `/var/lib/gentoo-zh-verify-bot` with mode `0700`. Without `$STATE_DIRECTORY`, ordinary runtime state is memory-only, and owner claims and runtime group registration fail.

| File | State preserved across restarts |
| --- | --- |
| `settings.json` | Owner identity, group registrations, control group, one-use enrollment capabilities, and per-group and bot-wide runtime overrides, including `/bc` state and sender-channel whitelists. |
| `pending.json` | In-progress verification, mode, language, question, attempts, nonce, and deadline. |
| `verifyfail.json`, `agents.json`, `heartbeat.json` | Verification failures and cooldowns, the cumulative LLM-tripwire tally, and the last successful Telegram contact. |
| `warns.json` | Per-group, per-user `/warn` counts. |
| `feed-<chat_id>.json` | Feed cursors and tracked Bugzilla messages. |

Daily `/stats`, settings-panel sessions and drafts, rate-limit windows, caches, cleanup timers, and transient alert throttles do not survive a restart. `antispam.json` is legacy migration input only; the current version does not write it.

The systemd unit uses `Restart=always`. Unless systemd stops it deliberately, the process restarts 30 seconds after exit, with no start-limit latch. `WatchdogSec=120s` is progress-based rather than a fixed heartbeat: the process notifies the watchdog only after a `getUpdates` call completes, and each call is bounded at 45 seconds. Quiet polls and failed retries therefore count as progress, while a stuck poll causes systemd to restart the service.

When Telegram is unreachable, an expiring verification receives a new full window without being declined or recording a failure. After an in-process outage longer than 90 seconds, every in-memory verification receives a fresh window. After a restart, restored `pending.json` entries also receive fresh windows when `heartbeat.json` proves that the service was down for more than 90 seconds. Each recovery attempts to re-notify at most 30 applicants. Telegram retains updates for a disconnected bot for about 24 hours, so a longer outage may lose join requests the bot never received. On recovery, the bot alerts administrators to inspect Telegram's pending join-request queue manually when `heartbeat.json` is readable.

## Forking for another community

Groups, verification modes, both question banks, the three existing locales, overlays, the news source, feed destinations, and message policy can all be changed through configuration or the settings panel without modifying code.

A rename or replacement of the Gentoo-specific behavior requires a complete cutover of:

- the module path in `go.mod` and every Go import;
- the command name, binary and release asset names, systemd paths, and state directory in `cmd/gentoo-zh-verify-bot`, `deploy/gentoo-zh-verify-bot.service`, and `.github/workflows/release.yml`;
- catalogue text, built-in short questions, and locale registration and selection branches under `internal/i18n/locales`;
- default overlays, Gentoo data sources, lookup commands, and feed endpoints under `internal/lookup` and `internal/feed`;
- documentation, release links, the security-reporting address, and the changelog.

## Documentation and project policies

- [Simplified Chinese documentation index](docs/zh-CN/README.md)
- [English documentation index](docs/README.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Changelog](CHANGELOG.md)
- [MIT License](LICENSE)
