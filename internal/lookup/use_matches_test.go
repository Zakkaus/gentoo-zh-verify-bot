package lookup

import (
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/edition"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// This suggestion is built in Go, not in the catalogue, so the {g} substitution does not reach
// it. The generic build must still suggest a command it actually answers.
func TestUseMultipleMatchesSuggestsThisBuildsCommand(t *testing.T) {
	atoms := []string{"www-client/firefox", "www-client/firefox-bin"}
	for _, l := range []i18n.Lang{i18n.LangEN, i18n.LangZH, i18n.LangZHHant} {
		got := renderUseMultipleMatches(l, append([]string(nil), atoms...), pkgLookupAvailability{})
		want := "/" + edition.CommandPrefix + "use "
		for _, atom := range atoms {
			if !strings.Contains(got, want+atom) {
				t.Errorf("%v: reply does not suggest %q for %s: %q", l, want, atom, got)
			}
		}
		if edition.CommandPrefix != "" && strings.Contains(got, " /use ") {
			t.Errorf("%v: the generic build suggests /use, which it does not answer: %q", l, got)
		}
	}
}

func TestUseMultipleMatchesSortsAtoms(t *testing.T) {
	got := renderUseMultipleMatches(i18n.LangEN, []string{"b/z", "a/a"}, pkgLookupAvailability{})
	if strings.Index(got, "a/a") > strings.Index(got, "b/z") {
		t.Errorf("atoms are not sorted: %q", got)
	}
}
