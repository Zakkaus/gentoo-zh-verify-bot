package main

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Debian and Ubuntu channel roles come from live distro-info-data, not hardcoded releases.
// /pkgs uses the cached roles for stable, testing, oldstable, LTS, and EOL labels.

const relInfoTTL = 24 * time.Hour

// Failed refreshes retry quickly instead of retaining degraded data for 24 hours.
const relInfoRetryTTL = 10 * time.Minute

var (
	fetchDebianStatusFn = fetchDebianStatus
	fetchUbuntuFn       = fetchUbuntu
)

var relInfo = struct {
	mu         sync.Mutex
	debian     map[string]string // Debian version ("13") -> status ("stable"/"testing"/...)
	ubuntu     map[string]bool   // Ubuntu version ("24.04") -> is it an LTS?
	ubuntuRel  map[string]bool   // Ubuntu version ("24.04") -> already released (date in the past)?
	ubuntuEOL  map[string]bool   // Ubuntu version ("18.04") -> past the standard-support end date?
	ubuntuSer  map[string]bool   // Ubuntu series codename ("resolute") -> already released?
	fetched    time.Time
	refreshing bool // a fetch is in flight (so concurrent /pkgs don't all hit upstream)
}{}

// Refresh is optional enrichment: failures retain old data and raw labels still work.
// The in-flight guard coalesces concurrent cold lookups.
func ensureReleaseInfo(ctx context.Context, now time.Time) {
	relInfo.mu.Lock()
	fresh := relInfo.debian != nil && now.Sub(relInfo.fetched) < relInfoTTL
	if fresh || relInfo.refreshing {
		relInfo.mu.Unlock()
		return // already fresh, or someone else is fetching — fall back to current data
	}
	relInfo.refreshing = true
	relInfo.mu.Unlock()
	// Always clear the in-flight flag, including during panic unwinding.
	defer func() {
		relInfo.mu.Lock()
		relInfo.refreshing = false
		relInfo.mu.Unlock()
	}()

	deb := fetchDebianStatusFn(ctx, now)
	ubu, ubuRel, ubuEOL, ubuSer := fetchUbuntuFn(ctx, now)

	// Empty HTTP-200 parses indicate upstream errors or schema drift; never replace good data.
	debOK, ubuOK := len(deb) > 0, len(ubu) > 0
	relInfo.mu.Lock()
	if debOK {
		relInfo.debian = deb
	}
	if ubuOK {
		relInfo.ubuntu, relInfo.ubuntuRel, relInfo.ubuntuEOL, relInfo.ubuntuSer = ubu, ubuRel, ubuEOL, ubuSer
	}
	if relInfo.debian == nil {
		relInfo.debian = map[string]string{} // mark attempted so the freshness gate can hold (no per-call refetch)
	}
	// Full TTL requires both sources; partial refreshes use the short retry window.
	relInfo.fetched = relInfoNextFetched(now, debOK && ubuOK)
	relInfo.mu.Unlock()
}

// Backdate failed refreshes to leave only relInfoRetryTTL freshness.
func relInfoNextFetched(now time.Time, bothOK bool) time.Time {
	if bothOK {
		return now
	}
	return now.Add(relInfoRetryTTL - relInfoTTL)
}

// A release date at or before now marks a distro-info row released.
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

// Derive stable generations and the next testing release from dates.
func deriveDebianStatus(body string, now time.Time) map[string]string {
	type rel struct {
		ver      string
		released bool
	}
	var rels []rel
	for _, c := range parseDistroInfo(body) {
		// Testing rows may omit the release column; versionless rows are sid/experimental.
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

// Ubuntu maps track LTS, release, standard-support end, and codename release state.
func fetchUbuntu(ctx context.Context, now time.Time) (lts, released, eol, series map[string]bool) {
	body, err := httpGetBody(ctx, "https://debian.pages.debian.net/distro-info-data/ubuntu.csv", 1<<20)
	if err != nil {
		return nil, nil, nil, nil
	}
	lts, released, eol, series = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range parseDistroInfo(string(body)) {
		if len(c) < 1 || c[0] == "" {
			continue
		}
		ver := strings.TrimSpace(strings.TrimSuffix(c[0], "LTS"))
		lts[ver] = strings.Contains(c[0], "LTS")
		// Store unreleased series as known false, not unknown.
		rel := false
		if len(c) >= 5 {
			if t, perr := time.Parse("2006-01-02", c[4]); perr == nil && !t.After(now) {
				rel = true
			}
		}
		released[ver] = rel
		// Exclude releases past standard support that would mask newer releases shipping only a Snap.
		if len(c) >= 6 {
			if t, perr := time.Parse("2006-01-02", c[5]); perr == nil && !t.After(now) {
				eol[ver] = true
			}
		}
		// Madison uses codenames, so retain their release state too.
		if len(c) >= 3 {
			if s := strings.ToLower(strings.TrimSpace(c[2])); s != "" {
				series[s] = rel
			}
		}
	}
	return lts, released, eol, series
}

// Known unreleased Ubuntu suites are development; unknown suites remain displayable.
func ubuntuDevSuite(series string) bool {
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	released, known := relInfo.ubuntuSer[strings.ToLower(series)]
	return known && !released
}

// Unknown Debian labels pass through before metadata loads.
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

func ubuntuRelabel(raw string) string {
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	out := raw
	if relInfo.ubuntu[raw] {
		out += " LTS"
	}
	if relInfo.ubuntuEOL[raw] { // the upstream EOL column marks the end of standard support
		out += " · 标准支持已结束"
	}
	return out
}

// Exclude proposed, backports, unreleased, and post-standard-support Ubuntu series from the current line.
// Unknown series remain eligible so lookups still work before metadata loads.
func ubuntuExcluded(label string) bool {
	if strings.Contains(label, "proposed") || strings.Contains(label, "backport") {
		return true
	}
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	if relInfo.ubuntuEOL[label] {
		return true
	}
	released, known := relInfo.ubuntuRel[label]
	return known && !released
}
