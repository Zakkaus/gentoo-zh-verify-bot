package main

import "testing"

// TestArm64Keywords verifies the arm64 keyword scan picks the newest arm64-stable and the
// newest ~arm64 (testing) version, skips 9999 live ebuilds, and reports empty for both
// when the package isn't keyworded on arm64 at all.
func TestArm64Keywords(t *testing.T) {
	// firefox-like: a newer testing version above an older stable one.
	stable, testing := arm64Keywords([]pkgVersionJSON{
		{Version: "9999", Keywords: []string{"arm64"}}, // live ebuild — must be skipped
		{Version: "152.0", Keywords: []string{"~amd64", "~arm64", "~x86"}},
		{Version: "140.12.0", Keywords: []string{"amd64", "arm64", "x86"}},
	})
	if stable != "140.12.0" || testing != "152.0" {
		t.Errorf("got (stable=%q testing=%q), want (140.12.0, 152.0)", stable, testing)
	}

	// not keyworded on arm64 at all (e.g. an amd64/x86-only package).
	if s, tt := arm64Keywords([]pkgVersionJSON{
		{Version: "1.0", Keywords: []string{"amd64", "x86"}},
	}); s != "" || tt != "" {
		t.Errorf("non-arm package: got (stable=%q testing=%q), want both empty", s, tt)
	}

	// testing only (no stable arm64).
	if s, tt := arm64Keywords([]pkgVersionJSON{
		{Version: "2.0", Keywords: []string{"~arm64"}},
	}); s != "" || tt != "2.0" {
		t.Errorf("testing-only: got (stable=%q testing=%q), want (\"\", 2.0)", s, tt)
	}
}
