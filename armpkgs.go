package main

import (
	"context"
	"fmt"
	"html"
	neturl "net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// /armpkgs checks arm64 (aarch64) support for a package across distros that expose a clean
// per-architecture API: Gentoo (keywords), Debian + Ubuntu (madison, arch-filtered),
// Fedora (mdapi; aarch64 is a primary arch), Arch Linux ARM (package presence) and AUR
// (PKGBUILD arch=()). This complements /arm (Gentoo only), since Gentoo's arm64 keywording is sometimes incomplete
// while a package may well be available on other ARM distros.

// gentooArmStatus resolves a Gentoo atom and reports its arm64 keyword status plus a link
// to the package's packages.gentoo.org page (a search link if the atom doesn't resolve).
func (v *Verifier) gentooArmStatus(ctx context.Context, name string) (status, url string) {
	atoms := searchMainTree(ctx, name)
	if len(atoms) == 0 {
		return "❌ 不在官方树", "https://packages.gentoo.org/packages/search?q=" + neturl.QueryEscape(name)
	}
	url = "https://packages.gentoo.org/packages/" + atoms[0]
	stable, testing, ok := armStatus(ctx, atoms[0])
	switch {
	case !ok:
		return "⚠️ 查询失败", url
	case stable != "" && testing != "":
		return fmt.Sprintf("✅ 稳定 %s · 🧪 ~%s", stable, testing), url
	case stable != "":
		return "✅ 稳定 " + stable, url
	case testing != "":
		return "🧪 仅 ~arm64 " + testing, url
	default:
		return "❌ 未 keyword arm64", url
	}
}

// madEntry is one "suite: version" row parsed from a madison listing.
type madEntry struct{ suite, ver string }

// parseMadison parses Debian/Ubuntu madison text ("pkg | version | suite | arch" per line)
// into base-release suites (pocket variants like -updates/-security are skipped), newest
// first, deduped. madison lists oldest-first, so the last distinct suites are the newest.
func parseMadison(body string) []madEntry {
	var ordered []madEntry
	idx := map[string]int{}
	for _, ln := range strings.Split(body, "\n") {
		parts := strings.Split(ln, "|")
		if len(parts) < 4 {
			continue
		}
		ver := strings.TrimSpace(parts[1])
		suite := strings.SplitN(strings.TrimSpace(parts[2]), "/", 2)[0] // drop "/universe" etc.
		if ver == "" || suite == "" || strings.Contains(suite, "-") {   // skip -updates/-security/-backports
			continue
		}
		if i, ok := idx[suite]; ok {
			ordered[i].ver = ver // newer line for the same suite wins
			continue
		}
		idx[suite] = len(ordered)
		ordered = append(ordered, madEntry{suite, ver})
	}
	return ordered
}

// pickMadison chooses the suite/version to display from madison entries (oldest-first): the
// newest RELEASED suite, falling back to the newest overall (dev=true, for the caller to flag)
// when only an unreleased development series carries the package. devSuite (optional) reports
// whether a suite is an unreleased dev series; nil means never (keep the newest, e.g. Debian sid).
func pickMadison(entries []madEntry, devSuite func(string) bool) (suite, ver string, dev bool) {
	pick := entries[len(entries)-1] // madison lists oldest-first, so the last is the newest suite
	dev = devSuite != nil && devSuite(pick.suite)
	if dev {
		for i := len(entries) - 2; i >= 0; i-- {
			if !devSuite(entries[i].suite) {
				return entries[i].suite, entries[i].ver, false
			}
		}
	}
	return pick.suite, pick.ver, dev
}

// madisonArmStatus queries a madison endpoint (arch-filtered to arm64) and reports the newest
// suite that ships the package on arm64. devSuite (optional) flags an unreleased dev series so an
// unshipped future release isn't presented as current; a Snap transitional version shows as "snap".
func madisonArmStatus(ctx context.Context, madisonURL, pkg string, devSuite func(string) bool) string {
	body, err := httpGetBody(ctx, madisonURL+neturl.QueryEscape(pkg)+"&text=on&a=arm64", 1<<20)
	if err != nil {
		return "⚠️ 查询失败"
	}
	entries := parseMadison(string(body))
	if len(entries) == 0 {
		return "❌ 无 arm64 包"
	}
	suite, ver, dev := pickMadison(entries, devSuite)
	if dev {
		suite += "(开发版)"
	}
	return fmt.Sprintf("✅ %s %s", suite, displayVer(ver))
}

// fedoraArmStatus checks Fedora rawhide via mdapi. aarch64 is a Fedora primary arch, so a
// package present in Fedora is built for aarch64 (barring an explicit ExcludeArch).
func fedoraArmStatus(ctx context.Context, pkg string) string {
	var r struct {
		Version string `json:"version"`
	}
	if err := httpGetJSON(ctx, "https://mdapi.fedoraproject.org/rawhide/pkg/"+neturl.PathEscape(pkg), nil, &r); err != nil || r.Version == "" {
		return "❌ 不在 Fedora"
	}
	return "✅ rawhide " + r.Version
}

var aurArchRe = regexp.MustCompile(`(?i)arch=\(([^)]*)\)`)

// aurArchLabel reads a PKGBUILD's arch=() declaration. "any" is architecture-independent;
// "aarch64" is declared arm64; "arm/armv6h/armv7h" are 32-bit ARM only; otherwise x86-only
// (the package may still build on arm64, the maintainer just didn't declare it).
func aurArchLabel(pkgbuild string) string {
	m := aurArchRe.FindStringSubmatch(pkgbuild)
	if m == nil {
		return "⚠️ 无法解析 PKGBUILD"
	}
	arch := strings.ToLower(m[1])
	switch {
	case strings.Contains(arch, "any"):
		return "✅ any(架构无关)"
	case strings.Contains(arch, "aarch64"):
		return "✅ 声明 aarch64"
	case strings.Contains(arch, "arm"):
		return "🟡 仅 32 位 ARM(无 aarch64)"
	default:
		return "❌ 仅 x86(PKGBUILD 未声明 aarch64;源码构建有时仍可)"
	}
}

// aurArmStatus fetches an AUR package's PKGBUILD and reports its declared arch support. A 404 means
// the package really isn't in the AUR; any other failure (timeout/5xx/network) is reported as a
// query failure rather than a false "not in AUR".
func (v *Verifier) aurArmStatus(ctx context.Context, pkg string) string {
	body, err := httpGetBody(ctx, "https://aur.archlinux.org/cgit/aur.git/plain/PKGBUILD?h="+neturl.QueryEscape(pkg), 64<<10)
	if err != nil {
		if httpStatusCode(err) == 404 {
			return "❌ 不在 AUR"
		}
		return "⚠️ AUR 查询失败"
	}
	return aurArchLabel(string(body))
}

// alarmArmStatus checks whether Arch Linux ARM packages the name for aarch64 (200 vs 404). A non-404
// failure is reported as a query failure, not a false "not packaged".
func alarmArmStatus(ctx context.Context, pkg string) string {
	if _, err := httpGetBody(ctx, "https://archlinuxarm.org/packages/aarch64/"+neturl.PathEscape(pkg), 1<<10); err != nil {
		if httpStatusCode(err) == 404 {
			return "❌ 未打包"
		}
		return "⚠️ 查询失败"
	}
	return "✅ 已打包"
}

// onArmpkgs handles /armpkgs <pkg> — cross-distro arm64 (aarch64) support.
func (v *Verifier) onArmpkgs(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.queryAllowed(ctx, msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	name := commandArg(msg.Text)
	if name == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, "用法:/armpkgs <包名>,例如 /armpkgs htop。查该包在各发行版 arm64 (aarch64) 上的支持(Gentoo / Debian / Ubuntu / Fedora / Arch Linux ARM / AUR)。")
		return nil
	}
	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	ensureReleaseInfo(hc, time.Now()) // load Ubuntu series status so an unreleased dev suite is flagged
	pe := neturl.PathEscape(name)

	// Each source is independent — query them concurrently. fn returns (status, link).
	sources := []struct {
		label string
		fn    func() (string, string)
	}{
		{"Gentoo", func() (string, string) { return v.gentooArmStatus(hc, name) }},
		{"Debian", func() (string, string) {
			return madisonArmStatus(hc, "https://qa.debian.org/madison.php?package=", name, nil), "https://tracker.debian.org/pkg/" + pe
		}},
		{"Ubuntu", func() (string, string) {
			return madisonArmStatus(hc, "https://people.canonical.com/~ubuntu-archive/madison.cgi?package=", name, ubuntuDevSuite), "https://launchpad.net/ubuntu/+source/" + pe
		}},
		{"Fedora", func() (string, string) {
			return fedoraArmStatus(hc, name), "https://packages.fedoraproject.org/pkgs/" + pe + "/"
		}},
		{"Arch Linux ARM", func() (string, string) {
			return alarmArmStatus(hc, name), "https://archlinuxarm.org/packages/aarch64/" + pe
		}},
		{"AUR", func() (string, string) { return v.aurArmStatus(hc, name), "https://aur.archlinux.org/packages/" + pe }},
	}
	type srcResult struct{ label, status, url string }
	results := make([]srcResult, len(sources))
	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Add(1)
		go func(i int, label string, fn func() (string, string)) {
			defer wg.Done()
			status, url := fn()
			results[i] = srcResult{label, status, url}
		}(i, s.label, s.fn)
	}
	wg.Wait()

	esc := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, "🦾 <b>%s</b> · arm64 (aarch64) 跨发行版支持", esc(name))
	for _, r := range results {
		fmt.Fprintf(&b, "\n • <a href=\"%s\">%s</a>:%s", esc(r.url), esc(r.label), esc(r.status))
	}
	b.WriteString("\n<i>Gentoo 未标但其它发行版支持 → 多半实际可用,可 ACCEPT_KEYWORDS=\"~arm64\" 自行编译。</i>")
	v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, b.String())
	return nil
}
