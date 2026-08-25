package main

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// classify groups translation titles by base topic and supported language.
type wikiSource struct {
	name      string
	api       string
	titleBase string
	classify  func(title string) (base, lang string)
}

// Gentoo translation subpages use short language suffixes; longer subpages are content.
var gentooLangRe = regexp.MustCompile(`/([a-z]{2}(?:-[a-z]{2,4})?)$`)

func classifyGentoo(title string) (string, string) {
	m := gentooLangRe.FindStringSubmatch(title)
	if m == nil {
		return title, "en"
	}
	base := title[:len(title)-len(m[0])]
	switch m[1] {
	case "zh-cn", "zh-hans":
		return base, "zh"
	default:
		return base, "other"
	}
}

// Arch translation titles use a parenthesized language label.
var archLangRe = regexp.MustCompile(` \(([^)]+)\)$`)

func classifyArch(title string) (string, string) {
	m := archLangRe.FindStringSubmatch(title)
	if m == nil {
		return title, "en"
	}
	base := title[:len(title)-len(m[0])]
	if m[1] == "简体中文" {
		return base, "zh"
	}
	return base, "other"
}

var wikiSources = []wikiSource{
	{name: "Gentoo", api: "https://wiki.gentoo.org/api.php", titleBase: "https://wiki.gentoo.org/wiki/", classify: classifyGentoo},
	{name: "Arch", api: "https://wiki.archlinux.org/api.php", titleBase: "https://wiki.archlinux.org/title/", classify: classifyArch},
}

// Escape each path segment separately so MediaWiki subpages retain their slashes.
func wikiTitlePath(title string) string {
	parts := strings.Split(strings.ReplaceAll(title, " ", "_"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (w wikiSource) pageURL(title string) string { return w.titleBase + wikiTitlePath(title) }

// Drop untagged foreign-language pages from the English fallback.
func hasNonASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return true
		}
	}
	return false
}

func cleanDisplayTitle(s string) string {
	return html.UnescapeString(strings.TrimSpace(tagRe.ReplaceAllString(s, "")))
}

// ok distinguishes a failed wiki fetch from an authoritative empty search.
func searchTitles(ctx context.Context, w wikiSource, query string, limit int) (titles []string, ok bool) {
	u := fmt.Sprintf("%s?action=query&list=search&srsearch=%s&srlimit=%d&srprop=&format=json",
		w.api, url.QueryEscape(query), limit)
	var resp struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := httpGetJSON(ctx, u, nil, &resp); err != nil {
		return nil, false // transient fetch failure — NOT a genuine "no results"
	}
	out := make([]string, 0, len(resp.Query.Search))
	for _, s := range resp.Query.Search {
		out = append(out, s.Title)
	}
	return out, true
}

// Display titles supply localized headings for canonical page names.
func displayTitles(ctx context.Context, w wikiSource, titles []string) map[string]string {
	out := map[string]string{}
	if len(titles) == 0 {
		return out
	}
	u := fmt.Sprintf("%s?action=query&prop=info&inprop=displaytitle&format=json&titles=%s",
		w.api, url.QueryEscape(strings.Join(titles, "|")))
	var resp struct {
		Query struct {
			Pages map[string]struct {
				Title        string `json:"title"`
				Displaytitle string `json:"displaytitle"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := httpGetJSON(ctx, u, nil, &resp); err != nil {
		return out
	}
	for _, p := range resp.Query.Pages {
		if p.Displaytitle != "" {
			out[p.Title] = p.Displaytitle
		}
	}
	return out
}

// Dedupe topics case-insensitively, preferring Simplified Chinese over English.
func (w wikiSource) pickWikiTitles(titles []string, max int) []string {
	type entry struct{ title, lang string }
	chosen := map[string]entry{}
	var order []string
	for _, t := range titles {
		base, lang := w.classify(t)
		if lang == "other" || (lang == "en" && hasNonASCII(base)) {
			continue
		}
		key := strings.ToLower(base) // case-insensitive topic key
		if cur, ok := chosen[key]; ok {
			if cur.lang != "zh" && lang == "zh" { // upgrade en -> zh for the same topic
				chosen[key] = entry{t, lang}
			}
			continue
		}
		chosen[key] = entry{t, lang}
		order = append(order, key)
	}
	var zh, en []string
	for _, b := range order {
		if chosen[b].lang == "zh" {
			zh = append(zh, chosen[b].title)
		} else {
			en = append(en, chosen[b].title)
		}
	}
	out := append(zh, en...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}
func wikiResultNotice(found bool, srcOK []bool) string {
	var b strings.Builder
	missing := 0
	for i, ok := range srcOK {
		if ok {
			continue
		}
		if missing == 0 {
			b.WriteString("\n\n以下来源暂时无法查询，结果可能不完整：")
		} else {
			b.WriteString("、")
		}
		b.WriteString(wikiSources[i].name)
		b.WriteString(" Wiki")
		missing++
	}
	if missing > 0 {
		b.WriteString("。请稍后重试。")
		return b.String()
	}
	if !found {
		return "\n\n没找到相关条目，换个关键词试试？"
	}
	return ""
}

func (v *Verifier) onWiki(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.queryAllowed(ctx, msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	q := commandArg(msg.Text)
	if q == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, "用法:/wiki <关键词>,例如 /wiki systemd boot —— 搜索 Gentoo / Arch wiki(优先简体中文页)")
		return nil
	}
	hc, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()

	titles := make([][]string, len(wikiSources))
	dtitles := make([]map[string]string, len(wikiSources))
	srcOK := make([]bool, len(wikiSources))
	var wg sync.WaitGroup
	for i, w := range wikiSources {
		wg.Add(1)
		go func(i int, w wikiSource) {
			defer wg.Done()
			raw, ok := searchTitles(hc, w, q, 24)
			srcOK[i] = ok
			titles[i] = w.pickWikiTitles(raw, 4)
			dtitles[i] = displayTitles(hc, w, titles[i])
		}(i, w)
	}
	wg.Wait()

	var b strings.Builder
	fmt.Fprintf(&b, "📚 <b>%s</b> 的 wiki 搜索", html.EscapeString(q))
	found := false
	for i, w := range wikiSources {
		if len(titles[i]) == 0 {
			continue
		}
		found = true
		fmt.Fprintf(&b, "\n\n<b>%s Wiki</b>", html.EscapeString(w.name))
		for _, t := range titles[i] {
			label := cleanDisplayTitle(dtitles[i][t])
			if label == "" {
				label = t
			}
			fmt.Fprintf(&b, "\n • <a href=\"%s\">%s</a>", html.EscapeString(w.pageURL(t)), html.EscapeString(label))
		}
	}
	b.WriteString(wikiResultNotice(found, srcOK))
	v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, b.String())
	return nil
}
