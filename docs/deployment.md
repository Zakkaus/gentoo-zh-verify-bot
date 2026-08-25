# Deployment

This document follows first startup, durable owner claim, runtime group registration, permission diagnostics, and the supplied systemd unit. Configuration keys are intentionally not duplicated here.

## First start with only `BOT_TOKEN`

**Implementation:** package `main`, `main` and `loadRuntimeState` in `cmd/gentoo-zh-verify-bot/main.go`; package `internal/config`, `LoadConfig` in `internal/config/config.go`; package `internal/store`, `LoadBaseline` and `EffectiveConfig` in `internal/store/baseline.go`.

Outside `-version`, `BOT_TOKEN` is the only required application input. The default config path is `/etc/gentoo-zh-verify-bot/config.json`. A missing file is treated as `{}` and starts with zero configured groups. An unreadable existing file, malformed JSON, invalid group/mode/question/channel values, or invalid baseline stops startup. Unknown keys only log warnings.

When `STATE_DIRECTORY` is nonempty, `loadRuntimeState` tries to create it with mode `0700`, removes orphan `.<name>.tmp-*` files, and places `settings.json` there. Directory-creation failure logs a warning but does not stop startup; subsequent persistence can fail. When the variable is empty, ordinary settings changes are memory-only and all verification/warning/feed state is non-durable. Owner claim and runtime registration are stricter: they refuse to operate without durable settings storage.

Startup then creates the Bot API client, performs mandatory `GetMe`, constructs the handler, and registers owner/enrollment routes before ordinary application routes. It starts the handler consumer and confirms its running state before initial long polling can fetch a backlog. Bot construction, handler construction or startup, initial long-poll setup, or `GetMe` failure is fatal. The process also starts asynchronous permission checks, registers command menus, and starts optional feeds, lookup warming, and heartbeat. An update stream ending without a shutdown signal exits nonzero so systemd can restart it.

## Owner claim

**Implementation:** package `main`, `(*registrationService).EnsureOwnerClaim` and `(*registrationService).onOwnerClaim` in `cmd/gentoo-zh-verify-bot/registration.go`; package `internal/store`, `(*Settings).EnsureOwnerClaim` and `(*Settings).ClaimOwner` in `internal/store/settings.go`.

When no owner is stored, startup durably creates or reuses a one-use claim nonce valid for 24 hours and logs a private `https://t.me/<bot>?start=owner_<nonce>` link. Opening the valid link in the bot’s DM binds that Telegram user ID as owner and consumes the nonce.

An absent, mismatched, reused, or expired nonce is refused. A storage failure receives a save-failure response and leaves ownership unclaimed. If settings persistence is absent, unreadable, unsupported, or unwritable, startup logs that owner claim is unavailable; it does not make an in-memory owner claim.

Treat the journal link as a secret capability until consumed. The code does not add a second identity check beyond possession of the valid nonce.

## Owner-authorized group registration

**Implementation:** package `main`, `(*registrationService).onEnrollmentCommand`, `(*registrationService).onEnrollmentStart`, `(*registrationService).onMyChatMember`, `(*registrationService).scheduleUnknownLeave`, and `(*registrationService).registrationCompleted` in `cmd/gentoo-zh-verify-bot/registration.go`; package `internal/store`, `(*Settings).IssueEnrollmentNonce` and `(*Settings).CommitRegistrations` in `internal/store/settings.go`.

The owner can send `/enroll` in DM. The bot durably issues a single-use `startgroup=enroll_<nonce>` link valid for ten minutes. A non-owner receives an owner-only refusal; a persistence failure receives a save-failure response.

The administrator opening that link in the target group must be a current human Telegram administrator. The bot’s own membership must be readable. If the bot is already creator/administrator, registration commits immediately. If it is an ordinary member, the service durably records a pending registration and waits up to ten minutes for promotion. Promotion completes only the matching unexpired pending record.

The stored owner can also register directly by adding/promoting the bot. If no effective group existed, the first registered group becomes the durable registration control group. Invalid, expired, or replayed enrollment; a bot/non-admin actor; unreadable membership; ineligible bot status; unauthorized promotion; or persistence failure causes a refusal and an attempted leave. Once an owner exists, an unknown member-only group can wait up to ten minutes for a valid enrollment payload, then the bot leaves. Without an owner it is refused immediately. Leave failures are logged and the bot remains until another event/operator action.

Registration writes the group to `settings.json` immediately. Moderation, `/settings`, and the registration-triggered permission report use the settings store and can see it in the same process. Join verification and several slash-command guards use the effective `config.Config` snapshot built only at startup, and command menus are also installed only at startup. Restart after registration before relying on join verification or the full command surface.

After registration, `registrationCompleted` sends `?start=configure_<group_id>`. The settings-panel parser accepts only `panel_<token>` links. No `configure_` handler exists; the generic DM `/start` path attempts applicant challenge delivery instead. The intended behavior of that completion link is therefore unclear in code. Use `/settings` in the registered group to launch the actual panel.

## Permission self-check

**Implementation:** package `internal/moderate`, `(*Service).CheckGroupSetup`, `(*Service).LogGroupSetup`, and `(*Service).LogGroupAdmin` in `internal/moderate/service.go`; package `internal/feed`, `probeFeedPerms` in `internal/feed/feed.go`.

Startup asynchronously checks every effective guarded group. It verifies readable group access and bot administrator/owner status with these rights:

- Invite users, used to approve join requests;
- Ban users/Restrict members, used by bans, mutes, warning kicks, and sender-channel bans;
- Delete messages, used by cleanup and moderation evidence removal;
- administrator/owner status in every configured required channel.

A ready group is logged but not messaged. A missing-rights report is logged and sent to the runtime registrant first, then the admin-log chat, then the group, stopping at the first successful delivery. Lookup or delivery errors are included in/logged around the report. The check is diagnostic and nonfatal; it does not disable handlers. There is no settings-panel action that reruns it. Restart reruns all group checks; completing runtime registration checks that one group immediately.

Feed destinations have a separate nonfatal startup probe. A channel requires administrator status and `can_post_messages`; a group/supergroup must not have the bot left, banned, or unable to send. Probe failure only warns, and the feed loop still runs.

## systemd operations and restart recovery

**Implementation:** package `main`, `main`, `prepareUpdateHandler`, `pollingProgressCaller`, and
`systemdNotifier` in `cmd/gentoo-zh-verify-bot`; deployment definition
`deploy/gentoo-zh-verify-bot.service`.

### Supervision policy

The supplied unit runs `/usr/local/bin/gentoo-zh-verify-bot --config
/etc/gentoo-zh-verify-bot/config.json`, reads `/etc/gentoo-zh-verify-bot/bot.env`, uses
`DynamicUser=yes`, and creates `/var/lib/gentoo-zh-verify-bot` as `STATE_DIRECTORY` with mode
`0700`.

`Restart=always` covers crashes, watchdog termination, and an unexpected clean exit. An explicit
30-second delay prevents a hot crash loop. `StartLimitIntervalSec=0` disables systemd's start-rate
latch, so a persistent startup failure keeps retrying every 30 seconds instead of stopping after
five attempts. An operator-requested `systemctl stop` remains stopped.

The service is `Type=notify`. It sends `READY=1` only after identity lookup, state restoration,
handler registration, and confirmation that the handler consumer is running; long polling starts
after that consumer confirmation and before readiness. A completed `getUpdates` attempt sends
`WATCHDOG=1`; no independent ticker can hide a stalled poll loop. A quiet successful long poll
completes within 30 seconds. Each API call is bounded at 45 seconds and a failed call retries after
five seconds, so the longest expected gap between progress signals is 50 seconds.
`WatchdogSec=120s` is more than twice that gap. Network, DNS, and Telegram failures still complete
an attempt and therefore keep the watchdog alive while the retry and verification-outage paths
recover. A poll loop that stops completing attempts for 120 seconds is terminated by systemd and
restarted after 30 seconds.

SIGINT or SIGTERM sends `STOPPING=1` and stops long polling first. The handler input then drains
every update already fetched into Telego's buffer; those updates may already have been confirmed
upstream by a later poll offset and cannot safely be abandoned. Draining, in-flight handlers, the
Telegram heartbeat, and the feed state flush share one 20-second deadline. Verification timers then
freeze and the verification files are saved synchronously. `TimeoutStopSec=30s` leaves systemd ten
additional seconds before forced termination. Shutdown logs name each component being waited for.

All state commits use one process-wide mutex. A commit writes a mode-`0600` temporary file in the
target directory, `fsync`s and closes it, atomically renames it over the state file, then `fsync`s
the parent directory. Termination before rename leaves the previous file intact; startup removes
orphan `.<name>.tmp-*` files. A storage error can still leave the newest in-memory change absent
from the last successful snapshot.

### Restart state

| State | Restart behavior |
| --- | --- |
| Pending verifications and deadlines | `pending.json` restores the challenge, nonce, attempts, question state, and deadline. Long outages receive the recovery rules described below. |
| Required-channel gate | The configured/runtime gate survives in `settings.json` or `config.json`. Channel membership is checked live; no cached membership decision is trusted after restart. |
| Verification strikes and retry cooldowns | `verifyfail.json` restores the count and last-failure time. |
| Warning counters | `warns.json` restores positive per-group, per-user counters. |
| Mute expiry | Telegram stores `until_date`; the server lifts the restriction even while the bot is stopped. The bot has no local mute timer to reconstruct. |
| Feed cursors and tracked messages | Each `feed-<chat_id>.json` restores the bug/news cursor and tracked bug-message state. Only the last successful atomic save is available. |
| Owner claim, registered groups, enrollment nonces, and pending promotions | `settings.json` restores them and reconstructs the leave deadline for a persisted pending promotion. |
| Settings-panel sessions and drafts | Discarded. They are uncommitted, capped at 256, and expire after 30 minutes; reopen `/settings`. |
| Daily approval/decline counters, positive admin cache entries, DM/lookup rate windows, lookup caches, cleanup timers, and alert throttles | Discarded. They are bounded operational caches or same-day observability, not promised durable state. |

One registration grace path is not durable: when the bot already has an owner and is added as an
ordinary member without an enrollment payload, the ten-minute leave deadline exists only in
`registrationService.waiting`. A crash or restart before expiry drops that deadline, so the bot may
remain in the unauthorized group. Remove it manually or repeat the enrollment flow. Persisting and
restoring that deadline requires a change in `cmd/gentoo-zh-verify-bot/registration.go`.

A delivered kernel prompt has another crash window: `markPrompted` changes the in-memory
`prompted` flag without immediately saving `pending.json`. A crash before the next state event or
graceful shutdown restores the request as unprompted, so the applicant's reply is not graded.
Closing that window requires a change in `internal/verify/service.go`.

Manual launches with no `STATE_DIRECTORY` keep all file-backed state in memory. Durable owner claim,
enrollment capability, and runtime group registration fail rather than report a non-durable
success. Detailed file semantics are in [State and persistence](state-persistence.md).

### Telegram outage and backlog recovery

Handlers are fully registered before `UpdatesViaLongPolling` starts, so a startup backlog cannot be
consumed before its route exists. The initial offset is zero. Telego copies that value into its
private polling parameters, advances it to `update_id + 1` only after receiving an update, and
leaves it unchanged when `GetUpdates` fails. A nonzero
`WithLongPollingRetryTimeout(5s)` retries indefinitely instead of closing the update channel.
Restarting the process starts from zero again, which asks Telegram for the oldest update it has not
already confirmed; no application path resets the live offset or advances it across a failed poll.

After a one-hour network loss, polling is still retrying, verification deadlines are paused by the
heartbeat logic, and Telegram's queued updates are delivered when connectivity returns. Pending
verifications receive fresh windows and bounded re-notification. An update-stream closure while the
process context is still live exits nonzero; `Restart=always` then starts a new process.

A token rejected during startup makes the required `GetMe` call fail, so the process exits and
systemd retries every 30 seconds. A token that becomes invalid after startup makes both polling and
heartbeat calls fail. Those completed failures prove that the loops are still progressing, so they
do not trip the watchdog; polling continues to retry until an operator replaces the token and
restarts the service.

Telegram retains disconnected-bot updates for about 24 hours. Older join-request updates can be
lost permanently, and the Bot API cannot enumerate the requests still pending in a group. On the
first successful heartbeat after a longer outage, the bot compares the existing
`heartbeat.json` timestamp with the retention window and sends one localized alert per guarded
group to `admin_log_chat_id`, or to the guarded group when no admin log is configured. Administrators
must then review Telegram's pending join-request queue manually.

### Memory limit

`MemoryMax=512M` is a safety boundary, not a steady-state target. Steady state is dominated by the
Go runtime, Telego buffers, the package/news caches, configuration and i18n data, and the bounded
state maps. The update channel holds at most 100 updates, no more than 64 update handlers run
concurrently, pending verification is capped at 2,000 globally and 500 per group, and the warning,
strike, panel, admin-cache, lookup-rate, cleanup-timer, and feed-tracking structures have explicit
bounds.

A join flood therefore leaves excess requests in Telegram for manual review instead of growing the
pending map or handler goroutine count without limit. Concurrent external lookups and their bounded
response bodies can create the largest transient allocation. If the cgroup reaches 512 MiB, the
kernel OOM-kills the service; systemd records an OOM failure and restarts it after 30 seconds.

### Live verification

After installing the unit, reload and inspect the effective values:

```sh
sudo systemctl daemon-reload
sudo systemctl restart gentoo-zh-verify-bot.service
systemctl show gentoo-zh-verify-bot.service \
  -p Type -p NotifyAccess -p Restart -p RestartUSec \
  -p StartLimitIntervalUSec -p StartLimitBurst \
  -p WatchdogUSec -p TimeoutStopUSec -p MemoryMax
```

Expected values include `Type=notify`, `NotifyAccess=main`, `Restart=always`,
`RestartUSec=30s`, `StartLimitIntervalUSec=0`, `StartLimitBurst=5`,
`WatchdogUSec=2min`, `TimeoutStopUSec=30s`, and `MemoryMax=536870912`.

Inspect the state driven by `sd_notify` and the last completed watchdog keepalive:

```sh
systemctl show gentoo-zh-verify-bot.service \
  -p ActiveState -p SubState -p Result -p MainPID -p NRestarts \
  -p WatchdogTimestamp -p WatchdogTimestampMonotonic
```

Before `READY=1`, the unit remains `ActiveState=activating`; afterward it is
`ActiveState=active` and `SubState=running`. Repeating the command after 30–50 seconds must show a
newer `WatchdogTimestampMonotonic`.

In a maintenance window, exercise clean-exit and crash recovery without stopping the unit:

```sh
sudo systemctl kill --kill-whom=main --signal=SIGTERM gentoo-zh-verify-bot.service
sleep 35
systemctl show gentoo-zh-verify-bot.service -p ActiveState -p SubState -p MainPID -p NRestarts

sudo systemctl kill --kill-whom=main --signal=SIGKILL gentoo-zh-verify-bot.service
sleep 35
systemctl show gentoo-zh-verify-bot.service -p ActiveState -p SubState -p MainPID -p NRestarts -p Result
```

`MainPID` changes and `NRestarts` increases in both cases. Repeating the SIGKILL cycle more than five
times must still return to `active/running`; `StartLimitIntervalUSec=0` prevents a `start-limit-hit`
latch. To exercise the watchdog, stop the main process from making progress and inspect the
journal after 120 seconds; systemd must record a watchdog failure, increment `NRestarts`, and return
the unit to `active/running`.

```sh
sudo systemctl kill --kill-whom=main --signal=SIGSTOP gentoo-zh-verify-bot.service
journalctl -fu gentoo-zh-verify-bot.service
```

After any SIGTERM test, verify the bounded shutdown and state flush in the journal:

```sh
journalctl -u gentoo-zh-verify-bot.service -b --grep='shutdown:'
systemctl show gentoo-zh-verify-bot.service \
  -p MemoryCurrent -p MemoryPeak -p MemoryMax -p OOMPolicy -p Result
```
