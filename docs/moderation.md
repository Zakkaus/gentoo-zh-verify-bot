# Moderation

Moderation commands run only in guarded groups. Unless stated otherwise, the command must reply to the target’s message and the caller must be a non-anonymous group administrator.

## Authorization and target checks

**Implementation:** package `internal/moderate`, `(*Service).warnPrecheck` and `(*Service).isGroupAdmin` in `internal/moderate/service.go`; package `internal/tg`, `(*Client).FreshAdmin` and `(*Client).CachedAdmin` in `internal/tg/tg.go`.

The caller check is a fresh Telegram membership lookup. A lookup error fails closed and no moderation action runs. `/ban`, `/sb`, `/mute`, and `/warn` also use a cached-positive membership check for the target and refuse to act if the target is an administrator. Failure to check the target also refuses the action. `/unmute` and `/clearwarn` deliberately skip the target-admin check.

A missing reply, missing target user, non-guarded chat, anonymous command sender, or failed caller authorization results in no target action. The command message is still scheduled for best-effort deletion once the handler has accepted a guarded command update.

## Ban and purge

**Implementation:** package `internal/moderate`, `(*Service).OnBan`, `(*Service).OnPurge`, and `(*Service).moderate` in `internal/moderate/service.go`; package `internal/tg`, `(*Client).Ban` in `internal/tg/tg.go`.

`/ban` calls Telegram’s member-ban operation with the group’s effective ban duration and without history revocation. `/sb` uses the same duration and sets `revoke_messages=true`, asking Telegram to remove the user’s group history. Both issue the ban before explicitly deleting the replied message, so a ban failure leaves that evidence in place. On success, deletion of the replied message, command cleanup, group notice, and admin-log alert are best-effort.

If the bot lacks Ban users/Restrict members or Telegram rejects the ban, the bot logs the error, sends a failure notice, falls back to the affected group if the admin-log destination is absent, and does not explicitly delete the target message. Failure to send those notices is ignored. The code does not retry the ban.

## Mute and unmute

**Implementation:** package `internal/moderate`, `(*Service).OnMute` and `(*Service).OnUnmute` in `internal/moderate/service.go`; package `internal/tg`, `(*Client).Mute` and `(*Client).Unmute` in `internal/tg/tg.go`.

`/mute` uses the configured finite default or an inline duration. Values below 30 seconds become 30 seconds; values above Telegram’s supported maximum become 366 days. Permanent/zero is invalid for mute. The target message is deleted only after Telegram accepts the restriction. A parse error returns usage without changing the target.

`/unmute` restores the group’s default permissions when Telegram supplies them; otherwise it sends the explicit unrestricted permission set. It can target an administrator because it skips the target-admin guard. A Telegram error produces a failure notice and no success message. Neither command retries.

Missing Restrict members causes the Bot API operation to fail. For `/mute`, the target message remains and a contextual operator alert goes to `admin_log_chat_id`, or to the group when no admin log is configured. For `/unmute`, permissions remain unchanged. Command deletion and notices still depend independently on Delete messages and Send messages.

## Warnings and automatic kick

**Implementation:** package `internal/moderate`, `(*Service).OnWarn` and `(*Service).warnKick` in `internal/moderate/service.go`; `(*warningState).increment` and `(*warningState).save` in `internal/moderate/state.go`.

`/warn` increments a per-group, per-user counter and immediately attempts to save it. Below `warn_limit`, it reports the new count. At the limit, it first bans permanently without history revocation, then immediately unbans with `only_if_banned=true` so the user is removed but may rejoin.

If the kick’s ban fails—normally because Restrict members is missing—the already incremented counter remains at or above the limit, the target remains, and the bot sends both a failure notice and an operator alert. The next warning tries the kick again. If the ban succeeds but unban fails, the counter is cleared and the result states that the user may remain banned. A fully successful kick also clears the counter.

Warning-state write errors are logged by the shared store but ignored by the command. The in-memory counter and moderation action still advance; the latest count may be lost on restart.

## Clear warnings

**Implementation:** package `internal/moderate`, `(*Service).OnClearWarn` in `internal/moderate/service.go`; `(*warningState).clear` and `(*warningState).save` in `internal/moderate/state.go`.

`/clearwarn` removes the replied user’s counter in the current group and reports the previous value, including zero. It requires caller admin status but allows an administrator as the target. No Telegram member restriction is changed.

A warning-state save failure is not returned to the administrator. The counter remains cleared in memory but can reappear after restart if the old file was not replaced. Missing Delete messages can leave the command visible without preventing the clear.

## Channel sock-puppet filter

**Implementation:** package `internal/moderate`, `(*Service).FilterChannelSenders` and `(*Service).OnBC` in `internal/moderate/antispam.go`; package `internal/bot`, `(*Service).Register` in `internal/bot/bot.go`.

The filter is middleware and runs before command handlers when the per-group anti-spam setting is enabled. It targets messages whose `sender_chat` is a different channel identity. It passes through:

- normal user posts;
- anonymous administrators posting as the group itself;
- Telegram automatic forwards from a linked discussion channel;
- configured/registered groups, required channels, trusted groups, feed/admin-log/known chats;
- sender channels in that group’s whitelist.

For an untrusted sender channel, the bot attempts to delete the message, then attempts to ban that sender channel, alerts the admin-log chat with whether the ban succeeded, and consumes the update so no later handler runs. Delete failure is ignored. Ban failure is logged and reported but does not restore a successfully deleted message.

`/bc` with no argument toggles this filter for the invoking group. `/bc allow <id>` commits the whitelist, then tries to unban the sender channel. If unban fails, the whitelist remains committed and the admin receives a partial-failure notice. `/bc deny <id>` removes the whitelist entry but does not itself ban the channel. IDs accept Bot API `-100...` form or the bare numeric segment used by `t.me/c/...`; a full URL is invalid. The whitelist holds at most 4,096 entries and evicts its oldest entries when additions exceed the cap.

The middleware requires BotFather privacy mode to be disabled before Telegram will deliver all channel-identity posts. That delivery condition is external to this repository.

## Missing rights and partial failures

**Implementation:** package `internal/moderate`, `(*Service).CheckGroupSetup` and `(*Service).LogGroupSetup` in `internal/moderate/service.go`; package `internal/tg`, `(*Client).Delete`, `(*Client).Notify`, and `(*Client).FailAlert` in `internal/tg/tg.go`.

The startup/registration self-check reports group access, administrator status, Invite users, Ban users, Delete messages, and required-channel administrator status. It is diagnostic, not a runtime gate: handlers still call Telegram and follow the error branches above.

| Missing capability | Observable runtime behavior |
| --- | --- |
| Caller membership lookup | Authorization fails closed; no target action. |
| Invite users | Verification approvals fail and their pending requests reopen; moderation commands are otherwise unaffected. |
| Ban users / Restrict members | `/ban`, `/sb`, `/mute`, `/unmute`, warning kicks, verification bans, and sender-channel bans fail at their Telegram call. Earlier state changes, such as a warning increment or whitelist commit, are not rolled back. |
| Delete messages | Cleanup is best-effort and errors are ignored. A moderation action may succeed while command or target messages remain. |
| Send messages | Notices and alerts may be absent. The primary moderation call can still have succeeded; the handlers do not use notice delivery as a transaction. |
| Required-channel administrator | Verification channel membership may become unreadable; its fail-open/fail-closed behavior is documented in [Applicant journey](applicant.md). |

There is no general rollback across Telegram calls, settings writes, and warning writes. Ordering in the preceding sections is the recovery contract a code change must preserve or deliberately revise.
