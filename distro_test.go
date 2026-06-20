package main

import "testing"

// TestVerTier verifies the /distro per-distro version preference: a real release (tier 0)
// beats a date/CalVer (tier 1) beats a Gentoo 9999 live ebuild (tier 2), so a family shows
// its actual packaged version rather than a live-ebuild placeholder.
func TestVerTier(t *testing.T) {
	for _, c := range []struct {
		v    string
		want int
	}{
		{"3.5.1", 0},
		{"2026.06.09", 1},  // dotted date
		{"2026-06-09", 1},  // dashed date
		{"9999", 2},        // live ebuild
		{"9999.9999", 2},   // live ebuild, multi-part
		{"152.0_beta9", 0}, // a real (pre)release, not a pseudo-version
	} {
		if got := verTier(c.v); got != c.want {
			t.Errorf("verTier(%q) = %d, want %d", c.v, got, c.want)
		}
	}
}

// TestBetterVer checks the tier-then-value ordering used to pick each distro's shown
// version: a real release replaces a 9999, a date replaces a 9999, and within a tier the
// higher version wins — while a date-only project keeps its newest date.
func TestBetterVer(t *testing.T) {
	for _, c := range []struct {
		cur, cand string
		want      bool // betterVer(cur, cand): should cand replace cur?
	}{
		{"9999", "3.5.1", true},            // real release beats live ebuild
		{"3.5.1", "9999", false},           // live ebuild never replaces a real release
		{"9999", "2026.06.09", true},       // date beats live ebuild
		{"2026.06.01", "2026.06.09", true}, // newer date wins within the date tier
		{"3.5.0", "3.5.1", true},           // higher real release wins
		{"3.5.1", "3.5.0", false},
	} {
		if got := betterVer(c.cur, c.cand); got != c.want {
			t.Errorf("betterVer(%q, %q) = %v, want %v", c.cur, c.cand, got, c.want)
		}
	}
}

// TestFamOf maps Repology repo ids to the displayed family, including the multi-prefix
// families (RHEL/EPEL) and the split openSUSE variants.
func TestFamOf(t *testing.T) {
	for _, c := range []struct{ repo, want string }{
		{"gentoo", "Gentoo"},
		{"aur", "AUR"},
		{"alpine_3_20", "Alpine"},
		{"debian_12", "Debian"},
		{"fedora_41", "Fedora"},
		{"epel_9", "RHEL/EPEL"},
		{"almalinux_9", "RHEL/EPEL"},
		{"opensuse_leap_15_6", "openSUSE Leap"},
		{"opensuse_tumbleweed", "openSUSE Tumbleweed"},
		{"freebsd", ""}, // not a family we surface
	} {
		if got := famOf(c.repo); got != c.want {
			t.Errorf("famOf(%q) = %q, want %q", c.repo, got, c.want)
		}
	}
}
