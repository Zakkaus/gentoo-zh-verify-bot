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

// Telegram treats until_date below 30 seconds or above 366 days as permanent.
const telegramBanMax = 366 * 86400

// clampBanSecs keeps displayed and enforced ban durations equal.
func clampBanSecs(secs int) int {
	switch {
	case secs <= 0:
		return 0
	case secs < 30:
		return 30
	case secs > telegramBanMax:
		return 0
	default:
		return secs
	}
}

// Mutes are always finite and must stay within Telegram's honoured window.
func clampMuteSecs(secs int) int {
	switch {
	case secs < 30:
		return 30
	case secs > telegramBanMax:
		return telegramBanMax
	default:
		return secs
	}
}

// parseBanDuration accepts permanent, seconds, or s/m/h/d suffixes.
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

// banDurationText renders 0 as the localized permanent label.
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

// applyBan uses RevokeMessages to select ban-only versus ban-and-purge.
func (v *Verifier) applyBan(c context.Context, bot verifyBot, gid, uid int64, secs int, revoke bool) error {
	p := &telego.BanChatMemberParams{ChatID: tu.ID(gid), UserID: uid, RevokeMessages: revoke}
	if secs > 0 {
		p.UntilDate = time.Now().Add(time.Duration(secs) * time.Second).Unix()
	}
	return bot.BanChatMember(c, p)
}

func (v *Verifier) onBanTime(ctx *th.Context, update telego.Update) error {
	usage := "用法:/bantime <时长>,设定 /ban、/sb 和验证自动封禁的封禁时长:\n" +
		"• /bantime 0 —— 永久封禁(被封后无法再加入本群)\n" +
		"• /bantime 7d / 12h / 30m / 3600 —— 限时封禁(到期后可重新申请加入,相当于「踢出 + 冷静期」)\n" +
		"(d=天 h=小时 m=分钟,纯数字=秒;最少 30 秒)"
	return v.adminCmd(ctx, update, func() string {
		arg := strings.TrimSpace(commandArg(update.Message.Text))
		if arg == "" {
			kind := "永久,被封后无法再加入"
			if v.banDuration() > 0 {
				kind = "限时,到期后可重新加入"
			}
			return fmt.Sprintf("⏱ 当前封禁时长:%s(%s)。\n\n%s", banDurationText(v.banDuration()), kind, usage)
		}
		secs, ok := parseBanDuration(arg)
		if !ok {
			return usage
		}
		v.setBanDuration(secs)
		kind := "永久,被封后无法再加入本群"
		if secs > 0 {
			kind = "限时,到期后可重新申请加入(相当于踢出 + 冷静期)"
		}
		return fmt.Sprintf("✅ 已设定封禁时长:%s —— %s。\n/ban、/sb 及验证连续失败自动封禁都会使用它。", banDurationText(secs), kind)
	})
}
