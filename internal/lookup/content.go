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
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
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
	bugStatusMessages = map[string]i18n.Text{
		"UNCONFIRMED": i18n.Messages.LookupContent.Bug.Status.Unconfirmed,
		"CONFIRMED":   i18n.Messages.LookupContent.Bug.Status.Confirmed,
		"IN_PROGRESS": i18n.Messages.LookupContent.Bug.Status.InProgress,
		"RESOLVED":    i18n.Messages.LookupContent.Bug.Status.Resolved,
		"VERIFIED":    i18n.Messages.LookupContent.Bug.Status.Verified,
	}
	bugResolutionMessages = map[string]i18n.Text{
		"FIXED":            i18n.Messages.LookupContent.Bug.Resolution.Fixed,
		"WONTFIX":          i18n.Messages.LookupContent.Bug.Resolution.WontFix,
		"CANTFIX":          i18n.Messages.LookupContent.Bug.Resolution.CantFix,
		"DUPLICATE":        i18n.Messages.LookupContent.Bug.Resolution.Duplicate,
		"INVALID":          i18n.Messages.LookupContent.Bug.Resolution.Invalid,
		"WORKSFORME":       i18n.Messages.LookupContent.Bug.Resolution.WorksForMe,
		"OBSOLETE":         i18n.Messages.LookupContent.Bug.Resolution.Obsolete,
		"UPSTREAM":         i18n.Messages.LookupContent.Bug.Resolution.Upstream,
		"PKGREMOVED":       i18n.Messages.LookupContent.Bug.Resolution.PackageRemoved,
		"NEEDINFO":         i18n.Messages.LookupContent.Bug.Resolution.NeedInfo,
		"TEST-REQUEST":     i18n.Messages.LookupContent.Bug.Resolution.TestRequest,
		"PENDING-UPSTREAM": i18n.Messages.LookupContent.Bug.Resolution.PendingUpstream,
	}
	bugSeverityMessages = map[string]i18n.Text{
		"blocker":     i18n.Messages.LookupContent.Bug.Severity.Blocker,
		"critical":    i18n.Messages.LookupContent.Bug.Severity.Critical,
		"major":       i18n.Messages.LookupContent.Bug.Severity.Major,
		"normal":      i18n.Messages.LookupContent.Bug.Severity.Normal,
		"minor":       i18n.Messages.LookupContent.Bug.Severity.Minor,
		"trivial":     i18n.Messages.LookupContent.Bug.Severity.Trivial,
		"enhancement": i18n.Messages.LookupContent.Bug.Severity.Enhancement,
	}
	bugPriorityMessages = map[string]i18n.Text{
		"Highest": i18n.Messages.LookupContent.Bug.Priority.Highest,
		"High":    i18n.Messages.LookupContent.Bug.Priority.High,
		"Normal":  i18n.Messages.LookupContent.Bug.Priority.Normal,
		"Low":     i18n.Messages.LookupContent.Bug.Priority.Low,
		"Lowest":  i18n.Messages.LookupContent.Bug.Priority.Lowest,
	}
	bugStatusZH     = localizedBugLabels(i18n.LangZH, bugStatusMessages)
	bugResolutionZH = localizedBugLabels(i18n.LangZH, bugResolutionMessages)
	bugSeverityZH   = localizedBugLabels(i18n.LangZH, bugSeverityMessages)
	bugPriorityZH   = localizedBugLabels(i18n.LangZH, bugPriorityMessages)
)

func localizedBugLabels(l i18n.Lang, messages map[string]i18n.Text) map[string]string {
	labels := make(map[string]string, len(messages))
	for code, message := range messages {
		labels[code] = message.For(l)
	}
	return labels
}

// TranslateBugValue localizes a known Bugzilla enum value for l.
func TranslateBugValue(l i18n.Lang, value string) string {
	if l == i18n.LangZH {
		for _, labels := range [...]map[string]string{bugStatusZH, bugResolutionZH, bugSeverityZH, bugPriorityZH} {
			if translated, ok := labels[value]; ok {
				return translated
			}
		}
		return value
	}
	for _, messages := range [...]map[string]i18n.Text{bugStatusMessages, bugResolutionMessages, bugSeverityMessages, bugPriorityMessages} {
		if message, ok := messages[value]; ok {
			return message.For(l)
		}
	}
	return value
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

func bugLookupFailureMessage(l i18n.Lang, id, link string, state bugLookupState) string {
	if state == bugLookupNotFound {
		return i18n.Messages.LookupContent.Bug.NotFound.Render(l, id)
	}
	return i18n.Messages.LookupContent.Bug.Unavailable.Render(l, id, link)
}

// OnBug handles Gentoo Bugzilla lookups.
func (v *Service) OnBug(ctx *th.Context, update telego.Update) error {
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
	id := commandArg(msg.Text)
	if !bugIDRe.MatchString(id) {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, i18n.Messages.LookupContent.Bug.Usage.For(l))
		return nil
	}
	link := "https://bugs.gentoo.org/" + id

	hc, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()
	info, state := fetchBug(hc, id)
	if state != bugLookupFound {
		// Keep unsuccessful lookups on the reply-linked cleanup path.
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, bugLookupFailureMessage(l, id, link, state))
		return nil
	}

	var b strings.Builder
	b.WriteString(i18n.Messages.LookupContent.Bug.Heading.Render(l, link, id, html.EscapeString(info.summary)))
	status := TranslateBugValue(l, info.status)
	if info.resolution != "" {
		status += i18n.Messages.LookupContent.Bug.Details.ResolutionSeparator.For(l) + TranslateBugValue(l, info.resolution)
	}
	b.WriteString(i18n.Messages.LookupContent.Bug.Details.Status.Render(l, html.EscapeString(status)))
	if info.severity != "" {
		b.WriteString(i18n.Messages.LookupContent.Bug.Details.Severity.Render(l, html.EscapeString(TranslateBugValue(l, info.severity))))
	}
	if info.product != "" {
		comp := info.product
		if info.component != "" {
			comp += " › " + info.component
		}
		b.WriteByte('\n')
		b.WriteString(i18n.Messages.LookupContent.Bug.Details.ProductComponent.Render(l, html.EscapeString(comp)))
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

func renderNews(l i18n.Lang, arg string, items []NewsItem, available bool) string {
	q := strings.ToLower(arg)
	var b strings.Builder
	if q == "" {
		b.WriteString(i18n.Messages.LookupContent.News.LatestHeading.For(l))
	} else {
		b.WriteString(i18n.Messages.LookupContent.News.SearchHeading.Render(l, html.EscapeString(arg)))
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
			b.WriteByte('\n')
			b.WriteString(i18n.Messages.LookupContent.News.NoMatches.For(l))
		} else {
			b.WriteByte('\n')
			b.WriteString(i18n.Messages.LookupContent.News.Unavailable.For(l))
		}
	} else if !available {
		b.WriteByte('\n')
		b.WriteString(i18n.Messages.LookupContent.News.Stale.For(l))
	}
	return b.String()
}

// OnNews handles Gentoo news lookups.
func (v *Service) OnNews(ctx *th.Context, update telego.Update) error {
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
	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	items, available := GetNews(hc)
	arg := commandArg(msg.Text)
	b := renderNews(l, arg, items, available)
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
	switch strings.ToLower(m[1]) {
	case "zh-cn", "zh-hans":
		return base, "zh"
	case "zh-tw", "zh-hant":
		return base, "zh-Hant"
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
	switch m[1] {
	case "\u7b80\u4f53\u4e2d\u6587":
		return base, "zh"
	case "\u7e41\u9ad4\u4e2d\u6587", "\u6b63\u9ad4\u4e2d\u6587":
		return base, "zh-Hant"
	default:
		return base, "other"
	}
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

// Dedupe topics case-insensitively, preferring the requester's language.
func (w wikiSource) pickWikiTitles(l i18n.Lang, titles []string, max int) []string {
	type entry struct {
		title string
		lang  string
	}
	preferred := l.String()
	rank := func(language string) int {
		if language == preferred {
			return 0
		}
		if language == "en" {
			return 1
		}
		return 2
	}
	chosen := map[string]entry{}
	var order []string
	for _, title := range titles {
		base, language := w.classify(title)
		if language == "other" || (language == "en" && hasNonASCII(base)) {
			continue
		}
		key := strings.ToLower(base)
		if current, ok := chosen[key]; ok {
			if rank(language) < rank(current.lang) {
				chosen[key] = entry{title: title, lang: language}
			}
			continue
		}
		chosen[key] = entry{title: title, lang: language}
		order = append(order, key)
	}
	var primary, fallback []string
	for _, key := range order {
		if chosen[key].lang == preferred {
			primary = append(primary, chosen[key].title)
		} else {
			fallback = append(fallback, chosen[key].title)
		}
	}
	out := append(primary, fallback...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}
func wikiResultNotice(l i18n.Lang, found bool, srcOK []bool) string {
	var missing []string
	for i, ok := range srcOK {
		if !ok {
			missing = append(missing, wikiSources[i].name+" Wiki")
		}
	}
	if len(missing) > 0 {
		sources := strings.Join(missing, i18n.Messages.LookupContent.Wiki.SourceJoin.For(l))
		return i18n.Messages.LookupContent.Wiki.SourcesUnavailable.Render(l, sources)
	}
	if !found {
		return i18n.Messages.LookupContent.Wiki.NoMatches.For(l)
	}
	return ""
}

// OnWiki handles Gentoo Wiki and ArchWiki searches.
func (v *Service) OnWiki(ctx *th.Context, update telego.Update) error {
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
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, i18n.Messages.LookupContent.Wiki.Usage.For(l))
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
			titles[i] = w.pickWikiTitles(l, raw, 4)
			dtitles[i] = displayTitles(hc, w, titles[i])
		}(i, w)
	}
	wg.Wait()

	var b strings.Builder
	b.WriteString(i18n.Messages.LookupContent.Wiki.Heading.Render(l, html.EscapeString(q)))
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
	b.WriteString(wikiResultNotice(l, found, srcOK))
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

var forumLinks = []struct {
	label i18n.Text
	site  string
}{
	{i18n.Messages.LookupContent.BBS.GentooForum, "forums.gentoo.org"},
	{i18n.Messages.LookupContent.BBS.ArchBBS, "bbs.archlinux.org"},
	{i18n.Messages.LookupContent.BBS.UbuntuForum, "ubuntuforums.org"},
	{i18n.Messages.LookupContent.BBS.DebianForum, "forums.debian.net"},
}

func ddgSiteSearch(site, query string) string {
	return "https://duckduckgo.com/?q=" + url.QueryEscape("site:"+site+" "+query)
}

// OnBbs handles Linux forum searches.
func (v *Service) OnBbs(ctx *th.Context, update telego.Update) error {
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
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, i18n.Messages.LookupContent.BBS.Usage.For(l))
		return nil
	}
	hc, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()

	var b strings.Builder
	b.WriteString(i18n.Messages.LookupContent.BBS.Heading.Render(l, html.EscapeString(q)))
	hits, archcnOK := searchArchcn(hc, q, 5)
	switch {
	case len(hits) > 0:
		b.WriteString(i18n.Messages.LookupContent.BBS.ArchCNHeading.For(l))
		for _, h := range hits {
			fmt.Fprintf(&b, "\n • <a href=\"%s\">%s</a>", html.EscapeString(h.url), html.EscapeString(h.title))
		}
	case !archcnOK: // the fetch failed — honest transient message, not a false "no results"
		b.WriteString(i18n.Messages.LookupContent.BBS.ArchCNUnavailable.For(l))
	default:
		b.WriteString(i18n.Messages.LookupContent.BBS.ArchCNNoMatches.For(l))
	}
	b.WriteString(i18n.Messages.LookupContent.BBS.OtherForums.For(l))

	// Telegram rejects the whole reply when a button URL exceeds its limit.
	qBtn := q
	if r := []rune(qBtn); len(r) > 200 {
		qBtn = string(r[:200])
	}
	var rows [][]telego.InlineKeyboardButton
	for i := 0; i < len(forumLinks); i += 2 {
		var row []telego.InlineKeyboardButton
		for j := i; j < i+2 && j < len(forumLinks); j++ {
			row = append(row, telego.InlineKeyboardButton{Text: forumLinks[j].label.For(l), URL: ddgSiteSearch(forumLinks[j].site, qBtn)})
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
