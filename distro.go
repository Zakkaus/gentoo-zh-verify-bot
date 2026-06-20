package main

import (
	"context"
	"fmt"
	"html"
	neturl "net/url"
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

// distroFamilies maps Repology repo ids to the families /distro surfaces, in display order.
// A repo belongs to a family if it equals the prefix or starts with it (e.g. debian_12).
var distroFamilies = []struct{ label, prefix string }{
	{"AUR", "aur"},
	{"Arch", "arch"},
	{"Debian", "debian_"},
	{"Ubuntu", "ubuntu_"},
	{"Nixpkgs", "nix_"},
	{"openSUSE", "opensuse_"},
	{"Fedora", "fedora_"},
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
// rather than a release), so it doesn't get ranked above real versions by numeric compare.
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

// fetchRepology resolves a package via Repology, returning the matched project name and
// its package rows. It tries the exact project first, then a search; empty slice = no match.
func fetchRepology(ctx context.Context, name string) (string, []repologyPkg) {
	q := strings.ToLower(strings.TrimSpace(name))
	if q == "" {
		return "", nil
	}
	var pkgs []repologyPkg
	if err := httpGetJSON(ctx, "https://repology.org/api/v1/project/"+neturl.PathEscape(q), nil, &pkgs); err == nil && len(pkgs) > 0 {
		return q, pkgs
	}
	// fallback: search returns {projectname: [pkgs], ...}
	var found map[string][]repologyPkg
	if err := httpGetJSON(ctx, "https://repology.org/api/v1/projects/?search="+neturl.QueryEscape(q), nil, &found); err != nil {
		return "", nil
	}
	best := "" // prefer an exact name, else the shortest (closest) project name
	for proj := range found {
		if proj == q {
			return proj, found[proj]
		}
		if best == "" || len(proj) < len(best) {
			best = proj
		}
	}
	return best, found[best]
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
	proj, pkgs := fetchRepology(hc, name)
	if len(pkgs) == 0 {
		v.notify(c, bot, msg.Chat.ID, fmt.Sprintf("❓ Repology 没找到「%s」。", name))
		return nil
	}

	// newest version seen per family (verLess gives a best-effort ordering)
	best := map[string]string{}
	for _, p := range pkgs {
		fam := famOf(p.Repo)
		if fam == "" || p.Version == "" {
			continue
		}
		if cur, ok := best[fam]; !ok || betterVer(cur, p.Version) {
			best[fam] = p.Version
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "📦 <a href=\"https://repology.org/project/%s/versions\">%s</a> 跨发行版版本:",
		neturl.PathEscape(proj), html.EscapeString(proj))
	any := false
	for _, f := range distroFamilies {
		if ver, ok := best[f.label]; ok {
			fmt.Fprintf(&b, "\n • <b>%s</b>:%s", f.label, html.EscapeString(ver))
			any = true
		}
	}
	if !any {
		b.WriteString("\n(这些发行版里都没有打包)")
	}
	_, _ = bot.SendMessage(c, htmlMessage(msg.Chat.ID, b.String()))
	return nil
}
