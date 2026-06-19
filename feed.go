package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// feedState is the on-disk dedup cursor so a restart doesn't re-post or miss items.
type feedState struct {
	LastBugID   int    `json:"last_bug_id"`
	LastNewsURL string `json:"last_news_url"`
}

type bugUser struct {
	RealName string `json:"real_name"`
	Name     string `json:"name"`
}

func (u bugUser) display() string {
	if u.RealName != "" {
		return u.RealName
	}
	return u.Name
}

type recentBug struct {
	ID           int      `json:"id"`
	Summary      string   `json:"summary"`
	Status       string   `json:"status"`
	Resolution   string   `json:"resolution"`
	Product      string   `json:"product"`
	Component    string   `json:"component"`
	Priority     string   `json:"priority"`
	Severity     string   `json:"severity"`
	Keywords     []string `json:"keywords"`
	CreationTime string   `json:"creation_time"`
	Atoms        string   `json:"cf_stabilisation_atoms"` // Gentoo keywording/stabilization package list
	AssignedTo   bugUser  `json:"assigned_to_detail"`
	Creator      bugUser  `json:"creator_detail"`
}

func fetchRecentBugs(ctx context.Context, fc *FeedConfig) []recentBug {
	u := "https://bugs.gentoo.org/rest/bug?order=bug_id%20DESC&limit=30" +
		"&include_fields=id,summary,status,resolution,product,component,priority,severity," +
		"keywords,creation_time,cf_stabilisation_atoms,assigned_to_detail,creator_detail"
	if fc.BugProduct != "" {
		u += "&product=" + url.QueryEscape(fc.BugProduct)
	}
	if fc.BugComponent != "" {
		u += "&component=" + url.QueryEscape(fc.BugComponent)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("feed: bugs fetch: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var br struct {
		Bugs []recentBug `json:"bugs"`
	}
	if json.NewDecoder(resp.Body).Decode(&br) != nil {
		return nil
	}
	return br.Bugs // newest first (order=bug_id DESC)
}

func loadFeedState(path string) feedState {
	var st feedState
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &st)
		}
	}
	return st
}

func saveFeedState(path string, st feedState) {
	if path == "" {
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func postFeed(ctx context.Context, bot *telego.Bot, chatID int64, text string, silent bool) {
	m := tu.Message(tu.ID(chatID), text).
		WithParseMode(telego.ModeHTML).
		WithLinkPreviewOptions(&telego.LinkPreviewOptions{IsDisabled: true})
	if silent {
		m = m.WithDisableNotification()
	}
	if _, err := bot.SendMessage(ctx, m); err != nil {
		log.Printf("feed: post to %d: %v", chatID, err)
	}
	time.Sleep(time.Second) // gentle pacing for catch-up bursts
}

// dateOnly turns "2026-02-26T04:42:47Z" into "2026-02-26".
func dateOnly(t string) string {
	if len(t) >= 10 {
		return t[:10]
	}
	return t
}

// flattenAtoms collapses the multi-line cf_stabilisation_atoms field into one capped line.
func flattenAtoms(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	out := strings.Join(parts, "; ")
	if len(out) > 300 {
		out = out[:297] + "…"
	}
	return out
}

// formatBug renders a Bugzilla bug for the feed. Optional fields (keywords, packages,
// assignee) only appear when present, so simple bugs stay short and keywording bugs stay rich.
func formatBug(b recentBug) string {
	esc := html.EscapeString
	var sb strings.Builder
	fmt.Fprintf(&sb, "🐞 <a href=\"https://bugs.gentoo.org/%d\"><b>Bug %d</b></a>\n%s", b.ID, b.ID, esc(b.Summary))

	status := b.Status
	if b.Resolution != "" {
		status += " / " + b.Resolution
	}
	fmt.Fprintf(&sb, "\n<b>状态</b>:%s", esc(status))

	comp := esc(b.Product)
	if b.Component != "" {
		comp += " › " + esc(b.Component)
	}
	fmt.Fprintf(&sb, "\n<b>组件</b>:%s", comp)

	if imp := strings.Trim(b.Priority+" · "+b.Severity, " ·"); imp != "" {
		fmt.Fprintf(&sb, "\n<b>重要度</b>:%s", esc(imp))
	}
	if len(b.Keywords) > 0 {
		fmt.Fprintf(&sb, "\n<b>关键词</b>:%s", esc(strings.Join(b.Keywords, ", ")))
	}
	if atoms := flattenAtoms(b.Atoms); atoms != "" {
		fmt.Fprintf(&sb, "\n<b>包</b>:%s", esc(atoms))
	}

	var who []string
	if a := b.AssignedTo.display(); a != "" {
		who = append(who, "负责 "+a)
	}
	if c := b.Creator.display(); c != "" {
		who = append(who, "报告 "+c)
	}
	if d := dateOnly(b.CreationTime); d != "" {
		who = append(who, d)
	}
	if len(who) > 0 {
		fmt.Fprintf(&sb, "\n%s", esc(strings.Join(who, " · ")))
	}
	return sb.String()
}

func formatNews(n newsItem) string {
	return fmt.Sprintf("📰 <a href=\"%s\">%s — %s</a>",
		html.EscapeString(n.url), n.date, html.EscapeString(html.UnescapeString(n.title)))
}

func pollFeed(ctx context.Context, bot *telego.Bot, fc *FeedConfig, statePath string, st *feedState) {
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	var bugs []recentBug
	var news []newsItem
	if fc.bugsOn() {
		bugs = fetchRecentBugs(fctx, fc)
	}
	if fc.newsOn() {
		news, _ = fetchNews(fctx)
	}
	cancel()

	if len(bugs) > 0 {
		if st.LastBugID != 0 { // not first run -> post new bugs (oldest first)
			var nb []recentBug
			for _, b := range bugs {
				if b.ID > st.LastBugID {
					nb = append(nb, b)
				}
			}
			for i := len(nb) - 1; i >= 0; i-- {
				postFeed(ctx, bot, fc.ChatID, formatBug(nb[i]), fc.silentBugs())
			}
		}
		st.LastBugID = bugs[0].ID
	}

	if len(news) > 0 {
		if st.LastNewsURL != "" { // not first run -> post new news (oldest first)
			var nn []newsItem
			for _, n := range news {
				if n.url == st.LastNewsURL {
					break
				}
				nn = append(nn, n)
			}
			for i := len(nn) - 1; i >= 0; i-- {
				postFeed(ctx, bot, fc.ChatID, formatNews(nn[i]), false) // news -> notify
			}
		}
		st.LastNewsURL = news[0].url
	}
	saveFeedState(statePath, *st)
}

// runFeed polls Gentoo Bugzilla + news on an interval and posts NEW items to chatID.
// The first poll only records a baseline (no backlog flood); later polls post new items.
func runFeed(ctx context.Context, bot *telego.Bot, fc *FeedConfig, statePath string) {
	st := loadFeedState(statePath)
	interval := fc.interval()
	log.Printf("feed: posting new Gentoo bugs + news to %d every %s", fc.ChatID, interval)
	pollFeed(ctx, bot, fc, statePath, &st)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pollFeed(ctx, bot, fc, statePath, &st)
		}
	}
}
