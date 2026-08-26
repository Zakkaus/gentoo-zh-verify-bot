package lookup

import (
	"strings"
	"testing"
)

// One family ships several repositories; the newest version wins, and repositories outside the
// known families are counted rather than named.
func TestRepologyGroupsByFamily(t *testing.T) {
	pkgs := []repologyPkg{
		{Repo: "gentoo", Version: "5.2"},
		{Repo: "debian_stable", Version: "5.1"},
		{Repo: "debian_unstable", Version: "5.3"},
		{Repo: "hpux_11_31", Version: "5.3.p9"},
		{Repo: "aix_osp", Version: "4.2"},
	}
	entries, others := repologyByFamily(pkgs)
	versions := map[string]string{}
	for _, e := range entries {
		versions[e.label] = e.version
	}
	if versions[famOf("debian_stable")] != "5.3" {
		t.Errorf("Debian version = %q, want the newest of its repositories", versions[famOf("debian_stable")])
	}
	if others < 2 {
		t.Errorf("uncounted repositories = %d, want the two nobody in a Linux chat runs", others)
	}
}

// A project nobody's distribution ships still reports what does have it.
func TestRepologyCountsWhatItCannotName(t *testing.T) {
	entries, others := repologyByFamily([]repologyPkg{{Repo: "hpux_11_31", Version: "1.0"}})
	if len(entries) != 0 {
		t.Errorf("entries = %v, want none: no known family ships it", entries)
	}
	if others != 1 {
		t.Errorf("others = %d, want 1", others)
	}
}

// Only a project name reaches the network.
func TestRepologyNameShape(t *testing.T) {
	for _, name := range []string{"bash", "gcc", "python3", "lib-foo", "x.y", "a+b"} {
		if !repologyNameRe.MatchString(name) {
			t.Errorf("%q is a legitimate project name", name)
		}
	}
	for _, name := range []string{"", "../x", "a b", "a/b", "a;b", strings.Repeat("x", 100)} {
		if repologyNameRe.MatchString(name) {
			t.Errorf("%q must not reach the network", name)
		}
	}
}
