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

// A distro family shown by /pkgs. A repo belongs to it if its id equals a prefix or starts
// with "<prefix>_". search is a printf template (%s = url-escaped project) to the family's
// package page. relabel (optional) maps a raw release label to a friendlier one — used to
// turn Debian/Ubuntu version numbers into their live role (stable/testing/LTS) so the labels
// aren't hardcoded. The RHEL ecosystem is split: RHEL (the AlmaLinux/Rocky 1:1 rebuilds =
// the actual RHEL versions), CentOS Stream (the rolling upstream), and EPEL — kept separate
// because they are genuinely different products with different version numbers.
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
			// Match the repo exactly, or as "<prefix>_<release>" — but NOT a different distro
			// that merely starts with the same letters (e.g. archpower_* is not "arch").
			if repo == p || strings.HasPrefix(repo, strings.TrimRight(p, "_")+"_") {
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

// newestRow returns the highest version among rows and the repo it came from.
func newestRow(rows []repologyPkg) (ver, repo string) {
	for _, p := range rows {
		if ver == "" || betterVer(ver, p.Version) {
			ver, repo = p.Version, p.Repo
		}
	}
	return ver, repo
}

// rollingRelease reports whether a release label names a rolling/development channel
// (sid, rawhide, edge, a label-less rolling repo) rather than a numbered stable release.
func rollingRelease(label string) bool {
	switch label {
	case "", "unstable", "testing", "rawhide", "edge", "sid", "devel", "cauldron", "current":
		return true
	}
	return false
}

type channelLine struct{ ver, label string }

// bestLabel returns the most representative release label for a given version among rows:
// a rolling/dev channel that carries it (when preferRolling), otherwise the highest-numbered
// release that ships it — so a version present in several releases is labelled by the newest
// one (e.g. RHEL firefox 140.11.0 → the newest clone release, not an arbitrary older one),
// not by whichever repo happened to be scanned first.
func bestLabel(rows []repologyPkg, prefixes []string, version string, preferRolling bool) string {
	rolling, numbered := "", ""
	for _, p := range rows {
		if p.Version != version {
			continue
		}
		lbl := releaseLabel(p.Repo, prefixes)
		if rollingRelease(lbl) {
			rolling = lbl
		} else if numbered == "" || verLess(numbered, lbl) {
			numbered = lbl
		}
	}
	if preferRolling && rolling != "" {
		return rolling
	}
	if numbered != "" {
		return numbered
	}
	return rolling
}

// familyChannels returns the versions to show for one distro family. It centres on the
// current STABLE release (so Fedora shows 44, not just rawhide) and adds the newest
// rolling/dev channel above it when that's ahead (so Debian shows sid AND stable). A package
// at one version everywhere stays a single line. isTesting (optional, Debian only) excludes a
// pre-release numbered series — Debian's highest number is testing/forky, not stable — so the
// stable line is the real stable (trixie/13), derived live rather than "highest number".
func familyChannels(rows []repologyPkg, prefixes []string, isTesting func(string) bool) []channelLine {
	if len(rows) == 0 {
		return nil
	}
	nv, _ := newestRow(rows) // newest across all channels (incl. rolling/testing)

	isStable := func(lbl string) bool {
		return !rollingRelease(lbl) && (isTesting == nil || !isTesting(lbl))
	}
	stableVer, stableLabel := "", ""
	for _, p := range rows {
		if !isStable(releaseLabel(p.Repo, prefixes)) {
			continue
		}
		if stableVer == "" || betterVer(stableVer, p.Version) {
			stableVer = p.Version
		}
	}
	if stableVer != "" { // label = the newest stable release that ships that version
		for _, p := range rows {
			if p.Version != stableVer {
				continue
			}
			if lbl := releaseLabel(p.Repo, prefixes); isStable(lbl) && (stableLabel == "" || verLess(stableLabel, lbl)) {
				stableLabel = lbl
			}
		}
	}

	if stableVer == "" { // a pure rolling distro (Arch, AUR, Tumbleweed) — just the rolling line
		return []channelLine{{nv, bestLabel(rows, prefixes, nv, true)}}
	}
	if nv == stableVer { // stable carries the newest version — one line, labelled by the release
		return []channelLine{{stableVer, stableLabel}}
	}
	// A rolling/dev (or testing) channel is ahead of stable — show it, then stable.
	return []channelLine{{nv, bestLabel(rows, prefixes, nv, true)}, {stableVer, stableLabel}}
}

// debianTesting reports whether a Debian release label is the current "testing" series
// (forky/14 today), per the live distro-info-data status — so it isn't mistaken for stable.
func debianTesting(label string) bool {
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	return relInfo.debian[label] == "testing"
}

// releaseLabel turns the Repology repo id of a family's winning version into a short
// release name shown in parentheses — so e.g. debian_unstable -> "unstable" (sid),
// ubuntu_25_04 -> "25.04", fedora_rawhide -> "rawhide", alpine_3_21 -> "3.21". A rolling
// repo with no per-release suffix (arch, aur, opensuse_tumbleweed) yields "" (no label).
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

// onPkgs handles /pkgs (and its alias /distro) — cross-distro package versions via Repology.
func (v *Verifier) onPkgs(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.queryAllowed(ctx, msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	name := commandArg(msg.Text)
	if name == "" {
		v.notify(c, bot, msg.Chat.ID, "用法:/pkgs <包名>,例如 /pkgs firefox。跨发行版查版本(Gentoo / AUR / Arch / Alpine / Debian / Ubuntu / Nix / Fedora / RHEL / CentOS Stream / openSUSE 等),Debian 等标注稳定/测试通道,RHEL 取自 AlmaLinux/Rocky 重建。")
		return nil
	}
	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	ensureReleaseInfo(hc, time.Now()) // refresh Debian/Ubuntu stable/testing labels (cached, non-hardcoded)
	proj, pkgs, alts, exact := fetchRepology(hc, name)
	esc := html.EscapeString
	if len(pkgs) == 0 {
		v.notify(c, bot, msg.Chat.ID, fmt.Sprintf("❓ 在 Repology 没找到和「%s」相关的跨发行版包,试试更精确的包名。", name))
		return nil
	}

	// group every repo row by family, so each family can show its rolling/dev channel AND
	// its newest stable release when their versions differ (e.g. Debian unstable vs stable).
	famRows := map[string][]repologyPkg{}
	for _, p := range pkgs {
		if fam := famOf(p.Repo); fam != "" && p.Version != "" {
			famRows[fam] = append(famRows[fam], p)
		}
	}

	// Build the displayed lines. Gentoo is special: use the bot's own packages.gentoo.org
	// data so amd64-stable and ~amd64 testing show on SEPARATE lines (Repology can't express
	// Gentoo keyword status). All other families come from Repology, one line each, annotated
	// with the release the version is from (so e.g. Debian shows it's from unstable/sid).
	type distroLine struct{ label, ver, rel, url string }
	var lines []distroLine
	if atoms := searchMainTree(hc, proj); len(atoms) > 0 {
		atom := atoms[0]
		if pkgName := atom[strings.LastIndexByte(atom, '/')+1:]; strings.EqualFold(pkgName, proj) {
			gURL := "https://packages.gentoo.org/packages/" + atom
			stable, testing := pkgVersion(hc, atom)
			switch {
			case stable != "" && testing != "" && stable != testing:
				lines = append(lines, distroLine{"Gentoo amd64", stable, "", gURL}, distroLine{"Gentoo ~amd64", testing, "", gURL})
			case testing != "":
				lines = append(lines, distroLine{"Gentoo ~amd64", testing, "", gURL})
			case stable != "":
				lines = append(lines, distroLine{"Gentoo amd64", stable, "", gURL})
			}
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
		// Show the current stable, plus the rolling/dev channel above it when ahead (e.g.
		// Debian sid + stable), one line each. The relabel hook turns a raw release number
		// into its live role (Debian "13" -> "13 stable", Ubuntu "24.04" -> "24.04 LTS").
		var isTesting func(string) bool
		if f.label == "Debian" { // only Debian numbers a testing series above stable
			isTesting = debianTesting
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
		rel := ""
		if ln.rel != "" {
			rel = fmt.Sprintf(" <i>(%s)</i>", esc(ln.rel))
		}
		fmt.Fprintf(&plain, "\n • <b>%s</b>:%s%s", famLink, esc(ln.ver), rel)
		fmt.Fprintf(&rich, "<li><b>%s</b>:%s%s</li>", famLink, esc(ln.ver), rel)
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
	v.sendRichOrHTML(c, bot, msg.Chat.ID, msg.MessageID, rich.String(), plain.String())
	return nil
}
