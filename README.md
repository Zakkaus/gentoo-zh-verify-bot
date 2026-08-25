# gentoo-zh-verify-bot

English | [简体中文](README.zh-CN.md)

`gentoo-zh-verify-bot` is a Telegram join-request verification bot for Gentoo Chinese community groups that need to screen bulk spam applicants.

A four-option button quiz gives a spam account a one-in-four chance when it taps at random. The default challenge therefore asks the applicant to **type the version of the Linux kernel they are running**. A hidden, applicant-specific tripwire also catches delegated LLM agents that follow its instruction instead of leaving the verification to a human.

The bot is one static Go binary, uses long polling, opens no inbound port, and has one direct dependency: [telego](https://github.com/mymmrac/telego), the Telegram Bot API binding.

## Verification

1. Telegram sends a join request. The bot leaves it pending and posts a `✅ 完成验证` deep link in the public group.
2. The applicant opens the bot in DM. A trusted-group membership may bypass the challenge; an optional required-channel gate may be applied before approval. If the same applicant is pending in groups with different required channels, the DM uses the first pending group's channel.
3. The applicant completes the configured challenge. Only then does the bot approve the request. A wrong answer or timeout declines it; admins can instead use `👮 直接通过` or `🚫 拒绝并封禁` on the group message.

| Mode | Applicant action | Notes |
| --- | --- | --- |
| `kernel` (default) | Run `uname -r` and type the result | Three attempts. An applicant without Linux can provide the current minute and receive an answer-hidden short question from `fallback_questions`. |
| `quiz` | Tap the correct shuffled option from `questions` | Retained for groups whose applicants are not expected to have Linux. |
| `mixed` | Complete one of the two modes, selected at random | Selected independently for each pending request. |

Applicant-facing verification text follows Telegram `language_code`: Simplified Chinese, Traditional Chinese, or English. Admin output remains Simplified Chinese, and configured `questions` are used verbatim rather than translated.

Failed verification starts the `verify_retry_seconds` cooldown. Reaching `verify_max_fails` within the strike window triggers an automatic ban; success clears the applicant's strikes. Pending state is bounded at 2,000 requests process-wide and 500 per group. At either cap, new requests remain for manual review and the admin alert is limited to once per 10 minutes.

## What this repository implements

The Bot API transport and types come from telego. The challenge, recovery, persistence, moderation, and Gentoo-specific interpretation below are repository code built on the standard library; external data still comes from the named upstream services. The linked files are the starting points for audit or patches.

- **Kernel challenge** — [`kernel.go`](kernel.go) parses a bare release, known English lead-ins, Chinese lead-ins, WSL output, and pasted `uname -a` or `/proc/version`-style output. Its narrow ASCII context whitelist rejects unrelated dotted identifiers such as `model=GPT-5.2`. Historical bounds accept 1.0–1.3 and 2.0–2.6; the forward range accepts 3.x through 30.x, so it is not pinned to today's kernel major.
- **AI tripwire** — [`kernel.go`](kernel.go), [`verify.go`](verify.go), and [`agents.go`](agents.go) derive the required `AGENT-… model=…` reply token from the pending request's nonce. There is no fixed token for an operator to hard-code. An exact match is declined; its self-declared model is persisted in `agents.json` and included in `/stats`. The declaration is a usage tally, not evidence, and the tripwire is deterrence rather than a security boundary.
- **Outage recovery** — [`verify.go`](verify.go) runs a Telegram heartbeat and probes again when a timer expires. An unreachable expiry receives a new full window without a decline or strike. After a sustained outage, every live request gets a fresh deadline; recovery also re-sends the DM notice and group challenge for up to 30 applicants, with duplicate notices suppressed during flapping.
- **State writes** — [`verify.go`](verify.go) serializes snapshot creation and commit, writes a same-directory temporary file, calls `fsync`, renames atomically, then syncs the directory. Malformed JSON is moved to `<name>.corrupt` before fresh state is created. For `pending.json`, `warns.json`, `antispam.json`, `verifyfail.json`, `settings.json`, and `heartbeat.json`, an unreadable file disables writes to that path instead of overwriting recoverable data.
- **Gentoo semantics** — [`pkg.go`](pkg.go) compares numeric revisions and Gentoo suffixes (`-r10` sorts above `-r2`; `_alpha < _beta < _pre < _rc < release < _p`). [`use.go`](use.go) separates local/global USE flags and USE_EXPAND groups, and parses overlay `IUSE` plus `metadata.xml`. [`arm.go`](arm.go) distinguishes stable `arm64`, testing `~arm64`, no keyword, and an unavailable source. [`pkgs.go`](pkgs.go) keeps RHEL rebuilds, CentOS Stream, and EPEL separate, while [`releaseinfo.go`](releaseinfo.go) resolves Debian and Ubuntu release roles from live distro metadata rather than hard-coded release numbers.
## Moderation
Run these as a non-anonymous group administrator, replying to the target message.

| Command | Action |
| --- | --- |
| `/mute [duration]` · `/unmute` | Mute for a finite duration (default 1 h, for example `/mute 30m`) or lift the mute early. |
| `/ban` | Remove and ban; duration follows `/bantime`. Deletes only the replied-to message. |
| `/sb` | Ban, then purge all messages from the user. |
| `/warn` · `/clearwarn` | Add or clear a warning; `warn_limit` warnings auto-kick. |
| `/bantime` | Set `0` for permanent or a duration such as `7d`, `12h`, or `30m`; 1–29 s becomes 30 s and more than 366 days becomes permanent. |
| `/bc` | Block channel-identity posts and manage the whitelist. Requires BotFather privacy mode off; state persists. |

`/start`, `/stop`, `/vmode`, `/rich`, `/spoiler`, `/autodel`, `/bantime`, and `/bc` change process-global state. Set `control_group_id` to restrict them to one guarded group. `/ping`, `/stats`, and `/help` report runtime state; `/stats` includes the lifetime AI-tripwire tally alongside daily approvals and declines.

## Gentoo and Linux lookups
Lookup commands also work in DM, limited by `private_query_per_min`.

| Command | Result |
| --- | --- |
| `/pkg <name>` | Gentoo package and version from the official tree plus configured overlays. |
| `/use <pkg>` | USE, USE_EXPAND, package information, and versions. |
| `/bug <id>` | Gentoo Bugzilla issue. |
| `/news [keyword]` | Gentoo news items. |
| `/wiki <keyword>` | Gentoo and Arch wiki results, preferring Simplified Chinese pages. |
| `/bbs <keyword>` | Arch Linux CN results and English forum search links. |
| `/pkgs <pkg>` · `/distro <pkg>` | Cross-distribution versions from [Repology](https://repology.org), labelled by release role. |
| `/arm <pkg>` | Gentoo `arm64` keyword status. |
| `/armpkgs <pkg>` | Cross-distribution arm64 support for Gentoo, Debian, Ubuntu, Fedora, Arch ARM, and AUR. |

The optional feed polls Gentoo Bugzilla and news for each `feed` or `feeds` destination. Cursors survive restarts. Bug status changes edit the original post; confirmation can emit one `🔔` notice, and resolution changes `🐞` to `✅` or `❌`.

## Deployment

`BOT_TOKEN` is the only required setting. The supplied systemd unit creates a private state directory; `config.json` is optional.

Build the command package, or place a release binary named `gentoo-zh-verify-bot` in the current directory:

```sh
CGO_ENABLED=0 go build -o gentoo-zh-verify-bot ./cmd/gentoo-zh-verify-bot
```

Install the binary, write the token environment file, and start the service:

```sh
sudo install -Dm755 gentoo-zh-verify-bot /usr/local/bin/gentoo-zh-verify-bot
printf '%s\n' 'BOT_TOKEN=123456:ABC-DEF' | sudo install -Dm600 /dev/stdin /etc/gentoo-zh-verify-bot/bot.env
sudo install -Dm644 deploy/gentoo-zh-verify-bot.service /etc/systemd/system/gentoo-zh-verify-bot.service && sudo systemctl daemon-reload && sudo systemctl enable --now gentoo-zh-verify-bot
```

Read the private, one-use owner-claim link from the journal:

```sh
sudo journalctl -u gentoo-zh-verify-bot
```

Open that link in Telegram. The first account to open the unexpired link becomes the owner. Add the bot to a group, promote it to administrator, and enable **Invite users**, **Ban users**, and **Delete messages**. The owner-authorized group is written to `settings.json`; the bot never writes `config.json`.

For delegated setup, the owner sends `/enroll` to the bot in a private chat and gives the resulting one-use, ten-minute group link to the group administrator. Reusing an enrollment link, using an expired link, or promoting the bot without owner authorization makes the bot leave the unknown group.

For a required channel, make the bot a channel administrator. Plain channel membership cannot read other users' membership, so it cannot enforce the membership gate. Startup emits one combined, actionable setup report for each registered group.

## Configuration

`BOT_TOKEN` is required and has no default. Optional `GITHUB_TOKEN` needs no scopes and raises the GitHub API allowance for overlay requests from 60/h to about 5,000/h. Optional `TELEGRAM_API_URL` selects a self-hosted Bot API server instead of Telegram's hosted API.

`config.json` is optional. When present, it supplies a validated baseline; malformed JSON and invalid configured values still stop startup. Sparse values saved through Telegram take precedence over the file, and file values take precedence over built-in defaults. Changing the file requires a restart.

### Groups and verification
| Key | Purpose | Default and normalization |
| --- | --- | --- |
| `groups` | Optional startup seed for guarded groups and per-group overrides: `id`, `required_channel_id`, `channel_display`, `channel_invite_url`, `trusted_member_group_ids`, `questions`, `verify_mode`. | `[]`; groups may instead be registered at runtime. Configured IDs must be nonzero and unique. Empty fields inherit globals; explicit channel ID `0` or trusted list `[]` disables that gate for the group. |
| `group_ids` / `group_id` | Legacy group-list and singular inputs merged into `groups`. | `[]` / `0`; duplicate legacy IDs merge with an existing group. |
| `control_group_id` | Group allowed to run process-global commands. | `0`: the first effective group is used. A nonzero startup value outside configured `groups` is invalid. |
| `required_channel_id` | Global required-channel gate. | `0`: off. |
| `channel_display` | Global channel label or public `@handle`. | Empty. |
| `channel_invite_url` | Global join link, required for a private channel without an `@handle`. | Empty. |
| `trusted_member_group_ids` | Groups whose confirmed members bypass verification. An unreadable membership falls back to normal verification. | `[]`: no bypass. |
| `known_chat_ids` | Other chats the bot may remain in; they gain no guarded, channel, or trust semantics. | `[]`. |
| `verify_mode` | Global `kernel`, `quiz`, or `mixed` mode; per-group values and `/vmode ...|auto` may override it. | Empty becomes `kernel`; any other value is a load error. |
| `timeout_seconds` | Verification window. | `<=0` becomes 240; 1–29 becomes 30; maximum 1,800. |
| `required_channel_fail_open` | Result when required-channel membership cannot be read after the challenge passes. Admins are alerted in either mode. | `true`: approve; `false`: decline for retry. |
| `verify_retry_seconds` | Cooldown after a failed verification. | `0` becomes 180; negative disables; positive unchanged. |
| `verify_max_fails` | Failures before automatic ban. | `0` becomes 3; negative disables; positive unchanged. |
| `fallback_questions` | Short-answer pool for applicants without Linux: `[{q,answers:[…]}]`. | `[]` selects the built-in localized pool. Each item needs a nonempty `q` and at least one nonempty whole-answer value. |
| `questions` | Global quiz pool: `[{q,options:[…],answer}]`. | `[]` is valid only when every configured group is kernel-only. At least two options; `answer` defaults to index 0 and must be in range; `q` is used verbatim. |

### Moderation, messages, and runtime defaults
| Key | Purpose | Default and normalization |
| --- | --- | --- |
| `notify_ttl_seconds` | Delete bot group messages after this many seconds. | `0` becomes 60; negative keeps messages; positive unchanged. |
| `lookup_ttl_seconds` | Delete lookup commands and replies together. `/autodel` saves a runtime override in `settings.json`. | Unset becomes 180; `0` or negative disables; positive unchanged. |
| `warn_limit` | `/warn` count before auto-kick. | `<=0` becomes 3; no maximum. |
| `private_query_per_min` | Per-user DM lookup limit; guarded groups are unlimited. | `<=0` becomes 3; no maximum. |
| `ban_seconds` | Default duration for `/ban`, `/sb`, and verification auto-ban. `/bantime` saves a runtime override in `settings.json`. | `<=0`: permanent; 1–29 becomes 30; more than 366 days becomes permanent. |
| `mute_seconds` | Default `/mute` duration; mute is always timed. | `<=0` becomes 3,600; 1–29 becomes 30; more than 366 days becomes 366 days. |
| `admin_log_chat_id` | Dedicated moderation and failed-action log. | `0`: off; no normalization. |
| `stats_timezone` | IANA timezone for daily `/stats` reset. | Empty or invalid becomes fixed UTC+8. |
| `rich_messages` | Baseline rich output for `/pkg` and `/use`; `/rich` saves a runtime override in `settings.json`. | `false`. |
| `private_reply` | Reply to ordinary non-command DMs outside verification. | Empty selects built-in help text. |
| `block_channel_senders` | Initial `/bc` filter state; requires privacy mode off. | `false`; persisted `antispam.json` takes precedence. |
| `channel_whitelist` | Initial channel-sender whitelist. | `[]`; persisted `antispam.json` takes precedence. |

### Lookup and feed sources
| Key | Purpose | Default and normalization |
| --- | --- | --- |
| `overlays` | GitHub overlays for `/pkg`: `[{name,repo,branch}]`. | `[]` selects gentoo-zh and guru. Empty `name` becomes `repo`; empty `branch` becomes `master`; `repo` must be `owner/name`; effective names must be unique. |
| `news_url` | Gentoo news index. | Empty becomes `https://www.gentoo.org/support/news-items/`. |
| `user_agent` | Outbound HTTP User-Agent. | Empty becomes `gentoo-zh-verify-bot`. |
| `feed` / `feeds` | One destination or an array of destinations. | Absent or `[]`: off. Duplicate nonzero `chat_id` entries after the first are ignored. |

| Feed key | Purpose | Default and normalization |
| --- | --- | --- |
| `chat_id` | Destination channel or group; the bot needs permission to post. | `0`: disabled. |
| `lang` | Bug field labels. | `en` selects English; empty or any other value selects Chinese. |
| `interval_seconds` | Poll interval. | `<=0` becomes 300; 1–59 becomes 60; no maximum. |
| `bugs` / `news` | Enable new Bugzilla issues and news items independently. | Unset becomes `true`. |
| `bug_product` / `bug_component` | Optional Bugzilla filters. | Empty matches all. |
| `silent_bugs` | Silence notifications for bug posts. | `true` silences all; unset or `false` silences only UNCONFIRMED bugs and permits the one-time confirmation notice. |

Unknown JSON keys are ignored but logged as `WARNING: config: unknown key ...`. Treat that warning as a spelling error and correct the file.

The Telegram settings surface does not edit feed destinations, the overlay list, the news source, `stats_timezone`, or `user_agent`. These remain `config.json` settings and require a service restart.

## Operations

### Telegram prerequisites
1. Create the bot with [@BotFather](https://t.me/BotFather).
2. Claim ownership from the private startup link before registering a group.
3. In each registered group, promote the bot and enable **Invite users**, **Ban users**, and **Delete messages**. Enable join requests for the group.
4. For a required channel, make the bot an administrator; being a member is insufficient for membership reads.
5. BotFather privacy mode may remain on unless `/bc` must inspect channel-identity posts.

### Build and install
Requires **Go 1.26.7+**, matching `go.mod`. Prebuilt static `linux-amd64` and `arm64` binaries plus `SHA256SUMS` are available from [Releases](https://github.com/Zakkaus/gentoo-zh-verify-bot/releases). `go install …@v3.x` is not supported because the module path intentionally has no `/vN` suffix; use a release binary or clone and build.

```sh
CGO_ENABLED=0 go build -o gentoo-zh-verify-bot ./cmd/gentoo-zh-verify-bot
```

Use the three installation commands in [Deployment](#deployment). The supplied unit reads `/etc/gentoo-zh-verify-bot/bot.env`, optionally reads `config.json`, runs with `DynamicUser=`, and creates `/var/lib/gentoo-zh-verify-bot` with mode `0700` through `StateDirectory=`. Long polling needs outbound HTTPS only; no listener or reverse proxy is required.


### State and restarts
When `$STATE_DIRECTORY` is unset, all state is memory-only and the bot logs a warning. The supplied unit sets it. Keep the directory private to the service user.

| File in `$STATE_DIRECTORY` | Survives restart |
| --- | --- |
| `pending.json` | In-progress verification, attempts, nonce, question, and deadline. Timers are re-armed. |
| `warns.json` | Per-user `/warn` counts. |
| `antispam.json` | `/bc` state and channel whitelist. |
| `verifyfail.json` | Verification strikes and cooldowns. |
| `settings.json` | Owner identity, one-use registration capabilities, runtime groups, control-group selection, and sparse runtime setting overrides. |
| `heartbeat.json` | Last successful Telegram contact for restart recovery. |
| `agents.json` | Lifetime tripwire total and self-declared model counts. |
| `feed-<chat_id>.json` | Feed cursors and tracked bug message IDs. |

Daily `/stats`, rate-limit windows, and lookup/news/package caches reset on restart. Runtime settings stored in `settings.json` do not.

### Failure behavior
| Failure | Bot behavior | Operator action |
| --- | --- | --- |
| Missing `BOT_TOKEN`, invalid values in an existing config, or Telegram unreachable during mandatory startup calls | Startup exits nonzero; systemd retries under `Restart=on-failure`. A missing config file is valid and starts with zero groups. | Read the first fatal journal line, fix the token, file, or network, then restart. |
| Telegram becomes unreachable while running | Long polling retries. Verification expiries are deferred without declines or strikes; recovery grants fresh windows and bounded re-notification. | No applicant cleanup is required. Investigate the network if heartbeat warnings continue. |
| `ERROR state load <path>: ...; writes disabled until restart` | Core state using that path remains in memory and does not persist. The file is left for recovery. | Stop the service immediately, inspect or restore the file, fix ownership and permissions, then restart. Waiting does not re-enable writes. |
| A package or distribution source does not answer | `/pkg`, `/use`, `/arm`, `/pkgs`, and `/armpkgs` distinguish “not found” from unavailable data and label partial results. Feed fetch failures leave cursors unchanged for the next poll. | Check the named source; do not interpret unavailable or partial output as absence. |
| Required-channel membership cannot be read | The bot alerts admins. After a passed challenge, default `required_channel_fail_open: true` approves; `false` declines for retry. | Restore the bot as channel administrator; choose fail-open or fail-closed explicitly if the default is unsuitable. |
| A per-group startup report says the setup is not ready | One or more required group rights are missing, or required-channel membership is unreadable. | Apply every action in that group's single report, then restart or run the panel's recheck action. |
| Feed target is unreachable or lacks post rights | Startup warns; transient send failures do not advance past an undelivered item. | Correct `chat_id`, add the bot, and grant channel post rights. |

## License
MIT — see [LICENSE](LICENSE).
