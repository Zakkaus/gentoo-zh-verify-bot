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
		return fmt.Sprintf("📊 今日(%s)\n✅ 通过:%d 人\n❌ 拒绝:%d 人\n验证:%s | 运行 %s",
			date, ap, de, v.stateText(), uptimeStr(v.startTime))
	})
}

// onRich (admin) toggles rich-message output for /pkg and /use at runtime.
func (v *Verifier) onRich(ctx *th.Context, update telego.Update) error {
	return v.adminCmd(ctx, update, func() string {
		if v.toggleRich() {
			return "🎨 富文本输出已开启(/pkg、/use 用富消息;旧版/第三方客户端可能渲染不佳)。"
		}
		return "📄 富文本输出已关闭,/pkg、/use 改用纯文本。"
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
		"/wiki <关键词> — 搜索 Gentoo / Arch wiki(含中文页)\n" +
		"/bbs <关键词> — 搜各大 Linux 论坛(中文优先)\n" +
		"/distro <包名> — 跨发行版查版本(AUR/Arch/Debian/Ubuntu/Nix/openSUSE/Fedora)\n" +
		"/ping — 机器人状态 / 运行时长\n" +
		"/stats — 今日通过 / 拒绝人数\n" +
		"/help — 显示本帮助"
	if v.isGroupAdmin(c, bot, gid, msg.From.ID) {
		help += "\n\n👮 管理员:\n" +
			"/start — 开启入群验证\n" +
			"/stop — 关闭入群验证\n" +
			"/rich — 开关富文本输出(/pkg /use)\n" +
			"/sb — 回复某消息:删消息并踢出(可再申请)\n" +
			"/ban — 回复某消息:删消息并永久封禁\n" +
			fmt.Sprintf("/warn — 回复某消息:警告用户(满 %d 次自动踢出)\n", v.cfg.WarnLimit) +
			"/clearwarn — 回复某消息:清除用户警告\n" +
			"/bc — 频道马甲封禁开关;/bc allow|deny <频道id> 管白名单"
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
