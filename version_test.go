package main

import "testing"

// TestVerLess covers the Gentoo version comparator, including the double-digit
// revision/suffix case that the natural-order fix addresses (r10 must be newer than r2,
// which the old strconv-then-string-compare path got backwards).
func TestVerLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool // want verLess(a,b)
	}{
		{"1.0-r2", "1.0-r10", true},   // r2 < r10  (regression guard: was wrongly false)
		{"1.0-r10", "1.0-r2", false},  // r10 is NOT older than r2
		{"1.0_p2", "1.0_p10", true},   // patch level, double digit
		{"1.0_rc9", "1.0_rc11", true}, // release candidate, double digit
		{"1.2", "1.10", true},         // plain numeric dotted parts
		{"1.10", "1.2", false},
		{"1.0", "1.0-r1", true}, // a revision is newer than the bare version
		{"2.0", "2.0", false},   // equal is not "less"
		{"9.1.1652", "9.2.0670", true},
		{"1.0.0", "1.0.0.0", true}, // more tokens (all-equal prefix) is newer
		// Gentoo suffix ordering: _alpha < _beta < _pre < _rc < (release) < _p, and -rN newer.
		{"1.0_rc1", "1.0", true},      // a release candidate is OLDER than the release
		{"1.0", "1.0_rc1", false},     // ...and the release is newer
		{"1.0_alpha1", "1.0", true},   // alpha is older
		{"1.0_beta", "1.0_rc1", true}, // beta < rc
		{"1.0_p1", "1.0", false},      // a patch level is NEWER than the release
		{"1.0", "1.0_p1", true},
		{"1.0_rc1", "1.0_rc2", true}, // rc1 < rc2
	}
	for _, c := range cases {
		if got := verLess(c.a, c.b); got != c.want {
			t.Errorf("verLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestCommandArg verifies the command argument is taken after the first run of whitespace,
// so tab/newline-separated arguments (a pasted "/pkg\nvim") work, not just a single space.
func TestCommandArg(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/pkg vim", "vim"},
		{"/pkg\nvim", "vim"},
		{"/pkg\tvim", "vim"},
		{"/pkg", ""},
		{"/pkg  a  b", "a b"},
		{"  /pkg  vim  ", "vim"},
	} {
		if got := commandArg(c.in); got != c.want {
			t.Errorf("commandArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCmpNum checks the overflow-safe digit-string comparison used inside cmpToken,
// including leading zeros and numbers far beyond int64 range.
func TestCmpNum(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2", "10", -1},
		{"10", "2", 1},
		{"007", "7", 0},                  // leading zeros ignored
		{"00", "0", 0},                   // all zeros
		{"99999999999999999999", "2", 1}, // no overflow: 20-digit number > 2
	}
	for _, c := range cases {
		if got := cmpNum(c.a, c.b); got != c.want {
			t.Errorf("cmpNum(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
