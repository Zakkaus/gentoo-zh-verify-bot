package main

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// ok distinguishes lookup failure from a package with no arm64 keyword.
func armStatus(ctx context.Context, atom string) (stable, testing string, ok bool) {
	var pj struct {
		Versions []pkgVersionJSON `json:"versions"`
	}
	if err := httpGetJSON(ctx, "https://packages.gentoo.org/packages/"+atom+".json", nil, &pj); err != nil {
		return "", "", false
	}
	stable, testing = arm64Keywords(pj.Versions)
	return stable, testing, true
}

// packages.gentoo.org returns newest first; live ebuilds are skipped.
func arm64Keywords(versions []pkgVersionJSON) (stable, testing string) {
	for _, vv := range versions {
		if strings.HasPrefix(vv.Version, "9999") {
			continue
		}
		for _, kw := range vv.Keywords {
			switch kw {
			case "arm64":
				if stable == "" {
					stable = vv.Version
				}
			case "~arm64":
				if testing == "" {
					testing = vv.Version
				}
			}
		}
	}
	return stable, testing
}

// Failed searches remain distinct from authoritative package misses.
func lookupArm(
	ctx context.Context,
	name string,
	search func(context.Context, string) ([]string, bool),
	status func(context.Context, string) (string, string, bool),
) (body string, useHTML bool) {
	atoms, available := search(ctx, name)
	if !available {
		return "暂时无法查询 Gentoo 官方树，请稍后重试。", false
	}
	if len(atoms) == 0 {
		return fmt.Sprintf("❓ Gentoo 官方树里没找到「%s」。", name), false
	}
	atom := atoms[0]
	stable, testing, ok := status(ctx, atom)
	url := "https://packages.gentoo.org/packages/" + atom
	esc := html.EscapeString

	var b strings.Builder
	fmt.Fprintf(&b, "🦾 <a href=\"%s\">%s</a> 在 arm64 (aarch64) 上:", esc(url), esc(atom))
	switch {
	case !ok:
		b.WriteString("\n⚠️ 暂时无法获取 keyword 信息,请稍后重试,或直接查看上面的链接。")
	case stable != "" && testing != "" && stable != testing:
		fmt.Fprintf(&b, "\n✅ 稳定(arm64):%s\n🧪 测试(~arm64):%s", esc(stable), esc(testing))
	case stable != "":
		fmt.Fprintf(&b, "\n✅ 稳定(arm64):%s", esc(stable))
	case testing != "":
		fmt.Fprintf(&b, "\n🧪 仅测试(~arm64):%s(尚无 arm64 稳定版,需在 package.accept_keywords 中接受 ~arm64)", esc(testing))
	default:
		b.WriteString("\n❌ 未设置 arm64 keyword —— Gentoo 官方树未给该包标记 arm64(可能尚不支持,或未经测试)。")
	}
	return b.String(), true
}

func (v *Verifier) onArm(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.queryAllowed(ctx, msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	name := commandArg(msg.Text)
	if name == "" {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, "用法:/arm <包名>,例如 /arm firefox。查该包在 arm64 (aarch64) 上的 Gentoo keyword 状态。")
		return nil
	}
	hc, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()
	body, useHTML := lookupArm(hc, name, searchMainTree, armStatus)
	if useHTML {
		v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, body)
	} else {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, body)
	}
	return nil
}
