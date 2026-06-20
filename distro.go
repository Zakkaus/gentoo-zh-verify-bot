package main

import (
	"context"
	"fmt"
	"html"
	neturl "net/url"
	"sort"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// repologyPkg is one repo's row from the Repology project API.
type repologyPkg struct {
	Repo    string `json:"repo"`
	Version string `json:"version"`
}

// distroFamilies maps Repology repo ids to the families /distro surfaces (a repo belongs
// to a family if it equals one of its prefixes or starts with one, e.g. debian_12).
// Variants of one ecosystem are listed separately (Fedora vs RHEL/EPEL, openSUSE Leap vs
// Tumbleweed) since their versions differ a lot. search is a printf template
// (%s = url-escaped project) to that distro's package page, so the label is clickable.
var distroFamilies = []struct {
	label    string
	prefixes []string
	search   string
}{
	{"Gentoo", []string{"gentoo"}, "https://packages.gentoo.org/packages/search?q=%s"},
	{"AUR", []string{"aur"}, "https://aur.archlinux.org/packages?K=%s"},
	{"Arch", []string{"arch"}, "https://archlinux.org/packages/?q=%s"},
	{"Alpine", []string{"alpine_"}, "https://pkgs.alpinelinux.org/packages?name=%s"},
	{"Debian", []string{"debian_"}, "https://packages.debian.org/search?keywords=%s"},
	{"Ubuntu", []string{"ubuntu_"}, "https://packages.ubuntu.com/search?keywords=%s"},
	{"Nixpkgs", []string{"nix_"}, "https://search.nixos.org/packages?query=%s"},
	{"Fedora", []string{"fedora_"}, "https://packages.fedoraproject.org/pkgs/%s/"},
	{"RHEL/EPEL", []string{"epel_", "centos_", "almalinux_", "rockylinux_", "rhel_"}, "https://packages.fedoraproject.org/pkgs/%s/"},
	{"openSUSE Leap", []string{"opensuse_leap"}, "https://software.opensuse.org/search?q=%s"},
	{"openSUSE Tumbleweed", []string{"opensuse_tumbleweed"}, "https://software.opensuse.org/search?q=%s"},
}

func famOf(repo string) string {
	for _, f := range distroFamilies {
		for _, p := range f.prefixes {
			if repo == p || strings.HasPrefix(repo, p) {
				return f.label
			}
		}
	}
	return ""
}

// dateSnapshot reports whether v starts with a YYYY-MM-DD or YYYY.MM.DD date (a git/
// snapshot ebuild rather than a release), so it isn't ranked above real versions by numeric
// compare. betterVer only deprioritizes it when a non-date version exists in the same
// family, so genuine calendar-versioned projects (yt-dlp, etc.) still compare correctly.
func dateSnapshot(v string) bool {
	if len(v) < 10 {
		return false
	}
	sep := v[4]
	if (sep != '-' && sep != '.') || v[7] != sep {
		return false
	}
	for i := 0; i < 10; i++ {
		if i == 4 || i == 7 {
			continue
		}
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	return true
}

// allNines reports whether v is a Gentoo live ebuild version (9999 / 9999.9999 …),
// which tracks git HEAD rather than a real release.
func allNines(v string) bool {
	nine := false
	for i := 0; i < len(v); i++ {
		switch {
		case v[i] == '9':
			nine = true
		case v[i] == '.':
		default:
			return false
		}
	}
	return nine
}

// verTier ranks a version so a family shows its real packaged version: 0 = real release,
// 1 = a date / CalVer (could be a snapshot OR a genuine version like yt-dlp's), 2 = a
// Gentoo 9999 live ebuild (tracks git HEAD). Lower is preferred.
func verTier(v string) int {
	switch {
	case allNines(v):
		return 2
	case dateSnapshot(v):
		return 1
	default:
		return 0
	}
}

// betterVer reports whether cand should replace cur as a family's shown version: a better
// (lower) tier wins — real release > date > 9999 live ebuild — otherwise, within the same
// tier, the higher version (verLess) wins. So a real release isn't masked by a live ebuild,
// but a project that only has date versions still shows its newest date.
func betterVer(cur, cand string) bool {
	if ct, nt := verTier(cur), verTier(cand); ct != nt {
		return nt < ct
	}
	return verLess(cur, cand)
}

func repologyVersionsURL(proj string) string {
	return "https://repology.org/project/" + neturl.PathEscape(proj) + "/versions"
}

// fetchRepology resolves a package via Repology. On an exact project match it returns that
// project (exact=true). Otherwise it picks the closest project that is actually packaged in
// the distros we show — ranked by distro coverage — as the result, plus a few alternatives,
// so a near-miss / vague query still yields a real cross-distro table instead of nothing.
func fetchRepology(ctx context.Context, name string) (proj string, pkgs []repologyPkg, alts []string, exact bool) {
	q := strings.ToLower(strings.TrimSpace(name))
	if q == "" {
		return "", nil, nil, false
	}
	if err := httpGetJSON(ctx, "https://repology.org/api/v1/project/"+neturl.PathEscape(q), nil, &pkgs); err == nil && len(pkgs) > 0 {
		return q, pkgs, nil, true
	}
	var found map[string][]repologyPkg
	if err := httpGetJSON(ctx, "https://repology.org/api/v1/projects/?search="+neturl.QueryEscape(q), nil, &found); err != nil {
		return "", nil, nil, false
	}
	if p, ok := found[q]; ok { // exact name surfaced by the search
		return q, p, nil, true
	}
	type cand struct {
		name string
		fams int
	}
	cands := make([]cand, 0, len(found))
	for n, ps := range found {
		if strings.Contains(n, ":") {
			continue // skip Repology's language-namespaced projects (go:…, haskell:…)
		}
		fset := map[string]bool{}
		for _, p := range ps {
			if f := famOf(p.Repo); f != "" {
				fset[f] = true
			}
		}
		if len(fset) > 0 { // only consider packages that exist in distros we show
			cands = append(cands, cand{n, len(fset)})
		}
	}
	if len(cands) == 0 {
		return "", nil, nil, false
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].fams != cands[j].fams {
			return cands[i].fams > cands[j].fams
		}
		return cands[i].name < cands[j].name
	})
	for i := 1; i < len(cands) && i <= 5; i++ {
		alts = append(alts, cands[i].name)
	}
	return cands[0].name, found[cands[0].name], alts, false
}

// onDistro handles /distro <pkg> — cross-distro package versions via Repology.
func (v *Verifier) onDistro(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	name := commandArg(msg.Text)
	if name == "" {
		v.notify(c, bot, msg.Chat.ID, "用法:/distro <包名>,例如 /distro firefox。跨发行版查版本(Gentoo / AUR / Arch / Alpine / Debian / Ubuntu / Nix / Fedora / RHEL / openSUSE)。")
		return nil
	}
	hc, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	proj, pkgs, alts, exact := fetchRepology(hc, name)
	esc := html.EscapeString
	if len(pkgs) == 0 {
		v.notify(c, bot, msg.Chat.ID, fmt.Sprintf("❓ 在 Repology 没找到和「%s」相关的跨发行版包,试试更精确的包名。", name))
		return nil
	}

	// newest version per family from Repology
	best := map[string]string{}
	for _, p := range pkgs {
		if fam := famOf(p.Repo); fam != "" && p.Version != "" {
			if cur, ok := best[fam]; !ok || betterVer(cur, p.Version) {
				best[fam] = p.Version
			}
		}
	}

	// Build the displayed lines. Gentoo is special: use the bot's own packages.gentoo.org
	// data so amd64-stable and ~amd64 testing show on SEPARATE lines (Repology can't express
	// Gentoo keyword status). All other families come from Repology, one line each.
	type distroLine struct{ label, ver, url string }
	var lines []distroLine
	if atoms := searchMainTree(hc, proj); len(atoms) > 0 {
		atom := atoms[0]
		if pkgName := atom[strings.LastIndexByte(atom, '/')+1:]; strings.EqualFold(pkgName, proj) {
			gURL := "https://packages.gentoo.org/packages/" + atom
			stable, testing := pkgVersion(hc, atom)
			switch {
			case stable != "" && testing != "" && stable != testing:
				lines = append(lines, distroLine{"Gentoo amd64", stable, gURL}, distroLine{"Gentoo ~amd64", testing, gURL})
			case testing != "":
				lines = append(lines, distroLine{"Gentoo ~amd64", testing, gURL})
			case stable != "":
				lines = append(lines, distroLine{"Gentoo amd64", stable, gURL})
			}
		}
	}
	qproj := neturl.QueryEscape(proj)
	for _, f := range distroFamilies {
		if f.label == "Gentoo" {
			if len(lines) == 0 { // bot lookup found nothing -> fall back to Repology's gentoo version
				if ver, ok := best["Gentoo"]; ok {
					lines = append(lines, distroLine{"Gentoo", ver, fmt.Sprintf(f.search, qproj)})
				}
			}
			continue
		}
		if ver, ok := best[f.label]; ok {
			lines = append(lines, distroLine{f.label, ver, fmt.Sprintf(f.search, qproj)})
		}
	}
	if len(lines) == 0 {
		v.notify(c, bot, msg.Chat.ID, fmt.Sprintf("「%s」在 Gentoo / AUR / Arch / Alpine / Debian / Ubuntu / Nix / Fedora / RHEL / openSUSE 里都没有打包(可能是某发行版专属)。", proj))
		return nil
	}

	head := fmt.Sprintf("📦 <a href=\"%s\">%s</a> 跨发行版版本", esc(repologyVersionsURL(proj)), esc(proj))
	if !exact {
		head += fmt.Sprintf(" <i>(「%s」最接近的匹配)</i>", esc(name))
	}
	var plain, rich strings.Builder
	plain.WriteString(head + ":")
	rich.WriteString("<h3>" + head + "</h3><ul>")
	for _, ln := range lines {
		famLink := fmt.Sprintf("<a href=\"%s\">%s</a>", esc(ln.url), esc(ln.label))
		fmt.Fprintf(&plain, "\n • <b>%s</b>:%s", famLink, esc(ln.ver))
		fmt.Fprintf(&rich, "<li><b>%s</b>:%s</li>", famLink, esc(ln.ver))
	}
	rich.WriteString("</ul>")
	if len(alts) > 0 {
		var al strings.Builder
		for i, a := range alts {
			if i > 0 {
				al.WriteString(" · ")
			}
			fmt.Fprintf(&al, "<a href=\"%s\">%s</a>", esc(repologyVersionsURL(a)), esc(a))
		}
		fmt.Fprintf(&plain, "\n其它匹配:%s", al.String())
		// collapsible in rich messages so the main table stays compact
		fmt.Fprintf(&rich, "<details><summary>其它匹配 (%d)</summary>%s</details>", len(alts), al.String())
	}
	v.sendRichOrHTML(c, bot, msg.Chat.ID, rich.String(), plain.String())
	return nil
}
