package moderate

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
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

// MemberLookup reads Telegram chat and membership records for startup permission diagnostics.
type MemberLookup interface {
	GetChat(ctx context.Context, params *telego.GetChatParams) (*telego.ChatFullInfo, error)
	GetChatMember(ctx context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error)
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
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

// SetupReport is one complete startup permission result for a guarded group.
type SetupReport struct {
	GroupID int64
	Ready   bool
	Text    string
}

// CheckGroupSetup checks every Telegram capability required by one guarded group.
func (s *Service) CheckGroupSetup(ctx context.Context, bot MemberLookup, selfID, groupID int64) SetupReport {
	l := s.groupLanguage(groupID)
	messages := i18n.Messages.Moderate.Setup
	title := strconv.FormatInt(groupID, 10)
	var missing []string
	if chat, err := bot.GetChat(ctx, &telego.GetChatParams{ChatID: tu.ID(groupID)}); err != nil || chat == nil {
		missing = append(missing, messages.GroupAccess.For(l))
	} else if chat.Title != "" {
		title = chat.Title
	}

	missingAllGroupRights := func() {
		missing = append(missing,
			messages.GroupAdmin.For(l),
			messages.ApproveJoinRequests.For(l),
			messages.BanUsers.For(l),
			messages.DeleteMessages.For(l),
		)
	}
	member, err := bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(groupID), UserID: selfID})
	if err != nil {
		missingAllGroupRights()
	} else {
		switch typed := member.(type) {
		case *telego.ChatMemberOwner:
		case *telego.ChatMemberAdministrator:
			if !typed.CanInviteUsers {
				missing = append(missing, messages.ApproveJoinRequests.For(l))
			}
			if !typed.CanRestrictMembers {
				missing = append(missing, messages.BanUsers.For(l))
			}
			if !typed.CanDeleteMessages {
				missing = append(missing, messages.DeleteMessages.For(l))
			}
		default:
			missingAllGroupRights()
		}
	}

	group, ok := s.settings.Group(groupID)
	if ok {
		channelID := group.RequiredChannelID().Value
		if channelID != 0 {
			channelTitle := strconv.FormatInt(channelID, 10)
			if channel, channelErr := bot.GetChat(ctx, &telego.GetChatParams{ChatID: tu.ID(channelID)}); channelErr == nil && channel != nil && channel.Title != "" {
				channelTitle = channel.Title
			}
			channelMember, channelErr := bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(channelID), UserID: selfID})
			status := ""
			if channelMember != nil {
				status = channelMember.MemberStatus()
			}
			if channelErr != nil || (status != telego.MemberStatusCreator && status != telego.MemberStatusAdministrator) {
				missing = append(missing, messages.ChannelAdmin.Render(l, channelTitle, channelID))
			}
		}
	}

	if len(missing) == 0 {
		return SetupReport{GroupID: groupID, Ready: true, Text: messages.Ready.Render(l, title, groupID)}
	}
	text := messages.MissingHeader.Render(l, title, groupID) + "\n- " +
		strings.Join(missing, "\n- ") + "\n" + messages.Recheck.For(l)
	return SetupReport{GroupID: groupID, Text: text}
}

// LogGroupSetup emits and delivers one actionable setup report for a guarded group.
func (s *Service) LogGroupSetup(ctx context.Context, bot MemberLookup, selfID, groupID int64) {
	report := s.CheckGroupSetup(ctx, bot, selfID, groupID)
	deliveredTo := int64(0)
	var deliveryErr error
	if !report.Ready {
		registrantID := int64(0)
		for _, group := range s.settings.Registrations().RegisteredGroups {
			if group.ID == groupID {
				registrantID = group.RegisteredBy
				break
			}
		}
		targets := []int64{registrantID, s.cfg.AdminLogChatID, groupID}
		seen := make(map[int64]bool, len(targets))
		for _, target := range targets {
			if target == 0 || seen[target] {
				continue
			}
			seen[target] = true
			if _, err := bot.SendMessage(ctx, tu.Message(tu.ID(target), report.Text)); err == nil {
				deliveredTo = target
				deliveryErr = nil
				break
			} else {
				deliveryErr = err
			}
		}
	}
	log.Printf("group setup: group=%d ready=%t delivered_to=%d delivery_error=%v report=%q",
		groupID, report.Ready, deliveredTo, deliveryErr, report.Text)
}

// LogGroupAdmin emits and delivers exactly one actionable setup report per guarded group.
func (s *Service) LogGroupAdmin(ctx context.Context, bot MemberLookup, selfID int64) {
	for _, groupID := range s.settings.GroupIDs() {
		s.LogGroupSetup(ctx, bot, selfID, groupID)
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

func (s *Service) warnPrecheck(ctx context.Context, msg *telego.Message, command string, checkTargetAdmin bool, l i18n.Lang) *telego.User {
	groupID := msg.Chat.ID
	if !s.isGroupAdmin(ctx, groupID, msg.From.ID) {
		s.notify(ctx, groupID, i18n.Messages.Moderate.Common.CommandAdminOnly.Render(l, command))
		return nil
	}
	if msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil {
		s.notify(ctx, groupID, i18n.Messages.Moderate.Common.ReplyUsage.Render(l, command))
		return nil
	}
	target := msg.ReplyToMessage.From
	if checkTargetAdmin {
		isAdmin, err := s.telegram.CachedAdmin(ctx, groupID, target.ID)
		if err != nil {
			s.notify(ctx, groupID, i18n.Messages.Moderate.Common.TargetAdminCheckFailed.For(l))
			return nil
		}
		if isAdmin {
			s.notify(ctx, groupID, i18n.Messages.Moderate.Common.TargetIsAdmin.For(l))
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
			s.notify(requestCtx, groupID, i18n.Messages.Moderate.Warning.LimitKickFailed.For(l))
			// A failed limit kick must reach admins even without a configured admin log.
			s.telegram.FailAlert(requestCtx, s.cfg.AdminLogChatID, groupID,
				i18n.Messages.Moderate.Warning.LimitKickAlert.Render(l, displayName(target), limit, displayName(msg.From)))
			return nil
		}
		s.warnings.clear(groupID, target.ID)
		s.warnings.save()
		outcome := i18n.Messages.Moderate.Warning.KickRejoinable.For(l)
		if !rejoinable {
			outcome = i18n.Messages.Moderate.Warning.KickUnbanFailed.For(l)
		}
		s.notify(requestCtx, groupID, i18n.Messages.Moderate.Warning.LimitReached.Render(l, displayName(target), limit, outcome, displayName(msg.From)))
		s.telegram.Alert(requestCtx, s.cfg.AdminLogChatID,
			i18n.Messages.Moderate.Warning.KickAlert.Render(l, groupID, target.ID, displayName(target), displayName(msg.From)))
		log.Printf("/warn-kick user=%d group=%d by=%d", target.ID, groupID, msg.From.ID)
		return nil
	}
	s.notify(requestCtx, groupID, i18n.Messages.Moderate.Warning.Issued.Render(l, displayName(target), count, limit, limit, displayName(msg.From)))
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
	s.notify(requestCtx, groupID, i18n.Messages.Moderate.Warning.Cleared.Render(l, displayName(target), previous, displayName(msg.From)))
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
		s.notify(requestCtx, groupID, i18n.Messages.Moderate.Ban.Failed.For(l))
		s.telegram.FailAlert(requestCtx, s.cfg.AdminLogChatID, groupID,
			i18n.Messages.Moderate.Ban.FailureAlert.Render(l, command, groupID, target.ID, displayName(target), displayName(msg.From)))
		return nil
	}
	s.telegram.Delete(requestCtx, groupID, msg.ReplyToMessage.MessageID)
	verb := i18n.Messages.Moderate.Ban.Verb.For(l)
	if command == "/sb" {
		verb = i18n.Messages.Moderate.Ban.PurgeVerb.For(l)
	}
	action := i18n.Messages.Moderate.Ban.Action.Render(l, verb, banDurationStatus(l, seconds))
	s.notify(requestCtx, groupID, i18n.Messages.Moderate.Ban.Applied.Render(l, action, displayName(target), target.ID, displayName(msg.From)))
	s.telegram.Alert(requestCtx, s.cfg.AdminLogChatID,
		i18n.Messages.Moderate.Ban.Alert.Render(l, command, action, groupID, target.ID, displayName(target), displayName(msg.From)))
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
			s.notify(requestCtx, groupID, i18n.Messages.Moderate.Mute.Usage.Render(l, banDurationStatus(l, s.cfg.MuteSeconds)))
			return nil
		}
		seconds = parsed
	}
	// Delete the offending message only after the restriction succeeds.
	if err := s.telegram.Mute(requestCtx, groupID, target.ID, seconds); err != nil {
		log.Printf("/mute user=%d in %d: %v", target.ID, groupID, err)
		s.notify(requestCtx, groupID, i18n.Messages.Moderate.Mute.Failed.For(l))
		return nil
	}
	s.telegram.Delete(requestCtx, groupID, msg.ReplyToMessage.MessageID)
	s.notify(requestCtx, groupID, i18n.Messages.Moderate.Mute.Applied.Render(l,
		displayName(target), target.ID, banDurationStatus(l, seconds), displayName(msg.From)))
	s.telegram.Alert(requestCtx, s.cfg.AdminLogChatID,
		i18n.Messages.Moderate.Mute.Alert.Render(l, banDurationStatus(l, seconds), groupID, target.ID, displayName(target), displayName(msg.From)))
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
		s.notify(requestCtx, groupID, i18n.Messages.Moderate.Mute.UnmuteFailed.For(l))
		return nil
	}
	s.notify(requestCtx, groupID, i18n.Messages.Moderate.Mute.Unmuted.Render(l, displayName(target), target.ID, displayName(msg.From)))
	log.Printf("/unmute by admin=%d target=%d group=%d", msg.From.ID, target.ID, groupID)
	return nil
}

// OnBanTime handles the group-specific /bantime policy command.
func (s *Service) OnBanTime(ctx *th.Context, update telego.Update) error {
	return s.runSettingsAdminCommand(ctx, update, func(groupID int64, l i18n.Lang) (string, error) {
		arg := strings.ToLower(strings.TrimSpace(commandArg(update.Message.Text)))
		usage := i18n.Messages.Moderate.BanTime.Usage.For(l)
		if arg == "" {
			seconds := s.banDuration(groupID)
			kind := i18n.Messages.Moderate.BanTime.PermanentDescription.For(l)
			if seconds > 0 {
				kind = i18n.Messages.Moderate.BanTime.TemporaryDescription.For(l)
			}
			return i18n.Messages.Moderate.BanTime.Current.Render(l, banDurationStatus(l, seconds), kind, usage), nil
		}
		seconds, ok := parseBanDuration(arg)
		if !ok {
			return usage, nil
		}
		if err := s.setBanDuration(groupID, seconds); err != nil {
			return "", err
		}
		kind := i18n.Messages.Moderate.BanTime.PermanentDescription.For(l)
		if seconds > 0 {
			kind = i18n.Messages.Moderate.BanTime.TemporaryDescription.For(l)
		}
		return i18n.Messages.Moderate.BanTime.Set.Render(l, banDurationStatus(l, seconds), kind), nil
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
		s.notify(requestCtx, groupID, i18n.Messages.Moderate.Common.AdminOnly.For(l))
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

func (s *Service) notifySettingsFailure(ctx context.Context, groupID int64, l i18n.Lang, err error) {
	log.Printf("moderation settings command in group %d failed: %v", groupID, err)
	s.notify(ctx, groupID, i18n.Messages.Moderate.Common.SettingsSaveFailed.For(l))
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
	case "0", "perm", "permanent", i18n.Messages.Moderate.Duration.PermanentInput.For(i18n.LangZH):
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

func banDurationText(l i18n.Lang, seconds int) string {
	if seconds <= 0 {
		return i18n.Messages.Moderate.Duration.Permanent.For(l)
	}
	switch {
	case seconds%86400 == 0:
		return i18n.Messages.Moderate.Duration.Days.Render(l, seconds/86400)
	case seconds%3600 == 0:
		return i18n.Messages.Moderate.Duration.Hours.Render(l, seconds/3600)
	case seconds%60 == 0:
		return i18n.Messages.Moderate.Duration.Minutes.Render(l, seconds/60)
	default:
		return i18n.Messages.Moderate.Duration.Seconds.Render(l, seconds)
	}
}

func banDurationStatus(l i18n.Lang, seconds int) string {
	effect := i18n.Messages.Moderate.Duration.PermanentEffect.For(l)
	if seconds > 0 {
		effect = i18n.Messages.Moderate.Duration.TemporaryEffect.For(l)
	}
	return i18n.Messages.Moderate.Duration.Status.Render(l, banDurationText(l, seconds), effect)
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
