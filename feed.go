package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// feedState is the on-disk dedup cursor so a restart doesn't re-post or miss items.
type feedState struct {
	LastBugID   int    `json:"last_bug_id"`
	LastNewsURL string `json:"last_news_url"`
}

type recentBug struct {
	ID        int    `json:"id"`
	Summary   string `json:"summary"`
	Status    string `json:"status"`
	Product   string `json:"product"`
	Component string `json:"component"`
}

func fetchRecentBugs(ctx context.Context) []recentBug {
	u := "https://bugs.gentoo.org/rest/bug?order=bug_id%20DESC&limit=30" +
		"&include_fields=id,summary,status,product,component"
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

func pollFeed(ctx context.Context, bot *telego.Bot, chatID int64, statePath string, st *feedState) {
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	bugs := fetchRecentBugs(fctx)
	news, _ := fetchNews(fctx)
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
				b := nb[i]
				where := html.EscapeString(b.Product)
				if b.Component != "" {
					where += " › " + html.EscapeString(b.Component)
				}
				text := fmt.Sprintf("🐞 <a href=\"https://bugs.gentoo.org/%d\">Bug %d</a>\n%s\n%s · %s",
					b.ID, b.ID, html.EscapeString(b.Summary), where, html.EscapeString(b.Status))
				postFeed(ctx, bot, chatID, text, true) // bugs are frequent -> silent
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
				n := nn[i]
				title := html.EscapeString(html.UnescapeString(n.title))
				text := fmt.Sprintf("📰 <a href=\"%s\">%s — %s</a>", html.EscapeString(n.url), n.date, title)
				postFeed(ctx, bot, chatID, text, false) // news is rare/important -> notify
			}
		}
		st.LastNewsURL = news[0].url
	}
	saveFeedState(statePath, *st)
}

// runFeed polls Gentoo Bugzilla + news on an interval and posts NEW items to chatID.
// The first poll only records a baseline (no backlog flood); later polls post new items.
func runFeed(ctx context.Context, bot *telego.Bot, chatID int64, interval time.Duration, statePath string) {
	st := loadFeedState(statePath)
	log.Printf("feed: posting new Gentoo bugs + news to %d every %s", chatID, interval)
	pollFeed(ctx, bot, chatID, statePath, &st)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pollFeed(ctx, bot, chatID, statePath, &st)
		}
	}
}
