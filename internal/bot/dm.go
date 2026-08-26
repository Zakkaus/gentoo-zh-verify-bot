package bot

import (
	"context"
	"crypto/sha256"
	"strings"
	"sync"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

type dmHandler struct {
	cfg            *config.Config
	settings       *store.Settings
	telegram       *tg.Client
	mu             sync.Mutex
	last           map[int64]time.Time
	catalogueReply bool
}

// Per-user throttling prevents DMs from amplifying into Telegram send floods.
const dmReplyCooldown = 30 * time.Second

// Keep the cooldown map bounded before untrusted user IDs can grow it indefinitely.
const dmMapMax = 10000

var legacyPrivateReplySHA256 = [sha256.Size]byte{
	0xbe, 0x70, 0x2f, 0x9d, 0xc8, 0xd2, 0x0b, 0x3f,
	0x2f, 0x88, 0x55, 0xb4, 0x9b, 0xdd, 0xe0, 0x9c,
	0x30, 0x0b, 0x36, 0xb2, 0x57, 0xc2, 0x05, 0x6a,
	0x7d, 0x20, 0xe7, 0x38, 0x86, 0xaa, 0x5d, 0x82,
}

// Config loading expands an empty private_reply to the legacy built-in text.
func isBuiltInPrivateReply(reply string) bool {
	if reply == "" {
		return true
	}
	return sha256.Sum256([]byte(reply)) == legacyPrivateReplySHA256
}

// Member commands bypass the unified DM reply. Deriving the set from the registered menu
// keeps two things right that a hand-written list got wrong: a newly added command works in
// direct messages straight away, and the Gentoo lookups carry the edition prefix, so the
// generic build admits /gpkg where the Gentoo build admits /pkg.
var dmCommands = func() map[string]bool {
	allowed := make(map[string]bool)
	for _, c := range memberCommands(i18n.LangEN) {
		allowed[c.Command] = true
	}
	return allowed
}()

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

func (v *dmHandler) privateReply(l i18n.Lang) string {
	if !v.catalogueReply {
		return v.cfg.PrivateReply
	}
	rate := v.cfg.PrivateQueryPerMin
	if v.settings != nil {
		rate = v.settings.Global().PrivateQueryPerMin().Value
	}
	dm := i18n.Messages.Bot.DirectMessage
	return dm.AutoReply.Render(l, rate, dm.Who(l))
}

func (v *dmHandler) onPrivateDM(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil {
		return nil
	}
	v.mu.Lock()
	now := time.Now()
	if last, ok := v.last[msg.From.ID]; ok && now.Sub(last) < dmReplyCooldown {
		v.mu.Unlock()
		return nil // within cooldown: stay silent rather than reply to every flooded message
	}
	if len(v.last) >= dmMapMax {
		cutoff := now.Add(-dmReplyCooldown)
		for userID, last := range v.last {
			if !last.After(cutoff) {
				delete(v.last, userID)
			}
		}
		if len(v.last) >= dmMapMax {
			v.last = map[int64]time.Time{}
		}
	}
	v.last[msg.From.ID] = now
	v.mu.Unlock()
	// Invalid admin-supplied HTML falls back to plain text.
	l := i18n.FromTelegram(msg.From.LanguageCode)
	v.telegram.SendPrivateHTMLFallback(ctx.Context(), msg.Chat.ID, v.privateReply(l))
	return nil
}
