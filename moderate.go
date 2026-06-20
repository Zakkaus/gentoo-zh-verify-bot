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
	// Probe each distinct required channel: if the bot can't read its own membership there,
	// the follow-gate can't be enforced (applicants would be wrongly blocked) — surface it now.
	seen := map[int64]bool{}
	for i := range v.cfg.Groups {
		rc := v.cfg.requiredChannel(v.cfg.Groups[i].ID)
		if rc == 0 || seen[rc] {
			continue
		}
		seen[rc] = true
		if _, err := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(rc), UserID: selfID}); err != nil {
			log.Printf("required channel %d: bot CANNOT read membership (%v) — the follow-gate can't be enforced; make the bot an admin of this channel", rc, err)
		} else {
			log.Printf("required channel %d: bot can read membership ✓", rc)
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
	return v.moderate(ctx, update, "/sb")
}
func (v *Verifier) onBan(ctx *th.Context, update telego.Update) error {
	return v.moderate(ctx, update, "/ban")
}

// moderate implements the two reply-to-a-message moderation commands; both ban the user for
// the configured duration (banDuration / /bantime; 0 = permanent) and log to the admin chat:
//   - /sb  = 举报并封禁 (report + ban): deletes ALL of the user's messages in the group
//     (revoke_messages) — for spam cleanup — then bans.
//   - /ban = 封禁 (ban): deletes only the replied-to message, then bans.
//
// Admin-only; any guarded group.
func (v *Verifier) moderate(ctx *th.Context, update telego.Update, cmd string) error {
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

	target := v.warnPrecheck(ctx, msg, cmd, true) // shared admin-gate + reply-target + skip-admins
	if target == nil {
		return nil
	}
	// Ban FIRST; only delete the replied message once the ban succeeded — so a permission
	// failure doesn't delete the offending message while leaving the user un-banned. (/sb's
	// RevokeMessages=true already purges all the user's messages as part of the ban.)
	secs := v.banDuration()
	revoke := cmd == "/sb"
	if err := v.applyBan(c, bot, gid, target.ID, secs, revoke); err != nil {
		log.Printf("%s ban user=%d in %d: %v", cmd, target.ID, gid, err)
		v.notify(c, bot, gid, "❌ 操作失败:bot 可能缺少「封禁用户」权限。")
		return nil
	}
	_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(gid), MessageID: msg.ReplyToMessage.MessageID})
	verb := "封禁"
	if cmd == "/sb" {
		verb = "举报并封禁(已清除其全部消息)" // /sb is the report-and-ban variant + message purge
	}
	action := fmt.Sprintf("已%s(%s)", verb, banDurationText(secs))

	v.notify(c, bot, gid, fmt.Sprintf("✅ %s:%s(id %d),操作人 %s。", action, displayName(target), target.ID, displayName(msg.From)))
	if v.cfg.AdminLogChatID != 0 {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(v.cfg.AdminLogChatID),
			fmt.Sprintf("%s %s: 群 %d 目标 %d (%s) 操作人 %s", cmd, action, gid, target.ID, displayName(target), displayName(msg.From))))
	}
	log.Printf("%s by admin=%d target=%d group=%d ban_secs=%d", cmd, msg.From.ID, target.ID, gid, secs)
	return nil
}

func displayName(u *telego.User) string {
	if u.Username != "" {
		return "@" + u.Username
	}
	return u.FirstName
}
