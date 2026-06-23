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

// adminCacheTTL is how long a confirmed admin status is reused before re-checking with Telegram.
// Only ADMINS are cached, so a freshly-promoted admin works immediately (a non-admin always
// re-checks); the only staleness window is a just-demoted admin still acting for up to this long.
const (
	adminCacheTTL = 60 * time.Second
	adminCacheMax = 4096 // safety cap on adminCache size (admins are few; bounds the map like the other per-user maps)
)

// pruneAdminCacheLocked drops expired entries from adminCache. Caller holds adminMu.
func (v *Verifier) pruneAdminCacheLocked(now time.Time) {
	for k, exp := range v.adminCache {
		if now.After(exp) {
			delete(v.adminCache, k)
		}
	}
}

// adminStatus returns whether userID is an admin/creator of chatID, surfacing any API error so
// callers can fail-closed where it matters. A confirmed admin is cached for adminCacheTTL so the
// hot path (admin buttons, moderation commands) skips a ~0.5s GetChatMember round-trip on repeat use.
func (v *Verifier) adminStatus(c context.Context, bot modBot, chatID, userID int64) (bool, error) {
	key := pkey{chatID, userID}
	v.adminMu.Lock()
	exp, cached := v.adminCache[key]
	v.adminMu.Unlock()
	if cached && time.Now().Before(exp) {
		return true, nil
	}
	cm, err := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(chatID), UserID: userID})
	if err != nil {
		return false, err
	}
	s := cm.MemberStatus()
	isAdmin := s == "creator" || s == "administrator"
	if isAdmin {
		v.adminMu.Lock()
		now := time.Now()
		v.adminCache[key] = now.Add(adminCacheTTL)
		if len(v.adminCache) > adminCacheMax { // bound the map (only admins are cached, so rarely hit)
			v.pruneAdminCacheLocked(now)
		}
		v.adminMu.Unlock()
	}
	return isAdmin, nil
}

// isGroupAdmin is the fail-safe form (error => not admin), suitable for checking
// whether the COMMAND INVOKER is allowed (denying on error is safe).
func (v *Verifier) isGroupAdmin(c context.Context, bot modBot, chatID, userID int64) bool {
	ok, err := v.adminStatus(c, bot, chatID, userID)
	if err != nil {
		log.Printf("isGroupAdmin getChatMember chat=%d user=%d: %v", chatID, userID, err)
		return false
	}
	return ok
}

// missingModRights returns the moderation rights the bot still lacks as a group admin: approving
// join requests needs can_invite_users, banning needs can_restrict_members, deleting needs
// can_delete_messages. The owner (ChatMemberOwner) implicitly has every right, so nothing is missing.
func missingModRights(cm telego.ChatMember) []string {
	adm, ok := cm.(*telego.ChatMemberAdministrator)
	if !ok {
		return nil // owner (all rights) or non-admin (the caller's NOT-admin branch handles that)
	}
	var miss []string
	if !adm.CanInviteUsers {
		miss = append(miss, "approve members (can_invite_users)")
	}
	if !adm.CanRestrictMembers {
		miss = append(miss, "ban/restrict (can_restrict_members)")
	}
	if !adm.CanDeleteMessages {
		miss = append(miss, "delete messages (can_delete_messages)")
	}
	return miss
}

// logGroupAdmin logs (non-fatally) whether the bot is an admin in each guarded group, so
// a group it hasn't been granted admin in yet is visible in the logs rather than silently
// inert. Telegram only delivers join requests to admins, so a non-admin group is harmless
// — the bot just can't verify there until granted admin. Safe to run in the background.
func (v *Verifier) logGroupAdmin(c context.Context, bot modBot, selfID int64) {
	for i := range v.cfg.Groups {
		gid := v.cfg.Groups[i].ID
		switch ok, err := v.adminStatus(c, bot, gid, selfID); {
		case err != nil:
			log.Printf("group %d: cannot read bot membership yet (%v) — verification stays inactive until the bot is added as admin", gid, err)
		case ok:
			log.Printf("group %d: bot is admin ✓", gid)
			if cm, e := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(gid), UserID: selfID}); e == nil {
				if miss := missingModRights(cm); len(miss) > 0 {
					log.Printf("group %d: WARNING bot is admin but MISSING rights: %s — those actions will fail until granted", gid, strings.Join(miss, ", "))
				}
			} else {
				log.Printf("group %d: bot is admin but couldn't read its exact rights (%v)", gid, e)
			}
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
	// Probe each distinct trusted-member source group: if the bot can't read its membership there,
	// the bypass can't be applied (applicants just fall back to verifying) — surface it now.
	trusted := append([]int64{}, v.cfg.TrustedMemberGroupIDs...)
	for i := range v.cfg.Groups {
		trusted = append(trusted, v.cfg.Groups[i].TrustedMemberGroupIDs...)
	}
	for _, src := range trusted {
		if src == 0 || seen[src] {
			continue
		}
		seen[src] = true
		if _, err := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(src), UserID: selfID}); err != nil {
			log.Printf("trusted group %d: bot CANNOT read membership (%v) — its members can't be auto-approved; add the bot there (member/admin)", src, err)
		} else {
			log.Printf("trusted group %d: bot can read membership ✓ — its members skip verification", src)
		}
	}
}

// notify sends a transient message to chatID and auto-deletes it after NotifyTTLSeconds.
func (v *Verifier) notify(c context.Context, bot modBot, chatID int64, text string) {
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

	target := v.warnPrecheck(c, bot, msg, cmd, true) // shared admin-gate + reply-target + skip-admins
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
		v.failAlert(c, bot, gid, fmt.Sprintf("⚠️ %s 失败:群 %d 目标 %d (%s),操作人 %s,bot 可能缺「封禁」权限", cmd, gid, target.ID, displayName(target), displayName(msg.From)))
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
