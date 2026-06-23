package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

const (
	answerPrefix  = "v:"   // applicant answers the quiz (in DM): v:<gid>:<uid>:<nonce>:<idx>
	adminPrefix   = "adm:" // admin override (in group): adm:<action>:<gid>:<uid>
	recheckPrefix = "ch:"  // "I followed the channel, continue" (in DM): ch:<uid>
)

type pkey struct{ gid, uid int64 }

// verifyBot is the slice of the telego.Bot API the verification approve / decline / ban path uses.
// Threading it (instead of *telego.Bot) through approve, decline, banApplicant, applyBan,
// deleteChallenge and adminAlert lets those critical handler branches be unit-tested with a fake
// bot — the test seam the reviews keep asking for. *telego.Bot satisfies it; callers are unchanged.
type verifyBot interface {
	ApproveChatJoinRequest(ctx context.Context, params *telego.ApproveChatJoinRequestParams) error
	DeclineChatJoinRequest(ctx context.Context, params *telego.DeclineChatJoinRequestParams) error
	BanChatMember(ctx context.Context, params *telego.BanChatMemberParams) error
	DeleteMessage(ctx context.Context, params *telego.DeleteMessageParams) error
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

type pending struct {
	groupMsgID int
	qText      string
	qOpts      []string
	correctIdx int
	nonce      string // per-pending token; a quiz button only counts if its nonce matches
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
	Nonce      string   `json:"nonce"`
	Deadline   int64    `json:"deadline"`
}

// newNonce returns a short random token used to bind a DM quiz button to the pending it was
// issued for, so a stale button from a previous (overwritten) request can't answer a new quiz.
func newNonce() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36) // fallback; uniqueness is what matters
	}
	return hex.EncodeToString(b[:])
}

// Verifier holds the bot's runtime state: config, the pending-verification map
// (keyed by group+user), the daily approve/decline counters, and the enabled /
// rich-output toggles. All mutable fields are guarded by mu.
type Verifier struct {
	cfg          *Config
	botUsername  string
	botID        int64
	statePath    string
	warnPath     string
	acPath       string
	loc          *time.Location
	startTime    time.Time
	mu           sync.Mutex
	pend         map[pkey]*pending
	warns        map[pkey]int // group+user -> warning count (persisted)
	enabled      bool
	rich         bool // runtime toggle for rich-message output (init from cfg.RichMessages, flipped by /rich)
	nameSpoiler  bool // hide a joiner's display name behind a Telegram spoiler in the in-group challenge (anti-advert; /spoiler, persisted)
	statDate     string
	approved     int
	declined     int
	acMu         sync.RWMutex // guards the channel-sock-puppet filter's runtime state
	acOn         bool         // /bc toggle (seeded from cfg.BlockChannelSenders, persisted)
	acWhite      map[int64]bool
	chanAlert    map[int64]time.Time   // required-channel -> last "bot can't access" alert (throttle), guarded by mu
	dmLast       map[int64]time.Time   // user -> last DM auto-reply time (throttle), guarded by mu
	queryHits    map[int64][]time.Time // user -> recent private-query times (rate limit), guarded by mu
	lookupOn     bool                  // auto-delete lookup command+answer (seeded from cfg, toggled by /autodel), guarded by mu
	lookupTTL    time.Duration         // how long before that deletion, guarded by mu
	banSecs      int                   // default ban duration in seconds, 0 = permanent (seeded from cfg, set by /bantime), guarded by mu
	vfail        map[pkey]*vfailRec    // group+user -> failed-verification strikes + last-fail time (anti-spam), guarded by mu
	vfailPath    string                // persistence path for vfail
	settingsPath string                // persistence path for runtime settings (verification enabled state)
	adminMu      sync.Mutex            // guards adminCache
	adminCache   map[pkey]time.Time    // group+user -> admin-status cache expiry; only ADMINS are cached (short TTL) so the verify/moderation admin checks skip a GetChatMember round-trip on repeat use
}

func loadStatsLoc(name string) *time.Location {
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.FixedZone("UTC+8", 8*3600)
}

// htmlMessage builds the bot's standard outbound message: HTML parse mode with link
// previews disabled. Chain .WithReplyMarkup / .WithDisableNotification as needed.
func htmlMessage(chatID int64, text string) *telego.SendMessageParams {
	return tu.Message(tu.ID(chatID), text).
		WithParseMode(telego.ModeHTML).
		WithLinkPreviewOptions(&telego.LinkPreviewOptions{IsDisabled: true})
}

// replyParams binds a response to the user's command message. The lookup commands hit
// slow external APIs, so when several are in flight at once their free-floating answers
// could be mistaken for one another — replying to the trigger ties each answer to its
// question. A zero msgID yields nil (no binding).
func replyParams(msgID int) *telego.ReplyParameters {
	if msgID == 0 {
		return nil
	}
	return &telego.ReplyParameters{MessageID: msgID}
}

// NewVerifier builds a Verifier from config: verification starts enabled, rich output
// follows cfg.RichMessages, and the stats timezone is resolved (default UTC+8).
func NewVerifier(cfg *Config) *Verifier {
	v := &Verifier{cfg: cfg, startTime: time.Now(), loc: loadStatsLoc(cfg.StatsTimezone),
		pend: make(map[pkey]*pending), warns: make(map[pkey]int), acWhite: map[int64]bool{},
		chanAlert: map[int64]time.Time{}, dmLast: map[int64]time.Time{}, queryHits: map[int64][]time.Time{},
		adminCache: map[pkey]time.Time{},
		vfail:      map[pkey]*vfailRec{}, banSecs: cfg.BanSeconds,
		enabled: true, rich: cfg.RichMessages, acOn: cfg.BlockChannelSenders,
		nameSpoiler: true} // default ON: spam joiners often set their NAME to an advert; hide it behind a spoiler
	for _, id := range cfg.ChannelWhitelist {
		v.acWhite[id] = true
	}
	// Lookup auto-delete: unset => on at 3 min; 0/negative => off; positive => that many seconds.
	v.lookupTTL = 180 * time.Second
	v.lookupOn = true
	if cfg.LookupTTLSeconds != nil {
		if *cfg.LookupTTLSeconds <= 0 {
			v.lookupOn = false
		} else {
			v.lookupTTL = time.Duration(*cfg.LookupTTLSeconds) * time.Second
		}
	}
	return v
}

// lookupAutoDelete reports the lookup-response auto-delete TTL and whether it's enabled.
func (v *Verifier) lookupAutoDelete() (time.Duration, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lookupTTL, v.lookupOn
}

// setLookupAutoDelete updates the toggle and, when ttl > 0, the duration (/autodel).
func (v *Verifier) setLookupAutoDelete(ttl time.Duration, on bool) {
	v.mu.Lock()
	if ttl > 0 {
		v.lookupTTL = ttl
	}
	v.lookupOn = on
	v.mu.Unlock()
}

// scheduleLookupCleanup deletes a lookup command and its answer after the configured TTL,
// when auto-delete is enabled — so the group doesn't fill up with query/answer pairs. Uses
// a fresh context because the timer fires minutes after the request context is done.
func (v *Verifier) scheduleLookupCleanup(bot *telego.Bot, chatID int64, cmdMsgID, respMsgID int) {
	ttl, on := v.lookupAutoDelete()
	if !on || respMsgID == 0 || chatID >= 0 {
		return // private chats have non-negative ids — nothing to keep tidy there
	}
	time.AfterFunc(ttl, func() {
		_ = bot.DeleteMessage(context.Background(), &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: respMsgID})
		if cmdMsgID != 0 {
			_ = bot.DeleteMessage(context.Background(), &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: cmdMsgID})
		}
	})
}

// msgID returns m's id, or 0 if m is nil.
func msgID(m *telego.Message) int {
	if m == nil {
		return 0
	}
	return m.MessageID
}

// replyLookupPlain sends a PLAIN-text reply to a lookup command (a usage hint, "not found",
// disambiguation, or transient error) and schedules the same timed cleanup as a real answer,
// so the command and this reply are removed together after lookup_ttl instead of the command
// lingering. Plain text — not HTML — because these messages carry literal <包名> placeholders
// that HTML parse mode would reject. Mirrors sendRichOrHTML's reply+cleanup for the success path.
func (v *Verifier) replyLookupPlain(c context.Context, bot *telego.Bot, chatID int64, replyTo int, text string) {
	m := tu.Message(tu.ID(chatID), text)
	if rp := replyParams(replyTo); rp != nil {
		m = m.WithReplyParameters(rp)
	}
	sent, _ := bot.SendMessage(c, m)
	v.scheduleLookupCleanup(bot, chatID, replyTo, msgID(sent))
}

// replyLookupHTML sends an HTML-formatted reply to a lookup command and schedules the timed
// cleanup — the HTML sibling of replyLookupPlain / sendRichOrHTML. The caller is responsible
// for html-escaping any dynamic content in htmlText. Returns the sent message (may be nil).
func (v *Verifier) replyLookupHTML(c context.Context, bot *telego.Bot, chatID int64, replyTo int, htmlText string) *telego.Message {
	m := htmlMessage(chatID, htmlText)
	if rp := replyParams(replyTo); rp != nil {
		m = m.WithReplyParameters(rp)
	}
	sent, _ := bot.SendMessage(c, m)
	v.scheduleLookupCleanup(bot, chatID, replyTo, msgID(sent))
	return sent
}

const privateQueryWindow = time.Minute

// queryRateOK records a private-chat lookup for userID and reports whether it is within the
// per-minute limit (sliding window, cfg.PrivateQueryPerMin). Groups are never rate-limited.
func (v *Verifier) queryRateOK(userID int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-privateQueryWindow)
	kept := v.queryHits[userID][:0]
	for _, t := range v.queryHits[userID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= v.cfg.PrivateQueryPerMin {
		v.queryHits[userID] = kept
		return false
	}
	v.queryHits[userID] = append(kept, now)
	if len(v.queryHits) > dmMapMax { // bound the map: drop fully-expired users
		for u, ts := range v.queryHits {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(v.queryHits, u)
			}
		}
		if len(v.queryHits) > dmMapMax { // still over (all live) — hard-clear like dmLast
			v.queryHits = map[int64][]time.Time{}
		}
	}
	return true
}

// dmOrGroup reports whether msg is in a guarded group or a private chat — where the cheap,
// no-external-request member commands (/help /ping /stats) are allowed WITHOUT a rate limit
// (only the API-hitting lookups are throttled, via queryAllowed).
func (v *Verifier) dmOrGroup(msg *telego.Message) bool {
	return v.cfg.IsGroup(msg.Chat.ID) || msg.Chat.Type == "private"
}

// queryAllowed reports whether a lookup command may run for this message: unlimited in a
// guarded group, rate-limited per user in a private chat (anti-abuse), and not elsewhere. It
// sends the rate-limit notice itself when a DM user is over the limit.
func (v *Verifier) queryAllowed(ctx *th.Context, msg *telego.Message) bool {
	if v.cfg.IsGroup(msg.Chat.ID) {
		return true
	}
	if msg.Chat.Type == "private" && msg.From != nil {
		if v.queryRateOK(msg.From.ID) {
			return true
		}
		_, _ = ctx.Bot().SendMessage(ctx.Context(), tu.Message(tu.ID(msg.Chat.ID),
			fmt.Sprintf("⏳ 查询太频繁:私聊每分钟最多 %d 次,请稍后再试(在群里不限次)。", v.cfg.PrivateQueryPerMin)))
		return false
	}
	return false
}

func (v *Verifier) isEnabled() bool   { v.mu.Lock(); defer v.mu.Unlock(); return v.enabled }
func (v *Verifier) setEnabled(b bool) { v.mu.Lock(); v.enabled = b; v.mu.Unlock(); v.saveSettings() }

func (v *Verifier) isRichEnabled() bool { v.mu.Lock(); defer v.mu.Unlock(); return v.rich }
func (v *Verifier) toggleRich() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.rich = !v.rich
	return v.rich
}

func (v *Verifier) nameSpoilerOn() bool { v.mu.Lock(); defer v.mu.Unlock(); return v.nameSpoiler }

// toggleNameSpoiler flips the name-spoiler and persists it (like /start /stop) so a /spoiler choice
// survives a restart.
func (v *Verifier) toggleNameSpoiler() bool {
	v.mu.Lock()
	v.nameSpoiler = !v.nameSpoiler
	on := v.nameSpoiler
	v.mu.Unlock()
	v.saveSettings()
	return on
}

// joinerLabel renders the applicant's name for the in-group challenge. Normally a clickable
// mention; when the name-spoiler is on, the HTML-escaped name is hidden behind a Telegram spoiler —
// a single, always-valid entity (NOT a nested link, so it can never produce an HTML parse error
// that would break the critical challenge post) — so a spammer who set their display name to an
// advert can't show it in the group without a deliberate tap. The 👮/🚫 buttons act by id, so
// losing the click-through on a spoilered name costs admins nothing.
func joinerLabel(uid int64, name string, spoiler bool) string {
	esc := html.EscapeString(name)
	if spoiler {
		return "<tg-spoiler>" + esc + "</tg-spoiler>"
	}
	return fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a>", uid, esc)
}

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

// stateWriteMu serializes all state-file writes. The files are small and written rarely, so a
// single global lock is cheap and removes the race where two concurrent saves (e.g. an approve
// and a timeout-decline) would otherwise interleave on a shared temp file.
var stateWriteMu sync.Mutex

// writeJSONFile atomically writes val as JSON to path: marshal, write to a UNIQUE temp file in
// the same directory, then rename. The unique temp name (vs a fixed "path.tmp") means
// concurrent writers can't clobber each other's temp; the global lock serializes the rename.
// Any failure is logged so a missing/unwritable state directory is visible.
func writeJSONFile(path string, val any) {
	data, err := json.Marshal(val)
	if err != nil {
		log.Printf("state: marshal %s: %v", path, err)
		return
	}
	stateWriteMu.Lock()
	defer stateWriteMu.Unlock()
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*") // mode 0600
	if err != nil {
		log.Printf("state: temp for %s: %v", path, err)
		return
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync() // flush data to disk before the rename so a crash can't leave a torn/zero file
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		log.Printf("state: write %s: %v", path, werr)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("state: rename %s: %v", path, err)
		return
	}
	// fsync the directory so the rename itself is durable across a power loss.
	if d, derr := os.Open(filepath.Dir(path)); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
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
			QText: p.qText, QOpts: p.qOpts, CorrectIdx: p.correctIdx, Nonce: p.nonce, Deadline: p.deadline.Unix()})
	}
	v.mu.Unlock()
	writeJSONFile(v.statePath, recs)
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
			correctIdx: r.CorrectIdx, nonce: r.Nonce, deadline: time.Unix(r.Deadline, 0)}
		delay := time.Until(p.deadline)
		if delay < time.Second {
			delay = time.Second
		}
		v.mu.Lock()
		v.pend[pkey{gid, uid}] = p
		// arm the timer with the entry already in the map (mirrors onJoinRequest), so a
		// near-immediate fire can't decline()->consume() before the entry exists. The captured
		// nonce makes the decline a no-op if a fresh request has since replaced this pending.
		nonce := p.nonce
		p.timer = time.AfterFunc(delay, func() { v.decline(context.Background(), bot, gid, uid, nonce, "timeout") })
		v.mu.Unlock()
	}
	if len(recs) > 0 {
		log.Printf("restored %d pending verification(s)", len(recs))
	}
}

func (v *Verifier) register(bh *th.BotHandler) {
	// Contain a panic in any single handler (e.g. an unexpected nil) so one bad
	// update can't take the whole bot down — the update is dropped, the bot lives.
	bh.Use(th.PanicRecoveryHandler(func(recovered any) error {
		log.Printf("recovered from handler panic: %v", recovered)
		return nil
	}))
	// drop channel sock-puppet posts before any handler (no-op unless block_channel_senders)
	bh.Use(v.antispam)
	bh.Handle(v.onAnswer, th.CallbackDataPrefix(answerPrefix))
	bh.Handle(v.onAdminAction, th.CallbackDataPrefix(adminPrefix))
	bh.Handle(v.onChannelRecheck, th.CallbackDataPrefix(recheckPrefix))
	bh.Handle(v.onJoinRequest, th.AnyChatJoinRequest())
	bh.Handle(v.onMyChatMember, th.AnyMyChatMember())
	// before the command handlers: any private message except /start (verify deep link)
	// gets the unified auto-reply — so DM'd commands respond instead of silently no-opping
	bh.Handle(v.onPrivateDM, privateNonStart)
	bh.Handle(v.onSb, th.CommandEqual("sb"))
	bh.Handle(v.onBan, th.CommandEqual("ban"))
	bh.Handle(v.onWarn, th.CommandEqual("warn"))
	bh.Handle(v.onClearWarn, th.CommandEqual("clearwarn"))
	bh.Handle(v.onBc, th.CommandEqual("bc"))
	bh.Handle(v.onPing, th.CommandEqual("ping"))
	bh.Handle(v.onStart, th.CommandEqual("start"))
	bh.Handle(v.onStop, th.CommandEqual("stop"))
	bh.Handle(v.onStats, th.CommandEqual("stats"))
	bh.Handle(v.onPkg, th.CommandEqual("pkg"))
	bh.Handle(v.onUse, th.CommandEqual("use"))
	bh.Handle(v.onBug, th.CommandEqual("bug"))
	bh.Handle(v.onNews, th.CommandEqual("news"))
	bh.Handle(v.onWiki, th.CommandEqual("wiki"))
	bh.Handle(v.onBbs, th.CommandEqual("bbs"))
	bh.Handle(v.onPkgs, th.CommandEqual("pkgs"))
	bh.Handle(v.onPkgs, th.CommandEqual("distro")) // /distro kept as an alias
	bh.Handle(v.onArm, th.CommandEqual("arm"))
	bh.Handle(v.onArmpkgs, th.CommandEqual("armpkgs"))
	bh.Handle(v.onRich, th.CommandEqual("rich"))
	bh.Handle(v.onSpoiler, th.CommandEqual("spoiler"))
	bh.Handle(v.onAutoDel, th.CommandEqual("autodel"))
	bh.Handle(v.onBanTime, th.CommandEqual("bantime"))
	bh.Handle(v.onMute, th.CommandEqual("mute"))
	bh.Handle(v.onUnmute, th.CommandEqual("unmute"))
	bh.Handle(v.onHelp, th.CommandEqual("help"))
}

// onMyChatMember auto-leaves any group or channel the bot is added to that isn't a
// configured chat (guarded group / required channel / feed target / admin-log). So
// being pulled into a random group is a no-op and the bot removes itself instead of
// lingering. To add a NEW guarded group, put its id in the config first, then add the bot.
func (v *Verifier) onMyChatMember(ctx *th.Context, update telego.Update) error {
	cm := update.MyChatMember
	if cm == nil || cm.Chat.Type == "private" {
		return nil
	}
	switch cm.NewChatMember.MemberStatus() {
	case "left", "kicked": // the bot was removed — nothing to do
		return nil
	}
	if v.cfg.IsKnownChat(cm.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	log.Printf("auto-leave: leaving unauthorized chat %d (%q, %s)", cm.Chat.ID, cm.Chat.Title, cm.Chat.Type)
	if err := bot.LeaveChat(c, &telego.LeaveChatParams{ChatID: tu.ID(cm.Chat.ID)}); err != nil {
		log.Printf("auto-leave: failed to leave %d: %v", cm.Chat.ID, err)
		return nil
	}
	v.adminAlert(c, bot, fmt.Sprintf("🚪 已自动退出未授权聊天:%s(id %d,%s)", cm.Chat.Title, cm.Chat.ID, cm.Chat.Type))
	return nil
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
	// Anti-spam cooldown: a recently-failed applicant must wait out cfg.VerifyRetrySeconds
	// before re-applying (they were told the wait time when declined). Decline early re-tries
	// silently rather than reposting a challenge.
	if wait := v.verifyCooldownRemaining(gid, uid); wait > 0 {
		if err := bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
			log.Printf("verify cooldown: decline %d in %d failed: %v", uid, gid, err)
		}
		log.Printf("verify cooldown: declined early re-apply from %d in %d (%ds left)", uid, gid, int(wait.Seconds())+1)
		return nil
	}
	gidStr, uidStr := strconv.FormatInt(gid, 10), strconv.FormatInt(uid, 10)

	q := v.cfg.randomQuestion(gid)
	text, opts, correctIdx := shuffledQuestion(q)

	mention := joinerLabel(uid, displayName(&jr.From), v.nameSpoilerOn())
	link := ""
	if v.botUsername != "" {
		link = "https://t.me/" + v.botUsername + "?start=verify"
	}
	// Channel requirement is mentioned as plain text only — the actual follow
	// button lives in the DM step, so users aren't sent away from the verify flow.
	channelHint := ""
	if v.cfg.requiredChannel(gid) != 0 {
		channelHint = fmt.Sprintf("\n⚠️ 完成验证前还需先关注频道 %s。", html.EscapeString(v.cfg.channelDisplay(gid)))
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
	if sent, err := bot.SendMessage(c, htmlMessage(gid, body).
		WithReplyMarkup(tu.InlineKeyboard(rows...))); err != nil {
		log.Printf("join %d in %d: post challenge failed: %v", uid, gid, err)
		v.adminAlert(c, bot, fmt.Sprintf("⚠️ 群 %d 未能发出用户 %d 的入群验证消息:%v;请手动处理该申请", gid, uid, err))
	} else if sent != nil {
		msgID = sent.MessageID
	}

	key := pkey{gid, uid}
	v.mu.Lock()
	oldMsgID := 0
	if old, ok := v.pend[key]; ok {
		old.done = true // mark replaced, so a stale callback for it bails even before the nonce check
		if old.timer != nil {
			old.timer.Stop()
		}
		oldMsgID = old.groupMsgID
	}
	p := &pending{groupMsgID: msgID, qText: text, qOpts: opts, correctIdx: correctIdx, nonce: newNonce(),
		deadline: time.Now().Add(time.Duration(v.cfg.TimeoutSeconds) * time.Second)}
	nonce := p.nonce // captured so this pending's timeout only declines THIS pending (not a later one)
	p.timer = time.AfterFunc(time.Until(p.deadline), func() { v.decline(context.Background(), bot, gid, uid, nonce, "timeout") })
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

// firstPending returns the group id of one of the user's live verifications. Used to
// resolve a single channel for the DM follow-prompt (groups usually share one channel);
// the per-group channel is still enforced per group at answer time.
func (v *Verifier) firstPending(uid int64) (int64, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, p := range v.pend {
		if k.uid == uid && !p.done {
			return k.gid, true
		}
	}
	return 0, false
}

// sendDMChallenge runs when the applicant opens the bot via the deep link.
// Two-step: if a channel is required and not yet joined, ask them to follow it
// first (with a "I've followed, continue" button); otherwise send the quiz.
func (v *Verifier) sendDMChallenge(c context.Context, bot *telego.Bot, uid int64) {
	gid, ok := v.firstPending(uid)
	if !ok {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), "你当前没有待处理的入群申请。请先在群里发起加入申请,再点群内的「✅ 点此完成验证」按钮。"))
		return
	}
	// The follow-prompt uses the first pending group's channel (groups usually share one);
	// the per-group channel is still enforced at answer time in onAnswer.
	if v.cfg.requiredChannel(gid) != 0 && !v.isChannelMember(c, bot, gid, uid) {
		var rows [][]telego.InlineKeyboardButton
		if curl := v.channelURL(gid); curl != "" {
			rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: "📢 关注频道 " + v.cfg.channelDisplay(gid), URL: curl}))
		}
		rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: "✅ 我已关注,继续",
			CallbackData: recheckPrefix + strconv.FormatInt(gid, 10) + ":" + strconv.FormatInt(uid, 10)}))
		_, _ = bot.SendMessage(c, htmlMessage(uid,
			fmt.Sprintf("完成验证还差一步:请先关注频道 %s,关注后回到本对话点「✅ 我已关注,继续」。", v.channelLinkHTML(gid))).
			WithReplyMarkup(tu.InlineKeyboard(rows...)))
		return
	}
	v.sendQuizzes(c, bot, uid)
}

// sendQuizzes DMs the quiz for every group where this user has a live verification.
func (v *Verifier) sendQuizzes(c context.Context, bot *telego.Bot, uid int64) {
	type dmq struct {
		gid   int64
		text  string
		opts  []string
		nonce string
	}
	var qs []dmq
	v.mu.Lock()
	for k, p := range v.pend {
		if k.uid == uid && !p.done {
			qs = append(qs, dmq{k.gid, p.qText, p.qOpts, p.nonce})
		}
	}
	v.mu.Unlock()
	for _, dq := range qs {
		gidStr, uidStr := strconv.FormatInt(dq.gid, 10), strconv.FormatInt(uid, 10)
		rows := make([][]telego.InlineKeyboardButton, 0, len(dq.opts))
		for i, opt := range dq.opts {
			rows = append(rows, tu.InlineKeyboardRow(
				telego.InlineKeyboardButton{Text: opt, CallbackData: fmt.Sprintf("%s%s:%s:%s:%d", answerPrefix, gidStr, uidStr, dq.nonce, i)}))
		}
		_, _ = bot.SendMessage(c, htmlMessage(uid,
			fmt.Sprintf("请回答下面的问题完成入群验证:\n\n❓ %s", html.EscapeString(dq.text))).
			WithReplyMarkup(tu.InlineKeyboard(rows...)))
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
	parts := strings.SplitN(strings.TrimPrefix(cq.Data, recheckPrefix), ":", 2)
	if len(parts) != 2 {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
		return nil
	}
	gid, _ := strconv.ParseInt(parts[0], 10, 64)
	uid, _ := strconv.ParseInt(parts[1], 10, 64)
	if cq.From.ID != uid {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("这不是你的验证申请,无法操作。").WithShowAlert())
		return nil
	}
	if !v.hasPending(uid) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("该验证已处理或已过期。"))
		return nil
	}
	if v.cfg.requiredChannel(gid) != 0 && !v.isChannelMember(c, bot, gid, uid) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).
			WithText(fmt.Sprintf("还没检测到你关注 %s,关注后再点一次。", v.cfg.channelDisplay(gid))).WithShowAlert())
		return nil
	}
	// ACK first so the button stops spinning, THEN send the quiz DM(s) — sendQuizzes swallows send
	// errors, so the early ack loses no feedback (the channel-membership check above stays before
	// the ack because its toast is result-driven).
	_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("✅ 已关注,请回答下面的问题"))
	v.sendQuizzes(c, bot, uid)
	return nil
}

func (v *Verifier) onAnswer(ctx *th.Context, update telego.Update) error {
	cq := update.CallbackQuery
	if cq == nil {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	// callback data: v:<gid>:<uid>:<nonce>:<idx> (current). A legacy 3-part button
	// v:<gid>:<uid>:<idx> from a pre-nonce version still on a user's screen across the upgrade
	// restart is accepted with an empty nonce, which matches a restored pending (nonce "").
	parts := strings.Split(strings.TrimPrefix(cq.Data, answerPrefix), ":")
	var nonce, idxStr string
	switch len(parts) {
	case 4:
		nonce, idxStr = parts[2], parts[3]
	case 3:
		nonce, idxStr = "", parts[2]
	default:
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
		return nil
	}
	gid, _ := strconv.ParseInt(parts[0], 10, 64)
	owner, _ := strconv.ParseInt(parts[1], 10, 64)
	choice, err := strconv.Atoi(idxStr)
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
	correctIdx, curNonce := -1, ""
	if ok {
		correctIdx, curNonce = p.correctIdx, p.nonce
	}
	v.mu.Unlock()
	if done {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("该验证已处理或已过期。"))
		return nil
	}
	if nonce != curNonce {
		// A stale button from a previous (overwritten) request — don't let it answer this quiz.
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("该题目已过期,请重新打开验证链接获取新题。").WithShowAlert())
		return nil
	}

	if choice != correctIdx {
		_, banned := v.decline(c, bot, gid, owner, nonce, "wrong answer")
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(v.wrongAnswerText(banned)).WithShowAlert())
		return nil
	}
	if !v.isChannelMember(c, bot, gid, owner) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).
			WithText(fmt.Sprintf("请先关注频道 %s,关注后再点一次你的答案。", v.cfg.channelDisplay(gid))).WithShowAlert())
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
		p, ok := v.claimPending(gid, target)
		if !ok {
			_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("该申请已处理或无法批准。"))
			return nil
		}
		// ACK first so the button stops spinning, THEN do the approve round-trip(s). A failed
		// approve reopens the pending and alerts admins (executeApprove), so the early ack is safe.
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("✅ 已直接通过"))
		v.executeApprove(c, bot, gid, target, p)
	case "ban":
		p, ok := v.consume(gid, target)
		if !ok {
			_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("该申请已处理。"))
			return nil
		}
		// ACK first (button stops spinning), THEN decline/ban/delete. A ban failure is surfaced
		// via adminAlert (executeBan) and the applicant is declined either way.
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(fmt.Sprintf("🚫 已拒绝并封禁(%s)", banDurationText(v.banDuration()))))
		v.executeBan(c, bot, gid, target, p)
	default:
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
	}
	return nil
}

func (v *Verifier) isChannelMember(c context.Context, bot *telego.Bot, gid, userID int64) bool {
	rc := v.cfg.requiredChannel(gid)
	if rc == 0 {
		return true
	}
	cm, err := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(rc), UserID: userID})
	if err != nil {
		// Distinguish "the bot itself can't read this channel" (a misconfiguration — the bot
		// isn't an admin there) from a per-user/transient error. If the bot can't even see its
		// OWN membership, the requirement is unenforceable, so fail OPEN — a permission slip
		// must NOT lock every applicant out — and alert admins instead of silently blocking.
		if v.botID != 0 {
			if _, e2 := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(rc), UserID: v.botID}); e2 != nil {
				open := v.cfg.failOpenChannel()
				log.Printf("isChannelMember: bot cannot access required channel %d (%v) for applicant %d; fail_open=%v — make the bot an admin of that channel", rc, e2, userID, open)
				v.channelAccessAlert(c, bot, rc)
				return open // configurable: default fail-open (don't lock everyone out); strict deployments set required_channel_fail_open:false
			}
		}
		log.Printf("getChatMember(channel=%d user=%d): %v", rc, userID, err)
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
func (v *Verifier) channelURL(gid int64) string {
	if u := v.cfg.channelInvite(gid); u != "" {
		return u
	}
	if d := v.cfg.channelDisplay(gid); strings.HasPrefix(d, "@") {
		return "https://t.me/" + d[1:]
	}
	return ""
}

// channelLinkHTML returns the channel as a clickable HTML link (or escaped text).
func (v *Verifier) channelLinkHTML(gid int64) string {
	d := v.cfg.channelDisplay(gid)
	if d == "" {
		d = "管理员指定的频道"
	}
	if u := v.channelURL(gid); u != "" {
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

// consumeNonce is consume but only claims the pending if its nonce still matches — used by the
// timeout (and wrong-answer) path so a STALE timer/callback from a since-replaced request can't
// decline/strike/ban a freshly re-issued pending under the same (gid,uid) key.
func (v *Verifier) consumeNonce(gid, uid int64, nonce string) (*pending, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := pkey{gid, uid}
	p, ok := v.pend[key]
	if !ok || p.done || p.nonce != nonce {
		return nil, false // gone, already handled, or a different (newer) pending now holds the key
	}
	p.done = true
	if p.timer != nil {
		p.timer.Stop()
	}
	delete(v.pend, key)
	return p, true
}

func (v *Verifier) deleteChallenge(c context.Context, bot verifyBot, gid int64, msgID int) {
	if msgID != 0 {
		_ = bot.DeleteMessage(c, &telego.DeleteMessageParams{ChatID: tu.ID(gid), MessageID: msgID})
	}
}

func (v *Verifier) adminAlert(c context.Context, bot verifyBot, text string) {
	if v.cfg.AdminLogChatID != 0 {
		if _, err := bot.SendMessage(c, tu.Message(tu.ID(v.cfg.AdminLogChatID), text)); err != nil {
			log.Printf("adminAlert to %d failed (check admin_log_chat_id / bot membership): %v", v.cfg.AdminLogChatID, err)
		}
	}
}

// failAlert surfaces a failure notice to admins: to the admin-log chat if one is configured,
// otherwise to the group itself (gid) where the acting admin is. The ack-first admin buttons answer
// the callback optimistically, so this guarantees a rare approve/ban failure is never invisible
// (it would otherwise only reach the server log when admin_log_chat_id is unset).
func (v *Verifier) failAlert(c context.Context, bot verifyBot, gid int64, text string) {
	target := v.cfg.AdminLogChatID
	if target == 0 {
		target = gid
	}
	if _, err := bot.SendMessage(c, tu.Message(tu.ID(target), text)); err != nil {
		log.Printf("failAlert to %d failed: %v", target, err)
	}
}

// channelAccessAlert warns admins that the bot can't read a required channel (so the
// follow-gate can't be enforced and applicants are being passed through). Throttled to at
// most once per 10 minutes per channel so a busy join queue doesn't flood the admin log.
func (v *Verifier) channelAccessAlert(c context.Context, bot *telego.Bot, channelID int64) {
	v.mu.Lock()
	if last, ok := v.chanAlert[channelID]; ok && time.Since(last) < 10*time.Minute {
		v.mu.Unlock()
		return
	}
	v.chanAlert[channelID] = time.Now()
	v.mu.Unlock()
	mode := "正在放行通过答题的用户(fail-open)" // matches the default
	if !v.cfg.failOpenChannel() {
		mode = "正在拦下这些申请、让用户稍后重试(fail-closed)"
	}
	v.adminAlert(c, bot, fmt.Sprintf("⚠️ 机器人无法读取必关频道 %d 的成员(可能已不是该频道管理员)——关注门槛暂时无法核验,%s。请把机器人重新设为该频道管理员。", channelID, mode))
}

// claimPending atomically marks a pending done and stops its timeout timer but KEEPS it in the map,
// so a FAILED network action can reopenPending() it (re-arm the timeout) instead of stranding the
// applicant. Returns the claimed pending, or ok=false if it is gone/already handled. consume() is
// the sibling that DELETES — use it where there is no reopen-on-failure (e.g. a ban).
func (v *Verifier) claimPending(gid, uid int64) (*pending, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{gid, uid}]
	if !ok || p.done {
		return nil, false
	}
	p.done = true
	if p.timer != nil {
		p.timer.Stop()
	}
	return p, true
}

// approve claims the pending (stopping its timeout so the timer can't decline/strike/auto-ban a
// user we're about to approve) and approves the join request. A callback handler that wants to
// ACK the button first (so it stops spinning) can instead claimPending() itself, answer the
// callback, then call executeApprove() with the claimed pending.
func (v *Verifier) approve(c context.Context, bot verifyBot, gid, uid int64) bool {
	p, ok := v.claimPending(gid, uid)
	if !ok {
		return false
	}
	return v.executeApprove(c, bot, gid, uid, p)
}

// executeApprove runs the network approve + cleanup for an ALREADY-claimed pending p. On failure it
// reopens p as retryable (re-arms the timeout) so a transient error doesn't strand the applicant.
func (v *Verifier) executeApprove(c context.Context, bot verifyBot, gid, uid int64, p *pending) bool {
	if err := bot.ApproveChatJoinRequest(c, &telego.ApproveChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
		log.Printf("approve %d in %d: %v", uid, gid, err)
		v.failAlert(c, bot, gid, fmt.Sprintf("⚠️ 批准用户 %d 加入群 %d 失败(可能缺权限):%v;已保留申请,可重试或等待超时", uid, gid, err))
		v.reopenPending(bot, gid, uid, p) // restore as retryable (re-arm the timeout)
		return false
	}
	// Succeeded — drop the (already-claimed) pending and clean up. Only delete if it's still ours,
	// so a request that replaced it while the approve was in flight isn't clobbered.
	v.mu.Lock()
	if cur, ok := v.pend[pkey{gid, uid}]; ok && cur == p {
		delete(v.pend, pkey{gid, uid})
	}
	v.mu.Unlock()
	v.clearVerifyFails(gid, uid) // verified successfully — reset any failure strikes
	v.deleteChallenge(c, bot, gid, p.groupMsgID)
	v.recordDecision(true)
	v.save()
	log.Printf("approve user=%d group=%d", uid, gid)
	return true
}

// reopenPending re-arms a pending that was claimed for an approve that then FAILED, so the
// applicant can still retry, be approved by an admin, or time out normally. No-op if a newer
// request has since replaced the entry, or it was otherwise consumed.
func (v *Verifier) reopenPending(bot verifyBot, gid, uid int64, p *pending) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if cur, ok := v.pend[pkey{gid, uid}]; !ok || cur != p || !p.done {
		return // replaced or already consumed — leave it alone
	}
	p.done = false
	delay := time.Until(p.deadline)
	if delay < time.Second {
		delay = time.Second
	}
	nonce := p.nonce // mirror onJoinRequest: a background-context timer that only declines THIS pending
	p.timer = time.AfterFunc(delay, func() { v.decline(context.Background(), bot, gid, uid, nonce, "timeout") })
}

// wrongAnswerText is the callback alert shown after a wrong answer: a ban notice if this
// failure triggered the auto-ban, otherwise a decline + retry-after-cooldown hint.
func (v *Verifier) wrongAnswerText(banned bool) string {
	if banned {
		return "❌ 验证连续失败多次,已被封禁。"
	}
	if s := v.cfg.VerifyRetrySeconds; s > 0 {
		return fmt.Sprintf("❌ 答错了,已拒绝。请 %d 秒后重新申请。", s)
	}
	return "❌ 答错了,已拒绝。可重新申请。"
}

// decline rejects a failed verification (wrong answer / timeout). nonce identifies the exact
// pending being rejected, so a stale timer can't decline a since-replaced one (see consumeNonce).
// It records a strike; once an applicant reaches cfg.VerifyMaxFails strikes it is banned (for
// the configured duration) instead of retrying forever. Returns handled=false if there was no
// matching live pending, and banned=true if this failure crossed the auto-ban threshold.
func (v *Verifier) decline(c context.Context, bot verifyBot, gid, uid int64, nonce, reason string) (handled, banned bool) {
	p, ok := v.consumeNonce(gid, uid, nonce)
	if !ok {
		return false, false
	}
	v.deleteChallenge(c, bot, gid, p.groupMsgID)
	v.recordDecision(false)
	count, doBan := v.recordVerifyFail(gid, uid)

	_ = bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}) // benign if already gone
	if doBan {
		secs := v.banDuration()
		if err := v.applyBan(c, bot, gid, uid, secs, false); err != nil {
			log.Printf("verify auto-ban %d in %d: %v", uid, gid, err)
			v.adminAlert(c, bot, fmt.Sprintf("⚠️ 用户 %d 在群 %d 验证连续失败 %d 次,自动封禁失败(可能缺权限):%v", uid, gid, count, err))
			banned = false
		} else {
			v.adminAlert(c, bot, fmt.Sprintf("🚫 用户 %d 在群 %d 验证连续失败 %d 次,已自动封禁(%s)", uid, gid, count, banDurationText(secs)))
			banned = true
		}
		if banned {
			v.clearVerifyFails(gid, uid) // ONLY on a successful ban (so a later unban starts fresh).
			// On ban FAILURE keep the strikes: the threshold stays tripped, every further failure
			// re-attempts the ban and re-alerts admins, and the cooldown keeps throttling — so a
			// missing "ban users" right can't turn the cap into an infinite-retry loop.
		}
	}
	v.save()
	log.Printf("decline user=%d group=%d (%s) fails=%d banned=%v", uid, gid, reason, count, banned)
	return true, banned
}

// banApplicant declines the join request and bans the user. It returns handled=false if there
// was no live pending to act on, and banned=false if the BanChatMember call failed (e.g. the
// bot lacks ban rights) — so the admin gets honest feedback instead of a false "banned".
func (v *Verifier) banApplicant(c context.Context, bot verifyBot, gid, uid int64) (handled, banned bool) {
	p, ok := v.consume(gid, uid)
	if !ok {
		return false, false
	}
	return true, v.executeBan(c, bot, gid, uid, p)
}

// executeBan declines + bans an ALREADY-consumed applicant and clears the challenge. A callback
// handler can consume() + ACK the button first, then call this, so the button doesn't spin through
// the decline/ban/delete round-trips. Returns whether the ban itself succeeded (a failure is
// surfaced via adminAlert; the applicant is still declined regardless).
func (v *Verifier) executeBan(c context.Context, bot verifyBot, gid, uid int64, p *pending) (banned bool) {
	_ = bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid})
	banned = true
	if err := v.applyBan(c, bot, gid, uid, v.banDuration(), true); err != nil { // honour /bantime like the other ban paths
		banned = false
		log.Printf("banApplicant %d in %d: %v", uid, gid, err)
		v.failAlert(c, bot, gid, fmt.Sprintf("⚠️ 封禁用户 %d(群 %d)失败(可能缺权限):%v;申请已拒绝,请手动封禁", uid, gid, err))
	}
	v.deleteChallenge(c, bot, gid, p.groupMsgID)
	v.recordDecision(false)
	v.save()
	log.Printf("banApplicant user=%d group=%d banned=%v (admin report)", uid, gid, banned)
	return banned
}
