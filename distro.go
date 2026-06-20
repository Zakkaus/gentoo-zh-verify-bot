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
// to a family if it equals the prefix or starts with it, e.g. debian_12). search is a
// printf template (%s = url-escaped project) to that distro's package page, so the family
// label is clickable.
var distroFamilies = []struct{ label, prefix, search string }{
	{"AUR", "aur", "https://aur.archlinux.org/packages?K=%s"},
	{"Arch", "arch", "https://archlinux.org/packages/?q=%s"},
	{"Debian", "debian_", "https://packages.debian.org/search?keywords=%s"},
	{"Ubuntu", "ubuntu_", "https://packages.ubuntu.com/search?keywords=%s"},
	{"Nixpkgs", "nix_", "https://search.nixos.org/packages?query=%s"},
	{"openSUSE", "opensuse_", "https://software.opensuse.org/search?q=%s"},
	{"Fedora", "fedora_", "https://packages.fedoraproject.org/pkgs/%s/"},
}

func famOf(repo string) string {
	for _, f := range distroFamilies {
		if repo == f.prefix || strings.HasPrefix(repo, f.prefix) {
			return f.label
		}
	}
	return ""
}

// dateSnapshot reports whether v starts with a YYYY-MM-DD date (a git/rolling snapshot
// rather than a release), so it isn't ranked above real versions by numeric compare.
func dateSnapshot(v string) bool {
	if len(v) < 10 || v[4] != '-' || v[7] != '-' {
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

// betterVer reports whether cand should replace cur as a family's shown version: a real
// release always beats a date snapshot; otherwise the higher version (verLess) wins.
func betterVer(cur, cand string) bool {
	if cs, ds := dateSnapshot(cur), dateSnapshot(cand); cs != ds {
		return cs // cur is a snapshot and cand isn't -> cand is better
	}
	return verLess(cur, cand)
}

func repologyVersionsURL(proj string) string {
	return "https://repology.org/project/" + neturl.PathEscape(proj) + "/versions"
}

// fetchRepology resolves a package via Repology. On an exact project match it returns the
// project name + its rows. Otherwise it returns suggestions — the closest project names
// that are actually packaged in the distros we show, ranked by distro coverage — so a
// vague query ("kernel") offers real alternatives instead of a wrong silent pick.
func fetchRepology(ctx context.Context, name string) (proj string, pkgs []repologyPkg, suggestions []string) {
	q := strings.ToLower(strings.TrimSpace(name))
	if q == "" {
		return "", nil, nil
	}
	if err := httpGetJSON(ctx, "https://repology.org/api/v1/project/"+neturl.PathEscape(q), nil, &pkgs); err == nil && len(pkgs) > 0 {
		return q, pkgs, nil
	}
	var found map[string][]repologyPkg
	if err := httpGetJSON(ctx, "https://repology.org/api/v1/projects/?search="+neturl.QueryEscape(q), nil, &found); err != nil {
		return "", nil, nil
	}
	if p, ok := found[q]; ok { // exact name surfaced by the search
		return q, p, nil
	}
	type cand struct {
		name string
		fams int
	}
	cands := make([]cand, 0, len(found))
	for n, ps := range found {
		if strings.Contains(n, ":") {
			continue // skip Repology's language-namespaced projects (go:…, haskell:…) in suggestions
		}
		fset := map[string]bool{}
		for _, p := range ps {
			if f := famOf(p.Repo); f != "" {
				fset[f] = true
			}
		}
		if len(fset) > 0 { // only suggest packages that exist in distros we show
			cands = append(cands, cand{n, len(fset)})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].fams != cands[j].fams {
			return cands[i].fams > cands[j].fams
		}
		return cands[i].name < cands[j].name
	})
	for i := 0; i < len(cands) && i < 6; i++ {
		suggestions = append(suggestions, cands[i].name)
	}
	return "", nil, suggestions
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
		v.notify(c, bot, msg.Chat.ID, "用法:/distro <包名>,例如 /distro firefox。跨发行版查版本(AUR / Arch / Debian / Ubuntu / Nix / openSUSE / Fedora)。")
		return nil
	}
	hc, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	proj, pkgs, suggestions := fetchRepology(hc, name)
	esc := html.EscapeString

	if len(pkgs) == 0 {
		if len(suggestions) == 0 {
			v.notify(c, bot, msg.Chat.ID, fmt.Sprintf("❓ Repology 没找到「%s」。", name))
			return nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "❓ 没找到精确匹配「%s」。相关包(点击查看):\n", esc(name))
		for i, s := range suggestions {
			if i > 0 {
				b.WriteString(" · ")
			}
			fmt.Fprintf(&b, "<a href=\"%s\">%s</a>", esc(repologyVersionsURL(s)), esc(s))
		}
		_, _ = bot.SendMessage(c, htmlMessage(msg.Chat.ID, b.String()))
		return nil
	}

	// newest version per family (date-snapshot-aware)
	best := map[string]string{}
	for _, p := range pkgs {
		if fam := famOf(p.Repo); fam != "" && p.Version != "" {
			if cur, ok := best[fam]; !ok || betterVer(cur, p.Version) {
				best[fam] = p.Version
			}
		}
	}

	verURL, projEsc, qproj := esc(repologyVersionsURL(proj)), esc(proj), neturl.QueryEscape(proj)
	var plain, rich strings.Builder
	fmt.Fprintf(&plain, "📦 <a href=\"%s\">%s</a> 跨发行版版本:", verURL, projEsc)
	fmt.Fprintf(&rich, "<h3>📦 <a href=\"%s\">%s</a> 跨发行版版本</h3><ul>", verURL, projEsc)
	any := false
	for _, f := range distroFamilies {
		ver, ok := best[f.label]
		if !ok {
			continue
		}
		any = true
		famLink := fmt.Sprintf("<a href=\"%s\">%s</a>", esc(fmt.Sprintf(f.search, qproj)), f.label)
		fmt.Fprintf(&plain, "\n • <b>%s</b>:%s", famLink, esc(ver))
		fmt.Fprintf(&rich, "<li><b>%s</b>:%s</li>", famLink, esc(ver))
	}
	rich.WriteString("</ul>")
	if !any {
		v.notify(c, bot, msg.Chat.ID, fmt.Sprintf("「%s」在 AUR / Arch / Debian / Ubuntu / Nix / openSUSE / Fedora 里都没有打包(可能是某发行版专属)。", proj))
		return nil
	}
	v.sendRichOrHTML(c, bot, msg.Chat.ID, rich.String(), plain.String())
	return nil
}
