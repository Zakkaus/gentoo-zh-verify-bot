package main

import (
	"context"
	"fmt"
	"html"
	"net/http"
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

func zhVal(m map[string]string, v string, zh bool) string {
	if zh {
		if t, ok := m[v]; ok {
			return t
		}
	}
	return v
}

// Only an HTTP 404 is authoritative; malformed, restricted, and failed responses are retryable.
func fetchBug(ctx context.Context, id string) (bugInfo, bugLookupState) {
	u := "https://bugs.gentoo.org/rest/bug/" + id +
		"?include_fields=summary,status,resolution,product,component,severity"
	var br bugResponse
	if err := httpGetJSON(ctx, u, nil, &br); err != nil {
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
	info, state := fetchBug(hc, id)
	if state != bugLookupFound {
		// Keep unsuccessful lookups on the reply-linked cleanup path.
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, bugLookupFailureMessage(id, link, state))
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
