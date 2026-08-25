package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

// Empty permissions fully mute; Telegram lifts the restriction at UntilDate.
func (v *Verifier) applyMute(c context.Context, bot modBot, gid, uid int64, secs int) error {
	return bot.RestrictChatMember(c, &telego.RestrictChatMemberParams{
		ChatID:      tu.ID(gid),
		UserID:      uid,
		Permissions: telego.ChatPermissions{}, // all false => muted
		UntilDate:   time.Now().Add(time.Duration(secs) * time.Second).Unix(),
	})
}

func unrestrictedChatPermissions() telego.ChatPermissions {
	allowed := true
	return telego.ChatPermissions{
		CanSendMessages:       &allowed,
		CanSendAudios:         &allowed,
		CanSendDocuments:      &allowed,
		CanSendPhotos:         &allowed,
		CanSendVideos:         &allowed,
		CanSendVideoNotes:     &allowed,
		CanSendVoiceNotes:     &allowed,
		CanSendPolls:          &allowed,
		CanSendOtherMessages:  &allowed,
		CanAddWebPagePreviews: &allowed,
		CanReactToMessages:    &allowed,
		CanEditTag:            &allowed,
		CanChangeInfo:         &allowed,
		CanInviteUsers:        &allowed,
		CanPinMessages:        &allowed,
		CanManageTopics:       &allowed,
	}
}

// Group defaults preserve local policy; the explicit full set keeps unmute independent of GetChat.
func (v *Verifier) applyUnmute(c context.Context, bot modBot, gid, uid int64) error {
	permissions := unrestrictedChatPermissions()
	if chat, err := bot.GetChat(c, &telego.GetChatParams{ChatID: tu.ID(gid)}); err == nil && chat != nil && chat.Permissions != nil {
		permissions = *chat.Permissions
	}
	return bot.RestrictChatMember(c, &telego.RestrictChatMemberParams{
		ChatID:      tu.ID(gid),
		UserID:      uid,
		Permissions: permissions,
	})
}

// /mute is always timed; an inline duration overrides the configured default.
func (v *Verifier) onMute(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	gid := msg.Chat.ID
	defer func() {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(gid), MessageID: msg.MessageID})
	}()

	target := v.warnPrecheck(c, bot, msg, "/mute", true) // admin gate + reply target + skip admins
	if target == nil {
		return nil
	}
	secs := v.cfg.MuteSeconds // default (1h unless config overrides)
	if arg := strings.TrimSpace(commandArg(msg.Text)); arg != "" {
		s, ok := parseBanDuration(arg)
		if !ok || s <= 0 { // mute is always timed; reject 0/permanent/garbage
			v.notify(c, bot, gid, fmt.Sprintf("用法:/mute(默认禁言 %s),或 /mute 30m、/mute 2h、/mute 12h 指定时长(不支持永久)。", banDurationText(v.cfg.MuteSeconds)))
			return nil
		}
		secs = s
	}
	// Delete the offending message only after the restriction succeeds.
	if err := v.applyMute(c, bot, gid, target.ID, secs); err != nil {
		log.Printf("/mute user=%d in %d: %v", target.ID, gid, err)
		v.notify(c, bot, gid, "❌ 禁言失败:bot 可能缺少「封禁/限制成员」权限。")
		return nil
	}
	_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(gid), MessageID: msg.ReplyToMessage.MessageID})
	v.notify(c, bot, gid, fmt.Sprintf("🔇 已禁言 %s(id %d),时长 %s,到期自动解除(可 /unmute 提前解除)。操作人 %s。",
		displayName(target), target.ID, banDurationText(secs), displayName(msg.From)))
	if v.cfg.AdminLogChatID != 0 {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(v.cfg.AdminLogChatID),
			fmt.Sprintf("/mute %s: 群 %d 目标 %d (%s) 操作人 %s", banDurationText(secs), gid, target.ID, displayName(target), displayName(msg.From))))
	}
	log.Printf("/mute by admin=%d target=%d group=%d secs=%d", msg.From.ID, target.ID, gid, secs)
	return nil
}

func (v *Verifier) onUnmute(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	gid := msg.Chat.ID
	defer func() {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(gid), MessageID: msg.MessageID})
	}()

	target := v.warnPrecheck(c, bot, msg, "/unmute", false) // admin gate + reply target
	if target == nil {
		return nil
	}
	if err := v.applyUnmute(c, bot, gid, target.ID); err != nil {
		log.Printf("/unmute user=%d in %d: %v", target.ID, gid, err)
		v.notify(c, bot, gid, "❌ 解除禁言失败:bot 可能缺少「封禁/限制成员」权限。")
		return nil
	}
	v.notify(c, bot, gid, fmt.Sprintf("🔊 已解除 %s(id %d)的禁言。操作人 %s。", displayName(target), target.ID, displayName(msg.From)))
	log.Printf("/unmute by admin=%d target=%d group=%d", msg.From.ID, target.ID, gid)
	return nil
}
