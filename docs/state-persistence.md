# State and persistence

All durable runtime state is JSON under `STATE_DIRECTORY`. The application never writes `config.json`.

## Storage transaction

**Implementation:** package `internal/store`, `Load`, `Save`, `Write`, `writeLocked`, and `ReclaimTemps` in `internal/store/json.go`; package `main`, `loadRuntimeState` in `cmd/gentoo-zh-verify-bot/main.go`.

All state writes share one process-wide mutex. The store marshals a stable snapshot, creates a same-directory mode-`0600` temporary file, writes and `fsync`s it, closes it, atomically renames it over the target, and `fsync`s the parent directory. A parent-directory sync error is returned even though the rename has already occurred. Startup removes orphan temporary files matching `.<name>.tmp-*` from `STATE_DIRECTORY`.

A missing file is normal first-run state. A JSON decode failure is logged and the store attempts to rename the original to `<name>.corrupt`; no retention or cleanup policy for those backups exists in code. If that rename fails, the original remains in place and `Load` returns a write-disabling error so callers cannot overwrite it. An unreadable existing file returns the same classified error. Unknown JSON fields decode successfully but are dropped on a later rewrite.

Most state producers log and ignore write errors, so in-memory behavior continues and the next ordinary state event may try again. Settings commits are the exception: they do not publish a candidate snapshot when its write fails.

## `settings.json`

**Implementation:** package `internal/store`, `NewSettings`, `(*Settings).CommitGroup`, `(*Settings).CommitGlobal`, and `(*Settings).CommitRegistrations` in `internal/store/settings.go`.

Schema version 2 stores sparse per-group and bot-wide overrides, group/global revisions, legacy compatibility mirrors, owner claim and expiry, owner ID, durable control-group choice, registered groups, enrollment capabilities, and pending registrations. Effective values are rebuilt from runtime overrides over the immutable `config.json`/default baseline.

Group/global commits use optimistic revisions. With no settings path they update the in-memory snapshot and report non-durable success. Registration commits always require a real durable path. A successful write precedes publication of the immutable snapshot, so readers never observe an override whose commit failed.

- Missing: starts from the baseline and can create the file on the first durable commit.
- Corrupt JSON: if the original can be moved to `.corrupt`, the process starts from baseline state and remains writable. A later commit creates fresh schema-v2 state. Owner and prior registrations are absent until recovered manually. If the backup rename fails, settings writes are disabled and the original stays in place.
- Unreadable existing file: starts from baseline but marks settings unavailable; all group/global/registration writes fail until restart, and the original is not overwritten.
- Unsupported newer or invalid schema version: preserves the file and disables writes.
- Unwritable path: each commit fails and the previous effective snapshot remains active.

Versions 0 and 1 have explicit migrations. An applicable migration also imports legacy `antispam.json`; current version 2 does not read that legacy file.

## Upgrade and rollback

Before the first write that migrates a version-0 `settings.json` to schema v2, the current version makes a best-effort, byte-for-byte atomic backup at `settings.json.v0.bak`. A backup failure is logged at `ERROR` but does not block the migration. This file contains only the pre-upgrade version-0 state; it does not protect schema-v2 state from a later rollback.

Rolling back to a release without schema-v2 support destroys current state on that release's next settings write. The old writer keeps only `enabled`, `name_spoiler`, and `verify_mode`; it drops every group and global override and revision, `registration_revision`, the owner identity and claim, registered groups, enrollment nonces, and pending registrations. Upgrading again treats that result as version 0 and can recover only those three legacy settings. The old release also reads the frozen `antispam.json`, silently restoring the antispam toggles and channel whitelist that existed before migration.

Before rollback, stop the service and back up the entire `STATE_DIRECTORY`; at minimum, copy the current `settings.json` and `antispam.json`. Restore the schema-v2 `settings.json` before starting the current version again. The current version intentionally does not maintain a live mirror in `antispam.json`: that migration is one-way by design.

## `pending.json`

**Implementation:** package `internal/verify`, `(*Service).save` and `(*Service).load` in `internal/verify/state.go`; `(*Service).Shutdown` in `internal/verify/service.go`.

The file is an array of active verification records: group/user IDs, group challenge message ID, mode and locale, fallback answers, prompt and one-shot guards, used attempts, question/options/correct index, nonce, applicant name, and deadline. Graceful shutdown stops timers before the final save. Restart restores timers, subject to outage recovery, group validity, capacity, and quiz-payload validation.

Missing or successfully backed-up corrupt state restores no pending requests; later saves remain enabled. An unreadable file or a corrupt file whose backup failed restores nothing and clears the service's path, so the process cannot overwrite the recoverable original. Later save failures are ignored after the store log; live requests continue in memory but can be lost on restart.

Legacy records with no mode are treated as quizzes. See [Outage and recovery](outage-recovery.md) for deadline changes and re-notification.

## `verifyfail.json`, `agents.json`, and `heartbeat.json`

**Implementation:** package `internal/verify`, `(*Service).loadVerifyFails`, `(*Service).saveVerifyFails`, `(*Service).loadAgents`, `(*Service).recordAgent`, `(*Service).loadHeartbeat`, and `(*Service).saveHeartbeat` in `internal/verify/state.go`.

- `verifyfail.json` stores group/user failure count and last-failure Unix time. It preserves cooldowns and the six-hour automatic-ban accumulation window. Successful verification or successful automatic ban removes the relevant record.
- `agents.json` stores total AI-tripwire matches and counts by self-declared model. Distinct model keys are capped at 200; additional unknown keys fold into `other`.
- `heartbeat.json` stores the last successful Telegram-contact Unix time for restart outage estimation.

Each missing or successfully backed-up corrupt file starts empty/zero. Each unreadable file, or corrupt file whose backup failed, disables later writes only for its own path in the current process. Write failures leave the current in-memory strike/tally/heartbeat active and are otherwise ignored. A missing or unusable heartbeat removes long-outage evidence; it does not prevent pending restoration.

## `warns.json`

**Implementation:** package `internal/moderate`, `(*warningState).load`, `(*warningState).save`, `(*warningState).increment`, and `(*warningState).clear` in `internal/moderate/state.go`.

The file stores positive `{group_id,user_id,count}` records. At most 4,096 counters remain in memory; overflow evicts the least severe counters first with deterministic key tie-breaking. Counts are saved after every accepted warning and after a threshold kick clears the counter.

Missing/corrupt state starts empty. An unreadable file clears the warning path, preserving the original and disabling later writes for the process. Save errors do not roll back in-memory increments/clears or Telegram moderation. A failed save after a clear can leave the older count on disk, so restart can restore a warning already cleared in memory.

## `feed-<chat_id>.json`

**Implementation:** package `internal/feed`, `feedStatePath`, `loadFeedState`, `saveFeedState`, and `(*Service).Run` in `internal/feed/feed.go`.

One file per destination stores the bug ID cursor, news URL cursor, and tracked Telegram bug messages with rendered state, miss count, deterministic edit-failure count, and confirmation retry count. It is saved after each due poll and at feed shutdown. Legacy tracked `status` is migrated to `state: "<status>|"`.

Missing or successfully backed-up corrupt state starts empty, causing the next successful source fetch to baseline rather than replay history. An unreadable file, or a corrupt file whose backup failed, sets `writeDisabled`; later cycle and shutdown saves skip that path until restart. Save failure does not stop delivery or update rollback. A restart restores only the last successfully durable cursors and tracking.

## Legacy and generated files

**Implementation:** package `internal/store`, `loadLegacyAntispam` and `(*Settings).migrateLegacyAntispam` in `internal/store/settings.go`; package `internal/store`, `Load` and `ReclaimTemps` in `internal/store/json.go`.

`antispam.json` is migration input only. During an absent/v0/v1 settings migration, its global enabled flag and whitelist are copied into per-group `settings.json` overrides. No production function writes it, and a current schema-v2 settings file skips it. A malformed or unreadable legacy file during an applicable migration disables settings writes. The legacy source remains on disk.

`<name>.corrupt` is the preserved decode-failed input when its backup rename succeeds; it is never read automatically. If the rename fails, the original remains at `<name>` and writes to that path are disabled for the process. `.<name>.tmp-*` is an interrupted atomic write and is swept on startup when it matches the store pattern.

## What does not survive restart

**Implementation:** package `internal/verify`, `(*Service).Stats` in `internal/verify/state.go`; package `internal/panel`, `(*Panel).newSettingsSession` in `internal/panel/session.go`; package `main`, `main` in `cmd/gentoo-zh-verify-bot/main.go`.

Daily approval/decline counters, settings-panel sessions and drafts, admin positive-cache entries, DM and lookup rate-limit windows, lookup/package/news caches, in-flight cleanup timers, and transient alert throttles are memory-only. The cumulative tripwire tally is not part of daily stats and does survive through `agents.json`.

With `STATE_DIRECTORY` unset, every file in this document is absent and all ordinary runtime state is memory-only. Durable owner claim, enrollment capability, and runtime group registration fail rather than pretending to persist.
