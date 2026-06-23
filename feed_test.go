package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mymmrac/telego"
)

// TestBugSilent verifies status-aware notifications: UNCONFIRMED bugs post silently (a
// fresh report may be a false alarm), confirmed bugs notify, and silent_bugs=true forces
// every bug silent.
func TestBugSilent(t *testing.T) {
	f := &FeedConfig{}
	for _, c := range []struct {
		status string
		want   bool // want silent
	}{
		{"UNCONFIRMED", true},
		{"CONFIRMED", false},
		{"IN_PROGRESS", false},
		{"RESOLVED", false},
		{"VERIFIED", false},
	} {
		if got := f.bugSilent(recentBug{Status: c.status}); got != c.want {
			t.Errorf("bugSilent(%s) = %v, want %v", c.status, got, c.want)
		}
	}
	forced := true
	if !(&FeedConfig{SilentBugs: &forced}).bugSilent(recentBug{Status: "CONFIRMED"}) {
		t.Errorf("silent_bugs=true should force a CONFIRMED bug silent")
	}
}

// TestFormatNewBug guards the born-resolved case: a bug already resolved the first time the feed
// sees it (filed + closed within one poll, e.g. RESOLVED/INVALID) must render ✅ (not 🐞) and be
// posted silently; an open bug keeps 🐞 and the caller's status-aware silence.
func TestFormatNewBug(t *testing.T) {
	text, silent := formatNewBug(recentBug{ID: 1, Summary: "x", Status: "CONFIRMED"}, "en", false)
	if !strings.Contains(text, "🐞") || silent {
		t.Errorf("an open new bug should be 🐞 and not forced silent (silent=%v)", silent)
	}
	text, silent = formatNewBug(recentBug{ID: 2, Summary: "x", Status: "RESOLVED", Resolution: "INVALID"}, "en", false)
	if strings.Contains(text, "🐞") || !strings.Contains(text, "❌") || !silent {
		t.Errorf("a born-resolved INVALID (误报) bug should be ❌ and silent (silent=%v)", silent)
	}
	text, silent = formatNewBug(recentBug{ID: 3, Summary: "x", Status: "RESOLVED", Resolution: "FIXED"}, "en", false)
	if !strings.Contains(text, "✅") || strings.Contains(text, "🐞") || !silent {
		t.Errorf("a born-resolved FIXED bug should be ✅ and silent (silent=%v)", silent)
	}
}

// TestResolvedMark: only an actually-FIXED bug gets ✅; everything else closed (INVALID 误报,
// WONTFIX, DUPLICATE, WORKSFORME, …) gets ❌. Case-insensitive on the resolution.
func TestResolvedMark(t *testing.T) {
	if resolvedMark(recentBug{Resolution: "FIXED"}) != "✅" || resolvedMark(recentBug{Resolution: "fixed"}) != "✅" {
		t.Error("FIXED (any case) should be ✅")
	}
	for _, r := range []string{"INVALID", "WONTFIX", "DUPLICATE", "WORKSFORME", "OBSOLETE", ""} {
		if got := resolvedMark(recentBug{Resolution: r}); got != "❌" {
			t.Errorf("resolution %q should be ❌, got %s", r, got)
		}
	}
}

// TestNewsCursorMonotonic guards the news-feed dedup against the "cursor scrolled off the
// page -> re-broadcast the whole archive" bug: when the stored cursor URL is no longer in
// the fetched list, nothing is re-posted and the cursor re-baselines to the newest item.
// This mirrors the !found path in postFeedItems.
func TestNewsCursorMonotonic(t *testing.T) {
	news := []newsItem{{url: "u5"}, {url: "u4"}, {url: "u3"}, {url: "u2"}, {url: "u1"}}

	// Helper replicating the dedup window + the cursor-lost guard.
	window := func(cursor string) (toPost int, newCursor string) {
		found := false
		var nn []newsItem
		for _, n := range news {
			if n.url == cursor {
				found = true
				break
			}
			nn = append(nn, n)
		}
		if !found {
			return 0, news[0].url // cursor lost: post nothing, re-baseline
		}
		return len(nn), cursor
	}

	if n, c := window("GONE"); n != 0 || c != "u5" {
		t.Errorf("cursor lost: posted=%d cursor=%q, want 0 and re-baseline to u5", n, c)
	}
	if n, _ := window("u3"); n != 2 { // u5,u4 are newer than the cursor u3
		t.Errorf("normal window: posted=%d, want 2", n)
	}
	if n, _ := window("u5"); n != 0 { // already at newest
		t.Errorf("at newest: posted=%d, want 0", n)
	}
}

// TestBugCursorForwardOnly guards against the bug-feed cursor regressing: it must only ever
// advance forward, so a transiently lower max bug id (e.g. newest bugs hidden) can't make
// it re-post older bugs when they reappear. Mirrors the guarded advance in postFeedItems.
func TestBugCursorForwardOnly(t *testing.T) {
	advance := func(cursor, newestFetched int) int {
		if newestFetched > cursor {
			return newestFetched
		}
		return cursor
	}
	if got := advance(100, 98); got != 100 {
		t.Errorf("cursor regressed to %d, want it to stay 100", got)
	}
	if got := advance(100, 105); got != 105 {
		t.Errorf("cursor should advance to 105, got %d", got)
	}
}

// TestBugTracking covers tracking: open AND born-resolved bugs are tracked (so a later reopen can
// re-render), a bug with no msg id is not, and the cap evicts a RESOLVED bug before any open one.
func TestBugTracking(t *testing.T) {
	if bugResolved(recentBug{Status: "CONFIRMED"}) {
		t.Error("open bug (no resolution) should not be 'resolved'")
	}
	if !bugResolved(recentBug{Status: "RESOLVED", Resolution: "FIXED"}) {
		t.Error("bug with a resolution should be 'resolved'")
	}

	var st feedState
	st.trackBug(recentBug{ID: 100, Status: "CONFIRMED"}, 5001)                     // open -> tracked
	st.trackBug(recentBug{ID: 101, Status: "RESOLVED", Resolution: "FIXED"}, 5002) // born-resolved -> ALSO tracked (for a later reopen)
	st.trackBug(recentBug{ID: 102, Status: "CONFIRMED"}, 0)                        // no msg id -> not tracked
	if len(st.Tracked) != 2 || st.Tracked["100"].MsgID != 5001 || st.Tracked["101"].MsgID != 5002 {
		t.Fatalf("tracked = %+v, want bugs 100 and 101 tracked", st.Tracked)
	}
	if st.Tracked["102"] != nil {
		t.Error("a bug with msg id 0 must not be tracked")
	}

	// resolved-first eviction: fill to exactly the cap, then one more add must evict the RESOLVED
	// bug (101) and keep the older OPEN bug (100) — a long-lived open bug shouldn't be lost first.
	for i := 0; i < maxTracked-2; i++ {
		st.trackBug(recentBug{ID: 3000 + i, Status: "CONFIRMED"}, 7000+i)
	}
	st.trackBug(recentBug{ID: 9999, Status: "CONFIRMED"}, 8000) // forces a single eviction
	if len(st.Tracked) > maxTracked {
		t.Errorf("tracked grew to %d, want <= %d", len(st.Tracked), maxTracked)
	}
	if st.Tracked["101"] != nil {
		t.Error("resolved-first eviction: the resolved bug (101) must be evicted before any open bug")
	}
	if st.Tracked["100"] == nil {
		t.Error("resolved-first eviction: the open bug (100) must survive while a resolved one remains to evict")
	}

	got := formatBugResolved(recentBug{ID: 7, Summary: "x", Status: "RESOLVED", Resolution: "FIXED"}, "en")
	if !strings.HasPrefix(got, "✅") || strings.Contains(got, "🐞") {
		t.Errorf("formatBugResolved should render ✅, got prefix %q", got[:12])
	}
}

// TestResolvedState covers the persisted-state-key resolved check that drives resolved-first eviction.
func TestResolvedState(t *testing.T) {
	for _, s := range []string{"RESOLVED|FIXED", "VERIFIED|INVALID", "RESOLVED|WONTFIX"} {
		if !resolvedState(s) {
			t.Errorf("%q has a resolution -> should be resolved", s)
		}
	}
	for _, s := range []string{"CONFIRMED|", "UNCONFIRMED|", "IN_PROGRESS|", "CONFIRMED"} {
		if resolvedState(s) {
			t.Errorf("%q has no resolution -> should be open", s)
		}
	}
}

// TestEditErrClassification: "chat not found" is now TRANSIENT (a blip shouldn't drop every changed
// bug); genuine per-message errors stay permanent; and 429s are detected as rate-limited.
func TestEditErrClassification(t *testing.T) {
	if permanentEditErr(errors.New("Bad Request: chat not found")) {
		t.Error("'chat not found' must NOT be permanent (it's usually a transient blip)")
	}
	for _, s := range []string{"message to edit not found", "message can't be edited", "MESSAGE_ID_INVALID"} {
		if !permanentEditErr(errors.New("Bad Request: " + s)) {
			t.Errorf("%q should be a permanent edit error", s)
		}
	}
	for _, s := range []string{"Too Many Requests: retry after 30", "too many requests"} {
		if !isRateLimited(errors.New(s)) {
			t.Errorf("%q should be detected as rate-limited", s)
		}
	}
	if isRateLimited(errors.New("Bad Gateway")) {
		t.Error("a non-429 error must not be detected as rate-limited")
	}
}

// TestBugStateKey covers the status+resolution state key that drives in-place edits, and that an
// unchanged state doesn't attempt an edit (a matching bug must be skipped, not re-rendered).
func TestBugStateKey(t *testing.T) {
	if bugStateKey(recentBug{Status: "UNCONFIRMED"}) == bugStateKey(recentBug{Status: "CONFIRMED"}) {
		t.Error("UNCONFIRMED vs CONFIRMED must have distinct state keys (so a confirm triggers an edit)")
	}
	if bugStateKey(recentBug{Status: "RESOLVED", Resolution: "FIXED"}) == bugStateKey(recentBug{Status: "RESOLVED", Resolution: "WONTFIX"}) {
		t.Error("different resolutions must have distinct state keys")
	}
	var st feedState
	b := recentBug{ID: 200, Status: "UNCONFIRMED"}
	st.trackBug(b, 7001)
	if st.Tracked["200"] == nil || st.Tracked["200"].State != bugStateKey(b) {
		t.Fatalf("trackBug should store the state key, got %+v", st.Tracked["200"])
	}
	// Unchanged state in the refresh batch => skipped before any edit (nil bot is safe only
	// because no edit is attempted); the bug stays tracked.
	refreshTracked(context.Background(), nil, &FeedConfig{ChatID: -1, Lang: "en"}, &st, map[int]recentBug{200: b}, true)
	if st.Tracked["200"] == nil {
		t.Error("an unchanged bug must stay tracked (no edit, no drop)")
	}
}

// TestCapRunesAndNilTracked covers the rune-safe truncation and the nil-tracked-entry guard
// (a hand-edited state file with a null entry must not crash refreshTracked).
func TestCapRunesAndNilTracked(t *testing.T) {
	if got := capRunes("abcdef", 4); got != "abc…" {
		t.Errorf("capRunes(abcdef,4) = %q, want abc…", got)
	}
	if got := capRunes("ab", 4); got != "ab" {
		t.Errorf("capRunes short = %q, want ab", got)
	}
	if got := capRunes(strings.Repeat("包", 10), 4); !utf8.ValidString(got) {
		t.Errorf("capRunes produced invalid UTF-8: %q", got)
	}
	st := &feedState{Tracked: map[string]*trackedBug{"100": nil}}
	refreshTracked(context.Background(), nil, &FeedConfig{ChatID: -1, Lang: "en"}, st, map[int]recentBug{100: {Status: "CONFIRMED"}}, true)
	if _, ok := st.Tracked["100"]; ok {
		t.Error("a nil tracked entry should be dropped (not panic)")
	}
}

// fakeFeedBot is a feedBot stand-in so refreshTracked / postFeed edit & send branches can be
// exercised without a real Telegram connection. editErr (when set) is returned by
// EditMessageText; every SendMessage is recorded so a confirm-ping can be asserted.
type fakeFeedBot struct {
	editErr        error
	sendErr        error
	edits          int
	sends          int
	sentText       []string
	sentSilent     []bool
	sentReplyTo    []int
	sentReplyAllow []bool
}

func (b *fakeFeedBot) EditMessageText(_ context.Context, _ *telego.EditMessageTextParams) (*telego.Message, error) {
	b.edits++
	if b.editErr != nil {
		return nil, b.editErr
	}
	return &telego.Message{MessageID: 1}, nil
}

func (b *fakeFeedBot) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	b.sends++
	b.sentText = append(b.sentText, p.Text)
	b.sentSilent = append(b.sentSilent, p.DisableNotification)
	rt, allowWithout := 0, false
	if p.ReplyParameters != nil {
		rt = p.ReplyParameters.MessageID
		allowWithout = p.ReplyParameters.AllowSendingWithoutReply
	}
	b.sentReplyTo = append(b.sentReplyTo, rt)
	b.sentReplyAllow = append(b.sentReplyAllow, allowWithout)
	if b.sendErr != nil {
		return nil, b.sendErr
	}
	return &telego.Message{MessageID: 100 + b.sends}, nil
}

// TestRefreshTrackedEditBranches drives the real EditMessageText result branches via a fake:
// a successful edit syncs state and keeps tracking; "message is not modified" counts as success;
// a permanent error drops the bug; a transient error keeps it tracked with its old state; and a
// resolved bug is edited then untracked. None of these transitions trips the confirm-ping.
func TestRefreshTrackedEditBranches(t *testing.T) {
	feedSendPause = 0 // never sleep in tests
	f := &FeedConfig{ChatID: -100, Lang: "en"}
	track := func(state string) *feedState {
		return &feedState{Tracked: map[string]*trackedBug{"500": {MsgID: 42, State: state}}}
	}

	t.Run("success syncs state and keeps tracking", func(t *testing.T) {
		st := track("CONFIRMED|") // non-UNCONFIRMED origin: isolates edit-success without a confirm ping
		fb := &fakeFeedBot{}
		b := recentBug{ID: 500, Status: "IN_PROGRESS"}
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{500: b}, true)
		if fb.edits != 1 {
			t.Fatalf("want 1 edit, got %d", fb.edits)
		}
		if tb := st.Tracked["500"]; tb == nil || tb.State != bugStateKey(b) {
			t.Errorf("state not synced after a successful edit: %+v", tb)
		}
		if fb.sends != 0 {
			t.Errorf("a non-UNCONFIRMED-origin transition must not ping, got %d sends", fb.sends)
		}
	})

	t.Run("not-modified is treated as success", func(t *testing.T) {
		st := track("CONFIRMED|")
		fb := &fakeFeedBot{editErr: errors.New("Bad Request: message is not modified")}
		b := recentBug{ID: 500, Status: "IN_PROGRESS"}
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{500: b}, true)
		if tb := st.Tracked["500"]; tb == nil || tb.State != bugStateKey(b) {
			t.Errorf("not-modified should sync state and keep tracking: %+v", tb)
		}
	})

	t.Run("permanent error drops the bug", func(t *testing.T) {
		st := track("UNCONFIRMED|")
		fb := &fakeFeedBot{editErr: errors.New("Bad Request: message to edit not found")}
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{500: {ID: 500, Status: "IN_PROGRESS"}}, true)
		if _, ok := st.Tracked["500"]; ok {
			t.Error("a permanent edit error should drop the bug from tracking")
		}
	})

	t.Run("non-rate-limit transient keeps tracking, old state, counts a fail", func(t *testing.T) {
		st := track("UNCONFIRMED|")
		fb := &fakeFeedBot{editErr: errors.New("Bad Gateway")} // transient but NOT a 429
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{500: {ID: 500, Status: "IN_PROGRESS"}}, true)
		tb := st.Tracked["500"]
		if tb == nil {
			t.Fatal("a transient edit error must keep the bug tracked for retry")
		}
		if tb.State != "UNCONFIRMED|" {
			t.Errorf("a transient error must NOT advance the stored state, got %q", tb.State)
		}
		if tb.EditFails != 1 {
			t.Errorf("a non-rate-limit transient should count one edit failure, got %d", tb.EditFails)
		}
	})

	t.Run("resolved bug is edited and KEPT tracked for a later reopen", func(t *testing.T) {
		st := track("CONFIRMED|")
		fb := &fakeFeedBot{}
		b := recentBug{ID: 500, Status: "RESOLVED", Resolution: "FIXED"}
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{500: b}, true)
		if fb.edits != 1 {
			t.Fatalf("want 1 edit for the resolution, got %d", fb.edits)
		}
		tb := st.Tracked["500"]
		if tb == nil || tb.State != bugStateKey(b) {
			t.Errorf("a resolved bug must be KEPT tracked with its resolved state (for a later reopen): %+v", tb)
		}
	})
}

// TestRefreshTrackedRateLimitStops: a 429 stops the cycle after the first attempt (rather than
// hammering); the unattempted bugs keep their old state and 0 EditFails (retried next cycle).
func TestRefreshTrackedRateLimitStops(t *testing.T) {
	feedSendPause = 0
	f := &FeedConfig{ChatID: -100, Lang: "en"}
	st := &feedState{Tracked: map[string]*trackedBug{
		"800": {MsgID: 1, State: "CONFIRMED|"},
		"801": {MsgID: 2, State: "CONFIRMED|"},
	}}
	fb := &fakeFeedBot{editErr: errors.New("Too Many Requests: retry after 30")}
	byID := map[int]recentBug{800: {ID: 800, Status: "IN_PROGRESS"}, 801: {ID: 801, Status: "IN_PROGRESS"}}
	refreshTracked(context.Background(), fb, f, st, byID, true)
	if fb.edits != 1 {
		t.Errorf("a 429 must stop the cycle after one attempt, got %d edits", fb.edits)
	}
	for _, id := range []string{"800", "801"} {
		if tb := st.Tracked[id]; tb == nil || tb.State != "CONFIRMED|" || tb.EditFails != 0 {
			t.Errorf("bug %s should stay tracked, old state, 0 EditFails after a 429: %+v", id, tb)
		}
	}
}

// TestRefreshTrackedEditCap: when more tracked bugs changed than maxEditsPerCycle, only that many
// edits fire this call (the backlog drains over later cycles, never bursting past the rate limit).
func TestRefreshTrackedEditCap(t *testing.T) {
	feedSendPause = 0
	f := &FeedConfig{ChatID: -100, Lang: "en"}
	st := &feedState{Tracked: map[string]*trackedBug{}}
	byID := map[int]recentBug{}
	for i := 0; i < maxEditsPerCycle+5; i++ {
		id := 1000 + i
		st.Tracked[strconv.Itoa(id)] = &trackedBug{MsgID: id, State: "CONFIRMED|"}
		byID[id] = recentBug{ID: id, Status: "IN_PROGRESS"} // all changed (and not a ping transition)
	}
	fb := &fakeFeedBot{}
	refreshTracked(context.Background(), fb, f, st, byID, true)
	if fb.edits != maxEditsPerCycle {
		t.Errorf("edit cap: want exactly %d edits, got %d", maxEditsPerCycle, fb.edits)
	}
}

// TestRefreshTrackedMissDrop: a bug absent from a non-empty refetch (vanished from Bugzilla) is
// dropped only after maxTrackMisses consecutive misses — never edited in the meantime.
func TestRefreshTrackedMissDrop(t *testing.T) {
	feedSendPause = 0
	f := &FeedConfig{ChatID: -100, Lang: "en"}
	st := &feedState{Tracked: map[string]*trackedBug{"900": {MsgID: 1, State: "CONFIRMED|"}}}
	fb := &fakeFeedBot{}
	other := map[int]recentBug{12345: {ID: 12345, Status: "CONFIRMED"}} // non-empty, but 900 absent
	for i := 1; i < maxTrackMisses; i++ {
		refreshTracked(context.Background(), fb, f, st, other, true)
		if st.Tracked["900"] == nil {
			t.Fatalf("dropped too early after %d misses", i)
		}
		if st.Tracked["900"].Misses != i {
			t.Errorf("after %d cycles Misses=%d, want %d", i, st.Tracked["900"].Misses, i)
		}
	}
	refreshTracked(context.Background(), fb, f, st, other, true) // maxTrackMisses-th miss
	if st.Tracked["900"] != nil {
		t.Errorf("bug 900 should be dropped after %d consecutive misses", maxTrackMisses)
	}
	if fb.edits != 0 {
		t.Errorf("a missing bug must never be edited, got %d", fb.edits)
	}
}

// TestRefreshTrackedEditFailDrop: a bug whose edit keeps failing with a non-rate-limit transient
// error is dropped after maxEditFails consecutive failures, so it can't burn an edit forever.
func TestRefreshTrackedEditFailDrop(t *testing.T) {
	feedSendPause = 0
	f := &FeedConfig{ChatID: -100, Lang: "en"}
	st := &feedState{Tracked: map[string]*trackedBug{"950": {MsgID: 1, State: "CONFIRMED|"}}}
	fb := &fakeFeedBot{editErr: errors.New("Bad Gateway")} // transient, not a 429
	b := map[int]recentBug{950: {ID: 950, Status: "IN_PROGRESS"}}
	for i := 1; i < maxEditFails; i++ {
		refreshTracked(context.Background(), fb, f, st, b, true)
		if st.Tracked["950"] == nil {
			t.Fatalf("dropped too early after %d fails", i)
		}
	}
	refreshTracked(context.Background(), fb, f, st, b, true) // maxEditFails-th failure
	if st.Tracked["950"] != nil {
		t.Errorf("bug 950 should be dropped after %d consecutive edit failures", maxEditFails)
	}
}

// TestRefreshTrackedReopenReRenders: a bug already tracked as resolved (INVALID) that is reopened
// and re-resolved (FIXED) must re-edit and stay tracked — impossible before resolved bugs were kept.
func TestRefreshTrackedReopenReRenders(t *testing.T) {
	feedSendPause = 0
	f := &FeedConfig{ChatID: -100, Lang: "en"}
	st := &feedState{Tracked: map[string]*trackedBug{"600": {MsgID: 1, State: "RESOLVED|INVALID"}}}
	fb := &fakeFeedBot{}
	b := recentBug{ID: 600, Status: "RESOLVED", Resolution: "FIXED"}
	refreshTracked(context.Background(), fb, f, st, map[int]recentBug{600: b}, true)
	if fb.edits != 1 {
		t.Fatalf("a resolution flip (INVALID->FIXED) must re-edit, got %d edits", fb.edits)
	}
	if tb := st.Tracked["600"]; tb == nil || tb.State != bugStateKey(b) {
		t.Errorf("after a flip the bug must stay tracked with the new state: %+v", tb)
	}
}

// TestLoadFeedStateCorruptBackup: a corrupt state file is renamed to .corrupt (preserved for
// inspection) rather than silently clobbered, and loads as empty.
func TestLoadFeedStateCorruptBackup(t *testing.T) {
	dir := t.TempDir()
	path := feedStatePath(dir, -42)
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := loadFeedState(path)
	if st.LastBugID != 0 || len(st.Tracked) != 0 {
		t.Errorf("a corrupt state must load as empty, got %+v", st)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("the corrupt file should be backed up to %s.corrupt: %v", path, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the corrupt file should have been renamed away from the live path")
	}
}

// TestPaceFeed: pacing returns immediately when disabled, and bails out (false) on a cancelled ctx
// so a shutdown isn't held up by the pause (which would defeat the final state flush).
func TestPaceFeed(t *testing.T) {
	feedSendPause = 0
	if !paceFeed(context.Background()) {
		t.Error("with feedSendPause<=0 paceFeed should return true immediately")
	}
	feedSendPause = time.Hour // only a ctx cancel can return in time
	defer func() { feedSendPause = 0 }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if paceFeed(ctx) {
		t.Error("paceFeed must return false on a cancelled ctx (so shutdown isn't delayed)")
	}
}

// TestRefreshTrackedPartialFetchNoMiss locks in finding B's fix: when the refetch was incomplete
// (fetchOK=false, a chunk failed), an absent tracked bug must NOT accrue a miss — it could have been
// in the failed chunk — so a flaky chunk can never age out a live bug.
func TestRefreshTrackedPartialFetchNoMiss(t *testing.T) {
	feedSendPause = 0
	f := &FeedConfig{ChatID: -100, Lang: "en"}
	st := &feedState{Tracked: map[string]*trackedBug{"900": {MsgID: 1, State: "CONFIRMED|"}}}
	fb := &fakeFeedBot{}
	other := map[int]recentBug{12345: {ID: 12345, Status: "CONFIRMED"}} // 900 absent
	for i := 0; i < maxTrackMisses+3; i++ {
		refreshTracked(context.Background(), fb, f, st, other, false) // fetchOK=false every cycle
	}
	tb := st.Tracked["900"]
	if tb == nil {
		t.Fatal("a partial-fetch failure must NOT drop a tracked bug")
	}
	if tb.Misses != 0 {
		t.Errorf("a partial-fetch absence must not count a miss, got Misses=%d", tb.Misses)
	}
}

// TestRefreshTrackedConfirmPing covers the UNCONFIRMED->CONFIRMED confirm ping: a fresh
// non-silent notice is sent (on top of the in-place edit), silent_bugs suppresses it, and a
// transition that does not originate from UNCONFIRMED does not ping.
func TestRefreshTrackedConfirmPing(t *testing.T) {
	feedSendPause = 0
	f := &FeedConfig{ChatID: -100, Lang: "en"}

	t.Run("UNCONFIRMED->CONFIRMED sends one non-silent ping", func(t *testing.T) {
		st := &feedState{Tracked: map[string]*trackedBug{"700": {MsgID: 9, State: "UNCONFIRMED|"}}}
		fb := &fakeFeedBot{}
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{700: {ID: 700, Status: "CONFIRMED", Summary: "boom"}}, true)
		if fb.edits != 1 {
			t.Fatalf("the in-place edit must still happen, got %d edits", fb.edits)
		}
		if fb.sends != 1 {
			t.Fatalf("UNCONFIRMED->CONFIRMED should ping exactly once, got %d sends", fb.sends)
		}
		if fb.sentSilent[0] {
			t.Error("the confirm ping must be NON-silent")
		}
		if !strings.Contains(fb.sentText[0], "Bug 700") {
			t.Errorf("the confirm ping should reference the bug, got %q", fb.sentText[0])
		}
		if fb.sentReplyTo[0] != 9 {
			t.Errorf("the confirm ping should reply to the original bug message (id 9), got %d", fb.sentReplyTo[0])
		}
		if !fb.sentReplyAllow[0] {
			t.Error("the confirm-ping reply should set AllowSendingWithoutReply so a deleted original doesn't block it")
		}
	})

	t.Run("silent_bugs=true suppresses the ping", func(t *testing.T) {
		forced := true
		fs := &FeedConfig{ChatID: -100, Lang: "en", SilentBugs: &forced}
		st := &feedState{Tracked: map[string]*trackedBug{"701": {MsgID: 9, State: "UNCONFIRMED|"}}}
		fb := &fakeFeedBot{}
		refreshTracked(context.Background(), fb, fs, st, map[int]recentBug{701: {ID: 701, Status: "CONFIRMED", Summary: "x"}}, true)
		if fb.edits != 1 {
			t.Fatalf("the edit must still happen under silent_bugs, got %d edits", fb.edits)
		}
		if fb.sends != 0 {
			t.Errorf("silent_bugs=true must not ping, got %d sends", fb.sends)
		}
	})

	t.Run("CONFIRMED->IN_PROGRESS does not ping", func(t *testing.T) {
		st := &feedState{Tracked: map[string]*trackedBug{"702": {MsgID: 9, State: "CONFIRMED|"}}}
		fb := &fakeFeedBot{}
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{702: {ID: 702, Status: "IN_PROGRESS", Summary: "x"}}, true)
		if fb.edits != 1 {
			t.Fatalf("want the edit, got %d", fb.edits)
		}
		if fb.sends != 0 {
			t.Errorf("a transition not from UNCONFIRMED must not ping, got %d sends", fb.sends)
		}
	})

	t.Run("UNCONFIRMED->IN_PROGRESS pings (raced past CONFIRMED)", func(t *testing.T) {
		st := &feedState{Tracked: map[string]*trackedBug{"704": {MsgID: 9, State: "UNCONFIRMED|"}}}
		fb := &fakeFeedBot{}
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{704: {ID: 704, Status: "IN_PROGRESS", Summary: "x"}}, true)
		if fb.edits != 1 || fb.sends != 1 {
			t.Fatalf("a bug leaving UNCONFIRMED (even straight to IN_PROGRESS) must ping once: edits=%d sends=%d", fb.edits, fb.sends)
		}
		if fb.sentSilent[0] {
			t.Error("the confirm ping must be non-silent")
		}
	})

	t.Run("a failed confirm ping does not advance state (retries next cycle)", func(t *testing.T) {
		st := &feedState{Tracked: map[string]*trackedBug{"703": {MsgID: 9, State: "UNCONFIRMED|"}}}
		fb := &fakeFeedBot{sendErr: errors.New("Too Many Requests: retry after 5")}
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{703: {ID: 703, Status: "CONFIRMED", Summary: "x"}}, true)
		if fb.edits != 1 || fb.sends != 1 {
			t.Fatalf("want 1 edit + 1 attempted ping, got edits=%d sends=%d", fb.edits, fb.sends)
		}
		if tb := st.Tracked["703"]; tb == nil || tb.State != "UNCONFIRMED|" {
			t.Errorf("a failed ping must NOT advance state (so the transition retries), got %+v", tb)
		}
	})
}

// TestConfirmNotice guards the confirm-notice wording: it names the bug's ACTUAL status
// (localized in zh, raw in en), never always "confirmed", and falls back to the raw status for an
// unmapped value.
func TestConfirmNotice(t *testing.T) {
	if got := confirmNotice(recentBug{ID: 5, Status: "IN_PROGRESS"}, "en"); !strings.Contains(got, "IN_PROGRESS") {
		t.Errorf("en IN_PROGRESS notice should name the status, got %q", got)
	}
	if got := confirmNotice(recentBug{ID: 5, Status: "IN_PROGRESS"}, "zh"); !strings.Contains(got, "处理中") {
		t.Errorf("zh IN_PROGRESS notice should localize the status, got %q", got)
	}
	if got := confirmNotice(recentBug{ID: 5, Status: "CONFIRMED"}, "en"); !strings.Contains(got, "CONFIRMED") {
		t.Errorf("en CONFIRMED notice should name the status, got %q", got)
	}
	if got := confirmNotice(recentBug{ID: 5, Status: "WEIRD_STATE"}, "en"); !strings.Contains(got, "WEIRD_STATE") {
		t.Errorf("an unmapped status should fall back to the raw value, got %q", got)
	}
}

// TestFeedStateMigration covers the load-time upgrade of pre-v3.4.3 state: a tracked bug that
// carried only the legacy `status` is folded into the current `state` key, while an
// already-current entry is left untouched.
func TestFeedStateMigration(t *testing.T) {
	st := &feedState{Tracked: map[string]*trackedBug{
		"300": {MsgID: 5, Status: "UNCONFIRMED"}, // legacy: status only, no state
		"301": {MsgID: 6, State: "CONFIRMED|"},   // already current
		"302": nil,                               // a hand-edited null entry must not panic
	}}
	migrateFeedState(st)
	if tb := st.Tracked["300"]; tb.State != "UNCONFIRMED|" || tb.Status != "" {
		t.Errorf("legacy status not migrated to state: %+v", tb)
	}
	if tb := st.Tracked["301"]; tb.State != "CONFIRMED|" {
		t.Errorf("an already-current entry must be left intact: %+v", tb)
	}
}

// TestFeedStateRoundTrip proves feed cursors and tracked message state survive a save/load cycle
// (the persistence the review flagged as untested).
func TestFeedStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := feedStatePath(dir, -100200300)
	saveFeedState(path, feedState{
		LastBugID: 999,
		Tracked:   map[string]*trackedBug{"500": {MsgID: 42, State: "CONFIRMED|"}},
	})
	got := loadFeedState(path)
	if got.LastBugID != 999 {
		t.Errorf("LastBugID lost across save/load: %d", got.LastBugID)
	}
	if tb := got.Tracked["500"]; tb == nil || tb.MsgID != 42 || tb.State != "CONFIRMED|" {
		t.Errorf("tracked state did not survive save/load: %+v", tb)
	}
}

// TestFeedPostBlocked covers the startup permission classifier: channels need an admin with
// post rights; groups only need the bot to be present and not muted.
func TestFeedPostBlocked(t *testing.T) {
	for _, c := range []struct {
		name     string
		chatType string
		member   telego.ChatMember
		blocked  bool
	}{
		{"channel admin can post", "channel", &telego.ChatMemberAdministrator{CanPostMessages: true}, false},
		{"channel admin without post right", "channel", &telego.ChatMemberAdministrator{CanPostMessages: false}, true},
		{"channel plain member", "channel", &telego.ChatMemberMember{}, true},
		{"channel owner", "channel", &telego.ChatMemberOwner{}, false},
		{"supergroup member ok", "supergroup", &telego.ChatMemberMember{}, false},
		{"group restricted+muted", "supergroup", &telego.ChatMemberRestricted{CanSendMessages: false}, true},
		{"group restricted can send", "supergroup", &telego.ChatMemberRestricted{CanSendMessages: true}, false},
		{"left a channel", "channel", &telego.ChatMemberLeft{}, true},
		{"banned from a group", "supergroup", &telego.ChatMemberBanned{}, true},
	} {
		if got := feedPostBlocked(c.chatType, c.member) != ""; got != c.blocked {
			t.Errorf("%s: blocked=%v, want %v (reason=%q)", c.name, got, c.blocked, feedPostBlocked(c.chatType, c.member))
		}
	}
}
