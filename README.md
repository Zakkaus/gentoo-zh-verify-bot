# gentoo-zh-verify-bot

English | [简体中文](README.zh-CN.md)

A Telegram verification bot for Linux community groups that need to filter bulk spam applications, built and run by the [Gentoo-zh Community](https://gentoozh.org). Somebody applying to join is held until they pass or fail; somebody who joins a group that does not ask people to apply is muted until they do. It also provides group moderation, package and documentation lookups across distributions, and optional bug and news feeds.

## Two editions

One codebase, two binaries. They differ only in who gets the unqualified command names.

| | Built for | Gentoo lookups |
| --- | --- | --- |
| `gentoo-zh-verify-bot` | the Gentoo-zh Community | `/pkg` `/use` `/bug` `/news` `/bbs` `/arm` |
| `gentoo-zhbot` | Linux communities in general | `/gpkg` `/guse` `/gbug` `/gnews` `/gbbs` `/garm` |

Everything else is identical, including verification, moderation, the settings panel, and the lookups every Linux community shares: `/pkgs` `/distro` `/armpkgs` `/wiki` `/kernel` `/man` `/cve` `/repology`. A group running Arch or Debian keeps `/pkg` free for whatever it wants to bind it to, and can still ask a Gentoo question when one comes up.

Two more things differ by build: the identity sentence in the direct-message reply, and the built-in questions used when a group has configured none. The general edition claims no community of its own, and its questions ask about kernel.org and gnu.org, because an applicant elsewhere cannot name the Gentoo-zh Community's domain.

Build the Gentoo edition with `-tags gentoo`; the default build is the general one. Both are published on every release, for `amd64` and `arm64`.

## Fit and operating footprint

The bot works with Telegram groups and supergroups, whether or not they use join requests. It must be a group administrator with **Invite Users**, **Ban Users**, and **Delete Messages** permissions. If a required-channel gate is enabled, the bot must also be an administrator of that channel. BotFather privacy mode only needs to be disabled for `/bc`.

Releases are static Linux binaries for `amd64` and `arm64`. With Telegram's hosted Bot API and the default data sources, the bot requires outbound HTTPS only; it needs no database, reverse proxy, or inbound port. The supplied systemd unit keeps durable state in a private directory and sets `MemoryMax=512M`. The `512M` value is a safety limit, not a claim about resident memory use.

## Verification flow

Each join request is handled independently for its group. The per-group `delivery_mode` defaults to `both`: the bot posts the group challenge first, with a `verify_<groupID>` deep link that opens only that pending request, and then attempts the private challenge. Either confirmed send starts the full answer window.

`group` sends only the group challenge. `dm` attempts private delivery and posts the group challenge only after a definite Telegram rejection. A transport or 5xx error may follow an accepted private send, so `dm` suppresses fallback when delivery is uncertain; `both` never posts a second group copy.

| Mode | Applicant action | Behavior |
| --- | --- | --- |
| `kernel` (default) | Type the version of the Linux kernel currently running; use `uname -r` to obtain it | Up to 3 typed attempts. An applicant without a Linux device must first state that and provide the current minute, then answer a short question without seeing the accepted answers. |
| `quiz` | Select the correct shuffled option from the question bank | Uses the group's `questions`; an empty bank falls back to `kernel`. |
| `mixed` | Complete a randomly selected `kernel` or `quiz` challenge | Selected independently for every application; an empty question bank uses `kernel`. |

The `kernel` and short-answer prompts include an applicant-specific LLM delegation tripwire. Exact compliance with the hidden instruction fails verification. A self-declared model name is recorded only as an aggregate tally, so this is a deterrent rather than a security boundary.

A group can exempt confirmed members of trusted groups or require applicants to join a channel before approval. A wrong answer or timeout declines the request and starts a cooldown. By default, 3 failures within the 6-hour counting window trigger an automatic ban. Applicant messages use Simplified Chinese, Traditional Chinese, or English according to Telegram `language_code`; group and administrator messages use the group's `lang`.

## Installation

`BOT_TOKEN` is the only thing you have to supply. The install script downloads the release binary for this machine, checks it against the published `SHA256SUMS`, installs the systemd unit, and enables the service. Running it again upgrades in place and never overwrites `bot.env`.

```sh
curl --fail --location --remote-name \
  https://raw.githubusercontent.com/Zakkaus/gentoo-zh-verify-bot/main/deploy/install.sh
sh install.sh                       # or: sh install.sh v4.3.0
sudoedit /etc/gentoo-zh-verify-bot/bot.env   # add BOT_TOKEN=<token from @BotFather>
sudo systemctl start gentoo-zh-verify-bot
```

Read the script before running it; it is short and does nothing but the steps above.

### Building from source instead

Requires Go 1.26.7 or later. The unit and paths are the same as above.

```sh
CGO_ENABLED=0 go build -trimpath -o gentoo-zh-verify-bot ./cmd/gentoo-zh-verify-bot
sudo install -Dm755 gentoo-zh-verify-bot /usr/local/bin/gentoo-zh-verify-bot
sudo install -Dm644 deploy/gentoo-zh-verify-bot.service /etc/systemd/system/
sudo install -Dm600 /dev/null /etc/gentoo-zh-verify-bot/bot.env
sudoedit /etc/gentoo-zh-verify-bot/bot.env
sudo systemctl daemon-reload
sudo systemctl enable --now gentoo-zh-verify-bot
```

## First start and group registration

On first start, the service writes a private, one-use owner claim link to the journal. The link expires after 10 minutes by default. Until it is claimed, anyone who can read the journal could become the owner. Set `owner_claim_user_id` to restrict the link to one Telegram user.

```sh
sudo journalctl -u gentoo-zh-verify-bot
```

After claiming ownership, the private command menu is refreshed immediately and shows the member commands plus `/enroll` and `/unregister`. Add the bot to a group and promote it to administrator. The bot registers an owner-authorized group and stores the registration in `settings.json`. Run `/settings` in the group to review its verification mode, delivery mode, question banks, and required channel.

For delegated registration, the owner sends `/enroll` in a private chat and gives the resulting one-use group link to an administrator of that group. The link remains valid for 10 minutes. The bot automatically leaves unknown groups that have neither owner authorization nor a valid enrollment link.

The owner can send `/unregister <group-id>` in a private chat. This command accepts runtime-registered groups only. It removes the registration and that group's runtime overrides, then attempts to leave the group. Removing the bot directly does not clear its registration.

## Configuration

`config.json` is optional and most deployments never need one: groups are added at runtime and almost every setting is reachable from `/settings` without a restart. [`config.example.json`](config.example.json) is a two-line starting point, and the [configuration reference](docs/configuration.md) lists every field with its default and says which ones the settings panel already covers.

Values resolve in one order: a runtime override in `settings.json` wins, then `config.json`, then the built-in default. Editing `config.json` needs a restart; the panel does not.

Three environment variables exist. `BOT_TOKEN` is required; `GITHUB_TOKEN` raises the GitHub API allowance used by overlay lookups; `TELEGRAM_API_URL` points at a self-hosted Bot API server.

## Commands

| Scope | Commands | Purpose |
| --- | --- | --- |
| Registered group or private chat | `/help`, `/ping`, `/stats` | Show help, runtime state, and statistics. These commands do not consume the private lookup allowance. |
| Registered group or private chat | `/pkg`, `/use`, `/arm` | Query Gentoo packages, USE flags, and `arm64` keyword status. |
| Registered group or private chat | `/bug`, `/news`, `/wiki`, `/bbs` | Query Gentoo Bugzilla, Gentoo news, Gentoo Wiki, ArchWiki, and Arch Linux CN. |
| Registered group or private chat | `/pkgs`, `/distro`, `/armpkgs` | Compare package versions or `arm64` support across distributions; `/distro` is an alias for `/pkgs`. |
| Registered group or private chat | `/kernel`, `/man`, `/cve`, `/repology` | Look up kernel.org release versions, Linux manual pages, CVE identifiers, and package versions across distribution repositories. |
| Administrator in a registered group | `/mute [duration]`, `/unmute`, `/ban`, `/sb`, `/warn`, `/clearwarn` | Reply to the target message, then mute, ban, purge, or warn that user. |
| Administrator in a registered group | `/start`, `/stop`, `/settings`, `/bantime`, `/bc`, `/spoiler`, `/vmode`, `/autodel` | Change verification, moderation, and message policy for the current group; runtime values are written to `settings.json`. |
| Administrator in the control group | `/rich` | Change bot-wide rich output for `/pkg` and `/use`. |
| Owner in a private chat | `/enroll`, `/unregister <group-id>` | Issue a group enrollment link or remove a runtime-registered group. |
Command names above are the Gentoo edition's. In `gentoo-zhbot` the six Gentoo lookups are `/gpkg`, `/guse`, `/gbug`, `/gnews`, `/gbbs`, and `/garm`; every other name is the same, and `/rich` governs `/gpkg` and `/guse` there.

External lookups in private chats are limited per user to `private_query_per_min` requests per minute; registered groups are unlimited. `/start` also carries the owner-claim, group-enrollment, verification, and settings-panel deep links; each link type is accepted only in its corresponding private-chat or group scope.

## State, restarts, and outages

The supplied unit uses `StateDirectory=gentoo-zh-verify-bot` to create `/var/lib/gentoo-zh-verify-bot` with mode `0700`. Without `$STATE_DIRECTORY`, ordinary runtime state is memory-only, and owner claims and runtime group registration fail.

| File | State preserved across restarts |
| --- | --- |
| `settings.json` | Owner identity, group registrations, control group, one-use enrollment capabilities, and per-group and bot-wide runtime overrides, including `/bc` state and sender-channel whitelists. |
| `pending.json` | In-progress verification, delivery status, group/private challenge message IDs, mode, language, question, attempts, nonce, and deadline. |
| `verifyfail.json`, `agents.json`, `heartbeat.json` | Verification failures and cooldowns, the cumulative LLM-tripwire tally, and the last successful Telegram contact. |
| `warns.json` | Per-group, per-user `/warn` counts. |
| `feed-<chat_id>.json` | Feed cursors and tracked Bugzilla messages. |

Daily `/stats`, settings-panel sessions and drafts, rate-limit windows, caches, cleanup timers, and transient alert throttles do not survive a restart. `antispam.json` is legacy migration input only; the current version does not write it.

The systemd unit uses `Restart=always`. Unless systemd stops it deliberately, the process restarts 30 seconds after exit, with no start-limit latch. `WatchdogSec=120s` is progress-based rather than a fixed heartbeat: the process notifies the watchdog only after a `getUpdates` call completes, and each call is bounded at 45 seconds. Quiet polls and failed retries therefore count as progress, while a stuck poll causes systemd to restart the service.

When Telegram is unreachable, an expiring verification receives a new full window without being declined or recording a failure. After an in-process outage longer than 90 seconds, every in-memory verification receives a fresh window. After a restart, restored `pending.json` entries also receive fresh windows when `heartbeat.json` proves that the service was down for more than 90 seconds. Each recovery attempts to re-notify at most 30 applicants. Telegram retains updates for a disconnected bot for about 24 hours, so a longer outage may lose join requests the bot never received. On recovery, the bot alerts administrators to inspect Telegram's pending join-request queue manually when `heartbeat.json` is readable.

## Adapting it to another community

Most communities need no fork. Run the `gentoo-zhbot` edition and configure it: groups, verification modes, both question banks, the three existing locales, overlays, the news source, feed destinations, and message policy are all set through `config.json` or the settings panel without touching code.

Replacing the Gentoo-specific behaviour outright, rather than leaving it behind a `g` prefix, requires a complete cutover of:

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
