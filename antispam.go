package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
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
	data, err := os.ReadFile(v.acPath)
	if err != nil {
		return
	}
	var st antispamState
	if json.Unmarshal(data, &st) != nil {
		return
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
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := v.acPath + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, v.acPath)
	}
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
			if err := bot.BanChatSenderChat(c, &telego.BanChatSenderChatParams{ChatID: tu.ID(msg.Chat.ID), SenderChatID: sc.ID}); err != nil {
				log.Printf("antispam: ban sender_chat %d in %d: %v", sc.ID, msg.Chat.ID, err)
			}
			v.adminAlert(c, bot, fmt.Sprintf("🛡 已删除并封禁频道马甲「%s」(id %d,群 %d)。误封用 /bc allow %d 解封+白名单。", sc.Title, sc.ID, msg.Chat.ID, sc.ID))
			log.Printf("antispam: banned channel sender %d (%q) in group %d", sc.ID, sc.Title, msg.Chat.ID)
			return nil // blocked — don't run the normal handlers
		}
	}
	return ctx.Next(update)
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
		id, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			v.notify(c, bot, gid, "频道 id 不对,应为数字(如 -1001234567890)。")
			return nil
		}
		if fields[0] == "allow" {
			v.setChannelWhite(id, true)
			_ = bot.UnbanChatSenderChat(c, &telego.UnbanChatSenderChatParams{ChatID: tu.ID(gid), SenderChatID: id})
			v.notify(c, bot, gid, fmt.Sprintf("✅ 频道 %d 已加入白名单,并在本群解封。", id))
		} else {
			v.setChannelWhite(id, false)
			v.notify(c, bot, gid, fmt.Sprintf("已把频道 %d 移出白名单。", id))
		}
	default:
		v.notify(c, bot, gid, "用法:/bc 开关封禁;/bc allow <频道id> 加白名单+解封;/bc deny <频道id> 移出白名单。")
	}
	return nil
}
