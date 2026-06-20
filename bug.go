package main

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

var bugIDRe = regexp.MustCompile(`^[0-9]{1,9}$`)

type bugInfo struct {
	summary, status, resolution, product, component, severity string
}

// Bugzilla enum-value translations for Chinese output. The labels are already
// localized; these turn the finite status / resolution / severity / priority *values*
// into Chinese too. Component names, keywords and people stay as-is (official identifiers).
var (
	bugStatusZH = map[string]string{
		"UNCONFIRMED": "未确认", "CONFIRMED": "已确认", "IN_PROGRESS": "处理中",
		"RESOLVED": "已解决", "VERIFIED": "已验证",
	}
	bugResolutionZH = map[string]string{
		"FIXED": "已修复", "WONTFIX": "不予修复", "CANTFIX": "无法修复", "DUPLICATE": "重复",
		"INVALID": "无效", "WORKSFORME": "无法复现", "OBSOLETE": "已过时", "UPSTREAM": "上游",
		"NEEDINFO": "需补充信息", "TEST-REQUEST": "待测试", "PENDING-UPSTREAM": "待上游",
	}
	bugSeverityZH = map[string]string{
		"blocker": "阻断", "critical": "严重", "major": "重大", "normal": "普通",
		"minor": "次要", "trivial": "轻微", "enhancement": "增强",
	}
	bugPriorityZH = map[string]string{
		"Highest": "最高", "High": "高", "Normal": "普通", "Low": "低", "Lowest": "最低",
	}
)

// zhVal returns v translated via m when zh is true and a translation exists; else v.
func zhVal(m map[string]string, v string, zh bool) string {
	if zh {
		if t, ok := m[v]; ok {
			return t
		}
	}
	return v
}

// fetchBug queries the public Gentoo Bugzilla REST API. ok=false for missing,
// restricted (both return 404), or any error — callers fall back to a bare link.
func fetchBug(ctx context.Context, id string) (bugInfo, bool) {
	u := "https://bugs.gentoo.org/rest/bug/" + id +
		"?include_fields=summary,status,resolution,product,component,severity"
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
	if err := httpGetJSON(ctx, u, nil, &br); err != nil || br.Error || len(br.Bugs) == 0 {
		return bugInfo{}, false
	}
	b := br.Bugs[0]
	return bugInfo{b.Summary, b.Status, b.Resolution, b.Product, b.Component, b.Severity}, true
}

// onBug handles /bug <id> — Gentoo Bugzilla quick lookup.
func (v *Verifier) onBug(ctx *th.Context, update telego.Update) error {
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
	info, ok := fetchBug(hc, id)
	if !ok {
		// Route through replyLookupPlain like every other lookup's not-found path: reply-linked
		// + auto-deleted with the command, instead of lingering in the group forever.
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID,
			fmt.Sprintf("❓ 取不到 Bug %s 的详情(可能不存在或非公开)。直接看:%s", id, link))
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🐞 <a href=\"%s\">Bug %s</a>\n%s\n", link, id, html.EscapeString(info.summary))
	status := zhVal(bugStatusZH, info.status, true)
	if info.resolution != "" {
		status += " / " + zhVal(bugResolutionZH, info.resolution, true)
	}
	fmt.Fprintf(&b, "状态:%s", html.EscapeString(status))
	if info.severity != "" {
		fmt.Fprintf(&b, " · 严重性:%s", html.EscapeString(zhVal(bugSeverityZH, info.severity, true)))
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
