package main

import (
	"fmt"
	"log"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

// antispam is middleware that drops "channel sock-puppet" posts — a message sent in a
// guarded group on behalf of a channel that is NOT the group itself (anonymous group
// admins), the linked discussion channel (automatic forwards), a configured chat, or a
// whitelisted channel. Such a post is deleted and the channel is banned from posting again.
//
// Off by default; enable with block_channel_senders. Requires the bot's privacy mode to
// be OFF (BotFather) so it actually receives these messages — otherwise it never sees them.
func (v *Verifier) antispam(ctx *th.Context, update telego.Update) error {
	if msg := update.Message; v.cfg.BlockChannelSenders && msg != nil && v.cfg.IsGroup(msg.Chat.ID) {
		if sc := msg.SenderChat; sc != nil &&
			sc.ID != msg.Chat.ID && // anonymous group admins post as the group itself
			!msg.IsAutomaticForward && // the linked discussion channel auto-forwards
			!v.cfg.IsKnownChat(sc.ID) && // required channel / feed targets / guarded chats
			!v.cfg.channelAllowed(sc.ID) { // explicit whitelist
			bot := ctx.Bot()
			c := ctx.Context()
			_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(msg.Chat.ID), MessageID: msg.MessageID})
			if err := bot.BanChatSenderChat(c, &telego.BanChatSenderChatParams{ChatID: tu.ID(msg.Chat.ID), SenderChatID: sc.ID}); err != nil {
				log.Printf("antispam: ban sender_chat %d in %d: %v", sc.ID, msg.Chat.ID, err)
			}
			v.adminAlert(c, bot, fmt.Sprintf("🛡 已删除并封禁频道马甲「%s」(id %d,群 %d)", sc.Title, sc.ID, msg.Chat.ID))
			log.Printf("antispam: banned channel sender %d (%q) in group %d", sc.ID, sc.Title, msg.Chat.ID)
			return nil // blocked — don't run the normal handlers
		}
	}
	return ctx.Next(update)
}
