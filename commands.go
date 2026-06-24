package main

import (
	"context"
	"fmt"
	"log"

	"github.com/mymmrac/telego"
)

// setupCommands registers the bot's command list with Telegram so that typing
// "/" in the chat shows an autocomplete menu. Member commands are visible to
// everyone; the admin commands are only shown to chat administrators.
func setupCommands(ctx context.Context, bot *telego.Bot, warnLimit int) {
	// Keep menu descriptions SHORT — Telegram's command menu truncates long ones.
	member := []telego.BotCommand{
		{Command: "help", Description: "查看可用指令"},
		{Command: "pkg", Description: "搜索 Gentoo 包"},
		{Command: "use", Description: "查包的 USE 标志 + 信息"},
		{Command: "bug", Description: "查 Gentoo Bugzilla 编号"},
		{Command: "news", Description: "查 / 搜 Gentoo 新闻"},
		{Command: "wiki", Description: "搜 Gentoo / Arch wiki"},
		{Command: "bbs", Description: "搜 Linux 论坛(中文优先)"},
		{Command: "pkgs", Description: "跨发行版查包版本"},
		{Command: "arm", Description: "查包的 arm64 keyword 状态"},
		{Command: "armpkgs", Description: "跨发行版查 arm64 支持"},
		{Command: "ping", Description: "机器人状态 / 运行时长"},
		{Command: "stats", Description: "今日通过 / 拒绝人数"},
	}
	admin := append([]telego.BotCommand{
		{Command: "start", Description: "[管理] 开启入群验证"},
		{Command: "stop", Description: "[管理] 关闭入群验证"},
		{Command: "mute", Description: "[管理] 回复:禁言(默认1h,可 /mute 30m)"},
		{Command: "unmute", Description: "[管理] 回复:解除禁言"},
		{Command: "sb", Description: "[管理] 回复:封禁并清空其消息"},
		{Command: "ban", Description: "[管理] 回复:封禁(踢出群)"},
		{Command: "warn", Description: fmt.Sprintf("[管理] 回复:警告(满 %d 次自动踢)", warnLimit)},
		{Command: "clearwarn", Description: "[管理] 回复:清除警告"},
		{Command: "bc", Description: "[管理] 频道马甲封禁 / 白名单"},
		{Command: "rich", Description: "[管理] 开关富文本(/pkg /use)"},
		{Command: "spoiler", Description: "[管理] 开关:遮盖新成员名字(防广告)"},
		{Command: "autodel", Description: "[管理] 查询结果自动删除开关"},
		{Command: "bantime", Description: "[管理] 设定封禁时长(0=永久)"},
	}, member...)

	if err := bot.SetMyCommands(ctx, &telego.SetMyCommandsParams{
		Commands: member,
		Scope:    &telego.BotCommandScopeDefault{Type: "default"},
	}); err != nil {
		log.Printf("setMyCommands(default): %v", err)
	}
	if err := bot.SetMyCommands(ctx, &telego.SetMyCommandsParams{
		Commands: admin,
		Scope:    &telego.BotCommandScopeAllChatAdministrators{Type: "all_chat_administrators"},
	}); err != nil {
		log.Printf("setMyCommands(admins): %v", err)
	}
	log.Printf("registered bot commands (member=%d, admin=%d)", len(member), len(admin))
}
