# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and this project adheres to
[Semantic Versioning](https://semver.org/).

## [1.8.1] - 2026-06-20

### Changed
- `/distro` with no exact match now shows the **closest cross-distro package's full version
  table** (ranked by distro coverage) plus a collapsible (`<details>` in rich) list of other
  matches, instead of a bare list of names — so near-misses and vague queries still return
  real data. (The Linux kernel stays unresolvable cross-distro: each distro names it
  differently and neither Repology nor pkgs.org exposes a clean unified entry.)

## [1.8.0] - 2026-06-20

### Changed
- `/distro` now links **each distro to its package page** (clickable), honors the
  rich-message toggle (`rich_messages` / `/rich`) like `/pkg` and `/use`, and — when
  there's no exact match — suggests the closest packages that **actually exist across
  distros** (ranked by coverage, language-namespaced entries filtered) instead of
  silently picking a wrong or unpackaged project (e.g. `kernel` no longer → `genkernel`).

## [1.7.1] - 2026-06-20

### Added
- On startup the bot logs whether it is an admin in each guarded group, so a group it
  hasn't been granted admin in yet is clearly visible (and confirmed harmless — Telegram
  doesn't deliver join requests there) rather than silently inert.

## [1.7.0] - 2026-06-20

### Added
- **Per-group configuration** — a new `groups` array lets each guarded group set its own
  `required_channel_id`, `channel_display`, `channel_invite_url` and `questions`, each
  falling back to the global default when unset. The legacy `group_ids` / `group_id` are
  still accepted (treated as groups with no overrides). A configured group the bot isn't
  yet an admin of stays inert (no join requests reach it) rather than erroring.

## [1.6.0] - 2026-06-20

### Added
- **`/distro <pkg>`** — cross-distro package version lookup via the Repology API,
  showing the current version in AUR, Arch, Debian, Ubuntu, Nixpkgs, openSUSE and
  Fedora in one message.

## [1.5.4] - 2026-06-20

### Changed
- Feed bug footer is split into separate labelled lines (负责 / 报告 / 日期), and the
  **assignee and reporter now link to their Gentoo Bugzilla bug list** (substring email
  match, since Bugzilla redacts emails for anonymous API access).

## [1.5.3] - 2026-06-20

### Changed
- Feed bug **Priority** and **Severity** are now shown as two separate labelled lines
  (优先级 / 严重性) instead of one combined `重要度` line, so even identical values read
  clearly. Supersedes the 1.5.2 collapse.

## [1.5.2] - 2026-06-20

### Changed
- Feed bug **importance** collapses a redundant priority·severity when both render the
  same word (e.g. `普通 · 普通` → `普通`, `Normal · normal` → `Normal`); distinct
  values like `普通 · 增强` are unchanged.

## [1.5.1] - 2026-06-20

### Changed
- Feed bug notifications are now status-aware: **UNCONFIRMED bugs post silently** (a
  fresh report may be a false alarm) and confirmed bugs post with a notification.
  `silent_bugs: true` still forces every bug silent. (Was: all bugs silent by default.)

## [1.5.0] - 2026-06-20

### Added
- **`/bc`** — admin command to toggle the channel sock-puppet filter on/off at runtime,
  plus `/bc allow|deny <channel id>` to manage its whitelist (`allow` also un-bans the
  channel). The toggle and whitelist are now **persisted** (`antispam.json`), so they
  survive restarts; `block_channel_senders` / `channel_whitelist` seed the initial state.

## [1.4.0] - 2026-06-20

### Added
- **Channel sock-puppet block** (`block_channel_senders`, off by default) — a message
  posted in a guarded group on behalf of a channel is deleted and that channel is
  banned from posting; anonymous group admins and the linked discussion channel are
  exempt, and a `channel_whitelist` allows specific channels. Requires the bot's
  privacy mode to be OFF so it can see these messages.

## [1.3.1] - 2026-06-20

### Fixed
- The DM auto-reply now also covers **commands** sent in a private chat (`/pkg`,
  `/help`, …), which previously matched their group-only handler and silently did
  nothing. The `/start` verification deep link still reaches the verify flow.

## [1.3.0] - 2026-06-20

### Added
- **DM auto-reply** — a direct message to the bot outside the verification flow now
  gets a single unified reply (pointing to the group + commands) instead of silence.
  Customizable via the `private_reply` config key.

## [1.2.1] - 2026-06-20

### Changed
- The Chinese bug feed (and `/bug`) now localizes the Bugzilla **status, resolution,
  severity and priority *values*** to Chinese (e.g. `RESOLVED / FIXED` → 已解决 / 已修复,
  `Normal · normal` → 普通 · 普通), not just the field labels. The English (`lang:en`)
  feed is unchanged. Component names, keywords and people stay as-is.

## [1.2.0] - 2026-06-20

### Added
- **`/bbs <query>`** — Linux forum search. Inline results from the Arch Linux CN
  forum (Chinese, via its Discourse API), plus one-tap site-search buttons for the
  major English forums (Gentoo, Arch BBS, Ubuntu, Debian) — Chinese first, English
  as backup.

## [1.1.1] - 2026-06-20

### Changed
- `/wiki` now shows each page's **Chinese display title** for Gentoo `/zh-cn` pages
  (e.g. "Kernel/Gentoo 内核配置指南" instead of the English "…/zh-cn" title), filters
  out foreign-language pages that aren't tagged as translations, and widens the
  search window to surface more simplified-Chinese pages.
- `/help` and the command menu now show the actual configured `warn_limit` (was a
  literal "N").

## [1.1.0] - 2026-06-20

### Added
- **`/wiki <query>`** — search the Gentoo and Arch wikis (MediaWiki), **preferring
  Simplified-Chinese pages** and falling back to the default page; other languages
  are filtered out.
- **`/warn` and `/clearwarn`** — an admin, reply-based warning system. A user is
  auto-kicked (rejoinable) after `warn_limit` strikes (default 3); counts persist
  across restarts.

## [1.0.1] - 2026-06-20

### Added
- **Auto-leave unauthorized chats** — the bot now leaves any group/channel it is
  added to that isn't a configured chat (a guarded group, the required channel, a
  feed target, or the admin-log chat). Prevents being pulled into arbitrary groups.

## [1.0.0] - 2026-06-20

First stable release.

### Features
- **Join-request verification:** an in-group deep link opens a DM quiz (randomized,
  option-shuffled), with an optional required-channel follow gate (two-step in DM).
  Wrong answer / timeout auto-declines; admins can one-tap approve or report-and-ban.
- **Multiple guarded groups** from a single instance; **restart-safe** — in-progress
  verifications are persisted and their timers re-armed on restart.
- **Moderation:** `/sb` (delete + kick, rejoinable) and `/ban` (delete + permanent
  ban) — admin-only, reply-based, and they refuse to target other admins.
- **Gentoo helpers:** `/pkg` (official tree + overlays, with versions), `/use` (USE
  flags + info), `/bug` (Bugzilla), `/news`. Optional Bot API 10.1 rich output for
  `/pkg` and `/use` (`rich_messages` / `/rich`), off by default.
- **Auto-feed (optional):** polls Gentoo Bugzilla + news and broadcasts new items to
  one or more destinations (`feeds`), each with its own language and filters, from a
  single shared fetch per cycle. Deduped, restart-safe, baselines on first run.
- Single static binary (only dependency: [telego](https://github.com/mymmrac/telego));
  long polling, no inbound port; ships a hardened `systemd` unit (`DynamicUser` +
  sandboxing) and reads its token from the environment.

[1.8.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.8.1
[1.8.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.8.0
[1.7.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.7.1
[1.7.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.7.0
[1.6.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.6.0
[1.5.4]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.5.4
[1.5.3]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.5.3
[1.5.2]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.5.2
[1.5.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.5.1
[1.5.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.5.0
[1.4.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.4.0
[1.3.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.3.1
[1.3.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.3.0
[1.2.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.2.1
[1.2.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.2.0
[1.1.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.1.1
[1.1.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.1.0
[1.0.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.0.1
[1.0.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.0.0
