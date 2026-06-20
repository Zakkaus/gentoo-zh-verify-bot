package main

import (
	"context"
	"fmt"
	"html"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// /armpkgs checks arm64 (aarch64) support for a package across distros that expose a clean
// per-architecture API: Gentoo (keywords), Debian + Ubuntu (madison, arch-filtered),
// Fedora (mdapi; aarch64 is a primary arch) and Arch Linux ARM (package presence). This
// complements /arm (Gentoo only), since Gentoo's arm64 keywording is sometimes incomplete
// while a package may well be available on other ARM distros.

// gentooArmStatus resolves a Gentoo atom and reports its arm64 keyword status.
func (v *Verifier) gentooArmStatus(ctx context.Context, name string) string {
	atoms := searchMainTree(ctx, name)
	if len(atoms) == 0 {
		return "❌ 不在 Gentoo 官方树"
	}
	stable, testing, ok := armStatus(ctx, atoms[0])
	switch {
	case !ok:
		return "⚠️ 查询失败"
	case stable != "" && testing != "":
		return fmt.Sprintf("✅ 稳定 %s · 🧪 ~%s", stable, testing)
	case stable != "":
		return "✅ 稳定 " + stable
	case testing != "":
		return "🧪 仅 ~arm64 " + testing
	default:
		return "❌ 未 keyword arm64"
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

// madisonArmStatus queries a madison endpoint (arch-filtered to arm64) and summarises the
// newest few suites that ship the package on arm64.
func madisonArmStatus(ctx context.Context, madisonURL, pkg string) string {
	body, err := httpGetBody(ctx, madisonURL+neturl.QueryEscape(pkg)+"&text=on&a=arm64", 1<<20)
	if err != nil {
		return "⚠️ 查询失败"
	}
	entries := parseMadison(string(body))
	if len(entries) == 0 {
		return "❌ 无 arm64 包"
	}
	// newest first, at most 3
	var parts []string
	for i := len(entries) - 1; i >= 0 && len(parts) < 3; i-- {
		parts = append(parts, entries[i].suite+" "+entries[i].ver)
	}
	return "✅ " + strings.Join(parts, " · ")
}

// fedoraArmStatus checks Fedora rawhide via mdapi. aarch64 is a Fedora primary arch, so a
// package present in Fedora is built for aarch64 (barring an explicit ExcludeArch).
func fedoraArmStatus(ctx context.Context, pkg string) string {
	var r struct {
		Version string `json:"version"`
		Release string `json:"release"`
	}
	if err := httpGetJSON(ctx, "https://mdapi.fedoraproject.org/rawhide/pkg/"+neturl.PathEscape(pkg), nil, &r); err != nil || r.Version == "" {
		return "❌ 未找到(Fedora rawhide)"
	}
	return fmt.Sprintf("✅ rawhide %s(aarch64 主架构)", r.Version)
}

// alarmArmStatus checks whether Arch Linux ARM packages the name for aarch64 (200 vs 404).
func alarmArmStatus(ctx context.Context, pkg string) string {
	if _, err := httpGetBody(ctx, "https://archlinuxarm.org/packages/aarch64/"+neturl.PathEscape(pkg), 1<<10); err != nil {
		return "❌ 未打包"
	}
	return "✅ 已打包"
}

// onArmpkgs handles /armpkgs <pkg> — cross-distro arm64 (aarch64) support.
func (v *Verifier) onArmpkgs(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	name := commandArg(msg.Text)
	if name == "" {
		v.notify(c, bot, msg.Chat.ID, "用法:/armpkgs <包名>,例如 /armpkgs htop。查该包在各发行版 arm64 (aarch64) 上的支持(Gentoo / Debian / Ubuntu / Fedora / Arch Linux ARM)。")
		return nil
	}
	hc, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// Each source is independent — query them concurrently.
	type srcResult struct{ label, status string }
	sources := []struct {
		label string
		fn    func() string
	}{
		{"Gentoo", func() string { return v.gentooArmStatus(hc, name) }},
		{"Debian", func() string { return madisonArmStatus(hc, "https://qa.debian.org/madison.php?package=", name) }},
		{"Ubuntu", func() string {
			return madisonArmStatus(hc, "https://people.canonical.com/~ubuntu-archive/madison.cgi?package=", name)
		}},
		{"Fedora", func() string { return fedoraArmStatus(hc, name) }},
		{"Arch Linux ARM", func() string { return alarmArmStatus(hc, name) }},
	}
	results := make([]srcResult, len(sources))
	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Add(1)
		go func(i int, label string, fn func() string) {
			defer wg.Done()
			results[i] = srcResult{label, fn()}
		}(i, s.label, s.fn)
	}
	wg.Wait()

	esc := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, "🦾 <b>%s</b> 的 ARM (aarch64) 跨发行版支持:", esc(name))
	for _, r := range results {
		fmt.Fprintf(&b, "\n • <b>%s</b>:%s", esc(r.label), esc(r.status))
	}
	b.WriteString("\n<i>提示:若 Gentoo 未 keyword arm64 但其它发行版已支持,通常意味着实际可用 —— 可 ACCEPT_KEYWORDS=\"~arm64\" 强制开启自行编译。各发行版按各自包名查询;AUR 为源码构建未列入。</i>")
	sent, _ := bot.SendMessage(c, htmlMessage(msg.Chat.ID, b.String()).WithReplyParameters(replyParams(msg.MessageID)))
	v.scheduleLookupCleanup(bot, msg.Chat.ID, msg.MessageID, msgID(sent))
	return nil
}
