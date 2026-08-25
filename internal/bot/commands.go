package bot

import (
	"context"
	"fmt"
	"log"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

func memberCommands(l i18n.Lang) []telego.BotCommand {
	// Keep menu descriptions short because Telegram truncates them.
	menu := i18n.Messages.Bot.Menu.Member
	return []telego.BotCommand{
		{Command: "help", Description: menu.Help.For(l)},
		{Command: "pkg", Description: menu.Pkg.For(l)},
		{Command: "use", Description: menu.Use.For(l)},
		{Command: "bug", Description: menu.Bug.For(l)},
		{Command: "news", Description: menu.News.For(l)},
		{Command: "wiki", Description: menu.Wiki.For(l)},
		{Command: "bbs", Description: menu.BBS.For(l)},
		{Command: "pkgs", Description: menu.Pkgs.For(l)},
		{Command: "distro", Description: menu.Distro.For(l)},
		{Command: "arm", Description: menu.Arm.For(l)},
		{Command: "armpkgs", Description: menu.ArmPkgs.For(l)},
		{Command: "ping", Description: menu.Ping.For(l)},
		{Command: "stats", Description: menu.Stats.For(l)},
	}
}

func adminCommands(l i18n.Lang, warnLimit int) []telego.BotCommand {
	menu := i18n.Messages.Bot.Menu.Admin
	return append([]telego.BotCommand{
		{Command: "start", Description: menu.Start.For(l)},
		{Command: "settings", Description: i18n.Messages.Panel.Menu.Settings.For(l)},
		{Command: "stop", Description: menu.Stop.For(l)},
		{Command: "mute", Description: menu.Mute.For(l)},
		{Command: "unmute", Description: menu.Unmute.For(l)},
		{Command: "sb", Description: menu.Purge.For(l)},
		{Command: "ban", Description: menu.Ban.For(l)},
		{Command: "warn", Description: menu.Warn.Render(l, warnLimit)},
		{Command: "clearwarn", Description: menu.ClearWarn.For(l)},
		{Command: "bc", Description: menu.Channel.For(l)},
		{Command: "rich", Description: menu.RichText.For(l)},
		{Command: "spoiler", Description: menu.NameSpoiler.For(l)},
		{Command: "vmode", Description: menu.VerificationMode.For(l)},
		{Command: "autodel", Description: menu.AutoDelete.For(l)},
		{Command: "bantime", Description: menu.BanTime.For(l)},
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
