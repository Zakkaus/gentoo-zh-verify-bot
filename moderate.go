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

// Only positive admin status is cached; destructive paths always recheck Telegram.
const (
	adminCacheTTL   = 30 * time.Second
	adminCacheMax   = 4096
	cleanupTimerMax = 256
)

// Caller holds adminMu. Expired entries go first; the oldest live lookup is evicted at capacity.
func (v *Verifier) pruneAdminCacheLocked(now time.Time) {
	for k, exp := range v.adminCache {
		if !now.Before(exp) {
			delete(v.adminCache, k)
		}
	}
	for len(v.adminCache) > adminCacheMax {
		var victim pkey
		var victimExpiry time.Time
		found := false
		for key, expiry := range v.adminCache {
			if !found || expiry.Before(victimExpiry) ||
				expiry.Equal(victimExpiry) && (key.gid < victim.gid || key.gid == victim.gid && key.uid < victim.uid) {
				victim, victimExpiry, found = key, expiry, true
			}
		}
		delete(v.adminCache, victim)
	}
}

// Never cache non-admin status, so promotions take effect immediately.
func (v *Verifier) adminStatus(c context.Context, bot modBot, chatID, userID int64) (bool, error) {
	key := pkey{chatID, userID}
	v.adminMu.Lock()
	exp, cached := v.adminCache[key]
	v.adminMu.Unlock()
	if cached && time.Now().Before(exp) {
		return true, nil
	}
	return v.fetchAdminStatus(c, bot, key)
}

// Destructive actions ignore cached positives and fail closed on lookup errors.
func (v *Verifier) adminStatusFresh(c context.Context, bot modBot, chatID, userID int64) (bool, error) {
	return v.fetchAdminStatus(c, bot, pkey{chatID, userID})
}

func (v *Verifier) fetchAdminStatus(c context.Context, bot modBot, key pkey) (bool, error) {
	cm, err := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(key.gid), UserID: key.uid})
	if err != nil {
		return false, err
	}
	s := cm.MemberStatus()
	isAdmin := s == "creator" || s == "administrator"
	v.adminMu.Lock()
	if isAdmin {
		now := time.Now()
		v.adminCache[key] = now.Add(adminCacheTTL)
		if len(v.adminCache) > adminCacheMax {
			v.pruneAdminCacheLocked(now)
		}
	} else {
		delete(v.adminCache, key)
	}
	v.adminMu.Unlock()
	return isAdmin, nil
}

// Moderation and approval require a fresh, successful admin lookup.
func (v *Verifier) isGroupAdmin(c context.Context, bot modBot, chatID, userID int64) bool {
	ok, err := v.adminStatusFresh(c, bot, chatID, userID)
	if err != nil {
		log.Printf("isGroupAdmin getChatMember chat=%d user=%d: %v", chatID, userID, err)
		return false
	}
	return ok
}

func (v *Verifier) isGroupAdminCached(c context.Context, bot modBot, chatID, userID int64) bool {
	ok, err := v.adminStatus(c, bot, chatID, userID)
	if err != nil {
		log.Printf("isGroupAdminCached getChatMember chat=%d user=%d: %v", chatID, userID, err)
		return false
	}
	return ok
}

// Owners implicitly have all rights; administrators are checked per capability.
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

// Startup preflight exposes missing group permissions without making startup fatal.
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
	// An unreadable required channel makes its membership gate unenforceable.
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
	// An unreadable trusted group disables only its bypass.
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

func (v *Verifier) reserveCleanupTimer() bool {
	for {
		n := v.cleanupTimers.Load()
		if n >= cleanupTimerMax {
			return false
		}
		if v.cleanupTimers.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

func (v *Verifier) notify(c context.Context, bot modBot, chatID int64, text string) {
	m, err := bot.SendMessage(c, tu.Message(tu.ID(chatID), text))
	if err != nil || m == nil {
		return
	}
	ttl := v.cfg.NotifyTTLSeconds
	if ttl < 0 {
		return
	}
	if !v.reserveCleanupTimer() {
		return
	}
	msgID := m.MessageID
	time.AfterFunc(time.Duration(ttl)*time.Second, func() {
		defer v.cleanupTimers.Add(-1)
		_ = bot.DeleteMessage(context.Background(), &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: msgID})
	})
}

func (v *Verifier) onSb(ctx *th.Context, update telego.Update) error {
	return v.moderate(ctx, update, "/sb")
}
func (v *Verifier) onBan(ctx *th.Context, update telego.Update) error {
	return v.moderate(ctx, update, "/ban")
}

// /sb bans and purges all messages; /ban deletes only the replied message.
// Both require a fresh admin check and use the configured ban duration.
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
	// Ban before deleting, so a permission failure leaves evidence and the user unchanged.
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
		verb = "封禁并清空(已清除其全部消息)"
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
