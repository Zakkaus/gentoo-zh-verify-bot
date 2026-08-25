# Feed polling and delivery

The feed service is optional. Each nonzero configured destination has independent cursors and an interval, while due destinations share upstream fetches when safe.

## Startup and polling schedule

**Implementation:** package `internal/feed`, `(*Service).Run` and `pollAllWithSources` in `internal/feed/feed.go`; package `internal/config`, `LoadConfig` and `(*FeedConfig).Interval` in `internal/config/config.go`.

`LoadConfig` merges the legacy singular `feed` entry into `feeds`. A destination with `chat_id=0` is disabled by startup, and later duplicate nonzero chat IDs are ignored because state is per chat. Feed language must be empty, `zh`, `zh-Hant`, or `en`; another nonempty value is a fatal configuration error.

`Run` loads one state object per destination, performs a best-effort permission probe, and polls immediately. The process ticker uses the shortest configured interval. Each destination is selected only when its own `nextDue` is reached, then receives its next deadline from the current poll’s start time. An unset/nonpositive interval is five minutes; 1–59 seconds is clamped to 60 seconds.

Due destinations with the same bug cursor share one recent-bug fetch. Different cursors fetch separately so a baselining destination cannot skip another destination’s backlog. News is fetched once for all due destinations. Tracked bug IDs are deduplicated and refetched once, in chunks of 50. Recent bugs and news each have a 30-second fetch context; every tracked chunk gets its own 30-second context. A failed chunk does not stop later chunks.

A polling panic is recovered and logged, and later ticker cycles continue. Source errors are log-only for that cycle. They do not stop the feed goroutine.

## Cursors, first poll, and ordering

**Implementation:** package `internal/feed`, `fetchRecentBugsWith`, `collectRecentBugs`, and `postFeedItems` in `internal/feed/feed.go`.

The bug cursor is the highest fully processed bug ID. With no cursor, Bugzilla is queried for its newest bug and the service records that ID without publishing history. Later queries request IDs strictly greater than the cursor in ascending order, at most 100. Duplicate and stale IDs are discarded. Product/component-filtered bugs count as processed and advance the cursor.

Bugs are posted in ascending ID order. A successful send advances the cursor and records the Telegram message ID. A permanently rejected item-specific post logs the bug ID, advances past it, and continues with the next bug. Transient failures and destination-level failures such as an unavailable chat or missing post rights stop at that bug without advancing, so later polls retry it. At most 100 bugs are processed per destination cycle, so a larger backlog drains as contiguous batches.

The news cursor is the last processed URL. First state records the first fetched URL without publishing history. When that URL is still present, newer entries are posted oldest first. If the URL has disappeared from the fetched page, the code cannot distinguish archive expiry from a reordered source; it re-baselines to the first URL and intentionally skips the uncertain entries.

A permanently rejected individual news post advances past that item. Other news send failures stop without advancing. The code assumes element zero of the parsed Gentoo news index is newest; repository code does not prove the upstream ordering.

## Posting and in-place edits

**Implementation:** package `internal/feed`, `postFeed`, `refreshTracked`, and `confirmNotice` in `internal/feed/feed.go`.

Every Telegram send/edit has a 15-second child context. New `UNCONFIRMED` bugs are silent; other open bugs notify unless `silent_bugs` is set. A bug first observed with a resolution is silent and uses a success marker only for `FIXED`, otherwise a closed-without-fix marker.

Every successfully posted bug with a nonzero message ID is tracked. A change to `status|resolution` edits the original message, including confirmation, resolution, reopening, and re-resolution. A transition from stored `UNCONFIRMED` to another open, notifying status also sends one non-silent confirmation reply. A direct transition to resolved only edits the original. A permanently rejected item-specific reply is abandoned immediately and state advances. Other reply failures leave the old state in place so the edit/reply is attempted again; after ten failed confirmation sends, the reply is abandoned and state advances.

Telegram’s “message is not modified” result counts as success. A known permanently uneditable message drops the tracking record immediately. Other deterministic 400 edit failures drop it after ten consecutive failures. Transport, timeout, cancellation, and 5xx failures retain tracking and reset that deterministic failure count. Successful edits update state; resolved bugs remain tracked so a later reopen can still edit them.

## Tracked-bug limits and eviction

**Implementation:** package `internal/feed`, `(*feedState).trackBug`, `(*feedState).evictOne`, and `refreshTracked` in `internal/feed/feed.go`.

Each destination tracks at most 200 bugs. Inserting at capacity evicts the numerically lowest resolved bug first; only when none is resolved does it evict the numerically lowest open bug. Malformed keys and null records are removed when encountered.

A complete refetch that omits a tracked ID increments its miss count. Ten consecutive complete-fetch misses evict it. If any tracked fetch chunk failed, absent IDs do not accrue misses, although records returned by successful chunks may still be edited.

At most 20 edits are attempted per destination per cycle. The tracked map is iterated in Go map order, so the selected subset is intentionally not documented as deterministic. Unattempted changes retain their old state for later cycles.

## Rate limits, pacing, and retry

**Implementation:** package `internal/feed`, `postFeed`, `postFeedItems`, and `refreshTracked` in `internal/feed/feed.go`; package `internal/tg`, `IsRateLimited` and `Pace` in `internal/tg/errors.go`.

Successful sends and non-rate-limited edit attempts are paced by one second. A Telegram 429 during tracked edits stops further tracked edits for that destination in the current cycle and leaves state unadvanced. A rate-limited confirmation reply also stops that refresh. A 429 during ordinary bug/news posting stops the current item loop, but subsequent phases in the same destination poll can still run.

There is no feed-level exponential backoff and no use of Telegram’s requested `retry_after` duration. Retained work normally retries at the destination’s next configured interval. The repository does not establish whether telego performs additional internal retries.

Fetch, parse, and transient Telegram failures leave the relevant cursor or tracked state unchanged. Bug and news posts skip errors classified as permanently item-specific. Destination-level failures remain retryable and do not advance the cursor.

## Persistence and shutdown

**Implementation:** package `internal/feed`, `loadFeedState`, `saveFeedState`, and `(*Service).Run` in `internal/feed/feed.go`; package `internal/store`, `Load` and `Write` in `internal/store/json.go`.

Each destination uses `feed-<chat_id>.json`. It stores `last_bug_id`, `last_news_url`, and tracked message IDs plus rendered state, miss count, deterministic edit-failure count, and confirmation retry count. Legacy tracked records containing only `status` are migrated in memory to `status|` and normalized on the next successful write.

State is saved after posting/editing each due destination and once more on cancellation. The main process waits at most five seconds for final feed shutdown. Save errors are logged by the store but ignored by feed code; delivery and in-memory cursors continue. A restart after an unwritable save restores the older cursor and can retry or re-baseline work according to that file.

Feed delivery therefore has an at-least-once window. Successful sends advance only the in-memory cursors until `saveFeedState` runs after the destination's entire cycle. If the process crashes between a send and that save, the durable cursor remains unchanged and a restart re-sends the successfully delivered prefix: up to the 100-bug per-cycle cap, plus any news items already posted in that cycle. This trade-off favors possible duplicates over silently skipping items.

Missing state starts empty. Corrupt JSON is renamed to `.corrupt` when possible and starts empty. Unlike the core verification-state loaders, an unreadable feed file does not latch writes off: feed code starts empty and calls `Write` again every cycle. Whether that later write succeeds depends on the underlying path. All writes use a same-directory temporary file, file `fsync`, atomic rename, and parent-directory `fsync`.
