# Applicant journey

This document follows one Telegram join request from receipt to settlement. It describes repository behavior, not Telegram client behavior outside the Bot API updates the process receives.

## Entry gates

**Implementation:** package `internal/verify`, `(*Service).OnJoinRequest`, `(*Service).joinGate`, and `(*Service).tryTrustedBypass` in `internal/verify/service.go`.

`OnJoinRequest` acts only on configured or durably registered guarded groups. If verification is disabled, the request remains pending for manual review. Before creating a challenge, the bot checks trusted groups. Confirmed membership in any configured trusted group clears old verification strikes and approves immediately. A failed trusted-group lookup is treated as untrusted. If trusted membership is confirmed but approval fails, the bot alerts operators and continues with normal verification without applying the failure cooldown.

An untrusted applicant with an active cooldown is declined immediately and receives no new challenge. Failure of that decline is logged; the handler still stops processing that update. Queue limits are 2,000 live requests process-wide and 500 per group. At either limit, the request is left for manual review and the operator alert is throttled to one per ten minutes.

A new request for the same group and user replaces the old pending request, deletes the old group challenge when possible, and receives a fresh deadline. It does not restore used kernel attempts or one-shot hints. A request arriving while an approval or ban is in flight is deferred without replacing that terminal action.

## Group challenge and DM deep link

**Implementation:** package `internal/verify`, `(*Service).postGroupChallenge`, `(*Service).SendDMChallenge`, and `(*Service).sendQuizzes` in `internal/verify/state.go` and `internal/verify/service.go`; package `internal/panel`, `(*Panel).OnStart` in `internal/panel/panel.go`.

The bot first reserves a pending record, then posts an in-group challenge. The message contains the applicant, the deadline, an optional required-channel hint, a `t.me/<bot>?start=verify` button when the bot username is available, and administrator approve/ban buttons. The deep link reaches `Panel.OnStart` in DM, which calls `SendDMChallenge` for that user.

A failed group post produces message ID zero, alerts administrators, and keeps the pending request. Its eventual expiry is classified as a bot-caused delivery failure: the join request is declined, but no verification strike is recorded. The applicant can still open the bot manually and run `/start`. DM prompt sends are best-effort. Kernel answers are not graded until a kernel prompt was successfully delivered. Repeated `/start` requests within 15 seconds do not resend a live prompt.

If one user has live requests in several groups, `sendQuizzes` sends every live challenge after the initial channel gate. The initial gate uses one live pending selected from a Go map; the code does not define which group wins. Each answer later enforces the required channel of its own group.

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

An applicant who states that Linux is unavailable must also include a current-minute proof. One malformed declaration receives a format reminder. A valid declaration switches that pending request once to a typed, answer-hidden fallback question. Operator-configured fallback questions take precedence; otherwise a localized built-in pool is used. Whole normalized fallback answers pass, and a valid real kernel remains acceptable after the switch. Failed fallback or kernel replies consume attempts; the final failed reply declines the request.

Every kernel and fallback prompt contains a nonce-derived AI tripwire. Only the exact token plus a `model=...` declaration triggers it. A matching reply is treated as a wrong answer for every simultaneously pending, prompted kernel challenge belonging to that user. The declaration is tallied once, administrators are alerted, and each affected request is declined. The tally is self-reported telemetry, not proof that an AI system was used.

## Pass, wrong answer, timeout, cooldown, and automatic ban

**Implementation:** package `internal/verify`, `(*Service).executeApprove` and `(*Service).finishDecline` in `internal/verify/service.go`; `(*Service).recordVerifyFail` and `(*Service).verifyCooldownRemaining` in `internal/verify/state.go`.

A successful Bot API approval removes the pending record, clears prior strikes for that group and user, deletes the group challenge best-effort, and increments the in-memory daily approval count. If approval fails, the bot alerts the admin-log chat or affected group, reopens the same pending request, and grants at least a 60-second strike-free retry window.

Wrong quiz answers, the final failed kernel/fallback reply, the AI tripwire, and ordinary online timeouts record a failure. The bot deletes the challenge best-effort, records a decline, calls Telegram to decline the join request, and logs/alerts if that call fails. Failure of the Telegram decline does not roll back the strike.

Failures from the same group and user accumulate only while consecutive failures remain within a six-hour rolling window. `verify_retry_seconds` blocks early re-application after any recorded failure; a nonpositive effective value disables the cooldown. At `verify_max_fails`, a positive threshold triggers the effective verification ban. A successful automatic ban clears the strike record. A failed automatic ban is alerted and leaves the threshold strikes in place, so a later failure attempts the ban again. A negative threshold disables automatic bans. Ban duration follows the group’s effective ban setting; zero means permanent.

Bot-caused reasons—failed challenge delivery, failed approval retry, a lapsed deadline during a short restart, and recovery windows—may still decline the pending join request when their grace timer ends, but do not record a strike. Outage-specific deferral is covered in [Outage and recovery](outage-recovery.md).
