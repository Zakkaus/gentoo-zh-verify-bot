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

// /armpkgs compares arm64 support across distro-specific architecture APIs.

func (v *Verifier) gentooArmStatus(ctx context.Context, name string) (status, url string) {
	return gentooArmStatusWith(ctx, name, searchMainTree, armStatus)
}

func gentooArmStatusWith(
	ctx context.Context,
	name string,
	search func(context.Context, string) ([]string, bool),
	status func(context.Context, string) (string, string, bool),
) (string, string) {
	searchURL := "https://packages.gentoo.org/packages/search?q=" + neturl.QueryEscape(name)
	atoms, available := search(ctx, name)
	if !available {
		return "⚠️ 查询失败", searchURL
	}
	if len(atoms) == 0 {
		return "❌ 不在官方树", searchURL
	}
	url := "https://packages.gentoo.org/packages/" + atoms[0]
	stable, testing, ok := status(ctx, atoms[0])
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
		return "❌ 未设置 arm64 keyword", url
	}
}

type madEntry struct{ suite, ver string }

// Madison output is oldest-first; pocket variants are excluded and suites deduplicated.
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

// pickMadison prefers the newest released suite, flagging a development-only fallback.
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

// Development suites are never presented as current releases.
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

// Only an authoritative 404 proves absence; all other failures remain unknown.
func fedoraArmStatus(ctx context.Context, pkg string) string {
	return fedoraArmStatusWith(ctx, pkg, func(ctx context.Context, url string) (string, error) {
		var r struct {
			Version string `json:"version"`
		}
		err := httpGetJSON(ctx, url, nil, &r)
		return r.Version, err
	})
}

func fedoraArmStatusWith(
	ctx context.Context,
	pkg string,
	fetch func(context.Context, string) (string, error),
) string {
	version, err := fetch(ctx, "https://mdapi.fedoraproject.org/rawhide/pkg/"+neturl.PathEscape(pkg))
	if err != nil {
		if httpStatusCode(err) == 404 {
			return "❌ 不在 Fedora"
		}
		return "⚠️ Fedora 查询失败"
	}
	if version == "" {
		return "⚠️ Fedora 查询失败"
	}
	return "✅ rawhide " + version
}

var aurArchRe = regexp.MustCompile(`(?i)arch=\(([^)]*)\)`)

// AUR support follows the PKGBUILD arch declaration, not buildability in practice.
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

// Only an AUR 404 proves absence; other failures remain unknown.
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

// Only an Arch Linux ARM 404 proves absence.
func alarmArmStatus(ctx context.Context, pkg string) string {
	if _, err := httpGetBody(ctx, "https://archlinuxarm.org/packages/aarch64/"+neturl.PathEscape(pkg), 1<<10); err != nil {
		if httpStatusCode(err) == 404 {
			return "❌ 未打包"
		}
		return "⚠️ 查询失败"
	}
	return "✅ 已打包"
}

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
	b.WriteString("\n<i>Gentoo 未设置 arm64 keyword;其它发行版有包不等于该 ebuild 可用,需自行测试(必要时在 package.accept_keywords 中写 ** 强制)。</i>")
	v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, b.String())
	return nil
}
