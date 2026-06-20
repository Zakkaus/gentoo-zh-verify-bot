package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

// warnRec is the on-disk form of one (group, user) warning counter.
type warnRec struct {
	GroupID int64 `json:"group_id"`
	UserID  int64 `json:"user_id"`
	Count   int   `json:"count"`
}

func (v *Verifier) loadWarns() {
	if v.warnPath == "" {
		return
	}
	data, err := os.ReadFile(v.warnPath)
	if err != nil {
		return
	}
	var recs []warnRec
	if json.Unmarshal(data, &recs) != nil {
		return
	}
	v.mu.Lock()
	for _, r := range recs {
		if r.Count > 0 {
			v.warns[pkey{r.GroupID, r.UserID}] = r.Count
		}
	}
	n := len(v.warns)
	v.mu.Unlock()
	if n > 0 {
		log.Printf("restored %d warning counter(s)", n)
	}
}

func (v *Verifier) saveWarns() {
	if v.warnPath == "" {
		return
	}
	v.mu.Lock()
	recs := make([]warnRec, 0, len(v.warns))
	for k, n := range v.warns {
		if n > 0 {
			recs = append(recs, warnRec{GroupID: k.gid, UserID: k.uid, Count: n})
		}
	}
	v.mu.Unlock()
	data, err := json.Marshal(recs)
	if err != nil {
		return
	}
	tmp := v.warnPath + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, v.warnPath)
	}
}

// warnPrecheck shares the admin-gate + reply-target resolution of /warn and /clearwarn.
// It returns the target user, or nil if the caller should stop (it has already replied
// with the reason). The invoking command message is removed by the caller's defer.
func (v *Verifier) warnPrecheck(ctx *th.Context, msg *telego.Message, cmd string, checkTargetAdmin bool) *telego.User {
	bot := ctx.Bot()
	c := ctx.Context()
	gid := msg.Chat.ID
	if !v.isGroupAdmin(c, bot, gid, msg.From.ID) {
		v.notify(c, bot, gid, fmt.Sprintf("⛔ %s 只能由群管理员使用。", cmd))
		return nil
	}
	if msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil {
		v.notify(c, bot, gid, fmt.Sprintf("用法:回复目标用户的消息,再发送 %s。", cmd))
		return nil
	}
	target := msg.ReplyToMessage.From
	if checkTargetAdmin {
		if isAdmin, err := v.adminStatus(c, bot, gid, target.ID); err != nil {
			v.notify(c, bot, gid, "⚠️ 无法确认目标身份,请稍后重试。")
			return nil
		} else if isAdmin {
			v.notify(c, bot, gid, "目标是管理员,已忽略。")
			return nil
		}
	}
	return target
}

// onWarn handles /warn — reply to a user, add a warning; auto-kick (rejoinable) at the limit.
func (v *Verifier) onWarn(ctx *th.Context, update telego.Update) error {
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

	target := v.warnPrecheck(ctx, msg, "/warn", true)
	if target == nil {
		return nil
	}
	limit := v.cfg.WarnLimit
	v.mu.Lock()
	v.warns[pkey{gid, target.ID}]++
	n := v.warns[pkey{gid, target.ID}]
	v.mu.Unlock()

	if n >= limit {
		if err := bot.BanChatMember(c, &telego.BanChatMemberParams{ChatID: tu.ID(gid), UserID: target.ID}); err != nil {
			log.Printf("/warn kick %d in %d: %v", target.ID, gid, err)
			v.notify(c, bot, gid, "⚠️ 已达警告上限,但踢出失败:bot 可能缺少「封禁用户」权限。")
			return nil
		}
		_ = bot.UnbanChatMember(c, &telego.UnbanChatMemberParams{ChatID: tu.ID(gid), UserID: target.ID, OnlyIfBanned: true})
		v.mu.Lock()
		delete(v.warns, pkey{gid, target.ID})
		v.mu.Unlock()
		v.saveWarns()
		v.notify(c, bot, gid, fmt.Sprintf("🚫 %s 已达 %d 次警告上限,已踢出(可重新申请入群)。操作人 %s。", displayName(target), limit, displayName(msg.From)))
		v.adminAlert(c, bot, fmt.Sprintf("warn-kick: 群 %d 目标 %d (%s) 操作人 %s", gid, target.ID, displayName(target), displayName(msg.From)))
		log.Printf("/warn-kick user=%d group=%d by=%d", target.ID, gid, msg.From.ID)
		return nil
	}
	v.saveWarns()
	v.notify(c, bot, gid, fmt.Sprintf("⚠️ 已警告 %s(%d/%d);满 %d 次将自动踢出。操作人 %s。", displayName(target), n, limit, limit, displayName(msg.From)))
	log.Printf("/warn user=%d group=%d count=%d by=%d", target.ID, gid, n, msg.From.ID)
	return nil
}

// onClearWarn handles /clearwarn — reply to a user, clear their warning count.
func (v *Verifier) onClearWarn(ctx *th.Context, update telego.Update) error {
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

	target := v.warnPrecheck(ctx, msg, "/clearwarn", false)
	if target == nil {
		return nil
	}
	v.mu.Lock()
	had := v.warns[pkey{gid, target.ID}]
	delete(v.warns, pkey{gid, target.ID})
	v.mu.Unlock()
	v.saveWarns()
	v.notify(c, bot, gid, fmt.Sprintf("✅ 已清除 %s 的警告(原 %d 次)。操作人 %s。", displayName(target), had, displayName(msg.From)))
	log.Printf("/clearwarn user=%d group=%d was=%d by=%d", target.ID, gid, had, msg.From.ID)
	return nil
}
