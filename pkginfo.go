package main

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

type useFlag struct {
	name string
	desc string
	def  bool // default-enabled (+ prefix)
}

type pkgFullInfo struct {
	atom        string
	description string
	homepage    string
	stable      string
	latest      string
	local       []useFlag
	global      []useFlag
	fetched     time.Time
}

var infoC = struct {
	mu sync.Mutex
	m  map[string]pkgFullInfo
}{m: map[string]pkgFullInfo{}}

// useEntry mirrors packages.gentoo.org's USE flag JSON ({name, description}).
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

// officialInfo fetches description + USE flags + versions for an official-tree atom (cached).
func officialInfo(ctx context.Context, atom string) (pkgFullInfo, bool) {
	infoC.mu.Lock()
	if v, ok := infoC.m[atom]; ok && time.Since(v.fetched) < verCacheTTL {
		infoC.mu.Unlock()
		return v, true
	}
	infoC.mu.Unlock()

	var pj struct {
		Description string           `json:"description"`
		Versions    []pkgVersionJSON `json:"versions"`
		Use         struct {
			Local  []useEntry `json:"local"`
			Global []useEntry `json:"global"`
		} `json:"use"`
	}
	if err := httpGetJSON(ctx, "https://packages.gentoo.org/packages/"+atom+".json", nil, &pj); err != nil {
		return pkgFullInfo{}, false
	}
	info := pkgFullInfo{atom: atom, description: pj.Description, fetched: time.Now()}
	info.stable, info.latest = pickStableLatest(pj.Versions)
	info.local = toUseFlags(pj.Use.Local)
	info.global = toUseFlags(pj.Use.Global)
	infoC.mu.Lock()
	infoC.m[atom] = info
	infoC.mu.Unlock()
	return info, true
}

func fetchRaw(ctx context.Context, url string) []byte {
	b, _ := httpGetBody(ctx, url, 1<<20)
	return b
}

// parseIUSE extracts USE flag tokens from an ebuild's IUSE="..."/IUSE+="..."
// assignments (handles multi-line; drops tokens containing shell metachars).
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

// overlayInfo best-effort extracts description/homepage/USE for an overlay package
// from its latest ebuild (IUSE) + metadata.xml (flag descriptions), via raw.githubusercontent.com.
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

// shortDesc trims a USE flag description to one short line (first sentence, no
// URLs, capped) so /use stays compact.
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

// useLink renders a flag as "[+]name" with the name linked to its useflags page.
func useLink(f useFlag) string {
	u := "https://packages.gentoo.org/useflags/" + f.name
	return flagMark(f) + fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(u), html.EscapeString(f.name))
}

// writeLocalFlags lists package-specific flags with a one-line description.
func writeLocalFlags(b *strings.Builder, flags []useFlag) {
	if len(flags) == 0 {
		return
	}
	fmt.Fprintf(b, "\n<b>本地 USE</b>(%d)", len(flags))
	for i, f := range flags {
		if i >= 12 && len(flags) > 12 {
			fmt.Fprintf(b, "\n …(共 %d 个)", len(flags))
			break
		}
		if d := shortDesc(f.desc); d != "" {
			fmt.Fprintf(b, "\n • %s — %s", useLink(f), html.EscapeString(d))
		} else {
			fmt.Fprintf(b, "\n • %s", useLink(f))
		}
	}
}

// writeGlobalFlags lists generic flags as a compact name-only line (Gentoo users know them).
func writeGlobalFlags(b *strings.Builder, flags []useFlag) {
	if len(flags) == 0 {
		return
	}
	links := make([]string, 0, len(flags))
	for _, f := range flags {
		links = append(links, useLink(f))
	}
	fmt.Fprintf(b, "\n<b>全局 USE</b>(%d):%s", len(flags), strings.Join(links, " "))
}

// overlayByName looks up a configured overlay by its display name.
func overlayByName(name string) (overlay, bool) {
	for _, o := range overlays {
		if o.name == name {
			return o, true
		}
	}
	return overlay{}, false
}

// overlayRefs renders the overlays in alsoIn (that also carry atom) as a
// comma-separated list of linked names — the "overlay 也有此包" footer.
func overlayRefs(alsoIn []string, atom string) string {
	refs := make([]string, 0, len(alsoIn))
	for _, ovName := range alsoIn {
		ref := html.EscapeString(ovName)
		if o, ok := overlayByName(ovName); ok {
			ref = fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(o.treeURL(atom)), html.EscapeString(ovName))
		}
		refs = append(refs, ref)
	}
	return strings.Join(refs, ", ")
}

func renderUse(info pkgFullInfo, srcLabel, pkgURL string, overlay bool, alsoIn []string) string {
	esc := html.EscapeString
	var b strings.Builder
	label := ""
	if srcLabel != "" { // only overlay packages get a source label; official tree is implied
		label = "(" + esc(srcLabel) + ")"
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
		fmt.Fprintf(&b, "\n版本:%s  ~%s", esc(info.stable), esc(info.latest))
	case info.stable != "":
		fmt.Fprintf(&b, "\n版本:%s", esc(info.stable))
	case info.latest != "":
		fmt.Fprintf(&b, "\n版本:~%s", esc(info.latest))
	}
	writeLocalFlags(&b, info.local)
	writeGlobalFlags(&b, info.global)
	if len(info.local) == 0 && len(info.global) == 0 {
		b.WriteString("\n(该包无 USE 标志)")
	}
	if len(alsoIn) > 0 {
		fmt.Fprintf(&b, "\n<i>overlay 也有此包:%s</i>", overlayRefs(alsoIn, info.atom))
	}
	if overlay {
		b.WriteString("\n\n<i>overlay · USE 取自最新 ebuild,可能不全;+ 为默认开启</i>")
	} else {
		b.WriteString("\n\n<i>+ 为默认开启;~ 为测试版</i>")
	}
	return b.String()
}

// sendRichOrHTML sends via Bot API 10.1 sendRichMessage when enabled (richer, for
// upgraded clients), and falls back to a plain HTML message if rich is off or the
// server rejects it (e.g. Bot API < 10.1). Client-side render failures can't be
// detected here — that's the accepted trade-off, kept off the verification path.
func (v *Verifier) sendRichOrHTML(c context.Context, bot *telego.Bot, chatID int64, richHTML, plainHTML string) {
	if v.isRichEnabled() && richHTML != "" {
		params := (&telego.SendRichMessageParams{}).
			WithChatID(tu.ID(chatID)).
			WithRichMessage(*(&telego.InputRichMessage{}).WithHTML(richHTML).WithSkipEntityDetection())
		if _, err := bot.SendRichMessage(c, params); err == nil {
			return
		}
	}
	_, _ = bot.SendMessage(c, htmlMessage(chatID, plainHTML))
}

// renderUseRich builds the Bot API 10.1 rich-message /use — no truncation, full flag
// descriptions, and the (long) global USE list inside a collapsible <details> block.
func renderUseRich(info pkgFullInfo, srcLabel, pkgURL string, overlay bool, alsoIn []string) string {
	esc := html.EscapeString
	var b strings.Builder
	label := ""
	if srcLabel != "" {
		label = " (" + esc(srcLabel) + ")"
	}
	if pkgURL != "" {
		fmt.Fprintf(&b, "<h3>🧩 <a href=\"%s\">%s</a>%s</h3>", esc(pkgURL), esc(info.atom), label)
	} else {
		fmt.Fprintf(&b, "<h3>🧩 %s%s</h3>", esc(info.atom), label)
	}
	// pack description + homepage + version into ONE block; separate <p> blocks each
	// get paragraph spacing (= big gaps), so use <br> as a light intra-block break.
	var hdr []string
	if info.description != "" {
		hdr = append(hdr, esc(info.description))
	}
	if info.homepage != "" {
		hdr = append(hdr, fmt.Sprintf("🏠 <a href=\"%s\">homepage</a>", esc(info.homepage)))
	}
	switch {
	case info.stable != "" && info.latest != "" && info.latest != info.stable:
		hdr = append(hdr, "版本:"+esc(info.stable)+"  ~"+esc(info.latest))
	case info.stable != "":
		hdr = append(hdr, "版本:"+esc(info.stable))
	case info.latest != "":
		hdr = append(hdr, "版本:~"+esc(info.latest))
	}
	if len(hdr) > 0 {
		fmt.Fprintf(&b, "<p>%s</p>", strings.Join(hdr, "<br>"))
	}
	writeFlagsRich(&b, "本地 USE", info.local, false)
	writeFlagsRich(&b, "全局 USE", info.global, true)
	if len(info.local) == 0 && len(info.global) == 0 {
		b.WriteString("<p>(该包无 USE 标志)</p>")
	}
	if len(alsoIn) > 0 {
		fmt.Fprintf(&b, "<p>overlay 也有此包:%s</p>", overlayRefs(alsoIn, info.atom))
	}
	if overlay {
		b.WriteString("<footer><i>overlay · USE 取自最新 ebuild,可能不全;+ 为默认开启</i></footer>")
	} else {
		b.WriteString("<footer><i>+ 为默认开启;~ 为测试版</i></footer>")
	}
	return b.String()
}

// writeFlagsRich renders a USE-flag section for rich messages as a <ul> with full
// descriptions; a long section (global) is wrapped in a collapsible <details>.
// Rich messages treat newlines as whitespace, so structure MUST be block tags.
func writeFlagsRich(b *strings.Builder, title string, flags []useFlag, collapse bool) {
	if len(flags) == 0 {
		return
	}
	if collapse {
		fmt.Fprintf(b, "<details><summary><b>%s</b>(%d)</summary><ul>", title, len(flags))
	} else {
		fmt.Fprintf(b, "<p><b>%s</b>(%d)</p><ul>", title, len(flags))
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

// normalizeQuery turns a pasted packages.gentoo.org / GitHub-overlay tree URL
// into a "category/package" atom; otherwise returns the input unchanged. Shared by /pkg and /use.
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

// useSrc records where a /use atom was found: the official tree and/or named overlays.
type useSrc struct {
	official bool
	ovs      []string
}

// resolveUseSources finds which atom(s) match q for /use and from which sources. An
// exact "category/package" query resolves directly via the official JSON + overlay
// caches; a bare name is matched (name-exact) against the official tree search and
// the overlay caches. Returns atom -> sources (caller picks single / disambiguates).
func resolveUseSources(ctx context.Context, q string) map[string]*useSrc {
	srcs := map[string]*useSrc{}
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
		if _, ok := officialInfo(ctx, q); ok {
			get(q).official = true
		}
		for _, o := range overlays {
			if pkgC.overlayVer(o.name, q) != "" {
				s := get(q)
				s.ovs = append(s.ovs, o.name)
			}
		}
		return srcs
	}
	for _, a := range searchMainTree(ctx, q) {
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
	return srcs
}

// onUse handles /use <package> — show one package's USE flags + info (multi-source aware).
func (v *Verifier) onUse(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	q := commandArg(msg.Text)
	if q == "" {
		v.notify(c, bot, msg.Chat.ID, "用法:/use <包名>,例如 /use vim、/use app-editors/vim,或粘贴 packages.gentoo.org 链接")
		return nil
	}
	q = normalizeQuery(q)
	hc, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	pkgC.refresh(hc)

	srcs := resolveUseSources(hc, q)

	switch len(srcs) {
	case 0:
		v.notify(c, bot, msg.Chat.ID, fmt.Sprintf("没找到精确匹配「%s」的包。模糊搜索试试 /pkg %s", q, q))
		return nil
	case 1:
		// fall through below
	default:
		atoms := make([]string, 0, len(srcs))
		for a := range srcs {
			atoms = append(atoms, a)
		}
		sort.Strings(atoms)
		var b strings.Builder
		b.WriteString("匹配到多个包,请用完整名指定其一:")
		for _, a := range atoms {
			fmt.Fprintf(&b, "\n • /use %s", a)
		}
		v.notify(c, bot, msg.Chat.ID, b.String())
		return nil
	}

	var atom string
	var s *useSrc
	for a, ss := range srcs {
		atom, s = a, ss
	}

	out, outRich := "", ""
	if s.official {
		if info, ok := officialInfo(hc, atom); ok {
			url := "https://packages.gentoo.org/packages/" + atom
			out = renderUse(info, "", url, false, s.ovs)
			if v.isRichEnabled() {
				outRich = renderUseRich(info, "", url, false, s.ovs)
			}
		}
	}
	if out == "" && len(s.ovs) > 0 {
		ovName := s.ovs[0]
		o, _ := overlayByName(ovName)
		if info, ok := overlayInfo(hc, o, atom, pkgC.overlayVer(ovName, atom)); ok {
			url := o.treeURL(atom)
			out = renderUse(info, "overlay:"+ovName, url, true, s.ovs[1:])
			if v.isRichEnabled() {
				outRich = renderUseRich(info, "overlay:"+ovName, url, true, s.ovs[1:])
			}
		}
	}
	if out == "" {
		v.notify(c, bot, msg.Chat.ID, fmt.Sprintf("暂时取不到 %s 的信息,稍后再试。", atom))
		return nil
	}
	v.sendRichOrHTML(c, bot, msg.Chat.ID, outRich, out)
	return nil
}
