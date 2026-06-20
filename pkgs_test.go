package main

import "testing"

// TestFamilyChannels verifies the per-distro channel display: a rolling/dev channel plus
// the current stable when they differ, with the stable labelled by the highest-numbered
// release that actually ships that version (Debian → "13"/trixie, not the higher "14"/forky
// that carries a different version); a package at one version everywhere stays one line.
func TestFamilyChannels(t *testing.T) {
	deb := []string{"debian_"}
	// firefox-like: unstable newest; 11/12/13 share the stable version; 14 (forky) is lower.
	got := familyChannels([]repologyPkg{
		{"debian_unstable", "152.0.1"},
		{"debian_11", "140.12.0"},
		{"debian_12", "140.12.0"},
		{"debian_13", "140.12.0"},
		{"debian_14", "140.11.0"},
	}, deb)
	want := []channelLine{{"152.0.1", "unstable"}, {"140.12.0", "13"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("firefox-like = %v, want %v", got, want)
	}
	// htop-like: same version in unstable and the releases -> one line, prefer the rolling label.
	if g := familyChannels([]repologyPkg{
		{"debian_unstable", "3.5.1"}, {"debian_13", "3.5.1"}, {"debian_14", "3.5.1"},
	}, deb); len(g) != 1 || g[0] != (channelLine{"3.5.1", "unstable"}) {
		t.Errorf("htop-like = %v, want one line {3.5.1, unstable}", g)
	}
	// no rolling channel (Ubuntu-like, all numbered) -> one line, no phantom stable.
	if g := familyChannels([]repologyPkg{
		{"ubuntu_24_04", "1.0"}, {"ubuntu_22_04", "0.9"},
	}, []string{"ubuntu_"}); len(g) != 1 {
		t.Errorf("ubuntu-like = %v, want one line", g)
	}
}

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

// TestReleaseLabel checks the repo-id → release-name annotation shown next to each
// distro's version (rolling repos with no per-release suffix yield no label).
func TestReleaseLabel(t *testing.T) {
	for _, c := range []struct {
		repo, want string
		prefixes   []string
	}{
		{"debian_unstable", "unstable", []string{"debian_"}},
		{"ubuntu_24_04", "24.04", []string{"ubuntu_"}},
		{"fedora_rawhide", "rawhide", []string{"fedora_"}},
		{"fedora_41", "41", []string{"fedora_"}},
		{"alpine_3_21", "3.21", []string{"alpine_"}},
		{"alpine_edge", "edge", []string{"alpine_"}},
		{"opensuse_leap_15_6", "15.6", []string{"opensuse_leap"}},
		{"almalinux_9", "9", []string{"epel_", "centos_", "almalinux_", "rockylinux_", "rhel_"}},
		{"arch", "", []string{"arch"}},                               // rolling, exact prefix -> no label
		{"opensuse_tumbleweed", "", []string{"opensuse_tumbleweed"}}, // rolling
	} {
		if got := releaseLabel(c.repo, c.prefixes); got != c.want {
			t.Errorf("releaseLabel(%q) = %q, want %q", c.repo, got, c.want)
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
