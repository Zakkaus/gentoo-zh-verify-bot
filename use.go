package main

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

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
	err := httpGetJSON(ctx, "https://packages.gentoo.org/packages/"+atom+".json", nil, &pj)
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

// Bound compact USE_EXPAND output; l10n commonly exceeds 100 values.
const expandCap = 16

// Compact output truncates values; the rich view retains full descriptions.
func writeExpandFlags(b *strings.Builder, groups []useExpandGroup) {
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
			more = fmt.Sprintf(" …(共 %d)", len(g.flags))
		}
		fmt.Fprintf(b, "\n<b>%s</b>(%d):%s%s", html.EscapeString(strings.ToUpper(g.name)), len(g.flags), strings.Join(names, " "), more)
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
	writeExpandFlags(&b, info.expand)
	if len(info.local) == 0 && len(info.global) == 0 && len(info.expand) == 0 {
		b.WriteString("\n(该包无 USE 标志)")
	}
	if len(alsoIn) > 0 {
		fmt.Fprintf(&b, "\n<i>overlay 也有此包:%s</i>", overlayRefs(alsoIn, info.atom))
	}
	if overlay {
		b.WriteString("\n\n<i>overlay · USE 取自最新 ebuild,可能不完整;+ 表示默认启用</i>")
	} else {
		b.WriteString("\n\n<i>+ 表示默认启用;~ 表示测试 keyword</i>")
	}
	return b.String()
}

// Bot API 10.1 rich messages fall back to HTML on server rejection.
// Client-side rendering failures are not observable.
func (v *Verifier) sendRichOrHTML(c context.Context, bot *telego.Bot, chatID int64, replyTo int, richHTML, plainHTML string) {
	ttl, on := v.lookupAutoDelete()
	if !on {
		ttl = 0
	}
	v.telegram(bot).SendRichOrHTML(c, chatID, replyTo, richHTML, plainHTML, v.isRichEnabled(), ttl)
}

// Rich output keeps full flag descriptions in collapsible sections.
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
	writeExpandFlagsRich(&b, info.expand)
	if len(info.local) == 0 && len(info.global) == 0 && len(info.expand) == 0 {
		b.WriteString("<p>(该包无 USE 标志)</p>")
	}
	if len(alsoIn) > 0 {
		fmt.Fprintf(&b, "<p>overlay 也有此包:%s</p>", overlayRefs(alsoIn, info.atom))
	}
	if overlay {
		b.WriteString("<footer><i>overlay · USE 取自最新 ebuild,可能不完整;+ 表示默认启用</i></footer>")
	} else {
		b.WriteString("<footer><i>+ 表示默认启用;~ 表示测试 keyword</i></footer>")
	}
	return b.String()
}

// Rich messages require block structure; newlines are whitespace.
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

// Each large USE_EXPAND group gets its own collapsible section.
func writeExpandFlagsRich(b *strings.Builder, groups []useExpandGroup) {
	for _, g := range groups {
		if len(g.flags) == 0 {
			continue
		}
		fmt.Fprintf(b, "<details><summary><b>%s</b>(%d)</summary><ul>", html.EscapeString(strings.ToUpper(g.name)), len(g.flags))
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

func renderUseLookupMiss(q string, availability pkgLookupAvailability) string {
	if availability.anyUnavailable() {
		return fmt.Sprintf("部分来源暂时无法查询，目前无法确认是否有精确匹配「%s」的包，请稍后重试。", q)
	}
	return fmt.Sprintf("没找到精确匹配「%s」的包。模糊搜索试试 /pkg %s", q, q)
}

func appendUseAvailabilityNote(plain, rich string, availability pkgLookupAvailability) (string, string) {
	if !availability.anyUnavailable() {
		return plain, rich
	}
	plain += "\n\n<i>部分来源暂时无法查询，以上结果可能不完整。</i>"
	if rich != "" {
		rich += "<footer><i>部分来源暂时无法查询，以上结果可能不完整。</i></footer>"
	}
	return plain, rich
}

func (v *Verifier) onUse(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.queryAllowed(ctx, msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	q := commandArg(msg.Text)
	if q == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, "用法:/use <包名>,例如 /use vim、/use app-editors/vim,或粘贴 packages.gentoo.org 链接")
		return nil
	}
	q = normalizeQuery(q)
	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	overlayOK := pkgC.refresh(hc)

	srcs, availability := resolveUseSources(hc, q, overlayOK)

	switch len(srcs) {
	case 0:
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, renderUseLookupMiss(q, availability))
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
		b.WriteString("匹配到多个包,请用完整名指定其一:")
		for _, a := range atoms {
			fmt.Fprintf(&b, "\n • /use %s", a)
		}
		if availability.anyUnavailable() {
			b.WriteString("\n部分来源暂时无法查询，以上匹配结果可能不完整。")
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
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, fmt.Sprintf("暂时无法获取 %s 的信息,请稍后重试。", atom))
		return nil
	}
	out, outRich = appendUseAvailabilityNote(out, outRich, availability)
	v.sendRichOrHTML(c, bot, msg.Chat.ID, msg.MessageID, outRich, out)
	return nil
}
