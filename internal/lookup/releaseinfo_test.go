package lookup

import (
	"context"
	"testing"
	"time"
)

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

func TestUbuntuRelabelStandardSupportEnd(t *testing.T) {
	relInfo.mu.Lock()
	oldLTS, oldEOL := relInfo.ubuntu, relInfo.ubuntuEOL
	relInfo.ubuntu = map[string]bool{"18.04": true, "24.04": true}
	relInfo.ubuntuEOL = map[string]bool{"18.04": true, "22.10": true}
	relInfo.mu.Unlock()
	t.Cleanup(func() {
		relInfo.mu.Lock()
		relInfo.ubuntu, relInfo.ubuntuEOL = oldLTS, oldEOL
		relInfo.mu.Unlock()
	})

	tests := []struct {
		raw  string
		want string
	}{
		{raw: "18.04", want: "18.04 LTS · 标准支持已结束"},
		{raw: "22.10", want: "22.10 · 标准支持已结束"},
		{raw: "24.04", want: "24.04 LTS"},
		{raw: "99.99", want: "99.99"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := ubuntuRelabel(tt.raw); got != tt.want {
				t.Errorf("ubuntuRelabel(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
