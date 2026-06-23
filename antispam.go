package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

// antispamState is the persisted form of the channel-sock-puppet filter's runtime state:
// the on/off toggle (/bc) and the channel whitelist (/bc allow|deny). It is seeded from
// config (block_channel_senders + channel_whitelist) and persisted to antispam.json so
// runtime changes survive restarts. The live state is Verifier.{acOn,acWhite}, guarded by acMu.
type antispamState struct {
	Enabled   bool    `json:"enabled"`
	Whitelist []int64 `json:"whitelist"`
}

// loadAntispam overrides the config-seeded state with antispam.json when it exists.
//
// PRECEDENCE: config's block_channel_senders / channel_whitelist are only the INITIAL
// seed (applied in NewVerifier). Once antispam.json exists (after the first /bc command),
// it is authoritative and fully replaces that seed — so editing those config keys later
// has NO effect until antispam.json is deleted. This keeps runtime /bc changes from being
// silently reverted on restart. Documented in the README config table.
func (v *Verifier) loadAntispam() {
	if v.acPath == "" {
		return
	}
	var st antispamState
	if err := loadJSONFile(v.acPath, &st); err != nil {
		return // corrupt file backed up to .corrupt; start empty
	}
	v.acMu.Lock()
	v.acOn = st.Enabled
	v.acWhite = map[int64]bool{}
	for _, id := range st.Whitelist {
		v.acWhite[id] = true
	}
	v.acMu.Unlock()
}

func (v *Verifier) saveAntispam() {
	if v.acPath == "" {
		return
	}
	v.acMu.RLock()
	st := antispamState{Enabled: v.acOn, Whitelist: make([]int64, 0, len(v.acWhite))}
	for id := range v.acWhite {
		st.Whitelist = append(st.Whitelist, id)
	}
	v.acMu.RUnlock()
	writeJSONFile(v.acPath, st)
}

func (v *Verifier) antispamEnabled() bool {
	v.acMu.RLock()
	defer v.acMu.RUnlock()
	return v.acOn
}

func (v *Verifier) channelWhitelisted(id int64) bool {
	v.acMu.RLock()
	defer v.acMu.RUnlock()
	return v.acWhite[id]
}

func (v *Verifier) toggleAntispam() bool {
	v.acMu.Lock()
	v.acOn = !v.acOn
	on := v.acOn
	v.acMu.Unlock()
	v.saveAntispam()
	return on
}

func (v *Verifier) setChannelWhite(id int64, allow bool) {
	v.acMu.Lock()
	if allow {
		v.acWhite[id] = true
	} else {
		delete(v.acWhite, id)
	}
	v.acMu.Unlock()
	v.saveAntispam()
}

// antispam is middleware that drops "channel sock-puppet" posts — a message sent in a
// guarded group on behalf of a channel that is NOT the group itself (anonymous group
// admins), the linked discussion channel (automatic forwards), a configured chat, or a
// whitelisted channel. Such a post is deleted and the channel is banned from posting.
//
// Toggle with /bc; off until then. Requires the bot's privacy mode OFF (BotFather) so it
// actually receives these messages — otherwise it never sees them.
func (v *Verifier) antispam(ctx *th.Context, update telego.Update) error {
	if msg := update.Message; v.antispamEnabled() && msg != nil && v.cfg.IsGroup(msg.Chat.ID) {
		if sc := msg.SenderChat; sc != nil &&
			sc.ID != msg.Chat.ID && // anonymous group admins post as the group itself
			!msg.IsAutomaticForward && // the linked discussion channel auto-forwards
			!v.cfg.IsKnownChat(sc.ID) && // required channel / feed targets / guarded chats
			!v.channelWhitelisted(sc.ID) { // runtime whitelist (/bc allow)
			bot := ctx.Bot()
			c := ctx.Context()
			_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(msg.Chat.ID), MessageID: msg.MessageID})
			banned := true
			if err := bot.BanChatSenderChat(c, &telego.BanChatSenderChatParams{ChatID: tu.ID(msg.Chat.ID), SenderChatID: sc.ID}); err != nil {
				banned = false
				log.Printf("antispam: ban sender_chat %d in %d: %v", sc.ID, msg.Chat.ID, err)
			}
			if banned {
				v.adminAlert(c, bot, fmt.Sprintf("🛡 已删除并封禁频道马甲「%s」(id %d,群 %d)。误封用 /bc allow %d 解封+白名单。", sc.Title, sc.ID, msg.Chat.ID, sc.ID))
			} else { // honest feedback: don't claim a ban the API rejected
				v.adminAlert(c, bot, fmt.Sprintf("🛡 已删除频道马甲「%s」的消息,但封禁失败(bot 可能缺权限),请手动封禁。(id %d,群 %d)", sc.Title, sc.ID, msg.Chat.ID))
			}
			log.Printf("antispam: channel sender %d (%q) in group %d deleted, banned=%v", sc.ID, sc.Title, msg.Chat.ID, banned)
			return nil // blocked — don't run the normal handlers
		}
	}
	return ctx.Next(update)
}

// parseChannelID accepts a channel id in either the Bot API form (-1001234567890) or the bare
// internal form (1234567890 — e.g. copied from a t.me/c/<id>/… link without the -100 prefix) and
// returns the canonical SenderChat.ID (-100…) form Telegram actually reports for a channel, so
// /bc allow|deny works with whichever form the admin pastes. A value already in -100… form is used
// as-is. Returns false for non-numeric / overflowing input.
func parseChannelID(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	if id < 0 {
		return id, true // already a chat id (-100… Bot API form)
	}
	// A bare positive internal id -> prepend the supergroup/channel "-100" prefix; Telegram's
	// SenderChat.ID for a channel is always the decimal "-100" concatenated with the internal id.
	full, err := strconv.ParseInt("-100"+s, 10, 64)
	if err != nil { // an absurdly long input overflows int64
		return 0, false
	}
	return full, true
}

// onBc handles /bc — toggle the channel-sock-puppet filter, or manage its whitelist.
//
//	/bc              toggle on/off
//	/bc allow <id>   whitelist a channel + un-ban it in this group
//	/bc deny  <id>   remove a channel from the whitelist
func (v *Verifier) onBc(ctx *th.Context, update telego.Update) error {
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
	if !v.isGroupAdmin(c, bot, gid, msg.From.ID) {
		v.notify(c, bot, gid, "⛔ /bc 只能由群管理员使用。")
		return nil
	}
	fields := strings.Fields(commandArg(msg.Text))
	switch {
	case len(fields) == 0:
		if v.toggleAntispam() {
			v.notify(c, bot, gid, "🛡 频道马甲封禁:已开启(需在 BotFather 关闭 bot 隐私模式才能看到马甲消息)。")
		} else {
			v.notify(c, bot, gid, "频道马甲封禁:已关闭。")
		}
	case (fields[0] == "allow" || fields[0] == "deny") && len(fields) >= 2:
		id, ok := parseChannelID(fields[1])
		if !ok {
			v.notify(c, bot, gid, "频道 id 不对,应为数字 —— 完整形式 -1001234567890,或不带 -100 前缀的纯数字 1234567890 都行。")
			return nil
		}
		if fields[0] == "allow" {
			v.setChannelWhite(id, true)
			if err := bot.UnbanChatSenderChat(c, &telego.UnbanChatSenderChatParams{ChatID: tu.ID(gid), SenderChatID: id}); err != nil {
				log.Printf("/bc allow: unban sender_chat %d in %d: %v", id, gid, err)
				v.notify(c, bot, gid, fmt.Sprintf("✅ 频道 %d 已加入白名单,但本群解封失败(bot 可能缺权限);若它仍被封请手动解封。", id))
			} else {
				v.notify(c, bot, gid, fmt.Sprintf("✅ 频道 %d 已加入白名单,并在本群解封。", id))
			}
		} else {
			v.setChannelWhite(id, false)
			v.notify(c, bot, gid, fmt.Sprintf("已把频道 %d 移出白名单。", id))
		}
	default:
		v.notify(c, bot, gid, "用法:/bc 开关封禁;/bc allow <频道id> 加白名单+解封;/bc deny <频道id> 移出白名单。")
	}
	return nil
}
