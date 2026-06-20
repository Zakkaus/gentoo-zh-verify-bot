# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and this project adheres to
[Semantic Versioning](https://semver.org/).

## [3.3.0] - 2026-06-21

Configurable ban duration + verification anti-spam, plus fixes from an external code review
and a follow-up adversarial review of the new code (13 confirmed findings, each verified).

### ⚠️ Upgrade note
- **`/sb` is now a ban, not a re-joinable kick.** Both `/sb` and `/ban` now ban for the
  configured duration (`/bantime`, **default permanent**). If you relied on `/sb` being a
  soft kick, set `ban_seconds` to a finite value (e.g. `3600`) or use `/warn` for lenient
  moderation.

### Added
- **Configurable ban duration** — `/bantime` (admins): `0`=permanent (default), or `7d`/`12h`/
  `30m`/`3600`. Used by `/ban`, `/sb`, the verification auto-ban and the report button. Config
  `ban_seconds`. Durations are clamped to Telegram's honoured window (under 30 s → 30 s, over
  366 days → permanent) so the reported duration always matches what Telegram enforces.
- **Verification anti-spam** — a failed verification declines with a `verify_retry_seconds`
  (default 180) cooldown before re-applying; after `verify_max_fails` (default 3) failures
  **within a rolling window** the applicant is auto-banned for the configured duration. Strikes
  persist across restarts, reset on success, and **age out** so a genuine user's isolated
  mistakes don't accumulate. Negative values disable the cooldown / auto-ban.
- `required_channel_fail_open` — keep the default fail-open (a channel-permission slip won't
  lock everyone out) or set `false` to strictly enforce the channel gate.

### Fixed
- **Build toolchain** raised to **Go 1.26.4** (`go.mod` was `1.25.7`, below the fix for two
  stdlib advisories — `net/textproto` GO-2026-5039 and `crypto/x509` GO-2026-5037); **CI now
  runs `govulncheck`**. (The deployed binary was already built with 1.26.4.)
- **Stale DM quiz buttons** can no longer answer a *new* verification: each pending carries a
  random **nonce** in its callback data (legacy 3-part buttons still work across the upgrade).
- **`/ban` report button** reports honestly — a failed ban no longer claims success.
- **Verification auto-ban** only clears an applicant's strikes when the ban **actually
  succeeds**; where the bot lacks ban rights, strikes are kept (no infinite-retry loop) and
  admins keep getting alerted.
- **Data race** in the verification cooldown read fixed (fields copied under the lock).
- **State writes** use a unique temp file + a serialize lock (no shared-`.tmp` clobber under
  concurrent saves).
- Config **fail-fast validation**: rejects group id `0`, duplicate group ids, and malformed
  overlay `repo` / duplicate overlay names. `/news` now logs loudly if it parses 0 items
  (page-layout drift) instead of silently returning empty.

## [3.2.0] - 2026-06-20

Auto-delete consistency pass + a third multi-dimension audit (7 fresh dimensions, each
finding adversarially verified): 23 raised, 13 confirmed (0 critical/high), all fixed below.

### Changed
- **Auto-delete is now consistent across a lookup's whole interaction.** A lookup command's
  usage hint / "not found" / disambiguation reply previously used a path that left the command
  un-deleted and could fail to render; it now replies (reply-linked) and the command + reply
  are removed together after `lookup_ttl`, exactly like a successful answer. Control/admin
  commands keep deleting their trigger immediately; auto-delete still never runs in a DM.

### Fixed
- **`/pkgs` no longer shows a bare-date snapshot instead of the real version.** A package a
  distro ships as a bare 8-digit `YYYYMMDD` (e.g. Debian's `gcc-snapshot`) was treated as a
  huge real version and beat the actual release; it's now recognised as a date and ranked
  below real releases (gcc Debian shows `16.1.0` / `14.2.0`, not the snapshot date).
- **`/pkgs` Ubuntu line shows the current released release, not an in-development one.** An
  unreleased Ubuntu series (e.g. `26.10` before its release date) and `proposed`/`backports`
  pockets are now excluded from the stable line (derived live from distro-info-data release
  dates, mirroring Debian) — so it shows e.g. `26.04 LTS`, not `26.10`.
- **`private_reply`** (admin-supplied DM auto-reply, sent in HTML mode) now falls back to
  plain text and logs the error if a stray `<`/`>`/`&` makes Telegram reject it — so a typo
  in the config can't leave DMs silently unanswered.

### Hardened
- The private-chat query rate-limit map (`queryHits`) now has a hard upper bound (wholesale
  clear, like `dmLast`) instead of only soft eviction — flood-proof under a pathological burst.
- `ensureReleaseInfo` has an in-flight guard so a burst of `/pkgs` on a cold/expired cache
  triggers one upstream distro-info fetch, not N.

### Internal
- Factored the duplicated lookup send+reply+cleanup tail into `replyLookupHTML`/`replyLookupPlain`
  (used across all lookup handlers); factored `/autodel`'s argument parsing into a pure,
  unit-tested `parseAutoDelArg`. Added tests for the bare-date detection, the Ubuntu exclusion,
  and the `/autodel` parser. Fixed stale command lists (the `/autodel off` message, the
  `lookup_ttl_seconds` docs and the `/armpkgs` help/menu omitted `/arm`·`/armpkgs`·AUR) and a
  `/pkgs` "not found" message that under-listed distro families. Removed audit scratch files;
  `dm_test.go` uses `context.TODO()` (staticcheck-clean).

## [3.1.3] - 2026-06-20

### Fixed
- **`/help` in a private chat now actually outputs.** Its DM reply was sent in HTML parse
  mode, but the help text contains literal `<包名>` / `<编号>` placeholders that Telegram
  rejects as malformed HTML ("can't parse entities") — and the send error was discarded, so
  nothing appeared. It (and `/ping` / `/stats` in DM) now send as plain text, matching the
  group path. Verified against the live Bot API.

## [3.1.2] - 2026-06-20

### Fixed
- The per-minute private-chat rate limit now applies **only to the commands that make an
  external request** (`/pkg` `/use` `/bug` `/news` `/wiki` `/bbs` `/pkgs` `/arm` `/armpkgs`).
  The cheap local commands `/help`, `/ping`, `/stats` are no longer counted against it, so
  they always respond in a DM (previously `/help` could be blocked after a few queries).

## [3.1.1] - 2026-06-20

### Fixed
- `/help`, `/ping` and `/stats` now also work **in a private chat** (they previously got the
  generic auto-reply). And `/ping` / `/stats` — which are in the member command menu — are
  now usable by any member, not just admins (they were incorrectly admin-gated). All member
  commands are now consistent: usable in a guarded group (unlimited) and in a DM (rate-limited);
  admin/moderation commands stay group-only.

## [3.1.0] - 2026-06-20

### Added
- **Lookup commands now work in a private chat** with the bot (`/pkg` `/use` `/bug` `/news`
  `/wiki` `/bbs` `/pkgs` `/arm` `/armpkgs`), **rate-limited per user** to
  `private_query_per_min` queries/minute (config, default 3) to prevent abuse — guarded
  groups stay unlimited. Other DMs still get the unified auto-reply (now updated to mention
  this). Auto-delete doesn't apply in DMs (nothing to keep tidy there).

## [3.0.0] - 2026-06-20

A milestone release: the cross-distro `/pkgs` channel logic is now centred on each distro's
**current stable release**, with the rolling/dev channel shown above it when ahead.

### Changed
- `/pkgs` now centres on the **current stable release** and adds the rolling/dev channel
  above it only when that's newer — so Fedora shows its stable (`44`) even when rawhide
  matches it (was: `(rawhide)`), and a package whose stable lags shows both (rawhide + 44).
- **Debian's "stable" excludes the testing series** (forky/14 today) using the live
  distro-info-data status, so the stable line is the real stable (`13`/trixie), e.g.
  `nano 9.0 (unstable/sid)` + `8.4 (13 stable)` instead of mistaking testing for stable.
- The Debian unstable channel is labelled **`unstable/sid`** (it's codenamed sid, which many
  people track) so it's recognisable.

### Notes
- Kept the flat single-`package main` layout (22 source files, by-command) rather than
  splitting into sub-packages — see CONTRIBUTING "Project layout" for the rationale.

## [2.7.0] - 2026-06-20

### Added
- `/pkgs` labels Debian/Ubuntu releases by their **live role** — `stable` / `testing` /
  `oldstable` / `LTS` — derived from Debian's `distro-info-data` (release dates), not
  hardcoded; so when Debian 14 releases, "stable" follows it with no code change. (Debian
  firefox now reads `140.12.0 (13 stable)`.)
- The **RHEL ecosystem is split into three distinct families**: **RHEL** (the AlmaLinux/Rocky
  1:1 rebuilds — the actual RHEL versions), **CentOS Stream** (the rolling upstream of the
  next RHEL), and **EPEL** — each with its own version, since they are different products.
  (firefox now shows `RHEL 140.11.0 (9)` separately from `CentOS Stream 140.11.0 (10)`.)

## [2.6.1] - 2026-06-20

### Fixed
- `/pkgs` labels each version by the **newest release that actually ships it**, not whichever
  repo was scanned first — so e.g. RHEL/EPEL firefox `140.11.0`, present in AlmaLinux 8/9 and
  CentOS Stream 9/10, now shows `(stream.10)` instead of the misleading `(8)`.

## [2.6.0] - 2026-06-20

Hardening from a second full multi-dimension audit (each finding adversarially verified).

### Fixed
- **Version comparator**: a Gentoo pre-release (`1.0_rc1`, `_alpha`, `_beta`, `_pre`) is now
  correctly older than the release, and a patch `_pN` / revision `-rN` newer — previously any
  extra suffix sorted as *newer*, so `/pkg`/`/distro` could show an rc as "latest".
- **`commandArg`** splits on the first run of whitespace, so a tab/newline-separated argument
  (a pasted `/pkg<newline>vim`) works, not just a single space.
- **Feed poll interval** now clamps a too-fast `interval_seconds` (1–59) to the 60 s floor
  instead of silently falling back to 5 minutes.

### Changed / Hardened
- Lookup commands root their HTTP timeout on the request context, so in-flight work is
  cancelled on shutdown instead of lingering ~20 s.
- Persistence writes now go through one atomic helper that **logs** marshal/write/rename
  failures; the bot `MkdirAll`s `STATE_DIRECTORY` on start; the warn/antispam/feed loaders
  log a corrupt-file parse error (matching `pending.json`) instead of silently dropping state.
- Duplicate feed targets (same `chat_id`) are de-duplicated at config load (they would have
  shared one cursor and dropped each other's items).
- **CI now runs `go test -race ./...`** (the test suite previously wasn't gating merges).
- The build embeds a `version` (via `-ldflags -X main.version`), shown in `/ping` and the
  startup log.

## [2.5.3] - 2026-06-20

### Changed
- `/pkgs` shows a distro's **rolling/dev channel AND its current stable** on separate lines
  when they differ (e.g. Debian `152.0.1 (unstable)` + `140.12.0 (13)`), like the Gentoo
  amd64/~amd64 split — instead of only the newest. The stable is labelled by the highest
  release that actually ships it (Debian → `13`/trixie, not `14`/forky), and a package at one
  version everywhere stays a single line (labelled by its rolling channel when applicable).

## [2.5.2] - 2026-06-20

### Changed
- `/armpkgs` output is tidier: each distro is a **clickable link** to its package page, and
  Debian/Ubuntu show just the newest arm64 channel (not three) with a shorter footer.
- `/pkgs` Debian/Ubuntu links now point to the **clean package pages** (tracker.debian.org,
  Launchpad) instead of the cluttered `packages.debian.org` / `packages.ubuntu.com` search.

## [2.5.1] - 2026-06-20

### Internal
- Renamed `distro.go` → `pkgs.go` and the handler `onDistro` → `onPkgs` so the file
  matches the now-primary `/pkgs` command (`/distro` stays an alias). No behaviour change.

## [2.5.0] - 2026-06-20

### Added
- `/armpkgs` now also checks **AUR** (from the PKGBUILD `arch=()` declaration: any /
  aarch64 / 32-bit-ARM-only / x86-only).

### Fixed
- `/distro` no longer mis-attributes a different distro's repo to a family when the repo
  id merely shares a prefix (e.g. Arch Linux POWER's `archpower_*` was being counted as
  "Arch"); matching now requires an exact id or a `<prefix>_<release>` form.

## [2.4.0] - 2026-06-20

### Added
- **`/armpkgs <pkg>`** — cross-distro arm64 (aarch64) support check across **Gentoo,
  Debian, Ubuntu, Fedora and Arch Linux ARM**, each via its own per-architecture API
  (Debian/Ubuntu madison arch-filtered, Fedora mdapi, ALARM package presence), queried
  concurrently. Built for the case where Gentoo hasn't keyworded a package for arm64 but
  other distros ship it.
- **`/pkgs`** as a memorable alias for `/distro` (cross-distro version search).

## [2.3.0] - 2026-06-20

### Changed
- `/distro` now annotates each distro's version with the **release it comes from** —
  e.g. `Debian 152.0.1 (unstable)`, `Fedora 152.0 (43)`, `Alpine … (edge)`,
  `openSUSE Leap … (15.6)` — so you can tell whether a version is from a rolling/unstable
  branch or an older stable release. Stays one line per distro (no per-release wall).

## [2.2.0] - 2026-06-20

### Added
- **`/arm <pkg>`** — show a Gentoo package's **arm64 (aarch64) keyword status** (stable
  version, newest `~arm64` testing version, or "not keyworded"), so ARM users can quickly
  see whether a package is available/tested for their arch.

## [2.1.0] - 2026-06-20

### Added
- **Auto-delete for lookup results** — a lookup command (`/pkg` `/use` `/bug` `/news`
  `/wiki` `/bbs` `/distro`) and its answer are removed after a delay (default **3 minutes**)
  to keep the group tidy. Configurable via `lookup_ttl_seconds` (unset → on at 3 min;
  `0`/negative → off) and adjustable at runtime by admins with **`/autodel`**
  (`/autodel on|off` or `/autodel <minutes>`).

## [2.0.0] - 2026-06-20

Stable release after a full multi-dimension audit (logic, APIs, concurrency/leaks,
docs, permissions, robustness), each finding adversarially verified and the confirmed
ones fixed and unit-tested.

### Added
- Lookup commands (`/distro` `/pkg` `/use` `/bug` `/news` `/wiki` `/bbs`) now **reply to
  the command message**, so concurrent answers (these hit slow external APIs) can't be
  mistaken for one another.
- A unit-test suite (`*_test.go`) covering the version comparator, per-group config
  resolution/validation, feed cursor logic, status-aware notifications and the quiz shuffler.
- Startup now probes each required channel and logs whether the bot can read its members.

### Fixed
- **Feed news dedup** no longer re-broadcasts the entire news archive if the stored cursor
  URL falls out of the fetched list — it re-baselines instead of flooding the channel.
- **Feed bug cursor** is now strictly forward-only (can't regress and re-post older bugs).
- **Version comparison** (`/pkg`, `/distro`) handles double-digit revisions/suffixes
  correctly (`r10` is newer than `r2`), via overflow-safe natural-order token comparison.
- **Required-channel gate fails open** when the bot can't read the channel (it isn't an
  admin there): rather than silently locking every applicant out, it passes verified users
  through and alerts admins — so a permission slip can't break joining.

### Changed / Hardened
- Bounded the `/pkg` version/info caches; warn when `STATE_DIRECTORY` is unset (persistence
  off) or a feed has `chat_id=0`; the DM auto-reply is throttled per user; failed admin-log
  sends are now logged. `config.example.json` gains `warn_limit` + `silent_bugs`; docs and
  the `/distro` command-menu text brought in sync.

## [1.9.1] - 2026-06-20

### Changed
- `/distro` now shows Gentoo's **amd64-stable and ~amd64 (testing) on separate lines**
  when they differ (from packages.gentoo.org, reusing the `/pkg` version logic) — e.g.
  firefox shows `Gentoo amd64 140.12.0` and `Gentoo ~amd64 152.0` — and collapses to one
  line when stable == testing.

## [1.9.0] - 2026-06-20

### Changed
- `/distro` now also covers **Gentoo and Alpine**, and lists ecosystem variants on
  separate lines (Fedora vs RHEL/EPEL, openSUSE Leap vs Tumbleweed). Per-distro version
  picking now prefers a real release over a date/CalVer over a Gentoo `9999` live ebuild,
  so each family shows its actual packaged version (a date-only project like yt-dlp still
  shows its newest date).

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

[3.3.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.3.0
[3.2.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.2.0
[3.1.3]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.1.3
[3.1.2]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.1.2
[3.1.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.1.1
[3.1.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.1.0
[3.0.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v3.0.0
[2.7.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.7.0
[2.6.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.6.1
[2.6.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.6.0
[2.5.3]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.5.3
[2.5.2]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.5.2
[2.5.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.5.1
[2.5.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.5.0
[2.4.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.4.0
[2.3.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.3.0
[2.2.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.2.0
[2.1.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.1.0
[2.0.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v2.0.0
[1.9.1]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.9.1
[1.9.0]: https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/tag/v1.9.0
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
