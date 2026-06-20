package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
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

func fetchRecentBugs(ctx context.Context) []recentBug {
	u := "https://bugs.gentoo.org/rest/bug?order=bug_id%20DESC&limit=30" +
		"&include_fields=id,summary,status,resolution,product,component,priority,severity," +
		"keywords,creation_time,cf_stabilisation_atoms,assigned_to,assigned_to_detail,creator,creator_detail"
	var br struct {
		Bugs []recentBug `json:"bugs"`
	}
	if err := httpGetJSON(ctx, u, nil, &br); err != nil {
		log.Printf("feed: bugs fetch: %v", err)
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

// postFeed sends one feed item. It returns false on a send failure so the caller
// won't advance the dedup cursor past an item that was never delivered (e.g. a
// transient error, a Telegram rate-limit, or the context being cancelled at shutdown).
func postFeed(ctx context.Context, bot *telego.Bot, chatID int64, text string, silent bool) bool {
	m := htmlMessage(chatID, text)
	if silent {
		m = m.WithDisableNotification()
	}
	if _, err := bot.SendMessage(ctx, m); err != nil {
		log.Printf("feed: post to %d: %v", chatID, err)
		return false
	}
	time.Sleep(time.Second) // gentle pacing for catch-up bursts
	return true
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

// formatBug renders a Bugzilla bug for the feed. lang "en" uses English field labels,
// otherwise (default) Chinese. Optional fields appear only when present, so simple bugs
// stay short and keywording bugs stay rich.
func formatBug(b recentBug, lang string) string {
	en := strings.EqualFold(lang, "en")
	sep := "："
	if en {
		sep = ": "
	}
	pick := func(zh, eng string) string {
		if en {
			return eng
		}
		return zh
	}
	esc := html.EscapeString
	var sb strings.Builder
	fmt.Fprintf(&sb, "🐞 <a href=\"https://bugs.gentoo.org/%d\"><b>Bug %d</b></a>\n%s", b.ID, b.ID, esc(b.Summary))
	line := func(label, val string) {
		if val != "" {
			fmt.Fprintf(&sb, "\n<b>%s</b>%s%s", label, sep, esc(val))
		}
	}

	zh := !en // zh feed: translate the enum values too (en feed keeps them English)
	status := zhVal(bugStatusZH, b.Status, zh)
	if b.Resolution != "" {
		status += " / " + zhVal(bugResolutionZH, b.Resolution, zh)
	}
	line(pick("状态", "Status"), status)

	comp := b.Product
	if b.Component != "" {
		comp += " › " + b.Component
	}
	line(pick("组件", "Component"), comp)
	line(pick("重要度", "Importance"), strings.Trim(zhVal(bugPriorityZH, b.Priority, zh)+" · "+zhVal(bugSeverityZH, b.Severity, zh), " ·"))
	if len(b.Keywords) > 0 {
		line(pick("关键词", "Keywords"), strings.Join(b.Keywords, ", "))
	}
	if atoms := flattenAtoms(b.Atoms); atoms != "" {
		line(pick("包", "Packages"), atoms)
	}

	var who []string
	if a := b.AssignedTo.display(); a != "" {
		who = append(who, pick("负责 ", "Assigned ")+a)
	}
	if c := b.Creator.display(); c != "" {
		who = append(who, pick("报告 ", "Reporter ")+c)
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

// matchesBug reports whether a bug passes this feed's optional product/component filter.
func (f *FeedConfig) matchesBug(b recentBug) bool {
	if f.BugProduct != "" && !strings.EqualFold(b.Product, f.BugProduct) {
		return false
	}
	if f.BugComponent != "" && !strings.EqualFold(b.Component, f.BugComponent) {
		return false
	}
	return true
}

func feedStatePath(dir string, chatID int64) string {
	if dir == "" {
		return ""
	}
	return dir + "/feed-" + strconv.FormatInt(chatID, 10) + ".json"
}

// postFeedItems posts the bugs/news that are new to this feed (filtered, localized, deduped).
func postFeedItems(ctx context.Context, bot *telego.Bot, f *FeedConfig, st *feedState, bugs []recentBug, news []newsItem) {
	if f.bugsOn() && len(bugs) > 0 {
		if st.LastBugID == 0 {
			st.LastBugID = bugs[0].ID // first run: record a baseline, don't backfill history
		} else {
			var nb []recentBug
			for _, b := range bugs {
				if b.ID > st.LastBugID && f.matchesBug(b) {
					nb = append(nb, b)
				}
			}
			delivered := true
			for i := len(nb) - 1; i >= 0; i-- { // oldest first
				if !postFeed(ctx, bot, f.ChatID, formatBug(nb[i], f.Lang), f.silentBugs()) {
					delivered = false // leave the cursor so the next cycle retries this item
					break
				}
				st.LastBugID = nb[i].ID
			}
			if delivered {
				st.LastBugID = bugs[0].ID // all sent -> advance past newest seen (incl. filtered-out)
			}
		}
	}
	if f.newsOn() && len(news) > 0 {
		if st.LastNewsURL == "" {
			st.LastNewsURL = news[0].url // first run: baseline only
		} else {
			var nn []newsItem
			for _, n := range news {
				if n.url == st.LastNewsURL {
					break
				}
				nn = append(nn, n)
			}
			delivered := true
			for i := len(nn) - 1; i >= 0; i-- { // oldest first
				if !postFeed(ctx, bot, f.ChatID, formatNews(nn[i]), false) {
					delivered = false
					break
				}
				st.LastNewsURL = nn[i].url
			}
			if delivered {
				st.LastNewsURL = news[0].url
			}
		}
	}
}

// pollAll fetches Gentoo bugs + news ONCE and fans them out to every feed, so the number of
// requests to the public Gentoo servers stays at 2 per cycle no matter how many feeds exist.
func pollAll(ctx context.Context, bot *telego.Bot, feeds []*FeedConfig, states map[int64]*feedState, stateDir string) {
	needBugs, needNews := false, false
	for _, f := range feeds {
		needBugs = needBugs || f.bugsOn()
		needNews = needNews || f.newsOn()
	}
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	var bugs []recentBug
	var news []newsItem
	if needBugs {
		bugs = fetchRecentBugs(fctx)
	}
	if needNews {
		news, _ = fetchNews(fctx)
	}
	cancel()

	for _, f := range feeds {
		st := states[f.ChatID]
		postFeedItems(ctx, bot, f, st, bugs, news)
		saveFeedState(feedStatePath(stateDir, f.ChatID), *st)
	}
}

// runFeeds polls Gentoo Bugzilla + news once per cycle (shared fetch) and posts new items to
// every configured feed — each with its own language, filters and dedup cursor. The first poll
// only records a baseline per feed (no backlog flood).
func runFeeds(ctx context.Context, bot *telego.Bot, feeds []*FeedConfig, stateDir string) {
	interval := feeds[0].interval()
	for _, f := range feeds {
		if d := f.interval(); d < interval {
			interval = d
		}
	}
	states := map[int64]*feedState{}
	for _, f := range feeds {
		st := loadFeedState(feedStatePath(stateDir, f.ChatID))
		states[f.ChatID] = &st
	}
	log.Printf("feed: %d destination(s), polling Gentoo bugs + news every %s (shared fetch)", len(feeds), interval)
	pollAll(ctx, bot, feeds, states, stateDir)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pollAll(ctx, bot, feeds, states, stateDir)
		}
	}
}
