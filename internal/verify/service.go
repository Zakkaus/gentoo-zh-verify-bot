package verify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"log"
	"math/big"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

const (
	// AnswerCallbackPrefix identifies applicant quiz-answer callbacks.
	AnswerCallbackPrefix = "v:" // v:<gid>:<uid>:<nonce>:<idx>
	// AdminCallbackPrefix identifies administrator verification callbacks.
	AdminCallbackPrefix = "adm:" // adm:<action>:<gid>:<uid>
	// ChannelRecheckCallbackPrefix identifies required-channel recheck callbacks.
	ChannelRecheckCallbackPrefix = "ch:" // ch:<gid>:<uid>
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

// Service owns verification challenges, pending settlement, recovery, and verification state.
type Service struct {
	cfg               *config.Config
	botUsername       string
	botID             int64
	statePath         string
	loc               *time.Location
	messages          *i18n.Catalog
	mu                sync.Mutex
	pend              map[pkey]*pending
	terminal          map[pkey]*pending // claimed terminal actions remain here until their Telegram call returns
	shuttingDown      bool              // set at graceful shutdown; consumeNonce refuses so a firing timeout timer can't decline/strike/ban a mid-verification user (guarded by mu)
	statDate          string
	approved          int
	declined          int
	chanAlert         map[int64]time.Time // required-channel -> last "bot can't access" alert (throttle), guarded by mu
	pendingCapAlertAt time.Time           // last queue-cap alert; one global throttle prevents a join flood from flooding the admin log
	challengeAt       map[int64]time.Time // user -> last verification prompt sent (resend throttle), guarded by mu
	vfail             map[pkey]*vfailRec  // group+user -> failed-verification strikes + last-fail time (anti-spam), guarded by mu
	vfailPath         string              // persistence path for vfail
	agentMu           sync.Mutex          // guards agents; separate from mu so the tally's file write never blocks the verification hot paths
	agents            agentTally          // tripped automated agents, counted per claimed model
	agentPath         string              // persistence path for the automated-agent tally
	settings          *store.Settings     // authoritative runtime-settings transaction
	tgMu              sync.Mutex          // guards telegramBot and telegramClient
	telegramBot       *telego.Bot         // concrete handler bot wrapped by telegramClient
	telegramClient    *tg.Client          // shared transport client; owns admin cache and cleanup timer counts
	lastOnline        time.Time           // last time a heartbeat confirmed the bot can reach Telegram (guarded by mu); seeded to start time so we begin "online"
	hbPath            string              // persistence path for the online heartbeat, so a restart can estimate how long the bot was down
	probe             liveProbe           // liveness prober (the bot) for reachable(); nil in tests => assume reachable
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

// Telegram is the Bot API surface required by verification and outage recovery.
type Telegram interface {
	modBot
	GetMe(ctx context.Context) (*telego.User, error)
}

// Identity is the verified Telegram identity used in challenge links and access checks.
type Identity struct {
	// ID is the bot's Telegram user ID.
	ID int64
	// Username is the bot username without a leading at sign.
	Username string
}

// New constructs verification with explicit state, transport, configuration, catalogue, and Bot API dependencies.
func New(settings *store.Settings, telegram *tg.Client, cfg *config.Config, messages *i18n.Catalog, bot Telegram, identity Identity, stateDir string) *Service {
	v := newService(settings, telegram, cfg, messages)
	v.botID = identity.ID
	v.botUsername = identity.Username
	v.probe = bot
	if raw, ok := bot.(*telego.Bot); ok {
		v.telegramBot = raw
	}
	if stateDir != "" {
		v.hbPath = filepath.Join(stateDir, "heartbeat.json")
		v.statePath = filepath.Join(stateDir, "pending.json")
		v.load(bot)
		v.vfailPath = filepath.Join(stateDir, "verifyfail.json")
		v.loadVerifyFails()
		v.agentPath = filepath.Join(stateDir, "agents.json")
		v.loadAgents()
	}
	return v
}

func newService(settings *store.Settings, telegram *tg.Client, cfg *config.Config, messages *i18n.Catalog) *Service {
	if settings == nil {
		panic("verify: settings must not be nil")
	}
	return &Service{
		cfg:            cfg,
		loc:            loadStatsLoc(cfg.StatsTimezone),
		messages:       messages,
		pend:           make(map[pkey]*pending),
		terminal:       make(map[pkey]*pending),
		chanAlert:      map[int64]time.Time{},
		challengeAt:    map[int64]time.Time{},
		vfail:          map[pkey]*vfailRec{},
		settings:       settings,
		telegramClient: telegram,
		lastOnline:     time.Now(), // begin online; the heartbeat only flips us offline after missed contact
	}
}

func (v *Service) telegram(bot *telego.Bot) *tg.Client {
	v.tgMu.Lock()
	defer v.tgMu.Unlock()
	if v.telegramClient == nil || (v.telegramBot != nil && v.telegramBot != bot) {
		v.telegramBot = bot
		v.telegramClient = tg.New(bot)
	}
	return v.telegramClient
}

func (v *Service) verificationTransport(bot verifyBot) verifyTransport {
	if transport, ok := bot.(verifyTransport); ok {
		return transport
	}
	return v.telegram(bot.(*telego.Bot))
}

func (v *Service) adminTransport(bot modBot) adminTransport {
	if transport, ok := bot.(adminTransport); ok {
		return transport
	}
	return v.telegram(bot.(*telego.Bot))
}
func (v *Service) groupSettings(groupID int64) (store.GroupView, bool) {
	return v.settings.Group(groupID)
}

// ControlGroupID returns the group used for bot-wide settings and DM status.
func (v *Service) ControlGroupID() int64 {
	return v.settings.ControlGroupID()
}

func (v *Service) updateGroupSettings(groupID int64, update func(store.GroupView, *store.GroupOverrides)) error {
	group, ok := v.settings.Group(groupID)
	if !ok {
		return fmt.Errorf("%w: %d", store.ErrUnknownGroup, groupID)
	}
	overrides := group.Overrides()
	update(group, &overrides)
	_, err := v.settings.CommitGroup(groupID, group.Revision(), overrides)
	return err
}

func (v *Service) updateGlobalSettings(update func(store.GlobalView, *store.GlobalOverrides)) error {
	global := v.settings.Global()
	overrides := global.Overrides()
	update(global, &overrides)
	_, err := v.settings.CommitGlobal(global.Revision(), overrides)
	return err
}

// SetAutoDelete updates one group's lookup cleanup policy.
func (v *Service) SetAutoDelete(groupID int64, ttl time.Duration, on bool) error {
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

func msgID(m *telego.Message) int {
	return tg.MessageID(m)
}

// DMOrGroup reports whether a message belongs to a guarded group or a private chat.
func (v *Service) DMOrGroup(msg *telego.Message) bool {
	return v.cfg.IsGroup(msg.Chat.ID) || msg.Chat.Type == "private"
}

// IsEnabled reports whether automated join verification is enabled for one group.
func (v *Service) IsEnabled(groupID int64) bool {
	group, ok := v.groupSettings(groupID)
	return ok && group.Enabled().Value
}

// SetEnabled updates automated join verification for one group.
func (v *Service) SetEnabled(groupID int64, enabled bool) error {
	return v.updateGroupSettings(groupID, func(_ store.GroupView, overrides *store.GroupOverrides) {
		overrides.Enabled = &enabled
	})
}

// ToggleRich flips the bot-wide rich-message setting.
func (v *Service) ToggleRich() (bool, error) {
	var enabled bool
	err := v.updateGlobalSettings(func(global store.GlobalView, overrides *store.GlobalOverrides) {
		enabled = !global.RichMessages().Value
		overrides.RichMessages = &enabled
	})
	return enabled, err
}

// NameSpoilerOn reports whether applicant names are hidden in one group.
func (v *Service) NameSpoilerOn(groupID int64) bool {
	group, ok := v.groupSettings(groupID)
	return ok && group.NameSpoiler().Value
}

// ToggleNameSpoiler flips applicant-name hiding for one group.
func (v *Service) ToggleNameSpoiler(groupID int64) (bool, error) {
	var enabled bool
	err := v.updateGroupSettings(groupID, func(group store.GroupView, overrides *store.GroupOverrides) {
		enabled = !group.NameSpoiler().Value
		overrides.NameSpoiler = &enabled
	})
	return enabled, err
}
func (v *Service) timeout(groupID int64) time.Duration {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return 0
	}
	return time.Duration(group.TimeoutSeconds().Value) * time.Second
}

func (v *Service) verificationBanDuration(groupID int64) int {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return 0
	}
	return group.BanSeconds().Value
}

func verificationBanDurationText(messages *i18n.Catalog, l i18n.Lang, seconds int) string {
	duration := &messages.Verification.Duration
	if seconds <= 0 {
		return duration.Permanent.For(l)
	}
	switch {
	case seconds%86400 == 0:
		return duration.Days.Render(l, seconds/86400)
	case seconds%3600 == 0:
		return duration.Hours.Render(l, seconds/3600)
	case seconds%60 == 0:
		return duration.Minutes.Render(l, seconds/60)
	default:
		return duration.Seconds.Render(l, seconds)
	}
}

func (v *Service) applyVerificationBan(ctx context.Context, bot verifyBot, groupID, userID int64, seconds int, revoke bool) error {
	return v.verificationTransport(bot).Ban(ctx, groupID, userID, seconds, revoke)
}

// RequiredChannelID returns the channel applicants must join for one group.
func (v *Service) RequiredChannelID(groupID int64) int64 {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return 0
	}
	overrides := group.Overrides()
	if overrides.RequiredChannelID != nil {
		return *overrides.RequiredChannelID
	}
	return group.Baseline().RequiredChannelID.Value
}

func (v *Service) channelDisplay(groupID int64) string {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return ""
	}
	return group.ChannelDisplay().Value
}

func (v *Service) channelInviteURL(groupID int64) string {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return ""
	}
	return group.ChannelInviteURL().Value
}

func (v *Service) trustedGroups(groupID int64) []int64 {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return nil
	}
	return group.TrustedMemberGroupIDs().Value
}

func (v *Service) groupLanguage(groupID int64) i18n.Lang {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return i18n.LangZH
	}
	return i18n.FromStored(group.Lang().Value)
}

func (v *Service) applicantLanguage(groupID, userID int64, telegramCode string) i18n.Lang {
	v.mu.Lock()
	defer v.mu.Unlock()
	if p := v.pend[pkey{groupID, userID}]; p != nil && !p.done {
		return p.lang
	}
	return i18n.FromTelegram(telegramCode)
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

// Membership lookup errors fail safe into normal verification, never bypass it.
func (v *Service) isChatMember(c context.Context, bot modBot, chatID, uid int64) bool {
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
func (v *Service) tryTrustedBypass(c context.Context, bot modBot, gid, uid int64) (handled, trusted bool) {
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
			alert := v.messages.Verification.Admin.TrustedBypassFailed.Render(v.groupLanguage(gid), uid, src, gid, err)
			v.adminAlert(c, bot, alert)
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
func (v *Service) joinGate(c context.Context, bot modBot, gid, uid int64) (done bool) {
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
func (v *Service) pendingCapacityOKLocked(gid int64) bool {
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
func (v *Service) startPending(bot verifyBot, gid, uid int64, p *pending) (oldMsgID int, status pendingStartStatus) {
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
func (v *Service) finishPendingChallenge(bot verifyBot, gid, uid int64, p *pending, groupMsgID int) bool {
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
func (v *Service) alertPendingCap(c context.Context, bot verifyBot, gid int64) {
	now := time.Now()
	v.mu.Lock()
	if !v.pendingCapAlertAt.IsZero() && now.Sub(v.pendingCapAlertAt) < pendingCapAlertCooldown {
		v.mu.Unlock()
		return
	}
	v.pendingCapAlertAt = now
	v.mu.Unlock()
	admin := &v.messages.Verification.Admin
	v.failAlert(c, bot, gid, admin.PendingCap.Render(v.groupLanguage(gid), pendingGlobalCap, pendingPerGroupCap, gid))
}

// OnJoinRequest starts verification for one eligible group join request.
func (v *Service) OnJoinRequest(ctx *th.Context, update telego.Update) error {
	jr := update.ChatJoinRequest
	if jr == nil || !v.cfg.IsGroup(jr.Chat.ID) {
		return nil
	}
	if !v.IsEnabled(jr.Chat.ID) {
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
	// Applicant and group surfaces resolve independently at the update boundary.
	applicantLang := i18n.FromTelegram(jr.From.LanguageCode)
	groupLang := v.groupLanguage(gid)
	mode, text, opts, correctIdx := v.newChallenge(gid, applicantLang)
	name := applicantDisplayName(&jr.From)
	p := &pending{mode: mode, lang: applicantLang, qText: text, qOpts: opts, correctIdx: correctIdx,
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
	groupMsgID := v.postGroupChallenge(c, bot, gid, uid, name, groupLang)
	if !v.finishPendingChallenge(bot, gid, uid, p, groupMsgID) {
		v.deleteChallenge(c, bot, gid, groupMsgID)
		return nil // another action handled or replaced this request while the post was in flight
	}
	v.save()
	log.Printf("join %d (@%s) in group %d: pending (%s challenge), group message=%d", uid, jr.From.Username, gid, mode, groupMsgID)
	return nil
}

func (v *Service) hasPending(uid int64) bool {
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
func (v *Service) firstPending(uid int64) (gid int64, ul i18n.Lang, ok bool) {
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
func (v *Service) challengeResendOK(uid int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if last, ok := v.challengeAt[uid]; ok && time.Since(last) < challengeResendCooldown {
		return false
	}
	if len(v.challengeAt) >= challengeResendMapMax {
		v.challengeAt = map[int64]time.Time{}
	}
	v.challengeAt[uid] = time.Now()
	return true
}

// Fifteen seconds limits prompt floods without materially delaying a user.
const challengeResendCooldown = 15 * time.Second

// Keep the resend throttle bounded independently of the root DM responder.
const challengeResendMapMax = 10000

// SendDMChallenge sends or re-sends one applicant's active private challenge.
func (v *Service) SendDMChallenge(c context.Context, bot *telego.Bot, uid int64) {
	gid, ul, ok := v.firstPending(uid)
	if ok && !v.challengeResendOK(uid) {
		return // pressed again within the cooldown: the prompt they already have is still valid
	}
	channel := &v.messages.Verification.Channel
	if !ok {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), channel.NoPending.For(ul)))
		return
	}
	groupLang := v.groupLanguage(gid)
	// The DM prompt uses one channel; answer settlement enforces each group separately.
	if v.RequiredChannelID(gid) != 0 && !v.isChannelMember(c, bot, gid, uid, groupLang) {
		var rows [][]telego.InlineKeyboardButton
		if curl := v.channelURL(gid); curl != "" {
			rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{
				Text: channel.FollowButton.Render(ul, v.channelDisplay(gid)), URL: curl,
			}))
		}
		rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: channel.ContinueButton.For(ul),
			CallbackData: ChannelRecheckCallbackPrefix + strconv.FormatInt(gid, 10) + ":" + strconv.FormatInt(uid, 10)}))
		_, _ = bot.SendMessage(c, htmlMessage(uid,
			channel.FollowPrompt.Render(ul, v.channelLinkHTML(gid, ul))).
			WithReplyMarkup(tu.InlineKeyboard(rows...)))
		return
	}
	v.sendQuizzes(c, bot, uid)
}

// DM every live challenge; kernel mode routes the next text DM as its answer.
func (v *Service) sendQuizzes(c context.Context, bot verifyBot, uid int64) {
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
				render(v.messages, dq.lang, dq.text, left, dq.nonce, true),    // collapsed tripwire (Bot API 7.4)
				render(v.messages, dq.lang, dq.text, left, dq.nonce, false)) { // without the blockquote, for an old API server
				v.markPrompted(dq.gid, uid) // only a delivered question makes the next DM gradeable
			}
			continue
		}
		gidStr, uidStr := strconv.FormatInt(dq.gid, 10), strconv.FormatInt(uid, 10)
		rows := make([][]telego.InlineKeyboardButton, 0, len(dq.opts))
		for i, opt := range dq.opts {
			rows = append(rows, tu.InlineKeyboardRow(
				telego.InlineKeyboardButton{Text: opt, CallbackData: fmt.Sprintf("%s%s:%s:%s:%d", AnswerCallbackPrefix, gidStr, uidStr, dq.nonce, i)}))
		}
		_, _ = bot.SendMessage(c, htmlMessage(uid,
			v.messages.Verification.Challenge.QuizPrompt.Render(dq.lang, html.EscapeString(dq.text))).
			WithReplyMarkup(tu.InlineKeyboard(rows...)))
	}
}

// Grade DMs only after successful prompt delivery; stray pre-prompt messages cost nothing.
func (v *Service) markPrompted(gid, uid int64) {
	v.mu.Lock()
	if p, ok := v.pend[pkey{gid, uid}]; ok && !p.done {
		p.prompted = true
	}
	v.mu.Unlock()
}

// Return success only when some rendering was delivered.
func (v *Service) sendVerifyDM(c context.Context, bot verifyBot, uid int64, rich, simpler string) bool {
	return v.verificationTransport(bot).SendHTMLFallback(c, uid, rich, simpler)
}

// OnChannelRecheck continues verification after an applicant rechecks channel membership.
func (v *Service) OnChannelRecheck(ctx *th.Context, update telego.Update) error {
	cq := update.CallbackQuery
	if cq == nil {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	parts := strings.SplitN(strings.TrimPrefix(cq.Data, ChannelRecheckCallbackPrefix), ":", 2)
	if len(parts) != 2 {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
		return nil
	}
	gid, _ := strconv.ParseInt(parts[0], 10, 64)
	uid, _ := strconv.ParseInt(parts[1], 10, 64)
	ul := v.applicantLanguage(gid, uid, cq.From.LanguageCode)
	groupLang := v.groupLanguage(gid)
	result := &(*v.messages).Verification.Result
	channel := &(*v.messages).Verification.Channel
	if cq.From.ID != uid {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(result.NotYours.For(ul)).WithShowAlert())
		return nil
	}
	if !v.hasPending(uid) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(result.AlreadyHandled.For(ul)))
		return nil
	}
	if v.RequiredChannelID(gid) != 0 && !v.isChannelMember(c, bot, gid, uid, groupLang) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).
			WithText(channel.NotFollowedYet.Render(ul, v.channelDisplay(gid))).WithShowAlert())
		return nil
	}
	// Acknowledge before sends; membership toasts remain result-driven and happen first.
	_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(channel.ContinueOK.For(ul)))
	v.sendQuizzes(c, bot, uid)
	return nil
}

// OnAnswer settles one nonce-bound quiz callback.
func (v *Service) OnAnswer(ctx *th.Context, update telego.Update) error {
	cq := update.CallbackQuery
	if cq == nil {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	// Accept legacy nonce-less buttons only for restored nonce-less pendings.
	parts := strings.Split(strings.TrimPrefix(cq.Data, AnswerCallbackPrefix), ":")
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
	ul := v.applicantLanguage(gid, owner, cq.From.LanguageCode)
	groupLang := v.groupLanguage(gid)
	result := &(*v.messages).Verification.Result
	channel := &(*v.messages).Verification.Channel
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
	if !v.isChannelMember(c, bot, gid, owner, groupLang) {
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

func (v *Service) isGroupAdmin(ctx context.Context, bot modBot, chatID, userID int64) bool {
	ok, err := v.adminTransport(bot).FreshAdmin(ctx, chatID, userID)
	if err != nil {
		log.Printf("isGroupAdmin getChatMember chat=%d user=%d: %v", chatID, userID, err)
		return false
	}
	return ok
}

// OnAdminAction settles one administrator approval or ban callback.
func (v *Service) OnAdminAction(ctx *th.Context, update telego.Update) error {
	cq := update.CallbackQuery
	if cq == nil {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	parts := strings.SplitN(strings.TrimPrefix(cq.Data, AdminCallbackPrefix), ":", 3)
	if len(parts) != 3 {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
		return nil
	}
	action := parts[0]
	gid, _ := strconv.ParseInt(parts[1], 10, 64)
	target, _ := strconv.ParseInt(parts[2], 10, 64)

	l := v.groupLanguage(gid)
	admin := &v.messages.Verification.Admin
	if !v.isGroupAdmin(c, bot, gid, cq.From.ID) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(admin.OnlyGroupAdmin.For(l)).WithShowAlert())
		return nil
	}
	switch action {
	case "pass":
		p, ok := v.claimPending(gid, target)
		if !ok {
			_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(admin.CannotApprove.For(l)))
			return nil
		}
		// Acknowledge the pending action; failures reopen the request and alert admins.
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(admin.Approving.For(l)))
		v.executeApprove(c, bot, gid, target, p)
	case "ban":
		p, ok := v.consume(gid, target)
		if !ok {
			_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(admin.AlreadyHandled.For(l)))
			return nil
		}
		// Acknowledge the pending action; failures remain visible and the request stays declined.
		duration := verificationBanDurationText(v.messages, l, v.verificationBanDuration(gid))
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(admin.Banning.Render(l, duration)))
		v.executeBan(c, bot, gid, target, p)
	default:
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
	}
	return nil
}

// Required-channel lookup also renders a throttled operator alert when the gate is unavailable.
func (v *Service) isChannelMember(c context.Context, bot modBot, gid, userID int64, groupLang i18n.Lang) bool {
	rc := v.RequiredChannelID(gid)
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
				v.channelAccessAlert(c, bot, groupLang, rc)
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
func (v *Service) channelURL(gid int64) string {
	if u := v.channelInviteURL(gid); u != "" {
		return u
	}
	if d := v.channelDisplay(gid); strings.HasPrefix(d, "@") {
		return "https://t.me/" + d[1:]
	}
	return ""
}

func (v *Service) channelLinkHTML(gid int64, ul i18n.Lang) string {
	d := v.channelDisplay(gid)
	if d == "" {
		d = (*v.messages).Verification.Channel.FallbackName.For(ul) // unnamed channels still read naturally
	}
	if u := v.channelURL(gid); u != "" {
		return fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(u), html.EscapeString(d))
	}
	return html.EscapeString(d)
}

// Caller holds v.mu. A pointer match prevents an old action from releasing a newer claim.
func (v *Service) markTerminalLocked(key pkey, p *pending) {
	if v.terminal == nil {
		v.terminal = make(map[pkey]*pending)
	}
	v.terminal[key] = p
}

// Caller holds v.mu.
func (v *Service) releaseTerminalLocked(key pkey, p *pending) {
	if v.terminal[key] == p {
		delete(v.terminal, key)
	}
}

func (v *Service) consume(gid, uid int64) (*pending, bool) {
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
func (v *Service) consumeNonce(gid, uid int64, nonce string) (*pending, bool) {
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
func (v *Service) consumeExpiry(gid, uid int64, nonce string, epoch uint64) (*pending, bool) {
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
func (v *Service) stopForShutdown() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.shuttingDown = true
	for _, p := range v.pend {
		if p != nil && p.timer != nil {
			p.timer.Stop()
		}
	}
}

// Shutdown freezes pending settlement and persists every verification state file.
func (v *Service) Shutdown() {
	v.stopForShutdown()
	v.save()
	v.saveVerifyFails()
	v.saveHeartbeat()
}

func (v *Service) deleteChallenge(c context.Context, bot verifyBot, gid int64, msgID int) {
	v.verificationTransport(bot).Delete(c, gid, msgID)
}

func (v *Service) adminAlert(c context.Context, bot verifyBot, text string) {
	v.verificationTransport(bot).Alert(c, v.cfg.AdminLogChatID, text)
}

// Failure notices fall back to the acting group when no admin-log chat is configured.
// This keeps optimistic callback acknowledgements from hiding rare network failures.
func (v *Service) failAlert(c context.Context, bot verifyBot, gid int64, text string) {
	v.verificationTransport(bot).FailAlert(c, v.cfg.AdminLogChatID, gid, text)
}

// Throttle unreadable-channel alerts per channel to avoid flooding operators.
func (v *Service) channelAccessAlert(c context.Context, bot verifyBot, l i18n.Lang, channelID int64) {
	v.mu.Lock()
	if last, ok := v.chanAlert[channelID]; ok && time.Since(last) < 10*time.Minute {
		v.mu.Unlock()
		return
	}
	v.chanAlert[channelID] = time.Now()
	v.mu.Unlock()
	admin := &v.messages.Verification.Admin
	mode := admin.ChannelFailOpen.For(l)
	if !v.cfg.FailOpenChannel() {
		mode = admin.ChannelFailClosed.For(l)
	}
	v.adminAlert(c, bot, admin.ChannelAccessFailed.Render(l, channelID, mode))
}

// Keep claimed approvals in the map so network failure can reopen them; consume deletes final claims.
func (v *Service) claimPending(gid, uid int64) (*pending, bool) {
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
func (v *Service) claimPendingNonce(gid, uid int64, nonce string) (*pending, bool) {
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
func (v *Service) approve(c context.Context, bot verifyBot, gid, uid int64) bool {
	p, ok := v.claimPending(gid, uid)
	if !ok {
		return false
	}
	return v.executeApprove(c, bot, gid, uid, p)
}

// Failed approval reopens the claimed pending instead of stranding the applicant.
func (v *Service) executeApprove(c context.Context, bot verifyBot, gid, uid int64, p *pending) bool {
	if err := bot.ApproveChatJoinRequest(c, &telego.ApproveChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
		log.Printf("approve %d in %d: %v", uid, gid, err)
		admin := &v.messages.Verification.Admin
		v.failAlert(c, bot, gid, admin.ApproveFailed.Render(v.groupLanguage(gid), uid, gid, err))
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
func (v *Service) reopenPending(bot verifyBot, gid, uid int64, p *pending) {
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
func (v *Service) wrongAnswerText(groupID int64, l i18n.Lang, banned bool) string {
	result := &v.messages.Verification.Result
	if banned {
		return v.bannedResultText(groupID, l)
	}
	if seconds := v.verifyRetrySeconds(groupID); seconds > 0 {
		return result.WrongRetry.Render(l, seconds)
	}
	return result.WrongNoWait.For(l)
}

func (v *Service) agentCaughtText(groupID int64, l i18n.Lang, banned bool) string {
	result := &v.messages.Verification.Result
	if banned {
		return v.bannedResultText(groupID, l)
	}
	if seconds := v.verifyRetrySeconds(groupID); seconds > 0 {
		return result.AICaught.Render(l, seconds)
	}
	return result.AICaughtNoWait.For(l)
}

func (v *Service) bannedResultText(groupID int64, l i18n.Lang) string {
	duration := verificationBanDurationText(v.messages, l, v.verificationBanDuration(groupID))
	return v.messages.Verification.Result.WrongBanned.Render(l, v.verifyMaxFails(groupID), duration)
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
func (v *Service) decline(c context.Context, bot verifyBot, gid, uid int64, nonce, reason string) (handled, banned bool) {
	p, ok := v.consumeNonce(gid, uid, nonce)
	if !ok {
		return false, false
	}
	return true, v.finishDecline(c, bot, gid, uid, p, reason)
}

// Settle an already-claimed decline, striking only user-caused failures and banning at threshold.
func (v *Service) finishDecline(c context.Context, bot verifyBot, gid, uid int64, p *pending, reason string) (banned bool) {
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
		admin := &v.messages.Verification.Admin
		v.adminAlert(c, bot, admin.DeclineFailed.Render(v.groupLanguage(gid), uid, gid, err))
	}
	if doBan {
		secs := v.verificationBanDuration(gid)
		if err := v.applyVerificationBan(c, bot, gid, uid, secs, false); err != nil {
			log.Printf("verify auto-ban %d in %d: %v", uid, gid, err)
			admin := &v.messages.Verification.Admin
			v.adminAlert(c, bot, admin.AutoBanFailed.Render(v.groupLanguage(gid), uid, gid, count, err))
			banned = false
		} else {
			l := v.groupLanguage(gid)
			duration := verificationBanDurationText(v.messages, l, secs)
			v.adminAlert(c, bot, v.messages.Verification.Admin.AutoBanned.Render(l, uid, gid, count, duration))
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

func (v *Service) finishTerminal(gid, uid int64, p *pending) {
	v.mu.Lock()
	v.releaseTerminalLocked(pkey{gid, uid}, p)
	v.mu.Unlock()
}

// banApplicant reports ban failure honestly even though the join request is still declined.
func (v *Service) banApplicant(c context.Context, bot verifyBot, gid, uid int64) (handled, banned bool) {
	p, ok := v.consume(gid, uid)
	if !ok {
		return false, false
	}
	return true, v.executeBan(c, bot, gid, uid, p)
}

// executeBan settles an already-consumed pending; ban failure remains visible after callback ACK.
func (v *Service) executeBan(c context.Context, bot verifyBot, gid, uid int64, p *pending) (banned bool) {
	defer v.finishTerminal(gid, uid, p)
	_ = bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid})
	banned = true
	if err := v.applyVerificationBan(c, bot, gid, uid, v.verificationBanDuration(gid), true); err != nil { // Honor /bantime like the other ban paths.
		banned = false
		log.Printf("banApplicant %d in %d: %v", uid, gid, err)
		admin := &v.messages.Verification.Admin
		v.failAlert(c, bot, gid, admin.BanFailed.Render(v.groupLanguage(gid), uid, gid, err))
	}
	v.deleteChallenge(c, bot, gid, p.groupMsgID)
	v.recordDecision(false)
	v.save()
	log.Printf("banApplicant user=%d group=%d banned=%v (admin report)", uid, gid, banned)
	return banned
}

// Challenge selection uses crypto/rand; failure degrades to deterministic index zero.
func cryptoIntn(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

func randomQuestion(questions []config.Question) config.Question {
	return questions[cryptoIntn(len(questions))]
}

// Shuffling prevents fixed-position clicks while preserving the correct option's new index.
func shuffledQuestion(q config.Question) (text string, opts []string, correctIdx int) {
	order := make([]int, len(q.Options))
	for i := range order {
		order[i] = i
	}
	for i := len(order) - 1; i > 0; i-- {
		j := cryptoIntn(i + 1)
		order[i], order[j] = order[j], order[i]
	}
	opts = make([]string, len(order))
	for newPos, orig := range order {
		opts[newPos] = q.Options[orig]
		if orig == q.Answer {
			correctIdx = newPos
		}
	}
	return q.Q, opts, correctIdx
}
