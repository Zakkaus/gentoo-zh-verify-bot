package moderate

import (
	"context"
	"fmt"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"log"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

const channelWhitelistMax = 4096

// Telegram is the caller-owned transport used for moderation and authorization.
type Telegram interface {
	Delete(ctx context.Context, chatID int64, messageID int)
	Notify(ctx context.Context, chatID int64, text string, ttlSeconds int)
	Alert(ctx context.Context, adminLogChatID int64, text string)
	FailAlert(ctx context.Context, adminLogChatID, groupID int64, text string)
	CachedAdmin(ctx context.Context, chatID, userID int64) (bool, error)
	FreshAdmin(ctx context.Context, chatID, userID int64) (bool, error)
	Ban(ctx context.Context, chatID, userID int64, seconds int, revokeMessages bool) error
	Unban(ctx context.Context, chatID, userID int64, onlyIfBanned bool) error
	Mute(ctx context.Context, chatID, userID int64, seconds int) error
	Unmute(ctx context.Context, chatID, userID int64) error
	BanSenderChat(ctx context.Context, chatID, senderChatID int64) error
	UnbanSenderChat(ctx context.Context, chatID, senderChatID int64) error
}

// MemberLookup reads full Telegram membership records for startup permission diagnostics.
type MemberLookup interface {
	GetChatMember(ctx context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error)
}

// Service owns moderation handlers, policy, and warning state.
type Service struct {
	settings *store.Settings
	telegram Telegram
	cfg      *config.Config
	warnings warningState
}

// New constructs a moderation service and restores warns.json from stateDirectory.
func New(settings *store.Settings, telegram Telegram, cfg *config.Config, stateDirectory string) *Service {
	s := &Service{
		settings: settings,
		telegram: telegram,
		cfg:      cfg,
		warnings: newWarningState(stateDirectory),
	}
	s.warnings.load()
	return s
}

// LogGroupAdmin reports whether the bot has the permissions required for moderation.
func (s *Service) LogGroupAdmin(ctx context.Context, bot MemberLookup, selfID int64) {
	for _, groupID := range s.settings.GroupIDs() {
		switch ok, err := s.telegram.CachedAdmin(ctx, groupID, selfID); {
		case err != nil:
			log.Printf("group %d: cannot read bot membership yet (%v) — verification stays inactive until the bot is added as admin", groupID, err)
		case ok:
			log.Printf("group %d: bot is admin ✓", groupID)
			member, memberErr := bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(groupID), UserID: selfID})
			if memberErr != nil {
				log.Printf("group %d: bot is admin but couldn't read its exact rights (%v)", groupID, memberErr)
				continue
			}
			if missing := tg.MissingModRights(member); len(missing) > 0 {
				log.Printf("group %d: WARNING bot is admin but MISSING rights: %s — those actions will fail until granted", groupID, strings.Join(missing, ", "))
			}
		default:
			log.Printf("group %d: bot is NOT admin — join verification inactive until it's granted admin (approve members / ban / delete)", groupID)
		}
	}
}

func (s *Service) isGroupAdmin(ctx context.Context, chatID, userID int64) bool {
	ok, err := s.telegram.FreshAdmin(ctx, chatID, userID)
	if err != nil {
		log.Printf("isGroupAdmin getChatMember chat=%d user=%d: %v", chatID, userID, err)
		return false
	}
	return ok
}

func (s *Service) notify(ctx context.Context, chatID int64, text string) {
	s.telegram.Notify(ctx, chatID, text, s.cfg.NotifyTTLSeconds)
}
func (s *Service) groupLanguage(groupID int64) i18n.Lang {
	if s.settings != nil {
		if group, ok := s.settings.Group(groupID); ok {
			return i18n.FromStored(group.Lang().Value)
		}
	}
	return i18n.FromStored(s.cfg.LangForGroup(groupID))
}

func (s *Service) warnPrecheck(ctx context.Context, msg *telego.Message, command string, checkTargetAdmin bool, _ i18n.Lang) *telego.User {
	groupID := msg.Chat.ID
	if !s.isGroupAdmin(ctx, groupID, msg.From.ID) {
		s.notify(ctx, groupID, fmt.Sprintf("⛔ %s 只能由群管理员使用。", command))
		return nil
	}
	if msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil {
		s.notify(ctx, groupID, fmt.Sprintf("用法:回复目标用户的消息,再发送 %s。", command))
		return nil
	}
	target := msg.ReplyToMessage.From
	if checkTargetAdmin {
		isAdmin, err := s.telegram.CachedAdmin(ctx, groupID, target.ID)
		if err != nil {
			s.notify(ctx, groupID, "⚠️ 无法确认目标身份,请稍后重试。")
			return nil
		}
		if isAdmin {
			s.notify(ctx, groupID, "目标是管理员,已忽略。")
			return nil
		}
	}
	return target
}

func (s *Service) warnKick(ctx context.Context, groupID, userID int64) (rejoinable bool, err error) {
	if err = s.telegram.Ban(ctx, groupID, userID, 0, false); err != nil {
		return false, err
	}
	if unbanErr := s.telegram.Unban(ctx, groupID, userID, true); unbanErr != nil {
		log.Printf("/warn unban %d in %d: %v", userID, groupID, unbanErr)
		return false, nil
	}
	return true, nil
}

// OnWarn increments the replied user's group-specific warning counter and kicks at the limit.
func (s *Service) OnWarn(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !s.settings.IsGroup(msg.Chat.ID) {
		return nil
	}
	requestCtx := ctx.Context()
	groupID := msg.Chat.ID
	defer s.telegram.Delete(requestCtx, groupID, msg.MessageID)
	l := s.groupLanguage(groupID)
	target := s.warnPrecheck(requestCtx, msg, "/warn", true, l)
	if target == nil {
		return nil
	}
	limit := s.cfg.WarnLimit
	count := s.warnings.increment(groupID, target.ID)
	s.warnings.save() // Persist immediately so a failed at-limit kick survives restart.

	if count >= limit {
		rejoinable, err := s.warnKick(requestCtx, groupID, target.ID)
		if err != nil {
			log.Printf("/warn kick %d in %d: %v", target.ID, groupID, err)
			s.notify(requestCtx, groupID, "⚠️ 已达警告上限,但踢出失败:bot 可能缺少「封禁用户」权限。")
			// A failed limit kick must reach admins even without a configured admin log.
			s.telegram.FailAlert(requestCtx, s.cfg.AdminLogChatID, groupID,
				fmt.Sprintf("⚠️ %s 已达 %d 次警告上限但自动踢出失败,请手动处理(操作人 %s)", displayName(target), limit, displayName(msg.From)))
			return nil
		}
		s.warnings.clear(groupID, target.ID)
		s.warnings.save()
		outcome := "已踢出(可重新申请入群)"
		if !rejoinable {
			outcome = "已封禁,但解封失败(用户暂时无法重新加入),请手动解封"
		}
		s.notify(requestCtx, groupID, fmt.Sprintf("🚫 %s 已达 %d 次警告上限,%s。操作人 %s。", displayName(target), limit, outcome, displayName(msg.From)))
		s.telegram.Alert(requestCtx, s.cfg.AdminLogChatID,
			fmt.Sprintf("warn-kick: 群 %d 目标 %d (%s) 操作人 %s", groupID, target.ID, displayName(target), displayName(msg.From)))
		log.Printf("/warn-kick user=%d group=%d by=%d", target.ID, groupID, msg.From.ID)
		return nil
	}
	s.notify(requestCtx, groupID, fmt.Sprintf("⚠️ 已警告 %s(%d/%d);满 %d 次将自动踢出。操作人 %s。", displayName(target), count, limit, limit, displayName(msg.From)))
	log.Printf("/warn user=%d group=%d count=%d by=%d", target.ID, groupID, count, msg.From.ID)
	return nil
}

// OnClearWarn clears the replied user's warning counter in the current group.
func (s *Service) OnClearWarn(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !s.settings.IsGroup(msg.Chat.ID) {
		return nil
	}
	requestCtx := ctx.Context()
	groupID := msg.Chat.ID
	defer s.telegram.Delete(requestCtx, groupID, msg.MessageID)
	l := s.groupLanguage(groupID)
	target := s.warnPrecheck(requestCtx, msg, "/clearwarn", false, l)
	if target == nil {
		return nil
	}
	previous := s.warnings.clear(groupID, target.ID)
	s.warnings.save()
	s.notify(requestCtx, groupID, fmt.Sprintf("✅ 已清除 %s 的警告(原 %d 次)。操作人 %s。", displayName(target), previous, displayName(msg.From)))
	log.Printf("/clearwarn user=%d group=%d was=%d by=%d", target.ID, groupID, previous, msg.From.ID)
	return nil
}

// OnPurge handles /sb by banning the replied user and purging their messages.
func (s *Service) OnPurge(ctx *th.Context, update telego.Update) error {
	return s.moderate(ctx, update, "/sb")
}

// OnBan handles /ban by banning the replied user and deleting the replied message.
func (s *Service) OnBan(ctx *th.Context, update telego.Update) error {
	return s.moderate(ctx, update, "/ban")
}

// Both ban commands require a fresh admin check and use the group's effective duration.
func (s *Service) moderate(ctx *th.Context, update telego.Update, command string) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !s.settings.IsGroup(msg.Chat.ID) {
		return nil
	}
	requestCtx := ctx.Context()
	groupID := msg.Chat.ID
	defer s.telegram.Delete(requestCtx, groupID, msg.MessageID)
	l := s.groupLanguage(groupID)
	target := s.warnPrecheck(requestCtx, msg, command, true, l)
	if target == nil {
		return nil
	}
	// Ban before deleting, so a permission failure leaves evidence and the user unchanged.
	seconds := s.banDuration(groupID)
	revoke := command == "/sb"
	if err := s.telegram.Ban(requestCtx, groupID, target.ID, seconds, revoke); err != nil {
		log.Printf("%s ban user=%d in %d: %v", command, target.ID, groupID, err)
		s.notify(requestCtx, groupID, "❌ 操作失败:bot 可能缺少「封禁用户」权限。")
		s.telegram.FailAlert(requestCtx, s.cfg.AdminLogChatID, groupID,
			fmt.Sprintf("⚠️ %s 失败:群 %d 目标 %d (%s),操作人 %s,bot 可能缺「封禁」权限", command, groupID, target.ID, displayName(target), displayName(msg.From)))
		return nil
	}
	s.telegram.Delete(requestCtx, groupID, msg.ReplyToMessage.MessageID)
	verb := "封禁"
	if command == "/sb" {
		verb = "封禁并清空(已清除其全部消息)"
	}
	action := fmt.Sprintf("已%s(%s)", verb, banDurationText(l, seconds))
	s.notify(requestCtx, groupID, fmt.Sprintf("✅ %s:%s(id %d),操作人 %s。", action, displayName(target), target.ID, displayName(msg.From)))
	s.telegram.Alert(requestCtx, s.cfg.AdminLogChatID,
		fmt.Sprintf("%s %s: 群 %d 目标 %d (%s) 操作人 %s", command, action, groupID, target.ID, displayName(target), displayName(msg.From)))
	log.Printf("%s by admin=%d target=%d group=%d ban_secs=%d", command, msg.From.ID, target.ID, groupID, seconds)
	return nil
}

// OnMute handles a finite /mute duration, with an optional inline override.
func (s *Service) OnMute(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !s.settings.IsGroup(msg.Chat.ID) {
		return nil
	}
	requestCtx := ctx.Context()
	groupID := msg.Chat.ID
	defer s.telegram.Delete(requestCtx, groupID, msg.MessageID)
	l := s.groupLanguage(groupID)
	target := s.warnPrecheck(requestCtx, msg, "/mute", true, l)
	if target == nil {
		return nil
	}
	seconds := s.cfg.MuteSeconds
	if arg := strings.TrimSpace(commandArg(msg.Text)); arg != "" {
		parsed, ok := parseBanDuration(arg)
		if !ok || parsed <= 0 {
			s.notify(requestCtx, groupID, fmt.Sprintf("用法:/mute(默认禁言 %s),或 /mute 30m、/mute 2h、/mute 12h 指定时长(不支持永久)。", banDurationText(l, s.cfg.MuteSeconds)))
			return nil
		}
		seconds = parsed
	}
	// Delete the offending message only after the restriction succeeds.
	if err := s.telegram.Mute(requestCtx, groupID, target.ID, seconds); err != nil {
		log.Printf("/mute user=%d in %d: %v", target.ID, groupID, err)
		s.notify(requestCtx, groupID, "❌ 禁言失败:bot 可能缺少「封禁/限制成员」权限。")
		return nil
	}
	s.telegram.Delete(requestCtx, groupID, msg.ReplyToMessage.MessageID)
	s.notify(requestCtx, groupID, fmt.Sprintf("🔇 已禁言 %s(id %d),时长 %s,到期自动解除(可 /unmute 提前解除)。操作人 %s。",
		displayName(target), target.ID, banDurationText(l, seconds), displayName(msg.From)))
	s.telegram.Alert(requestCtx, s.cfg.AdminLogChatID,
		fmt.Sprintf("/mute %s: 群 %d 目标 %d (%s) 操作人 %s", banDurationText(l, seconds), groupID, target.ID, displayName(target), displayName(msg.From)))
	log.Printf("/mute by admin=%d target=%d group=%d secs=%d", msg.From.ID, target.ID, groupID, seconds)
	return nil
}

// OnUnmute handles /unmute and fails closed when caller authorization is unavailable.
func (s *Service) OnUnmute(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !s.settings.IsGroup(msg.Chat.ID) {
		return nil
	}
	requestCtx := ctx.Context()
	groupID := msg.Chat.ID
	defer s.telegram.Delete(requestCtx, groupID, msg.MessageID)
	l := s.groupLanguage(groupID)
	target := s.warnPrecheck(requestCtx, msg, "/unmute", false, l)
	if target == nil {
		return nil
	}
	if err := s.telegram.Unmute(requestCtx, groupID, target.ID); err != nil {
		log.Printf("/unmute user=%d in %d: %v", target.ID, groupID, err)
		s.notify(requestCtx, groupID, "❌ 解除禁言失败:bot 可能缺少「封禁/限制成员」权限。")
		return nil
	}
	s.notify(requestCtx, groupID, fmt.Sprintf("🔊 已解除 %s(id %d)的禁言。操作人 %s。", displayName(target), target.ID, displayName(msg.From)))
	log.Printf("/unmute by admin=%d target=%d group=%d", msg.From.ID, target.ID, groupID)
	return nil
}

// OnBanTime handles the group-specific /bantime policy command.
func (s *Service) OnBanTime(ctx *th.Context, update telego.Update) error {
	return s.runSettingsAdminCommand(ctx, update, func(groupID int64, l i18n.Lang) (string, error) {
		arg := strings.ToLower(strings.TrimSpace(commandArg(update.Message.Text)))
		usage := "用法:/bantime 0(永久),或 /bantime 7d、12h、30m、90s"
		if arg == "" {
			seconds := s.banDuration(groupID)
			kind := "永久"
			if seconds > 0 {
				kind = "到期后可重新加入"
			}
			return fmt.Sprintf("⏱ 当前封禁时长:%s(%s)。\n\n%s", banDurationText(l, seconds), kind, usage), nil
		}
		seconds, ok := parseBanDuration(arg)
		if !ok {
			return usage, nil
		}
		if err := s.setBanDuration(groupID, seconds); err != nil {
			return "", err
		}
		kind := "永久封禁"
		if seconds > 0 {
			kind = "到期后可重新加入"
		}
		return fmt.Sprintf("✅ 已设定封禁时长:%s —— %s。\n/ban、/sb 及验证连续失败自动封禁都会使用它。", banDurationText(l, seconds), kind), nil
	})
}

func (s *Service) runSettingsAdminCommand(ctx *th.Context, update telego.Update, run func(groupID int64, l i18n.Lang) (string, error)) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !s.settings.IsGroup(msg.Chat.ID) {
		return nil
	}
	requestCtx := ctx.Context()
	groupID := msg.Chat.ID
	l := s.groupLanguage(groupID)
	defer s.telegram.Delete(requestCtx, groupID, msg.MessageID)
	if !s.isGroupAdmin(requestCtx, groupID, msg.From.ID) {
		s.notify(requestCtx, groupID, "⛔ 该命令仅群管理员可用。")
		return nil
	}
	text, err := run(groupID, l)
	if err != nil {
		s.notifySettingsFailure(requestCtx, groupID, l, err)
		return nil
	}
	s.notify(requestCtx, groupID, text)
	return nil
}

func (s *Service) notifySettingsFailure(ctx context.Context, groupID int64, _ i18n.Lang, err error) {
	log.Printf("moderation settings command in group %d failed: %v", groupID, err)
	s.notify(ctx, groupID, "❌ 无法保存设置,请稍后重试。")
}

func (s *Service) banDuration(groupID int64) int {
	group, _ := s.settings.Group(groupID)
	return group.BanSeconds().Value
}

func (s *Service) setBanDuration(groupID int64, seconds int) error {
	group, ok := s.settings.Group(groupID)
	if !ok {
		return fmt.Errorf("%w: %d", store.ErrUnknownGroup, groupID)
	}
	overrides := group.Overrides()
	overrides.BanSeconds = &seconds
	_, err := s.settings.CommitGroup(groupID, group.Revision(), overrides)
	return err
}

// parseBanDuration accepts permanent, seconds, or s/m/h/d suffixes.
func parseBanDuration(arg string) (seconds int, ok bool) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	switch arg {
	case "":
		return 0, false
	case "0", "perm", "permanent", "永久":
		return 0, true
	}
	multiplier := 1
	switch arg[len(arg)-1] {
	case 's':
		arg = arg[:len(arg)-1]
	case 'm':
		multiplier, arg = 60, arg[:len(arg)-1]
	case 'h':
		multiplier, arg = 3600, arg[:len(arg)-1]
	case 'd':
		multiplier, arg = 86400, arg[:len(arg)-1]
	}
	value, err := strconv.Atoi(arg)
	if err != nil || value < 0 || value > 1<<31 {
		return 0, false
	}
	return config.ClampBanSeconds(value * multiplier), true
}

func banDurationText(_ i18n.Lang, seconds int) string {
	if seconds <= 0 {
		return "永久"
	}
	switch {
	case seconds%86400 == 0:
		return fmt.Sprintf("%d 天", seconds/86400)
	case seconds%3600 == 0:
		return fmt.Sprintf("%d 小时", seconds/3600)
	case seconds%60 == 0:
		return fmt.Sprintf("%d 分钟", seconds/60)
	default:
		return fmt.Sprintf("%d 秒", seconds)
	}
}

func commandArg(text string) string {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return ""
	}
	return strings.Join(fields[1:], " ")
}

func displayName(user *telego.User) string {
	if user.Username != "" {
		return "@" + user.Username
	}
	return user.FirstName
}

func warningsPath(stateDirectory string) string {
	if stateDirectory == "" {
		return ""
	}
	return filepath.Join(stateDirectory, "warns.json")
}
