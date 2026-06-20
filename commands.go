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
	member := []telego.BotCommand{
		{Command: "help", Description: "查看可用指令"},
		{Command: "pkg", Description: "搜索 Gentoo 包(官方树 / gentoo-zh / guru)"},
		{Command: "use", Description: "查看某个包的 USE 标志 + 信息"},
		{Command: "bug", Description: "查询 Gentoo Bugzilla 编号"},
		{Command: "news", Description: "查看 / 搜索 Gentoo 新闻"},
		{Command: "wiki", Description: "搜索 Gentoo / Arch wiki(含中文页)"},
		{Command: "bbs", Description: "搜各大 Linux 论坛(中文优先)"},
		{Command: "distro", Description: "跨发行版查包版本(Gentoo/AUR/Arch/Alpine/Debian/Nix/Fedora/SUSE 等)"},
		{Command: "ping", Description: "查看机器人状态 / 运行时长"},
		{Command: "stats", Description: "今日通过 / 拒绝人数"},
	}
	admin := append([]telego.BotCommand{
		{Command: "start", Description: "[管理] 开启入群验证"},
		{Command: "stop", Description: "[管理] 关闭入群验证"},
		{Command: "sb", Description: "[管理] 回复消息:删消息并踢出(可再申请)"},
		{Command: "ban", Description: "[管理] 回复消息:删消息并永久封禁"},
		{Command: "warn", Description: fmt.Sprintf("[管理] 回复消息:警告用户(满 %d 次自动踢出)", warnLimit)},
		{Command: "clearwarn", Description: "[管理] 回复消息:清除用户警告"},
		{Command: "bc", Description: "[管理] 频道马甲封禁开关 / 白名单"},
		{Command: "rich", Description: "[管理] 开关富文本输出(/pkg /use)"},
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
