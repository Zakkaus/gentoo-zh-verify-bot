package bot

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

type dmHandler struct {
	cfg      *config.Config
	telegram *tg.Client
	mu       sync.Mutex
	last     map[int64]time.Time
}

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

func (v *dmHandler) privateReply(_ i18n.Lang) string {
	return v.cfg.PrivateReply
}

func (v *dmHandler) onPrivateDM(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil {
		return nil
	}
	v.mu.Lock()
	if last, ok := v.last[msg.From.ID]; ok && time.Since(last) < dmReplyCooldown {
		v.mu.Unlock()
		return nil // within cooldown: stay silent rather than reply to every flooded message
	}
	if len(v.last) >= dmMapMax {
		v.last = map[int64]time.Time{}
	}
	v.last[msg.From.ID] = time.Now()
	v.mu.Unlock()
	// Invalid admin-supplied HTML falls back to plain text.
	l := i18n.FromTelegram(msg.From.LanguageCode)
	v.telegram.SendPrivateHTMLFallback(ctx.Context(), msg.Chat.ID, v.privateReply(l))
	return nil
}
