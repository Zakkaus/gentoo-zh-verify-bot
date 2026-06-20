package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

// telegramBanMax is the longest finite ban Telegram honours: an until_date more than 366 days
// (or less than 30 seconds) out is treated as permanent. We map those onto our own 0=permanent
// so the displayed duration always matches the actual effect.
const telegramBanMax = 366 * 86400

// parseBanDuration parses a /bantime argument into seconds: "0" (or 永久/perm) => permanent
// (0), a bare number => seconds, or a number with a unit suffix s/m/h/d. It clamps to
// Telegram's honoured window: a value under 30s is raised to 30s, and a value over 366 days
// is treated as permanent (0) — both so the reported duration matches what Telegram enforces.
// ok=false on garbage.
func parseBanDuration(arg string) (secs int, ok bool) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	switch arg {
	case "":
		return 0, false
	case "0", "perm", "permanent", "永久":
		return 0, true
	}
	mult := 1
	switch arg[len(arg)-1] {
	case 's':
		arg = arg[:len(arg)-1]
	case 'm':
		mult, arg = 60, arg[:len(arg)-1]
	case 'h':
		mult, arg = 3600, arg[:len(arg)-1]
	case 'd':
		mult, arg = 86400, arg[:len(arg)-1]
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 0 || n > 1<<31 {
		return 0, false
	}
	switch secs = n * mult; {
	case secs <= 0:
		return 0, true // permanent
	case secs < 30:
		return 30, true // Telegram treats <30s as permanent — use its real 30s minimum instead
	case secs > telegramBanMax:
		return 0, true // Telegram treats >366d as permanent
	default:
		return secs, true
	}
}

// banDurationText renders a ban duration for user-facing messages (0 => 永久).
func banDurationText(secs int) string {
	if secs <= 0 {
		return "永久"
	}
	switch {
	case secs%86400 == 0:
		return fmt.Sprintf("%d 天", secs/86400)
	case secs%3600 == 0:
		return fmt.Sprintf("%d 小时", secs/3600)
	case secs%60 == 0:
		return fmt.Sprintf("%d 分钟", secs/60)
	default:
		return fmt.Sprintf("%d 秒", secs)
	}
}

func (v *Verifier) banDuration() int        { v.mu.Lock(); defer v.mu.Unlock(); return v.banSecs }
func (v *Verifier) setBanDuration(secs int) { v.mu.Lock(); v.banSecs = secs; v.mu.Unlock() }

// applyBan bans uid from gid for the configured duration: secs<=0 => permanent (no until-date),
// secs>0 => Telegram auto-unbans after now+secs. revoke deletes the user's recent messages.
func (v *Verifier) applyBan(c context.Context, bot *telego.Bot, gid, uid int64, secs int, revoke bool) error {
	p := &telego.BanChatMemberParams{ChatID: tu.ID(gid), UserID: uid, RevokeMessages: revoke}
	if secs > 0 {
		p.UntilDate = time.Now().Add(time.Duration(secs) * time.Second).Unix()
	}
	return bot.BanChatMember(c, p)
}

// onBanTime handles /bantime — show or set the default ban duration used by /ban, /sb and the
// repeat-failure auto-ban. "/bantime" shows; "/bantime 0" => permanent; "/bantime 7d", etc.
// Runtime override of cfg.BanSeconds (resets to the config value on restart).
func (v *Verifier) onBanTime(ctx *th.Context, update telego.Update) error {
	return v.adminCmd(ctx, update, func() string {
		arg := strings.TrimSpace(commandArg(update.Message.Text))
		if arg == "" {
			return fmt.Sprintf("⏱ 当前封禁时长:%s。\n用法:/bantime 0(永久)、/bantime 7d、/bantime 12h、/bantime 30m、/bantime 3600(秒)。", banDurationText(v.banDuration()))
		}
		secs, ok := parseBanDuration(arg)
		if !ok {
			return "用法:/bantime 0(永久)、/bantime 7d、/bantime 12h、/bantime 30m、/bantime 3600(秒)。"
		}
		v.setBanDuration(secs)
		return fmt.Sprintf("✅ 已设定封禁时长:%s。/ban、/sb 及验证连续失败自动封禁都会使用它。", banDurationText(secs))
	})
}
