# Deployment

This document follows first startup, durable owner claim, runtime group registration, permission diagnostics, and the supplied systemd unit. Configuration keys are intentionally not duplicated here.

## First start with only `BOT_TOKEN`

**Implementation:** package `main`, `main` and `loadRuntimeState` in `cmd/gentoo-zh-verify-bot/main.go`; package `internal/config`, `LoadConfig` in `internal/config/config.go`; package `internal/store`, `LoadBaseline` and `EffectiveConfig` in `internal/store/baseline.go`.

Outside `-version`, `BOT_TOKEN` is the only required application input. The default config path is `/etc/gentoo-zh-verify-bot/config.json`. A missing file is treated as `{}` and starts with zero configured groups. An unreadable existing file, malformed JSON, invalid group/mode/question/channel values, or invalid baseline stops startup. Unknown keys only log warnings.

When `STATE_DIRECTORY` is nonempty, `loadRuntimeState` tries to create it with mode `0700`, removes orphan `.<name>.tmp-*` files, and places `settings.json` there. Directory-creation failure logs a warning but does not stop startup; subsequent persistence can fail. When the variable is empty, ordinary settings changes are memory-only and all verification/warning/feed state is non-durable. Owner claim and runtime registration are stricter: they refuse to operate without durable settings storage.

Startup then creates the Bot API client, starts long-poll setup with five-second retry delay, constructs the handler, and performs mandatory `GetMe`. Bot construction, initial long-poll setup, handler construction, or `GetMe` failure is fatal. The process registers owner/enrollment routes before ordinary application routes, starts asynchronous permission checks, registers command menus, starts optional feeds, lookup warming, and heartbeat, then starts the handler. An update stream ending without a shutdown signal exits nonzero so systemd can restart it.

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

## systemd unit and state directory

**Implementation:** package `main`, `main` in `cmd/gentoo-zh-verify-bot/main.go`; deployment definition `deploy/gentoo-zh-verify-bot.service`.

The supplied unit:

- runs `/usr/local/bin/gentoo-zh-verify-bot --config /etc/gentoo-zh-verify-bot/config.json`;
- reads `/etc/gentoo-zh-verify-bot/bot.env`;
- uses `DynamicUser=yes`;
- creates `/var/lib/gentoo-zh-verify-bot` as `STATE_DIRECTORY` with mode `0700`;
- restarts only on failure, after five seconds;
- allows outbound `AF_UNIX`, IPv4, and IPv6 but opens no listener;
- applies `MemoryMax=512M`, `UMask=0077`, an empty capability set, filesystem/kernel/process hardening, and the `@system-service` syscall filter.

Normal SIGINT/SIGTERM cancellation stops the handler, freezes verification timers, persists verification state, and gives the feed goroutine up to five seconds to flush. Fatal startup/handler exits use `log.Fatal`/`log.Fatalf`, so deferred graceful shutdown does not run; recovery then uses the latest state already written during operation.

The unit creates the state directory before execution. Manual launches must set `STATE_DIRECTORY` to a private writable directory if state and owner registration must survive restart. Detailed file semantics are in [State and persistence](state-persistence.md).
