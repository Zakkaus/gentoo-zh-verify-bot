package main

import (
	"fmt"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

func uptimeStr(start time.Time) string {
	return time.Since(start).Round(time.Second).String()
}

func (v *Verifier) stateText() string {
	if v.isEnabled() {
		return "开启"
	}
	return "关闭"
}

func (v *Verifier) onPing(ctx *th.Context, update telego.Update) error {
	return v.adminCmd(ctx, update, func() string {
		return fmt.Sprintf("🏓 pong | 运行 %s | 验证:%s", uptimeStr(v.startTime), v.stateText())
	})
}

// onStart: in a private chat it's the deep-link verification entry; in a group it's the admin toggle.
func (v *Verifier) onStart(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg != nil && msg.Chat.Type == "private" {
		if msg.From != nil {
			v.sendDMChallenge(ctx.Context(), ctx.Bot(), msg.From.ID)
		}
		return nil
	}
	return v.adminCmd(ctx, update, func() string {
		v.setEnabled(true)
		return "✅ 入群验证已开启。"
	})
}

func (v *Verifier) onStop(ctx *th.Context, update telego.Update) error {
	return v.adminCmd(ctx, update, func() string {
		v.setEnabled(false)
		return "⏸ 入群验证已关闭。新入群申请将不再自动验证(留给人工审批)。"
	})
}

func (v *Verifier) onStats(ctx *th.Context, update telego.Update) error {
	return v.adminCmd(ctx, update, func() string {
		date, ap, de := v.stats()
		return fmt.Sprintf("📊 今日(%s)\n✅ 同意:%d 人\n❌ 拒绝:%d 人\n验证:%s | 运行 %s",
			date, ap, de, v.stateText(), uptimeStr(v.startTime))
	})
}

// onHelp lists commands (admins also see the moderation/admin commands).
func (v *Verifier) onHelp(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	gid := msg.Chat.ID
	defer func() {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(gid), MessageID: msg.MessageID})
	}()
	help := "🤖 可用指令:\n" +
		"/pkg <包名> — 搜索 Gentoo 包(官方树/gentoo-zh/guru)\n" +
		"/use <包名> — 某个包的 USE 标志 + 信息\n" +
		"/bug <编号> — 查询 Gentoo Bugzilla\n" +
		"/news [关键词] — 查看/搜索 Gentoo 新闻\n" +
		"/ping — 机器人状态 / 运行时长\n" +
		"/stats — 今日同意 / 拒绝人数\n" +
		"/help — 显示本帮助"
	if v.isGroupAdmin(c, bot, gid, msg.From.ID) {
		help += "\n\n👮 管理员:\n" +
			"/start — 开启入群验证\n" +
			"/stop — 关闭入群验证\n" +
			"/sb — 回复某消息:删消息并踢出(可再申请)\n" +
			"/ban — 回复某消息:删消息并永久封禁"
	}
	v.notify(c, bot, gid, help)
	return nil
}

// adminCmd runs fn only for a group admin in a guarded group, posts the result as
// a transient (auto-deleting) message, and removes the command message.
func (v *Verifier) adminCmd(ctx *th.Context, update telego.Update, fn func() string) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	gid := msg.Chat.ID
	defer func() {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(gid), MessageID: msg.MessageID})
	}()
	if !v.isGroupAdmin(c, bot, gid, msg.From.ID) {
		v.notify(c, bot, gid, "⛔ 该命令仅群管理员可用。")
		return nil
	}
	v.notify(c, bot, gid, fn())
	return nil
}
