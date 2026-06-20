package main

import "testing"

// TestFamilyChannels verifies the per-distro channel display: a rolling/dev channel plus
// the current stable when they differ, with the stable labelled by the highest-numbered
// release that actually ships that version (Debian → "13"/trixie, not the higher "14"/forky
// that carries a different version); a package at one version everywhere stays one line.
func TestFamilyChannels(t *testing.T) {
	deb := []string{"debian_"}
	// 14/forky is testing (excluded from stable); 13/trixie is the real stable.
	debTesting := func(lbl string) bool { return lbl == "14" }

	// firefox-like: sid newest; 11/12/13 share the stable version; 14 (testing) is excluded.
	got := familyChannels([]repologyPkg{
		{"debian_unstable", "152.0.1"},
		{"debian_12", "140.12.0"}, {"debian_13", "140.12.0"}, {"debian_14", "140.11.0"},
	}, deb, debTesting)
	want := []channelLine{{"152.0.1", "unstable"}, {"140.12.0", "13"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("firefox-like = %v, want %v", got, want)
	}
	// nano-like: testing(14) ties with sid at 9.0 but real stable(13) is older -> 2 lines.
	if g := familyChannels([]repologyPkg{
		{"debian_unstable", "9.0"}, {"debian_14", "9.0"}, {"debian_13", "8.4"},
	}, deb, debTesting); len(g) != 2 || g[1] != (channelLine{"8.4", "13"}) {
		t.Errorf("nano-like = %v, want sid 9.0 + stable {8.4,13}", g)
	}
	// Fedora-like: rawhide newest, but stable(44) carries a different version -> 2 lines; when
	// rawhide == stable, a single line labelled by the stable release (not "rawhide").
	if g := familyChannels([]repologyPkg{
		{"fedora_rawhide", "9.0"}, {"fedora_44", "8.7"}, {"fedora_43", "8.5"},
	}, []string{"fedora_"}, nil); len(g) != 2 || g[1] != (channelLine{"8.7", "44"}) {
		t.Errorf("fedora-like = %v, want rawhide 9.0 + {8.7,44}", g)
	}
	if g := familyChannels([]repologyPkg{
		{"fedora_rawhide", "152.0"}, {"fedora_44", "152.0"},
	}, []string{"fedora_"}, nil); len(g) != 1 || g[0] != (channelLine{"152.0", "44"}) {
		t.Errorf("fedora-coincide = %v, want one line {152.0,44} (not rawhide)", g)
	}
	// pure rolling (Arch) -> one line, rolling label.
	if g := familyChannels([]repologyPkg{{"arch", "153.0b2"}}, []string{"arch"}, nil); len(g) != 1 || g[0].label != "" {
		t.Errorf("arch-like = %v, want one rolling line", g)
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
		{"almalinux_9", "RHEL"}, // RHEL rebuild
		{"rocky_9", "RHEL"},     // RHEL rebuild
		{"centos_stream_10", "CentOS Stream"},
		{"epel_9", "EPEL"},
		{"centos_8", ""}, // old CentOS Linux (EOL) — deliberately not surfaced
		{"opensuse_leap_15_6", "openSUSE Leap"},
		{"opensuse_tumbleweed", "openSUSE Tumbleweed"},
		{"freebsd", ""}, // not a family we surface
	} {
		if got := famOf(c.repo); got != c.want {
			t.Errorf("famOf(%q) = %q, want %q", c.repo, got, c.want)
		}
	}
}

// TestBareDateSnapshot verifies a bare YYYYMMDD snapshot (e.g. Debian gcc-snapshot) is
// treated as a date and never beats / displaces the real release.
func TestBareDateSnapshot(t *testing.T) {
	for _, v := range []string{"20250315", "20260327", "20210106"} {
		if !bareDate(v) {
			t.Errorf("%q should be a bare date", v)
		}
	}
	for _, v := range []string{"99999999", "16100000", "12345678", "9999", "1234567", "2025031a"} {
		if bareDate(v) {
			t.Errorf("%q should NOT be a bare date", v)
		}
	}
	if betterVer("14.2.0", "20250315") {
		t.Error("snapshot 20250315 must not beat real 14.2.0")
	}
	// gcc-like Debian rows: real versions must win, snapshots excluded from the output.
	rows := []repologyPkg{
		{"debian_unstable", "16.1.0"}, {"debian_unstable", "20260327"},
		{"debian_13", "14.2.0"}, {"debian_13", "20250315"},
	}
	for _, ch := range familyChannels(rows, []string{"debian_"}, func(string) bool { return false }) {
		if ch.ver == "20260327" || ch.ver == "20250315" {
			t.Errorf("snapshot leaked into /pkgs output: %+v", ch)
		}
	}
}
