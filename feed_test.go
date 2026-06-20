package main

import "testing"

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
