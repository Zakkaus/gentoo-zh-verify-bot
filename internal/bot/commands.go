package bot

import (
	"context"
	"fmt"
	"log"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

func memberCommands(_ i18n.Lang) []telego.BotCommand {
	// Keep menu descriptions short because Telegram truncates them.
	return []telego.BotCommand{
		{Command: "help", Description: "查看可用指令"},
		{Command: "pkg", Description: "搜索 Gentoo 包"},
		{Command: "use", Description: "查包的 USE 标志 + 信息"},
		{Command: "bug", Description: "查 Gentoo Bugzilla 编号"},
		{Command: "news", Description: "查 / 搜 Gentoo 新闻"},
		{Command: "wiki", Description: "搜 Gentoo / Arch wiki"},
		{Command: "bbs", Description: "搜 Linux 论坛(中文优先)"},
		{Command: "pkgs", Description: "跨发行版查包版本"},
		{Command: "distro", Description: "/pkgs 的别名"},
		{Command: "arm", Description: "查包的 arm64 keyword 状态"},
		{Command: "armpkgs", Description: "跨发行版查 arm64 支持"},
		{Command: "ping", Description: "机器人状态 / 运行时长"},
		{Command: "stats", Description: "今日通过 / 拒绝人数"},
	}
}

func adminCommands(l i18n.Lang, warnLimit int) []telego.BotCommand {
	return append([]telego.BotCommand{
		{Command: "start", Description: "[管理] 开启入群验证"},
		{Command: "stop", Description: "[管理] 关闭入群验证"},
		{Command: "mute", Description: "[管理] 回复:禁言(默认1h,可 /mute 30m)"},
		{Command: "unmute", Description: "[管理] 回复:解除禁言"},
		{Command: "sb", Description: "[管理] 回复:封禁并清空其消息"},
		{Command: "ban", Description: "[管理] 回复:封禁(踢出群)"},
		{Command: "warn", Description: fmt.Sprintf("[管理] 回复:警告(满 %d 次自动踢)", warnLimit)},
		{Command: "clearwarn", Description: "[管理] 回复:清除警告"},
		{Command: "bc", Description: "[管理] 频道身份发言封禁 / 白名单"},
		{Command: "rich", Description: "[管理] 开关富文本(/pkg /use)"},
		{Command: "spoiler", Description: "[管理] 开关:遮盖新成员名字(防广告)"},
		{Command: "vmode", Description: "[管理] 切换验证方式(内核版本 / 选择题)"},
		{Command: "autodel", Description: "[管理] 查询结果自动删除开关"},
		{Command: "bantime", Description: "[管理] 设定封禁时长(0=永久)"},
	}, memberCommands(l)...)
}

// SetupCommands registers the member and administrator Telegram command menus.
func (s *Service) SetupCommands(ctx context.Context, bot *telego.Bot) {
	type commandMenu struct {
		name         string
		commands     []telego.BotCommand
		scope        telego.BotCommandScope
		languageCode string
	}
	groupIDs := []int64(nil)
	if s.settings != nil {
		groupIDs = s.settings.GroupIDs()
	}
	menus := make([]commandMenu, 0, 6+2*len(groupIDs))
	for _, language := range []struct {
		name string
		lang i18n.Lang
		code string
	}{
		{name: "fallback", lang: i18n.LangZH},
		{name: "zh", lang: i18n.LangZH, code: "zh"},
		{name: "en", lang: i18n.LangEN, code: "en"},
	} {
		member := memberCommands(language.lang)
		admin := adminCommands(language.lang, s.cfg.WarnLimit)
		menus = append(menus,
			commandMenu{name: "members/" + language.name, commands: member,
				scope: &telego.BotCommandScopeDefault{Type: "default"}, languageCode: language.code},
			commandMenu{name: "admins/" + language.name, commands: admin,
				scope: &telego.BotCommandScopeAllChatAdministrators{Type: "all_chat_administrators"}, languageCode: language.code},
		)
	}
	for _, groupID := range groupIDs {
		if s.groupLanguage(groupID) != i18n.LangZHHant {
			continue
		}
		member := memberCommands(i18n.LangZHHant)
		admin := adminCommands(i18n.LangZHHant, s.cfg.WarnLimit)
		menus = append(menus,
			commandMenu{name: fmt.Sprintf("members/chat/%d", groupID), commands: member,
				scope: &telego.BotCommandScopeChat{Type: "chat", ChatID: tu.ID(groupID)}},
			commandMenu{name: fmt.Sprintf("admins/chat/%d", groupID), commands: admin,
				scope: &telego.BotCommandScopeChatAdministrators{Type: "chat_administrators", ChatID: tu.ID(groupID)}},
		)
	}
	for _, menu := range menus {
		if err := bot.SetMyCommands(ctx, &telego.SetMyCommandsParams{
			Commands: menu.commands, Scope: menu.scope, LanguageCode: menu.languageCode,
		}); err != nil {
			log.Printf("setMyCommands(%s): %v", menu.name, err)
		}
	}
	log.Printf("registered bot command menus (%d scopes)", len(menus))
}

func (s *Service) groupLanguage(groupID int64) i18n.Lang {
	if s.settings != nil {
		if group, ok := s.settings.Group(groupID); ok {
			return i18n.FromStored(group.Lang().Value)
		}
	}
	return i18n.FromStored(s.cfg.LangForGroup(groupID))
}
