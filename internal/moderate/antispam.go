package moderate

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

func (s *Service) antispamEnabled(groupID int64) bool {
	group, ok := s.settings.Group(groupID)
	return ok && group.AntispamEnabled().Value
}

func (s *Service) channelWhitelisted(groupID, senderID int64) bool {
	group, ok := s.settings.Group(groupID)
	if !ok {
		return false
	}
	for _, allowedID := range group.ChannelWhitelist().Value {
		if allowedID == senderID {
			return true
		}
	}
	return false
}

func (s *Service) isKnownChat(chatID int64) bool {
	if s.settings.IsGroup(chatID) || s.adminLogChatID() == chatID {
		return true
	}
	for _, feed := range s.cfg.Feeds {
		if feed.ChatID == chatID {
			return true
		}
	}
	for _, groupID := range s.settings.GroupIDs() {
		group, _ := s.settings.Group(groupID)
		requiredChannelID := group.Baseline().RequiredChannelID.Value
		if override := group.Overrides().RequiredChannelID; override != nil {
			requiredChannelID = *override
		}
		if requiredChannelID == chatID {
			return true
		}
		for _, knownID := range group.KnownChatIDs().Value {
			if knownID == chatID {
				return true
			}
		}
		for _, trustedID := range group.TrustedMemberGroupIDs().Value {
			if trustedID == chatID {
				return true
			}
		}
	}
	return false
}

func (s *Service) toggleAntispam(groupID int64) (bool, error) {
	group, ok := s.settings.Group(groupID)
	if !ok {
		return false, fmt.Errorf("%w: %d", store.ErrUnknownGroup, groupID)
	}
	enabled := !group.AntispamEnabled().Value
	overrides := group.Overrides()
	overrides.AntispamEnabled = &enabled
	_, err := s.settings.CommitGroup(groupID, group.Revision(), overrides)
	return enabled, err
}

func nextChannelWhitelist(current []int64, senderID int64, allow bool) []int64 {
	for index, existingID := range current {
		if existingID != senderID {
			continue
		}
		if allow {
			return current
		}
		copy(current[index:], current[index+1:])
		return current[:len(current)-1]
	}
	if !allow {
		return current
	}
	if len(current) >= channelWhitelistMax {
		drop := len(current) - channelWhitelistMax + 1
		copy(current, current[drop:])
		current = current[:len(current)-drop]
	}
	return append(current, senderID)
}

// UpdateChannelWhitelist applies the shared whitelist bound and unbans newly allowed senders.
func (s *Service) UpdateChannelWhitelist(ctx context.Context, groupID, senderID int64, allow bool) (unbanErr, updateErr error) {
	group, ok := s.settings.Group(groupID)
	if !ok {
		return nil, fmt.Errorf("%w: %d", store.ErrUnknownGroup, groupID)
	}
	whitelist := nextChannelWhitelist(group.ChannelWhitelist().Value, senderID, allow)
	overrides := group.Overrides()
	overrides.ChannelWhitelist = &whitelist
	if _, err := s.settings.CommitGroup(groupID, group.Revision(), overrides); err != nil {
		return nil, err
	}
	if allow {
		return s.telegram.UnbanSenderChat(ctx, groupID, senderID), nil
	}
	return nil, nil
}

func channelSenderAlert(l i18n.Lang, banned bool, title string, senderID, groupID int64) string {
	if banned {
		return i18n.Messages.Moderate.Antispam.SenderBannedAlert.Render(l, title, senderID, groupID, senderID)
	}
	return i18n.Messages.Moderate.Antispam.SenderBanFailedAlert.Render(l, title, senderID, groupID)
}

// FilterChannelSenders drops untrusted sender-channel posts when BotFather privacy mode is disabled.
func (s *Service) FilterChannelSenders(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg != nil && s.settings.IsGroup(msg.Chat.ID) && s.antispamEnabled(msg.Chat.ID) {
		if sender := msg.SenderChat; sender != nil &&
			sender.ID != msg.Chat.ID && // Anonymous group admins post as the group itself.
			!msg.IsAutomaticForward && // Telegram auto-forwards linked discussion-channel posts.
			!s.isKnownChat(sender.ID) &&
			!s.channelWhitelisted(msg.Chat.ID, sender.ID) {
			requestCtx := ctx.Context()
			l := s.groupLanguage(msg.Chat.ID)
			// A discussion group's own channel replying to a comment is not an impersonator.
			linked, linkKnown := s.telegram.LinkedChat(requestCtx, msg.Chat.ID)
			if linkKnown && linked != 0 && linked == sender.ID {
				return ctx.Next(update)
			}
			s.telegram.Delete(requestCtx, msg.Chat.ID, msg.MessageID)
			banned := false
			if !linkKnown {
				// A sender-chat ban is permanent and only an administrator can lift it. Never
				// impose one on a guess: delete the advert, and leave the ban for a reading that
				// can rule out this group's own channel.
				log.Printf("antispam: linked channel for %d unknown; deleted message from %d without banning", msg.Chat.ID, sender.ID)
			} else if err := s.telegram.BanSenderChat(requestCtx, msg.Chat.ID, sender.ID); err != nil {
				log.Printf("antispam: ban sender_chat %d in %d: %v", sender.ID, msg.Chat.ID, err)
			} else {
				banned = true
			}
			s.telegram.Alert(requestCtx, s.adminLogChatID(),
				channelSenderAlert(l, banned, sender.Title, sender.ID, msg.Chat.ID))
			log.Printf("antispam: channel sender %d (%q) in group %d deleted, banned=%v", sender.ID, sender.Title, msg.Chat.ID, banned)
			return nil
		}
	}
	return ctx.Next(update)
}

// OnBC handles per-group channel-sender filtering and whitelist changes.
func (s *Service) OnBC(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !s.settings.IsGroup(msg.Chat.ID) {
		return nil
	}
	requestCtx := ctx.Context()
	groupID := msg.Chat.ID
	l := s.groupLanguage(groupID)
	defer s.telegram.Delete(requestCtx, groupID, msg.MessageID)
	if admin, err := s.isGroupAdmin(requestCtx, groupID, msg.From.ID); !admin {
		s.notify(requestCtx, groupID, callerRefusal(l, err, i18n.Messages.Moderate.Common.CommandAdminOnly.Render(l, "/bc")))
		return nil
	}

	fields := strings.Fields(commandArg(msg.Text))
	switch {
	case len(fields) == 0:
		enabled, err := s.toggleAntispam(groupID)
		if err != nil {
			s.notifySettingsFailure(requestCtx, groupID, l, err)
			return nil
		}
		if enabled {
			s.notify(requestCtx, groupID, i18n.Messages.Moderate.Antispam.Enabled.For(l))
		} else {
			s.notify(requestCtx, groupID, i18n.Messages.Moderate.Antispam.Disabled.For(l))
		}
	case (fields[0] == "allow" || fields[0] == "deny") && len(fields) >= 2:
		senderID, ok := parseChannelID(fields[1])
		if !ok {
			s.notify(requestCtx, groupID, i18n.Messages.Moderate.Antispam.InvalidChannelID.For(l))
			return nil
		}
		allow := fields[0] == "allow"
		unbanErr, err := s.UpdateChannelWhitelist(requestCtx, groupID, senderID, allow)
		if err != nil {
			s.notifySettingsFailure(requestCtx, groupID, l, err)
			return nil
		}
		if !allow {
			s.notify(requestCtx, groupID, i18n.Messages.Moderate.Antispam.Removed.Render(l, senderID))
			return nil
		}
		if unbanErr != nil {
			log.Printf("/bc allow: unban sender_chat %d in %d: %v", senderID, groupID, unbanErr)
			s.notify(requestCtx, groupID, i18n.Messages.Moderate.Antispam.AllowedUnbanFailed.Render(l, senderID))
			return nil
		}
		s.notify(requestCtx, groupID, i18n.Messages.Moderate.Antispam.Allowed.Render(l, senderID))
	default:
		s.notify(requestCtx, groupID, i18n.Messages.Moderate.Antispam.Usage.For(l))
	}
	return nil
}

// parseChannelID canonicalizes Bot API -100 IDs and bare t.me/c IDs.
func parseChannelID(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false
	}
	if id < 0 {
		return id, true
	}
	full, err := strconv.ParseInt("-100"+value, 10, 64)
	if err != nil {
		return 0, false
	}
	return full, true
}
