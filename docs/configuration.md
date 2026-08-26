# Configuration reference

`config.json` is optional. With no file the bot starts with no preconfigured groups and waits for runtime registration, which is the normal way to add a group. Everything below has a working default, so a first deployment usually needs nothing but `BOT_TOKEN` in the environment.

Values resolve in one order everywhere: a runtime override in `settings.json` wins, then `config.json`, then the built-in default. Editing `config.json` needs a service restart; the settings panel does not.

## Environment

| Variable | Purpose |
| --- | --- |
| `BOT_TOKEN` | Required. The Telegram bot token. |
| `GITHUB_TOKEN` | Optional. Raises the GitHub API allowance used by overlay lookups. |
| `TELEGRAM_API_URL` | Optional. Points at a self-hosted Bot API server. |

## Prefer the settings panel

Most settings are reachable from `/settings`, per group, without a restart. Putting them in `config.json` only sets the starting value:

verification on/off · challenge delivery · verification mode · applicant-name hiding · ban duration · mute duration · warning limit · verification timeout · maximum failures · retry cooldown · whether invited members verify · lookup cleanup and its lifetime · group language · required channel and invite · quiz and fallback question banks · sender-channel blocking and whitelist · trusted groups · known chats · rich output · the alert chat · the bot-wide private-query rate.

## Editions

`gentoo-zh-verify-bot` is built with `-tags gentoo` and gives the Gentoo lookups the short names
`/pkg` `/use` `/bug` `/news` `/bbs` `/arm`. `gentoo-zhbot` is the default build for Linux
communities in general and prefixes those with `g`, leaving `/pkg` free. Both answer `/pkgs`
`/distro` `/armpkgs` `/wiki` `/kernel` `/man` `/cve` `/repology`. No setting selects between
them: the choice is which binary you install.

Everything the build decides:

| | `gentoo-zh-verify-bot` | `gentoo-zhbot` |
| --- | --- | --- |
| Gentoo lookups | `/pkg` `/use` `/bug` `/news` `/bbs` `/arm` | the same six, prefixed with `g` |
| Direct-message identity | names the Gentoo-zh Community | names no community |
| Built-in fallback questions | gentoozh.org, gentoo.org | kernel.org, gnu.org |
| Binary, systemd unit, `/etc` and `/var/lib` directories | `gentoo-zh-verify-bot` | `gentoo-zhbot` |
| Default `user_agent` | `gentoo-zh-verify-bot` | `gentoo-zhbot` |

Nothing else does. Group language, verification, moderation, the panel, and every other
setting behave identically.

## Fields

Only `group_ids` is worth setting by hand in a fresh deployment, and only if you already know the group.

### Groups

| Field | Default | Meaning |
| --- | --- | --- |
| `group_ids` | none | Guarded group IDs. `groups` accepts the same list with per-group settings; `group_id` is the legacy singular form. |
| `control_group_id` | first effective group | The group whose administrators may change bot-wide settings. It must name a group that is configured here or registered at runtime; anything else fails at startup. |
| `known_chat_ids` | none | Chats the bot stays in without verifying them. Not a bypass. |
| `trusted_member_group_ids` | none | Membership in one of these skips verification entirely. |

### Verification

| Field | Default | Meaning |
| --- | --- | --- |
| `verify_mode` | `kernel` | `kernel`, `quiz`, or `mixed`. An empty quiz bank falls back to `kernel`. |
| `delivery_mode` | `both` | `group`, `dm`, or `both`. |
| `timeout_seconds` | `240` | How long an applicant has. A member verified after joining gets ten minutes unless this is set in the panel. |
| `verify_max_fails` | `3` | Failures before an automatic ban. Negative disables it. |
| `verify_retry_seconds` | `180` | Cooldown after a failure, before applying again or rejoining. Negative disables it. |
| `verify_invited` | `true` | Whether a member somebody else added still has to verify. |
| `ban_seconds` | `0` (permanent) | Automatic-ban duration. |
| `questions` | none | Quiz bank. There is no built-in one: with an empty bank `verify_mode: quiz` falls back to `kernel`. See [`examples/quiz-bank.json`](../examples/quiz-bank.json). |
| `fallback_questions` | built-in (differs by edition) | Short-answer bank for applicants with no Linux machine. Left empty, the built-in bank is used: the Gentoo edition asks about gentoozh.org and gentoo.org, the general edition about kernel.org and gnu.org. See [`examples/fallback-questions.json`](../examples/fallback-questions.json). |

### Required channel

| Field | Default | Meaning |
| --- | --- | --- |
| `required_channel_id` | `0` (disabled) | Applicants must be in this channel. |
| `channel_display` | none | The channel's `@handle`. Required once `required_channel_id` is set, unless `channel_invite_url` is given instead; without either, startup fails. |
| `channel_invite_url` | none | Needed only for a private channel with no public handle. |
| `required_channel_fail_open` | `true` | Whether an unreadable membership check still admits people. |

### Moderation

| Field | Default | Meaning |
| --- | --- | --- |
| `warn_limit` | `3` | Warnings before an automatic kick. |
| `mute_seconds` | `3600` | Default `/mute` duration. Always finite. |
| `block_channel_senders` | `false` | Delete and ban posts sent under a channel identity. |
| `channel_whitelist` | none | Sender chats exempt from that. |
| `admin_log_chat_id` | `0` | Where operator alerts go. Zero posts them in the affected group. |

### Messages and lookups

| Field | Default | Meaning |
| --- | --- | --- |
| `lang` | `zh` | `zh`, `zh-Hant`, or `en`. |
| `notify_ttl_seconds` | `60` | Lifetime of transient group notices. |
| `lookup_ttl_seconds` | `180` | Lifetime of lookup results in a group. Direct messages are never deleted on a timer. |
| `private_query_per_min` | `3` | Per-user direct-message query rate. |
| `rich_messages` | `false` | Rich Bot API output with an HTML fallback. |
| `private_reply` | built-in | Reply to non-command direct messages. |
| `overlays` | `gentoo-zh/overlay`, `gentoo/guru` | Overlays searched by `/pkg` (`/gpkg` in the general edition). |
| `news_url` | gentoo.org news items | Source for `/news` (`/gnews` in the general edition). |
| `user_agent` | the build's name | Outbound HTTP User-Agent. Defaults to `gentoo-zh-verify-bot` or `gentoo-zhbot`, matching the binary. |
| `stats_timezone` | `Asia/Shanghai` | Day boundary for `/stats`. |

### Ownership and feeds

| Field | Default | Meaning |
| --- | --- | --- |
| `owner_claim_lifetime_seconds` | `600` | Lifetime of the one-use owner claim written to the journal at first start. |
| `owner_claim_user_id` | none | Restricts that claim to one Telegram user. Worth setting when others can read the journal. |
| `feeds` | none | Bugzilla and news destinations. See [`examples/feeds.json`](../examples/feeds.json) and [Feed](feed.md). |
