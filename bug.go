package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

var bugIDRe = regexp.MustCompile(`^[0-9]{1,9}$`)

type bugInfo struct {
	summary, status, resolution, product, component, severity string
}

// fetchBug queries the public Gentoo Bugzilla REST API. ok=false for missing,
// restricted (both return 404), or any error — callers fall back to a bare link.
func fetchBug(ctx context.Context, id string) (bugInfo, bool) {
	u := "https://bugs.gentoo.org/rest/bug/" + id +
		"?include_fields=summary,status,resolution,product,component,severity"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return bugInfo{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return bugInfo{}, false
	}
	var br struct {
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
	if json.NewDecoder(resp.Body).Decode(&br) != nil || br.Error || len(br.Bugs) == 0 {
		return bugInfo{}, false
	}
	b := br.Bugs[0]
	return bugInfo{b.Summary, b.Status, b.Resolution, b.Product, b.Component, b.Severity}, true
}

// onBug handles /bug <id> — Gentoo Bugzilla quick lookup.
func (v *Verifier) onBug(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	id := commandArg(msg.Text)
	if !bugIDRe.MatchString(id) {
		v.notify(c, bot, msg.Chat.ID, "用法:/bug <编号>,例如 /bug 900000")
		return nil
	}
	link := "https://bugs.gentoo.org/" + id

	hc, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	info, ok := fetchBug(hc, id)
	if !ok {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(msg.Chat.ID),
			fmt.Sprintf("❓ 取不到 Bug %s 的详情(可能不存在或非公开)。直接看:%s", id, link)).
			WithLinkPreviewOptions(&telego.LinkPreviewOptions{IsDisabled: true}))
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🐞 <a href=\"%s\">Bug %s</a>\n%s\n", link, id, html.EscapeString(info.summary))
	status := info.status
	if info.resolution != "" {
		status += " / " + info.resolution
	}
	fmt.Fprintf(&b, "状态:%s", html.EscapeString(status))
	if info.severity != "" {
		fmt.Fprintf(&b, " · 严重性:%s", html.EscapeString(info.severity))
	}
	if info.product != "" {
		comp := info.product
		if info.component != "" {
			comp += " › " + info.component
		}
		fmt.Fprintf(&b, "\n产品:%s", html.EscapeString(comp))
	}
	_, _ = bot.SendMessage(c, tu.Message(tu.ID(msg.Chat.ID), b.String()).
		WithParseMode(telego.ModeHTML).
		WithLinkPreviewOptions(&telego.LinkPreviewOptions{IsDisabled: true}))
	return nil
}
