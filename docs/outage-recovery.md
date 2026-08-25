# Outage and recovery

The recovery mechanism protects applicants from deadlines consumed while the bot cannot reach Telegram. It does not pause wall-clock timers globally; each expiry checks reachability before settlement.

## Heartbeat and outage detection

**Implementation:** package `internal/verify`, `(*Service).RunHeartbeat`, `(*Service).heartbeatTick`, and `(*Service).offlineNow` in `internal/verify/state.go`; package `main`, `main` in `cmd/gentoo-zh-verify-bot/main.go`.

The process starts one heartbeat goroutine. Its first probe occurs after 25 seconds and repeats every 25 seconds. Each `GetMe` probe has a ten-second context. Successful probes update `lastOnline` and best-effort persist `heartbeat.json`. Failed probes log that verification timeouts are paused and leave `lastOnline` unchanged.

More than 70 seconds since confirmed contact marks the service offline. More than 90 seconds between successful probes makes the next success a recovery event. These thresholds differ deliberately: an expiry also performs a fresh probe, so it can detect an outage before the heartbeat’s stale-contact threshold.

Heartbeat write failure does not stop probing and is not surfaced beyond the shared store log. Cancellation stops the loop without another probe.

## Expiry while Telegram is unreachable

**Implementation:** package `internal/verify`, `(*Service).onExpiry`, `(*Service).reachable`, and `(*Service).deferExpiry` in `internal/verify/state.go`.

When a pending timer fires, `onExpiry` first checks stale heartbeat state and then, when needed, performs a fresh ten-second `GetMe` probe. If either says Telegram is unreachable, the pending request is not consumed, declined, or struck. `deferExpiry` gives it a new full group timeout and arms a new epoch-bound timer.

“Paused” therefore means repeated fresh windows, not a stopped timer. The original reason is retained. An ordinary timeout deferred through an outage can still become a normal striking timeout if its later timer fires while Telegram is reachable and no sustained-recovery event has replaced the reason.

Every timer captures the pending nonce and an incrementing epoch. A stale timer from a replaced request, previous deadline, or recovery cannot settle the current pending record. Failure of the expiry-time probe is enough to defer; no operator action is required.

## Runtime recovery and re-notification

**Implementation:** package `internal/verify`, `(*Service).onRecovery` and `(*Service).renotifyPending` in `internal/verify/state.go`.

After more than 90 seconds without successful contact, the next successful heartbeat grants every live pending request a new full deadline with the strike-free reason `recovered`. It snapshots notifications under the service lock, then performs Telegram calls without holding that lock.

At most 30 applicants per recovery receive both a DM outage notice and a replacement group challenge. Additional pendings receive the fresh deadline silently. A pending re-notified within its own timeout window is not messaged again during flapping, although its deadline is refreshed. Recovery is suppressed after graceful shutdown begins.

DM-send errors are ignored. If posting the replacement group challenge fails, the bot logs and alerts, deletes the old challenge best-effort anyway, and records message ID zero for the still-live pending. Its eventual expiry is then no-fault because no replacement challenge was confirmed delivered. Delete and alert failures do not roll back the fresh window.

After re-notification, pending state is saved so new deadlines and group message IDs survive another crash. Save failure leaves the fresh state only in memory.

## Restart recovery

**Implementation:** package `internal/verify`, `(*Service).load` and `(*Service).loadHeartbeat` in `internal/verify/state.go`; package `internal/verify`, `New` in `internal/verify/service.go`.

Construction loads `pending.json` and uses the last successful timestamp in `heartbeat.json` to estimate downtime. It restores only records for groups present in the startup effective config. Invalid/unwinnable quiz payloads are skipped. Kernel attempts, fallback state, one-shot guards, nonce, language, question, deadline, and group message ID are retained.

If estimated downtime exceeds 90 seconds, every restored pending receives a fresh full window with strike-free recovery reason. Up to 30 are re-notified as in runtime recovery, and the adjusted state is saved. If downtime is shorter, a future deadline keeps its remaining duration. A deadline that elapsed during the short restart receives a 60-second strike-free `restart-lapsed` window.

The short-restart 60-second adjustment is not immediately saved by `load`. If the process crashes again before another pending-state save, the next restart can calculate the grace again from the old deadline. Missing, corrupt, or unreadable heartbeat state yields no downtime estimate, so long-outage restart recovery is not selected. The pending records still follow their stored deadlines/short-restart rules.

An unreadable `pending.json` leaves the file untouched, disables pending writes for this process, and restores nothing. Corrupt JSON is moved to `.corrupt` when possible, restores nothing, and leaves the path available for later fresh saves.

## Shutdown and process-level failure

**Implementation:** package `internal/verify`, `(*Service).Shutdown` in `internal/verify/service.go`; package `main`, `streamEndedUnexpectedly` and `main` in `cmd/gentoo-zh-verify-bot/main.go`.

On signal-driven shutdown, long polling stops first and the handler drains every update already fetched into Telego before it stops. `Shutdown` then marks verification as shutting down, stops every timer, refuses later settlement claims, and saves pending, strike, and heartbeat state. A timer racing shutdown therefore cannot decline, strike, or ban an applicant after the freeze.

Long polling is configured to retry transient polling errors every five seconds. If the update stream ends while the process context is still live, the process exits nonzero for systemd restart. Fatal paths do not execute graceful deferred cleanup. They rely on per-event atomic saves and the previously persisted heartbeat; an in-flight mutation whose save had not completed can be absent after restart.
