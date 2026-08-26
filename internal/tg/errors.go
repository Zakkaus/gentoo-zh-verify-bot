package tg

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/mymmrac/telego/telegoapi"
)

// MessageLimit is Telegram's maximum text length in UTF-16 code units.
const MessageLimit = 4096

// MarkupRejected reports errors that may indicate rejected Telegram HTML entities.
func MarkupRejected(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "parse") || strings.Contains(message, "entit") || strings.Contains(message, "bad request")
}

// ErrorCode returns a structured Telegram Bot API error code or zero.
func ErrorCode(err error) int {
	var apiErr *telegoapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode
	}
	return 0
}

// CannotInitiateConversation reports the ordinary 403 returned before a user has started the bot.
func CannotInitiateConversation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "bot can't initiate conversation with a user") {
		return false
	}
	code := ErrorCode(err)
	return code == 0 || code == 403
}

// BotWasBlockedByUser reports the distinct 403 returned after a user blocks the bot.
func BotWasBlockedByUser(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "bot was blocked by the user") {
		return false
	}
	code := ErrorCode(err)
	return code == 0 || code == 403
}

// JoinRequestGone reports a join-request action that Telegram rejected because it no
// longer holds the request: already settled, withdrawn by the applicant, or the user is
// a member by now. Retrying such a call can never succeed.
func JoinRequestGone(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "hide_requester_missing") ||
		strings.Contains(message, "user_already_participant") ||
		strings.Contains(message, "participant_id_invalid")
}

// GroupUnreachable reports a chat the bot can no longer act in at all: it was removed, or the
// chat is gone. Unlike missing rights, this cannot be repaired by retrying — only by an
// administrator putting the bot back.
func GroupUnreachable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "bot is not a member of") ||
		strings.Contains(message, "bot was kicked from") ||
		strings.Contains(message, "chat not found") ||
		strings.Contains(message, "the group chat was deleted")
}

// IsBlocked reports Telegram 403 responses indicating that the bot cannot contact the target.
func IsBlocked(err error) bool {
	if err == nil {
		return false
	}
	if ErrorCode(err) == 403 {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "bot was blocked") || strings.Contains(message, "forbidden: bot")
}

// RetryAfter returns Telegram's requested 429 delay or zero when none is available.
func RetryAfter(err error) time.Duration {
	var apiErr *telegoapi.Error
	if errors.As(err, &apiErr) && apiErr.Parameters != nil && apiErr.Parameters.RetryAfter > 0 {
		return time.Duration(apiErr.Parameters.RetryAfter) * time.Second
	}
	if err == nil {
		return 0
	}
	message := strings.ToLower(err.Error())
	const marker = "retry after"
	index := strings.Index(message, marker)
	if index < 0 {
		return 0
	}
	remainder := strings.TrimLeft(message[index+len(marker):], " \t\r\n:,.;")
	end := 0
	for end < len(remainder) && remainder[end] >= '0' && remainder[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	seconds, parseErr := strconv.Atoi(remainder[:end])
	if parseErr != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// IsNotModified reports an edit that already has the requested text.
func IsNotModified(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "message is not modified")
}

// PermanentEditError reports errors proving that one specific message can never be edited.
func PermanentEditError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "message to edit not found") ||
		strings.Contains(message, "message can't be edited") ||
		strings.Contains(message, "message_id_invalid")
}

// CountablePermanentEditError reports deterministic unclassified 400 edit rejections.
func CountablePermanentEditError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "chat not found") {
		return false
	}
	code := ErrorCode(err)
	return code == 400 || code == 0 && strings.Contains(message, "bad request")
}

// IsRateLimited reports Telegram throttling from a structured 429 or retry-after message.
func IsRateLimited(err error) bool {
	if err == nil {
		return false
	}
	if ErrorCode(err) == 429 {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "too many requests") || strings.Contains(message, "retry after")
}

// PermanentPostError reports deterministic item rejection without treating destination failures as permanent.
func PermanentPostError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "chat not found") ||
		strings.Contains(message, "migrate to chat") ||
		strings.Contains(message, "not enough rights") {
		return false
	}
	code := ErrorCode(err)
	return code == 400 || code == 0 && strings.Contains(message, "bad request")
}

// Pace waits for pause or returns false when ctx is cancelled first.
func Pace(ctx context.Context, pause time.Duration) bool {
	if pause <= 0 {
		return true
	}
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// TextUnits measures Telegram text length in UTF-16 code units.
func TextUnits(text string) int {
	units := 0
	for _, r := range text {
		units += utf16.RuneLen(r)
	}
	return units
}

// CapText truncates text by UTF-16 units and appends an ellipsis when cut.
func CapText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if TextUnits(text) <= limit {
		return text
	}
	target := limit - 1
	var builder strings.Builder
	for _, r := range text {
		units := utf16.RuneLen(r)
		if target < units {
			break
		}
		builder.WriteRune(r)
		target -= units
	}
	builder.WriteRune('…')
	return builder.String()
}
