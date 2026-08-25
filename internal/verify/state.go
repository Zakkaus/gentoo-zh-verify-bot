package verify

import (
	"context"
	"fmt"
	"html"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// Tallies self-reported models from the kernel challenge's agent tripwire.
// Claims are untrusted usage data, never evidence.

// Unknown models fold into "other" after this many distinct keys.
const agentModelMax = 200

// Only model-ID characters may reach logs or persisted state.
var modelValue = regexp.MustCompile(`[^0-9A-Za-z.:_/+-]+`)

// modelDeclare matches the explicit form requested by the tripwire.
var modelDeclare = regexp.MustCompile(`(?i)\bmodel\s*[=:]\s*([0-9A-Za-z][0-9A-Za-z.:_/+-]*)`)

// Prose replies are normalized to these families. Longest matches win.
var modelFamilies = []string{
	// western
	"claude", "sonnet", "opus", "haiku", "chatgpt", "gpt", "openai", "o3", "o4", "gemini", "gemma",
	"bard", "grok", "llama", "mistral", "mixtral", "command-r", "cohere", "copilot", "perplexity",
	"sonar", "phi",
	// chinese / low-cost, the ones cheap spam bots actually run
	"deepseek", "qwen", "tongyi", "kimi", "moonshot", "chatglm", "glm", "zhipu", "doubao", "hunyuan",
	"ernie", "wenxin", "spark", "minimax", "abab", "baichuan", "internlm", "yi", "step", "skywork",
	"telechat", "sensechat",
	// hosting layers an agent may name instead of a model
	"ollama", "openrouter", "groq", "together", "siliconflow",
}

// Whole-word matching prevents short names such as "yi" from matching inside other words.
var familyRe *regexp.Regexp

func init() {
	sort.Slice(modelFamilies, func(i, j int) bool {
		if len(modelFamilies[i]) != len(modelFamilies[j]) {
			return len(modelFamilies[i]) > len(modelFamilies[j]) // "chatgpt" must win over "gpt"
		}
		return modelFamilies[i] < modelFamilies[j]
	})
	familyRe = regexp.MustCompile(`(?i)\b(` + strings.Join(modelFamilies, "|") + `)\b`)
}

// claimedModel extracts and sanitizes an explicit model ID or recognized family.
func claimedModel(text string) string {
	if m := modelDeclare.FindStringSubmatch(text); len(m) == 2 {
		return sanitizeModel(m[1])
	}
	if f := familyRe.FindString(text); f != "" {
		return strings.ToLower(f)
	}
	return "unknown"
}

func sanitizeModel(s string) string {
	s = strings.ToLower(strings.TrimSpace(modelValue.ReplaceAllString(s, "")))
	if s == "" {
		return "unknown"
	}
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// agentTally persists tripwire counts by claimed model.
type agentTally struct {
	Total  int            `json:"total"`
	Counts map[string]int `json:"counts"`
}

// recordAgent persists one tripwire result and returns its model and the new total.
func (v *Service) recordAgent(text string) (model string, total int) {
	model = claimedModel(text)
	// Snapshot under the store write lock before agentMu; reversing that order can deadlock other saves.
	count := func() any {
		v.agentMu.Lock()
		defer v.agentMu.Unlock()
		if v.agents.Counts == nil {
			v.agents.Counts = map[string]int{}
		}
		if _, known := v.agents.Counts[model]; !known && len(v.agents.Counts) >= agentModelMax {
			model = "other" // key cap reached: fold the long tail into one bucket
		}
		v.agents.Counts[model]++
		v.agents.Total++
		total = v.agents.Total
		return agentTally{Total: v.agents.Total, Counts: copyCounts(v.agents.Counts)}
	}
	if v.agentPath == "" {
		count() // no persistence configured: the in-memory tally still has to advance
		return model, total
	}
	_ = store.Save(v.agentPath, count)
	return model, total
}

// copyCounts isolates the persisted snapshot from later increments.
func copyCounts(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, n := range m {
		out[k] = n
	}
	return out
}

// Missing or corrupt state restores as an empty tally; unreadable state disables later writes.
func (v *Service) loadAgents() {
	if v.agentPath == "" {
		return
	}
	var t agentTally
	if err := store.Load(v.agentPath, &t); err != nil {
		if store.ReadFailed(err) {
			v.agentPath = ""
		}
		return
	}
	v.agentMu.Lock()
	v.agents = t
	if v.agents.Counts == nil {
		v.agents.Counts = map[string]int{}
	}
	v.agentMu.Unlock()
	if t.Total > 0 {
		log.Printf("restored automated-agent tally: %d total across %d model(s)", t.Total, len(t.Counts))
	}
}

// AgentStatsText returns the six busiest claimed models or an empty string before the first catch.
func (v *Service) AgentStatsText(l i18n.Lang) string {
	v.agentMu.Lock()
	total := v.agents.Total
	counts := copyCounts(v.agents.Counts)
	v.agentMu.Unlock()
	if total == 0 {
		return ""
	}
	type kv struct {
		model string
		n     int
	}
	list := make([]kv, 0, len(counts))
	for m, n := range counts {
		list = append(list, kv{m, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].model < list[j].model
	})
	if len(list) > 6 { // keep the line short; the state file holds the full breakdown
		list = list[:6]
	}
	parts := make([]string, 0, len(list))
	for _, e := range list {
		parts = append(parts, fmt.Sprintf("%s %d", e.model, e.n))
	}
	return v.messages.Verification.Admin.AgentStats.Render(l, total, strings.Join(parts, "、"))
}

// vfailRec drives both retry cooldowns and automatic bans.
type vfailRec struct {
	count int
	last  time.Time
}

// JSON uses a slice because pkey cannot be an object key.
type vfailDisk struct {
	GroupID int64 `json:"group_id"`
	UserID  int64 `json:"user_id"`
	Count   int   `json:"count"`
	Last    int64 `json:"last"`
}

// Clear the bounded map wholesale under an exceptional ID flood.
const vfailMax = 50000

// Only sustained failures within this rolling window accumulate toward a ban.
const verifyFailWindow = 6 * time.Hour

func (v *Service) loadVerifyFails() {
	if v.vfailPath == "" {
		return
	}
	var recs []vfailDisk
	if err := store.Load(v.vfailPath, &recs); err != nil {
		if store.ReadFailed(err) {
			v.vfailPath = ""
		}
		return // corrupt files were backed up; unreadable files remain untouched and write-disabled
	}
	v.mu.Lock()
	for _, r := range recs {
		if r.Count > 0 {
			v.vfail[pkey{r.GroupID, r.UserID}] = &vfailRec{count: r.Count, last: time.Unix(r.Last, 0)}
		}
	}
	n := len(v.vfail)
	v.mu.Unlock()
	if n > 0 {
		log.Printf("restored %d verification-strike record(s)", n)
	}
}

func (v *Service) saveVerifyFails() {
	if v.vfailPath == "" {
		return
	}
	_ = store.Save(v.vfailPath, func() any {
		v.mu.Lock()
		defer v.mu.Unlock()
		recs := make([]vfailDisk, 0, len(v.vfail))
		for k, r := range v.vfail {
			if r.count > 0 {
				recs = append(recs, vfailDisk{GroupID: k.gid, UserID: k.uid, Count: r.count, Last: r.last.Unix()})
			}
		}
		return recs
	})
}

// Strikes persist across restarts; a negative threshold disables automatic bans.
func (v *Service) recordVerifyFail(gid, uid int64) (count int, ban bool) {
	v.mu.Lock()
	key := pkey{gid, uid}
	r := v.vfail[key]
	if r == nil {
		r = &vfailRec{}
		if len(v.vfail) >= vfailMax {
			v.vfail = map[pkey]*vfailRec{} // bound the map (see vfailMax)
		}
		v.vfail[key] = r
	}
	if r.count > 0 && time.Since(r.last) > verifyFailWindow {
		r.count = 0 // Isolated old failures must not accumulate into a ban.
		// Only failures inside verifyFailWindow accumulate.
	}
	r.count++
	r.last = time.Now()
	count = r.count
	v.mu.Unlock()
	v.saveVerifyFails()
	max := v.verifyMaxFails(gid)
	return count, max > 0 && count >= max
}

// Successful verification clears prior strikes.
func (v *Service) clearVerifyFails(gid, uid int64) {
	v.mu.Lock()
	_, had := v.vfail[pkey{gid, uid}]
	delete(v.vfail, pkey{gid, uid})
	v.mu.Unlock()
	if had {
		v.saveVerifyFails()
	}
}

func (v *Service) verifyMaxFails(groupID int64) int {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return 0
	}
	return group.VerifyMaxFails().Value
}

func (v *Service) verifyRetrySeconds(groupID int64) int {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return 0
	}
	return group.VerifyRetrySeconds().Value
}

// verifyCooldownRemaining returns zero when the applicant may reapply.
func (v *Service) verifyCooldownRemaining(gid, uid int64) time.Duration {
	secs := v.verifyRetrySeconds(gid)
	if secs <= 0 {
		return 0
	}
	v.mu.Lock()
	var count int
	var last time.Time
	if r := v.vfail[pkey{gid, uid}]; r != nil {
		count, last = r.count, r.last // copy under the lock — r is a pointer shared with recordVerifyFail
	}
	v.mu.Unlock()
	if count == 0 {
		return 0
	}
	if elapsed := time.Since(last); elapsed < time.Duration(secs)*time.Second {
		return time.Duration(secs)*time.Second - elapsed
	}
	return 0
}
func (v *Service) now() time.Time { return time.Now().In(v.loc) }

func (v *Service) recordDecision(approve bool) {
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

// Stats returns today's approve and decline counters in the configured statistics timezone.
func (v *Service) Stats() (date string, approved, declined int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	today := v.now().Format("2006-01-02")
	if v.statDate != today {
		return today, 0, 0
	}
	return v.statDate, v.approved, v.declined
}

func (v *Service) save() {
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

func (v *Service) load(bot verifyBot) {
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
func (v *Service) offlineNow() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return !v.lastOnline.IsZero() && time.Since(v.lastOnline) > offlineThreshold
}

// Probe at expiry to cover heartbeat detection lag at outage onset.
// Tests without a probe remain reachable.
func (v *Service) reachable(c context.Context) bool {
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
func (v *Service) armExpiry(bot verifyBot, p *pending, gid, uid int64, delay time.Duration, reason string) {
	p.epoch++
	epoch := p.epoch
	nonce := p.nonce
	p.timer = time.AfterFunc(delay, func() { v.onExpiry(context.Background(), bot, gid, uid, nonce, epoch, reason) })
}

// Unreachable expiries receive a fresh window without consume or strike.
// Online settlement still requires the captured nonce and epoch.
func (v *Service) onExpiry(c context.Context, bot verifyBot, gid, uid int64, nonce string, epoch uint64, reason string) {
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
func (v *Service) deferExpiry(bot verifyBot, gid, uid int64, nonce string, epoch uint64, reason string) {
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
func (v *Service) postGroupChallenge(c context.Context, bot verifyBot, gid, uid int64, name string, l i18n.Lang) int {
	gidStr, uidStr := strconv.FormatInt(gid, 10), strconv.FormatInt(uid, 10)
	mention := joinerLabel(uid, name, v.NameSpoilerOn(gid))
	link := ""
	if v.botUsername != "" {
		link = "https://t.me/" + v.botUsername + "?start=verify"
	}
	// Keep channel navigation inside the DM verification flow.
	group := &(*v.messages).Verification.Group
	admin := &(*v.messages).Verification.Admin
	channelHint := ""
	if v.RequiredChannelID(gid) != 0 {
		channelHint = group.ChannelHint.Render(l, html.EscapeString(v.channelDisplay(gid)))
	}
	linkText := ""
	if link != "" {
		linkText = group.LinkText.Render(l, link)
	}
	body := group.Body.Render(l, mention, linkText, int(v.timeout(gid)/time.Second), channelHint)

	var rows [][]telego.InlineKeyboardButton
	if link != "" {
		rows = append(rows, tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: group.VerifyButton.For(l), URL: link}))
	}
	rows = append(rows, tu.InlineKeyboardRow(
		telego.InlineKeyboardButton{Text: admin.ApproveButton.For(l), CallbackData: AdminCallbackPrefix + "pass:" + gidStr + ":" + uidStr},
		telego.InlineKeyboardButton{Text: admin.BanButton.For(l), CallbackData: AdminCallbackPrefix + "ban:" + gidStr + ":" + uidStr},
	))
	sent, err := bot.SendMessage(c, htmlMessage(gid, body).WithReplyMarkup(tu.InlineKeyboard(rows...)))
	if err != nil {
		log.Printf("join %d in %d: post challenge failed: %v", uid, gid, err)
		v.adminAlert(c, bot, admin.ChallengePostFailed.Render(l, gid, uid, err))
		return 0
	}
	return msgID(sent)
}

type heartbeatRec struct {
	LastOnline int64 `json:"last_online"`
}

// Persist reachability so restart recovery can estimate downtime.
func (v *Service) saveHeartbeat() {
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
func (v *Service) loadHeartbeat() time.Time {
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

// RunHeartbeat probes Telegram until ctx is cancelled and refreshes pending challenges after outages.
func (v *Service) RunHeartbeat(ctx context.Context, bot Telegram) {
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
func (v *Service) heartbeatTick(ctx context.Context, bot heartbeatBot) bool {
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
func (v *Service) onRecovery(c context.Context, bot verifyBot, outage time.Duration) {
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
func (v *Service) renotifyPending(c context.Context, bot verifyBot, gid, uid int64, name string, oldMsg int, p *pending, outage time.Duration) {
	ul := p.lang
	notice := v.messages.Verification.Recovery.Renotify.Render(ul, outageText(v.messages, ul, outage))
	_, _ = bot.SendMessage(c, htmlMessage(uid, notice))
	newMsg := v.postGroupChallenge(c, bot, gid, uid, name, v.groupLanguage(gid))
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
func outageText(messages *i18n.Catalog, l i18n.Lang, d time.Duration) string {
	recovery := &messages.Verification.Recovery
	switch {
	case d < time.Minute:
		return recovery.OutageSeconds.Render(l, int(d.Seconds()))
	case d < time.Hour:
		return recovery.OutageMinutes.Render(l, int(d.Minutes()))
	default:
		return recovery.OutageHours.Render(l, int(d.Hours()))
	}
}

func capNote(capped int) string {
	if capped <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d more refreshed silently, over the re-notify cap)", capped)
}
