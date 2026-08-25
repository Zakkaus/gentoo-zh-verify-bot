package main

import (
	"context"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// Per-user throttling prevents DMs from amplifying into Telegram send floods.
const dmReplyCooldown = 30 * time.Second

// Clear the cooldown map before untrusted user IDs can grow it without bound.
const dmMapMax = 10000

// Only these member commands bypass the unified DM reply.
var dmCommands = map[string]bool{
	"pkg": true, "use": true, "bug": true, "news": true, "wiki": true, "bbs": true,
	"distro": true, "pkgs": true, "arm": true, "armpkgs": true,
	"help": true, "ping": true, "stats": true,
}

// /start and allowed DM commands must reach their registered handlers.
func privateNonStart(_ context.Context, update telego.Update) bool {
	m := update.Message
	if m == nil || m.Chat.Type != "private" {
		return false
	}
	if fields := strings.Fields(m.Text); len(fields) > 0 {
		cmd := fields[0]
		if i := strings.IndexByte(cmd, '@'); i >= 0 { // strip /cmd@BotName
			cmd = cmd[:i]
		}
		if cmd == "/start" {
			return false
		}
		if strings.HasPrefix(cmd, "/") && dmCommands[cmd[1:]] {
			return false // a member command usable in DM — let its (rate-limited) handler run
		}
	}
	return true
}

func (v *Verifier) onPrivateDM(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil {
		return nil
	}
	v.mu.Lock()
	if last, ok := v.dmLast[msg.From.ID]; ok && time.Since(last) < dmReplyCooldown {
		v.mu.Unlock()
		return nil // within cooldown: stay silent rather than reply to every flooded message
	}
	if len(v.dmLast) >= dmMapMax {
		v.dmLast = map[int64]time.Time{}
	}
	v.dmLast[msg.From.ID] = time.Now()
	v.mu.Unlock()
	// Invalid admin-supplied HTML falls back to plain text.
	v.telegram(ctx.Bot()).SendPrivateHTMLFallback(ctx.Context(), msg.Chat.ID, v.cfg.PrivateReply)
	return nil
}
