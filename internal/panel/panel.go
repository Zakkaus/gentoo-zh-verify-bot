// Package panel owns the existing Telegram administration command surface.
package panel

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/verify"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

// Verification is the administration surface provided by verification.
type Verification interface {
	AgentStatsText(l i18n.Lang) string
	ControlGroupID() int64
	DMOrGroup(msg *telego.Message) bool
	KernelAnswerDM(ctx context.Context, update telego.Update) bool
	EffectiveMode(groupID int64) string
	IsEnabled(groupID int64) bool
	SendDMChallenge(ctx context.Context, bot *telego.Bot, userID int64)
	SetAutoDelete(groupID int64, ttl time.Duration, enabled bool) error
	SetEnabled(groupID int64, enabled bool) error
	SetVerifyMode(groupID int64, mode string) error
	Stats() (date string, approved, declined int)
	ToggleNameSpoiler(groupID int64) (bool, error)
	ToggleRich() (bool, error)
}

// Moderation is the administration surface provided by moderation.
type Moderation interface {
	OnBanTime(ctx *th.Context, update telego.Update) error
	UpdateChannelWhitelist(ctx context.Context, groupID, senderID int64, allow bool) (unbanErr, updateErr error)
}

// Lookup is the administration surface provided by lookup.
type Lookup interface {
	AutoDelete(groupID int64) (time.Duration, bool)
}

// Panel owns the existing administration handlers and their policy gates.
type Panel struct {
	settings   *store.Settings
	telegram   *tg.Client
	cfg        *config.Config
	verifier   Verification
	moderation Moderation
	lookups    Lookup
	version    string
	startedAt  time.Time
	panelState *settingsPanelState
}

// New constructs the existing administration surface from explicit dependencies.
func New(
	settings *store.Settings,
	telegram *tg.Client,
	cfg *config.Config,
	_ *i18n.Catalog,
	verifier Verification,
	moderation Moderation,
	lookups Lookup,
	version string,
	startedAt time.Time,
) *Panel {
	return &Panel{
		settings:   settings,
		telegram:   telegram,
		cfg:        cfg,
		verifier:   verifier,
		moderation: moderation,
		lookups:    lookups,
		version:    version,
		startedAt:  startedAt,
		panelState: newSettingsPanelState(),
	}
}

func uptimeStr(start time.Time) string {
	return time.Since(start).Round(time.Second).String()
}
func (v *Panel) groupLanguage(groupID int64) i18n.Lang {
	if v.settings != nil {
		if group, ok := v.settings.Group(groupID); ok {
			return i18n.FromStored(group.Lang().Value)
		}
	}
	return i18n.FromStored(v.cfg.LangForGroup(groupID))
}

func (v *Panel) isGroup(groupID int64) bool {
	if v.settings != nil {
		return v.settings.IsGroup(groupID)
	}
	return v.cfg.IsGroup(groupID)
}

func (v *Panel) privateQueryPerMin() int {
	if v.settings != nil {
		return v.settings.Global().PrivateQueryPerMin().Value
	}
	return v.cfg.PrivateQueryPerMin
}

func (v *Panel) controlGroupAllowed(groupID int64, l i18n.Lang) (bool, string) {
	controlGroupID := v.cfg.ControlGroupID
	if v.settings != nil {
		controlGroupID = v.settings.ControlGroupID()
	}
	if controlGroupID == 0 || groupID == controlGroupID {
		return true, ""
	}
	return false, i18n.Messages.Feed.Config.ControlGroupOnly.Render(l, controlGroupID)
}

func (v *Panel) requesterLanguage(msg *telego.Message) i18n.Lang {
	fallback := i18n.LangEN
	if v.isGroup(msg.Chat.ID) {
		fallback = v.groupLanguage(msg.Chat.ID)
	}
	return i18n.FromRequester(msg.From.LanguageCode, fallback)
}

func (v *Panel) stateText(l i18n.Lang, groupID int64) string {
	if v.verifier.IsEnabled(groupID) {
		return i18n.Messages.Panel.State.Enabled.For(l)
	}
	return i18n.Messages.Panel.State.Disabled.For(l)
}

// OnPing reports the version, uptime, and verification state.
func (v *Panel) OnPing(ctx *th.Context, update telego.Update) error {
	return v.memberCmd(ctx, update, func(groupID int64, l i18n.Lang) string {
		return i18n.Messages.Panel.Status.Ping.Render(l, v.version, uptimeStr(v.startedAt), v.stateText(l, groupID))
	})
}

// OnStart routes settings deep links before the existing verification branch.
func (v *Panel) OnStart(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg != nil && msg.Chat.Type == "private" {
		if v.openSettingsStart(ctx, msg, panelStartToken(msg.Text)) {
			return nil
		}
		if msg.From != nil {
			v.verifier.SendDMChallenge(ctx.Context(), ctx.Bot(), msg.From.ID)
		}
		return nil
	}
	return v.settingsAdminCmd(ctx, update, func(groupID int64, l i18n.Lang) (string, error) {
		if err := v.verifier.SetEnabled(groupID, true); err != nil {
			return "", err
		}
		return i18n.Messages.Panel.Verification.Started.For(l), nil
	})
}

// OnStop disables verification in the invoking group.
func (v *Panel) OnStop(ctx *th.Context, update telego.Update) error {
	return v.settingsAdminCmd(ctx, update, func(groupID int64, l i18n.Lang) (string, error) {
		if err := v.verifier.SetEnabled(groupID, false); err != nil {
			return "", err
		}
		return i18n.Messages.Panel.Verification.Stopped.For(l), nil
	})
}

// OnStats reports daily verification counts and cumulative agent counts.
func (v *Panel) OnStats(ctx *th.Context, update telego.Update) error {
	return v.memberCmd(ctx, update, func(groupID int64, l i18n.Lang) string {
		date, ap, de := v.verifier.Stats()
		out := i18n.Messages.Panel.Status.Stats.Render(
			l, date, ap, de, v.stateText(l, groupID), uptimeStr(v.startedAt))
		if ai := v.verifier.AgentStatsText(l); ai != "" { // Lifetime tally; daily stats reset separately.
			out += "\n" + ai
		}
		return out
	})
}

// OnRich toggles the process-wide rich-message setting.
func (v *Panel) OnRich(ctx *th.Context, update telego.Update) error {
	return v.globalSettingsAdminCmd(ctx, update, func(_ int64, l i18n.Lang) (string, error) {
		enabled, err := v.verifier.ToggleRich()
		if err != nil {
			return "", err
		}
		if enabled {
			return i18n.Messages.Panel.RichText.Enabled.For(l), nil
		}
		return i18n.Messages.Panel.RichText.Disabled.For(l), nil
	})
}

// OnSpoiler toggles persisted applicant-name hiding for the invoking group.
func (v *Panel) OnSpoiler(ctx *th.Context, update telego.Update) error {
	return v.settingsAdminCmd(ctx, update, func(groupID int64, l i18n.Lang) (string, error) {
		enabled, err := v.verifier.ToggleNameSpoiler(groupID)
		if err != nil {
			return "", err
		}
		if enabled {
			return i18n.Messages.Panel.NameSpoiler.Enabled.For(l), nil
		}
		return i18n.Messages.Panel.NameSpoiler.Disabled.For(l), nil
	})
}

// OnVMode changes the invoking group's verification mode.
func (v *Panel) OnVMode(ctx *th.Context, update telego.Update) error {
	return v.settingsAdminCmd(ctx, update, func(groupID int64, l i18n.Lang) (string, error) {
		arg := strings.ToLower(strings.TrimSpace(adminCommandArg(update.Message.Text)))
		switch arg {
		case "":
			source := i18n.Messages.Panel.VerificationMode.ConfigSource.For(l)
			if group, ok := v.settings.Group(groupID); ok && group.VerifyMode().Source == store.SourceRuntime {
				source = i18n.Messages.Panel.VerificationMode.RuntimeSource.For(l)
			}
			return i18n.Messages.Panel.VerificationMode.Current.Render(
				l, verify.ModeName(l, v.verifier.EffectiveMode(groupID)), source), nil
		case config.ModeKernel, config.ModeQuiz, config.ModeMixed:
			if err := v.verifier.SetVerifyMode(groupID, arg); err != nil {
				return "", err
			}
			if arg == (config.ModeKernel) {
				return i18n.Messages.Panel.VerificationMode.KernelSet.For(l), nil
			}
			return i18n.Messages.Panel.VerificationMode.Set.Render(l, verify.ModeName(l, arg)), nil
		case "auto", "config", "default":
			if err := v.verifier.SetVerifyMode(groupID, ""); err != nil {
				return "", err
			}
			return i18n.Messages.Panel.VerificationMode.AutoSet.Render(
				l, verify.ModeName(l, v.verifier.EffectiveMode(groupID))), nil
		}
		return i18n.Messages.Panel.VerificationMode.Usage.For(l), nil
	})
}

func adminCommandArg(text string) string {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return ""
	}
	return strings.TrimSpace(strings.Join(fields[1:], " "))
}

func parseAutoDelArg(arg string) (action string, ttl time.Duration) {
	switch arg {
	case "":
		return "show", 0
	case "off":
		return "off", 0
	case "on":
		return "on", 0
	}
	if n, err := strconv.Atoi(arg); err == nil && n >= 1 && n <= 1440 {
		return "set", time.Duration(n) * time.Minute
	}
	return "", 0
}

// OnAutoDel handles the invoking group's lookup cleanup setting.
func (v *Panel) OnAutoDel(ctx *th.Context, update telego.Update) error {
	return v.settingsAdminCmd(ctx, update, func(groupID int64, l i18n.Lang) (string, error) {
		action, ttl := parseAutoDelArg(strings.ToLower(strings.TrimSpace(adminCommandArg(update.Message.Text))))
		switch action {
		case "show":
			if current, enabled := v.lookups.AutoDelete(groupID); enabled {
				return i18n.Messages.Panel.AutoDelete.CurrentEnabled.Render(l, int(current/time.Minute)), nil
			}
			return i18n.Messages.Panel.AutoDelete.CurrentDisabled.For(l), nil
		case "off":
			if err := v.verifier.SetAutoDelete(groupID, 0, false); err != nil {
				return "", err
			}
			return i18n.Messages.Panel.AutoDelete.Disabled.For(l), nil
		case "on":
			if err := v.verifier.SetAutoDelete(groupID, 0, true); err != nil {
				return "", err
			}
			current, _ := v.lookups.AutoDelete(groupID)
			return i18n.Messages.Panel.AutoDelete.Enabled.Render(l, int(current/time.Minute)), nil
		case "set":
			if err := v.verifier.SetAutoDelete(groupID, ttl, true); err != nil {
				return "", err
			}
			return i18n.Messages.Panel.AutoDelete.Set.Render(l, int(ttl/time.Minute)), nil
		default:
			return i18n.Messages.Panel.AutoDelete.Usage.For(l), nil
		}
	})
}

// OnBanTime delegates the group-specific ban-duration command to moderation.
func (v *Panel) OnBanTime(ctx *th.Context, update telego.Update) error {
	return v.moderation.OnBanTime(ctx, update)
}

func memberHelpText(l i18n.Lang) string {
	return i18n.Messages.Panel.Help.Member.For(l)
}

// Settings and verification commands require a fresh, successful admin lookup.
func (v *Panel) isGroupAdmin(ctx context.Context, _ *telego.Bot, chatID, userID int64) bool {
	ok, err := v.telegram.FreshAdmin(ctx, chatID, userID)
	if err != nil {
		log.Printf("isGroupAdmin getChatMember chat=%d user=%d: %v", chatID, userID, err)
		return false
	}
	return ok
}

func (v *Panel) isGroupAdminCached(ctx context.Context, _ *telego.Bot, chatID, userID int64) bool {
	ok, err := v.telegram.CachedAdmin(ctx, chatID, userID)
	if err != nil {
		log.Printf("isGroupAdminCached getChatMember chat=%d user=%d: %v", chatID, userID, err)
		return false
	}
	return ok
}

func (v *Panel) notify(ctx context.Context, _ *telego.Bot, chatID int64, text string) {
	v.telegram.Notify(ctx, chatID, text, v.cfg.NotifyTTLSeconds)
}

// OnHelp reports member commands and group administration commands when authorized.
func (v *Panel) OnHelp(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !v.verifier.DMOrGroup(msg) { // /help is free (no external request)
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	chatID := msg.Chat.ID
	inGroup := v.isGroup(chatID)
	l := v.requesterLanguage(msg)
	help := memberHelpText(l)
	if inGroup {
		help += "\n\n" + i18n.Messages.Panel.Help.GroupState.Render(l, v.stateText(l, chatID))
	}
	if inGroup && v.isGroupAdminCached(c, bot, chatID, msg.From.ID) {
		help += "\n\n" + i18n.Messages.Panel.Help.Admin.Render(l, v.cfg.WarnLimit)
	}
	if inGroup {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: msg.MessageID})
		v.notify(c, bot, chatID, help)
		return nil
	}
	help += "\n\n" + i18n.Messages.Panel.Help.DirectMessageNote.Render(l, v.privateQueryPerMin())
	// Plain text keeps angle-bracket placeholders from being parsed as Telegram HTML.
	_, _ = bot.SendMessage(c, tu.Message(tu.ID(chatID), help))
	return nil
}

// Informational commands are unrestricted; only external lookups are rate-limited.
func (v *Panel) memberCmd(ctx *th.Context, update telego.Update, fn func(groupID int64, l i18n.Lang) string) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !v.verifier.DMOrGroup(msg) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	groupID := msg.Chat.ID
	l := v.requesterLanguage(msg)
	if v.isGroup(groupID) {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(groupID), MessageID: msg.MessageID})
		v.notify(c, bot, groupID, fn(groupID, l))
		return nil
	}
	_, _ = bot.SendMessage(c, tu.Message(tu.ID(groupID), fn(v.verifier.ControlGroupID(), l)))
	return nil
}

func (v *Panel) notifySettingsFailure(c context.Context, bot *telego.Bot, groupID int64, l i18n.Lang, err error) {
	log.Printf("settings command in group %d failed: %v", groupID, err)
	v.notify(c, bot, groupID, i18n.Messages.Panel.Error.SaveSettings.For(l))
}

func (v *Panel) settingsAdminCmd(ctx *th.Context, update telego.Update, fn func(groupID int64, l i18n.Lang) (string, error)) error {
	return v.runSettingsAdminCmd(ctx, update, false, fn)
}

func (v *Panel) globalSettingsAdminCmd(ctx *th.Context, update telego.Update, fn func(groupID int64, l i18n.Lang) (string, error)) error {
	return v.runSettingsAdminCmd(ctx, update, true, fn)
}

func (v *Panel) runSettingsAdminCmd(ctx *th.Context, update telego.Update, controlGroupOnly bool, fn func(groupID int64, l i18n.Lang) (string, error)) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !v.isGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	groupID := msg.Chat.ID
	l := v.groupLanguage(groupID)
	defer func() {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(groupID), MessageID: msg.MessageID})
	}()
	if controlGroupOnly {
		if allowed, refusal := v.controlGroupAllowed(groupID, l); !allowed {
			v.notify(c, bot, groupID, refusal)
			return nil
		}
	}
	if !v.isGroupAdmin(c, bot, groupID, msg.From.ID) {
		v.notify(c, bot, groupID, i18n.Messages.Panel.Error.AdminOnly.For(l))
		return nil
	}
	text, err := fn(groupID, l)
	if err != nil {
		v.notifySettingsFailure(c, bot, groupID, l, err)
		return nil
	}
	v.notify(c, bot, groupID, text)
	return nil
}
