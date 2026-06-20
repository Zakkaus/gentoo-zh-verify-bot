package main

import (
	"strings"
	"testing"
)

// TestParseMadison verifies the madison parser: it keeps base-release suites (newest wins
// per suite), drops pocket variants (-updates / -security / -backports) and the
// "/component" qualifier, and ignores malformed lines.
func TestParseMadison(t *testing.T) {
	body := `htop | 3.0.5-7 | bullseye | arm64
htop | 3.2.2-2 | bookworm | arm64
htop | 3.4.1-5 | trixie | arm64
htop | 3.4.1-5+b1 | bookworm-backports | arm64
htop | 3.5.1-3 | sid | arm64
some garbage line without pipes
htop | 2.0.1-1 | xenial/universe | arm64`

	got := parseMadison(body)
	// expect base suites in first-seen order, pockets dropped, /universe stripped
	want := []madEntry{
		{"bullseye", "3.0.5-7"},
		{"bookworm", "3.2.2-2"},
		{"trixie", "3.4.1-5"},
		{"sid", "3.5.1-3"},
		{"xenial", "2.0.1-1"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %v, want %v", i, got[i], want[i])
		}
	}

	// a newer line for an already-seen suite updates its version in place
	dup := parseMadison("p | 1.0 | sid | arm64\np | 2.0 | sid | arm64")
	if len(dup) != 1 || dup[0].ver != "2.0" {
		t.Errorf("dedupe-keep-newest failed: %v", dup)
	}

	// no arm64 lines -> empty
	if e := parseMadison(""); len(e) != 0 {
		t.Errorf("empty body should yield no entries, got %v", e)
	}
}

// TestPickMadison verifies the /armpkgs suite selection: the newest RELEASED suite wins, an
// unreleased development series (e.g. "stonking") is skipped (or flagged when it's all there is),
// and with no devSuite filter (Debian) the newest suite — including sid — is kept as-is. The Snap
// transitional version is left for displayVer to render.
func TestPickMadison(t *testing.T) {
	dev := func(s string) bool { return s == "stonking" } // the only unreleased dev series here

	// firefox-like Ubuntu: newest suite is the unreleased "stonking" -> skip to released "resolute".
	s, v, d := pickMadison([]madEntry{
		{"jammy", "110"}, {"noble", "120"},
		{"resolute", "1:1snap1-0ubuntu10"}, {"stonking", "1:1snap1-0ubuntu10"},
	}, dev)
	if s != "resolute" || d {
		t.Errorf("pickMadison should pick released 'resolute', got %q dev=%v", s, d)
	}
	if displayVer(v) != "snap" {
		t.Errorf("a Snap transitional must display as snap, got %q", displayVer(v))
	}

	// nil devSuite (Debian): keep the newest suite (sid), unflagged.
	if s2, _, d2 := pickMadison([]madEntry{{"trixie", "1"}, {"sid", "2"}}, nil); s2 != "sid" || d2 {
		t.Errorf("nil devSuite should keep the newest suite, got %q dev=%v", s2, d2)
	}

	// only a dev series ships it -> fall back to it, flagged dev.
	if s3, _, d3 := pickMadison([]madEntry{{"stonking", "9"}}, dev); s3 != "stonking" || !d3 {
		t.Errorf("all-dev should fall back to newest flagged dev, got %q dev=%v", s3, d3)
	}
}

// TestAurArchLabel verifies the PKGBUILD arch=() classification: any / aarch64 / 32-bit
// ARM only / x86-only, and a missing arch line.
func TestAurArchLabel(t *testing.T) {
	for _, c := range []struct{ pkgbuild, wantSub string }{
		{"pkgname=x\narch=('any')\n", "any"},
		{"arch=('i686' 'x86_64' 'aarch64' 'armv7h')", "aarch64"},
		{"arch=(x86_64 aarch64)", "aarch64"},
		{"arch=('armv7h' 'armv6h')", "32"},  // 32-bit ARM only, no aarch64
		{"arch=('x86_64')", "x86"},          // x86-only
		{"pkgname=x\nno arch here", "无法解析"}, // missing arch=()
	} {
		if got := aurArchLabel(c.pkgbuild); !strings.Contains(got, c.wantSub) {
			t.Errorf("aurArchLabel(%q) = %q, want substring %q", c.pkgbuild, got, c.wantSub)
		}
	}
}
