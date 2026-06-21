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

// relInfoRetryTTL is the short freshness window used after a fetch fails, so degraded/empty
// release metadata is retried within minutes instead of being cached for the full relInfoTTL.
const relInfoRetryTTL = 10 * time.Minute

// fetchDebianStatusFn / fetchUbuntuFn indirect over the real fetchers so tests can inject
// empty/malformed results and assert ensureReleaseInfo's store + freshness behaviour offline.
var (
	fetchDebianStatusFn = fetchDebianStatus
	fetchUbuntuFn       = fetchUbuntu
)

var relInfo = struct {
	mu         sync.Mutex
	debian     map[string]string // Debian version ("13") -> status ("stable"/"testing"/...)
	ubuntu     map[string]bool   // Ubuntu version ("24.04") -> is it an LTS?
	ubuntuRel  map[string]bool   // Ubuntu version ("24.04") -> already released (date in the past)?
	ubuntuEOL  map[string]bool   // Ubuntu version ("18.04") -> past standard end-of-life?
	ubuntuSer  map[string]bool   // Ubuntu series codename ("resolute") -> already released?
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
	// Always clear the in-flight flag, even if a fetch panics — otherwise refreshing would stay
	// true forever and the labels would never refresh again (mirrors pkgCache.refresh/getNews).
	defer func() {
		relInfo.mu.Lock()
		relInfo.refreshing = false
		relInfo.mu.Unlock()
	}()

	deb := fetchDebianStatusFn(ctx, now)
	ubu, ubuRel, ubuEOL, ubuSer := fetchUbuntuFn(ctx, now)

	// Treat an EMPTY parsed result as a failed fetch, not success: a malformed/empty HTTP-200 body
	// (GitLab Pages error page, schema drift) parses to zero rows -> empty maps, which must not
	// overwrite good data or be cached as fresh for the full 24h. A valid CSV always yields a
	// non-empty status/lts map, so len>0 is a sound validity proxy.
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
	// Only treat the data fresh for the full TTL when BOTH sources succeeded this round; otherwise
	// keep it fresh only briefly (relInfoRetryTTL) so a failed/degraded source self-heals soon
	// instead of serving degraded EOL/dev labels for 24h.
	relInfo.fetched = relInfoNextFetched(now, debOK && ubuOK)
	relInfo.mu.Unlock()
}

// relInfoNextFetched returns the `fetched` marker to store after a refresh round: now (full-TTL
// freshness) when both sources succeeded, else a back-dated marker giving only relInfoRetryTTL of
// freshness so ensureReleaseInfo retries soon rather than caching a partial/empty result for 24h.
func relInfoNextFetched(now time.Time, bothOK bool) time.Time {
	if bothOK {
		return now
	}
	return now.Add(relInfoRetryTTL - relInfoTTL)
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
		// Record released status for EVERY series (true/false), so an unreleased series is
		// known-and-false (excluded) rather than merely absent (treated as unknown).
		rel := false
		if len(c) >= 5 {
			if t, perr := time.Parse("2006-01-02", c[4]); perr == nil && !t.After(now) {
				rel = true
			}
		}
		released[ver] = rel
		// eol (index 5) = end of standard support. A series past it (e.g. 18.04, 20.04) is no
		// longer a current desktop release, so /pkgs must not surface its last lingering deb as
		// Ubuntu's current version — newer releases ship the app as a Snap instead.
		if len(c) >= 6 {
			if t, perr := time.Parse("2006-01-02", c[5]); perr == nil && !t.After(now) {
				eol[ver] = true
			}
		}
		// series codename (index 2) -> released, keyed lowercase: madison labels Ubuntu suites by
		// codename (e.g. "resolute"), so /armpkgs can flag an unreleased dev series ("stonking").
		if len(c) >= 3 {
			if s := strings.ToLower(strings.TrimSpace(c[2])); s != "" {
				series[s] = rel
			}
		}
	}
	return lts, released, eol, series
}

// ubuntuDevSuite reports whether an Ubuntu madison suite (a series codename like "stonking" or
// "questing") is a not-yet-released development series, per distro-info-data — so /armpkgs flags
// it instead of presenting it as Ubuntu's current arm64 version. Unknown suites (a Debian
// codename, or before the CSV loads) are NOT dev, so the newest suite still shows.
func ubuntuDevSuite(series string) bool {
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	released, known := relInfo.ubuntuSer[strings.ToLower(series)]
	return known && !released
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
	out := raw
	if relInfo.ubuntu[raw] {
		out += " LTS"
	}
	if relInfo.ubuntuEOL[raw] { // honest marker if an EOL series is shown anyway (fallback path)
		out += " · 已停止支持"
	}
	return out
}

// ubuntuExcluded reports whether an Ubuntu release label should be excluded from the "current
// stable" line: a pre-release pocket (proposed/backports); a series whose release date is still
// in the future per distro-info-data (e.g. 26.10 before it ships); or a series past its standard
// end-of-life (e.g. 18.04, 20.04). The EOL exclusion is what stops an ancient LTS — which only
// still carries a real deb because newer releases moved the app to a Snap — from masquerading as
// Ubuntu's current version. All derived live from distro-info-data; unknown/clean released series
// are NOT excluded, so before the CSV loads /pkgs falls back to the highest numbered series.
// Mirrors debianTesting (which excludes the numbered testing series).
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
