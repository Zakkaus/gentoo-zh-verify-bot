package verify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	AdminCallbackPrefix = "adm:" // adm:<action>:<gid>:<uid>[:<nonce>]
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
	SendHTMLFallback(ctx context.Context, chatID int64, rich, simpler string) (*telego.Message, error)
	Delete(ctx context.Context, chatID int64, messageID int)
	Alert(ctx context.Context, adminLogChatID int64, text string)
	AuditLog(ctx context.Context, adminLogChatID int64, text string)
	FailAlert(ctx context.Context, adminLogChatID, groupID int64, text string)
	Notify(ctx context.Context, chatID int64, text string, ttlSeconds int)
	Ban(ctx context.Context, chatID, userID int64, seconds int, revokeMessages bool) error
	Unban(ctx context.Context, chatID, userID int64, onlyIfBanned bool) error
	Mute(ctx context.Context, chatID, userID int64, seconds int) error
	Unmute(ctx context.Context, chatID, userID int64) error
}

// adminTransport is the caller-owned authorization and notice surface used outside moderation.
type adminTransport interface {
	CachedAdmin(ctx context.Context, chatID, userID int64) (bool, error)
	FreshAdmin(ctx context.Context, chatID, userID int64) (bool, error)
	Notify(ctx context.Context, chatID int64, text string, ttlSeconds int)
}

type challengeMessages struct {
	groupMsgID   int
	privateMsgID int
}

// A verification either holds a join request the applicant has not entered on yet, or holds
// someone who is already inside the group and muted until they pass. The two settle through
// entirely different Telegram calls, so every pending records which one it is.
const (
	gateRequest = ""     // default, and what every pre-existing stored record means
	gateMute    = "mute" // the applicant is already a member; passing lifts the restriction
)

type pending struct {
	gate               string // gateRequest or gateMute
	invited            bool   // somebody else added this member, so the group notice tells administrators they can vouch
	held               bool   // a verification mute is actually in place, so passing has something to lift
	holdUntil          int64  // Unix expiry of the mute this verification placed, so another one is recognisable
	passing            bool   // the applicant answered correctly; a retry must complete the approval, never decline
	groupMsgID         int
	privateMsgID       int
	challengeDelivered bool
	mode               string    // challenge type this applicant got: config.ModeKernel (typed answer) or config.ModeQuiz (buttons)
	lang               i18n.Lang // applicant locale from Telegram; every applicant message uses it
	storedLang         string    // original persisted tag retained for byte-stable legacy round trips
	preserveStoredLang bool
	qText              string
	qOpts              []string
	correctIdx         int
	tries              int      // kernel mode: replies used so far (kernelMaxTries before the decline)
	hinted             bool     // kernel mode: the "no Linux installed yet" fallback was already offered
	prompted           bool     // kernel mode: the question has actually been DM'd, so a reply can be graded as an answer
	fallbackPending    bool     // fallback question prepared but not yet confirmed delivered; replies remain ungraded
	sampleBounced      bool     // kernel mode: the "you sent back our own example" nudge was already spent
	noLinuxReminded    bool     // kernel mode: the "no-Linux replies need the current minute" reminder was already spent
	osClarified        bool     // kernel mode: the "you named another OS but sent a real kernel version" clarification was already spent
	fbAnswers          []string // kernel mode: once the short-answer fallback replaced the kernel question, the answers it is graded against
	nonce              string   // per-pending token; a quiz button only counts if its nonce matches
	name               string   // applicant display name, kept so a post-outage re-notify can address them
	deadline           time.Time
	deferredSince      time.Time // first unreachable expiry; retained across recovery and restart
	timer              *time.Timer
	epoch              uint64    // bumped on every (re-)arm; a timer callback carries the epoch it was armed with and no-ops if it no longer matches, so a re-arm (defer / recovery) can't be acted on by the timer it replaced
	lastRenotify       time.Time // last post-outage re-notify, so repeated recoveries don't re-message the same applicant every cycle
	failedAt           time.Time // claim time for rolling-window strike accounting
	deferralCapReached bool      // persisted so the operator warning and short settlement retries survive restart
	settleFailures     int       // consecutive unconfirmed settlements; persisted so the retry stays bounded across restarts
	settlePendingSaid  bool      // the "still being settled" notice was already sent; retries stay silent
	channelUnreadable  bool      // the last required-channel reading failed, so a failure here is not the applicant's
	done               bool
	removed            bool // group removal cancels an in-flight settlement without charging the applicant
}

func (p *pending) persistedLang() string {
	if p.preserveStoredLang {
		return p.storedLang
	}
	return p.lang.String()
}

func (p *pending) messages() challengeMessages {
	return challengeMessages{groupMsgID: p.groupMsgID, privateMsgID: p.privateMsgID}
}

// renotifyItem is one applicant to re-notify after an outage — snapshotted under the lock, then
// messaged outside it. Shared by the runtime-recovery (onRecovery) and restart-recovery (load) paths.
type renotifyItem struct {
	gid, uid    int64
	name        string
	oldMessages challengeMessages
	p           *pending
}

type pendingRec struct {
	UserID             int64    `json:"user_id"`
	GroupID            int64    `json:"group_id"`
	GroupMsgID         int      `json:"group_msg_id"`
	PrivateMsgID       int      `json:"private_msg_id,omitempty"`
	ChallengeDelivered bool     `json:"challenge_delivered,omitempty"`
	Mode               string   `json:"mode,omitempty"`       // empty in a pre-kernel-mode record => quiz
	Lang               string   `json:"lang,omitempty"`       // applicant locale; empty => Simplified Chinese
	FbAnswers          []string `json:"fb_answers,omitempty"` // prepared or confirmed short-answer fallback answers
	FallbackPending    bool     `json:"fallback_pending,omitempty"`
	Prompted           bool     `json:"prompted,omitempty"` // the question was DM'd, so a reply counts as an answer
	// Persist one-shot guards so process restarts cannot replenish free replies.
	Hinted             bool     `json:"hinted,omitempty"`
	SampleBounced      bool     `json:"sample_bounced,omitempty"`
	NoLinuxReminded    bool     `json:"no_linux_reminded,omitempty"`
	OSClarified        bool     `json:"os_clarified,omitempty"`
	Tries              int      `json:"tries,omitempty"`
	QText              string   `json:"q_text"`
	QOpts              []string `json:"q_opts"`
	CorrectIdx         int      `json:"correct_idx"`
	Nonce              string   `json:"nonce"`
	Name               string   `json:"name,omitempty"`
	Deadline           int64    `json:"deadline"`
	DeferredSince      int64    `json:"deferred_since,omitempty"`
	DeferralCapReached bool     `json:"deferral_cap_reached,omitempty"`
	Gate               string   `json:"gate,omitempty"`                // gateMute for a member verified after joining; empty means the join-request gate
	Invited            bool     `json:"invited,omitempty"`             // the member was added by somebody else
	Held               bool     `json:"held,omitempty"`                // a verification mute is in place
	HoldUntil          int64    `json:"hold_until,omitempty"`          // Unix expiry of the mute this verification placed
	ChannelUnreadable  bool     `json:"channel_unreadable,omitempty"`  // the last required-channel reading failed
	Passing            bool     `json:"passing,omitempty"`             // answered correctly; the settlement retry must approve
	SettleFailures     int      `json:"settle_failures,omitempty"`     // consecutive unconfirmed settlements; bounds the retry
	SettlePendingSaid  bool     `json:"settle_pending_said,omitempty"` // the "still being settled" notice was already sent
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
	challengeAt       map[pkey]time.Time  // group+user -> last verification prompt sent (resend throttle), guarded by mu
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
	passed            map[pkey]time.Time  // group+user -> recent verification pass, so the bot's own approval is not re-challenged (guarded by mu)
	timeNow           func() time.Time
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
		challengeAt:    map[pkey]time.Time{},
		vfail:          map[pkey]*vfailRec{},
		settings:       settings,
		telegramClient: telegram,
		lastOnline:     time.Now(), // begin online; the heartbeat only flips us offline after missed contact
		timeNow:        time.Now,
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
	return v.settings.IsGroup(msg.Chat.ID) || msg.Chat.Type == "private"
}

// IsEnabled reports whether automated join verification is enabled for one group.
func (v *Service) IsEnabled(groupID int64) bool {
	group, ok := v.groupSettings(groupID)
	return ok && group.Enabled().Value
}

// DeliveryMode returns one group's effective challenge delivery mode.
func (v *Service) DeliveryMode(groupID int64) string {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return config.DeliveryBoth
	}
	return group.DeliveryMode().Value
}

// SetEnabled updates automated join verification for one group.
func (v *Service) SetEnabled(groupID int64, enabled bool) error {
	if err := v.updateGroupSettings(groupID, func(_ store.GroupView, overrides *store.GroupOverrides) {
		overrides.Enabled = &enabled
	}); err != nil {
		return err
	}
	if !enabled {
		// Verifications already running would otherwise keep their timers and settle after the
		// administrator turned verification off: applicants declined, members removed, failures
		// recorded, all for a rule that no longer applies.
		v.cancelGroupVerifications(groupID)
	}
	return nil
}

// cancelGroupVerifications abandons every verification in one group without settling or striking
// it, and lifts the holds it placed. Nobody is punished for a rule that was withdrawn.
func (v *Service) cancelGroupVerifications(groupID int64) {
	type releaseTarget struct {
		uid      int64
		messages challengeMessages
		held     bool
	}
	var targets []releaseTarget
	v.mu.Lock()
	for key, p := range v.pend {
		if key.gid != groupID || p == nil {
			continue
		}
		p.removed = true
		p.done = true
		if p.timer != nil {
			p.timer.Stop()
		}
		targets = append(targets, releaseTarget{uid: key.uid, messages: p.messages(), held: p.gate == gateMute && p.held})
		delete(v.pend, key)
	}
	v.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	bot := v.handlerBot()
	if bot == nil {
		log.Printf("verification disabled for %d: cancelled %d verification(s); no Telegram handle to clean up with", groupID, len(targets))
		v.save()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cancelCleanupTimeout)
	defer cancel()
	for _, target := range targets {
		v.deleteChallenges(ctx, bot, groupID, target.uid, target.messages)
		if target.held {
			if err := v.releaseMember(ctx, bot, groupID, target.uid, nil); err != nil {
				log.Printf("verification disabled for %d: could not lift the hold on %d: %v", groupID, target.uid, err)
			}
		}
	}
	log.Printf("verification disabled for %d: cancelled %d verification(s) without settling them", groupID, len(targets))
	v.save()
}

// cancelCleanupTimeout bounds the cleanup that follows switching verification off.
const cancelCleanupTimeout = 30 * time.Second

// handlerBot returns the concrete bot the handlers run with, or nil before one is known.
func (v *Service) handlerBot() modBot {
	v.tgMu.Lock()
	defer v.tgMu.Unlock()
	if v.telegramBot == nil {
		return nil
	}
	return v.telegramBot
}

// RemoveGroup cancels every verification owned by an unregistered group without settling or striking it.
func (v *Service) RemoveGroup(groupID int64) {
	v.mu.Lock()
	removed := false
	for key, p := range v.pend {
		if key.gid != groupID {
			continue
		}
		if p != nil {
			p.removed = true
			p.done = true
			if p.timer != nil {
				p.timer.Stop()
			}
		}
		delete(v.pend, key)
		removed = true
	}
	for key, p := range v.terminal {
		if key.gid != groupID {
			continue
		}
		if p != nil {
			p.removed = true
			p.done = true
			if p.timer != nil {
				p.timer.Stop()
			}
		}
		delete(v.terminal, key)
		removed = true
	}
	for key := range v.challengeAt {
		if key.gid == groupID {
			delete(v.challengeAt, key)
		}
	}
	v.mu.Unlock()
	if removed {
		v.save()
	}
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
	return v.gateTimeout(groupID, gateRequest)
}

// livePending returns the unfinished verification for this exact group and member, if any.
func (v *Service) livePending(gid, uid int64) (*pending, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{gid, uid}]
	if !ok || p.done {
		return nil, false
	}
	return p, true
}

// removeDuringCooldown takes a member out again without starting a verification they are not yet
// allowed to attempt. The notice is throttled: a rejoin loop must not become a direct-message
// loop, and the removal alone already conveys the answer.
func (v *Service) removeDuringCooldown(c context.Context, bot modBot, gid, uid int64, l i18n.Lang, wait time.Duration) {
	if _, err := v.removeMember(c, bot, gid, uid); err != nil {
		log.Printf("post-join verify: cannot remove %d from %d during cooldown: %v", uid, gid, err)
		v.failAlert(c, bot, gid, v.adminSays(gateMute).BanFailed.Render(v.groupLanguage(gid), uid, gid, err))
		return
	}
	log.Printf("post-join verify: %d rejoined %d during the failure cooldown (%s left); removed again", uid, gid, wait.Round(time.Second))
	if !v.challengeResendOK(gid, uid) {
		return
	}
	text := v.messages.Verification.Held.CooldownActive.Render(l, durationSecondsCeil(wait))
	_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), text))
}

// verifyInvited reports whether a member somebody else added still has to verify here.
func (v *Service) verifyInvited(groupID int64) bool {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return v.cfg.VerifyInvitedMembers()
	}
	return group.VerifyInvited().Value
}

// postJoinTimeout is the default window for someone verified after joining. An applicant waiting
// outside is watching for the challenge; a member who is already in the group may not notice it
// for a while, and the hold keeps them harmless in the meantime, so they get longer.
const postJoinTimeout = 10 * time.Minute

// gateTimeout returns the verification window. timeout_seconds describes how long an applicant
// waits outside, so the post-join window keeps its own longer default; an administrator who sets
// the timeout in the panel is making a deliberate choice and it applies to both.
func (v *Service) gateTimeout(groupID int64, gate string) time.Duration {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return 0
	}
	setting := group.TimeoutSeconds()
	if gate == gateMute && setting.Source != store.SourceRuntime {
		return postJoinTimeout
	}
	duration, ok := config.SecondsToDuration(setting.Value)
	if !ok {
		return 0
	}
	return duration
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

// holdMember mutes a member for the whole verification window. Telegram only restricts members
// of supergroups, so a basic group cannot hold anyone: the challenge still runs, and failing it
// still removes them, but they can speak in the meantime.
func (v *Service) holdMember(ctx context.Context, bot verifyBot, groupID, userID int64, supergroup bool, p *pending) {
	if !supergroup {
		return
	}
	seconds := int(v.gateTimeout(groupID, gateMute)/time.Second) + muteGraceSeconds
	until := v.wallNow().Add(time.Duration(seconds) * time.Second).Unix()
	if err := v.verificationTransport(bot).Mute(ctx, groupID, userID, seconds); err != nil {
		log.Printf("post-join verify: cannot mute %d in %d (%v); the challenge continues unheld", userID, groupID, err)
		return
	}
	if p == nil {
		return
	}
	v.mu.Lock()
	p.held = true
	p.holdUntil = until
	v.mu.Unlock()
}

// muteGraceSeconds keeps the hold slightly longer than the window so a mute cannot expire in the
// instant between the deadline and the settlement that acts on it.
const muteGraceSeconds = 60

// releaseMember lifts the verification hold by restoring the group's own default permissions.
// Restoring defaults would also lift a restriction somebody else added, so the hold is only
// lifted while the one in force is still the one this verification placed.
func (v *Service) releaseMember(ctx context.Context, bot modBot, groupID, userID int64, p *pending) error {
	if p != nil && !v.holdStillOurs(ctx, bot, groupID, userID, p) {
		log.Printf("post-join verify: the restriction on %d in %d is no longer the one verification placed; leaving it", userID, groupID)
		return nil
	}
	return v.verificationTransport(bot).Unmute(ctx, groupID, userID)
}

// holdStillOurs reports that the restriction Telegram currently reports is the verification hold.
// An unreadable answer counts as ours: failing to release would silence somebody indefinitely,
// which is worse than lifting a restriction an administrator can simply re-apply.
func (v *Service) holdStillOurs(ctx context.Context, bot modBot, groupID, userID int64, p *pending) bool {
	v.mu.Lock()
	until := p.holdUntil
	v.mu.Unlock()
	if until == 0 {
		return true // placed before this was recorded, or by an older build
	}
	cm, err := bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(groupID), UserID: userID})
	if err != nil || cm == nil {
		return true
	}
	restricted, ok := cm.(*telego.ChatMemberRestricted)
	if !ok {
		return true // not restricted any more, or restricted in a shape we cannot compare
	}
	return restricted.UntilDate == until
}

// removeMember takes a failed applicant out of the group without keeping them out: banning and
// immediately unbanning removes them while leaving the invite link usable.
//
// The unban is the dangerous half. An administrator who banned this person moments ago would
// find their ban quietly lifted, so anyone Telegram already reports as banned or gone is left
// exactly as they are: whoever put them there outranks this settlement.
// It reports whether the member was left banned, which happens when the ban lands but the unban
// that follows it does not.
func (v *Service) removeMember(ctx context.Context, bot modBot, groupID, userID int64) (stillBanned bool, err error) {
	if settled, known := v.alreadyOut(ctx, bot, groupID, userID); known && settled {
		log.Printf("post-join verify: %d is already out of %d; leaving that decision alone", userID, groupID)
		return false, nil
	}
	transport := v.verificationTransport(bot)
	if err := transport.Ban(ctx, groupID, userID, 0, false); err != nil {
		return false, err
	}
	if err := transport.Unban(ctx, groupID, userID, true); err != nil {
		// The removal worked, but they are still banned. Telling them to come back would be
		// false, so the caller is told to word it as a ban and operators are told to lift it.
		log.Printf("post-join verify: removed %d from %d but could not lift the ban: %v", userID, groupID, err)
		return true, nil
	}
	return false, nil
}

// alreadyOut reports that somebody is banned or has left. known=false means the answer could not
// be read, and the caller proceeds rather than guessing.
func (v *Service) alreadyOut(ctx context.Context, bot modBot, groupID, userID int64) (out, known bool) {
	cm, err := bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(groupID), UserID: userID})
	if err != nil || cm == nil {
		return false, false
	}
	switch cm.MemberStatus() {
	case telego.MemberStatusBanned, telego.MemberStatusLeft:
		return true, true
	default:
		return false, true
	}
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

// chatMemberState separates "confirmed not a member" from "could not read the answer".
// Settling a request the bot did not get to handle needs that distinction: an unreadable
// answer must not become a claim about where the applicant ended up.
func (v *Service) chatMemberState(c context.Context, bot modBot, chatID, uid int64) (member, known bool) {
	cm, err := bot.GetChatMember(c, &telego.GetChatMemberParams{ChatID: tu.ID(chatID), UserID: uid})
	if err != nil {
		log.Printf("exempt: getChatMember(chat=%d user=%d): %v", chatID, uid, err)
		return false, false
	}
	if cm == nil {
		log.Printf("exempt: getChatMember(chat=%d user=%d) returned no member", chatID, uid)
		return false, false
	}
	switch cm.MemberStatus() {
	case "creator", "administrator", "member":
		return true, true
	default:
		return cm.MemberIsMember(), true
	}
}

// Membership lookup errors fail safe into normal verification, never bypass it.
func (v *Service) isChatMember(c context.Context, bot modBot, chatID, uid int64) bool {
	member, known := v.chatMemberState(c, bot, chatID, uid)
	return known && member
}

// Confirmed members of trusted chats bypass verification and cooldowns.
// Lookup failure is untrusted and follows normal verification.
// Approval failure returns trusted=true so normal verification runs without cooldown rejection.
// trustedMember reports confirmed membership of any group this one trusts. An unreadable lookup
// is not trust, so verification proceeds.
func (v *Service) trustedMember(c context.Context, bot modBot, gid, uid int64) bool {
	for _, src := range v.trustedGroups(gid) {
		if src == 0 || src == gid {
			continue
		}
		if v.isChatMember(c, bot, src, uid) {
			return true
		}
	}
	return false
}

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
			if tg.JoinRequestGone(err) {
				// Already settled in Telegram's own interface; a challenge now would be noise.
				log.Printf("trusted-bypass: join request from %d in %d is already gone: %v", uid, gid, err)
				return true, true
			}
			log.Printf("trusted-bypass: approve %d in %d failed (%v) — falling back to normal verification", uid, gid, err)
			alert := v.messages.Verification.Admin.TrustedBypassFailed.Render(v.groupLanguage(gid), uid, src, gid, err)
			v.adminAlert(c, bot, alert)
			return false, true
		}
		v.clearVerifyFails(gid, uid) // a now-trusted member starts with a clean slate
		v.recordDecision(true)
		v.notePassed(gid, uid)
		log.Printf("verify: trusted-bypass auto-approved %d in %d (already a member of trusted group %d)", uid, gid, src)
		return true, true
	}
	return false, false
}

// Trusted membership is evaluated before the failed-applicant cooldown.
func (v *Service) joinGate(c context.Context, bot modBot, gid, uid int64, applicantLang i18n.Lang) (done bool) {
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
			if tg.JoinRequestGone(err) {
				log.Printf("verify cooldown: join request from %d in %d is already gone: %v", uid, gid, err)
				return true
			}
			log.Printf("verify cooldown: decline %d in %d failed: %v", uid, gid, err)
			v.failAlert(c, bot, gid, v.adminSays(gateRequest).DeclineFailed.Render(v.groupLanguage(gid), uid, gid, err))
			_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), v.messages.Verification.Result.DeclinePending.For(applicantLang)))
			return true
		}
		seconds := durationSecondsCeil(v.verifyCooldownRemaining(gid, uid))
		log.Printf("verify cooldown: declined early re-apply from %d in %d (%ds left)", uid, gid, seconds)
		// The decline is the answer; repeating the explanation on every re-apply would turn a
		// determined applicant into a direct-message loop. Same throttle the post-join gate uses.
		if v.challengeResendOK(gid, uid) {
			_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), v.messages.Verification.Result.CooldownActive.Render(applicantLang, seconds)))
		}
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

// Only a confirmed challenge delivery may expire as an applicant-caused timeout.
func challengeExpiryReason(delivered bool) string {
	if !delivered {
		return "challenge-post-failed"
	}
	return "timeout"
}

// Delivery has its own no-fault deadline; the applicant's answer window starts only after confirmation.
const pendingDeliveryTimeout = 60 * time.Second

func (v *Service) expiryDelay(gid int64, gate, reason string) time.Duration {
	if reason == challengeExpiryReason(false) {
		return pendingDeliveryTimeout
	}
	return v.gateTimeout(gid, gate)
}

// Reserve capacity before delivery; only a confirmed challenge may install a striking timeout.
func (v *Service) startPending(bot modBot, gid, uid int64, p *pending) (oldMessages challengeMessages, status pendingStartStatus) {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := pkey{gid, uid}
	old, replacing := v.pend[key]
	if inFlight := v.terminal[key]; inFlight != nil || replacing && old.done {
		return challengeMessages{}, pendingBlockedTerminal
	}
	if !replacing && !v.pendingCapacityOKLocked(gid) {
		return challengeMessages{}, pendingBlockedCapacity
	}
	if replacing {
		old.done = true
		if old.timer != nil {
			old.timer.Stop()
		}
		oldMessages = old.messages()
		// Re-applying must not replenish attempts or one-shot guards.
		p.tries, p.hinted, p.sampleBounced = old.tries, old.hinted, old.sampleBounced
		p.noLinuxReminded, p.osClarified = old.noLinuxReminded, old.osClarified
	}
	delay := pendingDeliveryTimeout
	p.deadline = v.wallNow().Add(delay)
	v.pend[key] = p
	v.armExpiry(bot, p, gid, uid, delay, challengeExpiryReason(false))
	return oldMessages, pendingStarted
}

// Start a full window after delivery while preserving no-fault status on send failure.
func (v *Service) finishPendingChallenge(
	bot modBot,
	gid, uid int64,
	p *pending,
	messages challengeMessages,
	delivered bool,
) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if cur, ok := v.pend[pkey{gid, uid}]; !ok || cur != p || p.done {
		return false
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	p.groupMsgID = messages.groupMsgID
	p.privateMsgID = messages.privateMsgID
	p.challengeDelivered = delivered
	delay := v.gateTimeout(gid, p.gate)
	p.deadline = v.wallNow().Add(delay)
	v.armExpiry(bot, p, gid, uid, delay, challengeExpiryReason(delivered))
	return true
}

// One process-wide throttle prevents a multi-group flood from spamming operator alerts.
func (v *Service) alertPendingCap(c context.Context, bot modBot, gid int64, gate string) {
	now := time.Now()
	v.mu.Lock()
	if !v.pendingCapAlertAt.IsZero() && now.Sub(v.pendingCapAlertAt) < pendingCapAlertCooldown {
		v.mu.Unlock()
		return
	}
	v.pendingCapAlertAt = now
	v.mu.Unlock()
	v.failAlert(c, bot, gid, v.adminSays(gate).PendingCap.Render(v.groupLanguage(gid), pendingGlobalCap, pendingPerGroupCap, gid))
}

type challengeDeliveryResult struct {
	messages             challengeMessages
	delivered            bool
	active               bool
	modeLabel            string
	replacedPrivateMsgID int
}

// deliverPendingChallenge is the single delivery-mode decision for initial and recovery challenges.
// gateOf reads the gate of the pending a delivery belongs to; a delivery with no owner follows
// the join-request wording, which is what a user-triggered resend of a request challenge wants.
func gateOf(owner *pending) challengeVoice {
	if owner == nil {
		return challengeVoice{gate: gateRequest}
	}
	return challengeVoice{gate: owner.gate, invited: owner.invited, nonce: owner.nonce}
}

// challengeVoice selects the public challenge wording: applying, arriving on their own, or
// being brought in by somebody who can vouch for them.
type challengeVoice struct {
	gate    string
	invited bool
	nonce   string
}

func (v *Service) deliverPendingChallenge(
	c context.Context,
	bot modBot,
	gid, uid int64,
	name string,
	owner *pending,
) challengeDeliveryResult {
	result := challengeDeliveryResult{active: true, modeLabel: v.DeliveryMode(gid)}
	groupLang := v.groupLanguage(gid)
	switch result.modeLabel {
	case config.DeliveryGroup:
		result.messages.groupMsgID = v.postGroupChallenge(c, bot, gid, uid, name, groupLang, gateOf(owner))
		result.delivered = result.messages.groupMsgID != 0
	case config.DeliveryBoth:
		result.messages.groupMsgID = v.postGroupChallenge(c, bot, gid, uid, name, groupLang, gateOf(owner))
		result.delivered = result.messages.groupMsgID != 0
		outcome := v.attemptPrivateChallenge(c, bot, gid, uid, owner)
		result.messages.privateMsgID = outcome.privateMsgID
		result.replacedPrivateMsgID = outcome.replacedPrivateMsgID
		switch outcome.result {
		case privateDelivered:
			result.delivered = true
			result.modeLabel = "both-delivered"
		case privateRejected:
			result.modeLabel = "group-private-rejected"
		case privateUncertain:
			result.modeLabel = "group-private-uncertain"
		case privateGone:
			result.active = false
			v.deleteChallenges(c, bot, gid, uid, result.messages)
		}
	case config.DeliveryDM:
		outcome := v.attemptPrivateChallenge(c, bot, gid, uid, owner)
		result.messages.privateMsgID = outcome.privateMsgID
		result.replacedPrivateMsgID = outcome.replacedPrivateMsgID
		switch outcome.result {
		case privateDelivered:
			result.delivered = true
			result.modeLabel = "private"
		case privateRejected:
			result.modeLabel = "group-fallback"
			result.messages.groupMsgID = v.postGroupChallenge(c, bot, gid, uid, name, groupLang, gateOf(owner))
			result.delivered = result.messages.groupMsgID != 0
		case privateUncertain:
			result.modeLabel = "private-uncertain"
		case privateGone:
			result.active = false
		}
	}
	return result
}

// OnJoinRequest starts verification for one eligible group join request.
func (v *Service) OnJoinRequest(ctx *th.Context, update telego.Update) error {
	jr := update.ChatJoinRequest
	if jr == nil || !v.settings.IsGroup(jr.Chat.ID) {
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
	// Applicant and group surfaces resolve independently at the update boundary.
	applicantLang := i18n.FromTelegram(jr.From.LanguageCode)
	// Trusted bypass precedes cooldown enforcement.
	// Untrusted applicants then face the retry cooldown.
	if v.joinGate(c, bot, gid, uid, applicantLang) {
		return nil
	}
	mode, text, opts, correctIdx := v.newChallenge(gid, applicantLang)
	name := applicantDisplayName(&jr.From)
	p := &pending{mode: mode, lang: applicantLang, qText: text, qOpts: opts, correctIdx: correctIdx,
		nonce: newNonce(), name: name}
	oldMessages, status := v.startPending(bot, gid, uid, p)
	switch status {
	case pendingBlockedCapacity:
		log.Printf("join %d in group %d: pending cap reached; left for manual review", uid, gid)
		v.alertPendingCap(c, bot, gid, gateRequest)
		return nil
	case pendingBlockedTerminal:
		log.Printf("join %d in group %d: terminal action still in flight; deferred re-application", uid, gid)
		return nil
	}
	v.deleteChallenges(c, bot, gid, uid, oldMessages)

	delivery := v.deliverPendingChallenge(c, bot, gid, uid, name, p)
	if !delivery.active {
		return nil
	}
	if delivery.replacedPrivateMsgID != 0 {
		v.deleteChallenge(c, bot, uid, delivery.replacedPrivateMsgID)
	}
	if !v.finishPendingChallenge(bot, gid, uid, p, delivery.messages, delivery.delivered) {
		v.deleteChallenges(c, bot, gid, uid, delivery.messages)
		return nil // another action handled or replaced this request while delivery was in flight
	}
	v.save()
	log.Printf("join %d (@%s) in group %d: pending (%s challenge), delivery=%s, group message=%d, private message=%d",
		uid, jr.From.Username, gid, mode, delivery.modeLabel, delivery.messages.groupMsgID, delivery.messages.privateMsgID)
	return nil
}

// recentPassWindow suppresses a second verification for someone the bot itself just let in:
// approving a join request produces a membership update that would otherwise read as a fresh
// arrival needing a challenge.
const recentPassWindow = 5 * time.Minute

func (v *Service) notePassed(gid, uid int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.passed == nil {
		v.passed = make(map[pkey]time.Time)
	}
	now := v.wallNow()
	for key, at := range v.passed {
		if now.Sub(at) > recentPassWindow {
			delete(v.passed, key)
		}
	}
	v.passed[pkey{gid, uid}] = now
}

func (v *Service) recentlyPassed(gid, uid int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	at, ok := v.passed[pkey{gid, uid}]
	return ok && v.wallNow().Sub(at) <= recentPassWindow
}

// OnMemberJoined verifies someone who is already inside a guarded group. Groups that ask people
// to apply never reach this for their applicants — the bot's own approval is remembered — but a
// group without join approval, and anyone an administrator adds directly, both arrive here.
func (v *Service) OnMemberJoined(ctx *th.Context, update telego.Update) error {
	member := update.ChatMember
	if member == nil || !joinedNow(member) {
		return nil
	}
	gid := member.Chat.ID
	user := member.NewChatMember.MemberUser()
	uid := user.ID
	if !v.settings.IsGroup(gid) || !v.IsEnabled(gid) || user.IsBot || uid == v.botID {
		return nil
	}
	if v.recentlyPassed(gid, uid) {
		return nil // the bot let them in a moment ago; that was the verification
	}
	bot := ctx.Bot()
	c := ctx.Context()
	applicantLang := i18n.FromTelegram(user.LanguageCode)
	if v.trustedMember(c, bot, gid, uid) {
		// Someone already inside the group needs no admitting: trusting them simply means not
		// asking. Calling approve here would fail — there is no join request — and the failure
		// would quietly put a trusted member through the challenge anyway.
		v.clearVerifyFails(gid, uid)
		v.recordDecision(true)
		v.notePassed(gid, uid)
		log.Printf("post-join verify: %d in %d is a member of a trusted group; not challenged", uid, gid)
		return nil
	}
	if v.isGroupAdmin(c, bot, gid, uid) {
		return nil // administrators are not challenged in their own group
	}
	invited := member.From.ID != uid
	if invited && !v.verifyInvited(gid) {
		log.Printf("post-join verify: %d was invited into %d and invited members are exempt here", uid, gid)
		return nil
	}
	// A member already being verified must not be re-challenged by a second arrival: rejoining
	// would otherwise post a fresh notice and question every time. Re-apply the hold, since
	// leaving the group clears it, and leave the running deadline alone so nobody can extend
	// their own window by walking in and out.
	if live, ok := v.livePending(gid, uid); ok {
		// A verification started while they were outside settles by declining a join request.
		// They are inside now, so that call would settle nothing and the member would simply
		// stay, unverified, forever. Move the running verification onto the gate that can
		// actually end it, keeping the progress they have made.
		v.adoptAsHeld(gid, uid, live)
		v.holdMember(c, bot, gid, uid, member.Chat.Type == telego.ChatTypeSupergroup, live)
		v.save()
		log.Printf("post-join verify: %d entered %d while still being verified; kept the running challenge", uid, gid)
		return nil
	}
	// The bot tells a removed member how long to wait. Honour it, or that promise means nothing
	// and every rejoin costs a fresh notice and question. Somebody an administrator just added is
	// exempt: removing them seconds later would be the bot overruling the person who added them.
	// They still verify — being vouched for is not verification — they are just not thrown out
	// over an earlier failure.
	if wait := v.verifyCooldownRemaining(gid, uid); wait > 0 && !invited {
		v.removeDuringCooldown(c, bot, gid, uid, applicantLang, wait)
		return nil
	}
	supergroup := member.Chat.Type == telego.ChatTypeSupergroup
	mode, text, opts, correctIdx := v.newChallenge(gid, applicantLang)
	name := applicantDisplayName(&user)
	p := &pending{gate: gateMute, invited: invited, mode: mode, lang: applicantLang,
		qText: text, qOpts: opts, correctIdx: correctIdx, nonce: newNonce(), name: name}
	oldMessages, status := v.startPending(bot, gid, uid, p)
	switch status {
	case pendingBlockedCapacity:
		// An applicant blocked here waits outside and an administrator can still admit them. A
		// member blocked here is already inside and would simply stay, unverified. Take them
		// back out — no strike, they did nothing wrong — so they can return once the queue drains.
		log.Printf("post-join verify: %d in %d skipped, pending cap reached; removing them until the queue drains", uid, gid)
		v.alertPendingCap(c, bot, gid, gateMute)
		if _, err := v.removeMember(c, bot, gid, uid); err != nil {
			log.Printf("post-join verify: cannot remove %d from %d at capacity: %v", uid, gid, err)
		}
		return nil
	case pendingBlockedTerminal:
		log.Printf("post-join verify: %d in %d skipped, terminal action still in flight", uid, gid)
		return nil
	}
	v.deleteChallenges(c, bot, gid, uid, oldMessages)
	// Persist before restricting anyone. A crash between the two would otherwise leave a member
	// silenced by a verification no restart knows about.
	v.save()
	v.holdMember(c, bot, gid, uid, supergroup, p)

	delivery := v.deliverPendingChallenge(c, bot, gid, uid, name, p)
	if !delivery.active {
		return nil
	}
	if delivery.replacedPrivateMsgID != 0 {
		v.deleteChallenge(c, bot, uid, delivery.replacedPrivateMsgID)
	}
	if !v.finishPendingChallenge(bot, gid, uid, p, delivery.messages, delivery.delivered) {
		v.deleteChallenges(c, bot, gid, uid, delivery.messages)
		return nil
	}
	v.save()
	log.Printf("post-join verify: %d (@%s) joined %d: pending (%s challenge), held=%v, delivery=%s",
		uid, user.Username, gid, mode, supergroup, delivery.modeLabel)
	return nil
}

// joinedNow reports a membership update where someone who was outside the group is now an
// ordinary member. Promotions, demotions and departures are not arrivals.
func joinedNow(member *telego.ChatMemberUpdated) bool {
	if member.NewChatMember == nil || member.OldChatMember == nil {
		return false
	}
	if member.NewChatMember.MemberStatus() != telego.MemberStatusMember {
		return false
	}
	switch member.OldChatMember.MemberStatus() {
	case telego.MemberStatusLeft, telego.MemberStatusBanned:
		return true
	case telego.MemberStatusRestricted:
		return !member.OldChatMember.MemberIsMember()
	default:
		return false
	}
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

// Use one pending for legacy bare verification links; map iteration preserves the existing selection rule.
func (v *Service) firstPending(uid int64) (gid int64, ul i18n.Lang, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, p := range v.pend {
		if k.uid == uid && !p.done {
			return k.gid, p.lang, true
		}
	}
	return 0, 0, false
}

// Throttle each group's /start prompt independently so concurrent requests do not block one another.
func (v *Service) challengeResendOK(gid, uid int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := time.Now()
	key := pkey{gid, uid}
	if last, ok := v.challengeAt[key]; ok && now.Sub(last) < challengeResendCooldown {
		return false
	}
	if len(v.challengeAt) >= challengeResendMapMax {
		cutoff := now.Add(-challengeResendCooldown)
		for pendingKey, last := range v.challengeAt {
			if !last.After(cutoff) {
				delete(v.challengeAt, pendingKey)
			}
		}
		if len(v.challengeAt) >= challengeResendMapMax {
			v.challengeAt = map[pkey]time.Time{}
		}
	}
	v.challengeAt[key] = now
	return true
}

// Fifteen seconds limits prompt floods without materially delaying a user.
const challengeResendCooldown = 15 * time.Second

// Keep the resend throttle bounded independently of the root DM responder.
const challengeResendMapMax = 10000

type dmPrompt struct {
	gid             int64
	mode            string
	lang            i18n.Lang
	text            string
	opts            []string
	nonce           string
	tries           int
	fallback        bool
	fallbackPending bool
	pending         *pending
}

type dmSendResult struct {
	messageID         int
	replacedMessageID int
	current           bool
	stateChanged      bool
}

// owner binds a delivery to the pending it was started for. A recovery that took a snapshot
// must not write its message IDs into a replacement pending created meanwhile by a fresh
// application, or the replacement ends up with a full window and no visible challenge.
// A nil owner means "whatever pending is current", which is what a user-triggered resend wants.
func (v *Service) pendingDMChallenge(gid, uid int64, owner *pending) (dmPrompt, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{gid, uid}]
	if !ok || p.done {
		return dmPrompt{}, false
	}
	if owner != nil && (p != owner || p.nonce != owner.nonce) {
		return dmPrompt{}, false
	}
	return dmPrompt{gid: gid, mode: p.mode, lang: p.lang, text: p.qText, opts: p.qOpts,
		nonce: p.nonce, tries: p.tries, fallback: len(p.fbAnswers) > 0,
		fallbackPending: p.fallbackPending, pending: p}, true
}

func (v *Service) pendingDMChallenges(uid int64) []dmPrompt {
	var prompts []dmPrompt
	v.mu.Lock()
	for k, p := range v.pend {
		if k.uid == uid && !p.done {
			prompts = append(prompts, dmPrompt{gid: k.gid, mode: p.mode, lang: p.lang, text: p.qText,
				opts: p.qOpts, nonce: p.nonce, tries: p.tries, fallback: len(p.fbAnswers) > 0,
				fallbackPending: p.fallbackPending, pending: p})
		}
	}
	v.mu.Unlock()
	return prompts
}

func definiteDMFailure(err error) bool {
	if err == nil {
		return false
	}
	code := tg.ErrorCode(err)
	return tg.CannotInitiateConversation(err) || tg.BotWasBlockedByUser(err) || code >= 400 && code < 500
}

// Bind delivery completion to the pending pointer and nonce captured before the Bot API call.
func (v *Service) completeDMDelivery(
	bot modBot,
	uid int64,
	prompt dmPrompt,
	messageID int,
	sendErr error,
	question bool,
	resetExpiry bool,
) (current, changed bool, oldPrivateMsgID int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{prompt.gid, uid}]
	if !ok || p != prompt.pending || p.done || p.nonce != prompt.nonce {
		return false, false, 0
	}
	if sendErr != nil {
		if question && prompt.fallbackPending && p.fallbackPending && definiteDMFailure(sendErr) {
			p.qText = kernelQuestion(v.messages, p.lang)
			p.fbAnswers = nil
			p.fallbackPending = false
			p.hinted = false
			if resetExpiry {
				if p.timer != nil {
					p.timer.Stop()
				}
				delay := v.gateTimeout(prompt.gid, prompt.pending.gate)
				p.deadline = v.wallNow().Add(delay)
				v.armExpiry(bot, p, prompt.gid, uid, delay, challengeExpiryReason(true))
			}
			return true, true, 0
		}
		return true, false, 0
	}
	resetDeadline := false
	if question && prompt.fallbackPending && p.fallbackPending {
		p.fallbackPending = false
		resetDeadline = resetExpiry
		changed = true
	}
	if messageID != 0 && p.privateMsgID != messageID {
		oldPrivateMsgID = p.privateMsgID
		p.privateMsgID = messageID
		changed = true
	}
	if !p.challengeDelivered {
		p.challengeDelivered = true
		resetDeadline = resetExpiry
		changed = true
	}
	if resetDeadline {
		if p.timer != nil {
			p.timer.Stop()
		}
		delay := v.gateTimeout(prompt.gid, prompt.pending.gate)
		p.deadline = v.wallNow().Add(delay)
		v.armExpiry(bot, p, prompt.gid, uid, delay, challengeExpiryReason(true))
	}
	if question && p.mode == config.ModeKernel && !p.prompted {
		p.prompted = true
		changed = true
	}
	return true, changed, oldPrivateMsgID
}

func (v *Service) sendChannelPrompt(c context.Context, bot verifyBot, gid, uid int64, ul i18n.Lang) (int, error) {
	channel := &v.messages.Verification.Channel
	if v.channelWasUnreadable(gid, uid) {
		// Asking someone to join a channel they may already be in is misleading when the bot
		// simply could not look. Say what actually happened instead.
		sent, err := bot.SendMessage(c, htmlMessage(uid, channel.Unreadable.For(ul)))
		return msgID(sent), err
	}
	var rows [][]telego.InlineKeyboardButton
	if curl := v.channelURL(gid); curl != "" {
		rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{
			Text: channel.FollowButton.Render(ul, v.channelDisplay(gid)), URL: curl,
		}))
	}
	rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: channel.ContinueButton.For(ul),
		CallbackData: ChannelRecheckCallbackPrefix + strconv.FormatInt(gid, 10) + ":" + strconv.FormatInt(uid, 10)}))
	sent, err := bot.SendMessage(c, htmlMessage(uid,
		channel.FollowPrompt.Render(ul, v.channelLinkHTML(gid, ul))).
		WithReplyMarkup(tu.InlineKeyboard(rows...)))
	return msgID(sent), err
}

func (v *Service) sendDMQuestionRetainingPrevious(
	c context.Context,
	bot modBot,
	uid int64,
	prompt dmPrompt,
	resetExpiry bool,
) (dmSendResult, error) {
	var sent *telego.Message
	var err error
	if prompt.mode == config.ModeKernel {
		left := kernelMaxTries - prompt.tries
		render := kernelPromptHTML
		if prompt.fallback {
			render = fallbackPromptHTML
		}
		gate := gateOf(prompt.pending).gate
		sent, err = v.sendVerifyDM(c, bot, uid,
			render(v.messages, prompt.lang, prompt.text, left, prompt.nonce, true, gate),
			render(v.messages, prompt.lang, prompt.text, left, prompt.nonce, false, gate))
	} else {
		gidStr, uidStr := strconv.FormatInt(prompt.gid, 10), strconv.FormatInt(uid, 10)
		rows := make([][]telego.InlineKeyboardButton, 0, len(prompt.opts))
		for i, opt := range prompt.opts {
			rows = append(rows, tu.InlineKeyboardRow(
				telego.InlineKeyboardButton{Text: opt, CallbackData: fmt.Sprintf("%s%s:%s:%s:%d", AnswerCallbackPrefix, gidStr, uidStr, prompt.nonce, i)}))
		}
		sent, err = bot.SendMessage(c, htmlMessage(uid,
			v.messages.Verification.Challenge.QuizPrompt.Render(prompt.lang, html.EscapeString(prompt.text))).
			WithReplyMarkup(tu.InlineKeyboard(rows...)))
	}
	messageID := msgID(sent)
	current, changed, oldPrivateMsgID := v.completeDMDelivery(bot, uid, prompt, messageID, err, true, resetExpiry)
	return dmSendResult{
		messageID:         messageID,
		replacedMessageID: oldPrivateMsgID,
		current:           current,
		stateChanged:      changed,
	}, err
}

func (v *Service) sendDMQuestion(c context.Context, bot modBot, uid int64, prompt dmPrompt) (dmSendResult, error) {
	result, err := v.sendDMQuestionRetainingPrevious(c, bot, uid, prompt, true)
	if result.replacedMessageID != 0 {
		v.deleteChallenge(c, bot, uid, result.replacedMessageID)
	}
	return result, err
}

// sendDMChallengeForGroup delivers only the named pending request and lets its caller clean up the replaced message.
func (v *Service) sendDMChallengeForGroup(
	c context.Context,
	bot modBot,
	gid, uid int64,
	resetExpiry bool,
	owner *pending,
) (active bool, privateMsgID, replacedPrivateMsgID int, err error) {
	prompt, ok := v.pendingDMChallenge(gid, uid, owner)
	if !ok {
		return false, 0, 0, nil
	}
	if v.RequiredChannelID(gid) != 0 && !v.isChannelMember(c, bot, gid, uid, v.groupLanguage(gid)) {
		privateMsgID, err := v.sendChannelPrompt(c, bot, gid, uid, prompt.lang)
		current, changed, oldPrivateMsgID := v.completeDMDelivery(bot, uid, prompt, privateMsgID, err, false, resetExpiry)
		if changed {
			v.save()
		}
		if !current {
			v.deleteChallenge(c, bot, uid, privateMsgID)
			return false, 0, 0, err
		}
		return true, privateMsgID, oldPrivateMsgID, err
	}
	result, err := v.sendDMQuestionRetainingPrevious(c, bot, uid, prompt, resetExpiry)
	if result.stateChanged {
		v.save()
	}
	if !result.current {
		v.deleteChallenge(c, bot, uid, result.messageID)
		return false, 0, 0, err
	}
	return true, result.messageID, result.replacedMessageID, err
}

type privateDeliveryResult uint8

const (
	privateDelivered privateDeliveryResult = iota
	privateRejected
	privateUncertain
	privateGone
)

type privateDeliveryOutcome struct {
	result               privateDeliveryResult
	privateMsgID         int
	replacedPrivateMsgID int
}

// attemptPrivateChallenge classifies private delivery without assuming that ambiguous failures rejected the send.
// rateLimitSendMargin leaves room for the retried send itself after a flood wait.
const rateLimitSendMargin = 10 * time.Second

// deliveryBudget is the time left before the pending this delivery belongs to expires.
func (v *Service) deliveryBudget(gid, uid int64, owner *pending) time.Duration {
	v.mu.Lock()
	defer v.mu.Unlock()
	p := owner
	if p == nil {
		p = v.pend[pkey{gid, uid}]
	}
	if p == nil {
		return pendingDeliveryTimeout
	}
	return p.deadline.Sub(v.wallNow())
}

func (v *Service) attemptPrivateChallenge(c context.Context, bot modBot, gid, uid int64, owner *pending) privateDeliveryOutcome {
	active, privateMsgID, replacedPrivateMsgID, err := v.sendDMChallengeForGroup(c, bot, gid, uid, false, owner)
	if !active {
		return privateDeliveryOutcome{result: privateGone}
	}
	if err == nil {
		return privateDeliveryOutcome{
			result:               privateDelivered,
			privateMsgID:         privateMsgID,
			replacedPrivateMsgID: replacedPrivateMsgID,
		}
	}
	if tg.IsRateLimited(err) {
		wait := tg.RetryAfter(err)
		// Waiting out a flood limit is only worth it if the applicant's own window outlives the
		// wait. Sleeping past their deadline means they are declined before the question ever
		// arrives; falling through instead lets the group challenge carry the verification.
		if wait > 0 && wait < v.gateTimeout(gid, gateOf(owner).gate) && wait+rateLimitSendMargin <= v.deliveryBudget(gid, uid, owner) {
			log.Printf("join %d in %d: private challenge rate-limited; retrying after %s", uid, gid, wait)
			if !tg.Pace(c, wait) {
				return privateDeliveryOutcome{result: privateUncertain}
			}
			active, privateMsgID, replacedPrivateMsgID, err = v.sendDMChallengeForGroup(c, bot, gid, uid, false, owner)
			if !active {
				return privateDeliveryOutcome{result: privateGone}
			}
			if err == nil {
				return privateDeliveryOutcome{
					result:               privateDelivered,
					privateMsgID:         privateMsgID,
					replacedPrivateMsgID: replacedPrivateMsgID,
				}
			}
		}
	}
	switch {
	case tg.CannotInitiateConversation(err):
		return privateDeliveryOutcome{result: privateRejected}
	case tg.BotWasBlockedByUser(err):
		log.Printf("join %d in %d: private challenge rejected because the bot is blocked", uid, gid)
		return privateDeliveryOutcome{result: privateRejected}
	case tg.ErrorCode(err) >= 400 && tg.ErrorCode(err) < 500:
		log.Printf("join %d in %d: private challenge rejected by Telegram (%v)", uid, gid, err)
		return privateDeliveryOutcome{result: privateRejected}
	default:
		log.Printf("join %d in %d: private challenge delivery is uncertain (%v)", uid, gid, err)
		return privateDeliveryOutcome{result: privateUncertain}
	}
}

// SendDMChallenge sends or re-sends an applicant's private challenge. A zero group keeps legacy bare-link behavior.
func (v *Service) SendDMChallenge(c context.Context, bot *telego.Bot, uid int64, languageCode string, groupID int64) {
	channel := &v.messages.Verification.Channel
	if groupID != 0 {
		if _, ok := v.pendingDMChallenge(groupID, uid, nil); !ok {
			_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), channel.NoPending.For(i18n.FromTelegram(languageCode))))
			return
		}
		if !v.challengeResendOK(groupID, uid) {
			return
		}
		active, _, replacedPrivateMsgID, err := v.sendDMChallengeForGroup(c, bot, groupID, uid, true, nil)
		if active && replacedPrivateMsgID != 0 {
			v.deleteChallenge(c, bot, uid, replacedPrivateMsgID)
		}
		if err != nil {
			log.Printf("verify DM for %d in %d failed: %v", uid, groupID, err)
		}
		return
	}

	gid, ul, ok := v.firstPending(uid)
	if ok && !v.challengeResendOK(gid, uid) {
		return
	}
	if !ok {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), channel.NoPending.For(i18n.FromTelegram(languageCode))))
		return
	}
	// Bare links preserve the legacy first-pending channel gate and all-pending fan-out.
	if v.RequiredChannelID(gid) != 0 && !v.isChannelMember(c, bot, gid, uid, v.groupLanguage(gid)) {
		if _, err := v.sendChannelPrompt(c, bot, gid, uid, ul); err != nil {
			log.Printf("verify DM channel prompt for %d in %d failed: %v", uid, gid, err)
		}
		return
	}
	v.sendQuizzes(c, bot, uid)
}

// DM every live challenge; kernel mode routes the next text DM as its answer.
func (v *Service) sendQuizzes(c context.Context, bot modBot, uid int64) {
	stateChanged := false
	for _, prompt := range v.pendingDMChallenges(uid) {
		result, _ := v.sendDMQuestion(c, bot, uid, prompt)
		stateChanged = stateChanged || result.stateChanged
		if !result.current {
			v.deleteChallenge(c, bot, uid, result.messageID)
		}
	}
	if stateChanged {
		v.save()
	}
}

func (v *Service) sendVerifyDM(c context.Context, bot verifyBot, uid int64, rich, simpler string) (*telego.Message, error) {
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
		gate := v.pendingGate(gid, owner)
		outcome, banned := v.decline(c, bot, gid, owner, nonce, wrongAnswerReason)
		text := v.voice(gate).AlreadyHandled.For(ul)
		if outcome != declineNoPending {
			text = v.declineResultText(outcome, ul, gate, func() string { return v.wrongAnswerText(gid, ul, gate, banned) })
		}
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(text).WithShowAlert())
		return nil
	}
	if !v.isChannelMember(c, bot, gid, owner, groupLang) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).
			WithText(channel.NotFollowedYet.Render(ul, v.channelDisplay(gid))).WithShowAlert())
		return nil
	}
	p, claimed := v.claimPendingNonce(gid, owner, nonce)
	if claimed && v.executeApprove(c, bot, gid, owner, p) == approveConfirmed {
		text := v.voice(v.pendingGate(gid, owner)).Passed.For(ul)
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

// Verification keeps no permanent record in the group: the failure notice for an
// administrator button is cleaned up like the challenge it belongs to.
const adminActionNoticeTTL = 240

// OnAdminAction settles one administrator approval or ban callback.
func (v *Service) OnAdminAction(ctx *th.Context, update telego.Update) error {
	cq := update.CallbackQuery
	if cq == nil {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	parts := strings.SplitN(strings.TrimPrefix(cq.Data, AdminCallbackPrefix), ":", 4)
	if len(parts) < 3 {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
		return nil
	}
	action := parts[0]
	gid, _ := strconv.ParseInt(parts[1], 10, 64)
	target, _ := strconv.ParseInt(parts[2], 10, 64)
	// The nonce ties the button to the verification it was posted for. A button left behind by a
	// failed deletion would otherwise settle whatever verification is running now — one the
	// administrator never looked at. Buttons posted before this existed carry no nonce and keep
	// the old behaviour; they disappear with the verification they belong to.
	nonce := ""
	if len(parts) == 4 {
		nonce = parts[3]
	}

	l := v.groupLanguage(gid)
	admin := &v.messages.Verification.Admin
	if nonce != "" && !v.pendingHasNonce(gid, target, nonce) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(admin.AlreadyHandled.For(l)).WithShowAlert())
		return nil
	}
	if !v.isGroupAdmin(c, bot, gid, cq.From.ID) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(admin.OnlyGroupAdmin.For(l)).WithShowAlert())
		return nil
	}
	switch action {
	case "pass":
		gate := v.pendingGate(gid, target)
		says := v.adminSays(gate)
		p, ok := v.claimPending(gid, target)
		if !ok {
			_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(says.CannotApprove.For(l)))
			return nil
		}
		// Acknowledge the pending action; failures reopen the request, tell the group, and alert admins.
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(says.Approving.For(l)))
		switch v.executeApprove(c, bot, gid, target, p) {
		case approveFailed:
			v.verificationTransport(bot).Notify(c, gid, says.ActionFailed.For(l), adminActionNoticeTTL)
		case approveGone:
			// Someone settled the request in Telegram itself; do not claim the button did it.
			v.verificationTransport(bot).Notify(c, gid, says.AlreadyHandled.For(l), adminActionNoticeTTL)
		case approveConfirmed:
		}
	case "ban":
		gate := v.pendingGate(gid, target)
		says := v.adminSays(gate)
		p, ok := v.consume(gid, target)
		if !ok {
			_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(says.AlreadyHandled.For(l)))
			return nil
		}
		// Acknowledge the pending action; failures retain the request and evidence.
		duration := verificationBanDurationText(v.messages, l, v.verificationBanDuration(gid))
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(says.Banning.Render(l, duration)))
		if !v.executeBan(c, bot, gid, target, p) {
			v.verificationTransport(bot).Notify(c, gid, says.ActionFailed.For(l), adminActionNoticeTTL)
		}
	default:
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID))
	}
	return nil
}

// Required-channel lookup also renders a throttled operator alert when the gate is unavailable.
// isChannelMember answers the required-channel gate and records, on the applicant's pending,
// whether the answer could be read at all. An unreadable gate still refuses entry, but the
// applicant must not carry a strike for a door the bot could not see through.
func (v *Service) isChannelMember(c context.Context, bot modBot, gid, userID int64, groupLang i18n.Lang) bool {
	member, known := v.channelGate(c, bot, gid, userID, groupLang)
	v.markChannelReadable(gid, userID, known)
	return member
}

func (v *Service) channelGate(c context.Context, bot modBot, gid, userID int64, groupLang i18n.Lang) (member, known bool) {
	rc := v.RequiredChannelID(gid)
	if rc == 0 {
		return true, true
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
				// configurable: default fail-open (don't lock everyone out); strict deployments set required_channel_fail_open:false
				return open, false
			}
		}
		log.Printf("getChatMember(channel=%d user=%d): %v", rc, userID, err)
		return false, false
	}
	switch cm.MemberStatus() {
	case "creator", "administrator", "member":
		return true, true
	default:
		return cm.MemberIsMember(), true
	}
}

func (v *Service) channelWasUnreadable(gid, uid int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{gid, uid}]
	return ok && p.channelUnreadable
}

// markChannelReadable records the latest gate reading on the live pending. Clearing it on a
// readable answer matters as much as setting it: one transient failure must not exempt an
// applicant who genuinely never joined the channel.
func (v *Service) markChannelReadable(gid, uid int64, known bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if p, ok := v.pend[pkey{gid, uid}]; ok {
		p.channelUnreadable = !known
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

// consume claims a terminal action while keeping it recoverable until Telegram settlement returns.
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
	return p, true
}

// Nonce and timer epoch must both match, so superseded timers cannot claim a pending.
func (v *Service) claimPendingExpiry(gid, uid int64, nonce string, epoch uint64) (*pending, bool) {
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
	p.failedAt = v.wallNow()
	if p.timer != nil {
		p.timer.Stop()
	}
	v.markTerminalLocked(key, p)
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

func (v *Service) deleteChallenges(c context.Context, bot verifyBot, gid, uid int64, messages challengeMessages) {
	v.deleteChallenge(c, bot, gid, messages.groupMsgID)
	v.deleteChallenge(c, bot, uid, messages.privateMsgID)
}

// The alert destination is a live setting, so a panel change takes effect without a restart.
func (v *Service) adminLogChatID() int64 {
	return v.settings.Global().AdminLogChatID().Value
}

func (v *Service) adminAlert(c context.Context, bot verifyBot, text string) {
	v.verificationTransport(bot).Alert(c, v.adminLogChatID(), text)
}

// adminRecord logs an action that happened once, so identical repeats must all appear.
func (v *Service) adminRecord(c context.Context, bot verifyBot, text string) {
	v.verificationTransport(bot).AuditLog(c, v.adminLogChatID(), text)
}

// Failure notices fall back to the acting group when no admin-log chat is configured.
// This keeps optimistic callback acknowledgements from hiding rare network failures.
func (v *Service) failAlert(c context.Context, bot verifyBot, gid int64, text string) {
	v.verificationTransport(bot).FailAlert(c, v.adminLogChatID(), gid, text)
}

// Throttle unreadable-channel alerts per channel to avoid flooding operators.
const channelAccessAlertCooldown = 10 * time.Minute

func (v *Service) channelAccessAlert(c context.Context, bot verifyBot, l i18n.Lang, channelID int64) {
	v.mu.Lock()
	now := time.Now()
	if last, ok := v.chanAlert[channelID]; ok && now.Sub(last) < channelAccessAlertCooldown {
		v.mu.Unlock()
		return
	}
	cutoff := now.Add(-channelAccessAlertCooldown)
	for id, last := range v.chanAlert {
		if !last.After(cutoff) {
			delete(v.chanAlert, id)
		}
	}
	v.chanAlert[channelID] = now
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
	if v.shuttingDown {
		return nil, false
	}
	key := pkey{gid, uid}
	p, ok := v.pend[key]
	if !ok || p.done || p.nonce != nonce {
		return nil, false
	}
	p.done = true
	p.failedAt = v.wallNow()
	if p.timer != nil {
		p.timer.Stop()
	}
	v.markTerminalLocked(key, p)
	return p, true
}

// Claim before approval so its timeout cannot decline or strike concurrently.
// Callback handlers may acknowledge between claimPending and executeApprove.
func (v *Service) approve(c context.Context, bot modBot, gid, uid int64) bool {
	p, ok := v.claimPending(gid, uid)
	if !ok {
		return false
	}
	return v.executeApprove(c, bot, gid, uid, p) == approveConfirmed
}

// approveOutcome distinguishes a confirmed approval from a request that was settled
// elsewhere, so no caller can announce a result the bot did not actually produce.
type approveOutcome int

const (
	approveFailed    approveOutcome = iota // unconfirmed; the request is kept for a retry
	approveConfirmed                       // the applicant is in the group
	approveGone                            // the request is gone and where it went is unknown
)

// Failed approval reopens the claimed pending instead of stranding the applicant.
func (v *Service) executeApprove(c context.Context, bot modBot, gid, uid int64, p *pending) approveOutcome {
	if p.gate == gateMute {
		return v.executeRelease(c, bot, gid, uid, p)
	}
	if err := bot.ApproveChatJoinRequest(c, &telego.ApproveChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
		if !tg.JoinRequestGone(err) {
			log.Printf("approve %d in %d: %v", uid, gid, err)
			v.failAlert(c, bot, gid, v.adminSays(p.gate).ApproveFailed.Render(v.groupLanguage(gid), uid, gid, err))
			v.markPassing(gid, uid, p)
			v.stopRetrying(c, bot, gid, uid, p, "approve-retry", err)
			return approveFailed
		}
		// An administrator settled this request in Telegram's own interface. The same error
		// covers a manual approval and a manual rejection, so ask who is actually in the
		// group rather than announcing an outcome the bot did not produce.
		log.Printf("approve %d in %d: join request is already gone: %v", uid, gid, err)
		member, known := v.chatMemberState(c, bot, gid, uid)
		if known && member {
			v.notePassed(gid, uid) // before the terminal marker goes, as in the confirmed path
		}
		v.finishTerminal(gid, uid, p)
		v.deleteChallenges(c, bot, gid, uid, p.messages())
		if known && member {
			v.clearVerifyFails(gid, uid)
			v.recordDecision(true)
			v.save()
			return approveConfirmed
		}
		v.save()
		return approveGone
	}
	// Record the pass before the terminal marker is released. Approving produces a membership
	// update of its own, and it can be handled while the calls below are still in flight; until
	// the marker goes the terminal claim blocks a second verification, and from then on this
	// record does. Leaving a gap between them re-challenges the person just admitted.
	v.notePassed(gid, uid)
	v.mu.Lock()
	key := pkey{gid, uid}
	if cur, ok := v.pend[key]; ok && cur == p {
		delete(v.pend, key)
	}
	v.releaseTerminalLocked(key, p)
	v.mu.Unlock()
	v.clearVerifyFails(gid, uid) // verified successfully — reset any failure strikes
	v.deleteChallenges(c, bot, gid, uid, p.messages())
	v.recordDecision(true)
	v.save()
	log.Printf("approve user=%d group=%d", uid, gid)
	return approveConfirmed
}

// A settlement the bot cannot complete — it lost the rights to approve and decline — would
// otherwise retry every minute forever, DMing the applicant each round. Give up after this many
// attempts and leave the request for an administrator: Telegram still holds it, so nobody gets
// in without verification.
const maxSettleFailures = 10

// giveUpSettling reports a failure no retry can repair, so the attempt budget is spent at once.
func giveUpSettling(err error) bool { return tg.GroupUnreachable(err) }

// stopRetrying reopens a failed settlement for another attempt, unless the error proves no
// attempt can succeed — then the budget is spent immediately rather than a minute at a time.
func (v *Service) stopRetrying(c context.Context, bot modBot, gid, uid int64, p *pending, reason string, err error) {
	if giveUpSettling(err) {
		v.abandonSettlement(c, bot, gid, uid, p, reason, err)
		return
	}
	if !v.reopenPending(bot, gid, uid, p, reason) {
		v.releaseAbandonedHold(c, bot, gid, uid, p)
	}
}

// releaseAbandonedHold lifts a verification mute the bot has stopped trying to settle. Dropping
// the verification must not leave somebody silenced with nothing left to lift it; the restriction
// carries its own expiry, so a failure here only delays them.
func (v *Service) releaseAbandonedHold(c context.Context, bot modBot, gid, uid int64, p *pending) {
	if p.gate != gateMute || !p.held {
		return
	}
	if err := v.releaseMember(c, bot, gid, uid, p); err != nil {
		log.Printf("post-join verify: gave up on %d in %d and could not lift the hold: %v", uid, gid, err)
	}
}

// adoptAsHeld moves a verification that began outside the group onto the gate that can settle it
// now that the applicant is inside. Their progress is untouched: same question, same attempts,
// same deadline.
func (v *Service) adoptAsHeld(gid, uid int64, p *pending) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if cur, ok := v.pend[pkey{gid, uid}]; ok && cur == p && p.gate != gateMute {
		log.Printf("post-join verify: %d in %d entered the group mid-verification; settling as a member from here", uid, gid)
		p.gate = gateMute
	}
}

// abandonSettlement drops a pending the bot can never settle. The join request stays with
// Telegram for an administrator, so abandoning it admits nobody.
func (v *Service) abandonSettlement(c context.Context, bot modBot, gid, uid int64, p *pending, reason string, err error) {
	log.Printf("WARNING: cannot settle verification for %d in %d (%s): %v; "+
		"the join request stays with Telegram for an administrator", uid, gid, reason, err)
	if p.gate == gateMute && p.held {
		// Dropping the verification must not leave somebody silenced with nothing left to lift
		// it. The restriction carries its own expiry, so a failure here only delays them.
		if releaseErr := v.releaseMember(c, bot, gid, uid, p); releaseErr != nil {
			log.Printf("post-join verify: giving up on %d in %d but could not lift the hold: %v", uid, gid, releaseErr)
		}
	}
	v.discardPending(gid, uid, p)
	v.save()
}

// Reopen a bot-caused failed settlement unless the pending was replaced or consumed.
// Reports whether the retry was armed; false means the attempt limit is spent.
func (v *Service) reopenPending(bot modBot, gid, uid int64, p *pending, reason string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := pkey{gid, uid}
	defer v.releaseTerminalLocked(key, p)
	if cur, ok := v.pend[key]; !ok || cur != p || !p.done {
		return false
	}
	p.settleFailures++
	if p.settleFailures >= maxSettleFailures {
		log.Printf("WARNING: giving up on settling verification for %d in %d after %d attempts (%s); "+
			"the join request stays with Telegram for an administrator", uid, gid, p.settleFailures, reason)
		if p.timer != nil {
			p.timer.Stop()
		}
		delete(v.pend, key)
		return false
	}
	p.done = false
	// Keep the moment the applicant actually failed. Every caller here is retrying a settlement
	// for a failure that already happened, so stamping the strike with the retry time instead
	// would start their cooldown from however long the bot spent retrying.
	delay := p.deadline.Sub(v.wallNow())
	if delay < noFaultGrace {
		delay = noFaultGrace
		p.deadline = v.wallNow().Add(delay)
	}
	v.armExpiry(bot, p, gid, uid, delay, reason)
	return true
}

// Wrong-answer feedback distinguishes automatic ban from cooldown retry.
// outcomeVoice is the applicant-facing wording for one gate. Passing a join request and lifting a
// hold are different events, and saying "your join request was declined" to somebody who is
// standing in the group is simply false.
type outcomeVoice struct {
	Passed          i18n.Text
	AlreadyHandled  i18n.Text
	SettlePending   i18n.Text
	DeferralExpired i18n.Text
	Undelivered     i18n.Text
	WrongNoWait     i18n.Text
	WrongRetry      i18n.Format
	WrongBanned     i18n.Format
	TimeoutNoWait   i18n.Text
	TimeoutRetry    i18n.Format
	TimeoutBanned   i18n.Format
	AICaught        i18n.Format
	AICaughtNoWait  i18n.Text
}

// markPassing records that the applicant already earned admission, so a settlement retry
// completes the approval instead of falling into the generic timeout path, which declines.
func (v *Service) markPassing(gid, uid int64, p *pending) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if cur, ok := v.pend[pkey{gid, uid}]; ok && cur == p {
		p.passing = true
	}
}

// pendingHasNonce reports that the live verification is the one a button was posted for.
func (v *Service) pendingHasNonce(gid, uid int64, nonce string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{gid, uid}]
	return ok && !p.done && p.nonce == nonce
}

// pendingGate reads the gate of a live pending; a missing one reads as the join-request gate.
func (v *Service) pendingGate(gid, uid int64) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if p, ok := v.pend[pkey{gid, uid}]; ok {
		return p.gate
	}
	return gateRequest
}

// adminVoice is the operator-facing wording for one gate. Telling an administrator that a join
// request is still pending, when the person is standing in the group muted, sends them looking
// for a queue entry that does not exist.
type adminVoice struct {
	Approving           i18n.Text
	Banning             i18n.Format
	ApproveFailed       i18n.Format
	BanFailed           i18n.Format
	DeclineFailed       i18n.Format
	ActionFailed        i18n.Text
	CannotApprove       i18n.Text
	AlreadyHandled      i18n.Text
	ChallengePostFailed i18n.Format
	PendingCap          i18n.Format
}

func (v *Service) adminSays(gate string) adminVoice {
	a := &v.messages.Verification.Admin
	if gate == gateMute {
		return adminVoice{
			Approving: a.ApprovingHeld, Banning: a.BanningHeld, ApproveFailed: a.ApproveFailedHeld,
			BanFailed: a.BanFailedHeld, DeclineFailed: a.DeclineFailedHeld, ActionFailed: a.ActionFailedHeld,
			CannotApprove: a.CannotApproveHeld, AlreadyHandled: a.AlreadyHandledHeld,
			ChallengePostFailed: a.ChallengePostFailedHeld, PendingCap: a.PendingCapHeld,
		}
	}
	return adminVoice{
		Approving: a.Approving, Banning: a.Banning, ApproveFailed: a.ApproveFailed,
		BanFailed: a.BanFailed, DeclineFailed: a.DeclineFailed, ActionFailed: a.ActionFailed,
		CannotApprove: a.CannotApprove, AlreadyHandled: a.AlreadyHandled,
		ChallengePostFailed: a.ChallengePostFailed, PendingCap: a.PendingCap,
	}
}

func (v *Service) voice(gate string) outcomeVoice {
	if gate == gateMute {
		held := &v.messages.Verification.Held
		return outcomeVoice{
			Passed: held.Passed, AlreadyHandled: held.AlreadyHandled, SettlePending: held.SettlePending,
			DeferralExpired: held.DeferralExpired, Undelivered: held.Undelivered,
			WrongNoWait: held.WrongNoWait, WrongRetry: held.WrongRetry, WrongBanned: held.WrongBanned,
			TimeoutNoWait: held.TimeoutNoWait, TimeoutRetry: held.TimeoutRetry, TimeoutBanned: held.TimeoutBanned,
			AICaught: held.AICaught, AICaughtNoWait: held.AICaughtNoWait,
		}
	}
	result := &v.messages.Verification.Result
	return outcomeVoice{
		Passed: result.Approved, AlreadyHandled: result.AlreadyHandled, SettlePending: result.DeclinePending,
		DeferralExpired: result.DeferralExpired, Undelivered: result.Undelivered,
		WrongNoWait: result.WrongNoWait, WrongRetry: result.WrongRetry, WrongBanned: result.WrongBanned,
		TimeoutNoWait: result.TimeoutNoWait, TimeoutRetry: result.TimeoutRetry, TimeoutBanned: result.TimeoutBanned,
		AICaught: result.AICaught, AICaughtNoWait: result.AICaughtNoWait,
	}
}

func (v *Service) wrongAnswerText(groupID int64, l i18n.Lang, gate string, banned bool) string {
	voice := v.voice(gate)
	if banned {
		return v.bannedResultText(groupID, l, gate)
	}
	if seconds := v.verifyRetrySeconds(groupID); seconds > 0 {
		return voice.WrongRetry.Render(l, seconds)
	}
	return voice.WrongNoWait.For(l)
}

func (v *Service) agentCaughtText(groupID int64, l i18n.Lang, gate string, banned bool) string {
	voice := v.voice(gate)
	if banned {
		return v.bannedResultText(groupID, l, gate)
	}
	if seconds := v.verifyRetrySeconds(groupID); seconds > 0 {
		return voice.AICaught.Render(l, seconds)
	}
	return voice.AICaughtNoWait.For(l)
}

func (v *Service) bannedResultText(groupID int64, l i18n.Lang, gate string) string {
	duration := verificationBanDurationText(v.messages, l, v.verificationBanDuration(groupID))
	return v.voice(gate).WrongBanned.Render(l, duration)
}

func durationSecondsCeil(wait time.Duration) int {
	if wait <= 0 {
		return 0
	}
	return int((wait + time.Second - 1) / time.Second)
}

func (v *Service) timeoutResultText(groupID, userID int64, l i18n.Lang, gate string, banned bool) string {
	voice := v.voice(gate)
	if banned {
		duration := verificationBanDurationText(v.messages, l, v.verificationBanDuration(groupID))
		return voice.TimeoutBanned.Render(l, duration)
	}
	if seconds := durationSecondsCeil(v.verifyCooldownRemaining(groupID, userID)); seconds > 0 {
		return voice.TimeoutRetry.Render(l, seconds)
	}
	return voice.TimeoutNoWait.For(l)
}

// Bot-caused failures receive a meaningful strike-free retry window.
const noFaultGrace = 60 * time.Second

// Timeouts and wrong answers strike; delivery, settlement, restart, and recovery failures do not.
// declineResultText keeps every applicant-facing decline message inside what the bot knows:
// a definite result only when the request was actually settled, and a neutral "already handled"
// when it vanished and the applicant's fate could not be read.
func (v *Service) declineResultText(outcome declineOutcome, l i18n.Lang, gate string, settledText func() string) string {
	voice := v.voice(gate)
	switch outcome {
	case declineUnsettled:
		return voice.SettlePending.For(l)
	case declineGoneUnknown:
		return voice.AlreadyHandled.For(l)
	default:
		return settledText()
	}
}

// errRemovalLeftBanned names the state a failed unban leaves behind, for the operator alert.
var errRemovalLeftBanned = errors.New("removed but the ban could not be lifted; unban them manually")

// wrongAnswerReason marks a decline the applicant caused by failing the challenge.
const wrongAnswerReason = "wrong answer"

// A request that vanished before the bot could reject it still leaves the applicant out of the
// group, so a wrong answer still counts. A timeout does not: the request may have been rejected
// by an administrator long before the window ran out, and nobody should carry a strike for that.
func (v *Service) strikesFor(outcome declineOutcome, reason string) bool {
	if !strikesUser(reason) {
		return false
	}
	return outcome == declineConfirmed || reason == wrongAnswerReason
}

func strikesUser(reason string) bool {
	switch reason {
	case "approve-retry", "ban-retry", "decline-retry", "restart-lapsed", "recovered",
		"challenge-post-failed", deferredExpiryReason:
		return false
	default:
		return true
	}
}

// executeRelease settles a held member by lifting the verification mute. There is no join request
// to lose here, so the only outcomes are success and a failure worth retrying.
func (v *Service) executeRelease(c context.Context, bot modBot, gid, uid int64, p *pending) approveOutcome {
	// A basic group could never be held, so there is nothing to lift and nothing to retry.
	if p.held {
		if err := v.releaseMember(c, bot, gid, uid, p); err != nil {
			log.Printf("post-join verify: cannot lift the hold on %d in %d: %v", uid, gid, err)
			v.failAlert(c, bot, gid, v.adminSays(p.gate).ApproveFailed.Render(v.groupLanguage(gid), uid, gid, err))
			v.markPassing(gid, uid, p)
			v.stopRetrying(c, bot, gid, uid, p, "approve-retry", err)
			return approveFailed
		}
	}
	v.notePassed(gid, uid)
	v.finishTerminal(gid, uid, p)
	v.clearVerifyFails(gid, uid)
	v.deleteChallenges(c, bot, gid, uid, p.messages())
	v.recordDecision(true)
	v.save()
	log.Printf("post-join verify: released user=%d group=%d", uid, gid)
	return approveConfirmed
}

// declineOutcome tells a caller how much the bot actually knows, so no message states an
// outcome Telegram never confirmed.
type declineOutcome int

const (
	declineNoPending   declineOutcome = iota // nothing matched the claim
	declineUnsettled                         // a transient failure; the request is kept for a retry
	declineConfirmed                         // Telegram confirmed the rejection
	declineGoneAndOut                        // the request is gone and the applicant is not in the group
	declineGoneUnknown                       // the request is gone and where it went cannot be read
)

// Live wrong answers use nonce claims; timeout settlement uses epoch claims so outages may defer it.
func (v *Service) decline(c context.Context, bot modBot, gid, uid int64, nonce, reason string) (outcome declineOutcome, banned bool) {
	p, ok := v.claimPendingNonce(gid, uid, nonce)
	if !ok {
		return declineNoPending, false
	}
	return v.finishDecline(c, bot, gid, uid, p, reason)
}

// settled reports the outcomes that let a caller state a definite verification result.
func (o declineOutcome) settled() bool {
	return o == declineConfirmed || o == declineGoneAndOut
}

// Settle an already-claimed decline, striking at claim time only after Telegram confirms rejection.
func (v *Service) finishDecline(c context.Context, bot modBot, gid, uid int64, p *pending, reason string) (outcome declineOutcome, banned bool) {
	outcome = declineConfirmed
	leftBanned := false
	settle := func() error {
		if p.gate == gateMute {
			stranded, err := v.removeMember(c, bot, gid, uid)
			leftBanned = stranded
			return err
		}
		return bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid})
	}
	if err := settle(); err != nil {
		// A vanished join request has no counterpart when the applicant is already a member:
		// removal either worked or is worth retrying.
		if p.gate == gateMute || !tg.JoinRequestGone(err) {
			log.Printf("decline %d in %d failed: %v", uid, gid, err)
			v.failAlert(c, bot, gid, v.adminSays(p.gate).DeclineFailed.Render(v.groupLanguage(gid), uid, gid, err))
			v.stopRetrying(c, bot, gid, uid, p, "decline-retry", err)
			v.save()
			return declineUnsettled, false
		}
		// Telegram no longer holds the request, so no retry can settle it. Whether the applicant
		// ended up inside the group decides both what to tell them and whether the failure is
		// theirs to carry, and only their membership can answer that.
		log.Printf("decline %d in %d: join request is already gone: %v", uid, gid, err)
		member, known := v.chatMemberState(c, bot, gid, uid)
		switch {
		case known && member:
			// An administrator let them in. Nothing here is the applicant's fault.
			v.deleteChallenges(c, bot, gid, uid, p.messages())
			v.clearVerifyFails(gid, uid)
			v.finishTerminal(gid, uid, p)
			v.save()
			return declineGoneUnknown, false
		case !known:
			v.deleteChallenges(c, bot, gid, uid, p.messages())
			v.finishTerminal(gid, uid, p)
			v.save()
			return declineGoneUnknown, false
		}
		// They are out, exactly as this decline intended, so settle it like a confirmed one.
		outcome = declineGoneAndOut
	}
	v.deleteChallenges(c, bot, gid, uid, p.messages())
	var count int
	var doBan bool
	// A gate the bot could not read is the bot's problem; the applicant does not carry it.
	if v.strikesFor(outcome, reason) && !p.channelUnreadable {
		failedAt := p.failedAt
		if failedAt.IsZero() {
			failedAt = v.wallNow()
		}
		var recorded bool
		count, doBan, recorded = v.recordPendingVerifyFail(gid, uid, p, failedAt)
		if recorded {
			v.recordDecision(false)
		}
	}
	v.finishTerminal(gid, uid, p)
	if doBan {
		secs := v.verificationBanDuration(gid)
		if err := v.applyVerificationBan(c, bot, gid, uid, secs, false); err != nil {
			log.Printf("verify auto-ban %d in %d: %v", uid, gid, err)
			admin := &v.messages.Verification.Admin
			v.failAlert(c, bot, gid, admin.AutoBanFailed.Render(v.groupLanguage(gid), uid, gid, count, err))
		} else {
			l := v.groupLanguage(gid)
			duration := verificationBanDurationText(v.messages, l, secs)
			v.adminRecord(c, bot, v.messages.Verification.Admin.AutoBanned.Render(l, uid, gid, count, duration))
			banned = true
		}
		if banned {
			v.clearVerifyFails(gid, uid)
		}
	}
	if leftBanned {
		// The removal could not be undone, so they really are banned. Say that instead of
		// inviting them back, and tell operators, because only they can lift it.
		banned = true
		admin := &v.messages.Verification.Admin
		v.failAlert(c, bot, gid, admin.BanFailed.Render(v.groupLanguage(gid), uid, gid, errRemovalLeftBanned))
	}
	v.save()
	log.Printf("decline user=%d group=%d (%s) fails=%d banned=%v", uid, gid, reason, count, banned)
	return outcome, banned
}

func (v *Service) finishTerminal(gid, uid int64, p *pending) {
	v.mu.Lock()
	key := pkey{gid, uid}
	if cur, ok := v.pend[key]; ok && cur == p {
		delete(v.pend, key)
	}
	v.releaseTerminalLocked(key, p)
	v.mu.Unlock()
}

// banApplicant preserves the request for retry when either required Telegram action is unconfirmed.
func (v *Service) banApplicant(c context.Context, bot modBot, gid, uid int64) (handled, banned bool) {
	p, ok := v.consume(gid, uid)
	if !ok {
		return false, false
	}
	return true, v.executeBan(c, bot, gid, uid, p)
}

// executeBan confirms the ban before declining so an unconfirmed ban retains the request and its evidence.
func (v *Service) executeBan(c context.Context, bot modBot, gid, uid int64, p *pending) bool {
	if err := v.applyVerificationBan(c, bot, gid, uid, v.verificationBanDuration(gid), true); err != nil {
		log.Printf("banApplicant %d in %d: %v", uid, gid, err)
		v.failAlert(c, bot, gid, v.adminSays(p.gate).BanFailed.Render(v.groupLanguage(gid), uid, gid, err))
		v.stopRetrying(c, bot, gid, uid, p, "ban-retry", err)
		v.save()
		return false
	}
	if p.gate == gateMute {
		// The ban already removed them; there is no join request left to decline.
		v.deleteChallenges(c, bot, gid, uid, p.messages())
		v.recordDecision(false)
		v.finishTerminal(gid, uid, p)
		v.save()
		log.Printf("banApplicant user=%d group=%d banned=true (admin report, held member)", uid, gid)
		return true
	}
	if err := bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
		if !tg.JoinRequestGone(err) {
			log.Printf("decline after ban %d in %d: %v", uid, gid, err)
			v.failAlert(c, bot, gid, v.adminSays(p.gate).DeclineFailed.Render(v.groupLanguage(gid), uid, gid, err))
			v.stopRetrying(c, bot, gid, uid, p, "ban-retry", err)
			v.save()
			return false
		}
		// The ban is confirmed and the request is gone; retrying the decline cannot help.
		log.Printf("decline after ban %d in %d: join request is already gone: %v", uid, gid, err)
	}
	v.deleteChallenges(c, bot, gid, uid, p.messages())
	v.recordDecision(false)
	v.finishTerminal(gid, uid, p)
	v.save()
	log.Printf("banApplicant user=%d group=%d banned=true (admin report)", uid, gid)
	return true
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
