package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/moderate"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

const (
	answerPrefix  = "v:"   // applicant answers the quiz (in DM): v:<gid>:<uid>:<nonce>:<idx>
	adminPrefix   = "adm:" // admin override (in group): adm:<action>:<gid>:<uid>
	recheckPrefix = "ch:"  // "I followed the channel, continue" (in DM): ch:<uid>
)

// Bound queues and mutable counters before adversarial traffic can exhaust memory.
const (
	pendingGlobalCap        = 2000
	pendingPerGroupCap      = 500
	pendingCapAlertCooldown = 10 * time.Minute
)

type pendingStartStatus uint8

const (
	pendingStarted pendingStartStatus = iota
	pendingBlockedCapacity
	pendingBlockedTerminal
)

type pkey struct{ gid, uid int64 }

// verifyBot is the Telegram API used by verification settlement.
type verifyBot interface {
	ApproveChatJoinRequest(ctx context.Context, params *telego.ApproveChatJoinRequestParams) error
	DeclineChatJoinRequest(ctx context.Context, params *telego.DeclineChatJoinRequestParams) error
	BanChatMember(ctx context.Context, params *telego.BanChatMemberParams) error
	DeleteMessage(ctx context.Context, params *telego.DeleteMessageParams) error
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
}

// modBot adds membership lookups and callback acknowledgements to verification.
type modBot interface {
	verifyBot
	GetChatMember(ctx context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error)
	AnswerCallbackQuery(ctx context.Context, params *telego.AnswerCallbackQueryParams) error
}

// verifyTransport is the caller-owned Telegram mechanics used by verification.
type verifyTransport interface {
	SendHTMLFallback(ctx context.Context, chatID int64, rich, simpler string) bool
	Delete(ctx context.Context, chatID int64, messageID int)
	Alert(ctx context.Context, adminLogChatID int64, text string)
	FailAlert(ctx context.Context, adminLogChatID, groupID int64, text string)
	Ban(ctx context.Context, chatID, userID int64, seconds int, revokeMessages bool) error
}

// adminTransport is the caller-owned authorization and notice surface used outside moderation.
type adminTransport interface {
	CachedAdmin(ctx context.Context, chatID, userID int64) (bool, error)
	FreshAdmin(ctx context.Context, chatID, userID int64) (bool, error)
	Notify(ctx context.Context, chatID int64, text string, ttlSeconds int)
}

type pending struct {
	groupMsgID         int
	mode               string    // challenge type this applicant got: config.ModeKernel (typed answer) or config.ModeQuiz (buttons)
	lang               i18n.Lang // applicant locale from Telegram; every applicant message uses it
	storedLang         string    // original persisted tag retained for byte-stable legacy round trips
	preserveStoredLang bool
	qText              string
	qOpts              []string
	correctIdx         int
	tries              int      // kernel mode: replies used so far (kernelMaxTries before the decline)
	hinted             bool     // kernel mode: the "no Linux installed yet" fallback was already offered (deliberately not persisted — a restart may re-offer it, which costs nothing)
	prompted           bool     // kernel mode: the question has actually been DM'd, so a reply can be graded as an answer
	sampleBounced      bool     // kernel mode: the "you sent back our own example" nudge was already spent
	noLinuxReminded    bool     // kernel mode: the "no-Linux replies need the current minute" reminder was already spent
	osClarified        bool     // kernel mode: the "you named another OS but sent a real kernel version" clarification was already spent
	fbAnswers          []string // kernel mode: once the short-answer fallback replaced the kernel question, the answers it is graded against
	nonce              string   // per-pending token; a quiz button only counts if its nonce matches
	name               string   // applicant display name, kept so a post-outage re-notify can address them
	deadline           time.Time
	timer              *time.Timer
	epoch              uint64    // bumped on every (re-)arm; a timer callback carries the epoch it was armed with and no-ops if it no longer matches, so a re-arm (defer / recovery) can't be acted on by the timer it replaced
	lastRenotify       time.Time // last post-outage re-notify, so repeated recoveries don't re-message the same applicant every cycle
	done               bool
}

func (p *pending) persistedLang() string {
	if p.preserveStoredLang {
		return p.storedLang
	}
	return p.lang.String()
}

// renotifyItem is one applicant to re-notify after an outage — snapshotted under the lock, then
// messaged outside it. Shared by the runtime-recovery (onRecovery) and restart-recovery (load) paths.
type renotifyItem struct {
	gid, uid int64
	name     string
	oldMsg   int
	p        *pending
}

type pendingRec struct {
	UserID     int64    `json:"user_id"`
	GroupID    int64    `json:"group_id"`
	GroupMsgID int      `json:"group_msg_id"`
	Mode       string   `json:"mode,omitempty"`       // empty in a pre-kernel-mode record => quiz
	Lang       string   `json:"lang,omitempty"`       // applicant locale; empty => Simplified Chinese
	FbAnswers  []string `json:"fb_answers,omitempty"` // set once the applicant moved to the short-answer fallback
	Prompted   bool     `json:"prompted,omitempty"`   // the question was DM'd, so a reply counts as an answer
	// Persist one-shot guards so process restarts cannot replenish free replies.
	Hinted          bool     `json:"hinted,omitempty"`
	SampleBounced   bool     `json:"sample_bounced,omitempty"`
	NoLinuxReminded bool     `json:"no_linux_reminded,omitempty"`
	OSClarified     bool     `json:"os_clarified,omitempty"`
	Tries           int      `json:"tries,omitempty"`
	QText           string   `json:"q_text"`
	QOpts           []string `json:"q_opts"`
	CorrectIdx      int      `json:"correct_idx"`
	Nonce           string   `json:"nonce"`
	Name            string   `json:"name,omitempty"`
	Deadline        int64    `json:"deadline"`
}

// Per-pending randomness makes stale quiz buttons unable to answer replacements.
func newNonce() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36) // fallback; uniqueness is what matters
	}
	return hex.EncodeToString(b[:])
}

// Verifier owns mutable runtime state; fields document their guarding mutex.
type Verifier struct {
	cfg               *config.Config
	botUsername       string
	botID             int64
	statePath         string
	loc               *time.Location
	startTime         time.Time
	mu                sync.Mutex
	pend              map[pkey]*pending
	terminal          map[pkey]*pending // claimed terminal actions remain here until their Telegram call returns
	shuttingDown      bool              // set at graceful shutdown; consumeNonce refuses so a firing timeout timer can't decline/strike/ban a mid-verification user (guarded by mu)
	statDate          string
	approved          int
	declined          int
	chanAlert         map[int64]time.Time   // required-channel -> last "bot can't access" alert (throttle), guarded by mu
	pendingCapAlertAt time.Time             // last queue-cap alert; one global throttle prevents a join flood from flooding the admin log
	dmLast            map[int64]time.Time   // user -> last DM auto-reply time (throttle), guarded by mu
	challengeAt       map[int64]time.Time   // user -> last verification prompt sent (resend throttle), guarded by mu
	queryHits         map[int64][]time.Time // user -> recent private-query times (rate limit), guarded by mu
	vfail             map[pkey]*vfailRec    // group+user -> failed-verification strikes + last-fail time (anti-spam), guarded by mu
	vfailPath         string                // persistence path for vfail
	agentMu           sync.Mutex            // guards agents; separate from mu so the tally's file write never blocks the verification hot paths
	agents            agentTally            // tripped automated agents, counted per claimed model
	agentPath         string                // persistence path for the automated-agent tally
	settings          *store.Settings       // authoritative runtime-settings transaction
	tgMu              sync.Mutex            // guards telegramBot and telegramClient
	telegramBot       *telego.Bot           // concrete handler bot wrapped by telegramClient
	telegramClient    *tg.Client            // shared transport client; owns admin cache and cleanup timer counts
	lastOnline        time.Time             // last time a heartbeat confirmed the bot can reach Telegram (guarded by mu); seeded to start time so we begin "online"
	hbPath            string                // persistence path for the online heartbeat, so a restart can estimate how long the bot was down
	probe             liveProbe             // liveness prober (the bot) for reachable(); nil in tests => assume reachable
}

func loadStatsLoc(name string) *time.Location {
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.FixedZone("UTC+8", 8*3600)
}

// Standard outbound HTML disables link previews.
func htmlMessage(chatID int64, text string) *telego.SendMessageParams {
	return tg.HTMLMessage(chatID, text)
}

// Reply binding disambiguates concurrent slow lookups; zero means no binding.
func replyParams(msgID int) *telego.ReplyParameters {
	return tg.ReplyParameters(msgID)
}

// NewVerifier seeds runtime defaults and bounds mutable maps.
func NewVerifier(cfg *config.Config) *Verifier {
	return newVerifier(cfg, nil)
}

func newVerifier(cfg *config.Config, settings *store.Settings) *Verifier {
	v := &Verifier{
		cfg:         cfg,
		startTime:   time.Now(),
		loc:         loadStatsLoc(cfg.StatsTimezone),
		pend:        make(map[pkey]*pending),
		terminal:    make(map[pkey]*pending),
		chanAlert:   map[int64]time.Time{},
		dmLast:      map[int64]time.Time{},
		challengeAt: map[int64]time.Time{},
		queryHits:   map[int64][]time.Time{},
		vfail:       map[pkey]*vfailRec{},
		lastOnline:  time.Now(), // begin online; the heartbeat only flips us offline after missed contact
	}
	if settings == nil {
		installBaselineRuntimeSettings(v)
	} else {
		installRuntimeSettings(v, settings)
	}
	return v
}

func (v *Verifier) telegram(bot *telego.Bot) *tg.Client {
	v.tgMu.Lock()
	defer v.tgMu.Unlock()
	if v.telegramClient == nil || v.telegramBot != bot {
		v.telegramBot = bot
		v.telegramClient = tg.New(bot)
	}
	return v.telegramClient
}

func (v *Verifier) verificationTransport(bot verifyBot) verifyTransport {
	if transport, ok := bot.(verifyTransport); ok {
		return transport
	}
	return v.telegram(bot.(*telego.Bot))
}

func (v *Verifier) adminTransport(bot modBot) adminTransport {
	if transport, ok := bot.(adminTransport); ok {
		return transport
	}
	return v.telegram(bot.(*telego.Bot))
}

func (v *Verifier) lookupAutoDelete(groupID int64) (time.Duration, bool) {
	if group, ok := v.groupSettings(groupID); ok {
		return time.Duration(group.LookupTTLSeconds().Value) * time.Second, group.LookupAutoDeleteEnabled().Value
	}
	fallback := v.fallbackGroupSettings(groupID)
	return time.Duration(fallback.LookupTTLSeconds.Value) * time.Second, fallback.LookupAutoDeleteEnabled.Value
}

func (v *Verifier) setLookupAutoDelete(groupID int64, ttl time.Duration, on bool) error {
	return v.updateGroupSettings(groupID, func(group store.GroupView, overrides *store.GroupOverrides) {
		if ttl <= 0 && on && group.LookupTTLSeconds().Value <= 0 {
			ttl = 3 * time.Minute
		}
		if ttl > 0 {
			seconds := int(ttl / time.Second)
			overrides.LookupTTLSeconds = &seconds
		}
		overrides.LookupAutoDeleteEnabled = &on
	})
}

func (v *Verifier) lookupSettingsGroupID(chatID int64) int64 {
	if v.settings != nil && v.settings.IsGroup(chatID) {
		return chatID
	}
	return v.controlGroupID()
}

// Delete group lookup commands and answers together using a fresh timer context.
func (v *Verifier) scheduleLookupCleanup(bot *telego.Bot, chatID int64, cmdMsgID, respMsgID int) {
	ttl, on := v.lookupAutoDelete(v.lookupSettingsGroupID(chatID))
	if !on {
		ttl = 0
	}
	v.telegram(bot).ScheduleCleanup(chatID, cmdMsgID, respMsgID, ttl)
}

func msgID(m *telego.Message) int {
	return tg.MessageID(m)
}

// Plain text preserves angle-bracket placeholders and still follows reply/cleanup semantics.
func (v *Verifier) replyLookupPlain(c context.Context, bot *telego.Bot, chatID int64, replyTo int, text string) {
	ttl, on := v.lookupAutoDelete(v.lookupSettingsGroupID(chatID))
	if !on {
		ttl = 0
	}
	v.telegram(bot).ReplyPlain(c, chatID, replyTo, text, ttl)
}

// HTML lookup replies require callers to escape dynamic content.
func (v *Verifier) replyLookupHTML(c context.Context, bot *telego.Bot, chatID int64, replyTo int, htmlText string) *telego.Message {
	ttl, on := v.lookupAutoDelete(v.lookupSettingsGroupID(chatID))
	if !on {
		ttl = 0
	}
	return v.telegram(bot).ReplyHTML(c, chatID, replyTo, htmlText, ttl)
}

const privateQueryWindow = time.Minute

// Sliding-window limits apply only to private-chat lookups.
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
	if len(kept) >= v.privateQueryPerMin() {
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

// Cheap informational commands are unrestricted in guarded groups and DMs.
func (v *Verifier) dmOrGroup(msg *telego.Message) bool {
	return v.cfg.IsGroup(msg.Chat.ID) || msg.Chat.Type == "private"
}

// External lookups are unlimited in guarded groups and rate-limited in DMs.
func (v *Verifier) queryAllowed(ctx *th.Context, msg *telego.Message) bool {
	if v.cfg.IsGroup(msg.Chat.ID) {
		return true
	}
	if msg.Chat.Type == "private" && msg.From != nil {
		if v.queryRateOK(msg.From.ID) {
			return true
		}
		_, _ = ctx.Bot().SendMessage(ctx.Context(), tu.Message(tu.ID(msg.Chat.ID),
			fmt.Sprintf("⏳ 查询太频繁:私聊每分钟最多 %d 次,请稍后再试(在群里不限次)。", v.privateQueryPerMin())))
		return false
	}
	return false
}

func (v *Verifier) isEnabled(groupID int64) bool {
	if group, ok := v.groupSettings(groupID); ok {
		return group.Enabled().Value
	}
	return v.fallbackGroupSettings(groupID).Enabled.Value
}

func (v *Verifier) setEnabled(groupID int64, enabled bool) error {
	return v.updateGroupSettings(groupID, func(_ store.GroupView, overrides *store.GroupOverrides) {
		overrides.Enabled = &enabled
	})
}

func (v *Verifier) isRichEnabled() bool {
	if v.settings != nil {
		return v.settings.Global().RichMessages().Value
	}
	return settingsBaselineFromConfig(v.cfg, configPresence{}).Global.RichMessages.Value
}

func (v *Verifier) toggleRich() (bool, error) {
	var enabled bool
	err := v.updateGlobalSettings(func(global store.GlobalView, overrides *store.GlobalOverrides) {
		enabled = !global.RichMessages().Value
		overrides.RichMessages = &enabled
	})
	return enabled, err
}

func (v *Verifier) privateQueryPerMin() int {
	if v.settings != nil {
		return v.settings.Global().PrivateQueryPerMin().Value
	}
	return settingsBaselineFromConfig(v.cfg, configPresence{}).Global.PrivateQueryPerMin.Value
}

func (v *Verifier) nameSpoilerOn(groupID int64) bool {
	if group, ok := v.groupSettings(groupID); ok {
		return group.NameSpoiler().Value
	}
	return v.fallbackGroupSettings(groupID).NameSpoiler.Value
}

func (v *Verifier) toggleNameSpoiler(groupID int64) (bool, error) {
	var enabled bool
	err := v.updateGroupSettings(groupID, func(group store.GroupView, overrides *store.GroupOverrides) {
		enabled = !group.NameSpoiler().Value
		overrides.NameSpoiler = &enabled
	})
	return enabled, err
}
func (v *Verifier) timeout(groupID int64) time.Duration {
	if group, ok := v.groupSettings(groupID); ok {
		return time.Duration(group.TimeoutSeconds().Value) * time.Second
	}
	return time.Duration(v.fallbackGroupSettings(groupID).TimeoutSeconds.Value) * time.Second
}

func (v *Verifier) verificationBanDuration(groupID int64) int {
	if group, ok := v.groupSettings(groupID); ok {
		return group.BanSeconds().Value
	}
	return v.fallbackGroupSettings(groupID).BanSeconds.Value
}

func verificationBanDurationText(seconds int) string {
	if seconds <= 0 {
		return "永久"
	}
	switch {
	case seconds%86400 == 0:
		return fmt.Sprintf("%d 天", seconds/86400)
	case seconds%3600 == 0:
		return fmt.Sprintf("%d 小时", seconds/3600)
	case seconds%60 == 0:
		return fmt.Sprintf("%d 分钟", seconds/60)
	default:
		return fmt.Sprintf("%d 秒", seconds)
	}
}

func (v *Verifier) applyVerificationBan(ctx context.Context, bot verifyBot, groupID, userID int64, seconds int, revoke bool) error {
	return v.verificationTransport(bot).Ban(ctx, groupID, userID, seconds, revoke)
}

func (v *Verifier) requiredChannelID(groupID int64) int64 {
	if group, ok := v.groupSettings(groupID); ok {
		overrides := group.Overrides()
		if overrides.RequiredChannelID != nil {
			return *overrides.RequiredChannelID
		}
		return group.Baseline().RequiredChannelID.Value
	}
	return v.fallbackGroupSettings(groupID).RequiredChannelID.Value
}

func (v *Verifier) channelDisplay(groupID int64) string {
	if group, ok := v.groupSettings(groupID); ok {
		return group.ChannelDisplay().Value
	}
	return v.fallbackGroupSettings(groupID).ChannelDisplay.Value
}

func (v *Verifier) channelInviteURL(groupID int64) string {
	if group, ok := v.groupSettings(groupID); ok {
		return group.ChannelInviteURL().Value
	}
	return v.fallbackGroupSettings(groupID).ChannelInviteURL.Value
}

func (v *Verifier) trustedGroups(groupID int64) []int64 {
	if group, ok := v.groupSettings(groupID); ok {
		return group.TrustedMemberGroupIDs().Value
	}
	return v.fallbackGroupSettings(groupID).TrustedMemberGroupIDs.Value
}

func (v *Verifier) groupLanguage(groupID int64, telegramCode string) i18n.Lang {
	if group, ok := v.groupSettings(groupID); ok {
		if language := group.Lang().Value; language != "" {
			return i18n.FromStored(language)
		}
	} else if language := v.fallbackGroupSettings(groupID).Lang.Value; language != "" {
		return i18n.FromStored(language)
	}
	return i18n.FromTelegram(telegramCode)
}

func (v *Verifier) isKnownChat(chatID int64) bool {
	if v.settings == nil {
		return v.cfg.IsKnownChat(chatID)
	}
	if v.settings.IsGroup(chatID) || v.cfg.AdminLogChatID == chatID {
		return true
	}
	for _, feed := range v.cfg.Feeds {
		if feed.ChatID == chatID {
			return true
		}
	}
	for _, groupID := range v.settings.GroupIDs() {
		if v.requiredChannelID(groupID) == chatID {
			return true
		}
		group, _ := v.settings.Group(groupID)
		for _, knownID := range group.KnownChatIDs().Value {
			if knownID == chatID {
				return true
			}
		}
		for _, trustedID := range group.TrustedMemberGroupIDs().Value {
			if trustedID == chatID {
				return true
			}
		}
	}
	return false
}

// logVerificationAccess reports required-channel and trusted-group membership visibility.
func (v *Verifier) logVerificationAccess(ctx context.Context, bot modBot, selfID int64) {
	groupIDs := v.cfg.GroupIDs
	if v.settings != nil {
		groupIDs = v.settings.GroupIDs()
	}
	seen := make(map[int64]bool)
	for _, groupID := range groupIDs {
		requiredChannelID := v.requiredChannelID(groupID)
		if requiredChannelID == 0 || seen[requiredChannelID] {
			continue
		}
		seen[requiredChannelID] = true
		if _, err := bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(requiredChannelID), UserID: selfID}); err != nil {
			log.Printf("required channel %d: bot CANNOT read membership (%v) — the follow-gate can't be enforced; make the bot an admin of this channel", requiredChannelID, err)
		} else {
			log.Printf("required channel %d: bot can read membership ✓", requiredChannelID)
		}
	}
	var trusted []int64
	for _, groupID := range groupIDs {
		trusted = append(trusted, v.trustedGroups(groupID)...)
	}
	for _, sourceID := range trusted {
		if sourceID == 0 || seen[sourceID] {
			continue
		}
		seen[sourceID] = true
		if _, err := bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(sourceID), UserID: selfID}); err != nil {
			log.Printf("trusted group %d: bot CANNOT read membership (%v) — its members can't be auto-approved; add the bot there (member/admin)", sourceID, err)
		} else {
			log.Printf("trusted group %d: bot can read membership ✓ — its members skip verification", sourceID)
		}
	}
}

// Spoilered names use one non-nested entity so hostile names cannot break challenge HTML.
// Admin buttons act by ID, so losing the clickable mention does not affect moderation.
func joinerLabel(uid int64, name string, spoiler bool) string {
	esc := html.EscapeString(name)
	if spoiler {
		return "<tg-spoiler>" + esc + "</tg-spoiler>"
	}
	return fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a>", uid, esc)
}

func applicantDisplayName(user *telego.User) string {
	if user.Username != "" {
		return "@" + user.Username
	}
	return user.FirstName
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

func (v *Verifier) save() {
	if v.statePath == "" {
		return
	}
	_ = store.Save(v.statePath, func() any {
		v.mu.Lock()
		defer v.mu.Unlock()
		recs := make([]pendingRec, 0, len(v.pend))
		for k, p := range v.pend {
			if p.done {
				continue
			}
			recs = append(recs, pendingRec{UserID: k.uid, GroupID: k.gid, GroupMsgID: p.groupMsgID,
				Mode: p.mode, Lang: p.persistedLang(), FbAnswers: p.fbAnswers, Prompted: p.prompted, Tries: p.tries,
				Hinted: p.hinted, SampleBounced: p.sampleBounced, NoLinuxReminded: p.noLinuxReminded, OSClarified: p.osClarified, QText: p.qText, QOpts: p.qOpts, CorrectIdx: p.correctIdx,
				Nonce: p.nonce, Name: p.name, Deadline: p.deadline.Unix()})
		}
		return recs
	})
}

func (v *Verifier) load(bot verifyBot) {
	if v.statePath == "" {
		return
	}
	var recs []pendingRec
	if err := store.Load(v.statePath, &recs); err != nil {
		if store.ReadFailed(err) {
			v.statePath = ""
		}
		return // corrupt files were backed up; unreadable files remain untouched and write-disabled
	}
	// Long downtime gets fresh windows and re-notification; quick restarts stay quiet.
	var downtime time.Duration
	if last := v.loadHeartbeat(); !last.IsZero() {
		if d := time.Since(last); d > 0 {
			downtime = d
		}
	}
	longOutage := downtime > outageRecovery

	var refresh []renotifyItem
	for _, r := range recs {
		gid, uid := r.GroupID, r.UserID
		// Never restore pendings for unguarded chats or unwinnable quiz payloads.
		if !v.cfg.IsGroup(gid) {
			log.Printf("state load: skip pending for unconfigured group %d (user %d)", gid, uid)
			continue
		}
		mode := r.Mode
		if mode == "" {
			mode = (config.ModeQuiz) // a record written before kernel mode existed always held a quiz
		}
		// Kernel challenges have no options; quiz payloads must remain winnable.
		if mode == (config.ModeQuiz) && (len(r.QOpts) < 2 || r.CorrectIdx < 0 || r.CorrectIdx >= len(r.QOpts)) {
			log.Printf("state load: skip pending with invalid question payload (group %d user %d)", gid, uid)
			continue
		}
		p := &pending{groupMsgID: r.GroupMsgID, mode: mode, lang: i18n.FromStored(r.Lang), storedLang: r.Lang, preserveStoredLang: true,
			fbAnswers: r.FbAnswers, prompted: r.Prompted, tries: r.Tries, hinted: r.Hinted, sampleBounced: r.SampleBounced,
			noLinuxReminded: r.NoLinuxReminded, osClarified: r.OSClarified, qText: r.QText, qOpts: r.QOpts, correctIdx: r.CorrectIdx,
			nonce: r.Nonce, name: r.Name, deadline: time.Unix(r.Deadline, 0)}
		delay := time.Until(p.deadline)
		reason := challengeExpiryReason(r.GroupMsgID)
		switch {
		case longOutage:
			// The outage consumed the window, so refresh and do not strike on this lapse.
			delay = v.timeout(gid)
			p.deadline = time.Now().Add(delay)
			p.lastRenotify = time.Now() // mark re-notified so a runtime recovery right after doesn't re-message
			reason = "recovered"
			refresh = append(refresh, renotifyItem{gid, uid, r.Name, r.GroupMsgID, p})
		case delay <= 0:
			// Short-restart lapses receive a strike-free grace window.
			delay = noFaultGrace
			p.deadline = time.Now().Add(delay)
			reason = "restart-lapsed"
		case delay < time.Second:
			delay = time.Second
		}
		v.mu.Lock()
		key := pkey{gid, uid}
		if _, replacing := v.pend[key]; !replacing && !v.pendingCapacityOKLocked(gid) {
			v.mu.Unlock()
			log.Printf("state load: pending cap reached; leaving user %d in group %d for manual review", uid, gid)
			v.alertPendingCap(context.Background(), bot, gid)
			continue
		}
		v.pend[key] = p
		// Publish the entry before arming even a near-zero timer.
		v.armExpiry(bot, p, gid, uid, delay, reason)
		v.mu.Unlock()
	}
	if len(recs) > 0 {
		log.Printf("restored %d pending verification(s)", len(recs))
	}
	// A real outage replaces stale restored challenges, bounded by renotifyCap.
	if longOutage && len(refresh) > 0 {
		capped := 0
		if len(refresh) > renotifyCap {
			capped = len(refresh) - renotifyCap
			refresh = refresh[:renotifyCap]
		}
		for _, it := range refresh {
			v.renotifyPending(context.Background(), bot, it.gid, it.uid, it.name, it.oldMsg, it.p, downtime)
		}
		v.save() // persist the fresh deadlines so a further crash doesn't reload the stale ones
		log.Printf("recovery: re-notified %d restored verification(s) after ~%s down%s", len(refresh), downtime.Round(time.Second), capNote(capped))
	}
}

func (v *Verifier) register(bh *th.BotHandler, moderation *moderate.Service) {
	// One malformed update must not terminate the bot.
	bh.Use(th.PanicRecoveryHandler(func(recovered any) error {
		log.Printf("recovered from handler panic: %v", recovered)
		return nil
	}))
	// Channel-sender filtering runs before handlers.
	bh.Use(moderation.FilterChannelSenders)
	bh.Handle(v.onAnswer, th.CallbackDataPrefix(answerPrefix))
	bh.Handle(v.onAdminAction, th.CallbackDataPrefix(adminPrefix))
	bh.Handle(v.onChannelRecheck, th.CallbackDataPrefix(recheckPrefix))
	bh.Handle(v.onJoinRequest, th.AnyChatJoinRequest())
	bh.Handle(v.onMyChatMember, th.AnyMyChatMember())
	// First-match routing requires kernel answers before the generic DM reply.
	bh.Handle(v.onKernelAnswer, v.kernelAnswerDM)
	// The generic DM reply precedes command handlers except /start and allowed DM commands.
	bh.Handle(v.onPrivateDM, privateNonStart)
	bh.Handle(moderation.OnPurge, th.CommandEqual("sb"))
	bh.Handle(moderation.OnBan, th.CommandEqual("ban"))
	bh.Handle(moderation.OnWarn, th.CommandEqual("warn"))
	bh.Handle(moderation.OnClearWarn, th.CommandEqual("clearwarn"))
	bh.Handle(moderation.OnBC, th.CommandEqual("bc"))
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
	bh.Handle(v.onVMode, th.CommandEqual("vmode"))
	bh.Handle(v.onAutoDel, th.CommandEqual("autodel"))
	bh.Handle(moderation.OnBanTime, th.CommandEqual("bantime"))
	bh.Handle(moderation.OnMute, th.CommandEqual("mute"))
	bh.Handle(moderation.OnUnmute, th.CommandEqual("unmute"))
	bh.Handle(v.onHelp, th.CommandEqual("help"))
}

// Leave any group or channel outside config.Config.IsKnownChat.
// Configure a guarded group before adding the bot.
func (v *Verifier) onMyChatMember(ctx *th.Context, update telego.Update) error {
	cm := update.MyChatMember
	if cm == nil || cm.Chat.Type == "private" {
		return nil
	}
	switch cm.NewChatMember.MemberStatus() {
	case "left", "kicked": // the bot was removed — nothing to do
		return nil
	}
	if v.isKnownChat(cm.Chat.ID) {
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

// Membership lookup errors fail safe into normal verification, never bypass it.
func (v *Verifier) isChatMember(c context.Context, bot modBot, chatID, uid int64) bool {
	cm, err := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(chatID), UserID: uid})
	if err != nil {
		log.Printf("exempt: getChatMember(chat=%d user=%d): %v", chatID, uid, err)
		return false
	}
	switch cm.MemberStatus() {
	case "creator", "administrator", "member":
		return true
	default:
		return cm.MemberIsMember()
	}
}

// Confirmed members of trusted chats bypass verification and cooldowns.
// Lookup failure is untrusted and follows normal verification.
// Approval failure returns trusted=true so normal verification runs without cooldown rejection.
func (v *Verifier) tryTrustedBypass(c context.Context, bot modBot, gid, uid int64) (handled, trusted bool) {
	for _, src := range v.trustedGroups(gid) {
		if src == 0 || src == gid {
			continue // ignore a blank or self-referential entry
		}
		if !v.isChatMember(c, bot, src, uid) { // fail-closed: error / non-member / unreadable => not trusted
			continue
		}
		// Trusted membership takes priority over failure cooldown.
		if err := bot.ApproveChatJoinRequest(c, &telego.ApproveChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
			log.Printf("trusted-bypass: approve %d in %d failed (%v) — falling back to normal verification", uid, gid, err)
			v.adminAlert(c, bot, fmt.Sprintf("⚠️ 用户 %d 是可信群 %d 的成员,但在群 %d 免验证批准失败(%v);将改用常规验证流程", uid, src, gid, err))
			return false, true
		}
		v.clearVerifyFails(gid, uid) // a now-trusted member starts with a clean slate
		v.recordDecision(true)
		log.Printf("verify: trusted-bypass auto-approved %d in %d (already a member of trusted group %d)", uid, gid, src)
		return true, true
	}
	return false, false
}

// Trusted membership is evaluated before the failed-applicant cooldown.
func (v *Verifier) joinGate(c context.Context, bot modBot, gid, uid int64) (done bool) {
	handled, trusted := v.tryTrustedBypass(c, bot, gid, uid)
	if handled {
		return true
	}
	if trusted {
		return false // confirmed trusted member, approve failed -> normal verification, skip the cooldown
	}
	// Early retries are declined without posting another challenge.
	if wait := v.verifyCooldownRemaining(gid, uid); wait > 0 {
		if err := bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
			log.Printf("verify cooldown: decline %d in %d failed: %v", uid, gid, err)
		}
		log.Printf("verify cooldown: declined early re-apply from %d in %d (%ds left)", uid, gid, int(wait.Seconds())+1)
		return true
	}
	return false
}

// Caller holds v.mu; replacements do not grow either queue cap.
func (v *Verifier) pendingCapacityOKLocked(gid int64) bool {
	if len(v.pend) >= pendingGlobalCap {
		return false
	}
	groupN := 0
	for k := range v.pend {
		if k.gid == gid {
			groupN++
		}
	}
	return groupN < pendingPerGroupCap
}

// A zero groupMsgID means Telegram never confirmed challenge delivery, so expiry is no-fault.
func challengeExpiryReason(groupMsgID int) string {
	if groupMsgID == 0 {
		return "challenge-post-failed"
	}
	return "timeout"
}

// Reserve capacity before delivery; only a confirmed challenge may install a striking timeout.
func (v *Verifier) startPending(bot verifyBot, gid, uid int64, p *pending) (oldMsgID int, status pendingStartStatus) {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := pkey{gid, uid}
	old, replacing := v.pend[key]
	if inFlight := v.terminal[key]; inFlight != nil || replacing && old.done {
		return 0, pendingBlockedTerminal
	}
	if !replacing && !v.pendingCapacityOKLocked(gid) {
		return 0, pendingBlockedCapacity
	}
	if replacing {
		old.done = true
		if old.timer != nil {
			old.timer.Stop()
		}
		oldMsgID = old.groupMsgID
		// Re-applying must not replenish attempts or one-shot guards.
		p.tries, p.hinted, p.sampleBounced = old.tries, old.hinted, old.sampleBounced
		p.noLinuxReminded, p.osClarified = old.noLinuxReminded, old.osClarified
	}
	delay := v.timeout(gid)
	p.deadline = time.Now().Add(delay)
	v.pend[key] = p
	v.armExpiry(bot, p, gid, uid, delay, challengeExpiryReason(0))
	return oldMsgID, pendingStarted
}

// Start a full window after delivery while preserving no-fault status on send failure.
func (v *Verifier) finishPendingChallenge(bot verifyBot, gid, uid int64, p *pending, groupMsgID int) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if cur, ok := v.pend[pkey{gid, uid}]; !ok || cur != p || p.done {
		return false
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	p.groupMsgID = groupMsgID
	delay := v.timeout(gid)
	p.deadline = time.Now().Add(delay)
	v.armExpiry(bot, p, gid, uid, delay, challengeExpiryReason(groupMsgID))
	return true
}

// One process-wide throttle prevents a multi-group flood from spamming operator alerts.
func (v *Verifier) alertPendingCap(c context.Context, bot verifyBot, gid int64) {
	now := time.Now()
	v.mu.Lock()
	if !v.pendingCapAlertAt.IsZero() && now.Sub(v.pendingCapAlertAt) < pendingCapAlertCooldown {
		v.mu.Unlock()
		return
	}
	v.pendingCapAlertAt = now
	v.mu.Unlock()
	v.failAlert(c, bot, gid, fmt.Sprintf("⚠️ 待验证队列已达上限(全局 %d,单群 %d);群 %d 的新申请将保留给管理员手动审核。", pendingGlobalCap, pendingPerGroupCap, gid))
}

func (v *Verifier) onJoinRequest(ctx *th.Context, update telego.Update) error {
	jr := update.ChatJoinRequest
	if jr == nil || !v.cfg.IsGroup(jr.Chat.ID) {
		return nil
	}
	if !v.isEnabled(jr.Chat.ID) {
		log.Printf("verification disabled — leaving join request from %d for manual review", jr.From.ID)
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	gid := jr.Chat.ID
	uid := jr.From.ID
	// Trusted bypass precedes cooldown enforcement.
	// Untrusted applicants then face the retry cooldown.
	if v.joinGate(c, bot, gid, uid) {
		return nil
	}
	// Applicant messages follow Telegram's interface language.
	ul := v.groupLanguage(gid, jr.From.LanguageCode)
	mode, text, opts, correctIdx := v.newChallenge(gid, ul)
	name := applicantDisplayName(&jr.From)
	p := &pending{mode: mode, lang: ul, qText: text, qOpts: opts, correctIdx: correctIdx,
		nonce: newNonce(), name: name}
	oldMsgID, status := v.startPending(bot, gid, uid, p)
	switch status {
	case pendingBlockedCapacity:
		log.Printf("join %d in group %d: pending cap reached; left for manual review", uid, gid)
		v.alertPendingCap(c, bot, gid)
		return nil
	case pendingBlockedTerminal:
		log.Printf("join %d in group %d: terminal action still in flight; deferred re-application", uid, gid)
		return nil
	}
	if oldMsgID != 0 {
		v.deleteChallenge(c, bot, gid, oldMsgID)
	}
	groupMsgID := v.postGroupChallenge(c, bot, gid, uid, name, ul)
	if !v.finishPendingChallenge(bot, gid, uid, p, groupMsgID) {
		v.deleteChallenge(c, bot, gid, groupMsgID)
		return nil // another action handled or replaced this request while the post was in flight
	}
	v.save()
	log.Printf("join %d (@%s) in group %d: pending (%s challenge), group message=%d", uid, jr.From.Username, gid, mode, groupMsgID)
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

// Use one pending for the DM channel prompt; each answer still enforces its own group's channel.
func (v *Verifier) firstPending(uid int64) (gid int64, ul i18n.Lang, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, p := range v.pend {
		if k.uid == uid && !p.done {
			return k.gid, p.lang, true
		}
	}
	return 0, i18n.LangZH, false
}

// Throttle /start fan-out per user without delaying normal channel-follow completion.
func (v *Verifier) challengeResendOK(uid int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if last, ok := v.challengeAt[uid]; ok && time.Since(last) < challengeResendCooldown {
		return false
	}
	if len(v.challengeAt) >= dmMapMax {
		v.challengeAt = map[int64]time.Time{} // bounded like dmLast
	}
	v.challengeAt[uid] = time.Now()
	return true
}

// Fifteen seconds limits prompt floods without materially delaying a user.
const challengeResendCooldown = 15 * time.Second

func (v *Verifier) sendDMChallenge(c context.Context, bot *telego.Bot, uid int64) {
	gid, ul, ok := v.firstPending(uid)
	if ok && !v.challengeResendOK(uid) {
		return // pressed again within the cooldown: the prompt they already have is still valid
	}
	channel := &i18n.Messages.Verification.Channel
	if !ok {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), channel.NoPending.For(ul)))
		return
	}
	// The DM prompt uses one channel; answer settlement enforces each group separately.
	if v.requiredChannelID(gid) != 0 && !v.isChannelMember(c, bot, gid, uid) {
		var rows [][]telego.InlineKeyboardButton
		if curl := v.channelURL(gid); curl != "" {
			rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{
				Text: channel.FollowButton.Render(ul, v.channelDisplay(gid)), URL: curl,
			}))
		}
		rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: channel.ContinueButton.For(ul),
			CallbackData: recheckPrefix + strconv.FormatInt(gid, 10) + ":" + strconv.FormatInt(uid, 10)}))
		_, _ = bot.SendMessage(c, htmlMessage(uid,
			channel.FollowPrompt.Render(ul, v.channelLinkHTML(gid, ul))).
			WithReplyMarkup(tu.InlineKeyboard(rows...)))
		return
	}
	v.sendQuizzes(c, bot, uid)
}

// DM every live challenge; kernel mode routes the next text DM as its answer.
func (v *Verifier) sendQuizzes(c context.Context, bot verifyBot, uid int64) {
	type dmq struct {
		gid      int64
		mode     string
		lang     i18n.Lang
		text     string
		opts     []string
		nonce    string
		tries    int
		fallback bool
	}
	var qs []dmq
	v.mu.Lock()
	for k, p := range v.pend {
		if k.uid == uid && !p.done {
			qs = append(qs, dmq{k.gid, p.mode, p.lang, p.qText, p.qOpts, p.nonce, p.tries, len(p.fbAnswers) > 0})
		}
	}
	v.mu.Unlock()
	for _, dq := range qs {
		if dq.mode == (config.ModeKernel) {
			left := kernelMaxTries - dq.tries
			render := kernelPromptHTML
			if dq.fallback { // already moved to the short-answer question — re-send THAT, not the kernel one
				render = fallbackPromptHTML
			}
			if v.sendVerifyDM(c, bot, uid,
				render(dq.lang, dq.text, left, dq.nonce, true),    // collapsed tripwire (Bot API 7.4)
				render(dq.lang, dq.text, left, dq.nonce, false)) { // without the blockquote, for an old API server
				v.markPrompted(dq.gid, uid) // only a delivered question makes the next DM gradeable
			}
			continue
		}
		gidStr, uidStr := strconv.FormatInt(dq.gid, 10), strconv.FormatInt(uid, 10)
		rows := make([][]telego.InlineKeyboardButton, 0, len(dq.opts))
		for i, opt := range dq.opts {
			rows = append(rows, tu.InlineKeyboardRow(
				telego.InlineKeyboardButton{Text: opt, CallbackData: fmt.Sprintf("%s%s:%s:%s:%d", answerPrefix, gidStr, uidStr, dq.nonce, i)}))
		}
		_, _ = bot.SendMessage(c, htmlMessage(uid,
			i18n.Messages.Verification.Challenge.QuizPrompt.Render(dq.lang, html.EscapeString(dq.text))).
			WithReplyMarkup(tu.InlineKeyboard(rows...)))
	}
}

// Grade DMs only after successful prompt delivery; stray pre-prompt messages cost nothing.
func (v *Verifier) markPrompted(gid, uid int64) {
	v.mu.Lock()
	if p, ok := v.pend[pkey{gid, uid}]; ok && !p.done {
		p.prompted = true
	}
	v.mu.Unlock()
}

// Return success only when some rendering was delivered.
func (v *Verifier) sendVerifyDM(c context.Context, bot verifyBot, uid int64, rich, simpler string) bool {
	return v.verificationTransport(bot).SendHTMLFallback(c, uid, rich, simpler)
}

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
	ul := v.groupLanguage(gid, cq.From.LanguageCode)
	result := &i18n.Messages.Verification.Result
	channel := &i18n.Messages.Verification.Channel
	if cq.From.ID != uid {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(result.NotYours.For(ul)).WithShowAlert())
		return nil
	}
	if !v.hasPending(uid) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(result.AlreadyHandled.For(ul)))
		return nil
	}
	if v.requiredChannelID(gid) != 0 && !v.isChannelMember(c, bot, gid, uid) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).
			WithText(channel.NotFollowedYet.Render(ul, v.channelDisplay(gid))).WithShowAlert())
		return nil
	}
	// Acknowledge before sends; membership toasts remain result-driven and happen first.
	_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(channel.ContinueOK.For(ul)))
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
	// Accept legacy nonce-less buttons only for restored nonce-less pendings.
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
	ul := v.groupLanguage(gid, cq.From.LanguageCode)
	result := &i18n.Messages.Verification.Result
	channel := &i18n.Messages.Verification.Channel
	if cq.From.ID != owner {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(result.NotYours.For(ul)).WithShowAlert())
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
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(result.AlreadyHandled.For(ul)))
		return nil
	}
	if nonce != curNonce {
		// A stale button from a previous (overwritten) request — don't let it answer this quiz.
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(result.StaleQuestion.For(ul)).WithShowAlert())
		return nil
	}

	if choice != correctIdx {
		_, banned := v.decline(c, bot, gid, owner, nonce, "wrong answer")
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(v.wrongAnswerText(gid, ul, banned)).WithShowAlert())
		return nil
	}
	if !v.isChannelMember(c, bot, gid, owner) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).
			WithText(channel.NotFollowedYet.Render(ul, v.channelDisplay(gid))).WithShowAlert())
		return nil
	}
	p, claimed := v.claimPendingNonce(gid, owner, nonce)
	if claimed && v.executeApprove(c, bot, gid, owner, p) {
		text := result.Approved.For(ul)
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(text))
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(owner), text))
	} else {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(result.AlreadyHandled.For(ul)).WithShowAlert())
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
		// Acknowledge before approval; failures reopen the pending and alert admins.
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("✅ 已直接通过"))
		v.executeApprove(c, bot, gid, target, p)
	case "ban":
		p, ok := v.consume(gid, target)
		if !ok {
			_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText("该申请已处理。"))
			return nil
		}
		// Acknowledge before ban; failures remain visible and the request stays declined.
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(fmt.Sprintf("🚫 已拒绝并封禁(%s)", verificationBanDurationText(v.verificationBanDuration(gid)))))
		v.executeBan(c, bot, gid, target, p)
	default:
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
	}
	return nil
}

// Required-channel lookup uses modBot so fail-open policy remains testable.
func (v *Verifier) isChannelMember(c context.Context, bot modBot, gid, userID int64) bool {
	rc := v.requiredChannelID(gid)
	if rc == 0 {
		return true
	}
	cm, err := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(rc), UserID: userID})
	if err != nil {
		// If the bot cannot read its own membership, the gate is unenforceable.
		// Apply configured fail-open policy and alert admins instead of silently blocking everyone.
		if v.botID != 0 {
			if _, e2 := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(rc), UserID: v.botID}); e2 != nil {
				open := v.cfg.FailOpenChannel()
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

// Prefer an explicit private-channel invite; otherwise derive a public t.me URL.
func (v *Verifier) channelURL(gid int64) string {
	if u := v.channelInviteURL(gid); u != "" {
		return u
	}
	if d := v.channelDisplay(gid); strings.HasPrefix(d, "@") {
		return "https://t.me/" + d[1:]
	}
	return ""
}

func (v *Verifier) channelLinkHTML(gid int64, ul i18n.Lang) string {
	d := v.channelDisplay(gid)
	if d == "" {
		d = i18n.Messages.Verification.Channel.FallbackName.For(ul) // unnamed channels still read naturally
	}
	if u := v.channelURL(gid); u != "" {
		return fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(u), html.EscapeString(d))
	}
	return html.EscapeString(d)
}

// Caller holds v.mu. A pointer match prevents an old action from releasing a newer claim.
func (v *Verifier) markTerminalLocked(key pkey, p *pending) {
	if v.terminal == nil {
		v.terminal = make(map[pkey]*pending)
	}
	v.terminal[key] = p
}

// Caller holds v.mu.
func (v *Verifier) releaseTerminalLocked(key pkey, p *pending) {
	if v.terminal[key] == p {
		delete(v.terminal, key)
	}
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
	v.markTerminalLocked(key, p)
	delete(v.pend, key)
	return p, true
}

// Nonce matching prevents stale answers from consuming replacement pendings.
func (v *Verifier) consumeNonce(gid, uid int64, nonce string) (*pending, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.shuttingDown {
		return nil, false // shutting down: leave the pending intact so it persists across the restart
	}
	key := pkey{gid, uid}
	p, ok := v.pend[key]
	if !ok || p.done || p.nonce != nonce {
		return nil, false // gone, already handled, or a different (newer) pending now holds the key
	}
	p.done = true
	if p.timer != nil {
		p.timer.Stop()
	}
	v.markTerminalLocked(key, p)
	delete(v.pend, key)
	return p, true
}

// Nonce and timer epoch must both match, so superseded timers cannot act after recovery.
func (v *Verifier) consumeExpiry(gid, uid int64, nonce string, epoch uint64) (*pending, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.shuttingDown {
		return nil, false
	}
	key := pkey{gid, uid}
	p, ok := v.pend[key]
	if !ok || p.done || p.nonce != nonce || p.epoch != epoch {
		return nil, false // gone, handled, replaced, or superseded by a newer timer
	}
	p.done = true
	if p.timer != nil {
		p.timer.Stop()
	}
	v.markTerminalLocked(key, p)
	delete(v.pend, key)
	return p, true
}

// Stop all timers before the final save; callbacks also refuse settlement during shutdown.
// Pendings therefore survive a graceful restart intact.
func (v *Verifier) stopForShutdown() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.shuttingDown = true
	for _, p := range v.pend {
		if p != nil && p.timer != nil {
			p.timer.Stop()
		}
	}
}

func (v *Verifier) deleteChallenge(c context.Context, bot verifyBot, gid int64, msgID int) {
	v.verificationTransport(bot).Delete(c, gid, msgID)
}

func (v *Verifier) adminAlert(c context.Context, bot verifyBot, text string) {
	v.verificationTransport(bot).Alert(c, v.cfg.AdminLogChatID, text)
}

// Failure notices fall back to the acting group when no admin-log chat is configured.
// This keeps optimistic callback acknowledgements from hiding rare network failures.
func (v *Verifier) failAlert(c context.Context, bot verifyBot, gid int64, text string) {
	v.verificationTransport(bot).FailAlert(c, v.cfg.AdminLogChatID, gid, text)
}

// Throttle unreadable-channel alerts per channel to avoid flooding operators.
func (v *Verifier) channelAccessAlert(c context.Context, bot verifyBot, channelID int64) {
	v.mu.Lock()
	if last, ok := v.chanAlert[channelID]; ok && time.Since(last) < 10*time.Minute {
		v.mu.Unlock()
		return
	}
	v.chanAlert[channelID] = time.Now()
	v.mu.Unlock()
	mode := "正在批准已通过答题的申请人(fail-open)" // matches the default
	if !v.cfg.FailOpenChannel() {
		mode = "正在拒绝这些申请,请申请人稍后重试(fail-closed)"
	}
	v.adminAlert(c, bot, fmt.Sprintf("⚠️ 机器人无法读取必需关注频道 %d 的成员状态(可能已不再是该频道管理员)——频道门槛暂时无法核验,%s。请重新把机器人设为该频道管理员。", channelID, mode))
}

// Keep claimed approvals in the map so network failure can reopen them; consume deletes final claims.
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
	v.markTerminalLocked(pkey{gid, uid}, p)
	return p, true
}

// Bind answer validation and claiming to the same nonce.
func (v *Verifier) claimPendingNonce(gid, uid int64, nonce string) (*pending, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{gid, uid}]
	if !ok || p.done || p.nonce != nonce {
		return nil, false
	}
	p.done = true
	if p.timer != nil {
		p.timer.Stop()
	}
	v.markTerminalLocked(pkey{gid, uid}, p)
	return p, true
}

// Claim before approval so its timeout cannot decline or strike concurrently.
// Callback handlers may acknowledge between claimPending and executeApprove.
func (v *Verifier) approve(c context.Context, bot verifyBot, gid, uid int64) bool {
	p, ok := v.claimPending(gid, uid)
	if !ok {
		return false
	}
	return v.executeApprove(c, bot, gid, uid, p)
}

// Failed approval reopens the claimed pending instead of stranding the applicant.
func (v *Verifier) executeApprove(c context.Context, bot verifyBot, gid, uid int64, p *pending) bool {
	if err := bot.ApproveChatJoinRequest(c, &telego.ApproveChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
		log.Printf("approve %d in %d: %v", uid, gid, err)
		v.failAlert(c, bot, gid, fmt.Sprintf("⚠️ 批准用户 %d 加入群 %d 失败(可能缺权限):%v;已保留申请,可重试或等待超时", uid, gid, err))
		v.reopenPending(bot, gid, uid, p) // restore as retryable (re-arm the timeout)
		return false
	}
	v.mu.Lock()
	key := pkey{gid, uid}
	if cur, ok := v.pend[key]; ok && cur == p {
		delete(v.pend, key)
	}
	v.releaseTerminalLocked(key, p)
	v.mu.Unlock()
	v.clearVerifyFails(gid, uid) // verified successfully — reset any failure strikes
	v.deleteChallenge(c, bot, gid, p.groupMsgID)
	v.recordDecision(true)
	v.save()
	log.Printf("approve user=%d group=%d", uid, gid)
	return true
}

// Reopen failed approvals with a retry timer unless the pending was replaced or consumed.
func (v *Verifier) reopenPending(bot verifyBot, gid, uid int64, p *pending) {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := pkey{gid, uid}
	defer v.releaseTerminalLocked(key, p)
	if cur, ok := v.pend[key]; !ok || cur != p || !p.done {
		return
	}
	p.done = false
	delay := time.Until(p.deadline)
	if delay < noFaultGrace {
		delay = noFaultGrace // OUR approve failed — give the user a real retry window, not ~1s
	}
	// "approve-retry": if this timer fires it must NOT strike the user — the original failure was ours.
	v.armExpiry(bot, p, gid, uid, delay, "approve-retry")
}

// Wrong-answer feedback distinguishes automatic ban from cooldown retry.
func (v *Verifier) wrongAnswerText(groupID int64, l i18n.Lang, banned bool) string {
	result := &i18n.Messages.Verification.Result
	if banned {
		return result.WrongBanned.For(l)
	}
	if seconds := v.verifyRetrySeconds(groupID); seconds > 0 {
		return result.WrongRetry.Render(l, seconds)
	}
	return result.WrongNoWait.For(l)
}

// Bot-caused failures receive a meaningful strike-free retry window.
const noFaultGrace = 60 * time.Second

// Timeouts and wrong answers strike; delivery, approval, restart, and recovery failures do not.
func strikesUser(reason string) bool {
	switch reason {
	case "approve-retry", "restart-lapsed", "recovered", "challenge-post-failed":
		return false
	default:
		return true
	}
}

// Live wrong answers use nonce claims; timeout settlement uses epoch claims so outages may defer it.
// handled=false means no matching pending; banned reports only a successful threshold ban.
func (v *Verifier) decline(c context.Context, bot verifyBot, gid, uid int64, nonce, reason string) (handled, banned bool) {
	p, ok := v.consumeNonce(gid, uid, nonce)
	if !ok {
		return false, false
	}
	return true, v.finishDecline(c, bot, gid, uid, p, reason)
}

// Settle an already-claimed decline, striking only user-caused failures and banning at threshold.
func (v *Verifier) finishDecline(c context.Context, bot verifyBot, gid, uid int64, p *pending, reason string) (banned bool) {
	defer v.finishTerminal(gid, uid, p)
	v.deleteChallenge(c, bot, gid, p.groupMsgID)
	var count int
	var doBan bool
	if strikesUser(reason) { // a decline from OUR OWN failed approve / a restart-lapsed deadline isn't the user's fault — don't strike them
		v.recordDecision(false)
		count, doBan = v.recordVerifyFail(gid, uid)
	}
	if err := bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
		log.Printf("decline %d in %d failed: %v", uid, gid, err)
		v.adminAlert(c, bot, fmt.Sprintf("⚠️ 拒绝用户 %d 加入群 %d 失败(可能缺权限):%v;该申请仍需管理员手动处理", uid, gid, err))
	}
	if doBan {
		secs := v.verificationBanDuration(gid)
		if err := v.applyVerificationBan(c, bot, gid, uid, secs, false); err != nil {
			log.Printf("verify auto-ban %d in %d: %v", uid, gid, err)
			v.adminAlert(c, bot, fmt.Sprintf("⚠️ 用户 %d 在群 %d 验证连续失败 %d 次,自动封禁失败(可能缺权限):%v", uid, gid, count, err))
			banned = false
		} else {
			v.adminAlert(c, bot, fmt.Sprintf("🚫 用户 %d 在群 %d 验证连续失败 %d 次,已自动封禁(%s)", uid, gid, count, verificationBanDurationText(secs)))
			banned = true
		}
		if banned {
			v.clearVerifyFails(gid, uid) // ONLY on a successful ban (so a later unban starts fresh).
			// Keep threshold strikes after ban failure so later failures retry and alert again.
		}
	}
	v.save()
	log.Printf("decline user=%d group=%d (%s) fails=%d banned=%v", uid, gid, reason, count, banned)
	return banned
}

func (v *Verifier) finishTerminal(gid, uid int64, p *pending) {
	v.mu.Lock()
	v.releaseTerminalLocked(pkey{gid, uid}, p)
	v.mu.Unlock()
}

// banApplicant reports ban failure honestly even though the join request is still declined.
func (v *Verifier) banApplicant(c context.Context, bot verifyBot, gid, uid int64) (handled, banned bool) {
	p, ok := v.consume(gid, uid)
	if !ok {
		return false, false
	}
	return true, v.executeBan(c, bot, gid, uid, p)
}

// executeBan settles an already-consumed pending; ban failure remains visible after callback ACK.
func (v *Verifier) executeBan(c context.Context, bot verifyBot, gid, uid int64, p *pending) (banned bool) {
	defer v.finishTerminal(gid, uid, p)
	_ = bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid})
	banned = true
	if err := v.applyVerificationBan(c, bot, gid, uid, v.verificationBanDuration(gid), true); err != nil { // Honor /bantime like the other ban paths.
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

const (
	heartbeatInterval     = 25 * time.Second // how often the bot pings Telegram to confirm it is reachable
	heartbeatProbeTimeout = 10 * time.Second // per-probe timeout for the GetMe liveness call
	offlineThreshold      = 70 * time.Second // no successful contact within this => treat as offline (defer expiries)
	outageRecovery        = 90 * time.Second // an outage longer than this triggers fresh windows + a re-notify on recovery
	renotifyCap           = 30               // most applicants to re-notify per recovery, so a big backlog can't become a message storm
)

// liveProbe is the cheap GetMe liveness surface.
type liveProbe interface {
	GetMe(ctx context.Context) (*telego.User, error)
}

// Seeded online; only stale successful contact marks the bot offline.
func (v *Verifier) offlineNow() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return !v.lastOnline.IsZero() && time.Since(v.lastOnline) > offlineThreshold
}

// Probe at expiry to cover heartbeat detection lag at outage onset.
// Tests without a probe remain reachable.
func (v *Verifier) reachable(c context.Context) bool {
	if v.probe == nil {
		return true
	}
	pc, cancel := context.WithTimeout(c, heartbeatProbeTimeout)
	defer cancel()
	_, err := v.probe.GetMe(pc)
	return err == nil
}

// Caller holds v.mu. Every re-arm bumps epoch so replaced timers become stale.
// Expiry settlement defers while Telegram is unreachable.
func (v *Verifier) armExpiry(bot verifyBot, p *pending, gid, uid int64, delay time.Duration, reason string) {
	p.epoch++
	epoch := p.epoch
	nonce := p.nonce
	p.timer = time.AfterFunc(delay, func() { v.onExpiry(context.Background(), bot, gid, uid, nonce, epoch, reason) })
}

// Unreachable expiries receive a fresh window without consume or strike.
// Online settlement still requires the captured nonce and epoch.
func (v *Verifier) onExpiry(c context.Context, bot verifyBot, gid, uid int64, nonce string, epoch uint64, reason string) {
	if v.offlineNow() || !v.reachable(c) {
		v.deferExpiry(bot, gid, uid, nonce, epoch, reason)
		return
	}
	p, ok := v.consumeExpiry(gid, uid, nonce, epoch)
	if !ok {
		return
	}
	v.finishDecline(c, bot, gid, uid, p, reason)
}

// Keep the original reason while re-arming offline expiries; nonce and epoch guard replacement.
func (v *Verifier) deferExpiry(bot verifyBot, gid, uid int64, nonce string, epoch uint64, reason string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{gid, uid}]
	if !ok || p.done || p.nonce != nonce || p.epoch != epoch {
		return
	}
	if p.timer != nil {
		p.timer.Stop() // stop the current timer before armExpiry installs the next (matches every other re-arm site)
	}
	delay := v.timeout(gid)
	p.deadline = time.Now().Add(delay)
	v.armExpiry(bot, p, gid, uid, delay, reason)
	log.Printf("verify: bot offline — deferred %s for %d in %d, re-armed a fresh window", reason, uid, gid)
}

// Shared challenge rendering returns zero and alerts admins on delivery failure.
func (v *Verifier) postGroupChallenge(c context.Context, bot verifyBot, gid, uid int64, name string, ul i18n.Lang) int {
	gidStr, uidStr := strconv.FormatInt(gid, 10), strconv.FormatInt(uid, 10)
	mention := joinerLabel(uid, name, v.nameSpoilerOn(gid))
	link := ""
	if v.botUsername != "" {
		link = "https://t.me/" + v.botUsername + "?start=verify"
	}
	// Keep channel navigation inside the DM verification flow.
	group := &i18n.Messages.Verification.Group
	channelHint := ""
	if v.requiredChannelID(gid) != 0 {
		channelHint = group.ChannelHint.Render(ul, html.EscapeString(v.channelDisplay(gid)))
	}
	linkText := ""
	if link != "" {
		linkText = group.LinkText.Render(ul, link)
	}
	body := group.Body.Render(ul, mention, linkText, int(v.timeout(gid)/time.Second), channelHint)

	var rows [][]telego.InlineKeyboardButton
	if link != "" {
		rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: group.VerifyButton.For(ul), URL: link}))
	}
	rows = append(rows, tu.InlineKeyboardRow(
		telego.InlineKeyboardButton{Text: "👮 管理员直接通过", CallbackData: adminPrefix + "pass:" + gidStr + ":" + uidStr},
		telego.InlineKeyboardButton{Text: "🚫 拒绝并封禁", CallbackData: adminPrefix + "ban:" + gidStr + ":" + uidStr},
	))
	sent, err := bot.SendMessage(c, htmlMessage(gid, body).WithReplyMarkup(tu.InlineKeyboard(rows...)))
	if err != nil {
		log.Printf("join %d in %d: post challenge failed: %v", uid, gid, err)
		v.adminAlert(c, bot, fmt.Sprintf("⚠️ 群 %d 未能发出用户 %d 的入群验证消息:%v;请手动处理该申请", gid, uid, err))
		return 0
	}
	return msgID(sent)
}

type heartbeatRec struct {
	LastOnline int64 `json:"last_online"`
}

// Persist reachability so restart recovery can estimate downtime.
func (v *Verifier) saveHeartbeat() {
	if v.hbPath == "" {
		return
	}
	v.mu.Lock()
	t := v.lastOnline
	v.mu.Unlock()
	if t.IsZero() {
		return
	}
	_ = store.Write(v.hbPath, heartbeatRec{LastOnline: t.Unix()})
}

// Missing or unreadable heartbeat state returns zero time.
func (v *Verifier) loadHeartbeat() time.Time {
	if v.hbPath == "" {
		return time.Time{}
	}
	var r heartbeatRec
	if err := store.Load(v.hbPath, &r); err != nil {
		if store.ReadFailed(err) {
			v.hbPath = ""
		}
		return time.Time{}
	}
	if r.LastOnline == 0 {
		return time.Time{}
	}
	return time.Unix(r.LastOnline, 0)
}

// heartbeatBot combines liveness with recovery notification operations.
type heartbeatBot interface {
	liveProbe
	verifyBot
}

func (v *Verifier) runHeartbeat(ctx context.Context, bot heartbeatBot) {
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !v.heartbeatTick(ctx, bot) && ctx.Err() != nil {
				return // shutting down
			}
		}
	}
}

// Successful probes advance reachability and refresh pendings after a real outage.
func (v *Verifier) heartbeatTick(ctx context.Context, bot heartbeatBot) bool {
	pc, cancel := context.WithTimeout(ctx, heartbeatProbeTimeout)
	_, err := bot.GetMe(pc)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("heartbeat: cannot reach Telegram (%v) — verification timeouts are paused until contact resumes", err)
		}
		return false
	}
	now := time.Now()
	v.mu.Lock()
	prev := v.lastOnline
	v.lastOnline = now
	v.mu.Unlock()
	v.saveHeartbeat()
	if !prev.IsZero() && now.Sub(prev) > outageRecovery {
		log.Printf("heartbeat: back online after ~%s offline — refreshing in-progress verifications", now.Sub(prev).Round(time.Second))
		v.onRecovery(ctx, bot, now.Sub(prev))
	}
	return true
}

// Recovery grants every pending a fresh strike-free window.
// Re-notification is capped and suppressed during flapping or shutdown.
func (v *Verifier) onRecovery(c context.Context, bot verifyBot, outage time.Duration) {
	now := time.Now()
	v.mu.Lock()
	if v.shuttingDown {
		v.mu.Unlock()
		return
	}
	var items []renotifyItem
	refreshed := 0
	for k, p := range v.pend {
		if p == nil || p.done {
			continue
		}
		if p.timer != nil {
			p.timer.Stop()
		}
		delay := v.timeout(k.gid)
		p.deadline = now.Add(delay)
		v.armExpiry(bot, p, k.gid, k.uid, delay, "recovered")
		refreshed++
		if !p.lastRenotify.IsZero() && now.Sub(p.lastRenotify) < delay {
			continue // re-notified recently (flapping) — refresh the window silently, don't re-message
		}
		p.lastRenotify = now
		items = append(items, renotifyItem{k.gid, k.uid, p.name, p.groupMsgID, p})
	}
	v.mu.Unlock()
	if refreshed == 0 {
		return
	}
	capped := 0
	if len(items) > renotifyCap {
		capped = len(items) - renotifyCap
		items = items[:renotifyCap]
	}
	for _, it := range items {
		v.renotifyPending(c, bot, it.gid, it.uid, it.name, it.oldMsg, it.p, outage)
	}
	v.save() // after renotifyPending so the persisted groupMsgIDs point at the re-posted challenges
	log.Printf("recovery: refreshed %d verification(s), re-notified %d after ~%s offline%s", refreshed, len(items), outage.Round(time.Second), capNote(capped))
}

// Re-notify without holding v.mu, then update only the still-current pending's message ID.
func (v *Verifier) renotifyPending(c context.Context, bot verifyBot, gid, uid int64, name string, oldMsg int, p *pending, outage time.Duration) {
	ul := p.lang
	notice := i18n.Messages.Verification.Recovery.Renotify.Render(ul, outageText(ul, outage))
	_, _ = bot.SendMessage(c, htmlMessage(uid, notice))
	newMsg := v.postGroupChallenge(c, bot, gid, uid, name, ul)
	if oldMsg != 0 && oldMsg != newMsg {
		v.deleteChallenge(c, bot, gid, oldMsg)
	}
	v.mu.Lock()
	if cur, ok := v.pend[pkey{gid, uid}]; ok && cur == p {
		cur.groupMsgID = newMsg
	}
	v.mu.Unlock()
}

// Render whole seconds, minutes, or hours in the applicant's locale.
func outageText(l i18n.Lang, d time.Duration) string {
	units := [3]string{" 秒", " 分钟", " 小时"}
	switch l {
	case i18n.LangZHHant:
		units = [3]string{" 秒", " 分鐘", " 小時"}
	case i18n.LangEN:
		units = [3]string{" seconds", " minutes", " hours"}
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d%s", int(d.Seconds()), units[0])
	case d < time.Hour:
		return fmt.Sprintf("%d%s", int(d.Minutes()), units[1])
	default:
		return fmt.Sprintf("%d%s", int(d.Hours()), units[2])
	}
}

func capNote(capped int) string {
	if capped <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d more refreshed silently, over the re-notify cap)", capped)
}
