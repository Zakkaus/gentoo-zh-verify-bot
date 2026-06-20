package main

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

type newsItem struct {
	date, title, url string
}

const newsTTL = 30 * time.Minute

// newsURL / newsBase are configurable (default: gentoo.org).
var newsURL = "https://www.gentoo.org/support/news-items/"
var newsBase = "https://www.gentoo.org"

func configureNews(cfg *Config) {
	if cfg.NewsURL != "" {
		newsURL = cfg.NewsURL
	}
	if u, err := url.Parse(newsURL); err == nil && u.Scheme != "" && u.Host != "" {
		newsBase = u.Scheme + "://" + u.Host
	}
}

// matches <a href="/support/news-items/YYYY-MM-DD-slug.html">Title</a>
var newsRe = regexp.MustCompile(`href="(/support/news-items/(\d{4}-\d{2}-\d{2})-[^"]+\.html)"[^>]*>([^<]+)<`)

var newsC = struct {
	mu      sync.Mutex
	items   []newsItem
	fetched time.Time
	loading bool
}{}

func fetchNews(c context.Context) ([]newsItem, error) {
	body, err := httpGetBody(c, newsURL, 2<<20)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var items []newsItem
	for _, m := range newsRe.FindAllStringSubmatch(string(body), -1) {
		path, date, title := m[1], m[2], strings.TrimSpace(m[3])
		if seen[path] || title == "" {
			continue
		}
		seen[path] = true
		items = append(items, newsItem{date: date, title: title, url: newsBase + path})
	}
	return items, nil
}

func getNews(c context.Context) []newsItem {
	newsC.mu.Lock()
	fresh := len(newsC.items) > 0 && time.Since(newsC.fetched) < newsTTL
	if fresh || newsC.loading {
		items := newsC.items
		newsC.mu.Unlock()
		return items
	}
	newsC.loading = true
	newsC.mu.Unlock()
	defer func() { newsC.mu.Lock(); newsC.loading = false; newsC.mu.Unlock() }()

	items, err := fetchNews(c)
	if err != nil {
		log.Printf("news fetch: %v", err)
		newsC.mu.Lock()
		old := newsC.items
		newsC.mu.Unlock()
		return old
	}
	newsC.mu.Lock()
	newsC.items, newsC.fetched = items, time.Now()
	newsC.mu.Unlock()
	return items
}

// onNews handles /news [keyword] — list recent Gentoo news, or filter by keyword.
func (v *Verifier) onNews(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	hc, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	items := getNews(hc)

	arg := commandArg(msg.Text)
	q := strings.ToLower(arg)
	var b strings.Builder
	if q == "" {
		b.WriteString("📰 Gentoo 最新新闻:")
	} else {
		fmt.Fprintf(&b, "📰 Gentoo 新闻搜索「%s」:", html.EscapeString(arg))
	}
	n := 0
	for _, it := range items {
		if q != "" && !strings.Contains(strings.ToLower(it.title), q) && !strings.Contains(strings.ToLower(it.url), q) {
			continue
		}
		title := html.EscapeString(html.UnescapeString(it.title))
		fmt.Fprintf(&b, "\n • <a href=\"%s\">%s — %s</a>", html.EscapeString(it.url), it.date, title)
		n++
		if n >= 8 {
			break
		}
	}
	if n == 0 {
		if len(items) == 0 {
			b.WriteString("\n(暂时取不到新闻列表,稍后再试)")
		} else {
			b.WriteString("\n没找到匹配的新闻。")
		}
	}
	_, _ = bot.SendMessage(c, htmlMessage(msg.Chat.ID, b.String()).WithReplyParameters(replyParams(msg.MessageID)))
	return nil
}
