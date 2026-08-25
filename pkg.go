package main

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

func configurePkg(cfg *Config) {
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
const pkgKeywordLegend = "官方树：~ 为无 amd64 稳定 keyword 的最新版，无符号为 amd64 稳定版；overlay：~ 仅标记 overlay 版本"

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
	if err := httpGetJSON(ctx, u, hdr, &tree); err != nil {
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
	err := httpGetJSON(ctx, "https://packages.gentoo.org/packages/"+atom+".json", nil, &pj)
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

func (v *Verifier) onPkg(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.queryAllowed(ctx, msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()

	q := commandArg(msg.Text)
	if q == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, "用法:/pkg <包名>,例如 /pkg vim,或粘贴链接 /pkg https://packages.gentoo.org/packages/app-editors/vim")
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
	plain := renderPkg(q, mainRes, vm, ovRes, availability)
	rich := ""
	if v.isRichEnabled() {
		rich = renderPkgRich(q, mainRes, vm, ovRes, availability)
	}
	v.sendRichOrHTML(c, bot, msg.Chat.ID, msg.MessageID, rich, plain)
	return nil
}

func renderPkg(q string, mainRes []string, vm map[string][2]string, ovRes map[string][]string, availability pkgLookupAvailability) string {
	esc := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, "🔎 <b>%s</b> 的搜索结果", esc(q))
	found := false
	if len(mainRes) > 0 {
		found = true
		b.WriteString("\n\n📦 <b>官方树 gentoo</b>")
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
			b.WriteString("\n\n部分来源暂时无法查询，目前无法确认是否有匹配的包，请稍后重试。")
		} else {
			b.WriteString("\n\n没找到匹配的包,换个更短的关键词试试?")
		}
	} else {
		b.WriteString("\n\n<i>" + pkgKeywordLegend + "</i>")
		if availability.anyUnavailable() {
			b.WriteString("\n<i>部分来源暂时无法查询，以上结果可能不完整。</i>")
		}
	}
	return b.String()
}

// Rich messages require block tags because newlines are ignored.
func renderPkgRich(q string, mainRes []string, vm map[string][2]string, ovRes map[string][]string, availability pkgLookupAvailability) string {
	esc := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, "<h3>🔎 %s 的搜索结果</h3>", esc(q))
	found := false
	if len(mainRes) > 0 {
		found = true
		b.WriteString("<h4>📦 官方树 gentoo</h4><ul>")
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
		fmt.Fprintf(&b, "<details><summary>🧩 <b>%s</b>(%d)</summary><ul>", esc(o.name), len(hits))
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
			b.WriteString("<p>部分来源暂时无法查询，目前无法确认是否有匹配的包，请稍后重试。</p>")
		} else {
			b.WriteString("<p>没找到匹配的包,换个更短的关键词试试?</p>")
		}
	} else {
		b.WriteString("<footer><i>" + pkgKeywordLegend + "</i></footer>")
		if availability.anyUnavailable() {
			b.WriteString("<footer><i>部分来源暂时无法查询，以上结果可能不完整。</i></footer>")
		}
	}
	return b.String()
}
