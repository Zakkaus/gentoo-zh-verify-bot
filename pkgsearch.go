package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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

// shared outbound User-Agent for all HTTP requests this bot makes.
const userAgent = "gentoo-zh-verify-bot"

// overlays searched by /pkg, populated from config at startup (default gentoo-zh + guru).
var overlays []overlay

func configurePkg(cfg *Config) {
	if len(cfg.Overlays) == 0 {
		overlays = []overlay{
			{name: "gentoo-zh", repo: "microcai/gentoo-zh", branch: "master"},
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
const pkgRetryFloor = 3 * time.Minute // throttle refresh retries after a failure (avoids GitHub rate-limit storms)

var httpClient = &http.Client{Timeout: 25 * time.Second}

// githubToken (optional, from the GITHUB_TOKEN env var) lifts the GitHub API rate
// limit from 60/h to 5000/h. Reading public repos needs a token with NO scopes.
var githubToken string

// pkgCache holds, per overlay, a map of "category/package" atom -> latest version string.
type pkgCache struct {
	mu          sync.Mutex
	pkgs        map[string]map[string]string
	fetched     time.Time
	lastAttempt time.Time
	refreshing  bool
}

var pkgC = &pkgCache{pkgs: map[string]map[string]string{}}

// isPkgPath reports whether p looks like a Gentoo "category/package" path.
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

// splitVer breaks a Gentoo version into comparable tokens.
func splitVer(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '_' })
}

// verLess reports whether version a is older than b (best-effort; good enough to pick "latest").
func verLess(a, b string) bool {
	as, bs := splitVer(a), splitVer(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		nx, ex := strconv.Atoi(as[i])
		ny, ey := strconv.Atoi(bs[i])
		if ex == nil && ey == nil {
			if nx != ny {
				return nx < ny
			}
		} else if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
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

// fetchOverlay returns atom -> latest version for one overlay, via the cached GitHub recursive tree.
func fetchOverlay(ctx context.Context, o overlay) (map[string]string, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1", o.repo, o.branch)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	if githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+githubToken)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github trees %s: HTTP %d", o.repo, resp.StatusCode)
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
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
		if cur, seen := pkgs[atom]; !seen || verLess(cur, ver) {
			pkgs[atom] = ver
		}
	}
	if tree.Truncated {
		log.Printf("pkg cache: %s tree truncated (%d entries)", o.repo, len(tree.Tree))
	}
	return pkgs, nil
}

func (pc *pkgCache) refresh(ctx context.Context) {
	pc.mu.Lock()
	fresh := len(pc.pkgs) > 0 && time.Since(pc.fetched) < pkgCacheTTL
	// throttle retries after a failure: don't re-attempt within pkgRetryFloor, so a
	// failing overlay can't make every /pkg re-hit the GitHub API (rate-limit storm)
	throttled := time.Since(pc.lastAttempt) < pkgRetryFloor
	if fresh || pc.refreshing || throttled {
		pc.mu.Unlock()
		return
	}
	pc.refreshing = true
	pc.lastAttempt = time.Now()
	pc.mu.Unlock()
	defer func() { pc.mu.Lock(); pc.refreshing = false; pc.mu.Unlock() }()

	allOK := true
	for _, o := range overlays {
		m, err := fetchOverlay(ctx, o)
		if err != nil {
			log.Printf("pkg cache: %v", err)
			allOK = false
			continue
		}
		pc.mu.Lock()
		pc.pkgs[o.name] = m
		pc.mu.Unlock()
		log.Printf("pkg cache: %s -> %d packages", o.name, len(m))
	}
	// only mark fresh when every overlay succeeded, so a transient failure on one
	// doesn't freeze partial results for the whole TTL
	if allOK {
		pc.mu.Lock()
		pc.fetched = time.Now()
		pc.mu.Unlock()
	}
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

// verInfo: amd64-stable version and the newest version of an official-tree package.
type verInfo struct {
	stable, latest string
	fetched        time.Time
}

var verC = struct {
	mu sync.Mutex
	m  map[string]verInfo
}{m: map[string]verInfo{}}

// pkgVersion returns (amd64-stable, newest) versions for a "cat/pkg" atom via packages.gentoo.org JSON.
func pkgVersion(ctx context.Context, atom string) (string, string) {
	verC.mu.Lock()
	if v, ok := verC.m[atom]; ok && time.Since(v.fetched) < verCacheTTL {
		verC.mu.Unlock()
		return v.stable, v.latest
	}
	verC.mu.Unlock()

	u := "https://packages.gentoo.org/packages/" + atom + ".json"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	var pj struct {
		Versions []struct {
			Version  string   `json:"version"`
			Keywords []string `json:"keywords"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pj); err != nil || len(pj.Versions) == 0 {
		return "", ""
	}
	latest := pj.Versions[0].Version // packages.gentoo.org lists newest first
	stable := ""
	for _, vv := range pj.Versions { // first (newest) version stable on amd64
		for _, kw := range vv.Keywords {
			if kw == "amd64" {
				stable = vv.Version
				break
			}
		}
		if stable != "" {
			break
		}
	}
	verC.mu.Lock()
	verC.m[atom] = verInfo{stable: stable, latest: latest, fetched: time.Now()}
	verC.mu.Unlock()
	return stable, latest
}

var pkgHrefRe = regexp.MustCompile(`/packages/([a-z][a-z0-9-]+/[A-Za-z0-9][A-Za-z0-9+_.\-]*)`)

// searchMainTree queries packages.gentoo.org (official tree) and extracts matching atoms.
func searchMainTree(ctx context.Context, name string) []string {
	// A "category/package" query is an exact atom — resolve it directly via the
	// authoritative JSON (the search page doesn't match slashed queries well).
	if strings.Contains(name, "/") && isPkgPath(strings.ToLower(name)) {
		if s, l := pkgVersion(ctx, name); s != "" || l != "" {
			return []string{name}
		}
		return nil
	}
	u := "https://packages.gentoo.org/packages/search?q=" + url.QueryEscape(name)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("main tree search: %v", err)
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	seen := map[string]bool{}
	low := strings.ToLower(name)
	type scored struct {
		atom  string
		score int
	}
	var items []scored
	// Re-rank the server's results: a package literally named the query, or whose
	// CATEGORY contains it (sys-kernel/* for "kernel", dev-python/* for "python"), is
	// more relevant than an incidental substring match (dev-ml/core_kernel). We do NOT
	// drop non-matches — Gentoo strips version suffixes (fcitx5 → app-i18n/fcitx), so
	// the server's fuzzy hits stay (score 0) in page order.
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

// pkgRelevance scores how well an atom matches a bare query, to rank search results.
func pkgRelevance(atom, q string) int {
	cat := ""
	if i := strings.IndexByte(atom, '/'); i > 0 {
		cat = strings.ToLower(atom[:i])
	}
	p := strings.ToLower(pn(atom))
	switch {
	case p == q:
		return 100
	case strings.Contains(cat, q):
		return 50
	case strings.HasPrefix(p, q):
		return 30
	case strings.Contains(p, q):
		return 10
	default:
		return 0
	}
}

func commandArg(text string) string {
	parts := strings.SplitN(strings.TrimSpace(text), " ", 2)
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// onPkg handles /pkg <name> — searches the official tree + the configured overlays, with versions.
func (v *Verifier) onPkg(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()

	q := commandArg(msg.Text)
	if q == "" {
		v.notify(c, bot, msg.Chat.ID, "用法:/pkg <包名>,例如 /pkg yay,或粘贴链接 /pkg https://packages.gentoo.org/packages/app-editors/vim")
		return nil
	}
	q = normalizeQuery(q)

	hc, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	pkgC.refresh(hc)
	ovRes := pkgC.search(q)
	mainRes := searchMainTree(hc, q)

	// fetch official-tree versions concurrently
	vm := map[string][2]string{}
	if len(mainRes) > 0 {
		var wg sync.WaitGroup
		var vmu sync.Mutex
		for _, a := range mainRes {
			wg.Add(1)
			go func(a string) {
				defer wg.Done()
				s, l := pkgVersion(hc, a)
				vmu.Lock()
				vm[a] = [2]string{s, l}
				vmu.Unlock()
			}(a)
		}
		wg.Wait()
	}

	plain := renderPkg(q, mainRes, vm, ovRes, false)
	rich := ""
	if v.cfg.RichMessages {
		rich = renderPkg(q, mainRes, vm, ovRes, true)
	}
	v.sendRichOrHTML(c, bot, msg.Chat.ID, rich, plain)
	return nil
}

// renderPkg builds the /pkg result message. In rich mode each overlay section is a
// collapsible <details> so long multi-source results stay compact.
func renderPkg(q string, mainRes []string, vm map[string][2]string, ovRes map[string][]string, rich bool) string {
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
				ver = " — ~" + esc(vm[a][1]) // testing only: ~arch
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
		if rich {
			fmt.Fprintf(&b, "\n<details><summary>🧩 <b>%s</b>(%d)</summary>", esc(o.name), len(hits))
		} else {
			fmt.Fprintf(&b, "\n\n🧩 <b>%s</b>", esc(o.name))
		}
		for _, a := range hits {
			ver := ""
			if vv := pkgC.overlayVer(o.name, a); vv != "" {
				ver = " — ~" + esc(vv) // overlay packages are testing (~arch)
			}
			fmt.Fprintf(&b, "\n • <a href=\"%s\">%s</a>%s",
				esc("https://github.com/"+o.repo+"/tree/"+o.branch+"/"+a), esc(a), ver)
		}
		if rich {
			b.WriteString("\n</details>")
		}
	}
	if !found {
		b.WriteString("\n\n没找到匹配的包,换个更短的关键词试试?")
	} else {
		b.WriteString("\n\n<i>~ 为测试版(~arch);无符号为 amd64 稳定版</i>")
	}
	return b.String()
}
