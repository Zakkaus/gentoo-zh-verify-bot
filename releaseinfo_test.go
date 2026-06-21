package main

import (
	"context"
	"testing"
	"time"
)

// TestEnsureReleaseInfoEmptyDoesNotOverwrite drives ensureReleaseInfo with injected fetchers that
// return EMPTY maps (a malformed HTTP-200): the previously-good cache must NOT be overwritten, and
// the round must take the short retry window (fetched != now) rather than full-TTL freshness.
func TestEnsureReleaseInfoEmptyDoesNotOverwrite(t *testing.T) {
	relInfo.mu.Lock()
	relInfo.debian = map[string]string{"13": "stable"}
	relInfo.ubuntu = map[string]bool{"24.04": true}
	relInfo.fetched, relInfo.refreshing = time.Time{}, false // stale => ensureReleaseInfo will refetch
	relInfo.mu.Unlock()
	t.Cleanup(func() {
		relInfo.mu.Lock()
		relInfo.debian, relInfo.ubuntu, relInfo.ubuntuRel, relInfo.ubuntuEOL, relInfo.ubuntuSer = nil, nil, nil, nil, nil
		relInfo.fetched, relInfo.refreshing = time.Time{}, false
		relInfo.mu.Unlock()
	})

	od, ou := fetchDebianStatusFn, fetchUbuntuFn
	fetchDebianStatusFn = func(context.Context, time.Time) map[string]string { return map[string]string{} }
	fetchUbuntuFn = func(context.Context, time.Time) (map[string]bool, map[string]bool, map[string]bool, map[string]bool) {
		return map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	}
	t.Cleanup(func() { fetchDebianStatusFn, fetchUbuntuFn = od, ou })

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	ensureReleaseInfo(context.Background(), now)

	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	if relInfo.debian["13"] != "stable" || !relInfo.ubuntu["24.04"] {
		t.Error("an empty (malformed-200) fetch must NOT overwrite previously-good cached release data")
	}
	if relInfo.fetched.Equal(now) {
		t.Error("an empty fetch must take the short retry window (fetched != now), not full-TTL freshness")
	}
}

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

// TestDeriveDebianStatusEmpty verifies the v3.6.1 empty-CSV guard's signal: a malformed/empty or
// header-only HTTP-200 body parses to an EMPTY status map (which ensureReleaseInfo's len>0 check
// then treats as a failed fetch and retries soon), rather than a non-empty map cached as success.
func TestDeriveDebianStatusEmpty(t *testing.T) {
	now := time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)
	for _, body := range []string{
		"", // empty body
		"version,codename,series,created,release,eol", // header only, no data rows
		"<html>503 Service Unavailable</html>",        // an error page, not CSV
		"garbage,no,real,release,dates,here",          // a row with no past release date
	} {
		if got := deriveDebianStatus(body, now); len(got) != 0 {
			t.Errorf("deriveDebianStatus(%q) = %v, want empty (so ensureReleaseInfo treats it as a failed fetch)", body, got)
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
