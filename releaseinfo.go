package main

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Release-status info for distros whose channel semantics shift over time (Debian's
// "stable" is 13/trixie today, will be 14 next year). Rather than hardcode the numbers, we
// derive them live from Debian's distro-info-data (release dates decide what's stable vs
// testing) and cache the result. /pkgs uses these to label a Debian/Ubuntu release by its
// role (stable / testing / oldstable / LTS) instead of a bare number.

const relInfoTTL = 24 * time.Hour

var relInfo = struct {
	mu         sync.Mutex
	debian     map[string]string // Debian version ("13") -> status ("stable"/"testing"/...)
	ubuntu     map[string]bool   // Ubuntu version ("24.04") -> is it an LTS?
	ubuntuRel  map[string]bool   // Ubuntu version ("24.04") -> already released (date in the past)?
	fetched    time.Time
	refreshing bool // a fetch is in flight (so concurrent /pkgs don't all hit upstream)
}{}

// ensureReleaseInfo refreshes the Debian/Ubuntu release-status caches if stale. Best-effort:
// a fetch failure leaves the previous (or empty) data, and the relabel helpers fall back to
// the raw version, so /pkgs still works without this enrichment. A `refreshing` guard means a
// burst of concurrent /pkgs on a cold/expired cache triggers ONE upstream fetch, not N.
func ensureReleaseInfo(ctx context.Context, now time.Time) {
	relInfo.mu.Lock()
	fresh := relInfo.debian != nil && now.Sub(relInfo.fetched) < relInfoTTL
	if fresh || relInfo.refreshing {
		relInfo.mu.Unlock()
		return // already fresh, or someone else is fetching — fall back to current data
	}
	relInfo.refreshing = true
	relInfo.mu.Unlock()

	deb := fetchDebianStatus(ctx, now)
	ubu, ubuRel := fetchUbuntu(ctx, now)

	relInfo.mu.Lock()
	if deb != nil {
		relInfo.debian = deb
	}
	if ubu != nil {
		relInfo.ubuntu, relInfo.ubuntuRel = ubu, ubuRel
	}
	if relInfo.debian == nil {
		relInfo.debian = map[string]string{} // mark attempted so we don't refetch every call
	}
	relInfo.fetched = now
	relInfo.refreshing = false
	relInfo.mu.Unlock()
}

// distroInfoCSV columns: version,codename,series,created,release,eol[,eol-lts,...]. A row
// is "released" when its release date is set and in the past.
func parseDistroInfo(body string) (rows [][]string) {
	for i, line := range strings.Split(body, "\n") {
		if i == 0 || strings.TrimSpace(line) == "" { // skip header + blanks
			continue
		}
		rows = append(rows, strings.Split(line, ","))
	}
	return rows
}

func fetchDebianStatus(ctx context.Context, now time.Time) map[string]string {
	body, err := httpGetBody(ctx, "https://debian.pages.debian.net/distro-info-data/debian.csv", 1<<20)
	if err != nil {
		return nil
	}
	return deriveDebianStatus(string(body), now)
}

// deriveDebianStatus maps Debian version numbers to roles from distro-info-data, using
// release dates (vs now) rather than hardcoded numbers: the newest released versions are
// stable/oldstable/oldoldstable, and the lowest not-yet-released version above stable is
// testing. So when Debian 14 releases, "stable" follows automatically.
func deriveDebianStatus(body string, now time.Time) map[string]string {
	type rel struct {
		ver      string
		released bool
	}
	var rels []rel
	for _, c := range parseDistroInfo(body) {
		// Need at least version,codename,series,created. A not-yet-released version (testing)
		// has no release column (4 fields); a released one has a release date at index 4.
		if len(c) < 4 || c[0] == "" { // skip sid/experimental (no version) and malformed rows
			continue
		}
		released := false
		if len(c) >= 5 {
			if t, perr := time.Parse("2006-01-02", c[4]); perr == nil && !t.After(now) {
				released = true
			}
		}
		rels = append(rels, rel{c[0], released})
	}
	out := map[string]string{}
	// Released versions, newest first: stable, oldstable, oldoldstable.
	var rel0 []string
	for _, r := range rels {
		if r.released {
			rel0 = append(rel0, r.ver)
		}
	}
	sort.Slice(rel0, func(i, j int) bool { return verLess(rel0[j], rel0[i]) }) // desc
	for i, st := range []string{"stable", "oldstable", "oldoldstable"} {
		if i < len(rel0) {
			out[rel0[i]] = st
		}
	}
	// The lowest not-yet-released version above stable is "testing".
	if len(rel0) > 0 {
		stable := rel0[0]
		testing := ""
		for _, r := range rels {
			if !r.released && verLess(stable, r.ver) && (testing == "" || verLess(r.ver, testing)) {
				testing = r.ver
			}
		}
		if testing != "" {
			out[testing] = "testing"
		}
	}
	return out
}

// fetchUbuntu returns, per Ubuntu version, whether it's an LTS and whether it's already
// released (release date in the past) — the latter so /pkgs can exclude an in-development
// series (e.g. 26.10 before its release date) from the "current stable" line.
func fetchUbuntu(ctx context.Context, now time.Time) (lts, released map[string]bool) {
	body, err := httpGetBody(ctx, "https://debian.pages.debian.net/distro-info-data/ubuntu.csv", 1<<20)
	if err != nil {
		return nil, nil
	}
	lts, released = map[string]bool{}, map[string]bool{}
	for _, c := range parseDistroInfo(string(body)) {
		if len(c) < 1 || c[0] == "" {
			continue
		}
		ver := strings.TrimSpace(strings.TrimSuffix(c[0], "LTS"))
		lts[ver] = strings.Contains(c[0], "LTS")
		// Record released status for EVERY series (true/false), so an unreleased series is
		// known-and-false (excluded) rather than merely absent (treated as unknown).
		rel := false
		if len(c) >= 5 {
			if t, perr := time.Parse("2006-01-02", c[4]); perr == nil && !t.After(now) {
				rel = true
			}
		}
		released[ver] = rel
	}
	return lts, released
}

// debianRelabel maps a raw Debian release label to its role; "unstable" and unknowns pass
// through, so labels stay meaningful even before the CSV is loaded.
func debianRelabel(raw string) string {
	if raw == "unstable" {
		return "unstable/sid" // the rolling unstable channel is codenamed sid
	}
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	if s, ok := relInfo.debian[raw]; ok {
		return raw + " " + s // e.g. "13 stable"
	}
	return raw
}

// ubuntuRelabel appends "LTS" to an Ubuntu release that is one.
func ubuntuRelabel(raw string) string {
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	if relInfo.ubuntu[raw] {
		return raw + " LTS"
	}
	return raw
}

// ubuntuTesting reports whether an Ubuntu release label should be excluded from the "current
// stable" line: a pre-release pocket (proposed/backports), or a series whose release date is
// still in the future per distro-info-data (e.g. 26.10 before it ships). Unknown or clean
// released series are NOT excluded, so before the CSV loads /pkgs falls back to showing the
// highest numbered series. Mirrors debianTesting (which excludes the numbered testing series).
func ubuntuTesting(label string) bool {
	if strings.Contains(label, "proposed") || strings.Contains(label, "backport") {
		return true
	}
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	released, known := relInfo.ubuntuRel[label]
	return known && !released
}
