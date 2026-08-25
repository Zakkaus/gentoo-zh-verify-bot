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

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

type newsItem struct {
	date, title, url string
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
	items   []newsItem
	fetched time.Time
	loading bool
}{}

func fetchNews(c context.Context) ([]newsItem, error) {
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

func parseNews(body []byte) []newsItem {
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
	return items
}

func getNews(c context.Context) ([]newsItem, bool) {
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

	items, err := fetchNews(c)
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

func renderNews(arg string, items []newsItem, available bool) string {
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

func (v *Verifier) onNews(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.queryAllowed(ctx, msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	items, available := getNews(hc)
	arg := commandArg(msg.Text)
	b := renderNews(arg, items, available)
	v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, b)
	return nil
}
