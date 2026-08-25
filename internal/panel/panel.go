// Package panel owns the existing Telegram administration command surface.
package panel

import (
	"context"
	"fmt"
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

func (v *Panel) requesterLanguage(msg *telego.Message) i18n.Lang {
	fallback := i18n.LangEN
	if v.cfg.IsGroup(msg.Chat.ID) {
		fallback = v.groupLanguage(msg.Chat.ID)
	}
	return i18n.FromRequester(msg.From.LanguageCode, fallback)
}

func (v *Panel) stateText(_ i18n.Lang, groupID int64) string {
	if v.verifier.IsEnabled(groupID) {
		return "开启"
	}
	return "关闭"
}

// OnPing reports the version, uptime, and verification state.
func (v *Panel) OnPing(ctx *th.Context, update telego.Update) error {
	return v.memberCmd(ctx, update, func(groupID int64, l i18n.Lang) string {
		return fmt.Sprintf("🏓 pong | %s | 运行 %s | 验证:%s", v.version, uptimeStr(v.startedAt), v.stateText(l, groupID))
	})
}

// OnStart opens verification in DMs and enables verification in groups.
func (v *Panel) OnStart(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg != nil && msg.Chat.Type == "private" {
		if msg.From != nil {
			v.verifier.SendDMChallenge(ctx.Context(), ctx.Bot(), msg.From.ID)
		}
		return nil
	}
	return v.settingsAdminCmd(ctx, update, func(groupID int64, _ i18n.Lang) (string, error) {
		if err := v.verifier.SetEnabled(groupID, true); err != nil {
			return "", err
		}
		return "✅ 入群验证已开启。", nil
	})
}

// OnStop disables verification in the invoking group.
func (v *Panel) OnStop(ctx *th.Context, update telego.Update) error {
	return v.settingsAdminCmd(ctx, update, func(groupID int64, _ i18n.Lang) (string, error) {
		if err := v.verifier.SetEnabled(groupID, false); err != nil {
			return "", err
		}
		return "⏸ 入群验证已关闭。新入群申请将不再自动验证(留给人工审批)。", nil
	})
}

// OnStats reports daily verification counts and cumulative agent counts.
func (v *Panel) OnStats(ctx *th.Context, update telego.Update) error {
	return v.memberCmd(ctx, update, func(groupID int64, l i18n.Lang) string {
		date, ap, de := v.verifier.Stats()
		out := fmt.Sprintf("📊 今日(%s)\n✅ 通过:%d 人\n❌ 拒绝:%d 人\n验证:%s | 运行 %s",
			date, ap, de, v.stateText(l, groupID), uptimeStr(v.startedAt))
		if ai := v.verifier.AgentStatsText(l); ai != "" { // Lifetime tally; daily stats reset separately.
			out += "\n" + ai
		}
		return out
	})
}

// OnRich toggles the process-wide rich-message setting.
func (v *Panel) OnRich(ctx *th.Context, update telego.Update) error {
	return v.globalSettingsAdminCmd(ctx, update, func(_ int64, _ i18n.Lang) (string, error) {
		enabled, err := v.verifier.ToggleRich()
		if err != nil {
			return "", err
		}
		if enabled {
			return "🎨 富文本输出已开启(/pkg、/use 用富消息;旧版/第三方客户端可能渲染不佳)。", nil
		}
		return "📄 富文本输出已关闭,/pkg、/use 改用纯文本。", nil
	})
}

// OnSpoiler toggles persisted applicant-name hiding for the invoking group.
func (v *Panel) OnSpoiler(ctx *th.Context, update telego.Update) error {
	return v.settingsAdminCmd(ctx, update, func(groupID int64, _ i18n.Lang) (string, error) {
		enabled, err := v.verifier.ToggleNameSpoiler(groupID)
		if err != nil {
			return "", err
		}
		if enabled {
			return "🫥 已开启:新成员的名字会用「防剧透」遮盖,不点开看不到 —— 防止有人拿广告当名字在群里刷屏。", nil
		}
		return "👁 已关闭:新成员的名字将正常显示(不再遮盖)。", nil
	})
}

// OnVMode changes the invoking group's verification mode.
func (v *Panel) OnVMode(ctx *th.Context, update telego.Update) error {
	return v.settingsAdminCmd(ctx, update, func(groupID int64, l i18n.Lang) (string, error) {
		arg := strings.ToLower(strings.TrimSpace(adminCommandArg(update.Message.Text)))
		switch arg {
		case "":
			source := "配置文件"
			if group, ok := v.settings.Group(groupID); ok && group.VerifyMode().Source == store.SourceRuntime {
				source = "/vmode 设定"
			}
			return fmt.Sprintf("🔐 当前入群验证方式:%s(来自%s)。\n用法:/vmode kernel 改为填内核版本号;/vmode quiz 改回选择题;/vmode mixed 两者随机;/vmode auto 恢复按配置文件。",
				verify.ModeName(l, v.verifier.EffectiveMode(groupID)), source), nil
		case config.ModeKernel, config.ModeQuiz, config.ModeMixed:
			if err := v.verifier.SetVerifyMode(groupID, arg); err != nil {
				return "", err
			}
			if arg == (config.ModeKernel) {
				return "🔐 入群验证已改为:填写内核版本号 —— 申请者必须自己输入 uname -r 的版本号,只会乱点按钮的机器人进不来。", nil
			}
			return "🔐 入群验证已改为:" + verify.ModeName(l, arg) + "。", nil
		case "auto", "config", "default":
			if err := v.verifier.SetVerifyMode(groupID, ""); err != nil {
				return "", err
			}
			return fmt.Sprintf("🔐 已恢复按配置文件决定验证方式,本群当前为:%s。", verify.ModeName(l, v.verifier.EffectiveMode(groupID))), nil
		}
		return "用法:/vmode kernel|quiz|mixed|auto(不带参数查看当前设置)。", nil
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
	return v.settingsAdminCmd(ctx, update, func(groupID int64, _ i18n.Lang) (string, error) {
		action, ttl := parseAutoDelArg(strings.ToLower(strings.TrimSpace(adminCommandArg(update.Message.Text))))
		switch action {
		case "show":
			if current, enabled := v.lookups.AutoDelete(groupID); enabled {
				return fmt.Sprintf("🧹 查询结果自动删除:已开启,%d 分钟后连同提问一起删除。\n用法:/autodel off 关闭;/autodel <分钟> 调整时间。", int(current/time.Minute)), nil
			}
			return "查询结果自动删除:已关闭。\n用法:/autodel on 开启;/autodel <分钟> 设定时间并开启。", nil
		case "off":
			if err := v.verifier.SetAutoDelete(groupID, 0, false); err != nil {
				return "", err
			}
			return "已关闭查询结果自动删除(查询命令 /pkg、/use、/bug、/news、/wiki、/bbs、/pkgs、/arm、/armpkgs 的回复将保留)。", nil
		case "on":
			if err := v.verifier.SetAutoDelete(groupID, 0, true); err != nil {
				return "", err
			}
			current, _ := v.lookups.AutoDelete(groupID)
			return fmt.Sprintf("🧹 已开启:查询结果 %d 分钟后连同提问一起删除。", int(current/time.Minute)), nil
		case "set":
			if err := v.verifier.SetAutoDelete(groupID, ttl, true); err != nil {
				return "", err
			}
			return fmt.Sprintf("🧹 已设定:查询结果 %d 分钟后连同提问一起删除。", int(ttl/time.Minute)), nil
		default:
			return "用法:/autodel on|off,或 /autodel <分钟数>(1–1440)。", nil
		}
	})
}

// OnBanTime delegates the group-specific ban-duration command to moderation.
func (v *Panel) OnBanTime(ctx *th.Context, update telego.Update) error {
	return v.moderation.OnBanTime(ctx, update)
}

func memberHelpText(_ i18n.Lang) string {
	return "🤖 可用指令:\n" +
		"/pkg <包名> — 搜索 Gentoo 包(官方树/gentoo-zh/guru)\n" +
		"/use <包名> — 某个包的 USE 标志 + 信息\n" +
		"/bug <编号> — 查询 Gentoo Bugzilla\n" +
		"/news [关键词] — 查看/搜索 Gentoo 新闻\n" +
		"/wiki <关键词> — 搜索 Gentoo / Arch wiki(含中文页)\n" +
		"/bbs <关键词> — 搜各大 Linux 论坛(中文优先)\n" +
		"/pkgs <包名> — 跨发行版查包版本(Gentoo/Debian/Ubuntu/Fedora/Arch/openSUSE 等)\n" +
		"/distro <包名> — /pkgs 的别名\n" +
		"/arm <包名> — 查该包的 arm64 (aarch64) Gentoo keyword 状态\n" +
		"/armpkgs <包名> — 跨发行版查 arm64 支持\n" +
		"/ping — 机器人状态 / 运行时长\n" +
		"/stats — 今日通过 / 拒绝人数\n" +
		"/help — 显示本帮助"
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
	inGroup := v.cfg.IsGroup(chatID)
	l := v.requesterLanguage(msg)
	help := memberHelpText(l)
	if inGroup {
		help += "\n\n入群验证:" + v.stateText(l, chatID)
	}
	if inGroup && v.isGroupAdminCached(c, bot, chatID, msg.From.ID) {
		help += "\n\n👮 管理员(回复某条消息使用):\n" +
			"/mute — 禁言(留群不能发言,到期自动解除);默认1h,可 /mute 30m\n" +
			"/unmute — 解除禁言\n" +
			"/ban — 封禁(踢出群,仅删该条消息)\n" +
			"/sb — 封禁并清空消息(踢出群 + 清除其全部消息)\n" +
			fmt.Sprintf("/warn — 警告(满 %d 次自动踢出);/clearwarn — 清除警告\n", v.cfg.WarnLimit) +
			"\n其它管理指令:\n" +
			"/bantime — 设定封禁时长(0=永久;如 7d/12h/30m)\n" +
			"/autodel — 查询结果自动删除开关(/autodel on|off|<分钟>)\n" +
			"/rich — 开关富文本输出(/pkg /use)\n" +
			"/spoiler — 开关:遮盖新成员名字(防广告;默认开)\n" +
			"/vmode — 切换入群验证方式(kernel 填内核版本号 / quiz 选择题 / mixed 随机)\n" +
			"/bc — 频道身份发言封禁开关;/bc allow|deny <频道id> 管理白名单\n" +
			"/start /stop — 开启 / 关闭入群验证"
	}
	if inGroup {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: msg.MessageID})
		v.notify(c, bot, chatID, help)
		return nil
	}
	help += "\n\n(以上查询命令私聊也能用,每分钟限次;审核/管理命令仅在群里有效。)"
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
	if v.cfg.IsGroup(groupID) {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(groupID), MessageID: msg.MessageID})
		v.notify(c, bot, groupID, fn(groupID, l))
		return nil
	}
	_, _ = bot.SendMessage(c, tu.Message(tu.ID(groupID), fn(v.verifier.ControlGroupID(), l)))
	return nil
}

func (v *Panel) notifySettingsFailure(c context.Context, bot *telego.Bot, groupID int64, _ i18n.Lang, err error) {
	log.Printf("settings command in group %d failed: %v", groupID, err)
	v.notify(c, bot, groupID, "❌ 无法保存设置,请稍后重试。")
}

func (v *Panel) settingsAdminCmd(ctx *th.Context, update telego.Update, fn func(groupID int64, l i18n.Lang) (string, error)) error {
	return v.runSettingsAdminCmd(ctx, update, false, fn)
}

func (v *Panel) globalSettingsAdminCmd(ctx *th.Context, update telego.Update, fn func(groupID int64, l i18n.Lang) (string, error)) error {
	return v.runSettingsAdminCmd(ctx, update, true, fn)
}

func (v *Panel) runSettingsAdminCmd(ctx *th.Context, update telego.Update, controlGroupOnly bool, fn func(groupID int64, l i18n.Lang) (string, error)) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !v.cfg.IsGroup(msg.Chat.ID) {
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
		if allowed, refusal := v.cfg.ControlGroupAllowed(groupID); !allowed {
			v.notify(c, bot, groupID, refusal)
			return nil
		}
	}
	if !v.isGroupAdmin(c, bot, groupID, msg.From.ID) {
		v.notify(c, bot, groupID, "⛔ 该命令仅群管理员可用。")
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
