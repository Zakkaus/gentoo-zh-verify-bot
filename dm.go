package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
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
	"• 查询命令(/pkg /use /bug /news /wiki /bbs /pkgs /arm /armpkgs)私聊也能直接用(每分钟有限次,防滥用;群里不限次)。\n" +
	"• 审核/管理命令仅在群里有效。"

// dmCommands are the member commands usable in a private chat (rate-limited per user):
// the read-only lookups plus the informational /help, /ping, /stats. Everything else in a
// DM (admin/moderation commands, plain text) gets the unified auto-reply.
var dmCommands = map[string]bool{
	"pkg": true, "use": true, "bug": true, "news": true, "wiki": true, "bbs": true,
	"distro": true, "pkgs": true, "arm": true, "armpkgs": true,
	"help": true, "ping": true, "stats": true,
}

// privateNonStart matches a private-chat message that should get the unified auto-reply:
// anything EXCEPT the /start verification deep link and the dmCommands (which are allowed in
// DM and handled — rate-limited — by their own handlers registered after this).
func privateNonStart(_ context.Context, update telego.Update) bool {
	m := update.Message
	if m == nil || m.Chat.Type != "private" {
		return false
	}
	if fields := strings.Fields(m.Text); len(fields) > 0 {
		cmd := fields[0]
		if i := strings.IndexByte(cmd, '@'); i >= 0 { // strip /cmd@BotName
			cmd = cmd[:i]
		}
		if cmd == "/start" {
			return false
		}
		if strings.HasPrefix(cmd, "/") && dmCommands[cmd[1:]] {
			return false // a member command usable in DM — let its (rate-limited) handler run
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
	// private_reply is admin-supplied and sent in HTML mode; if a stray <, > or & makes
	// Telegram reject it ("can't parse entities"), fall back to plain text so the user still
	// gets a reply, and log it so the misconfiguration is diagnosable.
	if _, err := ctx.Bot().SendMessage(ctx.Context(), htmlMessage(msg.Chat.ID, v.cfg.PrivateReply)); err != nil {
		log.Printf("private_reply HTML send failed (%v); retrying as plain text", err)
		_, _ = ctx.Bot().SendMessage(ctx.Context(), tu.Message(tu.ID(msg.Chat.ID), v.cfg.PrivateReply))
	}
	return nil
}
