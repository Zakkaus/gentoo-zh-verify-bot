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
	mu      sync.Mutex
	debian  map[string]string // Debian version ("13") -> status ("stable"/"testing"/...)
	ubuntu  map[string]bool   // Ubuntu version ("24.04") -> is it an LTS?
	fetched time.Time
}{}

// ensureReleaseInfo refreshes the Debian/Ubuntu release-status caches if stale. Best-effort:
// a fetch failure leaves the previous (or empty) data, and the relabel helpers fall back to
// the raw version, so /pkgs still works without this enrichment.
func ensureReleaseInfo(ctx context.Context, now time.Time) {
	relInfo.mu.Lock()
	fresh := relInfo.debian != nil && now.Sub(relInfo.fetched) < relInfoTTL
	relInfo.mu.Unlock()
	if fresh {
		return
	}
	deb := fetchDebianStatus(ctx, now)
	ubu := fetchUbuntuLTS(ctx)
	relInfo.mu.Lock()
	if deb != nil {
		relInfo.debian = deb
	}
	if ubu != nil {
		relInfo.ubuntu = ubu
	}
	if relInfo.debian == nil {
		relInfo.debian = map[string]string{} // mark attempted so we don't refetch every call
	}
	relInfo.fetched = now
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

func fetchUbuntuLTS(ctx context.Context) map[string]bool {
	body, err := httpGetBody(ctx, "https://debian.pages.debian.net/distro-info-data/ubuntu.csv", 1<<20)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, c := range parseDistroInfo(string(body)) {
		if len(c) < 1 || c[0] == "" {
			continue
		}
		ver, isLTS := c[0], strings.Contains(c[0], "LTS")
		ver = strings.TrimSpace(strings.TrimSuffix(ver, "LTS"))
		out[ver] = isLTS
	}
	return out
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
