# gentoo-zh-verify-bot

English | [简体中文](README.zh-CN.md)

A lightweight Telegram **join-request verification bot** written in Go — a single static binary whose only dependency is [telego](https://github.com/mymmrac/telego).

Built for open-source community groups that get flooded with spam-bot join requests. When someone requests to join, the bot posts a verification link in the (public) group; the applicant opens the bot, answers a quiz (and optionally must have joined a channel), and only then is approved. Admins can also approve or report-and-ban an applicant with one tap. Includes light moderation commands and a Gentoo package search.

## Features

- **Join verification.** A join request is **not** auto-approved. The bot posts an in-group message that @-mentions the applicant with a `✅ 点此完成验证` deep-link button (+ a text link). The applicant opens the bot via the link, answers a randomized multiple-choice quiz in DM, and is then `approveChatJoinRequest`-ed. Wrong answer / timeout → `declineChatJoinRequest`. Spam bots that never click or never answer never get in.
- **Required channel (optional).** Require the applicant to have joined a channel before approval. The follow step happens **in DM** (two-step): if not yet joined, the bot first shows a 📢 join-channel button + a "✅ 我已关注,继续" button that re-checks membership, and only then sends the quiz. The in-group message deliberately has **no** channel button (only the verify deep-link + admin buttons) so users aren't sent away from the verify flow. For a **private** channel (no `@handle`), set `channel_invite_url`.
- **Admin override buttons** on every request: **👮 直接通过** (approve now) and **🚫 举报并封禁** (decline + permanent ban).
- **Multiple groups.** Guard several groups with one bot instance.
- **Moderation commands** (reply to a message, admins only): `/sb` = delete + kick (rejoinable), `/ban` = delete + permanent ban.
- **Control / info:** `/start` `/stop` (toggle verification), `/rich` (toggle rich output), `/ping`, `/stats` (today's approved/declined), `/help`.
- **Package search:** `/pkg <name>` searches the official tree ([packages.gentoo.org](https://packages.gentoo.org)) plus the configured overlays (default `gentoo-zh` + `guru`, GitHub, cached ~6h), and shows each result's version — the **amd64-stable** version (`稳定`) for official-tree packages, or the newest `~`testing version otherwise.
- **USE flags:** `/use <package>` shows one package's USE flags (with descriptions, each linked to its useflags page) + info. Accepts a bare name, a `cat/pkg` atom, or a pasted `packages.gentoo.org` (or overlay GitHub) URL. Data from the official tree's JSON, or an overlay's ebuild / `metadata.xml`.
- **Bugzilla:** `/bug <id>` looks up a [Gentoo Bugzilla](https://bugs.gentoo.org) bug (title + status), else just links it.
- **News:** `/news [keyword]` lists or searches [Gentoo news items](https://www.gentoo.org/support/news-items/).
- **Auto-feed (optional).** Configure a `feed` (or a `feeds` array for several destinations) and the bot polls Gentoo Bugzilla + news every `interval_seconds` (default 300) and posts each **new** bug / news item to that channel (the bot must be an admin there with post rights). Each feed has its own language (`lang`) and filters, and all feeds share a single fetch per cycle. Deduped + restart-safe, and seeds a baseline on first run so there's no backlog flood.
- **Restart-safe:** in-progress verifications are persisted to disk and resumed after a restart (no orphaned challenges).
- **Rich output (optional, off by default).** `/pkg` and `/use` can render as Bot API 10.1 rich messages (heading, lists, collapsible sections) — toggled per-config (`rich_messages`) or at runtime by the admin `/rich` command, with automatic fall-back to plain HTML. Off by default because older / third-party clients don't render rich messages; verification, `/bug` and `/news` always stay plain HTML.
- The bot's own group messages auto-delete after a TTL to stay tidy; commands appear in Telegram's `/` menu (admin commands only shown to admins).

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
  "group_ids": [-1001234567890, -1009876543210],
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
| `group_ids` | groups to guard (or use singular `group_id`) |
| `required_channel_id` | numeric channel id applicants must join; `0` disables it |
| `channel_display` | channel shown to users, e.g. `@YourChannel` |
| `channel_invite_url` | explicit channel join link; required for a **private** channel (no `@handle`) |
| `timeout_seconds` | time to finish verification (default 240, max 1800) |
| `notify_ttl_seconds` | auto-delete the bot's group messages after N s (`0`→60, negative→never) |
| `admin_log_chat_id` | optional chat that receives a line per moderation / failed-approve event |
| `overlays` | `/pkg` GitHub overlays `[{name,repo,branch}]` (default: gentoo-zh + guru) |
| `news_url` | `/news` source index URL (default: gentoo.org news-items) |
| `stats_timezone` | IANA tz for the daily /stats reset boundary (default: UTC+8) |
| `rich_messages` | render `/pkg` & `/use` as Bot API 10.1 rich messages (default `false`; also toggleable in-chat via `/rich`) |
| `user_agent` | override the outbound HTTP User-Agent (optional; default `gentoo-zh-verify-bot`) |
| `feed` / `feeds` | optional auto-feed — poll Gentoo Bugzilla + news and post new items to a chat. `feed` is one destination; `feeds` is an array of them (each with its own chat, language and filters). See below; omit to disable |
| `questions` | quiz pool; one is picked at random, options shuffled |

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
| `silent_bugs` | post bugs without a notification (default `true`; they're high-volume) |

## Build & run

Requires **Go 1.25+** (required by the telego library).

```sh
CGO_ENABLED=0 go build -o /usr/local/bin/gentoo-zh-verify-bot .
sudo cp deploy/gentoo-zh-verify-bot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now gentoo-zh-verify-bot
journalctl -fu gentoo-zh-verify-bot
```

Uses long polling — no inbound port or reverse proxy needed.

## Notes / limitations

- Daily **stats** are in-memory and reset on restart. In-progress **verifications are persisted** to `$STATE_DIRECTORY/pending.json` and resumed (timers re-armed) after a restart when run under systemd (`StateDirectory=`); if `STATE_DIRECTORY` is unset they are kept in memory only.
- The verification link relies on each group being **public**.
- User-facing strings are **Simplified Chinese** (this bot targets the Gentoo zh community). All *operational* settings are in the config; to localize the wording, edit the string literals in the `.go` sources (mainly `verify.go`, `admin.go`, `commands.go`).

## License

MIT — see [LICENSE](LICENSE).
