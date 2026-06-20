package main

import (
	"context"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// defaultPrivateReply is the built-in unified auto-reply for direct messages (used when
// config private_reply is empty). The bot's commands only work in the guarded groups,
// so a plain DM would otherwise get no response at all.
const defaultPrivateReply = "👋 这是 Gentoo 中文社区的入群验证 + Gentoo/Linux 助手机器人。\n\n" +
	"• 想入群:回到群里发起加入申请,再点群消息中的「✅ 点此完成验证」链接来这里答题。\n" +
	"• 机器人命令(/pkg /use /bug /news /wiki /bbs 等)请在群里使用,私聊不处理。"

// privateMessage matches a (non-command-consumed) text/other message in a private chat.
// Registered last, so the verify-flow /start and the in-group commands take precedence;
// only otherwise-unhandled DMs fall through to onPrivateDM.
func privateMessage(_ context.Context, update telego.Update) bool {
	return update.Message != nil && update.Message.Chat.Type == "private"
}

// onPrivateDM sends the unified auto-reply to a direct message.
func (v *Verifier) onPrivateDM(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil {
		return nil
	}
	_, _ = ctx.Bot().SendMessage(ctx.Context(), htmlMessage(msg.Chat.ID, v.cfg.PrivateReply))
	return nil
}
