# gentoo-zh-verify-bot

English | [简体中文](README.zh-CN.md)

`gentoo-zh-verify-bot` is a Telegram join-request verification bot for Gentoo Chinese community groups that need to screen bulk spam applicants.

A four-option button quiz gives a spam account a one-in-four chance when it taps at random. The default challenge therefore asks the applicant to **type the version of the Linux kernel they are running**. A hidden, applicant-specific tripwire also catches delegated LLM agents that follow its instruction instead of leaving the verification to a human.

The bot is one static Go binary, uses long polling, opens no inbound port, and has one direct dependency: [telego](https://github.com/mymmrac/telego), the Telegram Bot API binding.

## Verification

1. Telegram sends a join request. The bot leaves it pending and, by default, tries to send that group's challenge in DM immediately. Telegram allows this only if the applicant has started the bot before. A confirmed DM is the only challenge message.
2. If Telegram reports that the bot cannot initiate the conversation, the bot instead posts a group challenge with a group-scoped `verify_<groupID>` deep link. The applicant taps it to open that exact request in DM. Administrators can disable DM-first delivery per group in `/settings`.
3. A trusted-group membership may bypass the challenge; an optional required-channel gate may be applied before approval. The applicant completes the configured challenge, then the bot approves the request. A wrong answer or timeout declines it; admins can instead use `👮 直接通过` or `🚫 拒绝并封禁` on the group fallback.

| Mode | Applicant action | Notes |
| --- | --- | --- |
| `kernel` (default) | Run `uname -r` and type the result | Three attempts. An applicant without Linux can provide the current minute and receive an answer-hidden short question from `fallback_questions`. |
| `quiz` | Tap the correct shuffled option from `questions` | Retained for groups whose applicants are not expected to have Linux. |
| `mixed` | Complete one of the two modes, selected at random | Selected independently for each pending request. |

Applicant-facing verification text follows Telegram `language_code`: Simplified Chinese, Traditional Chinese, or English. Group and administrator output uses the effective group `lang`: the global value is the baseline, a `groups[].lang` value overrides it, and the settings panel can save a per-group runtime override. Configured `questions` are used verbatim rather than translated.

Failed verification starts the `verify_retry_seconds` cooldown. Reaching `verify_max_fails` within the strike window triggers an automatic ban; success clears the applicant's strikes. Pending state is bounded at 2,000 requests process-wide and 500 per group. At either cap, new requests remain for manual review and the admin alert is limited to once per 10 minutes.

## What this repository implements

The Bot API transport and types come from telego. The challenge, recovery, persistence, moderation, and Gentoo-specific interpretation below are repository code built on the standard library; external data still comes from the named upstream services. The linked files are the starting points for audit or patches.

- **Kernel challenge** — [`internal/verify/kernel.go`](internal/verify/kernel.go) parses a bare release, known English lead-ins, Chinese lead-ins, WSL output, and pasted `uname -a` or `/proc/version`-style output. Its narrow ASCII context whitelist rejects unrelated dotted identifiers such as `model=GPT-5.2`. Historical bounds accept 1.0–1.3 and 2.0–2.6; the forward range accepts 3.x through 30.x, so it is not pinned to today's kernel major.
- **AI tripwire** — [`internal/verify/kernel.go`](internal/verify/kernel.go) derives and grades the nonce-bound `AGENT-… model=…` reply, while [`internal/verify/state.go`](internal/verify/state.go) persists the tally in `agents.json` and renders it for `/stats`. There is no fixed token for an operator to hard-code. An exact match is declined. The self-declared model is a usage tally, not evidence, and the tripwire is deterrence rather than a security boundary.
- **Outage recovery** — [`internal/verify/state.go`](internal/verify/state.go) owns the Telegram heartbeat, expiry probes, fresh deadlines, and bounded re-notification. An unreachable expiry receives a new full window without a decline or strike. After a sustained outage, every live request gets a fresh deadline; recovery also re-sends the DM notice and group challenge for up to 30 applicants, with duplicate notices suppressed during flapping.
- **State writes** — [`internal/store/json.go`](internal/store/json.go) serializes snapshot creation and commit, writes a same-directory temporary file, calls `fsync`, renames atomically, then syncs the directory; subsystem loaders such as [`internal/verify/state.go`](internal/verify/state.go) disable writes after an unsafe read failure. Malformed JSON is moved to `<name>.corrupt` before fresh state is created. For `pending.json`, `warns.json`, `antispam.json`, `verifyfail.json`, `settings.json`, and `heartbeat.json`, an unreadable file disables writes to that path instead of overwriting recoverable data.
- **Gentoo semantics** — [`internal/lookup/packages.go`](internal/lookup/packages.go) owns `/pkg`, `/use`, and `/arm`: numeric revisions and Gentoo suffixes sort correctly (`-r10` above `-r2`; `_alpha < _beta < _pre < _rc < release < _p`), USE output separates local/global flags and USE_EXPAND groups, overlay metadata includes `IUSE` and `metadata.xml`, and arm64 results distinguish stable, testing, absent, and unavailable. [`internal/lookup/distros.go`](internal/lookup/distros.go) owns `/pkgs`, `/distro`, and `/armpkgs`; it keeps RHEL rebuilds, CentOS Stream, and EPEL separate and resolves Debian and Ubuntu release roles from live distro metadata rather than hard-coded release numbers.

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

`/start` and `/stop` change whether verification is enabled for the invoking group; `/vmode` changes that group's challenge mode; `/spoiler` changes that group's applicant-name hiding; `/autodel` changes that group's lookup cleanup policy; `/bantime` changes that group's ban duration; and `/bc` changes that group's sender-channel filter and whitelist. Each command commits a sparse per-group override. `/rich` alone changes bot-wide rich output and is the only command gated by `control_group_id`. `/ping`, `/stats`, and `/help` only report runtime state; `/stats` includes the lifetime AI-tripwire tally alongside daily approvals and declines.

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

### Build from source

Source builds require Go 1.26.7 or later. Build the binary and copy the unit into the current directory:

```sh
CGO_ENABLED=0 go build -trimpath -o gentoo-zh-verify-bot ./cmd/gentoo-zh-verify-bot
cp deploy/gentoo-zh-verify-bot.service .
```

### Install a prebuilt release

Release assets are named exactly `gentoo-zh-verify-bot-linux-amd64`, `gentoo-zh-verify-bot-linux-arm64`, and `SHA256SUMS`. Set `version` to the release tag and `arch` to the machine architecture, then download and verify the binary. The unit is not a release asset, so fetch it from the same tag:

```sh
version=v3.12.0
arch=amd64 # use arm64 on 64-bit Arm
release_url="https://github.com/Zakkaus/gentoo-zh-verify-bot/releases/download/${version}"
curl --fail --location --remote-name "${release_url}/gentoo-zh-verify-bot-linux-${arch}"
curl --fail --location --remote-name "${release_url}/SHA256SUMS"
sha256sum --ignore-missing --strict --check SHA256SUMS
mv "gentoo-zh-verify-bot-linux-${arch}" gentoo-zh-verify-bot
curl --fail --location \
  "https://raw.githubusercontent.com/Zakkaus/gentoo-zh-verify-bot/${version}/deploy/gentoo-zh-verify-bot.service" \
  --output gentoo-zh-verify-bot.service
```

### Install and start the service

Install the binary and unit. Create the token file empty with mode `0600`, then enter `BOT_TOKEN=<your-token>` through an editor; the token never appears in a shell argument or shell history.

```sh
sudo install -Dm755 gentoo-zh-verify-bot /usr/local/bin/gentoo-zh-verify-bot
sudo install -Dm600 /dev/null /etc/gentoo-zh-verify-bot/bot.env
sudoedit /etc/gentoo-zh-verify-bot/bot.env
sudo install -Dm644 gentoo-zh-verify-bot.service /etc/systemd/system/gentoo-zh-verify-bot.service
sudo systemctl daemon-reload
sudo systemctl enable --now gentoo-zh-verify-bot
```

Read the private, one-use owner-claim link from the journal:

```sh
sudo journalctl -u gentoo-zh-verify-bot
```

Open that link in Telegram. The unused link expires after 10 minutes by default. Until the claim succeeds, anyone who can read the journal can use the link to become owner. Set `owner_claim_user_id` to restrict the claim to one account.

Add the bot to a group, promote it to administrator, and enable **Invite users**, **Ban users**, and **Delete messages**. The owner-authorized group is written to `settings.json`; the bot never writes `config.json`.

For delegated setup, the owner sends `/enroll` to the bot in a private chat and gives the resulting one-use, ten-minute group link to the group administrator. Reusing an enrollment link, using an expired link, or promoting the bot without owner authorization makes the bot leave the unknown group.

To remove a runtime-registered group, the owner sends `/unregister <group-id>` in a private chat.
The bot removes the group and its runtime overrides, then attempts to leave the group. Removing the
bot from a group does not erase registration state; use `/unregister` for deliberate removal.

For a required channel, make the bot a channel administrator. Plain channel membership cannot read other users' membership, so it cannot enforce the membership gate. Startup emits one combined, actionable setup report for each registered group.

## Configuration

`BOT_TOKEN` is required and has no default. Optional `GITHUB_TOKEN` needs no scopes and raises the GitHub API allowance for overlay requests from 60/h to about 5,000/h. Optional `TELEGRAM_API_URL` selects a self-hosted Bot API server instead of Telegram's hosted API.

`config.json` is optional. When present, it supplies a validated baseline; malformed JSON and invalid configured values still stop startup. Sparse values saved through Telegram take precedence over the file, and file values take precedence over built-in defaults. Changing the file requires a restart. Positive `*_seconds` values above 9,223,372,036 are rejected before conversion; lower operational maxima are listed below.

### Groups and verification
| Key | Purpose | Default and normalization |
| --- | --- | --- |
| `groups` | Optional startup seed for guarded groups and per-group overrides: `id`, `required_channel_id`, `channel_display`, `channel_invite_url`, `trusted_member_group_ids`, `questions`, `verify_mode`, `lang`. | `[]`; groups may instead be registered at runtime. Configured IDs must be nonzero and unique. Empty fields inherit globals; explicit channel ID `0` or trusted list `[]` disables that gate for the group. |
| `group_ids` / `group_id` | Legacy group-list and singular inputs merged into `groups`. | `[]` / `0`; duplicate legacy IDs merge with an existing group. |
| `control_group_id` | Guarded group allowed to run `/rich`, the command-path bot-wide setting. | `0`: administrators of every guarded group may run it. A nonzero startup value outside configured `groups` is invalid. |
| `lang` | Baseline language for group and administrator output; `groups[].lang` and the settings panel may override it per group. | Empty selects `zh`. Accepted values are `zh`, `zh-Hant`, and `en`; any other value stops startup. |
| `required_channel_id` | Global required-channel gate. | `0`: off. |
| `channel_display` | Global channel label or public `@handle`. | Empty. |
| `channel_invite_url` | Global join link, required for a private channel without an `@handle`. | Empty. |
| `trusted_member_group_ids` | Groups whose confirmed members bypass verification. An unreadable membership falls back to normal verification. | `[]`: no bypass. |
| `known_chat_ids` | Other chats the bot may remain in; they gain no guarded, channel, or trust semantics. | `[]`. |
| `owner_claim_lifetime_seconds` | Lifetime of the private, one-use first-owner claim logged at startup. | `0` becomes 600; negative is invalid; maximum 86,400. |
| `owner_claim_user_id` | Optional Telegram user allowed to use the first-owner claim. | `0`: any journal reader with the link; negative is invalid. |
| `verify_mode` | Global `kernel`, `quiz`, or `mixed` mode; per-group values and `/vmode ...|auto` may override it. | Empty becomes `kernel`; any other value is a load error. |
| `timeout_seconds` | Verification window. | `<=0` becomes 240; 1–29 becomes 30; maximum 1,800. |
| `required_channel_fail_open` | Result when required-channel membership cannot be read after the challenge passes. Admins are alerted in either mode. | `true`: approve; `false`: decline for retry. |
| `verify_retry_seconds` | Cooldown after a failed verification. | `0` becomes 180; negative disables; maximum 31,622,400 (366 days). |
| `verify_max_fails` | Failures before automatic ban. | `0` becomes 3; negative disables; positive unchanged. |
| `fallback_questions` | Short-answer pool for applicants without Linux: `[{q,answers:[…]}]`. | `[]` selects the built-in localized pool. Each item needs a nonempty `q` and at least one nonempty whole-answer value. |
| `questions` | Global quiz pool: `[{q,options:[…],answer}]`. | `[]` is valid only when every configured group is kernel-only. At least two options; `answer` defaults to index 0 and must be in range; `q` is used verbatim. |

### Moderation, messages, and runtime defaults
| Key | Purpose | Default and normalization |
| --- | --- | --- |
| `notify_ttl_seconds` | Delete bot group messages after this many seconds. | `0` becomes 60; negative keeps messages; maximum 86,400. |
| `lookup_ttl_seconds` | Delete lookup commands and replies together. `/autodel` saves a runtime override in `settings.json`. | Unset becomes 180; `0` or negative disables; maximum 86,400. |
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
| `lang` | Language for bug field labels and news posts. | Empty selects `zh`. Accepted values are `zh`, `zh-Hant`, and `en`; any other value stops startup. |
| `interval_seconds` | Poll interval. | `<=0` becomes 300; 1–59 becomes 60; maximum 86,400. |
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

### Installation details
Requires **Go 1.26.7+** for source builds, matching `go.mod`. [Releases](https://github.com/Zakkaus/gentoo-zh-verify-bot/releases) contain only the two static Linux binaries and `SHA256SUMS`; the tagged unit must be fetched separately as shown in [Install a prebuilt release](#install-a-prebuilt-release). `go install …@v3.x` is not supported because the module path intentionally has no `/vN` suffix.

The supplied unit reads `/etc/gentoo-zh-verify-bot/bot.env`, optionally reads `config.json`, runs with `DynamicUser=`, and creates `/var/lib/gentoo-zh-verify-bot` with mode `0700` through `StateDirectory=`. Long polling needs outbound HTTPS only; no listener or reverse proxy is required.


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
| A per-group startup report says the setup is not ready | One or more required group rights are missing, or required-channel membership is unreadable. | Apply every action in that group's single report, then restart the service to rerun the check. |
| Feed target is unreachable or lacks post rights | Startup warns; transient send failures do not advance past an undelivered item. | Correct `chat_id`, add the bot, and grant channel post rights. |

## Forking for another community

Configuration can change Telegram group registration, verification and fallback question banks, the global or per-group language among the three existing locales, overlays, the Gentoo news index, feed destinations, and message/runtime policies. Prefer `groups` or runtime registration, `questions`, `fallback_questions`, `lang`, `overlays`, and `news_url` for those changes; they require no fork-specific code.

A complete community fork still requires a deliberate code and documentation cutover:

- Change the module path in `go.mod` and every Go import. Rename the command directory, binary and release assets, default `/etc` config path, `/var/lib` state directory, environment file, and systemd unit in `cmd/gentoo-zh-verify-bot`, `deploy/`, and `.github/workflows/release.yml`.
- Update the supported-locale registry and selection branches in `internal/i18n/catalog.go`, `internal/config/config.go`, `internal/bot/commands.go`, `internal/panel/codec.go`, and `internal/panel/settings_panel.go`. Change the Simplified-Chinese default separately if `zh` should not remain the fallback.
- Replace the built-in fallback question bank in `internal/i18n/locales/*/verification.json` if `fallback_questions` will not always be supplied.
- Replace the default gentoo-zh and GURU overlays in `internal/lookup/packages.go` if an `overlays` configuration will not always be supplied.
- Audit the Gentoo Bugzilla and feed endpoints in `internal/lookup/content.go` and `internal/feed/feed.go`; the Gentoo news, wiki, and package endpoints and release metadata in `internal/lookup/content.go`, `internal/lookup/packages.go`, and `internal/lookup/distros.go`; and the lookup command set in `internal/bot/bot.go`, `internal/bot/commands.go`, every locale catalogue, and the public command tables.
- Replace upstream repository, raw-file, release, security-reporting, and changelog URLs throughout the documentation and release workflow.

## License
MIT — see [LICENSE](LICENSE).
