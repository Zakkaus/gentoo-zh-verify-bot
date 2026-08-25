package tg

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

const (
	adminCacheTTL   = 30 * time.Second
	adminCacheMax   = 4096
	cleanupTimerMax = 256
)

type adminKey struct {
	chatID int64
	userID int64
}

// Client provides Telegram transport and authorization mechanics over one bot.
type Client struct {
	bot *telego.Bot

	adminMu    sync.Mutex
	adminCache map[adminKey]time.Time

	cleanupTimers atomic.Int32
}

// New wraps bot with transport helpers and a bounded positive admin cache.
func New(bot *telego.Bot) *Client {
	return &Client{bot: bot, adminCache: make(map[adminKey]time.Time)}
}

// HTMLMessage builds the standard outbound HTML message with link previews disabled.
func HTMLMessage(chatID int64, text string) *telego.SendMessageParams {
	return tu.Message(tu.ID(chatID), text).
		WithParseMode(telego.ModeHTML).
		WithLinkPreviewOptions(&telego.LinkPreviewOptions{IsDisabled: true})
}

// ReplyParameters binds a response to msgID and returns nil for an unbound response.
func ReplyParameters(msgID int) *telego.ReplyParameters {
	if msgID == 0 {
		return nil
	}
	return &telego.ReplyParameters{MessageID: msgID}
}

// MessageID returns a sent message ID or zero when Telegram returned no message.
func MessageID(message *telego.Message) int {
	if message == nil {
		return 0
	}
	return message.MessageID
}

// ReplyPlain sends reply-bound plain text and schedules optional group cleanup.
func (c *Client) ReplyPlain(ctx context.Context, chatID int64, replyTo int, text string, cleanupAfter time.Duration) {
	params := tu.Message(tu.ID(chatID), text)
	if reply := ReplyParameters(replyTo); reply != nil {
		params = params.WithReplyParameters(reply)
	}
	sent, _ := c.bot.SendMessage(ctx, params)
	c.ScheduleCleanup(chatID, replyTo, MessageID(sent), cleanupAfter)
}

// ReplyHTML sends reply-bound HTML and schedules optional group cleanup.
func (c *Client) ReplyHTML(ctx context.Context, chatID int64, replyTo int, text string, cleanupAfter time.Duration) *telego.Message {
	params := HTMLMessage(chatID, text)
	if reply := ReplyParameters(replyTo); reply != nil {
		params = params.WithReplyParameters(reply)
	}
	sent, _ := c.bot.SendMessage(ctx, params)
	c.ScheduleCleanup(chatID, replyTo, MessageID(sent), cleanupAfter)
	return sent
}

// SendHTMLFallback sends verification HTML, retries only rejected markup, and returns the sent message.
func (c *Client) SendHTMLFallback(ctx context.Context, chatID int64, rich, simpler string) (*telego.Message, error) {
	sent, err := c.bot.SendMessage(ctx, HTMLMessage(chatID, rich))
	if err == nil {
		return sent, nil
	}
	if !MarkupRejected(err) {
		// Do not retry transient failures: the first request may have landed despite the error.
		return nil, err
	}
	log.Printf("verify DM to %d rejected (%v) — retrying without the collapsed quote", chatID, err)
	if simpler != "" && simpler != rich {
		sent, err = c.bot.SendMessage(ctx, HTMLMessage(chatID, simpler))
		if err == nil {
			return sent, nil
		}
		if !MarkupRejected(err) {
			return nil, err
		}
	}
	sent, err = c.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), stripHTML(simpler)))
	if err != nil {
		log.Printf("verify DM to %d failed even as plain text: %v", chatID, err)
		return nil, err
	}
	return sent, nil
}

// SendPrivateHTMLFallback retries an HTML private reply as plain text after any send failure.
func (c *Client) SendPrivateHTMLFallback(ctx context.Context, chatID int64, text string) {
	if _, err := c.bot.SendMessage(ctx, HTMLMessage(chatID, text)); err != nil {
		log.Printf("private_reply HTML send failed (%v); retrying as plain text", err)
		_, _ = c.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), text))
	}
}

// SendRichOrHTML sends rich HTML when enabled and falls back to ordinary HTML on rejection.
func (c *Client) SendRichOrHTML(ctx context.Context, chatID int64, replyTo int, richHTML, plainHTML string, rich bool, cleanupAfter time.Duration) {
	reply := ReplyParameters(replyTo)
	if rich && richHTML != "" {
		params := (&telego.SendRichMessageParams{}).
			WithChatID(tu.ID(chatID)).
			WithRichMessage(*(&telego.InputRichMessage{}).WithHTML(richHTML).WithSkipEntityDetection())
		if reply != nil {
			params = params.WithReplyParameters(reply)
		}
		if sent, err := c.bot.SendRichMessage(ctx, params); err == nil {
			c.ScheduleCleanup(chatID, replyTo, MessageID(sent), cleanupAfter)
			return
		}
	}
	params := HTMLMessage(chatID, plainHTML)
	if reply != nil {
		params = params.WithReplyParameters(reply)
	}
	sent, _ := c.bot.SendMessage(ctx, params)
	c.ScheduleCleanup(chatID, replyTo, MessageID(sent), cleanupAfter)
}

// ScheduleCleanup deletes a group response and its command after cleanupAfter.
func (c *Client) ScheduleCleanup(chatID int64, commandMessageID, responseMessageID int, cleanupAfter time.Duration) {
	if cleanupAfter <= 0 || responseMessageID == 0 || chatID >= 0 {
		return
	}
	c.scheduleDelete(chatID, responseMessageID, commandMessageID, cleanupAfter)
}

// Delete removes one message and treats a zero message ID as a no-op.
func (c *Client) Delete(ctx context.Context, chatID int64, messageID int) {
	if messageID != 0 {
		_ = c.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: messageID})
	}
}

// Notify sends a transient plain-text notice and bounds outstanding deletion timers.
func (c *Client) Notify(ctx context.Context, chatID int64, text string, ttlSeconds int) {
	message, err := c.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), text))
	if err != nil || message == nil || ttlSeconds < 0 {
		return
	}
	duration, ok := config.SecondsToDuration(ttlSeconds)
	if !ok {
		return
	}
	c.scheduleDelete(chatID, message.MessageID, 0, duration)
}

// Alert sends an operator alert when an admin-log chat is configured.
func (c *Client) Alert(ctx context.Context, adminLogChatID int64, text string) {
	if adminLogChatID == 0 {
		return
	}
	if _, err := c.bot.SendMessage(ctx, tu.Message(tu.ID(adminLogChatID), text)); err != nil {
		log.Printf("adminAlert to %d failed (check admin_log_chat_id / bot membership): %v", adminLogChatID, err)
	}
}

// FailAlert sends a failure alert to the admin log or falls back to the affected group.
func (c *Client) FailAlert(ctx context.Context, adminLogChatID, groupID int64, text string) {
	target := adminLogChatID
	if target == 0 {
		target = groupID
	}
	if _, err := c.bot.SendMessage(ctx, tu.Message(tu.ID(target), text)); err != nil {
		log.Printf("failAlert to %d failed: %v", target, err)
	}
}

// CachedAdmin returns cached positive status or performs a Telegram membership lookup.
func (c *Client) CachedAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	key := adminKey{chatID: chatID, userID: userID}
	c.adminMu.Lock()
	expires, cached := c.adminCache[key]
	c.adminMu.Unlock()
	if cached && time.Now().Before(expires) {
		return true, nil
	}
	return c.fetchAdmin(ctx, key)
}

// FreshAdmin bypasses the positive cache for destructive authorization checks.
func (c *Client) FreshAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	return c.fetchAdmin(ctx, adminKey{chatID: chatID, userID: userID})
}

// MissingModRights reports missing invite, restrict, and delete capabilities for an administrator.
func MissingModRights(member telego.ChatMember) []string {
	admin, ok := member.(*telego.ChatMemberAdministrator)
	if !ok {
		return nil
	}
	var missing []string
	if !admin.CanInviteUsers {
		missing = append(missing, "approve members (can_invite_users)")
	}
	if !admin.CanRestrictMembers {
		missing = append(missing, "ban/restrict (can_restrict_members)")
	}
	if !admin.CanDeleteMessages {
		missing = append(missing, "delete messages (can_delete_messages)")
	}
	return missing
}

// Ban bans a member permanently or until the requested duration elapses.
func (c *Client) Ban(ctx context.Context, chatID, userID int64, seconds int, revokeMessages bool) error {
	params := &telego.BanChatMemberParams{ChatID: tu.ID(chatID), UserID: userID, RevokeMessages: revokeMessages}
	if seconds > 0 {
		duration, ok := config.SecondsToDuration(seconds)
		if !ok {
			return errors.New("ban duration seconds exceed time.Duration")
		}
		params.UntilDate = time.Now().Add(duration).Unix()
	}
	return c.bot.BanChatMember(ctx, params)
}

// Unban removes a member ban and optionally requires that the member is currently banned.
func (c *Client) Unban(ctx context.Context, chatID, userID int64, onlyIfBanned bool) error {
	return c.bot.UnbanChatMember(ctx, &telego.UnbanChatMemberParams{
		ChatID:       tu.ID(chatID),
		UserID:       userID,
		OnlyIfBanned: onlyIfBanned,
	})
}

// Mute restricts all member permissions until the requested duration elapses.
func (c *Client) Mute(ctx context.Context, chatID, userID int64, seconds int) error {
	duration, ok := config.SecondsToDuration(seconds)
	if !ok {
		return errors.New("mute duration seconds exceed time.Duration")
	}
	return c.bot.RestrictChatMember(ctx, &telego.RestrictChatMemberParams{
		ChatID:      tu.ID(chatID),
		UserID:      userID,
		Permissions: telego.ChatPermissions{},
		UntilDate:   time.Now().Add(duration).Unix(),
	})
}

// Unmute restores the group's default member permissions.
func (c *Client) Unmute(ctx context.Context, chatID, userID int64) error {
	chat, err := c.bot.GetChat(ctx, &telego.GetChatParams{ChatID: tu.ID(chatID)})
	if err != nil {
		return err
	}
	if chat == nil {
		return errors.New("get chat returned no result")
	}
	if chat.Permissions == nil {
		return errors.New("chat default permissions are unavailable")
	}
	return c.bot.RestrictChatMember(ctx, &telego.RestrictChatMemberParams{
		ChatID:      tu.ID(chatID),
		UserID:      userID,
		Permissions: *chat.Permissions,
	})
}

// BanSenderChat bans messages sent on behalf of a channel in one group.
func (c *Client) BanSenderChat(ctx context.Context, chatID, senderChatID int64) error {
	return c.bot.BanChatSenderChat(ctx, &telego.BanChatSenderChatParams{ChatID: tu.ID(chatID), SenderChatID: senderChatID})
}

// UnbanSenderChat removes a sender-chat ban in one group.
func (c *Client) UnbanSenderChat(ctx context.Context, chatID, senderChatID int64) error {
	return c.bot.UnbanChatSenderChat(ctx, &telego.UnbanChatSenderChatParams{ChatID: tu.ID(chatID), SenderChatID: senderChatID})
}

func (c *Client) fetchAdmin(ctx context.Context, key adminKey) (bool, error) {
	member, err := c.bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(key.chatID), UserID: key.userID})
	if err != nil {
		return false, err
	}
	status := member.MemberStatus()
	isAdmin := status == telego.MemberStatusCreator || status == telego.MemberStatusAdministrator
	c.adminMu.Lock()
	if isAdmin {
		now := time.Now()
		c.adminCache[key] = now.Add(adminCacheTTL)
		if len(c.adminCache) > adminCacheMax {
			c.pruneAdminCacheLocked(now)
		}
	} else {
		delete(c.adminCache, key)
	}
	c.adminMu.Unlock()
	return isAdmin, nil
}

func (c *Client) pruneAdminCacheLocked(now time.Time) {
	for key, expires := range c.adminCache {
		if !now.Before(expires) {
			delete(c.adminCache, key)
		}
	}
	for len(c.adminCache) > adminCacheMax {
		var victim adminKey
		var victimExpiry time.Time
		found := false
		for key, expires := range c.adminCache {
			if !found || expires.Before(victimExpiry) ||
				expires.Equal(victimExpiry) && (key.chatID < victim.chatID || key.chatID == victim.chatID && key.userID < victim.userID) {
				victim, victimExpiry, found = key, expires, true
			}
		}
		delete(c.adminCache, victim)
	}
}

func (c *Client) scheduleDelete(chatID int64, firstMessageID, secondMessageID int, after time.Duration) {
	if !c.reserveCleanupTimer() {
		return
	}
	time.AfterFunc(after, func() {
		defer c.cleanupTimers.Add(-1)
		_ = c.bot.DeleteMessage(context.Background(), &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: firstMessageID})
		if secondMessageID != 0 {
			_ = c.bot.DeleteMessage(context.Background(), &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: secondMessageID})
		}
	})
}

func (c *Client) reserveCleanupTimer() bool {
	for {
		count := c.cleanupTimers.Load()
		if count >= cleanupTimerMax {
			return false
		}
		if c.cleanupTimers.CompareAndSwap(count, count+1) {
			return true
		}
	}
}

func stripHTML(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	depth := 0
	for _, r := range text {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			builder.WriteRune(r)
		}
	}
	plain := builder.String()
	for _, pair := range [][2]string{{"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", "\""}, {"&#39;", "'"}, {"&amp;", "&"}} {
		plain = strings.ReplaceAll(plain, pair[0], pair[1])
	}
	return plain
}
