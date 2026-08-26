package lookup

import (
	"context"
	"strings"
	"testing"
)

const lsManPage = `LS(1)                            User Commands                           LS(1)

NAME
       ls - list directory contents

SYNOPSIS
       ls [OPTION]... [FILE]...

DESCRIPTION
       List information about the FILEs.
`

// The two sections worth quoting are read out of the plain-text page, and the description that
// follows them is not.
func TestManPageParsing(t *testing.T) {
	title, synopsis := parseManPage(lsManPage)
	if title != "ls - list directory contents" {
		t.Errorf("title = %q", title)
	}
	if synopsis != "ls [OPTION]... [FILE]..." {
		t.Errorf("synopsis = %q", synopsis)
	}
	if strings.Contains(synopsis, "List information") {
		t.Error("the synopsis must stop at the next section")
	}
}

// A page without the sections still resolves; the caller falls back to the page name.
func TestManPageWithoutSections(t *testing.T) {
	title, synopsis := parseManPage("SOMEPAGE(9)\n\n       free text only\n")
	if title != "" || synopsis != "" {
		t.Errorf("title = %q synopsis = %q, want both empty", title, synopsis)
	}
}

// Only a page name reaches the network; anything that could steer the request is refused before
// a request is made.
func TestManNameAccepts(t *testing.T) {
	// Real page names carry dots, so a trailing number that is not a section is just part of
	// the name; it simply will not be found.
	for _, name := range []string{"ls", "fstab.5", "ssh_config", "git-rebase", "a.out", "open.2",
		"systemd.service", "tmpfiles.d", "ls.99"} {
		if !manNameRe.MatchString(name) {
			t.Errorf("%q is a legitimate page name", name)
		}
	}
	for _, name := range []string{"", "../etc/passwd", "ls;rm", "a b", "http://x", "ls\nrm", strings.Repeat("x", 90)} {
		if manNameRe.MatchString(name) {
			t.Errorf("%q must not reach the network", name)
		}
	}
}

// A section that has no page is not an outage; only an unreachable site is.
func TestManMissingPageIsNotAnOutage(t *testing.T) {
	withStatusFixture(t, 404, func() {
		_, found, failed := fetchManPage(context.Background(), "nosuchpage", "1")
		if found {
			t.Error("a 404 means the page does not exist")
		}
		if failed {
			t.Error("a site that answered 404 is reachable; do not report an outage")
		}
	})
}
