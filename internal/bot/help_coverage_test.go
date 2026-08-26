package bot

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/mymmrac/telego"
)

var (
	helpLocales    = []i18n.Lang{i18n.LangEN, i18n.LangZH, i18n.LangZHHant}
	helpCommandRe  = regexp.MustCompile(`/([a-z]+)`)
	helpLocaleName = map[i18n.Lang]string{i18n.LangEN: "en", i18n.LangZH: "zh", i18n.LangZHHant: "zh-Hant"}
)

func namesOf(cmds []telego.BotCommand) map[string]bool {
	out := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		out[c.Command] = true
	}
	return out
}

// A command reaches users through two independent surfaces: the Telegram menu and the /help
// text. Nothing links them, so adding a command and forgetting the help text is silent. These
// tests are that link.
func TestMemberHelpListsEveryMenuCommand(t *testing.T) {
	for _, l := range helpLocales {
		help := i18n.Messages.Panel.Help.Member.For(l)
		for _, c := range memberCommands(l) {
			if !regexp.MustCompile(`/` + c.Command + `\b`).MatchString(help) {
				t.Errorf("%s: /%s is in the member menu but not in the /help text", helpLocaleName[l], c.Command)
			}
		}
	}
}

func TestMemberHelpMentionsNoUnknownCommand(t *testing.T) {
	for _, l := range helpLocales {
		known := namesOf(memberCommands(l))
		for _, m := range helpCommandRe.FindAllStringSubmatch(i18n.Messages.Panel.Help.Member.For(l), -1) {
			if !known[m[1]] {
				t.Errorf("%s: /help lists /%s, which the bot does not register", helpLocaleName[l], m[1])
			}
		}
	}
}

func TestAdminHelpMatchesAdminMenu(t *testing.T) {
	for _, l := range helpLocales {
		help := i18n.Messages.Panel.Help.Admin.Render(l, 3)
		member := namesOf(memberCommands(l))
		known := namesOf(adminCommands(l, 3))
		for _, c := range adminCommands(l, 3) {
			if member[c.Command] {
				continue // member commands are documented by the member help
			}
			if !regexp.MustCompile(`/` + c.Command + `\b`).MatchString(help) {
				t.Errorf("%s: /%s is in the administrator menu but not in the administrator help", helpLocaleName[l], c.Command)
			}
		}
		for _, m := range helpCommandRe.FindAllStringSubmatch(help, -1) {
			if !known[m[1]] {
				t.Errorf("%s: administrator help lists /%s, which the bot does not register", helpLocaleName[l], m[1])
			}
		}
	}
}

// Every rendered string must have had its edition prefix substituted; a leftover token would
// reach a user as literal "{g}".
// The auto-reply used to repeat the command list, which is how it went stale. It must now
// point at /help and name no lookup command of its own.
func TestAutoReplyNamesNoLookupCommand(t *testing.T) {
	for _, l := range helpLocales {
		reply := i18n.Messages.Bot.DirectMessage.AutoReply.Render(l, 5)
		if !strings.Contains(reply, "/help") {
			t.Errorf("%s: the direct-message reply must point at /help", helpLocaleName[l])
		}
		for _, m := range helpCommandRe.FindAllStringSubmatch(reply, -1) {
			if m[1] != "help" {
				t.Errorf("%s: the direct-message reply names /%s; list commands in /help only", helpLocaleName[l], m[1])
			}
		}
	}
}

func TestNoUnsubstitutedEditionToken(t *testing.T) {
	for _, l := range helpLocales {
		for name, s := range map[string]string{
			"help.member": i18n.Messages.Panel.Help.Member.For(l),
			"help.admin":  i18n.Messages.Panel.Help.Admin.Render(l, 3),
			"auto_reply":  i18n.Messages.Bot.DirectMessage.AutoReply.Render(l, 5),
		} {
			if strings.Contains(s, "{g}") {
				t.Errorf("%s: %s still contains an unsubstituted {g}", helpLocaleName[l], name)
			}
		}
	}
}

// The four lookups added in v4.5.0 reached the menu but not the direct-message allow-list, so
// they fell through to the generic auto-reply; in the generic build the prefixed Gentoo
// lookups did the same. Both are now derived from one list.
func TestDMAllowsEveryMemberCommand(t *testing.T) {
	for _, c := range memberCommands(i18n.LangEN) {
		update := telego.Update{Message: &telego.Message{
			Chat: telego.Chat{Type: "private"},
			Text: "/" + c.Command + " something",
		}}
		if privateNonStart(context.Background(), update) {
			t.Errorf("/%s does not reach its handler in a direct message", c.Command)
		}
	}
}

func TestDMRejectsUnregisteredCommand(t *testing.T) {
	for _, cmd := range []string{"/nosuchcommand", "/mute", "/settings"} {
		update := telego.Update{Message: &telego.Message{
			Chat: telego.Chat{Type: "private"},
			Text: cmd,
		}}
		if !privateNonStart(context.Background(), update) {
			t.Errorf("%s should fall through to the direct-message reply", cmd)
		}
	}
}
