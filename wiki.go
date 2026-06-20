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

// wikiSource is a MediaWiki site searchable by /wiki. classify maps a result title to
// its base topic + language ("zh" = simplified Chinese, "en" = default, "other" = drop),
// so /wiki prefers the zh-cn page and falls back to the default page when there's none.
type wikiSource struct {
	name      string
	api       string
	titleBase string
	classify  func(title string) (base, lang string)
}

// Gentoo wiki translations are "/<langcode>" subpages (e.g. Btrfs/zh-cn, Btrfs/fr); a
// real content subpage (Systemd/systemd-nspawn) is longer than a langcode so it stays "en".
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

// Arch wiki translations are "Title (Language)"; simplified Chinese is "(简体中文)".
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

// wikiTitlePath turns a page title into a URL path: spaces -> underscores, each
// "/"-separated segment percent-encoded (so subpages and non-ASCII titles both work).
func wikiTitlePath(title string) string {
	parts := strings.Split(strings.ReplaceAll(title, " ", "_"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (w wikiSource) pageURL(title string) string { return w.titleBase + wikiTitlePath(title) }

// hasNonASCII reports whether s has any non-ASCII rune — used to drop foreign-language
// pages that aren't tagged as a translation (e.g. Arch's "Kernel/Compilação").
func hasNonASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return true
		}
	}
	return false
}

// cleanDisplayTitle strips the HTML markup MediaWiki wraps around a displaytitle.
func cleanDisplayTitle(s string) string {
	return html.UnescapeString(strings.TrimSpace(tagRe.ReplaceAllString(s, "")))
}

// searchTitles queries one MediaWiki site and returns matching page titles in rank order.
func searchTitles(ctx context.Context, w wikiSource, query string, limit int) []string {
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
		return nil
	}
	out := make([]string, 0, len(resp.Query.Search))
	for _, s := range resp.Query.Search {
		out = append(out, s.Title)
	}
	return out
}

// displayTitles fetches the display title (the Chinese H1 for Gentoo /zh-cn pages) for the
// given canonical titles, as a title -> displaytitle map.
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

// pickWikiTitles drops other/foreign-language pages, dedupes by base topic preferring the
// zh-cn page, and returns titles with zh first then en (rank order preserved), capped at max.
func (w wikiSource) pickWikiTitles(titles []string, max int) []string {
	type entry struct{ title, lang string }
	chosen := map[string]entry{}
	var order []string
	for _, t := range titles {
		base, lang := w.classify(t)
		if lang == "other" || (lang == "en" && hasNonASCII(base)) {
			continue
		}
		if cur, ok := chosen[base]; ok {
			if cur.lang != "zh" && lang == "zh" { // upgrade en -> zh for the same topic
				chosen[base] = entry{t, lang}
			}
			continue
		}
		chosen[base] = entry{t, lang}
		order = append(order, base)
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

// onWiki handles /wiki <query> — searches the Gentoo and Arch wikis (MediaWiki) and posts
// the top hits inline, preferring simplified-Chinese pages and showing each page's display
// title (Chinese for zh-cn pages). Both wikis run concurrently.
func (v *Verifier) onWiki(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	q := commandArg(msg.Text)
	if q == "" {
		v.notify(c, bot, msg.Chat.ID, "用法:/wiki <关键词>,例如 /wiki systemd boot —— 搜索 Gentoo / Arch wiki(优先简体中文页)")
		return nil
	}
	hc, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()

	titles := make([][]string, len(wikiSources))
	dtitles := make([]map[string]string, len(wikiSources))
	var wg sync.WaitGroup
	for i, w := range wikiSources {
		wg.Add(1)
		go func(i int, w wikiSource) {
			defer wg.Done()
			titles[i] = w.pickWikiTitles(searchTitles(hc, w, q, 24), 4)
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
	if !found {
		b.WriteString("\n\n没找到相关条目,换个关键词试试?")
	}
	sent, _ := bot.SendMessage(c, htmlMessage(msg.Chat.ID, b.String()).WithReplyParameters(replyParams(msg.MessageID)))
	v.scheduleLookupCleanup(bot, msg.Chat.ID, msg.MessageID, msgID(sent))
	return nil
}
