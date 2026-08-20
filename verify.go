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

// modBot is the wider slice of the telego.Bot API the admin-gate + moderation paths use — a superset
// of verifyBot. Threading it instead of a concrete *telego.Bot lets the admin gate (adminStatus /
// isGroupAdmin), the mute/unmute helpers, and the warn-limit kick be unit-tested with a fake; so the
// security-critical "who is allowed / does the deny path act" branches get regression coverage.
// *telego.Bot satisfies it, so callers are unchanged — pure compile-checked type-widening.
type modBot interface {
	verifyBot
	GetChatMember(ctx context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error)
	AnswerCallbackQuery(ctx context.Context, params *telego.AnswerCallbackQueryParams) error
	RestrictChatMember(ctx context.Context, params *telego.RestrictChatMemberParams) error
	UnbanChatMember(ctx context.Context, params *telego.UnbanChatMemberParams) error
	GetChat(ctx context.Context, params *telego.GetChatParams) (*telego.ChatFullInfo, error)
}

type pending struct {
	groupMsgID    int
	mode          string // challenge type this applicant got: modeKernel (typed answer) or modeQuiz (buttons)
	lang          lang   // applicant's locale, from their Telegram language_code; every message they see uses it
	qText         string
	qOpts         []string
	correctIdx    int
	tries         int      // kernel mode: replies used so far (kernelMaxTries before the decline)
	hinted        bool     // kernel mode: the "no Linux installed yet" fallback was already offered (deliberately not persisted — a restart may re-offer it, which costs nothing)
	prompted      bool     // kernel mode: the question has actually been DM'd, so a reply can be graded as an answer
	sampleBounced bool     // kernel mode: the "you sent back our own example" nudge was already spent
	fbAnswers     []string // kernel mode: once the short-answer fallback replaced the kernel question, the answers it is graded against
	nonce         string   // per-pending token; a quiz button only counts if its nonce matches
	name          string   // applicant display name, kept so a post-outage re-notify can address them
	deadline      time.Time
	timer         *time.Timer
	epoch         uint64    // bumped on every (re-)arm; a timer callback carries the epoch it was armed with and no-ops if it no longer matches, so a re-arm (defer / recovery) can't be acted on by the timer it replaced
	lastRenotify  time.Time // last post-outage re-notify, so repeated recoveries don't re-message the same applicant every cycle
	done          bool
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
	Tries      int      `json:"tries,omitempty"`
	QText      string   `json:"q_text"`
	QOpts      []string `json:"q_opts"`
	CorrectIdx int      `json:"correct_idx"`
	Nonce      string   `json:"nonce"`
	Name       string   `json:"name,omitempty"`
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
	shuttingDown bool   // set at graceful shutdown; consumeNonce refuses so a firing timeout timer can't decline/strike/ban a mid-verification user (guarded by mu)
	rich         bool   // runtime toggle for rich-message output (init from cfg.RichMessages, flipped by /rich)
	nameSpoiler  bool   // hide a joiner's display name behind a Telegram spoiler in the in-group challenge (anti-advert; /spoiler, persisted)
	vmode        string // runtime verification-mode override (/vmode, persisted); "" => follow the config
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
	agentMu      sync.Mutex            // guards agents; separate from mu so the tally's file write never blocks the verification hot paths
	agents       agentTally            // tripped automated agents, counted per claimed model
	agentPath    string                // persistence path for the automated-agent tally
	settingsPath string                // persistence path for runtime settings (verification enabled state)
	adminMu      sync.Mutex            // guards adminCache
	adminCache   map[pkey]time.Time    // group+user -> admin-status cache expiry; only ADMINS are cached (short TTL) so the verify/moderation admin checks skip a GetChatMember round-trip on repeat use
	lastOnline   time.Time             // last time a heartbeat confirmed the bot can reach Telegram (guarded by mu); seeded to start time so we begin "online"
	hbPath       string                // persistence path for the online heartbeat, so a restart can estimate how long the bot was down
	probe        liveProbe             // liveness prober (the bot) for reachable(); nil in tests => assume reachable
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
		lastOnline:  time.Now(), // begin online; the heartbeat only flips us offline after missed contact
		nameSpoiler: true}       // default ON: spam joiners often set their NAME to an advert; hide it behind a spoiler
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

// loadJSONFile reads path and JSON-unmarshals it into dst. A MISSING file is not an error (first
// run — the caller keeps its seeded/empty default). On a CORRUPT file (readable but invalid JSON) it
// renames the file to path+".corrupt" before returning the error, so the next save can't silently
// overwrite the original — the same hardening loadFeedState has, now shared by every state loader.
func loadJSONFile(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(data, dst); err != nil {
		log.Printf("state load %s: %v — backing up to %s.corrupt and starting fresh", path, err, path)
		if rerr := os.Rename(path, path+".corrupt"); rerr != nil {
			log.Printf("state load: could not back up corrupt %s: %v", path, rerr)
		}
		return err
	}
	return nil
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
			Mode: p.mode, Lang: string(p.lang), FbAnswers: p.fbAnswers, Prompted: p.prompted, Tries: p.tries, QText: p.qText, QOpts: p.qOpts, CorrectIdx: p.correctIdx,
			Nonce: p.nonce, Name: p.name, Deadline: p.deadline.Unix()})
	}
	v.mu.Unlock()
	writeJSONFile(v.statePath, recs)
}

func (v *Verifier) load(bot verifyBot) {
	if v.statePath == "" {
		return
	}
	var recs []pendingRec
	if err := loadJSONFile(v.statePath, &recs); err != nil {
		return // corrupt file backed up to .corrupt; start empty
	}
	// Estimate how long the bot was down from the last persisted heartbeat. A long gap means every
	// in-progress window was eaten while we were away, so those pendings get a fresh full window and a
	// re-notify; a quick restart keeps the existing (short) behaviour so a routine deploy is quiet.
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
		// Don't restore a pending for a group no longer configured (it would decline/strike against an
		// unguarded chat), nor one whose question payload is out of range (an unwinnable quiz).
		if !v.cfg.IsGroup(gid) {
			log.Printf("state load: skip pending for unconfigured group %d (user %d)", gid, uid)
			continue
		}
		mode := r.Mode
		if mode == "" {
			mode = modeQuiz // a record written before kernel mode existed always held a quiz
		}
		// A quiz pending needs a usable option payload (a restored out-of-range answer index would be
		// unwinnable); a kernel pending carries no options at all, so it is exempt from that check.
		if mode == modeQuiz && (len(r.QOpts) < 2 || r.CorrectIdx < 0 || r.CorrectIdx >= len(r.QOpts)) {
			log.Printf("state load: skip pending with invalid question payload (group %d user %d)", gid, uid)
			continue
		}
		p := &pending{groupMsgID: r.GroupMsgID, mode: mode, lang: lang(r.Lang), fbAnswers: r.FbAnswers, prompted: r.Prompted, tries: r.Tries, qText: r.QText, qOpts: r.QOpts,
			correctIdx: r.CorrectIdx, nonce: r.Nonce, name: r.Name, deadline: time.Unix(r.Deadline, 0)}
		delay := time.Until(p.deadline)
		reason := "timeout"
		switch {
		case longOutage:
			// Down long enough to eat the whole window: a fresh full one, don't strike if it lapses
			// (the user never had a fair shot), and re-notify below.
			delay = v.timeout()
			p.deadline = time.Now().Add(delay)
			p.lastRenotify = time.Now() // mark re-notified so a runtime recovery right after doesn't re-message
			reason = "recovered"
			refresh = append(refresh, renotifyItem{gid, uid, r.Name, r.GroupMsgID, p})
		case delay <= 0:
			// Deadline lapsed during a SHORT restart — a fresh grace window, no strike.
			delay = noFaultGrace
			p.deadline = time.Now().Add(delay)
			reason = "restart-lapsed"
		case delay < time.Second:
			delay = time.Second
		}
		v.mu.Lock()
		v.pend[pkey{gid, uid}] = p
		// arm the timer with the entry already in the map (mirrors onJoinRequest), so a
		// near-immediate fire can't decline()->consume() before the entry exists. The captured
		// nonce makes the decline a no-op if a fresh request has since replaced this pending.
		v.armExpiry(bot, p, gid, uid, delay, reason)
		v.mu.Unlock()
	}
	if len(recs) > 0 {
		log.Printf("restored %d pending verification(s)", len(recs))
	}
	// After a real outage, proactively refresh the restored applicants (bounded) — a DM plus a fresh
	// in-group challenge — instead of leaving them staring at a stale, already-expired one.
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
	// before the DM auto-reply: a plain private message from someone mid-kernel-verification IS
	// their answer (routes are first-match, so this must be registered first)
	bh.Handle(v.onKernelAnswer, v.kernelAnswerDM)
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
	bh.Handle(v.onVMode, th.CommandEqual("vmode"))
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

// isChatMember reports whether uid is currently in chatID (creator/admin/member, or restricted but
// still a member). Returns false on ANY lookup error and for left/kicked/banned, so an exempt check
// fails SAFE — an unconfirmable membership just falls through to the normal verification.
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

// tryTrustedBypass tries the trusted-member fast path for a join request: if the applicant is already
// a member of one of group gid's configured trusted groups (config: trusted_member_group_ids), it
// approves WITHOUT a quiz. It reports two things: handled (the request was approved — caller should
// stop) and trusted (the applicant was CONFIRMED to be in a trusted group). Fails SAFE — a
// membership-lookup error or a non-member yields trusted=false (so the caller runs the ordinary flow,
// including the failure cooldown). A confirmed member whose auto-approve FAILS yields handled=false,
// trusted=true (logged + admin-alerted): the caller runs the NORMAL verification but, because the
// member is trusted, must NOT decline it under the cooldown. On success it clears prior failed-verify
// strikes and records the approval — creating no pending and posting no quiz.
func (v *Verifier) tryTrustedBypass(c context.Context, bot modBot, gid, uid int64) (handled, trusted bool) {
	for _, src := range v.cfg.trustedGroups(gid) {
		if src == 0 || src == gid {
			continue // ignore a blank or self-referential entry
		}
		if !v.isChatMember(c, bot, src, uid) { // fail-closed: error / non-member / unreadable => not trusted
			continue
		}
		// Confirmed member of a trusted group — this takes PRIORITY over the failure cooldown.
		if err := bot.ApproveChatJoinRequest(c, &telego.ApproveChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
			log.Printf("trusted-bypass: approve %d in %d failed (%v) — falling back to normal verification", uid, gid, err)
			v.adminAlert(c, bot, fmt.Sprintf("⚠️ 用户 %d 是可信群 %d 成员,在群 %d 自动免验证放行失败(%v);将走正常验证流程", uid, src, gid, err))
			return false, true
		}
		v.clearVerifyFails(gid, uid) // a now-trusted member starts with a clean slate
		v.recordDecision(true)
		log.Printf("verify: trusted-bypass auto-approved %d in %d (already a member of trusted group %d)", uid, gid, src)
		return true, true
	}
	return false, false
}

// joinGate runs the pre-challenge handling for a join request and reports whether it was fully handled
// (the caller should stop). The trusted-member fast path takes PRIORITY over the failure cooldown: a
// confirmed trusted member is approved (handled), and even if its auto-approve fails it proceeds to the
// normal verification WITHOUT being declined by the cooldown. Only a non-member / unconfirmable
// applicant is subject to the ordinary failed-applicant cooldown.
func (v *Verifier) joinGate(c context.Context, bot modBot, gid, uid int64) (done bool) {
	handled, trusted := v.tryTrustedBypass(c, bot, gid, uid)
	if handled {
		return true
	}
	if trusted {
		return false // confirmed trusted member, approve failed -> normal verification, skip the cooldown
	}
	// Anti-spam cooldown: a recently-failed applicant must wait out cfg.VerifyRetrySeconds before
	// re-applying. Decline an early re-try silently rather than reposting a challenge.
	if wait := v.verifyCooldownRemaining(gid, uid); wait > 0 {
		if err := bot.DeclineChatJoinRequest(c, &telego.DeclineChatJoinRequestParams{ChatID: tu.ID(gid), UserID: uid}); err != nil {
			log.Printf("verify cooldown: decline %d in %d failed: %v", uid, gid, err)
		}
		log.Printf("verify cooldown: declined early re-apply from %d in %d (%ds left)", uid, gid, int(wait.Seconds())+1)
		return true
	}
	return false
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
	// Pre-challenge gate: the trusted-member fast path (which takes priority over the failure
	// cooldown), then the anti-spam cooldown for everyone else. Returns true if fully handled.
	if v.joinGate(c, bot, gid, uid) {
		return nil
	}
	// Everything this applicant reads is rendered in their own Telegram interface language.
	ul := langFor(jr.From.LanguageCode)
	mode, text, opts, correctIdx := v.newChallenge(gid, ul)
	name := displayName(&jr.From)
	groupMsgID := v.postGroupChallenge(c, bot, gid, uid, name, ul)

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
	p := &pending{groupMsgID: groupMsgID, mode: mode, lang: ul, qText: text, qOpts: opts, correctIdx: correctIdx,
		nonce: newNonce(), name: name, deadline: time.Now().Add(v.timeout())}
	v.armExpiry(bot, p, gid, uid, v.timeout(), "timeout")
	v.pend[key] = p
	v.mu.Unlock()
	if oldMsgID != 0 && oldMsgID != groupMsgID {
		v.deleteChallenge(c, bot, gid, oldMsgID) // drop the stale challenge from a previous request
	}
	v.save()
	log.Printf("join %d (@%s) in group %d: pending (%s challenge), in-group verify link posted", uid, jr.From.Username, gid, mode)
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
func (v *Verifier) firstPending(uid int64) (gid int64, ul lang, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, p := range v.pend {
		if k.uid == uid && !p.done {
			return k.gid, p.lang, true
		}
	}
	return 0, langZH, false
}

// sendDMChallenge runs when the applicant opens the bot via the deep link.
// Two-step: if a channel is required and not yet joined, ask them to follow it
// first (with a "I've followed, continue" button); otherwise send the quiz.
func (v *Verifier) sendDMChallenge(c context.Context, bot *telego.Bot, uid int64) {
	gid, ul, ok := v.firstPending(uid)
	if !ok {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), tr(ul).NoPending))
		return
	}
	t := tr(ul)
	// The follow-prompt uses the first pending group's channel (groups usually share one);
	// the per-group channel is still enforced at answer time in onAnswer.
	if v.cfg.requiredChannel(gid) != 0 && !v.isChannelMember(c, bot, gid, uid) {
		var rows [][]telego.InlineKeyboardButton
		if curl := v.channelURL(gid); curl != "" {
			rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: fmt.Sprintf(t.FollowButton, v.cfg.channelDisplay(gid)), URL: curl}))
		}
		rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: t.ContinueButton,
			CallbackData: recheckPrefix + strconv.FormatInt(gid, 10) + ":" + strconv.FormatInt(uid, 10)}))
		_, _ = bot.SendMessage(c, htmlMessage(uid,
			fmt.Sprintf(t.FollowPrompt, v.channelLinkHTML(gid, ul))).
			WithReplyMarkup(tu.InlineKeyboard(rows...)))
		return
	}
	v.sendQuizzes(c, bot, uid)
}

// sendQuizzes DMs the challenge for every group where this user has a live verification: the
// button quiz, or — in kernel mode — the typed-answer prompt (no buttons; their next DM is the
// answer, handled by onKernelAnswer).
func (v *Verifier) sendQuizzes(c context.Context, bot *telego.Bot, uid int64) {
	type dmq struct {
		gid      int64
		mode     string
		lang     lang
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
		if dq.mode == modeKernel {
			left := kernelMaxTries - dq.tries
			render := kernelPromptHTML
			if dq.fallback { // already moved to the short-answer question — re-send THAT, not the kernel one
				render = fallbackPromptHTML
			}
			v.sendVerifyDM(c, bot, uid,
				render(dq.lang, dq.text, left, dq.nonce, true),  // collapsed tripwire (Bot API 7.4)
				render(dq.lang, dq.text, left, dq.nonce, false)) // …without the blockquote, for an old API server
			v.markPrompted(dq.gid, uid) // only now may a DM be graded as their answer
			continue
		}
		gidStr, uidStr := strconv.FormatInt(dq.gid, 10), strconv.FormatInt(uid, 10)
		rows := make([][]telego.InlineKeyboardButton, 0, len(dq.opts))
		for i, opt := range dq.opts {
			rows = append(rows, tu.InlineKeyboardRow(
				telego.InlineKeyboardButton{Text: opt, CallbackData: fmt.Sprintf("%s%s:%s:%s:%d", answerPrefix, gidStr, uidStr, dq.nonce, i)}))
		}
		_, _ = bot.SendMessage(c, htmlMessage(uid,
			fmt.Sprintf(tr(dq.lang).QuizPrompt, html.EscapeString(dq.text))).
			WithReplyMarkup(tu.InlineKeyboard(rows...)))
	}
}

// markPrompted records that the question has actually been delivered to this applicant, so their
// next plain DM may be graded as an answer. Until then a stray message ("hi", "已关注") must NOT
// cost an attempt — the applicant may not have seen a question at all yet.
func (v *Verifier) markPrompted(gid, uid int64) {
	v.mu.Lock()
	if p, ok := v.pend[pkey{gid, uid}]; ok && !p.done {
		p.prompted = true
	}
	v.mu.Unlock()
}

// sendVerifyDM delivers a verification DM, degrading instead of failing: the rich rendering first,
// then a simpler HTML one if Telegram rejected the markup (an old self-hosted Bot API server, an
// entity it doesn't know), then plain text with the tags stripped. An applicant must never be left
// with no question — and therefore auto-declined at timeout — because of markup, so unlike the other
// best-effort DMs this path inspects the send error instead of discarding it.
func (v *Verifier) sendVerifyDM(c context.Context, bot verifyBot, uid int64, rich, simpler string) {
	_, err := bot.SendMessage(c, htmlMessage(uid, rich))
	if err == nil {
		return
	}
	if !markupRejected(err) {
		// A transient failure, not bad markup: re-sending could deliver the question twice if the
		// first one actually landed. The applicant can re-open the link, and the timeout defers
		// while the bot is unreachable.
		log.Printf("verify DM to %d failed (%v)", uid, err)
		return
	}
	log.Printf("verify DM to %d rejected (%v) — retrying without the collapsed quote", uid, err)
	if simpler != "" && simpler != rich {
		if _, err := bot.SendMessage(c, htmlMessage(uid, simpler)); err == nil {
			return
		}
	}
	if _, err := bot.SendMessage(c, tu.Message(tu.ID(uid), stripHTML(simpler))); err != nil {
		log.Printf("verify DM to %d failed even as plain text: %v", uid, err)
	}
}

// markupRejected reports whether Telegram refused the message because of its HTML — the case worth
// re-rendering. Matched on the Bot API's wording ("Bad Request: can't parse entities…"), because the
// error arrives as an opaque string.
func markupRejected(err error) bool {
	if err == nil {
		return false
	}
	e := strings.ToLower(err.Error())
	return strings.Contains(e, "parse") || strings.Contains(e, "entit") || strings.Contains(e, "bad request")
}

// stripHTML renders our own outgoing HTML as plain text: drop the tags, unescape the entities. Only
// ever applied to messages this bot built, so a simple scanner is enough.
func stripHTML(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	out := b.String()
	for _, p := range [][2]string{{"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", "\""}, {"&#39;", "'"}, {"&amp;", "&"}} {
		out = strings.ReplaceAll(out, p[0], p[1])
	}
	return out
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
	t := tr(langFor(cq.From.LanguageCode))
	if cq.From.ID != uid {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(t.NotYours).WithShowAlert())
		return nil
	}
	if !v.hasPending(uid) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(t.AlreadyHandled))
		return nil
	}
	if v.cfg.requiredChannel(gid) != 0 && !v.isChannelMember(c, bot, gid, uid) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).
			WithText(fmt.Sprintf(t.NotFollowedYet, v.cfg.channelDisplay(gid))).WithShowAlert())
		return nil
	}
	// ACK first so the button stops spinning, THEN send the quiz DM(s) — sendQuizzes swallows send
	// errors, so the early ack loses no feedback (the channel-membership check above stays before
	// the ack because its toast is result-driven).
	_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(t.ContinueOK))
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
	t := tr(langFor(cq.From.LanguageCode))
	if cq.From.ID != owner {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(t.NotYours).WithShowAlert())
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
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(t.AlreadyHandled))
		return nil
	}
	if nonce != curNonce {
		// A stale button from a previous (overwritten) request — don't let it answer this quiz.
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(t.StaleQuestion).WithShowAlert())
		return nil
	}

	if choice != correctIdx {
		_, banned := v.decline(c, bot, gid, owner, nonce, "wrong answer")
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(v.wrongAnswerText(langFor(cq.From.LanguageCode), banned)).WithShowAlert())
		return nil
	}
	if !v.isChannelMember(c, bot, gid, owner) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).
			WithText(fmt.Sprintf(t.NotFollowedYet, v.cfg.channelDisplay(gid))).WithShowAlert())
		return nil
	}
	if v.approve(c, bot, gid, owner) {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(t.Approved))
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(owner), t.Approved))
	} else {
		_ = bot.AnswerCallbackQuery(c, tu.CallbackQuery(cq.ID).WithText(t.AlreadyHandled).WithShowAlert())
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

// isChannelMember reports whether userID satisfies group gid's required-channel gate. Takes the
// modBot slice rather than *telego.Bot so the gate — including its fail-open branch — is unit-testable
// with a fake; *telego.Bot satisfies it, so callers are unchanged.
func (v *Verifier) isChannelMember(c context.Context, bot modBot, gid, userID int64) bool {
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
func (v *Verifier) channelLinkHTML(gid int64, ul lang) string {
	d := v.cfg.channelDisplay(gid)
	if d == "" {
		d = tr(ul).ChannelFallbackName // an unnamed channel still has to read naturally in the applicant's language
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
// wrong-answer path so a STALE callback from a since-replaced request can't decline/strike/ban a
// freshly re-issued pending under the same (gid,uid) key.
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
	delete(v.pend, key)
	return p, true
}

// consumeExpiry is the timer path's claim: it takes the pending only if BOTH the nonce and the epoch
// still match. The epoch is bumped on every (re-)arm, so a timer that fired just before a defer /
// recovery re-armed the pending finds a stale epoch and no-ops here — closing the race where a
// pre-recovery timeout could decline (and strike) the very applicant recovery just refreshed.
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
	delete(v.pend, key)
	return p, true
}

// stopForShutdown freezes verification for a clean exit: it flags shutting-down (so a timeout timer
// that fires during the exit window no-ops in consumeNonce instead of declining/striking/banning a
// user who is still mid-verification) and stops every pending timer. Call it before the final save()
// so in-progress verifications persist intact across the restart (the README's documented guarantee).
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
func (v *Verifier) channelAccessAlert(c context.Context, bot verifyBot, channelID int64) {
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
	if delay < noFaultGrace {
		delay = noFaultGrace // OUR approve failed — give the user a real retry window, not ~1s
	}
	// "approve-retry": if this timer fires it must NOT strike the user — the original failure was ours.
	v.armExpiry(bot, p, gid, uid, delay, "approve-retry")
}

// wrongAnswerText is the callback alert shown after a wrong answer: a ban notice if this
// failure triggered the auto-ban, otherwise a decline + retry-after-cooldown hint.
func (v *Verifier) wrongAnswerText(l lang, banned bool) string {
	t := tr(l)
	if banned {
		return t.WrongBanned
	}
	if s := v.cfg.VerifyRetrySeconds; s > 0 {
		return fmt.Sprintf(t.WrongRetry, s)
	}
	return t.WrongNoWait
}

// noFaultGrace is the retry window granted when a decline would NOT be the user's fault — after the
// bot's own approve call failed, or when a restored pending's deadline lapsed while the bot was down
// — so a returning/retrying legitimate user gets a real chance instead of being declined within ~1s.
const noFaultGrace = 60 * time.Second

// strikesUser reports whether a decline with this reason counts a verification strike against the
// applicant. A genuine timeout or wrong answer does; a decline that isn't the user's fault does NOT,
// so it can never push a legitimate user toward the auto-ban: OUR OWN failed approve ("approve-retry"),
// a deadline that lapsed while the bot was DOWN ("restart-lapsed"), or the first fresh window granted
// right after the bot recovered from an outage ("recovered").
func strikesUser(reason string) bool {
	switch reason {
	case "approve-retry", "restart-lapsed", "recovered":
		return false
	default:
		return true
	}
}

// decline rejects a failed verification from the wrong-answer path — a live user action, so no offline
// check is needed. nonce identifies the exact pending, so a stale callback can't decline a
// since-replaced one (see consumeNonce). Returns handled=false if there was no matching live pending,
// banned=true if this crossed the auto-ban. The timeout path goes through onExpiry -> consumeExpiry ->
// finishDecline instead, so it can defer while the bot is offline.
func (v *Verifier) decline(c context.Context, bot verifyBot, gid, uid int64, nonce, reason string) (handled, banned bool) {
	p, ok := v.consumeNonce(gid, uid, nonce)
	if !ok {
		return false, false
	}
	return true, v.finishDecline(c, bot, gid, uid, p, reason)
}

// finishDecline performs the reject on an ALREADY-claimed pending: drop the challenge, record a strike
// (unless strikesUser(reason) is false — a decline that isn't the user's fault), decline the join
// request, and auto-ban once the applicant reaches cfg.VerifyMaxFails strikes. Returns whether the ban
// itself succeeded. Shared by the wrong-answer path (decline) and the timeout path (onExpiry).
func (v *Verifier) finishDecline(c context.Context, bot verifyBot, gid, uid int64, p *pending, reason string) (banned bool) {
	v.deleteChallenge(c, bot, gid, p.groupMsgID)
	var count int
	var doBan bool
	if strikesUser(reason) { // a decline from OUR OWN failed approve / a restart-lapsed deadline isn't the user's fault — don't strike them
		v.recordDecision(false)
		count, doBan = v.recordVerifyFail(gid, uid)
	}
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
	return banned
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

// --- outage resilience: heartbeat, offline-aware expiry, and post-recovery re-notify ---

const (
	heartbeatInterval     = 25 * time.Second // how often the bot pings Telegram to confirm it is reachable
	heartbeatProbeTimeout = 10 * time.Second // per-probe timeout for the GetMe liveness call
	offlineThreshold      = 70 * time.Second // no successful contact within this => treat as offline (defer expiries)
	outageRecovery        = 90 * time.Second // an outage longer than this triggers fresh windows + a re-notify on recovery
	renotifyCap           = 30               // most applicants to re-notify per recovery, so a big backlog can't become a message storm
)

// timeout is the configured verification window as a Duration.
func (v *Verifier) timeout() time.Duration { return time.Duration(v.cfg.TimeoutSeconds) * time.Second }

// liveProbe is the slice of the bot used for a liveness check (a cheap GetMe). *telego.Bot satisfies
// it; a fake satisfies it in tests.
type liveProbe interface {
	GetMe(ctx context.Context) (*telego.User, error)
}

// offlineNow reports whether the bot currently cannot reach Telegram: the heartbeat has had no
// successful contact within offlineThreshold. Seeded online at startup, so before the first heartbeat
// it reads online and normal declines proceed.
func (v *Verifier) offlineNow() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return !v.lastOnline.IsZero() && time.Since(v.lastOnline) > offlineThreshold
}

// reachable does an on-demand liveness probe, so an expiry that fires in the detection-lag window at
// the very START of an outage — before offlineNow flips true (the heartbeat only samples every
// heartbeatInterval) — still isn't acted on if the bot really can't reach Telegram. No probe wired
// (tests) => assume reachable, so the ordinary decline path is unchanged.
func (v *Verifier) reachable(c context.Context) bool {
	if v.probe == nil {
		return true
	}
	pc, cancel := context.WithTimeout(c, heartbeatProbeTimeout)
	defer cancel()
	_, err := v.probe.GetMe(pc)
	return err == nil
}

// armExpiry arms p's expiry timer to fire after delay. It BUMPS p.epoch and captures that epoch in the
// callback, so a timer replaced by a later re-arm (defer / recovery) no-ops in consumeExpiry rather
// than acting on the pending it was superseded on. The callback routes through onExpiry, which DEFERS
// the expiry (re-arming a fresh window) whenever the bot can't reach Telegram, so an outage can't
// decline or strike a user we simply couldn't hear from. Callers must hold v.mu.
func (v *Verifier) armExpiry(bot verifyBot, p *pending, gid, uid int64, delay time.Duration, reason string) {
	p.epoch++
	epoch := p.epoch
	nonce := p.nonce
	p.timer = time.AfterFunc(delay, func() { v.onExpiry(context.Background(), bot, gid, uid, nonce, epoch, reason) })
}

// onExpiry is the pending-timer callback. If the bot is offline (or a quick probe can't reach Telegram)
// the expiry is not trusted — the user may have answered without us receiving it, and we couldn't
// deliver a decline anyway — so it is DEFERRED for a fresh window rather than declining/striking. When
// online it declines. The epoch guard (consumeExpiry) makes a superseded timer a no-op.
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

// deferExpiry re-arms an expiry for a fresh full window instead of acting on it, used when the bot is
// offline at expiry. The pending is kept intact (no consume, no strike) and keeps its original reason,
// so once the bot is back and the user still hasn't finished within a fresh window it is handled
// normally. No-op if the pending was replaced/handled/superseded meanwhile (nonce + epoch guard).
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
	p.deadline = time.Now().Add(v.timeout())
	v.armExpiry(bot, p, gid, uid, v.timeout(), reason)
	log.Printf("verify: bot offline — deferred %s for %d in %d, re-armed a fresh window", reason, uid, gid)
}

// postGroupChallenge posts the in-group verification challenge for an applicant — the "click to
// verify" deep link, the admin pass/ban buttons, and an optional channel-follow hint — and returns the
// sent message id (0 on failure, with an admin alert). Shared by onJoinRequest and the post-outage
// re-notify so both render the challenge identically.
func (v *Verifier) postGroupChallenge(c context.Context, bot verifyBot, gid, uid int64, name string, ul lang) int {
	gidStr, uidStr := strconv.FormatInt(gid, 10), strconv.FormatInt(uid, 10)
	mention := joinerLabel(uid, name, v.nameSpoilerOn())
	link := ""
	if v.botUsername != "" {
		link = "https://t.me/" + v.botUsername + "?start=verify"
	}
	// The channel requirement is plain text only — the actual follow button lives in the DM step, so
	// users aren't sent away from the verify flow.
	t := tr(ul)
	channelHint := ""
	if v.cfg.requiredChannel(gid) != 0 {
		channelHint = fmt.Sprintf(t.GroupChannelHint, html.EscapeString(v.cfg.channelDisplay(gid)))
	}
	linkText := ""
	if link != "" {
		linkText = fmt.Sprintf(t.GroupLinkText, link)
	}
	body := fmt.Sprintf(t.GroupBody, mention, linkText, v.cfg.TimeoutSeconds, channelHint)

	var rows [][]telego.InlineKeyboardButton
	if link != "" {
		rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: t.VerifyButton, URL: link}))
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

// saveHeartbeat records the last time the bot reached Telegram, so a later restart can estimate the
// downtime. A no-op when the heartbeat path is unset (no STATE_DIRECTORY).
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
	writeJSONFile(v.hbPath, heartbeatRec{LastOnline: t.Unix()})
}

// loadHeartbeat returns the last persisted online time, or the zero time if there is none / it is
// unreadable (first run, or a corrupt file already backed up by loadJSONFile).
func (v *Verifier) loadHeartbeat() time.Time {
	if v.hbPath == "" {
		return time.Time{}
	}
	var r heartbeatRec
	if err := loadJSONFile(v.hbPath, &r); err != nil || r.LastOnline == 0 {
		return time.Time{}
	}
	return time.Unix(r.LastOnline, 0)
}

// heartbeatBot is what the heartbeat needs: a liveness probe plus the verifyBot actions the recovery
// re-notify uses. *telego.Bot satisfies it; a fake satisfies it in tests.
type heartbeatBot interface {
	liveProbe
	verifyBot
}

// runHeartbeat ticks a liveness probe every heartbeatInterval until ctx is cancelled (shutdown).
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

// heartbeatTick runs one liveness probe (a cheap GetMe): on success it advances lastOnline (which
// offlineNow / onExpiry read to pause timeouts during an outage) and, if that success ends an outage
// longer than outageRecovery, refreshes + re-notifies in-progress verifications so an outage doesn't
// quietly eat a legitimate applicant's window. Returns whether the probe succeeded.
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

// onRecovery refreshes every in-progress verification after the bot returns from an outage: each
// pending gets a fresh full window (its old one was eaten by the downtime) and, unless it was already
// re-notified within the last window, a best-effort re-notify — a DM to the applicant and a fresh
// in-group challenge. Bounded by renotifyCap so a large backlog can't become a message storm, and the
// per-pending cooldown keeps repeated flapping from re-messaging the same applicant every cycle. A
// no-op during shutdown.
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
		p.deadline = now.Add(v.timeout())
		v.armExpiry(bot, p, k.gid, k.uid, v.timeout(), "recovered")
		refreshed++
		if !p.lastRenotify.IsZero() && now.Sub(p.lastRenotify) < v.timeout() {
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

// renotifyPending re-notifies one applicant after a recovery: a best-effort DM (silently skipped if
// they've never opened the bot, so Telegram rejects it) and a fresh in-group challenge that replaces
// the stale one. Only network calls here — no lock is held across them; the pending's message id is
// updated afterward if it is still the live one.
func (v *Verifier) renotifyPending(c context.Context, bot verifyBot, gid, uid int64, name string, oldMsg int, p *pending, outage time.Duration) {
	ul := p.lang
	_, _ = bot.SendMessage(c, htmlMessage(uid, fmt.Sprintf(tr(ul).Renotify, outageText(ul, outage))))
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

// outageText renders an outage duration for a user-facing notice: whole seconds under a minute, whole
// minutes under an hour, whole hours above that.
func outageText(l lang, d time.Duration) string {
	units := [3]string{" 秒", " 分钟", " 小时"}
	switch l {
	case langZHT:
		units = [3]string{" 秒", " 分鐘", " 小時"}
	case langEN:
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

// capNote renders the " (… over the cap)" suffix for a recovery log line, or "" when nothing was capped.
func capNote(capped int) string {
	if capped <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d more refreshed silently, over the re-notify cap)", capped)
}
