package lookup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// Fixed upstream-shape fixtures turn silent parser drift into test failures.

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
	if items[0].Date != "2026-05-23" || !strings.Contains(items[0].Title, "KDE PIM") {
		t.Errorf("first item parsed wrong: %+v", items[0])
	}
	if !strings.HasSuffix(items[0].URL, "/support/news-items/2026-05-23-kdepim-sql-backend-change.html") {
		t.Errorf("url not built from newsBase + path: %q", items[0].URL)
	}
	if got := parseNews([]byte("<html>no news links</html>")); len(got) != 0 {
		t.Errorf("a non-matching page must parse 0 items, got %d", len(got))
	}
}

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

func upstreamFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("../../testdata/upstream/" + name)
	if err != nil {
		t.Fatalf("read upstream fixture %q: %v", name, err)
	}
	return body
}

type fixtureRoundTrip func(*http.Request) (*http.Response, error)

func (f fixtureRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func withFixtureHTTP(t *testing.T, fixtures map[string]string, fn func()) {
	t.Helper()
	oldClient := httpClient
	httpClient = &http.Client{Transport: fixtureRoundTrip(func(req *http.Request) (*http.Response, error) {
		name, ok := fixtures[req.URL.String()]
		if !ok {
			return nil, fmt.Errorf("unexpected fixture request: %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/octet-stream"}},
			Body:       io.NopCloser(bytes.NewReader(upstreamFixture(t, name))),
			Request:    req,
		}, nil
	})}
	defer func() { httpClient = oldClient }()
	fn()
}

func TestPackagesGentooLiveFixtures(t *testing.T) {
	infoC.mu.Lock()
	oldInfoCache := infoC.m
	infoC.m = map[string]pkgFullInfo{}
	infoC.mu.Unlock()
	verC.mu.Lock()
	oldVersionCache := verC.m
	verC.m = map[string]verInfo{}
	verC.mu.Unlock()
	t.Cleanup(func() {
		infoC.mu.Lock()
		infoC.m = oldInfoCache
		infoC.mu.Unlock()
		verC.mu.Lock()
		verC.m = oldVersionCache
		verC.mu.Unlock()
	})

	const firefoxURL = "https://packages.gentoo.org/packages/www-client/firefox.json"
	withFixtureHTTP(t, map[string]string{firefoxURL: "firefox.json"}, func() {
		info, found, available := officialInfo(context.Background(), "www-client/firefox")
		if !found || !available {
			t.Fatalf("officialInfo rejected Firefox fixture: found=%v available=%v", found, available)
		}
		if info.description != "Firefox Web Browser" || info.stable != "140.14.0" || info.latest != "154.0" {
			t.Fatalf("Firefox metadata decoded incorrectly: %+v", info)
		}
		if len(info.local) == 0 || len(info.global) == 0 {
			t.Fatalf("Firefox USE sections decoded empty: local=%d global=%d", len(info.local), len(info.global))
		}
		if len(info.expand) != 2 || info.expand[0].name != "l10n" ||
			len(info.expand[0].flags) != 100 || info.expand[0].flags[0].name != "ach" {
			t.Fatalf("Firefox USE_EXPAND decoded incorrectly: %+v", info.expand)
		}
	})

	const crocURL = "https://packages.gentoo.org/packages/net-misc/croc.json"
	withFixtureHTTP(t, map[string]string{crocURL: "croc.json"}, func() {
		stable, latest, available := pkgVersion(context.Background(), "net-misc/croc")
		if !available || stable != "" || latest != "10.2.7" {
			t.Errorf("masked package versions = available %v stable %q latest %q, want true, empty, and 10.2.7",
				available, stable, latest)
		}
		armStable, armTesting, ok := armStatus(context.Background(), "net-misc/croc")
		if !ok || armStable != "" || armTesting != "" {
			t.Fatalf("globally masked arm64 versions must not be reported available, got ok %v stable %q testing %q",
				ok, armStable, armTesting)
		}
	})
}

func TestGentooPackageSearchLiveFixtures(t *testing.T) {
	const base = "https://packages.gentoo.org/packages/search?q="
	withFixtureHTTP(t, map[string]string{
		base + "vim": "search-vim.html",
		base + "definitely-no-such-package-verifybot-20260825": "search-zero.html",
		base + "lib":          "search-lib.html",
		base + "oh-my-pi-bin": "search-oh-my-pi-bin.html",
	}, func() {
		if hits, available := searchMainTree(context.Background(), "vim"); !available ||
			len(hits) == 0 || hits[0] != "app-editors/vim" {
			t.Fatalf("vim search = available %v hits %v", available, hits)
		}
		if hits, available := searchMainTree(context.Background(), "definitely-no-such-package-verifybot-20260825"); !available ||
			len(hits) != 0 {
			t.Fatalf("zero-result search = available %v hits %v", available, hits)
		}
		if hits, available := searchMainTree(context.Background(), "lib"); !available ||
			len(hits) != maxHitsPerSource {
			t.Fatalf("large search = available %v, %d hits, want bounded %d: %v",
				available, len(hits), maxHitsPerSource, hits)
		}
		if hits, available := searchMainTree(context.Background(), "oh-my-pi-bin"); !available ||
			len(hits) != 0 {
			t.Fatalf("unrelated fuzzy redirect = available %v hits %v", available, hits)
		}
	})
}

func TestOverlayLiveFixtures(t *testing.T) {
	const treeURL = "https://api.github.com/repos/gentoo-zh/overlay/git/trees/master?recursive=1"
	withFixtureHTTP(t, map[string]string{treeURL: "gentoozh-tree.json"}, func() {
		pkgs, err := fetchOverlay(context.Background(), overlay{name: "gentoo-zh", repo: "gentoo-zh/overlay", branch: "master"})
		if err != nil {
			t.Fatalf("fetchOverlay fixture: %v", err)
		}
		if got := pkgs["dev-util/oh-my-pi-bin"]; got != "18.0.4" {
			t.Fatalf("overlay version = %q, want 18.0.4", got)
		}
	})

	const base = "https://raw.githubusercontent.com/gentoo-zh/overlay/master/dev-util/oh-my-pi-bin/"
	withFixtureHTTP(t, map[string]string{
		base + "oh-my-pi-bin-18.0.4.ebuild": "oh-my-pi-bin.ebuild",
		base + "metadata.xml":               "oh-my-pi-bin-metadata.xml",
	}, func() {
		info, ok := overlayInfo(context.Background(),
			overlay{name: "gentoo-zh", repo: "gentoo-zh/overlay", branch: "master"},
			"dev-util/oh-my-pi-bin", "18.0.4")
		if !ok {
			t.Fatal("overlayInfo rejected captured ebuild")
		}
		if info.description != "AI coding agent for the terminal" || info.homepage != "https://omp.sh" ||
			info.latest != "18.0.4" || len(info.local) != 5 {
			t.Fatalf("overlay metadata decoded incorrectly: %+v", info)
		}
	})
}

func TestRepologyLiveFixtures(t *testing.T) {
	withFixtureHTTP(t, map[string]string{
		"https://repology.org/api/v1/project/vim":          "repology-vim.json",
		"https://repology.org/api/v1/project/vmi":          "repology-vmi.json",
		"https://repology.org/api/v1/projects/?search=vmi": "repology-search-vmi.json",
	}, func() {
		proj, pkgs, alts, exact, available := fetchRepology(context.Background(), "vim")
		if !available || !exact || proj != "vim" || len(pkgs) != 2069 || len(alts) != 0 {
			t.Fatalf("direct Repology result = proj %q rows %d alts %v exact %v available %v",
				proj, len(pkgs), alts, exact, available)
		}
		proj, pkgs, alts, exact, available = fetchRepology(context.Background(), "vmi")
		if !available || exact || proj == "" || len(pkgs) == 0 || len(alts) > 5 {
			t.Fatalf("fallback Repology result = proj %q rows %d alts %v exact %v available %v",
				proj, len(pkgs), alts, exact, available)
		}
	})
}

func TestContentLiveFixtures(t *testing.T) {
	const bugURL = "https://bugs.gentoo.org/rest/bug/981294?include_fields=summary,status,resolution,product,component,severity"
	withFixtureHTTP(t, map[string]string{bugURL: "bug-wontfix.json"}, func() {
		info, state := fetchBug(context.Background(), "981294")
		if state != bugLookupFound || info.status != "RESOLVED" || info.resolution != "WONTFIX" ||
			info.product != "Gentoo Linux" {
			t.Fatalf("closed non-FIXED bug decoded incorrectly: state=%v info=%+v", state, info)
		}
	})

	oldNewsURL, oldNewsBase := newsURL, newsBase
	newsURL, newsBase = "https://www.gentoo.org/support/news-items/", "https://www.gentoo.org"
	t.Cleanup(func() { newsURL, newsBase = oldNewsURL, oldNewsBase })
	var items []NewsItem
	withFixtureHTTP(t, map[string]string{newsURL: "gentoo-news.html"}, func() {
		var err error
		items, err = FetchNews(context.Background())
		if err != nil {
			t.Fatalf("FetchNews fixture: %v", err)
		}
	})
	if len(items) == 0 || items[0].Date != "2026-06-28" ||
		items[0].Title != "32-bit s390 support dropped" {
		t.Fatalf("Gentoo news decoded incorrectly: first=%+v count=%d", items[0], len(items))
	}
	foundMismatchedSlug := false
	for _, item := range items {
		if strings.HasSuffix(item.URL, "/2025-10-23-tsm-init-changes.html") {
			foundMismatchedSlug = true
			if item.Date != "2025-09-24" {
				t.Errorf("news date came from URL slug instead of index row: got %q want 2025-09-24", item.Date)
			}
		}
	}
	if !foundMismatchedSlug {
		t.Fatal("captured news item with mismatched slug date was not parsed")
	}

	gentoo := wikiSources[0]
	const gentooSearchURL = "https://wiki.gentoo.org/api.php?action=query&list=search&srsearch=Portage&srlimit=24&srprop=&format=json"
	withFixtureHTTP(t, map[string]string{gentooSearchURL: "gentoo-wiki-search.json"}, func() {
		titles, ok := searchTitles(context.Background(), gentoo, "Portage", 24)
		if !ok || len(titles) != 24 || titles[0] != "Portage/Help/Upgrading Portage" {
			t.Fatalf("Gentoo Wiki search = ok %v titles %v", ok, titles)
		}
	})

	const gentooDisplayURL = "https://wiki.gentoo.org/api.php?action=query&prop=info&inprop=displaytitle&format=json&titles=Portage%2FHelp%2FUpgrading+Portage%7C%2Fvar%2Flib%2Fportage%7CPortage%2FHelp%2FBlockers%7CUseful+Portage+tools%2Fen"
	titles := []string{"Portage/Help/Upgrading Portage", "/var/lib/portage", "Portage/Help/Blockers", "Useful Portage tools/en"}
	withFixtureHTTP(t, map[string]string{gentooDisplayURL: "gentoo-wiki-display.json"}, func() {
		display := displayTitles(context.Background(), gentoo, titles)
		if display["Useful Portage tools/en"] != "Useful Portage tools" || len(display) != 4 {
			t.Fatalf("Gentoo Wiki display titles decoded incorrectly: %v", display)
		}
	})

	arch := wikiSources[1]
	const archSearchURL = "https://wiki.archlinux.org/api.php?action=query&list=search&srsearch=systemd&srlimit=24&srprop=&format=json"
	withFixtureHTTP(t, map[string]string{archSearchURL: "arch-wiki-systemd.json"}, func() {
		titles, ok := searchTitles(context.Background(), arch, "systemd", 24)
		if !ok || len(titles) != 24 || titles[0] != "Systemd" {
			t.Fatalf("ArchWiki search = ok %v titles %v", ok, titles)
		}
	})

	const archDisplayURL = "https://wiki.archlinux.org/api.php?action=query&prop=info&inprop=displaytitle&format=json&titles=Systemd%7CSystemd%2FJournal%7CSystemd%2Ftimers%7CSystemd%2Ftimer"
	archTitles := []string{"Systemd", "Systemd/Journal", "Systemd/timers", "Systemd/timer"}
	withFixtureHTTP(t, map[string]string{archDisplayURL: "arch-wiki-display.json"}, func() {
		display := displayTitles(context.Background(), arch, archTitles)
		if display["Systemd"] != "systemd" || len(display) != 4 {
			t.Fatalf("ArchWiki display titles decoded incorrectly: %v", display)
		}
	})

	for _, tc := range []struct {
		source  wikiSource
		url     string
		fixture string
	}{
		{gentoo, "https://wiki.gentoo.org/api.php?action=query&list=search&srsearch=zqjxvkwt&srlimit=24&srprop=&format=json", "gentoo-wiki-zero.json"},
		{arch, "https://wiki.archlinux.org/api.php?action=query&list=search&srsearch=zqjxvkwt&srlimit=24&srprop=&format=json", "arch-wiki-zero.json"},
	} {
		withFixtureHTTP(t, map[string]string{tc.url: tc.fixture}, func() {
			titles, ok := searchTitles(context.Background(), tc.source, "zqjxvkwt", 24)
			if !ok || len(titles) != 0 {
				t.Fatalf("%s zero-result search = ok %v titles %v", tc.source.name, ok, titles)
			}
		})
	}

	const (
		forumURL     = "https://forum.archlinuxcn.org/search.json?q=portage"
		forumZeroURL = "https://forum.archlinuxcn.org/search.json?q=definitely-no-such-topic-verifybot-20260825"
	)
	withFixtureHTTP(t, map[string]string{
		forumURL:     "archcn-search.json",
		forumZeroURL: "archcn-zero.json",
	}, func() {
		topics, ok := searchArchcn(context.Background(), "portage", 5)
		if !ok || len(topics) != 5 || topics[0].title != "国内真的是没多少用gentoo了吗？" ||
			topics[0].url != "https://forum.archlinuxcn.org/t/topic/8917" {
			t.Fatalf("Arch Linux CN search decoded incorrectly: ok=%v topics=%+v", ok, topics)
		}
		topics, ok = searchArchcn(context.Background(), "definitely-no-such-topic-verifybot-20260825", 5)
		if !ok || len(topics) != 0 {
			t.Fatalf("Arch Linux CN zero-result search = ok %v topics %+v", ok, topics)
		}
	})
}

func TestReleaseMetadataLiveFixtures(t *testing.T) {
	capturedAt := time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC)
	const debianURL = "https://debian.pages.debian.net/distro-info-data/debian.csv"
	var debianData debianReleaseData
	withFixtureHTTP(t, map[string]string{debianURL: "debian.csv"}, func() {
		debianData = fetchDebianStatus(context.Background(), capturedAt)
	})
	debian := debianData.roles
	if debian["13"] != "stable" || debian["12"] != "oldstable" ||
		debian["11"] != "oldoldstable" || debian["14"] != "testing" {
		t.Fatalf("Debian release roles decoded incorrectly: %v", debian)
	}
	relInfo.mu.Lock()
	oldDebianSeries := relInfo.debianSer
	relInfo.debianSer = debianData.series
	relInfo.mu.Unlock()
	t.Cleanup(func() {
		relInfo.mu.Lock()
		relInfo.debianSer = oldDebianSeries
		relInfo.mu.Unlock()
	})

	const ubuntuURL = "https://debian.pages.debian.net/distro-info-data/ubuntu.csv"
	var ubuntuSeries map[string]bool
	withFixtureHTTP(t, map[string]string{ubuntuURL: "ubuntu.csv"}, func() {
		lts, released, eol, series := fetchUbuntu(context.Background(), capturedAt)
		if !lts["26.04"] || !released["26.04"] || released["26.10"] ||
			!eol["20.04"] || series["stonking"] {
			t.Fatalf("Ubuntu release metadata decoded incorrectly: lts=%v released=%v eol=%v series=%v",
				lts, released, eol, series)
		}
		ubuntuSeries = series
	})
	relInfo.mu.Lock()
	oldUbuntuSeries := relInfo.ubuntuSer
	relInfo.ubuntuSer = ubuntuSeries
	relInfo.mu.Unlock()
	t.Cleanup(func() {
		relInfo.mu.Lock()
		relInfo.ubuntuSer = oldUbuntuSeries
		relInfo.mu.Unlock()
	})

	debianMadison := parseMadison(string(upstreamFixture(t, "debian-madison-vim.txt")))
	if len(debianMadison) != 5 || debianMadison[0].suite != "bullseye" ||
		debianMadison[len(debianMadison)-1].suite != "sid" {
		t.Fatalf("Debian Madison decoded incorrectly: %+v", debianMadison)
	}
	suite, version, dev := pickMadison(debianMadison, debianDevSuite)
	if suite != "trixie" || version != "2:9.1.1230-2" || dev {
		t.Fatalf("Debian transition selection = suite %q version %q dev %v", suite, version, dev)
	}
	const debianMadisonBase = "https://qa.debian.org/madison.php?package="
	wantDebian := i18n.Messages.LookupDistros.Armpkgs.Available.Render(i18n.LangEN, "trixie", "2:9.1.1230-2")
	withFixtureHTTP(t, map[string]string{
		debianMadisonBase + "vim&text=on&a=arm64": "debian-madison-vim.txt",
	}, func() {
		if got := madisonArmStatus(context.Background(), i18n.LangEN, debianMadisonBase, "vim", debianDevSuite); got != wantDebian {
			t.Fatalf("Debian Madison status = %q, want %q", got, wantDebian)
		}
	})
	ubuntuMadison := parseMadison(string(upstreamFixture(t, "ubuntu-madison-vim.txt")))
	suite, version, dev = pickMadison(ubuntuMadison, func(s string) bool { return s == "stonking" })
	if suite != "resolute" || version != "2:9.1.2141-1ubuntu4" || dev {
		t.Fatalf("Ubuntu transition selection = suite %q version %q dev %v", suite, version, dev)
	}
	const ubuntuMadisonBase = "https://people.canonical.com/~ubuntu-archive/madison.cgi?package="
	wantUbuntu := i18n.Messages.LookupDistros.Armpkgs.Available.Render(i18n.LangEN, "resolute", "2:9.1.2141-1ubuntu4")
	withFixtureHTTP(t, map[string]string{
		ubuntuMadisonBase + "vim&text=on&a=arm64": "ubuntu-madison-vim.txt",
	}, func() {
		if got := madisonArmStatus(context.Background(), i18n.LangEN, ubuntuMadisonBase, "vim", ubuntuDevSuite); got != wantUbuntu {
			t.Fatalf("Ubuntu Madison status = %q, want %q", got, wantUbuntu)
		}
	})
}

func TestFedoraArmLiveFixture(t *testing.T) {
	want := i18n.Messages.LookupDistros.Armpkgs.FedoraQueryFailed.For(i18n.LangEN)
	const fedoraURL = "https://mdapi.fedoraproject.org/rawhide/pkg/curl"
	withFixtureHTTP(t, map[string]string{fedoraURL: "fedora-curl.json"}, func() {
		if got := fedoraArmStatus(context.Background(), i18n.LangEN, "curl"); got != want {
			t.Fatalf("x86_64 Fedora metadata must not prove arm64 support: got %q want %q", got, want)
		}
	})
}

func TestAURArmLiveFixture(t *testing.T) {
	want := i18n.Messages.LookupDistros.Armpkgs.DeclaresAarch64.For(i18n.LangEN)
	const aurURL = "https://aur.archlinux.org/cgit/aur.git/plain/PKGBUILD?h=yay"
	withFixtureHTTP(t, map[string]string{aurURL: "aur-yay.PKGBUILD"}, func() {
		if got := (&Service{}).aurArmStatus(context.Background(), i18n.LangEN, "yay"); got != want {
			t.Fatalf("AUR PKGBUILD architecture = %q, want %q", got, want)
		}
	})
}

func TestArchLinuxARMLiveFixture(t *testing.T) {
	want := i18n.Messages.LookupDistros.Armpkgs.Packaged.For(i18n.LangEN)
	const alarmURL = "https://archlinuxarm.org/packages/aarch64/curl"
	withFixtureHTTP(t, map[string]string{alarmURL: "alarm-curl.html"}, func() {
		if got := alarmArmStatus(context.Background(), i18n.LangEN, "curl"); got != want {
			t.Fatalf("Arch Linux ARM package page = %q, want %q", got, want)
		}
	})
}
