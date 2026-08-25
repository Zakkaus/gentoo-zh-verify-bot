package main

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

// antispamState persists the /bc toggle and whitelist.
type antispamState struct {
	Enabled   bool    `json:"enabled"`
	Whitelist []int64 `json:"whitelist"`
}

// Oldest-added entries are evicted first; repeated allows retain their original position.
const channelWhitelistMax = 4096

// Caller holds acMu, or initialization has not published the verifier yet.
func (v *Verifier) addChannelWhiteLocked(id int64) {
	if v.acWhite == nil {
		v.acWhite = make(map[int64]bool)
	}
	if v.acWhite[id] {
		return
	}
	for len(v.acWhite) >= channelWhitelistMax {
		if len(v.acWhiteOrder) == 0 {
			var victim int64
			first := true
			for candidate := range v.acWhite {
				if first || candidate < victim {
					victim, first = candidate, false
				}
			}
			delete(v.acWhite, victim)
			break
		}
		victim := v.acWhiteOrder[0]
		v.acWhiteOrder = v.acWhiteOrder[1:]
		if v.acWhite[victim] {
			delete(v.acWhite, victim)
			break
		}
	}
	v.acWhite[id] = true
	v.acWhiteOrder = append(v.acWhiteOrder, id)
}

// Caller holds acMu.
func (v *Verifier) removeChannelWhiteLocked(id int64) {
	delete(v.acWhite, id)
	for i, existing := range v.acWhiteOrder {
		if existing == id {
			copy(v.acWhiteOrder[i:], v.acWhiteOrder[i+1:])
			v.acWhiteOrder = v.acWhiteOrder[:len(v.acWhiteOrder)-1]
			return
		}
	}
}

// Persisted /bc state replaces the config seed after the first runtime change.
// Delete antispam.json before expecting later config edits to take effect.
func (v *Verifier) loadAntispam() {
	if v.acPath == "" {
		return
	}
	var st antispamState
	if err := store.Load(v.acPath, &st); err != nil {
		if store.ReadFailed(err) {
			v.acPath = ""
		}
		return // corrupt files were backed up; unreadable files remain untouched and write-disabled
	}
	v.acMu.Lock()
	v.acOn = st.Enabled
	v.acWhite = map[int64]bool{}
	v.acWhiteOrder = nil
	for _, id := range st.Whitelist {
		v.addChannelWhiteLocked(id)
	}
	v.acMu.Unlock()
}

func (v *Verifier) saveAntispam() {
	if v.acPath == "" {
		return
	}
	_ = store.Save(v.acPath, func() any {
		v.acMu.RLock()
		defer v.acMu.RUnlock()
		st := antispamState{Enabled: v.acOn, Whitelist: make([]int64, 0, len(v.acWhite))}
		for id := range v.acWhite {
			st.Whitelist = append(st.Whitelist, id)
		}
		sort.Slice(st.Whitelist, func(i, j int) bool { return st.Whitelist[i] < st.Whitelist[j] })
		return st
	})
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
		v.addChannelWhiteLocked(id)
	} else {
		v.removeChannelWhiteLocked(id)
	}
	v.acMu.Unlock()
	v.saveAntispam()
}

// antispam drops posts sent on behalf of an untrusted channel.
// BotFather privacy mode must be off or Telegram will not deliver these messages.
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
			if err := v.telegram(bot).BanSenderChat(c, msg.Chat.ID, sc.ID); err != nil {
				banned = false
				log.Printf("antispam: ban sender_chat %d in %d: %v", sc.ID, msg.Chat.ID, err)
			}
			if banned {
				v.adminAlert(c, bot, fmt.Sprintf("🛡 已删除消息并封禁以频道身份发言的「%s」(id %d,群 %d)。如属误封,用 /bc allow %d 解除封禁并加入白名单。", sc.Title, sc.ID, msg.Chat.ID, sc.ID))
			} else {
				v.adminAlert(c, bot, fmt.Sprintf("🛡 已删除「%s」以频道身份发送的消息,但封禁失败(bot 可能缺权限),请手动封禁。(id %d,群 %d)", sc.Title, sc.ID, msg.Chat.ID))
			}
			log.Printf("antispam: channel sender %d (%q) in group %d deleted, banned=%v", sc.ID, sc.Title, msg.Chat.ID, banned)
			return nil // Do not run normal handlers for blocked posts.
		}
	}
	return ctx.Next(update)
}

// parseChannelID canonicalizes both Bot API -100 IDs and bare t.me/c IDs.
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
	// SenderChat.ID prefixes a channel's bare internal ID with "-100".
	full, err := strconv.ParseInt("-100"+s, 10, 64)
	if err != nil { // an absurdly long input overflows int64
		return 0, false
	}
	return full, true
}

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
	fields := strings.Fields(commandArg(msg.Text))
	isAllow := len(fields) >= 2 && fields[0] == "allow"
	if !isAllow {
		if allowed, refusal := v.cfg.ControlGroupAllowed(gid); !allowed {
			v.notify(c, bot, gid, refusal)
			return nil
		}
	}
	if !v.isGroupAdminCached(c, bot, gid, msg.From.ID) {
		v.notify(c, bot, gid, "⛔ /bc 只能由群管理员使用。")
		return nil
	}
	switch {
	case len(fields) == 0:
		if v.toggleAntispam() {
			v.notify(c, bot, gid, "🛡 频道身份发言封禁:已开启(需在 BotFather 关闭 bot 隐私模式,机器人才能收到这类消息)。")
		} else {
			v.notify(c, bot, gid, "频道身份发言封禁:已关闭。")
		}
	case (fields[0] == "allow" || fields[0] == "deny") && len(fields) >= 2:
		id, ok := parseChannelID(fields[1])
		if !ok {
			v.notify(c, bot, gid, "频道 id 不对,应为数字 —— 完整形式 -1001234567890,或不带 -100 前缀的纯数字 1234567890 都行。")
			return nil
		}
		if fields[0] == "allow" {
			v.setChannelWhite(id, true)
			failed := make([]string, 0)
			for _, groupID := range v.cfg.GroupIDs {
				if err := v.telegram(bot).UnbanSenderChat(c, groupID, id); err != nil {
					log.Printf("/bc allow: unban sender_chat %d in %d: %v", id, groupID, err)
					failed = append(failed, strconv.FormatInt(groupID, 10))
				}
			}
			if len(failed) > 0 {
				v.notify(c, bot, gid, fmt.Sprintf("✅ 频道 %d 已加入白名单,但以下群解封失败:%s。请确认机器人具有「封禁用户」权限后手动解封。", id, strings.Join(failed, ",")))
			} else {
				v.notify(c, bot, gid, fmt.Sprintf("✅ 频道 %d 已加入白名单,并在所有受保护群中解封。", id))
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
