package lookup

import (
	"context"
	"fmt"
	"html"
	neturl "net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
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
		return GetJSON(ctx, url, nil, dst)
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

// OnPkgs handles cross-distribution package version lookups.
func (v *Service) OnPkgs(ctx *th.Context, update telego.Update) error {
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

// /armpkgs compares arm64 support across distro-specific architecture APIs.

func (v *Service) gentooArmStatus(ctx context.Context, name string) (status, url string) {
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
		err := GetJSON(ctx, url, nil, &r)
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
func (v *Service) aurArmStatus(ctx context.Context, pkg string) string {
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

// OnArmpkgs handles cross-distribution arm64 support lookups.
func (v *Service) OnArmpkgs(ctx *th.Context, update telego.Update) error {
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

// Debian and Ubuntu channel roles come from live distro-info-data, not hardcoded releases.
// /pkgs uses the cached roles for stable, testing, oldstable, LTS, and EOL labels.

const relInfoTTL = 24 * time.Hour

// Failed refreshes retry quickly instead of retaining degraded data for 24 hours.
const relInfoRetryTTL = 10 * time.Minute

var (
	fetchDebianStatusFn = fetchDebianStatus
	fetchUbuntuFn       = fetchUbuntu
)

var relInfo = struct {
	mu         sync.Mutex
	debian     map[string]string // Debian version ("13") -> status ("stable"/"testing"/...)
	ubuntu     map[string]bool   // Ubuntu version ("24.04") -> is it an LTS?
	ubuntuRel  map[string]bool   // Ubuntu version ("24.04") -> already released (date in the past)?
	ubuntuEOL  map[string]bool   // Ubuntu version ("18.04") -> past the standard-support end date?
	ubuntuSer  map[string]bool   // Ubuntu series codename ("resolute") -> already released?
	fetched    time.Time
	refreshing bool // a fetch is in flight (so concurrent /pkgs don't all hit upstream)
}{}

// Refresh is optional enrichment: failures retain old data and raw labels still work.
// The in-flight guard coalesces concurrent cold lookups.
func ensureReleaseInfo(ctx context.Context, now time.Time) {
	relInfo.mu.Lock()
	fresh := relInfo.debian != nil && now.Sub(relInfo.fetched) < relInfoTTL
	if fresh || relInfo.refreshing {
		relInfo.mu.Unlock()
		return // already fresh, or someone else is fetching — fall back to current data
	}
	relInfo.refreshing = true
	relInfo.mu.Unlock()
	// Always clear the in-flight flag, including during panic unwinding.
	defer func() {
		relInfo.mu.Lock()
		relInfo.refreshing = false
		relInfo.mu.Unlock()
	}()

	deb := fetchDebianStatusFn(ctx, now)
	ubu, ubuRel, ubuEOL, ubuSer := fetchUbuntuFn(ctx, now)

	// Empty HTTP-200 parses indicate upstream errors or schema drift; never replace good data.
	debOK, ubuOK := len(deb) > 0, len(ubu) > 0
	relInfo.mu.Lock()
	if debOK {
		relInfo.debian = deb
	}
	if ubuOK {
		relInfo.ubuntu, relInfo.ubuntuRel, relInfo.ubuntuEOL, relInfo.ubuntuSer = ubu, ubuRel, ubuEOL, ubuSer
	}
	if relInfo.debian == nil {
		relInfo.debian = map[string]string{} // mark attempted so the freshness gate can hold (no per-call refetch)
	}
	// Full TTL requires both sources; partial refreshes use the short retry window.
	relInfo.fetched = relInfoNextFetched(now, debOK && ubuOK)
	relInfo.mu.Unlock()
}

// Backdate failed refreshes to leave only relInfoRetryTTL freshness.
func relInfoNextFetched(now time.Time, bothOK bool) time.Time {
	if bothOK {
		return now
	}
	return now.Add(relInfoRetryTTL - relInfoTTL)
}

// A release date at or before now marks a distro-info row released.
func parseDistroInfo(body string) (rows [][]string) {
	for i, line := range strings.Split(body, "\n") {
		if i == 0 || strings.TrimSpace(line) == "" { // skip header + blanks
			continue
		}
		rows = append(rows, strings.Split(line, ","))
	}
	return rows
}

func fetchDebianStatus(ctx context.Context, now time.Time) map[string]string {
	body, err := httpGetBody(ctx, "https://debian.pages.debian.net/distro-info-data/debian.csv", 1<<20)
	if err != nil {
		return nil
	}
	return deriveDebianStatus(string(body), now)
}

// Derive stable generations and the next testing release from dates.
func deriveDebianStatus(body string, now time.Time) map[string]string {
	type rel struct {
		ver      string
		released bool
	}
	var rels []rel
	for _, c := range parseDistroInfo(body) {
		// Testing rows may omit the release column; versionless rows are sid/experimental.
		if len(c) < 4 || c[0] == "" { // skip sid/experimental (no version) and malformed rows
			continue
		}
		released := false
		if len(c) >= 5 {
			if t, perr := time.Parse("2006-01-02", c[4]); perr == nil && !t.After(now) {
				released = true
			}
		}
		rels = append(rels, rel{c[0], released})
	}
	out := map[string]string{}
	// Released versions, newest first: stable, oldstable, oldoldstable.
	var rel0 []string
	for _, r := range rels {
		if r.released {
			rel0 = append(rel0, r.ver)
		}
	}
	sort.Slice(rel0, func(i, j int) bool { return verLess(rel0[j], rel0[i]) }) // desc
	for i, st := range []string{"stable", "oldstable", "oldoldstable"} {
		if i < len(rel0) {
			out[rel0[i]] = st
		}
	}
	// The lowest not-yet-released version above stable is "testing".
	if len(rel0) > 0 {
		stable := rel0[0]
		testing := ""
		for _, r := range rels {
			if !r.released && verLess(stable, r.ver) && (testing == "" || verLess(r.ver, testing)) {
				testing = r.ver
			}
		}
		if testing != "" {
			out[testing] = "testing"
		}
	}
	return out
}

// Ubuntu maps track LTS, release, standard-support end, and codename release state.
func fetchUbuntu(ctx context.Context, now time.Time) (lts, released, eol, series map[string]bool) {
	body, err := httpGetBody(ctx, "https://debian.pages.debian.net/distro-info-data/ubuntu.csv", 1<<20)
	if err != nil {
		return nil, nil, nil, nil
	}
	lts, released, eol, series = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range parseDistroInfo(string(body)) {
		if len(c) < 1 || c[0] == "" {
			continue
		}
		ver := strings.TrimSpace(strings.TrimSuffix(c[0], "LTS"))
		lts[ver] = strings.Contains(c[0], "LTS")
		// Store unreleased series as known false, not unknown.
		rel := false
		if len(c) >= 5 {
			if t, perr := time.Parse("2006-01-02", c[4]); perr == nil && !t.After(now) {
				rel = true
			}
		}
		released[ver] = rel
		// Exclude releases past standard support that would mask newer releases shipping only a Snap.
		if len(c) >= 6 {
			if t, perr := time.Parse("2006-01-02", c[5]); perr == nil && !t.After(now) {
				eol[ver] = true
			}
		}
		// Madison uses codenames, so retain their release state too.
		if len(c) >= 3 {
			if s := strings.ToLower(strings.TrimSpace(c[2])); s != "" {
				series[s] = rel
			}
		}
	}
	return lts, released, eol, series
}

// Known unreleased Ubuntu suites are development; unknown suites remain displayable.
func ubuntuDevSuite(series string) bool {
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	released, known := relInfo.ubuntuSer[strings.ToLower(series)]
	return known && !released
}

// Unknown Debian labels pass through before metadata loads.
func debianRelabel(raw string) string {
	if raw == "unstable" {
		return "unstable/sid" // the rolling unstable channel is codenamed sid
	}
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	if s, ok := relInfo.debian[raw]; ok {
		return raw + " " + s // e.g. "13 stable"
	}
	return raw
}

func ubuntuRelabel(raw string) string {
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	out := raw
	if relInfo.ubuntu[raw] {
		out += " LTS"
	}
	if relInfo.ubuntuEOL[raw] { // the upstream EOL column marks the end of standard support
		out += " · 标准支持已结束"
	}
	return out
}

// Exclude proposed, backports, unreleased, and post-standard-support Ubuntu series from the current line.
// Unknown series remain eligible so lookups still work before metadata loads.
func ubuntuExcluded(label string) bool {
	if strings.Contains(label, "proposed") || strings.Contains(label, "backport") {
		return true
	}
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	if relInfo.ubuntuEOL[label] {
		return true
	}
	released, known := relInfo.ubuntuRel[label]
	return known && !released
}
