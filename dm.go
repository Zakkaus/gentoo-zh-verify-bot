package main

import (
	"context"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// dmReplyCooldown throttles the DM auto-reply per user, so a message flood in the bot's DM
// can't amplify 1:1 into a SendMessage flood (which could trip Telegram's per-chat limits).
const dmReplyCooldown = 30 * time.Second

// dmMapMax bounds the per-user cooldown map (one entry per distinct DM'er); cleared
// wholesale past the cap so it can't grow without limit.
const dmMapMax = 10000

// defaultPrivateReply is the built-in unified auto-reply for direct messages (used when
// config private_reply is empty). The bot's commands only work in the guarded groups,
// so a plain DM would otherwise get no response at all.
const defaultPrivateReply = "👋 这是 Gentoo 中文社区的入群验证 + Gentoo/Linux 助手机器人。\n\n" +
	"• 想入群:回到群里发起加入申请,再点群消息中的「✅ 点此完成验证」链接来这里答题。\n" +
	"• 机器人命令(/pkg /use /bug /news /wiki /bbs 等)请在群里使用,私聊不处理。"

// privateNonStart matches any message in a private chat EXCEPT the /start command (the
// verification deep-link entry, which onStart handles). Registered before the command
// handlers so that DM'd commands — which only work in groups and would otherwise no-op
// silently — also get the unified auto-reply, while /start still reaches the verify flow.
func privateNonStart(_ context.Context, update telego.Update) bool {
	m := update.Message
	if m == nil || m.Chat.Type != "private" {
		return false
	}
	if fields := strings.Fields(m.Text); len(fields) > 0 {
		cmd := fields[0]
		if i := strings.IndexByte(cmd, '@'); i >= 0 { // strip /start@BotName
			cmd = cmd[:i]
		}
		if cmd == "/start" {
			return false
		}
	}
	return true
}

// onPrivateDM sends the unified auto-reply to a direct message, throttled per user so a
// DM flood doesn't amplify into a SendMessage flood (see dmReplyCooldown).
func (v *Verifier) onPrivateDM(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil {
		return nil
	}
	v.mu.Lock()
	if last, ok := v.dmLast[msg.From.ID]; ok && time.Since(last) < dmReplyCooldown {
		v.mu.Unlock()
		return nil // within cooldown: stay silent rather than reply to every flooded message
	}
	if len(v.dmLast) >= dmMapMax {
		v.dmLast = map[int64]time.Time{}
	}
	v.dmLast[msg.From.ID] = time.Now()
	v.mu.Unlock()
	_, _ = ctx.Bot().SendMessage(ctx.Context(), htmlMessage(msg.Chat.ID, v.cfg.PrivateReply))
	return nil
}
