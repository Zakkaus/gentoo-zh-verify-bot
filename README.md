# gentoo-zh-verify-bot

English | [简体中文](README.zh-CN.md)

A lightweight Telegram **join-request verification bot** written in Go — a single static binary whose only dependency is [telego](https://github.com/mymmrac/telego).

Built for open-source community groups that get flooded with spam-bot join requests. When someone requests to join, the bot posts a verification link in the (public) group; the applicant opens the bot, answers a quiz (and optionally must have joined a channel), and only then is approved. Admins can also approve or report-and-ban an applicant with one tap. Includes light moderation commands and a Gentoo package search.

## Features

**Join verification** — a join request is **not** auto-approved. The bot posts an in-group message @-mentioning the applicant with a `✅ 完成验证` deep-link; the applicant opens the bot, answers a randomized (crypto-shuffled) multiple-choice quiz in DM — optionally after joining a **required channel** (two-step DM prompt; private channels via `channel_invite_url`) — and only then is approved. Wrong answer / timeout declines. Each request also gets admin **👮 直接通过** / **🚫 举报并封禁** buttons.

- **Anti-spam:** a failed verification declines with a cooldown (`verify_retry_seconds`, 180 s); after `verify_max_fails` (3) failures within a few hours the applicant is auto-banned. Strikes persist, reset on success, and age out.

**Moderation** (admins, reply to a message):

| Command | Action |
| --- | --- |
| `/mute [时长]` · `/unmute` | 禁言 — stays in group but can't post; timed (default 1 h, e.g. `/mute 30m`); `/unmute` lifts early |
| `/ban` | 封禁 — remove from group; duration from `/bantime` (default permanent, or timed = rejoin after) |
| `/sb` | 举报并封禁 — like `/ban` + delete **all** the user's messages |
| `/warn` · `/clearwarn` | strike (auto-kick at `warn_limit`, default 3) · clear strikes |
| `/bantime` | set the ban duration: `0`=permanent, or `7d`/`12h`/`30m` |
| `/bc` | block channel sock-puppets + whitelist (needs privacy mode OFF; persists) |

**Gentoo / Linux lookups** (also work in DM, rate-limited to `private_query_per_min`/min):

| Command | Looks up |
| --- | --- |
| `/pkg <name>` | Gentoo package + version (official tree + `gentoo-zh`/`guru` overlays) |
| `/use <pkg>` | a package's USE flags + info |
| `/bug <id>` | a Gentoo Bugzilla bug |
| `/news [kw]` | Gentoo news items |
| `/wiki <kw>` | Gentoo / Arch wiki (Simplified-Chinese pages first) |
| `/bbs <kw>` | Linux forums (Arch Linux CN inline + EN forum buttons) |
| `/pkgs <pkg>` | cross-distro versions via [Repology](https://repology.org), labelled by release; RHEL ≠ CentOS Stream ≠ EPEL |
| `/arm <pkg>` | a Gentoo package's arm64 keyword status |
| `/armpkgs <pkg>` | cross-distro arm64 support (Gentoo/Debian/Ubuntu/Fedora/Arch ARM/AUR) |

**Auto-feed (optional)** — polls Gentoo Bugzilla + news and posts each **new** item to one or more channels (`feed` / `feeds`), each with its own language + filters; deduped, restart-safe, and **edits a bug's message in place when its state changes** — an UNCONFIRMED bug becoming CONFIRMED (plus a one-off 🔔 notification, since the original UNCONFIRMED post is silent), and on resolution 🐞→✅.

**Also:** guards multiple groups; auto-leaves unauthorized chats; persists in-progress verifications across restarts; bot messages auto-delete after a TTL; **hides each new member's display name behind a spoiler by default** so spam accounts can't broadcast an advert via their name (`/spoiler`, persisted); optional rich output for `/pkg` `/use` (`rich_messages` / `/rich`, off by default); `/ping` `/stats` `/start` `/stop` `/autodel` `/rich` `/spoiler` `/help`.

## Telegram setup

1. Create a bot via [@BotFather](https://t.me/BotFather) and get its token.
2. Add the bot to each group as an **administrator** with these rights: **Approve new members**, **Ban users**, **Delete messages**.
3. Each group must be **public** (so pending applicants can see the verification link) and set to **"Approve new members"** (join-by-request).
4. *(Optional channel requirement)* Add the bot to the required channel as an **administrator** (required — Telegram's `getChatMember` only reports other users' membership reliably when the bot is a channel admin), then set `required_channel_id` + `channel_display` in the config.

Tip: get a chat's numeric id (the `-100…` form) by forwarding a message to [@userinfobot](https://t.me/userinfobot) / [@JsonDumpBot](https://t.me/JsonDumpBot), or read this bot's logs.

## Configuration

Token comes from the environment (never commit it):

```sh
# /etc/gentoo-zh-verify-bot/bot.env   (chmod 600)
BOT_TOKEN=123456:ABC-DEF...
# optional: a GitHub token (NO scopes needed) lifts the /pkg overlay API rate
# limit from 60/h to ~5000/h, so you can configure more overlays safely.
GITHUB_TOKEN=ghp_xxx
```

Everything else lives in `config.json` (copy `config.example.json`):

```json
{
  "groups": [
    {"id": -1001234567890},
    {"id": -1009876543210, "required_channel_id": -1001112223334, "channel_display": "@OtherChannel"}
  ],
  "required_channel_id": 0,
  "channel_display": "@YourChannel",
  "timeout_seconds": 240,
  "notify_ttl_seconds": 60,
  "admin_log_chat_id": 0,
  "questions": [
    {"q": "Gentoo 官方的包管理器是?", "options": ["Portage", "apt", "pacman", "dnf"], "answer": 0}
  ]
}
```

| key | meaning |
| --- | --- |
| `groups` | per-group config: `[{id, required_channel_id?, channel_display?, channel_invite_url?, trusted_member_group_ids?, questions?}]`. Each optional field **falls back to the global default** below, so groups can share settings or be configured independently. A bare `group_ids` list (or singular `group_id`) is also accepted and treated as groups with no overrides |
| `required_channel_id` | **global default** channel applicants must join; `0` disables it (override per-group in `groups`) |
| `channel_display` | **global default** channel shown to users, e.g. `@YourChannel` |
| `channel_invite_url` | **global default** explicit join link; required for a **private** channel (no `@handle`) |
| `timeout_seconds` | time to finish verification (default 240, max 1800) |
| `notify_ttl_seconds` | auto-delete the bot's group messages after N s (`0`→60, negative→never) |
| `lookup_ttl_seconds` | auto-delete a lookup command (`/pkg` `/use` `/bug` `/news` `/wiki` `/bbs` `/pkgs` `/arm` `/armpkgs`) and its answer after N s (unset→180 = 3 min, on; `0`/negative→off). Admins toggle/adjust at runtime with `/autodel` |
| `warn_limit` | `/warn` strikes before a user is auto-kicked (default 3) |
| `private_query_per_min` | lookup queries a user may run per minute in a **private chat** (default 3; guarded groups are unlimited) |
| `ban_seconds` | default ban duration for `/ban`, `/sb` and the verification auto-ban; `0` = permanent (default). Runtime-adjustable with `/bantime` |
| `mute_seconds` | default `/mute` (禁言) duration; the user stays but can't post until it expires (default 3600 = 1h; always timed). Override per-use inline, e.g. `/mute 30m`; `/unmute` lifts early |
| `verify_retry_seconds` | a declined applicant must wait this long before re-applying (default 180; negative = no cooldown) |
| `verify_max_fails` | failed verifications before an applicant is auto-banned (default 3; negative = never auto-ban) |
| `required_channel_fail_open` | when the bot can't read the required channel's membership, let verified applicants through (`true`, default) or hold them back (`false`). Admins are alerted either way |
| `trusted_member_group_ids` | **trusted-member bypass**: an applicant who is **already a member of any of these chats** is auto-approved without a quiz (e.g. a sub-group trusting the main group's members). **Global default; per-group**: omit to inherit the global, `[]` to **disable** for that group, or list ids to override. Use real chat ids (groups are `-100…`); the bot must be in each listed chat to read membership (treated as known chats, never auto-left). A trusted member takes **priority over the failure cooldown** (they're approved + their strikes cleared even after a prior failed verify). Unlike a required channel this **fails closed** — if membership can't be confirmed the applicant just does the normal verification |
| `admin_log_chat_id` | optional chat that receives a line per moderation / failed-approve event |
| `overlays` | `/pkg` GitHub overlays `[{name,repo,branch}]` (default: gentoo-zh + guru) |
| `news_url` | `/news` source index URL (default: gentoo.org news-items) |
| `stats_timezone` | IANA tz for the daily /stats reset boundary (default: UTC+8) |
| `rich_messages` | render `/pkg` & `/use` as Bot API 10.1 rich messages (default `false`; also toggleable in-chat via `/rich`) |
| `user_agent` | override the outbound HTTP User-Agent (optional; default `gentoo-zh-verify-bot`) |
| `private_reply` | the unified auto-reply for DMs outside the verify flow (empty → built-in default) |
| `block_channel_senders` | **initial** state of the channel sock-puppet filter (runtime toggle is `/bc`, persisted; default `false`; needs privacy mode OFF). Once `antispam.json` exists it is authoritative — editing this key afterward has no effect until that file is deleted |
| `channel_whitelist` | **initial** channel whitelist (runtime is `/bc allow` / `deny`, persisted to `antispam.json`, which then takes precedence over this key) |
| `feed` / `feeds` | optional auto-feed — poll Gentoo Bugzilla + news and post new items to a chat. `feed` is one destination; `feeds` is an array of them (each with its own chat, language and filters). See below; omit to disable |
| `questions` | **global default** quiz pool; one is picked at random, options shuffled (override per-group in `groups`) |

The optional **`feed`** object — or **`feeds`**, an array of these objects for several destinations (all served by one shared fetch per cycle). Omit both to disable:

| `feed` key | meaning |
| --- | --- |
| `chat_id` | channel/group to post to (`0`/absent disables; the bot must be an admin there with post rights) |
| `lang` | bug field labels: `zh` (default) or `en` |
| `interval_seconds` | poll interval (default 300, min 60) |
| `bugs` | post new Bugzilla bugs (default `true`) |
| `news` | post new news items (default `true`) |
| `bug_product` | only post bugs in this Bugzilla product, e.g. `"Gentoo Security"` (empty = all) |
| `bug_component` | only post bugs in this component, e.g. `"Vulnerabilities"` (empty = all) |
| `silent_bugs` | `true` forces every bug silent. When unset, **UNCONFIRMED bugs post silently** (a fresh report may be a false alarm) and confirmed bugs post with a notification; when a silent UNCONFIRMED bug later becomes CONFIRMED, a one-off 🔔 notice is sent (suppressed when `silent_bugs` is `true`) |

## Build & run

Requires **Go 1.26.4+** (matches `go.mod`; the 1.26.4 toolchain carries security fixes).

> **Install:** grab a prebuilt static `linux-amd64`/`arm64` binary (with `SHA256SUMS`) from the
> [Releases](https://github.com/Zakkaus/gentoo-zh-verify-bot/releases) page, or build from source
> below. Note that `go install …@v3.x` does **not** work (the module path has no `/vN` major-version
> suffix, by design — this is a binary, not an imported library); clone + `go build`, or use a
> release binary.

```sh
CGO_ENABLED=0 go build -o /usr/local/bin/gentoo-zh-verify-bot .
sudo cp deploy/gentoo-zh-verify-bot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now gentoo-zh-verify-bot
journalctl -fu gentoo-zh-verify-bot
```

Uses long polling — no inbound port or reverse proxy needed.

## Notes / limitations

- **State persistence.** Run under systemd (`StateDirectory=` sets `$STATE_DIRECTORY`), the bot persists the state below and reloads it on restart; with `STATE_DIRECTORY` unset, **nothing** is persisted — everything is in-memory only and lost on restart (a warning is logged). The state directory must be **private to the bot's service user** and not writable by untrusted users (the unit's `StateDirectory=` + `DynamicUser=` ensure this).

  | Persisted (`$STATE_DIRECTORY/…`) | What |
  | --- | --- |
  | `pending.json` | in-progress verifications (timers re-armed on restart) |
  | `warns.json` | per-user `/warn` strike counters |
  | `antispam.json` | `/bc` channel sock-puppet state + whitelist |
  | `verifyfail.json` | verification failure strikes / cooldowns |
  | `feed-<chat_id>.json` | feed dedup cursors + tracked bug message IDs |
  | `settings.json` | verification enabled/paused (`/start` · `/stop`) **and** the name-spoiler toggle (`/spoiler`) — both survive a restart |

  **Not** persisted (reset on restart): daily `/stats`; the `/rich`, `/autodel` and `/bantime` runtime overrides; and the lookup / news / package caches.
- The verification link relies on each group being **public**.
- Admin commands must be sent **non-anonymously** — an anonymous-admin post appears as the group, not a user, so it won't pass the admin check.
- Multi-group with **different** required channels: the DM follow-prompt covers the first pending group's channel — sharing one channel across groups is smoothest.
- User-facing strings are **Simplified Chinese** (this bot targets the Gentoo zh community). All *operational* settings are in the config; to localize the wording, edit the string literals in the `.go` sources (mainly `verify.go`, `admin.go`, `commands.go`).

## License

MIT — see [LICENSE](LICENSE).
