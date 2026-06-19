package main

import (
	"context"
	"log"

	"github.com/mymmrac/telego"
)

// setupCommands registers the bot's command list with Telegram so that typing
// "/" in the chat shows an autocomplete menu. Member commands are visible to
// everyone; the admin commands are only shown to chat administrators.
func setupCommands(ctx context.Context, bot *telego.Bot) {
	member := []telego.BotCommand{
		{Command: "help", Description: "查看可用指令"},
		{Command: "pkg", Description: "搜索 Gentoo 包(官方树 / gentoo-zh / guru)"},
		{Command: "news", Description: "查看 / 搜索 Gentoo 新闻"},
		{Command: "ping", Description: "查看机器人状态 / 运行时长"},
		{Command: "stats", Description: "今日同意 / 拒绝人数"},
	}
	admin := append([]telego.BotCommand{
		{Command: "start", Description: "[管理] 开启入群验证"},
		{Command: "stop", Description: "[管理] 关闭入群验证"},
		{Command: "sb", Description: "[管理] 回复消息:删消息并踢出(可再申请)"},
		{Command: "ban", Description: "[管理] 回复消息:删消息并永久封禁"},
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
