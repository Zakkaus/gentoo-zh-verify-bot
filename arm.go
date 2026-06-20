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

// armStatus scans a Gentoo package's versions for arm64 (aarch64) keyword status and
// returns the newest version that is stable on arm64 and the newest that is merely
// ~arm64 (testing). Both empty with ok=true means the package exists but isn't keyworded
// on arm64 at all; ok=false means the lookup itself failed (network/JSON), which the
// caller must NOT report as "unsupported". Live ebuilds (9999) are skipped.
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

// arm64Keywords scans versions (newest-first, as packages.gentoo.org returns them) for the
// newest arm64-stable and newest ~arm64 (testing) version, skipping 9999 live ebuilds.
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

// onArm handles /arm <pkg> — a quick look at a Gentoo package's arm64 (aarch64) keyword
// status, so ARM users (Raspberry Pi, ARM servers) can tell if it's stable, testing, or
// not packaged for their arch.
func (v *Verifier) onArm(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	name := commandArg(msg.Text)
	if name == "" {
		v.notify(c, bot, msg.Chat.ID, "用法:/arm <包名>,例如 /arm firefox。查该包在 arm64 (aarch64) 上的 Gentoo keyword 状态。")
		return nil
	}
	hc, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()
	atoms := searchMainTree(hc, name)
	if len(atoms) == 0 {
		v.notify(c, bot, msg.Chat.ID, fmt.Sprintf("❓ Gentoo 官方树里没找到「%s」。", name))
		return nil
	}
	atom := atoms[0]
	stable, testing, ok := armStatus(hc, atom)
	url := "https://packages.gentoo.org/packages/" + atom
	esc := html.EscapeString

	var b strings.Builder
	fmt.Fprintf(&b, "🦾 <a href=\"%s\">%s</a> 在 arm64 (aarch64) 上:", esc(url), esc(atom))
	switch {
	case !ok:
		fmt.Fprintf(&b, "\n⚠️ 暂时取不到 keyword 信息,稍后再试或直接看上面的链接。")
	case stable != "" && testing != "" && stable != testing:
		fmt.Fprintf(&b, "\n✅ 稳定(arm64):%s\n🧪 测试(~arm64):%s", esc(stable), esc(testing))
	case stable != "":
		fmt.Fprintf(&b, "\n✅ 稳定(arm64):%s", esc(stable))
	case testing != "":
		fmt.Fprintf(&b, "\n🧪 仅测试(~arm64):%s(尚无 arm64 稳定版,需 accept ~arm64)", esc(testing))
	default:
		b.WriteString("\n❌ 未在 arm64 上 keyword —— Gentoo 官方树未对该包标注 aarch64(可能尚不支持/未测试)。")
	}
	sent, _ := bot.SendMessage(c, htmlMessage(msg.Chat.ID, b.String()).WithReplyParameters(replyParams(msg.MessageID)))
	v.scheduleLookupCleanup(bot, msg.Chat.ID, msg.MessageID, msgID(sent))
	return nil
}
