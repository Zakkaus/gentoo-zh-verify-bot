package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

const (
	answerPrefix  = "v:"   // applicant answers the quiz (in DM): v:<gid>:<uid>:<idx>
	adminPrefix   = "adm:" // admin override (in group): adm:<action>:<gid>:<uid>
	recheckPrefix = "ch:"  // "I followed the channel, continue" (in DM): ch:<uid>
)

type pkey struct{ gid, uid int64 }

type pending struct {
	groupMsgID int
	qText      string
	qOpts      []string
	correctIdx int
	deadline   time.Time
	timer      *time.Timer
	done       bool
}

type pendingRec struct {
	UserID     int64    `json:"user_id"`
	GroupID    int64    `json:"group_id"`
	GroupMsgID int      `json:"group_msg_id"`
	QText      string   `json:"q_text"`
	QOpts      []string `json:"q_opts"`
	CorrectIdx int      `json:"correct_idx"`
	Deadline   int64    `json:"deadline"`
}

type Verifier struct {
	cfg         *Config
	botUsername string
	statePath   string
	loc         *time.Location
	startTime   time.Time
	mu          sync.Mutex
	pend        map[pkey]*pending
	enabled     bool
	statDate    string
	approved    int
	declined    int
}

func loadStatsLoc(name string) *time.Location {
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.FixedZone("UTC+8", 8*3600)
}

func NewVerifier(cfg *Config) *Verifier {
	return &Verifier{cfg: cfg, startTime: time.Now(), loc: loadStatsLoc(cfg.StatsTimezone),
		pend: make(map[pkey]*pending), enabled: true}
}

func (v *Verifier) isEnabled() bool   { v.mu.Lock(); defer v.mu.Unlock(); return v.enabled }
func (v *Verifier) setEnabled(b bool) { v.mu.Lock(); v.enabled = b; v.mu.Unlock() }

func (v *Verifier) now() time.Time { return time.Now().In(v.loc) }

func (v *Verifier) recordDecision(approve bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	today := v.now().Format("2006-01-02")
	if v.statDate != today {
		v.statDate, v.approved, v.declined = today, 0, 0
	}
	if approve {
		v.approved++
	} else {
		v.declined++
	}
}

func (v *Verifier) stats() (date string, approved, declined int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	today := v.now().Format("2006-01-02")
	if v.statDate != today {
		return today, 0, 0
	}
	return v.statDate, v.approved, v.declined
}

func (v *Verifier) save() {
	if v.statePath == "" {
		return
	}
	v.mu.Lock()
	recs := make([]pendingRec, 0, len(v.pend))
	for k, p := range v.pend {
		if p.done {
			continue
		}
		recs = append(recs, pendingRec{UserID: k.uid, GroupID: k.gid, GroupMsgID: p.groupMsgID,
			QText: p.qText, QOpts: p.qOpts, CorrectIdx: p.correctIdx, Deadline: p.deadline.Unix()})
	}
	v.mu.Unlock()
	data, err := json.Marshal(recs)
	if err != nil {
		return
	}
	tmp := v.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err == nil {
		_ = os.Rename(tmp, v.statePath)
	}
}

func (v *Verifier) load(bot *telego.Bot) {
	if v.statePath == "" {
		return
	}
	data, err := os.ReadFile(v.statePath)
	if err != nil {
		return
	}
	var recs []pendingRec
	if err := json.Unmarshal(data, &recs); err != nil {
		log.Printf("state load: %v", err)
		return
	}
	for _, r := range recs {
		gid, uid := r.GroupID, r.UserID
		p := &pending{groupMsgID: r.GroupMsgID, qText: r.QText, qOpts: r.QOpts,
			correctIdx: r.CorrectIdx, deadline: time.Unix(r.Deadline, 0)}
		delay := time.Until(p.deadline)
		if delay < time.Second {
			delay = time.Second
		}
		p.timer = time.AfterFunc(delay, func() { v.decline(context.Background(), bot, gid, uid, "timeout") })
		v.mu.Lock()
		v.pend[pkey{gid, uid}] = p
		v.mu.Unlock()
	}
	if len(recs) > 0 {
		log.Printf("restored %d pending verification(s)", len(recs))
	}
}

func (v *Verifier) register(bh *th.BotHandler) {
	bh.Handle(v.onAnswer, th.CallbackDataPrefix(answerPrefix))
	bh.Handle(v.onAdminAction, th.CallbackDataPrefix(adminPrefix))
	bh.Handle(v.onChannelRecheck, th.CallbackDataPrefix(recheckPrefix))
	bh.Handle(v.onJoinRequest, th.AnyChatJoinRequest())
	bh.Handle(v.onSb, th.CommandEqual("sb"))
	bh.Handle(v.onBan, th.CommandEqual("ban"))
	bh.Handle(v.onPing, th.CommandEqual("ping"))
	bh.Handle(v.onStart, th.CommandEqual("start"))
	bh.Handle(v.onStop, th.CommandEqual("stop"))
	bh.Handle(v.onStats, th.CommandEqual("stats"))
	bh.Handle(v.onPkg, th.CommandEqual("pkg"))
	bh.Handle(v.onUse, th.CommandEqual("use"))
	bh.Handle(v.onBug, th.CommandEqual("bug"))
	bh.Handle(v.onNews, th.CommandEqual("news"))
	bh.Handle(v.onHelp, th.CommandEqual("help"))
}

func (v *Verifier) onJoinRequest(ctx *th.Context, update telego.Update) error {
	jr := update.ChatJoinRequest
	if jr == nil || !v.cfg.IsGroup(jr.Chat.ID) {
		return nil
	}
	if !v.isEnabled() {
		log.Printf("verification disabled — leaving join request from %d for manual review", jr.From.ID)
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	gid := jr.Chat.ID
	uid := jr.From.ID
	gidStr, uidStr := strconv.FormatInt(gid, 10), strconv.FormatInt(uid, 10)

	q := v.cfg.randomQuestion()
	text, opts, correctIdx := shuffledQuestion(q)

	mention := fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a>", uid, html.EscapeString(displayName(&jr.From)))
	link := ""
	if v.botUsername != "" {
		link = "https://t.me/" + v.botUsername + "?start=verify"
	}
	// Channel requirement is mentioned as plain text only — the actual follow
	// button lives in the DM step, so users aren't sent away from the verify flow.
	channelHint := ""
	if v.cfg.RequiredChannelID != 0 {
		channelHint = fmt.Sprintf("\n⚠️ 完成验证前还需先关注频道 %s。", html.EscapeString(v.cfg.ChannelDisplay))
	}
	linkText := ""
	if link != "" {
		linkText = fmt.Sprintf("(或 <a href=\"%s\">点此</a>)", link)
	}
	body := fmt.Sprintf("👋 %s 申请加入。请点下方「✅ 点此完成验证」%s 打开机器人私聊完成验证,%d 秒内未完成将被拒绝。%s",
		mention, linkText, v.cfg.TimeoutSeconds, channelHint)

	var rows [][]telego.InlineKeyboardButton
	if link != "" {
		rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: "✅ 点此完成验证", URL: link}))
	}
	rows = append(rows, tu.InlineKeyboardRow(
		telego.InlineKeyboardButton{Text: "👮 管理员直接通过", CallbackData: adminPrefix + "pass:" + gidStr + ":" + uidStr},
		telego.InlineKeyboardButton{Text: "🚫 举报并封禁", CallbackData: adminPrefix + "ban:" + gidStr + ":" + uidStr},
	))

	msgID := 0
	if sent, err := bot.SendMessage(c, tu.Message(tu.ID(gid), body).
		WithParseMode(telego.ModeHTML).
		WithReplyMarkup(tu.InlineKeyboard(rows...)).
		WithLinkPreviewOptions(&telego.LinkPreviewOptions{IsDisabled: true})); err != nil {
		log.Printf("join %d in %d: post challenge failed: %v", uid, gid, err)
		v.adminAlert(c, bot, fmt.Sprintf("⚠️ 群 %d 未能发出用户 %d 的入群验证消息:%v;请手动处理该申请", gid, uid, err))
	} else if sent != nil {
		msgID = sent.MessageID
	}

	key := pkey{gid, uid}
	v.mu.Lock()
	oldMsgID := 0
	if old, ok := v.pend[key]; ok {
		if old.timer != nil {
			old.timer.Stop()
		}
		oldMsgID = old.groupMsgID
	}
	p := &pending{groupMsgID: msgID, qText: text, qOpts: opts, correctIdx: correctIdx,
		deadline: time.Now().Add(time.Duration(v.cfg.TimeoutSeconds) * time.Second)}
	p.timer = time.AfterFunc(time.Until(p.deadline), func() { v.decline(context.Background(), bot, gid, uid, "timeout") })
	v.pend[key] = p
	v.mu.Unlock()
	if oldMsgID != 0 && oldMsgID != msgID {
		v.deleteChallenge(c, bot, gid, oldMsgID) // drop the stale challenge from a previous request
	}
	v.save()
	log.Printf("join %d (@%s) in group %d: pending, in-group verify link posted", uid, jr.From.Username, gid)
	return nil
}

func (v *Verifier) hasPending(uid int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, p := range v.pend {
		if k.uid == uid && !p.done {
			return true
		}
	}
	return false
}

// sendDMChallenge runs when the applicant opens the bot via the deep link.
// Two-step: if a channel is required and not yet joined, ask them to follow it
// first (with a "I've followed, continue" button); otherwise send the quiz.
func (v *Verifier) sendDMChallenge(c context.Context, bot *telego.Bot, uid int64) {
	if !v.hasPending(uid) {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), "你当前没有待处理的入群申请。请先在群里发起加入申请,再点群内的「✅ 点此完成验证」按钮。"))
		return
	}
	if v.cfg.RequiredChannelID != 0 && !v.isChannelMember(c, bot, uid) {
		var rows [][]telego.InlineKeyboardButton
		if curl := v.channelURL(); curl != "" {
			rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: "📢 关注频道 " + v.cfg.ChannelDisplay, URL: curl}))
		}
		rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: "✅ 我已关注,继续", CallbackData: recheckPrefix + strconv.FormatInt(uid, 10)}))
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid),
			fmt.Sprintf("完成验证还差一步:请先关注频道 %s,关注后回到本对话点「✅ 我已关注,继续」。", v.channelLinkHTML())).
			WithParseMode(telego.ModeHTML).
			WithReplyMarkup(tu.InlineKeyboard(rows...)).
			WithLinkPreviewOptions(&telego.LinkPreviewOptions{IsDisabled: true}))
		return
	}
	v.sendQuizzes(c, bot, uid)
}

// sendQuizzes DMs the quiz for every group where this user has a live verification.
func (v *Verifier) sendQuizzes(c context.Context, bot *telego.Bot, uid int64) {
	type dmq struct {
		gid  int64
		text string
		opts []string
	}
	var qs []dmq
	v.mu.Lock()
	for k, p := range v.pend {
		if k.uid == uid && !p.done {
			qs = append(qs, dmq{k.gid, p.qText, p.qOpts})
		}
	}
	v.mu.Unlock()
	for _, dq := range qs {
		gidStr, uidStr := strconv.FormatInt(dq.gid, 10), strconv.FormatInt(uid, 10)
		rows := make([][]telego.InlineKeyboardButton, 0, len(dq.opts))
		for i, opt := range dq.opts {
			rows = append(rows, tu.InlineKeyboardRow(
				telego.InlineKeyboardButton{Text: opt, CallbackData: fmt.Sprintf("%s%s:%s:%d", answerPrefix, gidStr, uidStr, i)}))
		}
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid),
			fmt.Sprintf("请回答下面的问题完成入群验证:\n\n❓ %s", html.EscapeString(dq.text))).
			WithParseMode(telego.ModeHTML).
			WithReplyMarkup(tu.InlineKeyboard(rows...)).
			WithLinkPreviewOptions(&telego.LinkPreviewOptions{IsDisabled: true}))
	}
}

// onChannelRecheck: user tapped "I've followed, continue" — re-check channel then show the quiz.
func (v *Verifier) onChannelRecheck(ctx *th.Context, update telego.Update) error {
	cq := update.CallbackQuery
	if cq == nil {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	uid, _ := strconv.ParseInt(strings.TrimPrefix(cq.Data, recheckPrefix), 10, 64)
	if cq.From.ID != uid {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("这不是你的验证申请,无法操作。").WithShowAlert())
		return nil
	}
	if !v.hasPending(uid) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("该验证已处理或已过期。"))
		return nil
	}
	if v.cfg.RequiredChannelID != 0 && !v.isChannelMember(c, bot, uid) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).
			WithText(fmt.Sprintf("还没检测到你关注 %s,关注后再点一次。", v.cfg.ChannelDisplay)).WithShowAlert())
		return nil
	}
	v.sendQuizzes(c, bot, uid)
	_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("✅ 已关注,请回答下面的问题"))
	return nil
}

func (v *Verifier) onAnswer(ctx *th.Context, update telego.Update) error {
	cq := update.CallbackQuery
	if cq == nil {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	parts := strings.SplitN(strings.TrimPrefix(cq.Data, answerPrefix), ":", 3)
	if len(parts) != 3 {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
		return nil
	}
	gid, _ := strconv.ParseInt(parts[0], 10, 64)
	owner, _ := strconv.ParseInt(parts[1], 10, 64)
	choice, err := strconv.Atoi(parts[2])
	if err != nil {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
		return nil
	}
	if cq.From.ID != owner {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("这不是你的验证申请,无法操作。").WithShowAlert())
		return nil
	}

	v.mu.Lock()
	p, ok := v.pend[pkey{gid, owner}]
	done := !ok || p.done
	correctIdx := -1
	if ok {
		correctIdx = p.correctIdx
	}
	v.mu.Unlock()
	if done {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("该验证已处理或已过期。"))
		return nil
	}

	if choice != correctIdx {
		v.decline(c, bot, gid, owner, "wrong answer")
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("❌ 答错了,已拒绝。可重新申请。").WithShowAlert())
		return nil
	}
	if !v.isChannelMember(c, bot, owner) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).
			WithText(fmt.Sprintf("请先关注频道 %s,关注后再点一次你的答案。", v.cfg.ChannelDisplay)).WithShowAlert())
		return nil
	}
	if v.approve(c, bot, gid, owner) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("✅ 验证通过,已批准加入,欢迎!"))
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(owner), "✅ 验证通过,已批准加入,欢迎!"))
	} else {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("验证已处理,或申请已过期/无法批准,请重新申请。").WithShowAlert())
	}
	return nil
}

func (v *Verifier) onAdminAction(ctx *th.Context, update telego.Update) error {
	cq := update.CallbackQuery
	if cq == nil {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	parts := strings.SplitN(strings.TrimPrefix(cq.Data, adminPrefix), ":", 3)
	if len(parts) != 3 {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
		return nil
	}
	action := parts[0]
	gid, _ := strconv.ParseInt(parts[1], 10, 64)
	target, _ := strconv.ParseInt(parts[2], 10, 64)

	if !v.isGroupAdmin(c, bot, gid, cq.From.ID) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("⛔ 仅群管理员可操作。").WithShowAlert())
		return nil
	}
	switch action {
	case "pass":
		if v.approve(c, bot, gid, target) {
			_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("✅ 已直接通过"))
		} else {
			_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("该申请已处理或无法批准。"))
		}
	case "ban":
		if v.banApplicant(c, bot, gid, target) {
			_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("🚫 已拒绝并永久封禁"))
		} else {
			_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("该申请已处理。"))
		}
	default:
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
	}
	return nil
}

func (v *Verifier) isChannelMember(c context.Context, bot *telego.Bot, userID int64) bool {
	if v.cfg.RequiredChannelID == 0 {
		return true
	}
	cm, err := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(v.cfg.RequiredChannelID), UserID: userID})
	if err != nil {
		log.Printf("getChatMember(channel=%d user=%d): %v", v.cfg.RequiredChannelID, userID, err)
		return false
	}
	switch cm.MemberStatus() {
	case "creator", "administrator", "member":
		return true
	default:
		return cm.MemberIsMember()
	}
}

// channelURL returns a join link for the required channel: an explicit
// channel_invite_url if set (needed for private channels with no @handle), else
// the t.me link derived from an @handle, else "".
func (v *Verifier) channelURL() string {
	if v.cfg.ChannelInviteURL != "" {
		return v.cfg.ChannelInviteURL
	}
	if d := v.cfg.ChannelDisplay; strings.HasPrefix(d, "@") {
		return "https://t.me/" + d[1:]
	}
	return ""
}

// channelLinkHTML returns the channel as a clickable HTML link (or escaped text).
func (v *Verifier) channelLinkHTML() string {
	d := v.cfg.ChannelDisplay
	if d == "" {
		d = "管理员指定的频道"
	}
	if u := v.channelURL(); u != "" {
		return fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(u), html.EscapeString(d))
	}
	return html.EscapeString(d)
}

func (v *Verifier) consume(gid, uid int64) (*pending, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := pkey{gid, uid}
	p, ok := v.pend[key]
	if !ok || p.done {
		return nil, false
	}
	p.done = true
	if p.timer != nil {
		p.timer.Stop()
	}
	delete(v.pend, key)
	return p, true
}

func (v *Verifier) deleteChallenge(c context.Context, bot *telego.Bot, gid int64, msgID int) {
	if msgID != 0 {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(gid), MessageID: msgID})
	}
}

func (v *Verifier) adminAlert(c context.Context, bot *telego.Bot, text string) {
	if v.cfg.AdminLogChatID != 0 {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(v.cfg.AdminLogChatID), text))
	}
}

func (v *Verifier) approve(c context.Context, bot *telego.Bot, gid, uid int64) bool {
	// Peek WITHOUT consuming, so a transient approve failure doesn't strand the
	// applicant — the pending, its timer and the in-group challenge all stay
	// intact for a retry / admin button / timeout.
	v.mu.Lock()
	p, ok := v.pend[pkey{gid, uid}]
	if !ok || p.done {
		v.mu.Unlock()
		return false
	}
	msgID := p.groupMsgID
	v.mu.Unlock()

	if err := bot.ApproveChatJoinRequest(c, &telego.ApproveChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
		log.Printf("approve %d in %d: %v", uid, gid, err)
		v.adminAlert(c, bot, fmt.Sprintf("⚠️ 批准用户 %d 加入群 %d 失败(可能缺权限):%v;已保留申请,可重试或等待超时", uid, gid, err))
		return false
	}
	// Succeeded — now claim the pending and clean up.
	if _, ok := v.consume(gid, uid); !ok {
		return true // a concurrent path already consumed it; the approve still succeeded
	}
	v.deleteChallenge(c, bot, gid, msgID)
	v.recordDecision(true)
	v.save()
	log.Printf("approve user=%d group=%d", uid, gid)
	return true
}

func (v *Verifier) decline(c context.Context, bot *telego.Bot, gid, uid int64, reason string) bool {
	p, ok := v.consume(gid, uid)
	if !ok {
		return false
	}
	if err := bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
		log.Printf("decline %d in %d (%s): %v", uid, gid, reason, err) // benign if request already gone
	}
	v.deleteChallenge(c, bot, gid, p.groupMsgID)
	v.recordDecision(false)
	v.save()
	log.Printf("decline user=%d group=%d (%s)", uid, gid, reason)
	return true
}

func (v *Verifier) banApplicant(c context.Context, bot *telego.Bot, gid, uid int64) bool {
	p, ok := v.consume(gid, uid)
	if !ok {
		return false
	}
	_ = bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid})
	if err := bot.BanChatMember(c, &telego.BanChatMemberParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
		log.Printf("banApplicant %d in %d: %v", uid, gid, err)
		v.adminAlert(c, bot, fmt.Sprintf("⚠️ 封禁用户 %d(群 %d)失败:%v", uid, gid, err))
	}
	v.deleteChallenge(c, bot, gid, p.groupMsgID)
	v.recordDecision(false)
	v.save()
	log.Printf("banApplicant user=%d group=%d (admin report)", uid, gid)
	return true
}
