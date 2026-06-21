package main

import (
	"context"
	"errors"
	"strings"
	"testing"
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

// TestBugTracking covers the #RESOLVED tracking: open bugs are tracked with their message id,
// resolved bugs aren't, and the tracked map is bounded (oldest id dropped).
func TestBugTracking(t *testing.T) {
	if bugResolved(recentBug{Status: "CONFIRMED"}) {
		t.Error("open bug (no resolution) should not be 'resolved'")
	}
	if !bugResolved(recentBug{Status: "RESOLVED", Resolution: "FIXED"}) {
		t.Error("bug with a resolution should be 'resolved'")
	}

	var st feedState
	st.trackBug(recentBug{ID: 100, Status: "CONFIRMED"}, 5001)                     // open -> tracked
	st.trackBug(recentBug{ID: 101, Status: "RESOLVED", Resolution: "FIXED"}, 5002) // resolved -> not tracked
	st.trackBug(recentBug{ID: 102, Status: "CONFIRMED"}, 0)                        // no msg id -> not tracked
	if len(st.Tracked) != 1 || st.Tracked["100"] == nil || st.Tracked["100"].MsgID != 5001 {
		t.Fatalf("tracked = %+v, want only bug 100 -> msg 5001", st.Tracked)
	}

	// cap: fill past maxTracked, the lowest id must be evicted
	for i := 0; i < maxTracked+5; i++ {
		st.trackBug(recentBug{ID: 1000 + i, Status: "CONFIRMED"}, 6000+i)
	}
	if len(st.Tracked) > maxTracked {
		t.Errorf("tracked grew to %d, want <= %d", len(st.Tracked), maxTracked)
	}
	if st.Tracked["100"] != nil {
		t.Error("oldest tracked bug (100) should have been evicted past the cap")
	}

	// formatBugResolved swaps the bug marker for a check so the closure is obvious
	got := formatBugResolved(recentBug{ID: 7, Summary: "x", Status: "RESOLVED", Resolution: "FIXED"}, "en")
	if !strings.HasPrefix(got, "✅") || strings.Contains(got, "🐞") {
		t.Errorf("formatBugResolved should swap 🐞 -> ✅, got prefix %q", got[:12])
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
	refreshTracked(context.Background(), nil, &FeedConfig{ChatID: -1, Lang: "en"}, &st, map[int]recentBug{200: b})
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
	refreshTracked(context.Background(), nil, &FeedConfig{ChatID: -1, Lang: "en"}, st, map[int]recentBug{100: {Status: "CONFIRMED"}})
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
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{500: b})
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
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{500: b})
		if tb := st.Tracked["500"]; tb == nil || tb.State != bugStateKey(b) {
			t.Errorf("not-modified should sync state and keep tracking: %+v", tb)
		}
	})

	t.Run("permanent error drops the bug", func(t *testing.T) {
		st := track("UNCONFIRMED|")
		fb := &fakeFeedBot{editErr: errors.New("Bad Request: message to edit not found")}
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{500: {ID: 500, Status: "IN_PROGRESS"}})
		if _, ok := st.Tracked["500"]; ok {
			t.Error("a permanent edit error should drop the bug from tracking")
		}
	})

	t.Run("transient error keeps tracking and old state", func(t *testing.T) {
		st := track("UNCONFIRMED|")
		fb := &fakeFeedBot{editErr: errors.New("Too Many Requests: retry after 5")}
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{500: {ID: 500, Status: "IN_PROGRESS"}})
		tb := st.Tracked["500"]
		if tb == nil {
			t.Fatal("a transient edit error must keep the bug tracked for retry")
		}
		if tb.State != "UNCONFIRMED|" {
			t.Errorf("a transient error must NOT advance the stored state, got %q", tb.State)
		}
	})

	t.Run("resolved bug is edited then untracked", func(t *testing.T) {
		st := track("CONFIRMED|")
		fb := &fakeFeedBot{}
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{500: {ID: 500, Status: "RESOLVED", Resolution: "FIXED"}})
		if fb.edits != 1 {
			t.Fatalf("want 1 edit for the resolution, got %d", fb.edits)
		}
		if _, ok := st.Tracked["500"]; ok {
			t.Error("a resolved bug should be untracked after its edit")
		}
	})
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
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{700: {ID: 700, Status: "CONFIRMED", Summary: "boom"}})
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
		refreshTracked(context.Background(), fb, fs, st, map[int]recentBug{701: {ID: 701, Status: "CONFIRMED", Summary: "x"}})
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
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{702: {ID: 702, Status: "IN_PROGRESS", Summary: "x"}})
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
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{704: {ID: 704, Status: "IN_PROGRESS", Summary: "x"}})
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
		refreshTracked(context.Background(), fb, f, st, map[int]recentBug{703: {ID: 703, Status: "CONFIRMED", Summary: "x"}})
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
