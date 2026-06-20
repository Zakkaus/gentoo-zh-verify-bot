package main

import (
	"testing"
	"time"
)

// TestDeriveDebianStatus verifies the role mapping is derived from release dates (not
// hardcoded): with Trixie/13 released and Forky/14 not yet, 13 is stable and 14 testing;
// when 14 later releases, the mapping shifts automatically (second sub-test).
func TestDeriveDebianStatus(t *testing.T) {
	csv := `version,codename,series,created,release,eol
11,Bullseye,bullseye,2019-07-06,2021-08-14,2024-08-14
12,Bookworm,bookworm,2021-08-14,2023-06-10,2026-07-11
13,Trixie,trixie,2023-06-10,2025-08-09,2028-08-09
14,Forky,forky,2025-08-09
,Sid,sid,1993-08-16`

	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC) // Trixie released, Forky not
	got := deriveDebianStatus(csv, now)
	for ver, want := range map[string]string{"13": "stable", "12": "oldstable", "11": "oldoldstable", "14": "testing"} {
		if got[ver] != want {
			t.Errorf("now=2026: status[%s] = %q, want %q", ver, got[ver], want)
		}
	}

	// After Forky releases (its row now carries a release date), stable becomes 14 with no
	// code change — the mapping is purely date-driven.
	csv2 := `version,codename,series,created,release,eol
12,Bookworm,bookworm,2021-08-14,2023-06-10,2026-07-11
13,Trixie,trixie,2023-06-10,2025-08-09,2028-08-09
14,Forky,forky,2025-08-09,2026-08-01,2029-08-01
15,Duke,duke,2027-08-01
,Sid,sid,1993-08-16`
	later := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if g := deriveDebianStatus(csv2, later); g["14"] != "stable" || g["13"] != "oldstable" {
		t.Errorf("after Forky release: 14=%q 13=%q, want stable/oldstable", g["14"], g["13"])
	}
}

// TestUbuntuExcluded verifies the stable-line exclusion: an unreleased series (future date),
// proposed/backports pockets, and a series past standard EOL (18.04/20.04) are all excluded;
// current released series and unknown labels are not.
func TestUbuntuExcluded(t *testing.T) {
	relInfo.mu.Lock()
	relInfo.ubuntuRel = map[string]bool{"18.04": true, "20.04": true, "24.04": true, "26.04": true, "26.10": false}
	relInfo.ubuntuEOL = map[string]bool{"18.04": true, "20.04": true}
	relInfo.mu.Unlock()
	defer func() {
		relInfo.mu.Lock()
		relInfo.ubuntuRel, relInfo.ubuntuEOL = nil, nil
		relInfo.mu.Unlock()
	}()

	for label, want := range map[string]bool{
		"26.10": true, "24.04": false, "26.04": false,
		"26.10.proposed": true, "24.04.backports": true, "99.99": false,
		"18.04": true, "20.04": true, // past standard end-of-life
	} {
		if got := ubuntuExcluded(label); got != want {
			t.Errorf("ubuntuExcluded(%q) = %v, want %v", label, got, want)
		}
	}
}

// TestRelInfoNextFetched verifies the freshness marker: both-sources-OK is fresh for the full TTL,
// while a failed fetch is only fresh for relInfoRetryTTL (so it self-heals soon, not in 24h).
func TestRelInfoNextFetched(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	if got := relInfoNextFetched(now, true); !got.Equal(now) {
		t.Errorf("bothOK should mark fresh at now, got %v", got)
	}
	marker := relInfoNextFetched(now, false)
	// the freshness gate is now.Sub(fetched) < relInfoTTL; the failure window must equal retryTTL.
	if window := relInfoTTL - now.Sub(marker); window != relInfoRetryTTL {
		t.Errorf("failure freshness window = %v, want %v", window, relInfoRetryTTL)
	}
	if now.Add(relInfoRetryTTL-time.Minute).Sub(marker) >= relInfoTTL {
		t.Error("should still be fresh just before the retry TTL elapses")
	}
	if now.Add(relInfoRetryTTL+time.Minute).Sub(marker) < relInfoTTL {
		t.Error("should be stale just after the retry TTL elapses (triggering a refetch)")
	}
}
