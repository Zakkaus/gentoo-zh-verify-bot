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

// applyMute restricts uid in gid from sending anything until now+secs; Telegram automatically
// lifts the restriction when it expires (so there's no separate unmute to schedule). An empty
// ChatPermissions{} means every "can send …" flag is false — a full mute. secs must be > 0.
func (v *Verifier) applyMute(c context.Context, bot *telego.Bot, gid, uid int64, secs int) error {
	return bot.RestrictChatMember(c, &telego.RestrictChatMemberParams{
		ChatID:      tu.ID(gid),
		UserID:      uid,
		Permissions: telego.ChatPermissions{}, // all false => muted
		UntilDate:   time.Now().Add(time.Duration(secs) * time.Second).Unix(),
	})
}

// applyUnmute lifts a mute early by restoring the member to the GROUP's own default permissions
// (so a restrictive group isn't over-granted) — fetched via GetChat. restoredDefault reports
// whether those defaults were actually read; if GetChat fails it falls back to a permissive set
// (the common case where members post freely) and returns restoredDefault=false so the caller can
// say so honestly rather than silently over-granting in a restrictive group.
func (v *Verifier) applyUnmute(c context.Context, bot *telego.Bot, gid, uid int64) (restoredDefault bool, err error) {
	perms := telego.ChatPermissions{
		CanSendMessages: telego.ToPtr(true), CanSendAudios: telego.ToPtr(true), CanSendDocuments: telego.ToPtr(true),
		CanSendPhotos: telego.ToPtr(true), CanSendVideos: telego.ToPtr(true), CanSendVideoNotes: telego.ToPtr(true),
		CanSendVoiceNotes: telego.ToPtr(true), CanSendPolls: telego.ToPtr(true), CanSendOtherMessages: telego.ToPtr(true),
		CanAddWebPagePreviews: telego.ToPtr(true), CanInviteUsers: telego.ToPtr(true),
	}
	if chat, gerr := bot.GetChat(c, &telego.GetChatParams{ChatID: tu.ID(gid)}); gerr == nil && chat != nil && chat.Permissions != nil {
		perms = *chat.Permissions // restore the group's default policy, not a blanket allow
		restoredDefault = true
	}
	return restoredDefault, bot.RestrictChatMember(c, &telego.RestrictChatMemberParams{ChatID: tu.ID(gid), UserID: uid, Permissions: perms})
}

// onMute handles /mute [时长] — reply to a message; mute the sender (禁言: stays in the group
// but can't post), delete that message. No arg => the configured default (mute_seconds, 1h);
// an inline duration (e.g. /mute 30m, /mute 2h) overrides it. Always timed (no permanent mute);
// Telegram auto-lifts it on expiry, and /unmute lifts it early.
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

	target := v.warnPrecheck(ctx, msg, "/mute", true) // admin gate + reply target + skip admins
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
	// Apply the restriction FIRST; only delete the offending message if it succeeded — so a
	// permission failure leaves both the message and the un-muted user intact (the "禁言失败"
	// notice then matches reality) rather than deleting a message while the user stays unmuted.
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

// onUnmute handles /unmute — reply to a member; lift their mute early (restore posting).
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

	target := v.warnPrecheck(ctx, msg, "/unmute", false) // admin gate + reply target
	if target == nil {
		return nil
	}
	restoredDefault, err := v.applyUnmute(c, bot, gid, target.ID)
	if err != nil {
		log.Printf("/unmute user=%d in %d: %v", target.ID, gid, err)
		v.notify(c, bot, gid, "❌ 解除禁言失败:bot 可能缺少「封禁/限制成员」权限。")
		return nil
	}
	notice := fmt.Sprintf("🔊 已解除 %s(id %d)的禁言。操作人 %s。", displayName(target), target.ID, displayName(msg.From))
	if !restoredDefault { // GetChat failed — we applied a generic allow, which may exceed a restrictive group's default
		notice += "\n⚠️ 暂时读不到本群默认权限,已按通用可发言权限解除;若本群有特殊发言限制,请手动核对该成员权限。"
		log.Printf("/unmute group=%d: GetChat default perms unavailable; applied permissive fallback", gid)
	}
	v.notify(c, bot, gid, notice)
	log.Printf("/unmute by admin=%d target=%d group=%d", msg.From.ID, target.ID, gid)
	return nil
}
