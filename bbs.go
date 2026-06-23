package main

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

const archcnForum = "https://forum.archlinuxcn.org"

type forumTopic struct{ title, url string }

// searchArchcn searches the Arch Linux CN Discourse forum (clean JSON API) and returns the top
// matching topics, plus ok=false if the FETCH failed (transient — distinct from a successful search
// with no matches) so the caller doesn't report a transient blip as a definitive "no results".
func searchArchcn(ctx context.Context, query string, limit int) (topics []forumTopic, ok bool) {
	u := archcnForum + "/search.json?q=" + url.QueryEscape(query)
	var resp struct {
		Topics []struct {
			ID    int    `json:"id"`
			Slug  string `json:"slug"`
			Title string `json:"title"`
		} `json:"topics"`
	}
	if err := httpGetJSON(ctx, u, nil, &resp); err != nil {
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

// forumLinks are the major English Linux forums offered as one-tap search buttons (a
// site-scoped DuckDuckGo search, which needs no forum API) — the English backup to the
// inline Chinese results above.
var forumLinks = []struct{ name, site string }{
	{"Gentoo 论坛", "forums.gentoo.org"},
	{"Arch BBS", "bbs.archlinux.org"},
	{"Ubuntu 论坛", "ubuntuforums.org"},
	{"Debian 论坛", "forums.debian.net"},
}

func ddgSiteSearch(site, query string) string {
	return "https://duckduckgo.com/?q=" + url.QueryEscape("site:"+site+" "+query)
}

// onBbs handles /bbs <query> — inline results from the Arch Linux CN forum (Chinese first),
// plus search-link buttons to the major English forums.
func (v *Verifier) onBbs(ctx *th.Context, update telego.Update) error {
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
		b.WriteString("\n\nArch Linux CN 论坛暂时取不到结果(稍后再试)。")
	default:
		b.WriteString("\n\nArch Linux CN 论坛暂无匹配结果。")
	}
	b.WriteString("\n\n其它论坛(点按钮搜索):")

	// Cap the query used in the button URLs: a pathologically long /bbs query would make a DuckDuckGo
	// button URL exceed Telegram's limit, which rejects the WHOLE reply — taking the Arch CN hits we
	// already fetched down with it. The buttons are a convenience; the inline results matter more.
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
	sent, err := bot.SendMessage(c, htmlMessage(msg.Chat.ID, b.String()).
		WithReplyMarkup(tu.InlineKeyboard(rows...)).
		WithReplyParameters(replyParams(msg.MessageID)))
	if err != nil {
		// The buttons sank the send (e.g. a still-too-long URL) — fall back to the inline results
		// alone so the user at least gets the Arch CN hits.
		log.Printf("/bbs send with buttons failed (%v) — retrying text-only", err)
		sent, _ = bot.SendMessage(c, htmlMessage(msg.Chat.ID, b.String()).WithReplyParameters(replyParams(msg.MessageID)))
	}
	v.scheduleLookupCleanup(bot, msg.Chat.ID, msg.MessageID, msgID(sent))
	return nil
}
