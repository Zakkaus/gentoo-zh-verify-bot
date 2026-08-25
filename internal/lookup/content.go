package lookup

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

var bugIDRe = regexp.MustCompile(`^[0-9]{1,9}$`)

type bugInfo struct {
	summary, status, resolution, product, component, severity string
}
type bugLookupState uint8

const (
	bugLookupUnavailable bugLookupState = iota
	bugLookupFound
	bugLookupNotFound
)

type bugResponse struct {
	Error bool `json:"error"`
	Bugs  []struct {
		Summary    string `json:"summary"`
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
		Product    string `json:"product"`
		Component  string `json:"component"`
		Severity   string `json:"severity"`
	} `json:"bugs"`
}

// Only Bugzilla's finite enum values are localized; official identifiers stay unchanged.
var (
	bugStatusZH = map[string]string{
		"UNCONFIRMED": "未确认", "CONFIRMED": "已确认", "IN_PROGRESS": "处理中",
		"RESOLVED": "已解决", "VERIFIED": "已验证",
	}
	bugResolutionZH = map[string]string{
		"FIXED": "已修复", "WONTFIX": "不予修复", "CANTFIX": "无法修复", "DUPLICATE": "重复",
		"INVALID": "无效", "WORKSFORME": "无法复现", "OBSOLETE": "已过时", "UPSTREAM": "需向上游报告",
		"PKGREMOVED": "软件包已移除", "NEEDINFO": "需补充信息", "TEST-REQUEST": "待测试", "PENDING-UPSTREAM": "待上游",
	}
	bugSeverityZH = map[string]string{
		"blocker": "阻断", "critical": "严重", "major": "重大", "normal": "普通",
		"minor": "次要", "trivial": "轻微", "enhancement": "功能请求",
	}
	bugPriorityZH = map[string]string{
		"Highest": "最高", "High": "高", "Normal": "普通", "Low": "低", "Lowest": "最低",
	}
)

// TranslateBugValue localizes a known Bugzilla enum value when zh is true.
func TranslateBugValue(v string, zh bool) string {
	if !zh {
		return v
	}
	for _, values := range [...]map[string]string{bugStatusZH, bugResolutionZH, bugSeverityZH, bugPriorityZH} {
		if translated, ok := values[v]; ok {
			return translated
		}
	}
	return v
}

// Only an HTTP 404 is authoritative; malformed, restricted, and failed responses are retryable.
func fetchBug(ctx context.Context, id string) (bugInfo, bugLookupState) {
	u := "https://bugs.gentoo.org/rest/bug/" + id +
		"?include_fields=summary,status,resolution,product,component,severity"
	var br bugResponse
	if err := GetJSON(ctx, u, nil, &br); err != nil {
		if httpStatusCode(err) == http.StatusNotFound {
			return bugInfo{}, bugLookupNotFound
		}
		return bugInfo{}, bugLookupUnavailable
	}
	if br.Error || len(br.Bugs) == 0 {
		return bugInfo{}, bugLookupUnavailable
	}
	b := br.Bugs[0]
	return bugInfo{b.Summary, b.Status, b.Resolution, b.Product, b.Component, b.Severity}, bugLookupFound
}

func bugLookupFailureMessage(id, link string, state bugLookupState) string {
	if state == bugLookupNotFound {
		return fmt.Sprintf("❓ Bug %s 不存在。", id)
	}
	return fmt.Sprintf("❓ 暂时无法获取 Bug %s 的详情，请稍后重试。可直接查看：%s", id, link)
}

// OnBug handles Gentoo Bugzilla lookups.
func (v *Service) OnBug(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.queryAllowed(ctx, msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	id := commandArg(msg.Text)
	if !bugIDRe.MatchString(id) {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, "用法:/bug <编号>,例如 /bug 900000")
		return nil
	}
	link := "https://bugs.gentoo.org/" + id

	hc, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()
	info, state := fetchBug(hc, id)
	if state != bugLookupFound {
		// Keep unsuccessful lookups on the reply-linked cleanup path.
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, bugLookupFailureMessage(id, link, state))
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🐞 <a href=\"%s\">Bug %s</a>\n%s\n", link, id, html.EscapeString(info.summary))
	status := TranslateBugValue(info.status, true)
	if info.resolution != "" {
		status += " / " + TranslateBugValue(info.resolution, true)
	}
	fmt.Fprintf(&b, "状态:%s", html.EscapeString(status))
	if info.severity != "" {
		fmt.Fprintf(&b, " · 严重性:%s", html.EscapeString(TranslateBugValue(info.severity, true)))
	}
	if info.product != "" {
		comp := info.product
		if info.component != "" {
			comp += " › " + info.component
		}
		fmt.Fprintf(&b, "\n产品:%s", html.EscapeString(comp))
	}
	v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, b.String())
	return nil
}

// NewsItem is one parsed Gentoo news index entry.
type NewsItem struct {
	// Date is the publication date encoded in the news URL.
	Date string
	// Title is the upstream news title.
	Title string
	// URL is the absolute upstream news URL.
	URL string
}

const newsTTL = 30 * time.Minute

var newsURL = "https://www.gentoo.org/support/news-items/"
var newsBase = "https://www.gentoo.org"

func configureNews(cfg *config.Config) {
	if cfg.NewsURL != "" {
		newsURL = cfg.NewsURL
	}
	if u, err := url.Parse(newsURL); err == nil && u.Scheme != "" && u.Host != "" {
		newsBase = u.Scheme + "://" + u.Host
	}
}

// Gentoo news links encode the publication date in their path.
var newsRe = regexp.MustCompile(`href="(/support/news-items/(\d{4}-\d{2}-\d{2})-[^"]+\.html)"[^>]*>([^<]+)<`)

var newsC = struct {
	mu      sync.Mutex
	items   []NewsItem
	fetched time.Time
	loading bool
}{}

// FetchNews fetches and parses the current Gentoo news index without using the command cache.
func FetchNews(c context.Context) ([]NewsItem, error) {
	body, err := httpGetBody(c, newsURL, 2<<20)
	if err != nil {
		return nil, err
	}
	items := parseNews(body)
	if len(items) == 0 && len(body) > 0 {
		// Treat markup drift as unavailable so it cannot become an authoritative empty index.
		return nil, fmt.Errorf("parsed 0 items from %d bytes of %s; the news page layout may have changed", len(body), newsURL)
	}
	return items, nil
}

func parseNews(body []byte) []NewsItem {
	seen := map[string]bool{}
	var items []NewsItem
	for _, m := range newsRe.FindAllStringSubmatch(string(body), -1) {
		path, date, title := m[1], m[2], strings.TrimSpace(m[3])
		if seen[path] || title == "" {
			continue
		}
		seen[path] = true
		items = append(items, NewsItem{Date: date, Title: title, URL: newsBase + path})
	}
	return items
}

// GetNews returns cached Gentoo news and whether the index was available for this lookup.
func GetNews(c context.Context) ([]NewsItem, bool) {
	newsC.mu.Lock()
	// Freshness follows fetch time so an empty success is cached.
	fresh := !newsC.fetched.IsZero() && time.Since(newsC.fetched) < newsTTL
	if fresh || newsC.loading {
		items := newsC.items
		newsC.mu.Unlock()
		return items, fresh
	}
	newsC.loading = true
	newsC.mu.Unlock()
	defer func() { newsC.mu.Lock(); newsC.loading = false; newsC.mu.Unlock() }()

	items, err := FetchNews(c)
	if err != nil {
		log.Printf("news fetch: %v", err)
		newsC.mu.Lock()
		old := newsC.items
		newsC.mu.Unlock()
		return old, false
	}
	newsC.mu.Lock()
	newsC.items, newsC.fetched = items, time.Now()
	newsC.mu.Unlock()
	return items, true
}

func renderNews(arg string, items []NewsItem, available bool) string {
	q := strings.ToLower(arg)
	var b strings.Builder
	if q == "" {
		b.WriteString("📰 Gentoo 最新新闻:")
	} else {
		fmt.Fprintf(&b, "📰 Gentoo 新闻搜索「%s」:", html.EscapeString(arg))
	}
	n := 0
	for _, it := range items {
		if q != "" && !strings.Contains(strings.ToLower(it.Title), q) && !strings.Contains(strings.ToLower(it.URL), q) {
			continue
		}
		title := html.EscapeString(html.UnescapeString(it.Title))
		fmt.Fprintf(&b, "\n • <a href=\"%s\">%s — %s</a>", html.EscapeString(it.URL), it.Date, title)
		n++
		if n >= 8 {
			break
		}
	}
	if n == 0 {
		if available {
			b.WriteString("\n没找到匹配的新闻。")
		} else {
			b.WriteString("\n暂时无法获取新闻列表，请稍后重试。")
		}
	} else if !available {
		b.WriteString("\n新闻列表暂时无法更新，以上结果可能不完整，请稍后重试。")
	}
	return b.String()
}

// OnNews handles Gentoo news lookups.
func (v *Service) OnNews(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.queryAllowed(ctx, msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	items, available := GetNews(hc)
	arg := commandArg(msg.Text)
	b := renderNews(arg, items, available)
	v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, b)
	return nil
}

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
	if err := GetJSON(ctx, u, nil, &resp); err != nil {
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
	if err := GetJSON(ctx, u, nil, &resp); err != nil {
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

// OnWiki handles Gentoo and Arch wiki searches.
func (v *Service) OnWiki(ctx *th.Context, update telego.Update) error {
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

const archcnForum = "https://forum.archlinuxcn.org"

type forumTopic struct{ title, url string }

// ok distinguishes a fetch failure from an authoritative empty search.
func searchArchcn(ctx context.Context, query string, limit int) (topics []forumTopic, ok bool) {
	u := archcnForum + "/search.json?q=" + url.QueryEscape(query)
	var resp struct {
		Topics []struct {
			ID    int    `json:"id"`
			Slug  string `json:"slug"`
			Title string `json:"title"`
		} `json:"topics"`
	}
	if err := GetJSON(ctx, u, nil, &resp); err != nil {
		return nil, false // transient fetch failure — NOT a genuine "no results"
	}
	out := make([]forumTopic, 0, limit)
	for _, t := range resp.Topics {
		out = append(out, forumTopic{t.Title, fmt.Sprintf("%s/t/%s/%d", archcnForum, t.Slug, t.ID)})
		if len(out) >= limit {
			break
		}
	}
	return out, true
}

var forumLinks = []struct{ name, site string }{
	{"Gentoo 论坛", "forums.gentoo.org"},
	{"Arch BBS", "bbs.archlinux.org"},
	{"Ubuntu 论坛", "ubuntuforums.org"},
	{"Debian 论坛", "forums.debian.net"},
}

func ddgSiteSearch(site, query string) string {
	return "https://duckduckgo.com/?q=" + url.QueryEscape("site:"+site+" "+query)
}

// OnBbs handles Linux forum searches.
func (v *Service) OnBbs(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.queryAllowed(ctx, msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	q := commandArg(msg.Text)
	if q == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, "用法:/bbs <关键词>,例如 /bbs nvidia 黑屏 —— 搜各大 Linux 论坛(中文优先)")
		return nil
	}
	hc, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()

	var b strings.Builder
	fmt.Fprintf(&b, "💬 <b>%s</b> 的论坛搜索", html.EscapeString(q))
	hits, archcnOK := searchArchcn(hc, q, 5)
	switch {
	case len(hits) > 0:
		b.WriteString("\n\n<b>Arch Linux CN 论坛</b>")
		for _, h := range hits {
			fmt.Fprintf(&b, "\n • <a href=\"%s\">%s</a>", html.EscapeString(h.url), html.EscapeString(h.title))
		}
	case !archcnOK: // the fetch failed — honest transient message, not a false "no results"
		b.WriteString("\n\n暂时无法获取 Arch Linux CN 论坛的结果(请稍后重试)。")
	default:
		b.WriteString("\n\nArch Linux CN 论坛暂无匹配结果。")
	}
	b.WriteString("\n\n其它论坛(点按钮搜索):")

	// Telegram rejects the whole reply when a button URL exceeds its limit.
	qBtn := q
	if r := []rune(qBtn); len(r) > 200 {
		qBtn = string(r[:200])
	}
	var rows [][]telego.InlineKeyboardButton
	for i := 0; i < len(forumLinks); i += 2 {
		var row []telego.InlineKeyboardButton
		for j := i; j < i+2 && j < len(forumLinks); j++ {
			row = append(row, telego.InlineKeyboardButton{Text: forumLinks[j].name, URL: ddgSiteSearch(forumLinks[j].site, qBtn)})
		}
		rows = append(rows, row)
	}
	sent, err := bot.SendMessage(c, tg.HTMLMessage(msg.Chat.ID, b.String()).
		WithReplyMarkup(tu.InlineKeyboard(rows...)).
		WithReplyParameters(tg.ReplyParameters(msg.MessageID)))
	if err != nil {
		// Preserve inline results when Telegram rejects the buttons.
		log.Printf("/bbs send with buttons failed (%v) — retrying text-only", err)
		sent, _ = bot.SendMessage(c, tg.HTMLMessage(msg.Chat.ID, b.String()).WithReplyParameters(tg.ReplyParameters(msg.MessageID)))
	}
	v.scheduleLookupCleanup(bot, msg.Chat.ID, msg.MessageID, tg.MessageID(sent))
	return nil
}
