# Applicant journey

This document follows one Telegram join request from receipt to settlement. It describes repository behavior, not Telegram client behavior outside the Bot API updates the process receives.

## Entry gates

**Implementation:** package `internal/verify`, `(*Service).OnJoinRequest`, `(*Service).joinGate`, and `(*Service).tryTrustedBypass` in `internal/verify/service.go`.

`OnJoinRequest` acts only on configured or durably registered guarded groups. If verification is disabled, the request remains pending for manual review. Before creating a challenge, the bot checks trusted groups. Confirmed membership in any configured trusted group clears old verification strikes and approves immediately. A failed trusted-group lookup is treated as untrusted. If trusted membership is confirmed but approval fails, the bot alerts operators and continues with normal verification without applying the failure cooldown.

An untrusted applicant with an active cooldown is declined immediately, receives a localized DM with the current remaining seconds, and receives no new challenge. If that decline fails, the bot alerts the admin-log chat or affected group and tells the applicant that the request may still be pending. Queue limits are 2,000 live requests process-wide and 500 per group. At either limit, the request is left for manual review and the operator alert is throttled to one per ten minutes.

A new request for the same group and user replaces the old pending request, deletes the old group and private challenges when possible, and receives a fresh deadline. It does not restore used kernel attempts or one-shot hints. A request arriving while an approval or ban is in flight is deferred without replacing that terminal action.

## Verifying someone who is already a member

**Implementation:** package `internal/verify`, `(*Service).OnMemberJoined`, `joinedNow`, `(*Service).holdMember`, `(*Service).executeRelease`, and `(*Service).removeMember` in `internal/verify/service.go`.

A group that does not ask people to apply never produces a join request, and an administrator adding someone directly bypasses approval even in a group that does. `OnMemberJoined` covers both: it acts on a `chat_member` update where someone outside the group becomes an ordinary member. Promotions, demotions, departures, and a lifted hold are not arrivals. Bots, the bot itself, group administrators, and confirmed members of a trusted group are not challenged. Neither is anyone the bot let in during the last five minutes, so its own approval of a join request does not read as a fresh arrival.

The group notice distinguishes a member who walked in from one somebody else added: an invited member still has to verify, and the notice says so and points administrators at the button that lets them in without the challenge. Nothing about the verification itself changes — an administrator who does not press it leaves the member verifying like anyone else. A group that would rather trust its own invitations can switch this off in the settings panel; it is on by default, because being vouched for is not verification.

`timeout_seconds` describes how long an applicant waits outside, so a held member gets a longer default window of ten minutes: they are not watching for a challenge the way an applicant is, and the hold keeps them harmless meanwhile. Setting the timeout in the settings panel is a deliberate choice and applies to both gates.

The pending record carries which gate it belongs to, and every settlement follows it. Passing a held verification lifts the restriction by restoring the group's own default permissions; passing a join request still calls `approveChatJoinRequest`. Failing a held verification bans and immediately unbans, which removes the member while leaving the invite link usable; the automatic-ban threshold still applies on top, and reaching it bans for the configured duration. Applicant-facing wording follows the gate throughout: a member standing in the group is never told their join request was declined.

A member an administrator added is never removed for an earlier failure — that would be the bot overruling the person who added them — though they still verify. Anyone else inside the failure cooldown is removed again rather than questioned again: the removal notice tells them how many seconds to wait, and rejoining before that expires is refused with one throttled explanation. A member who is already being verified keeps the challenge they have — the hold is re-applied, since leaving the group clears it, and the deadline does not move, so walking in and out buys no extra time.

Telegram only restricts members of supergroups. In a basic group the challenge still runs and failing it still removes the member, but there is no hold, so they can speak during the window. The hold otherwise lasts the verification timeout plus one minute, so it cannot expire between the deadline and the settlement acting on it.

## Challenge delivery modes

**Implementation:** package `internal/verify`, `(*Service).deliverPendingChallenge`, `(*Service).attemptPrivateChallenge`, `(*Service).postGroupChallenge`, `(*Service).SendDMChallenge`, and `(*Service).sendQuizzes` in `internal/verify/state.go` and `internal/verify/service.go`; package `internal/panel`, `(*Panel).OnStart` in `internal/panel/panel.go`.

The bot first reserves a pending record, then applies the group's `delivery_mode`:

- `group` posts only the group challenge.
- `dm` attempts the private required-channel prompt or challenge. A definite Telegram rejection falls back to the group challenge.
- `both`, the default, posts the group challenge first and then attempts the private prompt or challenge. The request counts as delivered when either send succeeds.

Telegram permits a proactive private send only when the applicant has started the bot at least once. A 403 because the bot cannot initiate a conversation or because the applicant blocked it is a definite rejection. A 429 is also definite: the handler waits for Telegram's `retry_after` and retries once when that delay fits inside the verification timeout. Other definite 4xx responses are logged as rejections.

A transport, cancellation, 5xx, or other ambiguous failure may follow an accepted send. In `dm`, that uncertainty suppresses the group fallback. In `both`, the existing group message remains and no second group message is posted.

The group challenge contains the applicant, a fresh full deadline, an optional required-channel hint, a `t.me/<bot>?start=verify_<groupID>` button when the bot username is available, and administrator approve/ban buttons. `Panel.OnStart` passes the encoded group to `SendDMChallenge`, which opens only that pending request. If the named request no longer exists, the same localized no-pending result used for an expired request is sent. Repeated starts for the same group within 15 seconds do not resend its live prompt.

Bare `?start=verify` links already in flight retain the old behavior: one live pending is selected from the Go map for the initial channel gate, then `sendQuizzes` sends every live challenge. New group-scoped links do not depend on map order and cannot be diverted by another group's required-channel gate.

The pending record persists the current group and private message IDs. A missing private ID in an older record means there is no private message to delete. Settlement and timeout delete both messages independently. Outage re-notification uses the current `delivery_mode`, records a newly delivered private challenge, and deletes both stale challenges after replacement delivery is confirmed.

If neither delivery can be confirmed, expiry is classified as a bot-caused delivery failure: a successful decline records no verification strike, and the applicant receives a localized timeout result without a cooldown. A confirmed delivery starts the normal full answer window.

## Required-channel gate

**Implementation:** package `internal/verify`, `(*Service).SendDMChallenge`, `(*Service).OnChannelRecheck`, and `(*Service).isChannelMember` in `internal/verify/service.go`.

When the selected pending group requires a channel and the applicant is not a member, DM shows the configured public or private join link when available and a recheck button. The recheck callback is bound to the applicant ID. Another user receives a “not yours” result; malformed, stale, or already settled callbacks do not change state. A successful recheck sends all live challenges. Membership is checked again on every correct quiz or kernel answer, so leaving the required channel before settlement cannot pass.

A user-membership lookup failure normally fails closed and leaves the pending request active. The code then checks whether the bot can read its own membership in that channel. If the bot itself cannot access the channel, it sends a throttled operator alert and applies `required_channel_fail_open`: the default `true` treats the gate as passed, while `false` leaves the request pending as if the applicant were not a member. The strict path does not immediately decline; a later timeout can do that.

## Challenge selection and quiz answers

**Implementation:** package `internal/verify`, `(*Service).pickMode` in `internal/verify/kernel.go` and `(*Service).OnAnswer` in `internal/verify/service.go`.

`kernel` selects a typed kernel challenge. `quiz` selects a random question and cryptographically shuffles its options. `mixed` uses a cryptographic coin flip for each pending request. If runtime settings leave a quiz or mixed choice with an empty quiz bank, `pickMode` falls back to `kernel` rather than creating an unwinnable quiz. A random-source failure selects index zero deterministically.

Quiz callback data binds group, applicant, pending nonce, and option. Buttons from another user, malformed option indices, already settled requests, and stale nonces cannot settle the current request. A wrong option immediately follows the failed-verification path. A correct option must pass the required-channel check before the bot claims and approves the request.

## Kernel challenge, no-Linux fallback, and AI tripwire

**Implementation:** package `internal/verify`, `(*Service).OnKernelAnswer`, `(*Service).gradeKernelAnswer`, and `(*Service).declineAgent` in `internal/verify/kernel.go`.

A kernel prompt accepts a plausible Linux release in constrained contexts, not an arbitrary dotted number. The applicant has three charged replies. The first copied sample, one mixed other-OS/kernel clarification, and the documented no-Linux transition do not consume an attempt in their one-shot branches.

An applicant who states that Linux is unavailable must also include a current-minute proof. One malformed declaration receives a format reminder. A valid declaration switches that pending request once to a typed, answer-hidden fallback question. Operator-configured fallback questions take precedence; otherwise a localized built-in pool is used. Whole normalized fallback answers pass, and a valid real kernel remains acceptable after the switch. Failed fallback or kernel replies consume attempts; the final failed reply enters decline settlement.

Every kernel and fallback prompt contains a localized AI tripwire with a nonce-derived token. Only the byte-identical token plus a `model=...` declaration triggers it; localization changes only the surrounding instruction. A matching reply is treated as a wrong answer for every simultaneously pending, prompted kernel challenge belonging to that user. The declaration is tallied once, administrators are alerted, and each affected request enters decline settlement. The tally is self-reported telemetry, not proof that an AI system was used.

## Pass, wrong answer, timeout, cooldown, and automatic ban

**Implementation:** package `internal/verify`, `(*Service).executeApprove`, `(*Service).executeBan`, and `(*Service).finishDecline` in `internal/verify/service.go`; `(*Service).recordVerifyFail` and `(*Service).verifyCooldownRemaining` in `internal/verify/state.go`.

A successful Bot API approval removes the pending record, clears prior strikes for that group and user, deletes both recorded challenges best-effort, and increments the in-memory daily approval count. If approval fails, the bot alerts the admin-log chat or affected group, reopens the same pending request, and grants at least a 60-second strike-free retry window.

An administrator decline-and-ban callback keeps the same pending record and both recorded challenges until Telegram confirms both operations. It bans first, so a rejected ban does not discard the join request before the action can be retried. Either failure posts a localized result to the group, alerts the admin-log chat or affected group, and grants a strike-free retry window.

Wrong quiz answers, the final failed kernel/fallback reply, the AI tripwire, and ordinary online timeouts first ask Telegram to decline the join request. A confirmed decline deletes both challenges best-effort, records the failure and decline decision, and applies the automatic-ban threshold. A failed decline records no strike, keeps both challenges, reopens and re-arms the same pending request with at least 60 seconds, alerts the admin-log chat or affected group, and tells the applicant that the request may still be pending.

Failures from the same group and user accumulate only while consecutive failures remain within a six-hour rolling window. `verify_retry_seconds` blocks early re-application after any recorded failure; a nonpositive effective value disables the cooldown. An early re-application receives the current remaining seconds. An ordinary timeout reports whether the applicant may reapply immediately, must wait for the cooldown, or received an automatic ban. At `verify_max_fails`, a positive threshold triggers the effective verification ban. A successful automatic ban clears the strike record. A failed automatic ban is alerted and leaves the threshold strikes in place, so a later failure attempts the ban again. A negative threshold disables automatic bans. Ban duration follows the group’s effective ban setting; zero means permanent.

Bot-caused reasons—failed challenge delivery; approval, administrator-ban, or decline retries; a lapsed deadline during a short restart; and recovery windows—may still decline the pending join request when their grace timer ends, but do not record a strike. Outage-specific deferral is covered in [Outage and recovery](outage-recovery.md).

The first timeout deferred because Telegram is unreachable starts a persisted 48-hour deferral limit. Recovery and restart do not reset it. At the limit, the bot keeps trying Telegram once per minute; after Telegram confirms the decline, the applicant receives a localized notice that verification could not be completed and may submit another join request. This settlement deletes both challenge messages and adds no strike, cooldown, or ban.
