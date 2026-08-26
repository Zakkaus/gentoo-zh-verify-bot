# Administrator flows

This document covers group commands, the administrator controls on verification challenges, and the private settings panel. Moderation commands are detailed in [Moderation](moderation.md).

## Routing and authorization

**Implementation:** package `internal/bot`, `(*Service).handlerRoutes` in `internal/bot/bot.go`; package `internal/panel`, `(*Panel).runSettingsAdminCmd` in `internal/panel/panel.go`.

Handlers are first-match routes. Verification callbacks and settings-panel input routes run before ordinary commands and DMs. Group settings commands accept only guarded groups, delete the command message best-effort, and perform a fresh Telegram membership lookup for the caller. A lookup error fails closed and is presented as administrator-only. The command mutation is not attempted.

Informational `/ping`, `/stats`, and `/help` do not require administrator rights. In a group, their command and response use best-effort cleanup. `/help` adds the admin command list only after a cached positive admin lookup; a lookup failure merely omits that section. In DM, status is reported against the effective control group.

The command catalogue has no owner chat scope while the durable owner ID is zero. A successful owner claim immediately refreshes Telegram command menus. The owner's private `BotCommandScopeChat` menu then contains all member commands plus `/enroll` and `/unregister`; restart is not required.

## Group settings commands

**Implementation:** package `internal/panel`, `(*Panel).OnStart`, `(*Panel).OnStop`, `(*Panel).OnRich`, `(*Panel).OnSpoiler`, `(*Panel).OnVMode`, and `(*Panel).OnAutoDel` in `internal/panel/panel.go`; package `internal/moderate`, `(*Service).OnBanTime` and `(*Service).OnBC` in `internal/moderate/service.go` and `internal/moderate/antispam.go`.

- `/start` and `/stop` enable or disable join verification for the invoking group. `/start` in DM instead opens a settings deep link or sends an applicant challenge.
- `/vmode` shows or sets `kernel`, `quiz`, or `mixed`; `auto`, `config`, and `default` remove the runtime override.
- `/spoiler` toggles hiding applicant names in group challenges.
- `/autodel` shows, disables, enables, or sets the per-group lookup cleanup interval from 1 to 1,440 minutes.
- `/bantime` shows or sets the per-group ban duration. Zero/permanent is accepted; Telegram-compatible bounds are applied.
- `/bc` toggles the per-group sender-channel filter or changes its whitelist. Its moderation behavior is in [Moderation](moderation.md).
- `/rich` is the command-path bot-wide setting.

All changes go through the settings store. If the state directory is absent, ordinary group/global commits are valid but runtime-only. A validation or write error leaves the previous effective snapshot in place and produces a settings-save failure notice.

## Verification challenge buttons

**Implementation:** package `internal/verify`, `(*Service).OnAdminAction`, `(*Service).executeApprove`, and `(*Service).executeBan` in `internal/verify/service.go`.

The in-group challenge offers direct approval and decline-plus-ban. Each click performs a fresh admin lookup for the acting user. Non-admins and lookup failures receive an admin-only callback result. The target applicant ID comes from callback data; no replied message is required.

Approval claims the pending request before calling Telegram, so its timeout cannot race the action. If approval succeeds, the pending request and old strikes are removed, and both recorded group and private challenges are deleted best-effort. If it fails, the request is reopened with a retry timer and operators are alerted.

Ban claims the pending request, applies the group’s effective ban with message revocation, and then asks Telegram to decline the join request. The callback is acknowledged before these network actions. Both operations must succeed before the pending record is removed and both recorded challenges are deleted. Either failure reopens the same pending request, preserves both challenges, grants a strike-free retry window, and sends an operator alert plus a localized group result.

## Control-group policy

**Implementation:** package `internal/config`, `(*Config).ControlGroupAllowed` in `internal/config/config.go`; package `internal/store`, `(*Settings).ControlGroupID` in `internal/store/settings.go`; package `internal/panel`, `(*Panel).runSettingsAdminCmd` and `(*Panel).applyTextInput` in `internal/panel/panel.go` and `internal/panel/settings_input.go`.

There are two distinct policy paths in the current code:

- The command-path global setting `/rich` calls `Config.ControlGroupAllowed`. A nonzero `control_group_id` permits only that group. When it is zero, any guarded group is allowed.
- The settings panel’s bot-wide private-query rate uses `Settings.ControlGroupID`. That resolves the durable registration control group, then falls back to the first effective group. Other groups can view the value but cannot edit it.

Runtime registration stores a control-group ID when the first runtime group is added to an otherwise empty deployment, but the startup effective-config projection is not refreshed after registration. Therefore a deployment with no configured `control_group_id` does not have one uniform control-group rule: `/rich` remains available to admins of every guarded group, while panel edits to the global private-query rate are restricted to the settings store’s control group. This is verified code behavior, not an intended-policy claim.

## Opening the settings panel and choosing a group

**Implementation:** package `internal/panel`, `(*Panel).OnSettings`, `(*Panel).openSettingsStart`, and `(*Panel).eligibleGroups` in `internal/panel/settings_panel.go`; `(*Panel).newSettingsSession` in `internal/panel/session.go`.

`/settings` must be sent in a guarded group by a current admin. The bot resolves its username, creates a user-bound session, and replies in the group with a private-chat deep link. Authorization lookup failure, non-admin status, missing bot username/GetMe failure, session-cap exhaustion, token generation failure, or launcher-send failure prevents the panel from opening.

A session lasts 30 minutes, there can be at most 256, and a user can own only one. Rendering another screen rotates its token but does not extend the deadline. A new launch invalidates that user’s previous session, and a process restart invalidates every session and unsaved draft. Opening the link in DM checks the token, owner ID, and fresh admin status in the anchor group. A wrong/expired token is rejected. Lost authorization destroys the session.

The group picker lists only effective groups where the bot is still present and the user is an admin. The anchor group reuses the launch authorization; every other group gets a fresh lookup. Lookup failures omit that group. Selecting a group verifies the bot’s membership again, records the group and global revisions, and opens its home screen. Pagination is eight groups per page.

## Settings available in DM

**Implementation:** package `internal/panel`, `(*Panel).dispatchRuntime`, `(*Panel).dispatchList`, `(*Panel).dispatchModeration`, and `(*Panel).dispatchVerificationParameters` in `internal/panel/settings_panel.go`; `(*Panel).dispatchChannel` in `internal/panel/settings_input.go`.

The panel exposes source provenance—runtime override, `config.json`, or built-in default—and edits these values:

- runtime: verification enabled, challenge delivery (`group`, `dm`, or the default `both`), mode, name spoiler, ban duration, lookup auto-delete and TTL, and group language;
- lists: sender-channel whitelist, trusted-member groups, and known/support chats;
- verification parameters: timeout (30–1,800 seconds), maximum failures or off, cooldown or off, whether a member somebody else invited still has to verify, and the bot-wide private-DM query rate;
- moderation: sender-channel blocking, `/mute` duration, `/warn` limit, and the two bot-wide switches — rich text and the alert chat;
- required channel: select a channel, set or clear a private invite, or disable the gate.

The moderation screen's mute duration must lift on its own, so a permanent (zero) value is rejected there; `/mute <duration>` still overrides it per call. The alert chat is picked with Telegram's chat picker and decides where operator alerts go; clearing it returns to the previous behaviour of posting in the group where the failure happened. Both bot-wide switches on that screen are editable only from the control group, and both take effect without a restart.

`delivery_mode` has a built-in `both` baseline and can also be set globally or per group in `config.json`. The three panel buttons commit a sparse group override at the revision shown by the panel. Selecting the baseline value removes a baseline-equal override. A concurrent group commit ends the panel with the same conflict handling as the neighboring runtime controls.

List additions use Telegram’s chat picker. The submitting admin must still belong to the selected chat. A required channel must also contain the bot. A private channel without a username requires a valid `https://t.me/...` invite before the channel and display are committed together. Duplicate list additions are no-ops. Removing an absent list item is treated as a concurrent change. Whitelisting commits first and then tries to unban the sender channel; unban failure is reported but does not roll back the whitelist.

Text/chat-picker input is blocked while the same user has a live kernel verification, so settings replies cannot be consumed as kernel answers. If such a verification appears after an input was armed, the panel cancels that input. Only an exact ForceReply message ID or exact chat-picker request ID is accepted. Invalid numbers, durations, URLs, chats, empty text, and stale prompts leave the input active for correction where the code provides a recoverable validation notice.

## Question banks

**Implementation:** package `internal/panel`, `(*Panel).dispatchQuizDraft`, `(*Panel).dispatchFallbackDraft`, and `(*Panel).dispatchConfirmation` in `internal/panel/settings_input.go`.

The quiz bank supports paged add, edit, delete, option add/remove, and selection of the correct option. Saving requires a nonempty prompt, at least two options, and a valid correct option. An empty quiz bank is allowed; verification selection then falls back to kernel when quiz cannot be constructed.

The no-Linux fallback bank supports add, edit, delete, answer add/remove, and reset to localized built-ins. A custom fallback question needs a nonempty prompt and at least one answer. Deleting the final custom question automatically switches back to built-ins. Destructive deletes, required-channel disable, and reset-to-built-ins require confirmation. Cancel leaves the persisted bank unchanged.

Draft edits exist only in the panel session until Save. Session expiry, close, cancel, or a conflicting committed revision discards an unsaved draft.

## Revision checks, stale controls, and failures

**Implementation:** package `internal/panel`, `(*Panel).OnSettingsCallback`, `(*Panel).OnPanelInput`, and `(*Panel).OnPanelChatShared` in `internal/panel/settings_panel.go` and `internal/panel/settings_input.go`; package `internal/store`, `(*Settings).CommitGroup` and `(*Settings).CommitGlobal` in `internal/store/settings.go`.

Every rendered panel rotates its callback token and binds callbacks to the owner, current screen, selected group, private chat, and panel message ID. Old buttons, malformed callback data, copied callbacks, and callbacks from another user cannot mutate settings. Every callback and every submitted input rechecks current admin status.

Navigation, pagination, refresh, and cancel can absorb a newer group revision. A mutation, draft action, confirmation, or input submission requires the revision captured by the session/prompt. A mismatch ends the session with a concurrent-change message rather than merging. Global private-rate commits independently check the captured global revision.

`CommitGroup` and `CommitGlobal` build and validate a candidate, write it atomically when persistence is configured, and publish the new immutable snapshot only after the write succeeds. A write or validation failure ends the current panel session with a save-failed message. If a runtime setting commits but the subsequent panel edit fails, the callback instead reports that the setting was probably saved and tells the administrator to reopen the panel. Telegram send/edit/delete failures can leave stale UI messages or keyboards; they do not bypass the revision or authorization checks.
