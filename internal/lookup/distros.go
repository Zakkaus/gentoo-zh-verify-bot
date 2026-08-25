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

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
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
	relabel  func(i18n.Lang, string) string
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

// Known unreleased Debian suites, including sid, are development channels.
func debianDevSuite(series string) bool {
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	released, known := relInfo.debianSer[strings.ToLower(series)]
	return known && !released
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

// A Repology 404 or empty direct result may fall back to search; other failures remain unavailable.
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

func renderRepologyLookupMiss(l i18n.Lang, name string, available bool) string {
	if !available {
		return i18n.Messages.LookupDistros.Pkgs.RepologyUnavailable.Render(l, name)
	}
	return i18n.Messages.LookupDistros.Pkgs.RepologyNotFound.Render(l, name)
}

// OnPkgs handles cross-distribution package version lookups.
func (v *Service) OnPkgs(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil {
		return nil
	}
	l := v.requesterLanguage(msg)
	if !v.queryAllowed(ctx, msg, l) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	name := commandArg(msg.Text)
	if name == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, i18n.Messages.LookupDistros.Pkgs.Usage.For(l))
		return nil
	}
	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	ensureReleaseInfo(hc, time.Now()) // refresh Debian/Ubuntu stable/testing labels (cached, non-hardcoded)
	proj, pkgs, alts, exact, repologyOK := fetchRepology(hc, repologyQuery(name))
	esc := html.EscapeString
	if len(pkgs) == 0 {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, renderRepologyLookupMiss(l, name, repologyOK))
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
				label = f.relabel(l, ch.label)
			}
			lines = append(lines, distroLine{f.label, ch.ver, label, url})
		}
	}
	if len(lines) == 0 {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, i18n.Messages.LookupDistros.Pkgs.NoSupportedDistro.Render(l, proj))
		return nil
	}

	head := i18n.Messages.LookupDistros.Pkgs.Heading.Render(l, esc(repologyVersionsURL(proj)), esc(proj))
	if !exact {
		head += i18n.Messages.LookupDistros.Pkgs.ClosestMatch.Render(l, esc(name))
	}
	var plain, rich strings.Builder
	plain.WriteString(i18n.Messages.LookupDistros.Pkgs.PlainHeading.Render(l, head))
	rich.WriteString("<h3>" + head + "</h3><ul>")
	for _, ln := range lines {
		famLink := fmt.Sprintf("<a href=\"%s\">%s</a>", esc(ln.url), esc(ln.label))
		rel := ""
		if ln.rel != "" {
			rel = i18n.Messages.LookupDistros.Pkgs.ReleaseRole.Render(l, esc(ln.rel))
		}
		plain.WriteString(i18n.Messages.LookupDistros.Pkgs.PlainRow.Render(l, famLink, esc(displayVer(ln.ver)), rel))
		rich.WriteString(i18n.Messages.LookupDistros.Pkgs.RichRow.Render(l, famLink, esc(displayVer(ln.ver)), rel))
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
		plain.WriteString(i18n.Messages.LookupDistros.Pkgs.Alternatives.Render(l, al.String()))
		// Collapse alternatives so the main table stays compact.
		rich.WriteString(i18n.Messages.LookupDistros.Pkgs.RichAlternatives.Render(l, len(alts), al.String()))
	}
	v.sendRichOrHTML(c, bot, msg.Chat.ID, msg.MessageID, rich.String(), plain.String())
	return nil
}

// /armpkgs compares arm64 support across distro-specific architecture APIs.

func (v *Service) gentooArmStatus(ctx context.Context, l i18n.Lang, name string) (status, url string) {
	return gentooArmStatusWith(ctx, l, name, searchMainTree, armStatus)
}

func gentooArmStatusWith(
	ctx context.Context,
	l i18n.Lang,
	name string,
	search func(context.Context, string) ([]string, bool),
	status func(context.Context, string) (string, string, bool),
) (string, string) {
	searchURL := "https://packages.gentoo.org/packages/search?q=" + neturl.QueryEscape(name)
	atoms, available := search(ctx, name)
	if !available {
		return i18n.Messages.LookupDistros.Armpkgs.QueryFailed.For(l), searchURL
	}
	if len(atoms) == 0 {
		return i18n.Messages.LookupDistros.Armpkgs.NotInOfficialTree.For(l), searchURL
	}
	url := "https://packages.gentoo.org/packages/" + atoms[0]
	stable, testing, ok := status(ctx, atoms[0])
	switch {
	case !ok:
		return i18n.Messages.LookupDistros.Armpkgs.QueryFailed.For(l), url
	case stable != "" && testing != "":
		return i18n.Messages.LookupDistros.Armpkgs.StableTesting.Render(l, stable, testing), url
	case stable != "":
		return i18n.Messages.LookupDistros.Armpkgs.StableOnly.Render(l, stable), url
	case testing != "":
		return i18n.Messages.LookupDistros.Armpkgs.TestingOnly.Render(l, testing), url
	default:
		return i18n.Messages.LookupDistros.Armpkgs.NoArm64Keyword.For(l), url
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
func madisonArmStatus(ctx context.Context, l i18n.Lang, madisonURL, pkg string, devSuite func(string) bool) string {
	body, err := httpGetBody(ctx, madisonURL+neturl.QueryEscape(pkg)+"&text=on&a=arm64", 1<<20)
	if err != nil {
		return i18n.Messages.LookupDistros.Armpkgs.QueryFailed.For(l)
	}
	entries := parseMadison(string(body))
	if len(entries) == 0 {
		return i18n.Messages.LookupDistros.Armpkgs.NoArm64Package.For(l)
	}
	suite, ver, dev := pickMadison(entries, devSuite)
	if dev {
		suite = i18n.Messages.LookupDistros.Armpkgs.DevelopmentSuite.Render(l, suite)
	}
	return i18n.Messages.LookupDistros.Armpkgs.Available.Render(l, suite, displayVer(ver))
}

// Only an authoritative 404 proves absence; all other failures remain unknown.
func fedoraArmStatus(ctx context.Context, l i18n.Lang, pkg string) string {
	return fedoraArmStatusWith(ctx, l, pkg, func(ctx context.Context, url string) (string, error) {
		var r struct {
			Version string `json:"version"`
			Arch    string `json:"arch"`
		}
		if err := GetJSON(ctx, url, nil, &r); err != nil {
			return "", err
		}
		// mdapi currently serves x86_64 metadata for this route. Only an
		// aarch64 or architecture-independent record can prove arm64 support.
		if r.Arch != "aarch64" && r.Arch != "noarch" {
			return "", fmt.Errorf("fedora mdapi returned architecture %q", r.Arch)
		}
		return r.Version, nil
	})
}

func fedoraArmStatusWith(
	ctx context.Context,
	l i18n.Lang,
	pkg string,
	fetch func(context.Context, string) (string, error),
) string {
	version, err := fetch(ctx, "https://mdapi.fedoraproject.org/rawhide/pkg/"+neturl.PathEscape(pkg))
	if err != nil {
		if httpStatusCode(err) == 404 {
			return i18n.Messages.LookupDistros.Armpkgs.NotInFedora.For(l)
		}
		return i18n.Messages.LookupDistros.Armpkgs.FedoraQueryFailed.For(l)
	}
	if version == "" {
		return i18n.Messages.LookupDistros.Armpkgs.FedoraQueryFailed.For(l)
	}
	return i18n.Messages.LookupDistros.Armpkgs.FedoraRawhide.Render(l, version)
}

var aurArchRe = regexp.MustCompile(`(?i)arch=\(([^)]*)\)`)

// AUR support follows the PKGBUILD arch declaration, not buildability in practice.
func aurArchLabel(l i18n.Lang, pkgbuild string) string {
	m := aurArchRe.FindStringSubmatch(pkgbuild)
	if m == nil {
		return i18n.Messages.LookupDistros.Armpkgs.PKGBUILDParseFailed.For(l)
	}
	arch := strings.ToLower(m[1])
	switch {
	case strings.Contains(arch, "any"):
		return i18n.Messages.LookupDistros.Armpkgs.AnyArchitecture.For(l)
	case strings.Contains(arch, "aarch64"):
		return i18n.Messages.LookupDistros.Armpkgs.DeclaresAarch64.For(l)
	case strings.Contains(arch, "arm"):
		return i18n.Messages.LookupDistros.Armpkgs.Arm32Only.For(l)
	default:
		return i18n.Messages.LookupDistros.Armpkgs.X86Only.For(l)
	}
}

// Only an AUR 404 proves absence; other failures remain unknown.
func (v *Service) aurArmStatus(ctx context.Context, l i18n.Lang, pkg string) string {
	body, err := httpGetBody(ctx, "https://aur.archlinux.org/cgit/aur.git/plain/PKGBUILD?h="+neturl.QueryEscape(pkg), 64<<10)
	if err != nil {
		if httpStatusCode(err) == 404 {
			return i18n.Messages.LookupDistros.Armpkgs.NotInAUR.For(l)
		}
		return i18n.Messages.LookupDistros.Armpkgs.AURQueryFailed.For(l)
	}
	return aurArchLabel(l, string(body))
}

// Only an Arch Linux ARM 404 proves absence.
func alarmArmStatus(ctx context.Context, l i18n.Lang, pkg string) string {
	resp, err := httpGet(ctx, "https://archlinuxarm.org/packages/aarch64/"+neturl.PathEscape(pkg), nil)
	if err == nil {
		err = resp.Body.Close()
	}
	if err != nil {
		if httpStatusCode(err) == 404 {
			return i18n.Messages.LookupDistros.Armpkgs.NotPackaged.For(l)
		}
		return i18n.Messages.LookupDistros.Armpkgs.QueryFailed.For(l)
	}
	return i18n.Messages.LookupDistros.Armpkgs.Packaged.For(l)
}

// OnArmpkgs handles cross-distribution arm64 support lookups.
func (v *Service) OnArmpkgs(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil {
		return nil
	}
	l := v.requesterLanguage(msg)
	if !v.queryAllowed(ctx, msg, l) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	name := commandArg(msg.Text)
	if name == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, i18n.Messages.LookupDistros.Armpkgs.Usage.For(l))
		return nil
	}
	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	ensureReleaseInfo(hc, time.Now()) // load Debian and Ubuntu series status so development suites are skipped
	pe := neturl.PathEscape(name)

	sources := []struct {
		label string
		fn    func() (string, string)
	}{
		{"Gentoo", func() (string, string) { return v.gentooArmStatus(hc, l, name) }},
		{"Debian", func() (string, string) {
			return madisonArmStatus(hc, l, "https://qa.debian.org/madison.php?package=", name, debianDevSuite), "https://tracker.debian.org/pkg/" + pe
		}},
		{"Ubuntu", func() (string, string) {
			return madisonArmStatus(hc, l, "https://people.canonical.com/~ubuntu-archive/madison.cgi?package=", name, ubuntuDevSuite), "https://launchpad.net/ubuntu/+source/" + pe
		}},
		{"Fedora", func() (string, string) {
			return fedoraArmStatus(hc, l, name), "https://packages.fedoraproject.org/pkgs/" + pe + "/"
		}},
		{"Arch Linux ARM", func() (string, string) {
			return alarmArmStatus(hc, l, name), "https://archlinuxarm.org/packages/aarch64/" + pe
		}},
		{"AUR", func() (string, string) {
			return v.aurArmStatus(hc, l, name), "https://aur.archlinux.org/packages/" + pe
		}},
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
	b.WriteString(i18n.Messages.LookupDistros.Armpkgs.Heading.Render(l, esc(name)))
	for _, r := range results {
		b.WriteString(i18n.Messages.LookupDistros.Armpkgs.Row.Render(l, esc(r.url), esc(r.label), esc(r.status)))
	}
	b.WriteByte('\n')
	b.WriteString(i18n.Messages.LookupDistros.Armpkgs.Footer.For(l))
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
	debianSer  map[string]bool   // Debian series codename ("trixie") -> already released?
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
	debOK, ubuOK := len(deb.roles) > 0, len(ubu) > 0
	relInfo.mu.Lock()
	if debOK {
		relInfo.debian, relInfo.debianSer = deb.roles, deb.series
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

type debianReleaseData struct {
	roles  map[string]string
	series map[string]bool
}

func fetchDebianStatus(ctx context.Context, now time.Time) debianReleaseData {
	body, err := httpGetBody(ctx, "https://debian.pages.debian.net/distro-info-data/debian.csv", 1<<20)
	if err != nil {
		return debianReleaseData{}
	}
	return deriveDebianReleaseData(string(body), now)
}

func deriveDebianStatus(body string, now time.Time) map[string]string {
	return deriveDebianReleaseData(body, now).roles
}

// Derive stable generations, the next testing release, and suite release state from dates.
func deriveDebianReleaseData(body string, now time.Time) debianReleaseData {
	type rel struct {
		ver      string
		released bool
	}
	var rels []rel
	series := map[string]bool{}
	for _, c := range parseDistroInfo(body) {
		if len(c) < 4 {
			continue
		}
		released := false
		if len(c) >= 5 {
			if t, perr := time.Parse("2006-01-02", c[4]); perr == nil && !t.After(now) {
				released = true
			}
		}
		if name := strings.ToLower(strings.TrimSpace(c[2])); name != "" {
			series[name] = released
		}
		if c[0] == "" {
			continue // sid/experimental have no numbered release role
		}
		rels = append(rels, rel{c[0], released})
	}
	roles := map[string]string{}
	// Released versions, newest first: stable, oldstable, oldoldstable.
	var releasedVersions []string
	for _, r := range rels {
		if r.released {
			releasedVersions = append(releasedVersions, r.ver)
		}
	}
	sort.Slice(releasedVersions, func(i, j int) bool {
		return verLess(releasedVersions[j], releasedVersions[i])
	})
	for i, status := range []string{"stable", "oldstable", "oldoldstable"} {
		if i < len(releasedVersions) {
			roles[releasedVersions[i]] = status
		}
	}
	// The lowest not-yet-released version above stable is "testing".
	if len(releasedVersions) > 0 {
		stable := releasedVersions[0]
		testing := ""
		for _, r := range rels {
			if !r.released && verLess(stable, r.ver) && (testing == "" || verLess(r.ver, testing)) {
				testing = r.ver
			}
		}
		if testing != "" {
			roles[testing] = "testing"
		}
	}
	return debianReleaseData{roles: roles, series: series}
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
func debianRelabel(_ i18n.Lang, raw string) string {
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

func ubuntuRelabel(l i18n.Lang, raw string) string {
	relInfo.mu.Lock()
	defer relInfo.mu.Unlock()
	out := raw
	if relInfo.ubuntu[raw] {
		out += " LTS"
	}
	if relInfo.ubuntuEOL[raw] { // the upstream EOL column marks the end of standard support
		out += i18n.Messages.LookupDistros.Release.StandardSupportEnded.For(l)
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
