package lookup

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

type overlay struct {
	name   string // short display name
	repo   string // GitHub owner/name
	branch string
}

var userAgent = "gentoo-zh-verify-bot"

var overlays []overlay

func configurePkg(cfg *config.Config) {
	if cfg.UserAgent != "" {
		userAgent = cfg.UserAgent
	}
	if len(cfg.Overlays) == 0 {
		overlays = []overlay{
			{name: "gentoo-zh", repo: "gentoo-zh/overlay", branch: "master"},
			{name: "guru", repo: "gentoo/guru", branch: "master"},
		}
		return
	}
	overlays = nil
	for _, o := range cfg.Overlays {
		br := o.Branch
		if br == "" {
			br = "master"
		}
		name := o.Name
		if name == "" {
			name = o.Repo
		}
		overlays = append(overlays, overlay{name: name, repo: o.Repo, branch: br})
	}
}

const pkgCacheTTL = 6 * time.Hour
const verCacheTTL = 6 * time.Hour
const maxHitsPerSource = 8

// Bound caches keyed by user input; exceptional overflow clears them wholesale.
const pkgCacheMax = 2000
const pkgRetryFloor = 3 * time.Minute // throttle refresh retries after a failure (avoids GitHub rate-limit storms)

type pkgCache struct {
	mu          sync.Mutex
	pkgs        map[string]map[string]string
	available   map[string]bool
	fetched     time.Time
	lastAttempt time.Time
	refreshing  bool
}

var pkgC = &pkgCache{
	pkgs:      map[string]map[string]string{},
	available: map[string]bool{},
}

// Preserve per-source availability so partial answers never become definitive misses.
type pkgLookupAvailability struct {
	official bool
	overlays map[string]bool
}

func (a pkgLookupAvailability) anyUnavailable() bool {
	if !a.official {
		return true
	}
	for _, ok := range a.overlays {
		if !ok {
			return true
		}
	}
	return false
}

func (a pkgLookupAvailability) unavailableSources(l i18n.Lang) string {
	messages := i18n.Messages.LookupPackages.Source
	sources := make([]string, 0, len(a.overlays)+1)
	if !a.official {
		sources = append(sources, messages.GentooOfficialTree.For(l))
	}
	unavailableOverlays := make([]string, 0, len(a.overlays))
	for name, ok := range a.overlays {
		if !ok {
			unavailableOverlays = append(unavailableOverlays, name)
		}
	}
	sort.Strings(unavailableOverlays)
	for _, name := range unavailableOverlays {
		sources = append(sources, messages.Overlay.Render(l, html.EscapeString(name)))
	}
	return strings.Join(sources, messages.ListSeparator.For(l))
}

func isPkgPath(p string) bool {
	i := strings.IndexByte(p, '/')
	if i < 1 || strings.Contains(p[i+1:], "/") {
		return false
	}
	switch p[:i] {
	case "metadata", "profiles", "eclass", "licenses", "scripts", ".github", ".gitlab":
		return false
	}
	cat := p[:i]
	return strings.Contains(cat, "-") || cat == "virtual"
}

func splitVer(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '_' })
}

// verLess implements the Gentoo ordering needed to select the latest version.
func verLess(a, b string) bool {
	as, bs := splitVer(a), splitVer(b)
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		if c := cmpToken(as[i], bs[i]); c != 0 {
			return c < 0
		}
	}
	if len(as) == len(bs) {
		return false
	}
	// Extra pre-release suffixes are older; patch, revision, and numeric suffixes are newer.
	aLonger := len(as) > len(bs)
	var extra string // the first token only the longer side has (index n into it)
	if aLonger {
		extra = as[n]
	} else {
		extra = bs[n]
	}
	if suffixWeight(extra) < 0 { // longer side is a pre-release => it is the OLDER one
		return aLonger
	}
	return !aLonger // longer side is a patch/revision/extra component => the NEWER one
}

// Negative suffixes precede a bare release; patches and revisions follow it.
func suffixWeight(tok string) int {
	switch {
	case strings.HasPrefix(tok, "alpha"):
		return -4
	case strings.HasPrefix(tok, "beta"):
		return -3
	case strings.HasPrefix(tok, "pre"):
		return -2
	case strings.HasPrefix(tok, "rc"):
		return -1
	default:
		return 1
	}
}

// Compare digit runs numerically without changing byte-wise ordering for other runs.
func cmpToken(a, b string) int {
	ai, bi := 0, 0
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	for ai < len(a) && bi < len(b) {
		if isDigit(a[ai]) && isDigit(b[bi]) {
			aj, bj := ai, bi
			for aj < len(a) && isDigit(a[aj]) {
				aj++
			}
			for bj < len(b) && isDigit(b[bj]) {
				bj++
			}
			if c := cmpNum(a[ai:aj], b[bi:bj]); c != 0 {
				return c
			}
			ai, bi = aj, bj
		} else {
			if a[ai] != b[bi] {
				if a[ai] < b[bi] {
					return -1
				}
				return 1
			}
			ai++
			bi++
		}
	}
	switch { // the token with more left is "greater" (e.g. "r" < "r2")
	case len(a)-ai < len(b)-bi:
		return -1
	case len(a)-ai > len(b)-bi:
		return 1
	default:
		return 0
	}
}

// cmpNum compares arbitrarily large digit strings without integer overflow.
func cmpNum(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	switch {
	case len(a) != len(b):
		if len(a) < len(b) {
			return -1
		}
		return 1
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// ebuildAtomVer extracts ("cat/pkg", "version") from an ebuild blob path "cat/pkg/pkg-VER.ebuild".
func ebuildAtomVer(path string) (string, string, bool) {
	if !strings.HasSuffix(path, ".ebuild") {
		return "", "", false
	}
	slash := strings.LastIndexByte(path, '/')
	if slash < 0 {
		return "", "", false
	}
	dir := path[:slash]    // cat/pkg
	file := path[slash+1:] // pkg-VER.ebuild
	pkg := dir[strings.LastIndexByte(dir, '/')+1:]
	ver := strings.TrimSuffix(file, ".ebuild")
	ver = strings.TrimPrefix(ver, pkg+"-")
	if ver == "" || strings.Contains(ver, "/") {
		return "", "", false
	}
	return dir, ver, true
}

func (o overlay) treeURL(atom string) string {
	return "https://github.com/" + o.repo + "/tree/" + o.branch + "/" + atom
}

// A real overlay release outranks 9999; a 9999-only package still reports 9999.
func overlayPickVer(cur string, seen bool, ver string) string {
	if !seen || betterVer(cur, ver) {
		return ver
	}
	return cur
}

// fetchOverlay selects the newest version from a recursive GitHub tree.
func fetchOverlay(ctx context.Context, o overlay) (map[string]string, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1", o.repo, o.branch)
	hdr := http.Header{"Accept": {"application/vnd.github+json"}}
	if githubToken != "" {
		hdr.Set("Authorization", "Bearer "+githubToken)
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := GetJSON(ctx, u, hdr, &tree); err != nil {
		return nil, err
	}
	pkgs := map[string]string{}
	for _, e := range tree.Tree {
		if e.Type != "blob" {
			continue
		}
		atom, ver, ok := ebuildAtomVer(e.Path)
		if !ok || !isPkgPath(atom) {
			continue
		}
		cur, seen := pkgs[atom]
		pkgs[atom] = overlayPickVer(cur, seen, ver)
	}
	if tree.Truncated {
		return nil, fmt.Errorf("%s tree is truncated (%d entries)", o.repo, len(tree.Tree))
	}
	return pkgs, nil
}

func (pc *pkgCache) refresh(ctx context.Context) map[string]bool {
	return pc.refreshWith(ctx, overlays, fetchOverlay)
}

func (pc *pkgCache) refreshWith(
	ctx context.Context,
	sources []overlay,
	fetch func(context.Context, overlay) (map[string]string, error),
) map[string]bool {
	pc.mu.Lock()
	if pc.available == nil {
		pc.available = map[string]bool{}
	}
	fresh := len(pc.pkgs) > 0 && time.Since(pc.fetched) < pkgCacheTTL
	// Retry throttling prevents GitHub rate-limit storms during outages.
	throttled := time.Since(pc.lastAttempt) < pkgRetryFloor
	if fresh || pc.refreshing || throttled {
		status := pc.availabilityLocked(sources)
		pc.mu.Unlock()
		return status
	}
	pc.refreshing = true
	pc.lastAttempt = time.Now()
	pc.mu.Unlock()
	defer func() {
		pc.mu.Lock()
		pc.refreshing = false
		pc.mu.Unlock()
	}()

	allOK := true
	for _, o := range sources {
		m, err := fetch(ctx, o)
		if err != nil {
			log.Printf("pkg cache: %v", err)
			pc.mu.Lock()
			pc.available[o.name] = false
			pc.mu.Unlock()
			allOK = false
			continue
		}
		pc.mu.Lock()
		pc.pkgs[o.name] = m
		pc.available[o.name] = true
		pc.mu.Unlock()
		log.Printf("pkg cache: %s -> %d packages", o.name, len(m))
	}
	// Partial refreshes retry after the floor, not the full TTL.
	if allOK {
		pc.mu.Lock()
		pc.fetched = time.Now()
		pc.mu.Unlock()
	}
	return pc.availability(sources)
}

func (pc *pkgCache) availability(sources []overlay) map[string]bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.availabilityLocked(sources)
}

func (pc *pkgCache) availabilityLocked(sources []overlay) map[string]bool {
	status := make(map[string]bool, len(sources))
	for _, o := range sources {
		ok, known := pc.available[o.name]
		if !known {
			_, ok = pc.pkgs[o.name]
		}
		status[o.name] = ok
	}
	return status
}

func pn(atom string) string { return atom[strings.IndexByte(atom, '/')+1:] }

func (pc *pkgCache) search(name string) map[string][]string {
	low := strings.ToLower(name)
	full := strings.Contains(low, "/") // query includes a category -> match the whole atom
	res := map[string][]string{}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	for ov, atoms := range pc.pkgs {
		var exact, sub []string
		for atom := range atoms {
			p := strings.ToLower(pn(atom))
			if full {
				p = strings.ToLower(atom)
			}
			if p == low {
				exact = append(exact, atom)
			} else if strings.Contains(p, low) {
				sub = append(sub, atom)
			}
		}
		sort.Strings(exact)
		sort.Strings(sub)
		hits := append(exact, sub...)
		if len(hits) > maxHitsPerSource {
			hits = hits[:maxHitsPerSource]
		}
		if len(hits) > 0 {
			res[ov] = hits
		}
	}
	return res
}

func (pc *pkgCache) overlayVer(ov, atom string) string {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if m, ok := pc.pkgs[ov]; ok {
		return m[atom]
	}
	return ""
}

type verInfo struct {
	stable, latest string
	fetched        time.Time
}

var verC = struct {
	mu sync.Mutex
	m  map[string]verInfo
}{m: map[string]verInfo{}}

// pkgVersionJSON is one entry of packages.gentoo.org's package "versions" array.
type pkgVersionJSON struct {
	Version  string   `json:"version"`
	Keywords []string `json:"keywords"`
}

// Input is newest-first; 9999 is excluded from stable/latest releases.
func pickStableLatest(versions []pkgVersionJSON) (stable, latest string) {
	for _, vv := range versions {
		if strings.HasPrefix(vv.Version, "9999") { // skip live ebuilds
			continue
		}
		if latest == "" {
			latest = vv.Version
		}
		if stable == "" {
			for _, kw := range vv.Keywords {
				if kw == "amd64" {
					stable = vv.Version
					break
				}
			}
		}
		if latest != "" && stable != "" {
			break
		}
	}
	return stable, latest
}

// A 404 is an answered miss; transport, overload, and parse failures are unavailable.
func pkgVersion(ctx context.Context, atom string) (stable, latest string, available bool) {
	verC.mu.Lock()
	if v, ok := verC.m[atom]; ok && time.Since(v.fetched) < verCacheTTL {
		verC.mu.Unlock()
		return v.stable, v.latest, true
	}
	verC.mu.Unlock()

	var pj struct {
		Versions []pkgVersionJSON `json:"versions"`
	}
	err := GetJSON(ctx, "https://packages.gentoo.org/packages/"+atom+".json", nil, &pj)
	if err != nil {
		return "", "", httpStatusCode(err) == http.StatusNotFound
	}
	if len(pj.Versions) == 0 {
		return "", "", true
	}
	stable, latest = pickStableLatest(pj.Versions)
	verC.mu.Lock()
	if len(verC.m) >= pkgCacheMax {
		verC.m = map[string]verInfo{}
	}
	verC.m[atom] = verInfo{stable: stable, latest: latest, fetched: time.Now()}
	verC.mu.Unlock()
	return stable, latest, true
}

var pkgHrefRe = regexp.MustCompile(`/packages/([a-z][a-z0-9-]+/[A-Za-z0-9][A-Za-z0-9+_.\-]*)`)

// Availability prevents official-tree outages from rendering as "not found".
func searchMainTree(ctx context.Context, name string) ([]string, bool) {
	return searchMainTreeWith(ctx, name, pkgVersion, httpGetBody)
}

func searchMainTreeWith(
	ctx context.Context,
	name string,
	version func(context.Context, string) (string, string, bool),
	getBody func(context.Context, string, int64) ([]byte, error),
) ([]string, bool) {
	// Slashed atoms use authoritative JSON because the HTML search handles them poorly.
	if strings.Contains(name, "/") && isPkgPath(strings.ToLower(name)) {
		stable, latest, ok := version(ctx, name)
		if !ok {
			return nil, false
		}
		if stable != "" || latest != "" {
			return []string{name}, true
		}
		return nil, true
	}
	body, err := getBody(ctx, "https://packages.gentoo.org/packages/search?q="+url.QueryEscape(name), 2<<20)
	if err != nil {
		log.Printf("main tree search: %v", err)
		return nil, false
	}
	return rankSearchHits(body, name), true
}

// Re-rank deduplicated server hits while preserving its fuzzy matches.
// Exact package names and matching categories outrank incidental substrings.
func rankSearchHits(body []byte, name string) []string {
	seen := map[string]bool{}
	low := strings.ToLower(name)
	type scored struct {
		atom  string
		score int
	}
	var items []scored
	for _, m := range pkgHrefRe.FindAllStringSubmatch(string(body), -1) {
		atom := m[1]
		if seen[atom] || !isPkgPath(atom) {
			continue
		}
		seen[atom] = true
		items = append(items, scored{atom, pkgRelevance(atom, low)})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].score > items[j].score })
	hits := make([]string, 0, len(items))
	for _, it := range items {
		hits = append(hits, it.atom)
	}
	if len(hits) > maxHitsPerSource {
		hits = hits[:maxHitsPerSource]
	}
	return hits
}

// pkgRelevance ranks bare package queries.
func pkgRelevance(atom, q string) int {
	cat := ""
	if i := strings.IndexByte(atom, '/'); i > 0 {
		cat = strings.ToLower(atom[:i])
	}
	p := strings.ToLower(pn(atom))
	var score int
	switch {
	case p == q:
		score = 100
	case strings.Contains(cat, q):
		score = 50
	case strings.HasPrefix(p, q):
		score = 30
	case strings.Contains(p, q):
		score = 10
	}
	// Real packages outrank same-name metadata packages, which remain valid fallback hits.
	switch cat {
	case "virtual", "acct-group", "acct-user":
		score -= 5
	}
	return score
}

// Repology indexes bare names, while the Gentoo lookup retains the full atom.
func repologyQuery(name string) string {
	if strings.Contains(name, "/") && isPkgPath(strings.ToLower(name)) {
		return name[strings.LastIndexByte(name, '/')+1:]
	}
	return name
}

func commandArg(text string) string {
	// Fields accepts tabs and newlines from pasted commands.
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return ""
	}
	return strings.TrimSpace(strings.Join(fields[1:], " "))
}

// OnPkg handles package searches across the Gentoo tree and configured overlays.
func (v *Service) OnPkg(ctx *th.Context, update telego.Update) error {
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

	q := commandArg(msg.Text)
	if q == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, i18n.Messages.LookupPackages.Pkg.Usage.For(l))
		return nil
	}
	q = normalizeQuery(q)

	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	overlayOK := pkgC.refresh(hc)
	ovRes := pkgC.search(q)
	mainRes, mainOK := searchMainTree(hc, q)

	vm := map[string][2]string{}
	if len(mainRes) > 0 {
		var wg sync.WaitGroup
		var vmu sync.Mutex
		for _, a := range mainRes {
			wg.Add(1)
			go func(a string) {
				defer wg.Done()
				s, l, _ := pkgVersion(hc, a)
				vmu.Lock()
				vm[a] = [2]string{s, l}
				vmu.Unlock()
			}(a)
		}
		wg.Wait()
	}

	availability := pkgLookupAvailability{official: mainOK, overlays: overlayOK}
	plain := renderPkg(l, q, mainRes, vm, ovRes, availability)
	rich := ""
	if v.richEnabled() {
		rich = renderPkgRich(l, q, mainRes, vm, ovRes, availability)
	}
	v.sendRichOrHTML(c, bot, msg.Chat.ID, msg.MessageID, rich, plain)
	return nil
}

func renderPkg(l i18n.Lang, q string, mainRes []string, vm map[string][2]string, ovRes map[string][]string, availability pkgLookupAvailability) string {
	esc := html.EscapeString
	messages := i18n.Messages.LookupPackages.Pkg
	var b strings.Builder
	fmt.Fprintf(&b, "🔎 %s", messages.ResultsHeading.Render(l, "<b>"+esc(q)+"</b>"))
	found := false
	if len(mainRes) > 0 {
		found = true
		fmt.Fprintf(&b, "\n\n📦 <b>%s</b>", messages.OfficialHeading.For(l))
		for _, a := range mainRes {
			ver := ""
			if vm[a][0] != "" {
				ver = " — " + esc(vm[a][0]) // amd64-stable: no symbol
			} else if vm[a][1] != "" {
				ver = " — ~" + esc(vm[a][1]) // no amd64-stable version; latest need not have ~amd64
			}
			fmt.Fprintf(&b, "\n • <a href=\"%s\">%s</a>%s",
				esc("https://packages.gentoo.org/packages/"+a), esc(a), ver)
		}
	}
	for _, o := range overlays {
		hits := ovRes[o.name]
		if len(hits) == 0 {
			continue
		}
		found = true
		fmt.Fprintf(&b, "\n\n🧩 <b>%s</b>", esc(o.name))
		for _, a := range hits {
			ver := ""
			if vv := pkgC.overlayVer(o.name, a); vv != "" {
				ver = " — ~" + esc(vv) // the marker identifies an overlay version, not keyword status
			}
			fmt.Fprintf(&b, "\n • <a href=\"%s\">%s</a>%s",
				esc(o.treeURL(a)), esc(a), ver)
		}
	}
	if !found {
		if availability.anyUnavailable() {
			fmt.Fprintf(&b, "\n\n%s", messages.Unavailable.Render(l, availability.unavailableSources(l)))
		} else {
			fmt.Fprintf(&b, "\n\n%s", messages.NotFound.For(l))
		}
	} else {
		fmt.Fprintf(&b, "\n\n<i>%s</i>", messages.KeywordLegend.For(l))
		if availability.anyUnavailable() {
			fmt.Fprintf(&b, "\n<i>%s</i>", i18n.Messages.LookupPackages.Source.PartialResults.Render(l, availability.unavailableSources(l)))
		}
	}
	return b.String()
}

// Rich messages require block tags because newlines are ignored.
func renderPkgRich(l i18n.Lang, q string, mainRes []string, vm map[string][2]string, ovRes map[string][]string, availability pkgLookupAvailability) string {
	esc := html.EscapeString
	messages := i18n.Messages.LookupPackages.Pkg
	var b strings.Builder
	fmt.Fprintf(&b, "<h3>🔎 %s</h3>", messages.ResultsHeading.Render(l, esc(q)))
	found := false
	if len(mainRes) > 0 {
		found = true
		fmt.Fprintf(&b, "<h4>📦 %s</h4><ul>", messages.OfficialHeading.For(l))
		for _, a := range mainRes {
			ver := ""
			if vm[a][0] != "" {
				ver = " — " + esc(vm[a][0])
			} else if vm[a][1] != "" {
				ver = " — ~" + esc(vm[a][1])
			}
			fmt.Fprintf(&b, "<li><a href=\"%s\">%s</a>%s</li>",
				esc("https://packages.gentoo.org/packages/"+a), esc(a), ver)
		}
		b.WriteString("</ul>")
	}
	for _, o := range overlays {
		hits := ovRes[o.name]
		if len(hits) == 0 {
			continue
		}
		found = true
		fmt.Fprintf(
			&b,
			"<details><summary>🧩 <b>%s</b>%s</summary><ul>",
			esc(o.name),
			messages.OverlayCount.Render(l, len(hits)),
		)
		for _, a := range hits {
			ver := ""
			if vv := pkgC.overlayVer(o.name, a); vv != "" {
				ver = " — ~" + esc(vv)
			}
			fmt.Fprintf(&b, "<li><a href=\"%s\">%s</a>%s</li>",
				esc(o.treeURL(a)), esc(a), ver)
		}
		b.WriteString("</ul></details>")
	}
	if !found {
		if availability.anyUnavailable() {
			fmt.Fprintf(&b, "<p>%s</p>", messages.Unavailable.Render(l, availability.unavailableSources(l)))
		} else {
			fmt.Fprintf(&b, "<p>%s</p>", messages.NotFound.For(l))
		}
	} else {
		fmt.Fprintf(&b, "<footer><i>%s</i></footer>", messages.KeywordLegend.For(l))
		if availability.anyUnavailable() {
			fmt.Fprintf(&b, "<footer><i>%s</i></footer>", i18n.Messages.LookupPackages.Source.PartialResults.Render(l, availability.unavailableSources(l)))
		}
	}
	return b.String()
}

type useFlag struct {
	name string
	desc string
	def  bool // default-enabled (+ prefix)
}

// Preserve USE_EXPAND groups so large sets such as l10n do not flood local flags.
type useExpandGroup struct {
	name  string
	flags []useFlag
}

type pkgFullInfo struct {
	atom        string
	description string
	homepage    string
	stable      string
	latest      string
	local       []useFlag
	global      []useFlag
	expand      []useExpandGroup
	fetched     time.Time
}

var infoC = struct {
	mu sync.Mutex
	m  map[string]pkgFullInfo
}{m: map[string]pkgFullInfo{}}

type useEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func toUseFlags(in []useEntry) []useFlag {
	out := make([]useFlag, 0, len(in))
	for _, f := range in {
		out = append(out, useFlag{
			name: strings.TrimLeft(f.Name, "+-"),
			desc: f.Description,
			def:  strings.HasPrefix(f.Name, "+"),
		})
	}
	return out
}

// Only an authoritative 404 proves absence; other failures leave it unknown.
func officialInfo(ctx context.Context, atom string) (info pkgFullInfo, found, available bool) {
	infoC.mu.Lock()
	if v, ok := infoC.m[atom]; ok && time.Since(v.fetched) < verCacheTTL {
		infoC.mu.Unlock()
		return v, true, true
	}
	infoC.mu.Unlock()

	var pj struct {
		Description string           `json:"description"`
		Versions    []pkgVersionJSON `json:"versions"`
		Use         struct {
			Local  []useEntry `json:"local"`
			Global []useEntry `json:"global"`
		} `json:"use"`
		// USE_EXPAND is a sibling of use in the upstream schema.
		UseExpand []struct {
			Name  string     `json:"name"`
			Flags []useEntry `json:"flags"`
		} `json:"use_expand"`
	}
	err := GetJSON(ctx, "https://packages.gentoo.org/packages/"+atom+".json", nil, &pj)
	if err != nil {
		return pkgFullInfo{}, false, httpStatusCode(err) == http.StatusNotFound
	}
	info = pkgFullInfo{atom: atom, description: pj.Description, fetched: time.Now()}
	info.stable, info.latest = pickStableLatest(pj.Versions)
	info.local = toUseFlags(pj.Use.Local)
	info.global = toUseFlags(pj.Use.Global)
	for _, g := range pj.UseExpand {
		if fl := toUseFlags(g.Flags); len(fl) > 0 {
			info.expand = append(info.expand, useExpandGroup{name: g.Name, flags: fl})
		}
	}
	infoC.mu.Lock()
	if len(infoC.m) >= pkgCacheMax {
		infoC.m = map[string]pkgFullInfo{}
	}
	infoC.m[atom] = info
	infoC.mu.Unlock()
	return info, true, true
}

func fetchRaw(ctx context.Context, url string) []byte {
	b, _ := httpGetBody(ctx, url, 1<<20)
	return b
}

// Parse multiline IUSE assignments while dropping shell expressions.
func parseIUSE(eb []byte) []string {
	lines := strings.Split(string(eb), "\n")
	var toks []string
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(t, "IUSE=") && !strings.HasPrefix(t, "IUSE+=") {
			continue
		}
		q := strings.IndexByte(t, '"')
		if q < 0 {
			continue
		}
		content := t[q+1:]
		for {
			if end := strings.IndexByte(content, '"'); end >= 0 {
				toks = append(toks, strings.Fields(content[:end])...)
				break
			}
			toks = append(toks, strings.Fields(content)...)
			i++
			if i >= len(lines) {
				break
			}
			content = lines[i]
		}
	}
	out := make([]string, 0, len(toks))
	for _, tk := range toks {
		if tk == "" || strings.ContainsAny(tk, "${}()") {
			continue
		}
		out = append(out, tk)
	}
	return out
}

var ebuildFieldRe = map[string]*regexp.Regexp{}
var ebuildFieldMu sync.Mutex

func ebuildField(eb []byte, key string) string {
	ebuildFieldMu.Lock()
	re := ebuildFieldRe[key]
	if re == nil {
		re = regexp.MustCompile(`(?m)^` + key + `="?([^"\n]*)"?`)
		ebuildFieldRe[key] = re
	}
	ebuildFieldMu.Unlock()
	m := re.FindSubmatch(eb)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

var mdFlagRe = regexp.MustCompile(`(?s)<flag name="([^"]+)">(.*?)</flag>`)
var tagRe = regexp.MustCompile(`<[^>]+>`)
var wsRe = regexp.MustCompile(`\s+`)

func parseMetadataUse(md []byte) map[string]string {
	out := map[string]string{}
	for _, m := range mdFlagRe.FindAllSubmatch(md, -1) {
		desc := tagRe.ReplaceAllString(string(m[2]), "")
		desc = strings.TrimSpace(wsRe.ReplaceAllString(desc, " "))
		out[string(m[1])] = desc
	}
	return out
}

// Overlay metadata comes from the latest ebuild and metadata.xml.
func overlayInfo(ctx context.Context, o overlay, atom, version string) (pkgFullInfo, bool) {
	if version == "" {
		return pkgFullInfo{}, false
	}
	pkg := pn(atom)
	base := "https://raw.githubusercontent.com/" + o.repo + "/" + o.branch + "/" + atom + "/"
	eb := fetchRaw(ctx, base+pkg+"-"+version+".ebuild")
	if eb == nil {
		return pkgFullInfo{}, false
	}
	descs := map[string]string{}
	if md := fetchRaw(ctx, base+"metadata.xml"); md != nil {
		descs = parseMetadataUse(md)
	}
	info := pkgFullInfo{
		atom:        atom,
		description: ebuildField(eb, "DESCRIPTION"),
		latest:      version,
	}
	if hp := ebuildField(eb, "HOMEPAGE"); hp != "" {
		info.homepage = strings.Fields(hp)[0]
	}
	for _, n := range parseIUSE(eb) {
		clean := strings.TrimLeft(n, "+-")
		info.local = append(info.local, useFlag{name: clean, desc: descs[clean], def: strings.HasPrefix(n, "+")})
	}
	return info, true
}

// Keep compact output to one short, URL-free sentence.
func shortDesc(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "http"); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	if i := strings.IndexAny(s, ".。"); i > 8 {
		s = s[:i]
	}
	r := []rune(strings.TrimSpace(s))
	if len(r) > 64 {
		return strings.TrimSpace(string(r[:64])) + "…"
	}
	return string(r)
}

func flagMark(f useFlag) string {
	if f.def {
		return "+"
	}
	return ""
}

func useLink(f useFlag) string {
	u := "https://packages.gentoo.org/useflags/" + f.name
	return flagMark(f) + fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(u), html.EscapeString(f.name))
}

func writeLocalFlags(b *strings.Builder, l i18n.Lang, flags []useFlag) {
	if len(flags) == 0 {
		return
	}
	messages := i18n.Messages.LookupPackages.Use
	fmt.Fprintf(b, "\n<b>%s</b>%s", messages.LocalFlags.For(l), messages.Count.Render(l, len(flags)))
	for i, f := range flags {
		if i >= 12 && len(flags) > 12 {
			fmt.Fprintf(b, "\n%s", messages.TruncatedCount.Render(l, len(flags)))
			break
		}
		if d := shortDesc(f.desc); d != "" {
			fmt.Fprintf(b, "\n • %s — %s", useLink(f), html.EscapeString(d))
		} else {
			fmt.Fprintf(b, "\n • %s", useLink(f))
		}
	}
}

func writeGlobalFlags(b *strings.Builder, l i18n.Lang, flags []useFlag) {
	if len(flags) == 0 {
		return
	}
	links := make([]string, 0, len(flags))
	for _, f := range flags {
		links = append(links, useLink(f))
	}
	messages := i18n.Messages.LookupPackages.Use
	fmt.Fprintf(
		b,
		"\n<b>%s</b>%s%s%s",
		messages.GlobalFlags.For(l),
		messages.Count.Render(l, len(flags)),
		messages.ValueSeparator.For(l),
		strings.Join(links, " "),
	)
}

// Bound compact USE_EXPAND output; l10n commonly exceeds 100 values.
const expandCap = 16

// Compact output truncates values; the rich view retains full descriptions.
func writeExpandFlags(b *strings.Builder, l i18n.Lang, groups []useExpandGroup) {
	messages := i18n.Messages.LookupPackages.Use
	for _, g := range groups {
		if len(g.flags) == 0 {
			continue
		}
		names := make([]string, 0, expandCap)
		for i, f := range g.flags {
			if i >= expandCap {
				break
			}
			names = append(names, flagMark(f)+html.EscapeString(f.name))
		}
		more := ""
		if len(g.flags) > expandCap {
			more = messages.TruncatedCount.Render(l, len(g.flags))
		}
		fmt.Fprintf(
			b,
			"\n<b>%s</b>%s%s%s%s",
			html.EscapeString(strings.ToUpper(g.name)),
			messages.Count.Render(l, len(g.flags)),
			messages.ValueSeparator.For(l),
			strings.Join(names, " "),
			more,
		)
	}
}

func overlayByName(name string) (overlay, bool) {
	for _, o := range overlays {
		if o.name == name {
			return o, true
		}
	}
	return overlay{}, false
}

// overlayRefs renders linked overlay names for the cross-source footer.
func overlayRefs(l i18n.Lang, alsoIn []string, atom string) string {
	refs := make([]string, 0, len(alsoIn))
	for _, ovName := range alsoIn {
		ref := html.EscapeString(ovName)
		if o, ok := overlayByName(ovName); ok {
			ref = fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(o.treeURL(atom)), html.EscapeString(ovName))
		}
		refs = append(refs, ref)
	}
	return strings.Join(refs, i18n.Messages.LookupPackages.Source.ListSeparator.For(l))
}

func renderUse(l i18n.Lang, info pkgFullInfo, srcLabel, pkgURL string, overlay bool, alsoIn []string) string {
	esc := html.EscapeString
	messages := i18n.Messages.LookupPackages.Use
	var b strings.Builder
	label := ""
	if srcLabel != "" { // only overlay packages get a source label; official tree is implied
		label = messages.SourceLabel.Render(l, esc(srcLabel))
	}
	if pkgURL != "" {
		fmt.Fprintf(&b, "🧩 <a href=\"%s\"><b>%s</b></a>%s", esc(pkgURL), esc(info.atom), label)
	} else {
		fmt.Fprintf(&b, "🧩 <b>%s</b>%s", esc(info.atom), label)
	}
	if info.description != "" {
		fmt.Fprintf(&b, "\n%s", esc(info.description))
	}
	if info.homepage != "" {
		fmt.Fprintf(&b, "\n🏠 %s", esc(info.homepage))
	}
	switch {
	case info.stable != "" && info.latest != "" && info.latest != info.stable:
		fmt.Fprintf(&b, "\n%s", messages.VersionStableLatest.Render(l, esc(info.stable), esc(info.latest)))
	case info.stable != "":
		fmt.Fprintf(&b, "\n%s", messages.VersionStable.Render(l, esc(info.stable)))
	case info.latest != "":
		fmt.Fprintf(&b, "\n%s", messages.VersionLatest.Render(l, esc(info.latest)))
	}
	writeLocalFlags(&b, l, info.local)
	writeGlobalFlags(&b, l, info.global)
	writeExpandFlags(&b, l, info.expand)
	if len(info.local) == 0 && len(info.global) == 0 && len(info.expand) == 0 {
		fmt.Fprintf(&b, "\n%s", messages.NoFlags.For(l))
	}
	if len(alsoIn) > 0 {
		fmt.Fprintf(&b, "\n<i>%s</i>", messages.AlsoInOverlay.Render(l, overlayRefs(l, alsoIn, info.atom)))
	}
	if overlay {
		fmt.Fprintf(&b, "\n\n<i>%s</i>", messages.OverlayLegend.For(l))
	} else {
		fmt.Fprintf(&b, "\n\n<i>%s</i>", messages.OfficialLegend.For(l))
	}
	return b.String()
}

// Rich output keeps full flag descriptions in collapsible sections.
func renderUseRich(l i18n.Lang, info pkgFullInfo, srcLabel, pkgURL string, overlay bool, alsoIn []string) string {
	esc := html.EscapeString
	messages := i18n.Messages.LookupPackages.Use
	var b strings.Builder
	label := ""
	if srcLabel != "" {
		label = messages.SourceLabel.Render(l, esc(srcLabel))
	}
	if pkgURL != "" {
		fmt.Fprintf(&b, "<h3>🧩 <a href=\"%s\">%s</a>%s</h3>", esc(pkgURL), esc(info.atom), label)
	} else {
		fmt.Fprintf(&b, "<h3>🧩 %s%s</h3>", esc(info.atom), label)
	}
	// One paragraph with <br> avoids large inter-paragraph gaps.
	var hdr []string
	if info.description != "" {
		hdr = append(hdr, esc(info.description))
	}
	if info.homepage != "" {
		hdr = append(hdr, fmt.Sprintf("🏠 <a href=\"%s\">homepage</a>", esc(info.homepage)))
	}
	switch {
	case info.stable != "" && info.latest != "" && info.latest != info.stable:
		hdr = append(hdr, messages.VersionStableLatest.Render(l, esc(info.stable), esc(info.latest)))
	case info.stable != "":
		hdr = append(hdr, messages.VersionStable.Render(l, esc(info.stable)))
	case info.latest != "":
		hdr = append(hdr, messages.VersionLatest.Render(l, esc(info.latest)))
	}
	if len(hdr) > 0 {
		fmt.Fprintf(&b, "<p>%s</p>", strings.Join(hdr, "<br>"))
	}
	writeFlagsRich(&b, l, messages.LocalFlags.For(l), info.local, false)
	writeFlagsRich(&b, l, messages.GlobalFlags.For(l), info.global, true)
	writeExpandFlagsRich(&b, l, info.expand)
	if len(info.local) == 0 && len(info.global) == 0 && len(info.expand) == 0 {
		fmt.Fprintf(&b, "<p>%s</p>", messages.NoFlags.For(l))
	}
	if len(alsoIn) > 0 {
		fmt.Fprintf(&b, "<p>%s</p>", messages.AlsoInOverlay.Render(l, overlayRefs(l, alsoIn, info.atom)))
	}
	if overlay {
		fmt.Fprintf(&b, "<footer><i>%s</i></footer>", messages.OverlayLegend.For(l))
	} else {
		fmt.Fprintf(&b, "<footer><i>%s</i></footer>", messages.OfficialLegend.For(l))
	}
	return b.String()
}

// Rich messages require block structure; newlines are whitespace.
func writeFlagsRich(b *strings.Builder, l i18n.Lang, title string, flags []useFlag, collapse bool) {
	if len(flags) == 0 {
		return
	}
	count := i18n.Messages.LookupPackages.Use.Count.Render(l, len(flags))
	if collapse {
		fmt.Fprintf(b, "<details><summary><b>%s</b>%s</summary><ul>", title, count)
	} else {
		fmt.Fprintf(b, "<p><b>%s</b>%s</p><ul>", title, count)
	}
	for _, f := range flags {
		if f.desc != "" {
			fmt.Fprintf(b, "<li>%s — %s</li>", useLink(f), html.EscapeString(f.desc))
		} else {
			fmt.Fprintf(b, "<li>%s</li>", useLink(f))
		}
	}
	b.WriteString("</ul>")
	if collapse {
		b.WriteString("</details>")
	}
}

// Each large USE_EXPAND group gets its own collapsible section.
func writeExpandFlagsRich(b *strings.Builder, l i18n.Lang, groups []useExpandGroup) {
	for _, g := range groups {
		if len(g.flags) == 0 {
			continue
		}
		fmt.Fprintf(
			b,
			"<details><summary><b>%s</b>%s</summary><ul>",
			html.EscapeString(strings.ToUpper(g.name)),
			i18n.Messages.LookupPackages.Use.Count.Render(l, len(g.flags)),
		)
		for _, f := range g.flags {
			if f.desc != "" {
				fmt.Fprintf(b, "<li>%s%s — %s</li>", flagMark(f), html.EscapeString(f.name), html.EscapeString(f.desc))
			} else {
				fmt.Fprintf(b, "<li>%s%s</li>", flagMark(f), html.EscapeString(f.name))
			}
		}
		b.WriteString("</ul></details>")
	}
}

// Normalize supported package and overlay URLs to category/package atoms.
func normalizeQuery(q string) string {
	q = strings.TrimSpace(q)
	q = strings.SplitN(q, "?", 2)[0]
	q = strings.SplitN(q, "#", 2)[0]
	if i := strings.Index(q, "packages.gentoo.org/packages/"); i >= 0 {
		rest := strings.TrimRight(q[i+len("packages.gentoo.org/packages/"):], "/")
		if parts := strings.SplitN(rest, "/", 3); len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			atom := parts[0] + "/" + strings.TrimSuffix(parts[1], ".json")
			if isPkgPath(strings.ToLower(atom)) {
				return atom
			}
		}
	}
	if strings.Contains(q, "github.com/") {
		for _, marker := range []string{"/tree/", "/blob/"} {
			if i := strings.Index(q, marker); i >= 0 {
				// layout after the marker is <branch>/<category>/<package>[/...]
				if segs := strings.Split(strings.TrimRight(q[i+len(marker):], "/"), "/"); len(segs) >= 3 {
					atom := segs[1] + "/" + segs[2]
					if isPkgPath(strings.ToLower(atom)) {
						return atom
					}
				}
			}
		}
	}
	return q
}

type useSrc struct {
	official bool
	ovs      []string
}

// Empty matches are definitive only when every source answered.
func resolveUseSources(ctx context.Context, q string, overlayOK map[string]bool) (map[string]*useSrc, pkgLookupAvailability) {
	return resolveUseSourcesWith(ctx, q, overlayOK, officialInfo, searchMainTree)
}

func resolveUseSourcesWith(
	ctx context.Context,
	q string,
	overlayOK map[string]bool,
	info func(context.Context, string) (pkgFullInfo, bool, bool),
	search func(context.Context, string) ([]string, bool),
) (map[string]*useSrc, pkgLookupAvailability) {
	srcs := map[string]*useSrc{}
	availability := pkgLookupAvailability{overlays: overlayOK}
	get := func(a string) *useSrc {
		s := srcs[a]
		if s == nil {
			s = &useSrc{}
			srcs[a] = s
		}
		return s
	}

	low := strings.ToLower(q)
	if strings.Contains(low, "/") && isPkgPath(low) {
		_, found, ok := info(ctx, q)
		availability.official = ok
		if found {
			get(q).official = true
		}
		for _, o := range overlays {
			if pkgC.overlayVer(o.name, q) != "" {
				s := get(q)
				s.ovs = append(s.ovs, o.name)
			}
		}
		return srcs, availability
	}
	atoms, ok := search(ctx, q)
	availability.official = ok
	for _, a := range atoms {
		if strings.EqualFold(pn(a), q) {
			get(a).official = true
		}
	}
	for ov, list := range pkgC.search(q) {
		for _, a := range list {
			if strings.EqualFold(pn(a), q) {
				s := get(a)
				s.ovs = append(s.ovs, ov)
			}
		}
	}
	return srcs, availability
}

func renderUseLookupMiss(l i18n.Lang, q string, availability pkgLookupAvailability) string {
	messages := i18n.Messages.LookupPackages.Use
	if availability.anyUnavailable() {
		return messages.Unavailable.Render(l, q, availability.unavailableSources(l))
	}
	return messages.NotFound.Render(l, q)
}

func appendUseAvailabilityNote(l i18n.Lang, plain, rich string, availability pkgLookupAvailability) (string, string) {
	if !availability.anyUnavailable() {
		return plain, rich
	}
	note := i18n.Messages.LookupPackages.Source.PartialResults.Render(l, availability.unavailableSources(l))
	plain += "\n\n<i>" + note + "</i>"
	if rich != "" {
		rich += "<footer><i>" + note + "</i></footer>"
	}
	return plain, rich
}

// OnUse handles package metadata and USE flag lookups.
func (v *Service) OnUse(ctx *th.Context, update telego.Update) error {
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
	q := commandArg(msg.Text)
	if q == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, i18n.Messages.LookupPackages.Use.Usage.For(l))
		return nil
	}
	q = normalizeQuery(q)
	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	overlayOK := pkgC.refresh(hc)

	srcs, availability := resolveUseSources(hc, q, overlayOK)

	switch len(srcs) {
	case 0:
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, renderUseLookupMiss(l, q, availability))
		return nil
	case 1:
		// A unique atom needs no disambiguation.
	default:
		atoms := make([]string, 0, len(srcs))
		for a := range srcs {
			atoms = append(atoms, a)
		}
		sort.Strings(atoms)
		var b strings.Builder
		b.WriteString(i18n.Messages.LookupPackages.Use.MultipleMatches.For(l))
		for _, a := range atoms {
			fmt.Fprintf(&b, "\n • /use %s", a)
		}
		if availability.anyUnavailable() {
			fmt.Fprintf(&b, "\n%s", i18n.Messages.LookupPackages.Use.PartialMatches.Render(l, availability.unavailableSources(l)))
		}
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, b.String())
		return nil
	}

	var atom string
	var s *useSrc
	for a, ss := range srcs {
		atom, s = a, ss
	}

	out, outRich := "", ""
	if s.official {
		if info, found, _ := officialInfo(hc, atom); found {
			url := "https://packages.gentoo.org/packages/" + atom
			out = renderUse(l, info, "", url, false, s.ovs)
			if v.richEnabled() {
				outRich = renderUseRich(l, info, "", url, false, s.ovs)
			}
		}
	}
	if out == "" && len(s.ovs) > 0 {
		ovName := s.ovs[0]
		o, _ := overlayByName(ovName)
		if info, ok := overlayInfo(hc, o, atom, pkgC.overlayVer(ovName, atom)); ok {
			url := o.treeURL(atom)
			out = renderUse(l, info, "overlay:"+ovName, url, true, s.ovs[1:])
			if v.richEnabled() {
				outRich = renderUseRich(l, info, "overlay:"+ovName, url, true, s.ovs[1:])
			}
		}
	}
	if out == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, i18n.Messages.LookupPackages.Use.InfoUnavailable.Render(l, atom))
		return nil
	}
	out, outRich = appendUseAvailabilityNote(l, out, outRich, availability)
	v.sendRichOrHTML(c, bot, msg.Chat.ID, msg.MessageID, outRich, out)
	return nil
}

// ok distinguishes lookup failure from a package with no arm64 keyword.
func armStatus(ctx context.Context, atom string) (stable, testing string, ok bool) {
	var pj struct {
		Versions []pkgVersionJSON `json:"versions"`
	}
	if err := GetJSON(ctx, "https://packages.gentoo.org/packages/"+atom+".json", nil, &pj); err != nil {
		return "", "", false
	}
	stable, testing = arm64Keywords(pj.Versions)
	return stable, testing, true
}

// packages.gentoo.org returns newest first; live ebuilds are skipped.
func arm64Keywords(versions []pkgVersionJSON) (stable, testing string) {
	for _, vv := range versions {
		if strings.HasPrefix(vv.Version, "9999") {
			continue
		}
		for _, kw := range vv.Keywords {
			switch kw {
			case "arm64":
				if stable == "" {
					stable = vv.Version
				}
			case "~arm64":
				if testing == "" {
					testing = vv.Version
				}
			}
		}
	}
	return stable, testing
}

// Failed searches remain distinct from authoritative package misses.
func lookupArm(
	ctx context.Context,
	l i18n.Lang,
	name string,
	search func(context.Context, string) ([]string, bool),
	status func(context.Context, string) (string, string, bool),
) (body string, useHTML bool) {
	messages := i18n.Messages.LookupPackages.Arm
	atoms, available := search(ctx, name)
	if !available {
		return messages.OfficialUnavailable.For(l), false
	}
	if len(atoms) == 0 {
		return messages.NotFound.Render(l, name), false
	}
	atom := atoms[0]
	stable, testing, ok := status(ctx, atom)
	url := "https://packages.gentoo.org/packages/" + atom
	esc := html.EscapeString

	var b strings.Builder
	b.WriteString(messages.Heading.Render(l, esc(url), esc(atom)))
	switch {
	case !ok:
		b.WriteString("\n" + messages.KeywordUnavailable.For(l))
	case stable != "" && testing != "" && stable != testing:
		b.WriteString(messages.StableTesting.Render(l, esc(stable), esc(testing)))
	case stable != "":
		b.WriteString(messages.StableOnly.Render(l, esc(stable)))
	case testing != "":
		b.WriteString(messages.TestingOnly.Render(l, esc(testing)))
	default:
		b.WriteString("\n" + messages.NoKeyword.For(l))
	}
	return b.String(), true
}

// OnArm handles Gentoo arm64 keyword lookups.
func (v *Service) OnArm(ctx *th.Context, update telego.Update) error {
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
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, i18n.Messages.LookupPackages.Arm.Usage.For(l))
		return nil
	}
	hc, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()
	body, useHTML := lookupArm(hc, l, name, searchMainTree, armStatus)
	if useHTML {
		v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, body)
	} else {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, body)
	}
	return nil
}
