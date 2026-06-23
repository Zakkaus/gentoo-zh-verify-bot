package main

import (
	"strings"
	"testing"
)

// These lock the upstream-page PARSERS against silent breakage if a site's HTML / ebuild / metadata
// structure drifts: a fixed fixture of the real shape must keep parsing the expected items. They
// change no logic — only pin the current contract so a drift fails a test instead of quietly
// returning empty results. (/armpkgs' parseMadison + aurArchLabel, and /wiki's pickWikiTitles, are
// already fixture-tested in armpkgs_test.go / wiki_test.go.)

// /news — the gentoo.org news-items index HTML (newsRe).
func TestParseNewsFixture(t *testing.T) {
	fixture := []byte(`<html><body><ul>
 <li><a href="/support/news-items/2026-05-23-kdepim-sql-backend-change.html">KDE PIM SQL backend change</a></li>
 <li><a href="/support/news-items/2026-04-01-portage-news.html">Portage news</a></li>
 <li><a href="/support/news-items/2026-05-23-kdepim-sql-backend-change.html">duplicate should dedupe</a></li>
 <li><a href="/about/index.html">not a news item</a></li>
</ul></body></html>`)
	items := parseNews(fixture)
	if len(items) != 2 {
		t.Fatalf("expected 2 deduped news items, got %d: %+v", len(items), items)
	}
	if items[0].date != "2026-05-23" || !strings.Contains(items[0].title, "KDE PIM") {
		t.Errorf("first item parsed wrong: %+v", items[0])
	}
	if !strings.HasSuffix(items[0].url, "/support/news-items/2026-05-23-kdepim-sql-backend-change.html") {
		t.Errorf("url not built from newsBase + path: %q", items[0].url)
	}
	if got := parseNews([]byte("<html>no news links</html>")); len(got) != 0 {
		t.Errorf("a non-matching page must parse 0 items, got %d", len(got))
	}
}

// /use — an ebuild's IUSE line(s).
func TestParseIUSEFixture(t *testing.T) {
	ebuild := []byte("EAPI=8\nDESCRIPTION=\"x\"\nIUSE=\"ssl +zlib doc\"\nIUSE+=\"test\"\nSLOT=\"0\"\n")
	got := parseIUSE(ebuild)
	want := map[string]bool{"ssl": true, "+zlib": true, "doc": true, "test": true}
	if len(got) != len(want) {
		t.Fatalf("parseIUSE = %v, want the 4 flags", got)
	}
	for _, f := range got {
		if !want[f] {
			t.Errorf("unexpected IUSE token %q", f)
		}
	}
	// a bash-substituted token must be dropped, not surfaced as a flag
	if hit := parseIUSE([]byte("IUSE=\"${PYTHON_USEDEP} real\"")); len(hit) != 1 || hit[0] != "real" {
		t.Errorf("parseIUSE must drop $-substituted tokens, got %v", hit)
	}
}

// /use — a metadata.xml's <flag> descriptions (inner tags stripped).
func TestParseMetadataUseFixture(t *testing.T) {
	md := []byte(`<?xml version="1.0"?>
<pkgmetadata>
 <use>
  <flag name="ssl">Enable <pkg>dev-libs/openssl</pkg> support</flag>
  <flag name="doc">Build documentation</flag>
 </use>
</pkgmetadata>`)
	got := parseMetadataUse(md)
	if len(got) != 2 {
		t.Fatalf("expected 2 flag descriptions, got %d: %v", len(got), got)
	}
	if !strings.Contains(got["ssl"], "openssl") || strings.Contains(got["ssl"], "<pkg>") {
		t.Errorf("inner tags must be stripped from the description: %q", got["ssl"])
	}
	if got["doc"] != "Build documentation" {
		t.Errorf("doc description = %q", got["doc"])
	}
}

// /pkg — the packages.gentoo.org search-results HTML (pkgHrefRe) + relevance ranking.
func TestRankSearchHitsFixture(t *testing.T) {
	body := []byte(`<html><body>
<a href="/packages/app-i18n/fcitx">app-i18n/fcitx</a>
<a href="/packages/dev-ml/core_kernel">dev-ml/core_kernel</a>
<a href="/packages/sys-kernel/gentoo-kernel">sys-kernel/gentoo-kernel</a>
<a href="/packages/app-i18n/fcitx">dup</a>
<a href="/about">not a package</a>
</body></html>`)
	hits := rankSearchHits(body, "kernel")
	if len(hits) != 3 {
		t.Fatalf("expected 3 deduped package atoms, got %d: %v", len(hits), hits)
	}
	pos := map[string]int{}
	for i, h := range hits {
		pos[h] = i
	}
	for _, want := range []string{"app-i18n/fcitx", "dev-ml/core_kernel", "sys-kernel/gentoo-kernel"} {
		if _, ok := pos[want]; !ok {
			t.Errorf("missing expected atom %q in %v", want, hits)
		}
	}
	// a kernel-relevant hit (its category contains the query) must outrank the incidental non-match
	if pos["sys-kernel/gentoo-kernel"] >= pos["app-i18n/fcitx"] {
		t.Errorf("relevance ranking broken: sys-kernel/* should outrank app-i18n/fcitx, got %v", hits)
	}
	if got := rankSearchHits([]byte("<html>no package links</html>"), "kernel"); len(got) != 0 {
		t.Errorf("a non-matching page must yield 0 hits, got %d", len(got))
	}
}
