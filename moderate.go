package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

// adminStatus returns whether userID is an admin/creator of chatID, surfacing
// any API error so callers can fail-closed where it matters.
func (v *Verifier) adminStatus(c context.Context, bot *telego.Bot, chatID, userID int64) (bool, error) {
	cm, err := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(chatID), UserID: userID})
	if err != nil {
		return false, err
	}
	s := cm.MemberStatus()
	return s == "creator" || s == "administrator", nil
}

// isGroupAdmin is the fail-safe form (error => not admin), suitable for checking
// whether the COMMAND INVOKER is allowed (denying on error is safe).
func (v *Verifier) isGroupAdmin(c context.Context, bot *telego.Bot, chatID, userID int64) bool {
	ok, err := v.adminStatus(c, bot, chatID, userID)
	if err != nil {
		log.Printf("isGroupAdmin getChatMember chat=%d user=%d: %v", chatID, userID, err)
		return false
	}
	return ok
}

// logGroupAdmin logs (non-fatally) whether the bot is an admin in each guarded group, so
// a group it hasn't been granted admin in yet is visible in the logs rather than silently
// inert. Telegram only delivers join requests to admins, so a non-admin group is harmless
// — the bot just can't verify there until granted admin. Safe to run in the background.
func (v *Verifier) logGroupAdmin(c context.Context, bot *telego.Bot, selfID int64) {
	for i := range v.cfg.Groups {
		gid := v.cfg.Groups[i].ID
		switch ok, err := v.adminStatus(c, bot, gid, selfID); {
		case err != nil:
			log.Printf("group %d: cannot read bot membership yet (%v) — verification stays inactive until the bot is added as admin", gid, err)
		case ok:
			log.Printf("group %d: bot is admin ✓", gid)
		default:
			log.Printf("group %d: bot is NOT admin — join verification inactive until it's granted admin (approve members / ban / delete)", gid)
		}
	}
}

// notify sends a transient message to chatID and auto-deletes it after NotifyTTLSeconds.
func (v *Verifier) notify(c context.Context, bot *telego.Bot, chatID int64, text string) {
	m, err := bot.SendMessage(c, tu.Message(tu.ID(chatID), text))
	if err != nil || m == nil {
		return
	}
	ttl := v.cfg.NotifyTTLSeconds
	if ttl < 0 {
		return
	}
	msgID := m.MessageID
	time.AfterFunc(time.Duration(ttl)*time.Second, func() {
		_ = bot.DeleteMessage(context.Background(), &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: msgID})
	})
}

func (v *Verifier) onSb(ctx *th.Context, update telego.Update) error {
	return v.moderate(ctx, update, false)
}
func (v *Verifier) onBan(ctx *th.Context, update telego.Update) error {
	return v.moderate(ctx, update, true)
}

// moderate implements /sb (delete + kick, rejoinable) and /ban (delete + permanent ban).
// Reply to the offender's message; admin-only; works in any guarded group.
func (v *Verifier) moderate(ctx *th.Context, update telego.Update, permanent bool) error {
	msg := update.Message
	if msg == nil || msg.From == nil || !v.cfg.IsGroup(msg.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	gid := msg.Chat.ID
	cmd := "/sb"
	if permanent {
		cmd = "/ban"
	}

	defer func() {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(gid), MessageID: msg.MessageID})
	}()

	if !v.isGroupAdmin(c, bot, gid, msg.From.ID) {
		v.notify(c, bot, gid, fmt.Sprintf("⛔ %s 只能由群管理员使用。", cmd))
		return nil
	}
	if msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil {
		v.notify(c, bot, gid, fmt.Sprintf("用法:回复要处理的用户的消息,再发送 %s。", cmd))
		return nil
	}
	target := msg.ReplyToMessage.From
	if isAdmin, err := v.adminStatus(c, bot, gid, target.ID); err != nil {
		v.notify(c, bot, gid, "⚠️ 无法确认目标身份,请稍后重试。")
		return nil
	} else if isAdmin {
		v.notify(c, bot, gid, "目标是管理员,已忽略。")
		return nil
	}

	_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(gid), MessageID: msg.ReplyToMessage.MessageID})

	if err := bot.BanChatMember(c, &telego.BanChatMemberParams{ChatID: tu.ID(gid), UserID: target.ID, RevokeMessages: true}); err != nil {
		log.Printf("%s ban user=%d in %d: %v", cmd, target.ID, gid, err)
		v.notify(c, bot, gid, "❌ 操作失败:bot 可能缺少「封禁用户」权限。")
		return nil
	}
	action := "已永久封禁(不可再加入)"
	if !permanent {
		_ = bot.UnbanChatMember(c, &telego.UnbanChatMemberParams{ChatID: tu.ID(gid), UserID: target.ID, OnlyIfBanned: true})
		action = "已踢出(可重新申请入群)"
	}

	v.notify(c, bot, gid, fmt.Sprintf("✅ %s:%s(id %d),操作人 %s。", action, displayName(target), target.ID, displayName(msg.From)))
	if v.cfg.AdminLogChatID != 0 {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(v.cfg.AdminLogChatID),
			fmt.Sprintf("%s %s: 群 %d 目标 %d (%s) 操作人 %s", cmd, action, gid, target.ID, displayName(target), displayName(msg.From))))
	}
	log.Printf("%s by admin=%d target=%d group=%d permanent=%v", cmd, msg.From.ID, target.ID, gid, permanent)
	return nil
}

func displayName(u *telego.User) string {
	if u.Username != "" {
		return "@" + u.Username
	}
	return u.FirstName
}
