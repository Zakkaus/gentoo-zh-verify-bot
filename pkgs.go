package main

import (
	"context"
	"fmt"
	"html"
	neturl "net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

type repologyPkg struct {
	Repo    string `json:"repo"`
	Version string `json:"version"`
}

// Repo prefixes define displayed families; relabel derives live release roles.
// RHEL rebuilds, CentOS Stream, and EPEL remain separate version channels.
var distroFamilies = []struct {
	label    string
	prefixes []string
	search   string
	relabel  func(string) string
}{
	{"Gentoo", []string{"gentoo"}, "https://packages.gentoo.org/packages/search?q=%s", nil},
	{"AUR", []string{"aur"}, "https://aur.archlinux.org/packages?K=%s", nil},
	{"Arch", []string{"arch"}, "https://archlinux.org/packages/?q=%s", nil},
	{"Alpine", []string{"alpine_"}, "https://pkgs.alpinelinux.org/packages?name=%s", nil},
	{"Debian", []string{"debian_"}, "https://tracker.debian.org/pkg/%s", debianRelabel},
	{"Ubuntu", []string{"ubuntu_"}, "https://launchpad.net/ubuntu/+source/%s", ubuntuRelabel},
	{"Nixpkgs", []string{"nix_"}, "https://search.nixos.org/packages?query=%s", nil},
	{"Fedora", []string{"fedora_"}, "https://packages.fedoraproject.org/pkgs/%s/", nil},
	{"RHEL", []string{"almalinux_", "rocky_"}, "https://repology.org/project/%s/versions", nil},
	{"CentOS Stream", []string{"centos_stream_"}, "https://repology.org/project/%s/versions", nil},
	{"EPEL", []string{"epel_"}, "https://packages.fedoraproject.org/pkgs/%s/", nil},
	{"openSUSE Leap", []string{"opensuse_leap"}, "https://software.opensuse.org/search?q=%s", nil},
	{"openSUSE Tumbleweed", []string{"opensuse_tumbleweed"}, "https://software.opensuse.org/search?q=%s", nil},
}

func famOf(repo string) string {
	for _, f := range distroFamilies {
		for _, p := range f.prefixes {
			// Require an exact prefix boundary; "archpower_*" is not Arch.
			if repo == p || strings.HasPrefix(repo, strings.TrimRight(p, "_")+"_") {
				return f.label
			}
		}
	}
	return ""
}

// Date-like snapshots rank below real releases but still order correctly in CalVer-only families.
func dateSnapshot(v string) bool {
	if bareDate(v) { // bare 8-digit YYYYMMDD, e.g. gcc-snapshot
		return true
	}
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

// Plausibility bounds keep non-date eight-digit versions out of the snapshot tier.
func bareDate(v string) bool {
	if len(v) != 8 {
		return false
	}
	for i := 0; i < 8; i++ {
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	y := int(v[0]-'0')*1000 + int(v[1]-'0')*100 + int(v[2]-'0')*10 + int(v[3]-'0')
	m := int(v[4]-'0')*10 + int(v[5]-'0')
	d := int(v[6]-'0')*10 + int(v[7]-'0')
	return y >= 1990 && y <= 2100 && m >= 1 && m <= 12 && d >= 1 && d <= 31
}

// Gentoo 9999 variants track live source rather than a release.
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

// Require digits around "snap" to avoid classifying genuine snapshot versions as transition stubs.
var snapTransitionalRe = regexp.MustCompile(`(?i)\d+snap\d+`)

// Snap transitional debs rank below real packages and render as "snap".
func snapVersion(v string) bool { return snapTransitionalRe.MatchString(v) }

// Transitional package versions render as "snap".
func displayVer(v string) string {
	if snapVersion(v) {
		return "snap"
	}
	return v
}

// Prefer real releases, then dates, then live or transitional pseudo-versions.
func verTier(v string) int {
	switch {
	case allNines(v), snapVersion(v):
		return 2
	case dateSnapshot(v):
		return 1
	default:
		return 0
	}
}

// Better tiers win; equal tiers use Gentoo version ordering.
func betterVer(cur, cand string) bool {
	if ct, nt := verTier(cur), verTier(cand); ct != nt {
		return nt < ct
	}
	return verLess(cur, cand)
}

func repologyVersionsURL(proj string) string {
	return "https://repology.org/project/" + neturl.PathEscape(proj) + "/versions"
}

func newestRow(rows []repologyPkg) (ver, repo string) {
	for _, p := range rows {
		if ver == "" || betterVer(ver, p.Version) {
			ver, repo = p.Version, p.Repo
		}
	}
	return ver, repo
}

// Rolling/development channels are distinct from numbered stable releases.
func rollingRelease(label string) bool {
	switch label {
	case "", "unstable", "testing", "rawhide", "edge", "sid", "devel", "cauldron", "current":
		return true
	}
	return false
}

type channelLine struct{ ver, label string }

// Show the newest supported numbered release and a newer rolling channel.
// Choose by release recency, not package version: an old release's higher version must not win.
// isTesting excludes development, unreleased, or EOL numbered series.
func familyChannels(rows []repologyPkg, prefixes []string, isTesting func(string) bool) []channelLine {
	if len(rows) == 0 {
		return nil
	}
	excluded := func(lbl string) bool { return isTesting != nil && isTesting(lbl) }

	// Select the newest rolling version.
	rollingVer, rollingLabel := "", ""
	for _, p := range rows {
		lbl := releaseLabel(p.Repo, prefixes)
		if !rollingRelease(lbl) || excluded(lbl) {
			continue
		}
		if rollingVer == "" || betterVer(rollingVer, p.Version) {
			rollingVer, rollingLabel = p.Version, lbl
		}
	}

	// Select the newest supported numbered release, then its best version.
	stableVer, stableLabel := "", ""
	for _, p := range rows {
		lbl := releaseLabel(p.Repo, prefixes)
		if rollingRelease(lbl) || excluded(lbl) {
			continue
		}
		switch {
		case stableLabel == "" || verLess(stableLabel, lbl): // first, or a newer release
			stableVer, stableLabel = p.Version, lbl
		case lbl == stableLabel && betterVer(stableVer, p.Version): // same release, better version
			stableVer = p.Version
		}
	}

	switch {
	case stableVer == "" && rollingVer == "": // everything excluded — fall back to the raw newest
		v, r := newestRow(rows)
		return []channelLine{{v, releaseLabel(r, prefixes)}}
	case stableVer == "": // a pure rolling distro (Arch, AUR, Tumbleweed) — just the rolling line
		return []channelLine{{rollingVer, rollingLabel}}
	case rollingVer == "" || !betterVer(stableVer, rollingVer): // no rolling, or it isn't ahead
		return []channelLine{{stableVer, stableLabel}}
	default: // a rolling/dev channel is ahead of stable — show it, then stable
		return []channelLine{{rollingVer, rollingLabel}, {stableVer, stableLabel}}
	}
}

// Live distro metadata prevents Debian testing from being mislabeled stable.
func debianTesting(label string) bool {
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	return relInfo.debian[label] == "testing"
}

// releaseLabel removes the family prefix; exact rolling repos have no label.
func releaseLabel(repo string, prefixes []string) string {
	s := repo
	for _, p := range prefixes {
		if strings.HasPrefix(repo, p) {
			s = strings.TrimPrefix(repo, p)
			break
		}
	}
	s = strings.TrimLeft(s, "_")
	if s == "" || s == repo { // exact-prefix (rolling) repo, or no prefix matched
		return ""
	}
	s = strings.TrimPrefix(s, "stable_") // nix_stable_25_11 -> 25.11, not "stable.25.11"
	return strings.ReplaceAll(s, "_", ".")
}

// A Repology 404 may fall back to search; other failures remain unavailable.
func fetchRepology(ctx context.Context, name string) (proj string, pkgs []repologyPkg, alts []string, exact, available bool) {
	return fetchRepologyWith(ctx, name, func(ctx context.Context, url string, dst any) error {
		return httpGetJSON(ctx, url, nil, dst)
	})
}

func fetchRepologyWith(
	ctx context.Context,
	name string,
	getJSON func(context.Context, string, any) error,
) (proj string, pkgs []repologyPkg, alts []string, exact, available bool) {
	q := strings.ToLower(strings.TrimSpace(name))
	if q == "" {
		return "", nil, nil, false, true
	}
	err := getJSON(ctx, "https://repology.org/api/v1/project/"+neturl.PathEscape(q), &pkgs)
	if err == nil && len(pkgs) > 0 {
		return q, pkgs, nil, true, true
	}
	if err != nil && httpStatusCode(err) != 404 {
		return "", nil, nil, false, false
	}
	var found map[string][]repologyPkg
	if err := getJSON(ctx, "https://repology.org/api/v1/projects/?search="+neturl.QueryEscape(q), &found); err != nil {
		return "", nil, nil, false, false
	}
	if p, ok := found[q]; ok { // exact name surfaced by the search
		return q, p, nil, true, true
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
		return "", nil, nil, false, true
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
	return cands[0].name, found[cands[0].name], alts, false, true
}

type distroLine struct{ label, ver, rel, url string }

// Show stable and newer ~amd64 separately; equal versions remain one stable line.
// Without a stable keyword, show only ~amd64.
func gentooDistroLines(stable, latest, url string) []distroLine {
	switch {
	case stable != "" && latest != "" && stable != latest:
		return []distroLine{{"Gentoo amd64", stable, "", url}, {"Gentoo ~amd64", latest, "", url}}
	case stable != "":
		return []distroLine{{"Gentoo amd64", stable, "", url}}
	case latest != "":
		return []distroLine{{"Gentoo ~amd64", latest, "", url}}
	}
	return nil
}

func renderRepologyLookupMiss(name string, available bool) string {
	if !available {
		return fmt.Sprintf("暂时无法查询 Repology 中「%s」的跨发行版信息，请稍后重试。", name)
	}
	return fmt.Sprintf("❓ 在 Repology 没找到和「%s」相关的跨发行版包,试试更精确的包名。", name)
}

func (v *Verifier) onPkgs(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.queryAllowed(ctx, msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	name := commandArg(msg.Text)
	if name == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, "用法:/pkgs <包名>,例如 /pkgs firefox。跨发行版查版本(Gentoo / AUR / Arch / Alpine / Debian / Ubuntu / Nix / Fedora / RHEL / CentOS Stream / openSUSE 等),Debian 等标注稳定/测试通道,RHEL 取自 AlmaLinux/Rocky 重建。")
		return nil
	}
	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	ensureReleaseInfo(hc, time.Now()) // refresh Debian/Ubuntu stable/testing labels (cached, non-hardcoded)
	proj, pkgs, alts, exact, repologyOK := fetchRepology(hc, repologyQuery(name))
	esc := html.EscapeString
	if len(pkgs) == 0 {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, renderRepologyLookupMiss(name, repologyOK))
		return nil
	}

	// Group rows before selecting stable and rolling channels.
	famRows := map[string][]repologyPkg{}
	for _, p := range pkgs {
		if fam := famOf(p.Repo); fam != "" && p.Version != "" {
			famRows[fam] = append(famRows[fam], p)
		}
	}

	// Gentoo uses authoritative keyword data; Repology cannot distinguish stable from ~amd64.
	var lines []distroLine
	if atoms, _ := searchMainTree(hc, proj); len(atoms) > 0 {
		atom := atoms[0]
		if pkgName := atom[strings.LastIndexByte(atom, '/')+1:]; strings.EqualFold(pkgName, proj) {
			gURL := "https://packages.gentoo.org/packages/" + atom
			stable, latest, _ := pkgVersion(hc, atom)
			lines = append(lines, gentooDistroLines(stable, latest, gURL)...)
		}
	}
	qproj := neturl.QueryEscape(proj)
	for _, f := range distroFamilies {
		rows := famRows[f.label]
		if f.label == "Gentoo" {
			if len(lines) == 0 && len(rows) > 0 { // bot lookup found nothing -> fall back to Repology
				nv, nr := newestRow(rows)
				lines = append(lines, distroLine{"Gentoo", nv, releaseLabel(nr, f.prefixes), fmt.Sprintf(f.search, qproj)})
			}
			continue
		}
		if len(rows) == 0 {
			continue
		}
		// Relabel raw release numbers from live distro metadata.
		var isTesting func(string) bool
		switch f.label {
		case "Debian": // Debian numbers a testing series (forky/14) above stable
			isTesting = debianTesting
		case "Ubuntu": // exclude unreleased + proposed/backports + EOL series (18.04/20.04, …)
			isTesting = ubuntuExcluded
		}
		url := fmt.Sprintf(f.search, qproj)
		for _, ch := range familyChannels(rows, f.prefixes, isTesting) {
			label := ch.label
			if f.relabel != nil {
				label = f.relabel(ch.label)
			}
			lines = append(lines, distroLine{f.label, ch.ver, label, url})
		}
	}
	if len(lines) == 0 {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, fmt.Sprintf("「%s」在 Gentoo / AUR / Arch / Alpine / Debian / Ubuntu / Nix / Fedora / RHEL / CentOS Stream / EPEL / openSUSE 等里都没有打包(可能是某发行版专属)。", proj))
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
		rel := ""
		if ln.rel != "" {
			rel = fmt.Sprintf(" <i>(%s)</i>", esc(ln.rel))
		}
		fmt.Fprintf(&plain, "\n • <b>%s</b>:%s%s", famLink, esc(displayVer(ln.ver)), rel)
		fmt.Fprintf(&rich, "<li><b>%s</b>:%s%s</li>", famLink, esc(displayVer(ln.ver)), rel)
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
		// Collapse alternatives so the main table stays compact.
		fmt.Fprintf(&rich, "<details><summary>其它匹配 (%d)</summary>%s</details>", len(alts), al.String())
	}
	v.sendRichOrHTML(c, bot, msg.Chat.ID, msg.MessageID, rich.String(), plain.String())
	return nil
}
