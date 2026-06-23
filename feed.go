package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// maxTracked bounds how many recently-posted bugs a feed follows for in-place state-change edits.
// When full, trackBug evicts an already-resolved tracked bug first (terminal-ish), only sacrificing
// a still-OPEN bug — whose eventual resolution we still want to catch — when no resolved one remains.
const maxTracked = 200

// recentBugsLimit is the newest-bugs page size. A gap (more new bugs in one interval than this) is
// detected and logged rather than silently skipped (see postFeedItems).
const recentBugsLimit = 100

// maxEditsPerCycle caps how many tracked-bug edits one refresh does, so a large backlog (e.g. after
// downtime, or a mass re-mark) drains over several cycles instead of bursting past Telegram's
// per-chat edit rate limit. The remainder stays tracked and is picked up next cycle.
const maxEditsPerCycle = 20

// maxTrackMisses drops a tracked bug after this many CONSECUTIVE cycles absent from a (non-empty)
// refetch — i.e. it vanished from Bugzilla (deleted / moved to a restricted product) — so it can't
// wedge a tracking slot forever. Generous, so a transient partial-fetch can't evict a live bug.
const maxTrackMisses = 10

// maxEditFails drops a tracked bug after this many CONSECUTIVE non-rate-limit transient edit
// failures (e.g. a misclassified-permanent error), so one un-editable message can't burn an edit
// every cycle forever. Rate-limit (429) failures are NOT counted (they're not the bug's fault).
const maxEditFails = 10

// feedBot is the slice of the telego.Bot API the feed uses to post and edit messages. Threading
// this interface (rather than *telego.Bot) through postFeed and refreshTracked lets the send/edit
// success and error branches be unit-tested with a fake; *telego.Bot satisfies it.
type feedBot interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
	EditMessageText(ctx context.Context, params *telego.EditMessageTextParams) (*telego.Message, error)
}

// feedSendPause throttles bursts of feed sends (catch-up after downtime). A package var so tests
// can zero it; production keeps the gentle 1s pacing.
var feedSendPause = time.Second

// paceFeed waits feedSendPause to space out feed API calls, but returns early (false) if ctx is
// cancelled — so a shutdown isn't held up by pacing, which would otherwise blow past the final
// state-flush grace period and risk re-posting already-delivered items on the next start.
func paceFeed(ctx context.Context) bool {
	if feedSendPause <= 0 {
		return true
	}
	t := time.NewTimer(feedSendPause)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// feedState is the on-disk dedup cursor so a restart doesn't re-post or miss items. Tracked
// records the message id of each recently-posted OPEN bug so the feed can edit it to show
// "resolved" once Bugzilla closes it.
type feedState struct {
	LastBugID   int                    `json:"last_bug_id"`
	LastNewsURL string                 `json:"last_news_url"`
	Tracked     map[string]*trackedBug `json:"tracked,omitempty"` // bug id (as string for JSON) -> posted message
}

type trackedBug struct {
	MsgID     int    `json:"msg_id"`
	State     string `json:"state"`                // last-rendered state key (status|resolution); edit when it changes
	Misses    int    `json:"misses,omitempty"`     // consecutive cycles absent from a non-empty refetch (vanished bug)
	EditFails int    `json:"edit_fails,omitempty"` // consecutive non-rate-limit transient edit failures
	Status    string `json:"status,omitempty"`     // legacy pre-v3.4.3 field; folded into State by migrateFeedState on load
}

// resolvedState reports whether a tracked bug's stored state key (status|resolution) is closed —
// the resolution component is non-empty. Mirrors bugResolved for a persisted state key.
func resolvedState(stateKey string) bool {
	if i := strings.IndexByte(stateKey, '|'); i >= 0 {
		return strings.TrimSpace(stateKey[i+1:]) != ""
	}
	return false
}

// bugStateKey captures a bug's *displayed* state (status + resolution) so the feed only edits a
// tracked message when something visible actually changed — e.g. UNCONFIRMED -> CONFIRMED, or a
// resolution being set.
func bugStateKey(b recentBug) string { return b.Status + "|" + b.Resolution }

// statusOf returns the status component of a "status|resolution" state key.
func statusOf(stateKey string) string {
	if i := strings.IndexByte(stateKey, '|'); i >= 0 {
		return stateKey[:i]
	}
	return stateKey
}

// trackBug remembers the message posted for a bug so a later status change, resolution, OR reopen
// can edit it in place. Both open and born-resolved bugs are tracked (a resolved bug can be reopened
// and re-resolved differently). The map is bounded by maxTracked; when full evictOne drops a
// resolved bug before an open one, so a long-lived open bug isn't lost before its resolution edit.
func (st *feedState) trackBug(b recentBug, msgID int) {
	if msgID == 0 {
		return
	}
	if st.Tracked == nil {
		st.Tracked = map[string]*trackedBug{}
	}
	for len(st.Tracked) >= maxTracked {
		if !st.evictOne() {
			break
		}
	}
	st.Tracked[strconv.Itoa(b.ID)] = &trackedBug{MsgID: msgID, State: bugStateKey(b)}
}

// evictOne removes one tracked bug to make room: the lowest-id RESOLVED bug if any exists, else the
// lowest-id open bug (a junk entry is cleared eagerly). Returns false only if there was nothing to
// evict. Preferring resolved over open means an old open bug keeps its slot until it finally closes.
func (st *feedState) evictOne() bool {
	bestResolved, bestOpen := 0, 0
	for k, tb := range st.Tracked {
		id, err := strconv.Atoi(k)
		if err != nil || tb == nil {
			delete(st.Tracked, k) // junk entry — clearing it makes room
			return true
		}
		if resolvedState(tb.State) {
			if bestResolved == 0 || id < bestResolved {
				bestResolved = id
			}
		} else if bestOpen == 0 || id < bestOpen {
			bestOpen = id
		}
	}
	evict := bestResolved
	if evict == 0 {
		evict = bestOpen
	}
	if evict == 0 {
		return false
	}
	delete(st.Tracked, strconv.Itoa(evict))
	return true
}

type bugUser struct {
	RealName string `json:"real_name"`
	Name     string `json:"name"`
}

func (u bugUser) display() string {
	if u.RealName != "" {
		return u.RealName
	}
	return u.Name
}

// link renders the user as an <a> to their Gentoo Bugzilla bug list in the given role
// ("assigned_to" or "reporter"). Falls back to plain escaped text when there's no email,
// and to "" when there's no name at all.
func (u bugUser) link(role string) string {
	disp := u.display()
	if disp == "" {
		return ""
	}
	if u.Name == "" {
		return html.EscapeString(disp)
	}
	// Bugzilla redacts emails for anonymous API access (Name is just the local part,
	// no @domain), so match by substring rather than equals.
	href := "https://bugs.gentoo.org/buglist.cgi?query_format=advanced&emailtype1=substring&email1=" +
		neturl.QueryEscape(u.Name) + "&email" + role + "1=1"
	return fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(href), html.EscapeString(disp))
}

type recentBug struct {
	ID           int      `json:"id"`
	Summary      string   `json:"summary"`
	Status       string   `json:"status"`
	Resolution   string   `json:"resolution"`
	Product      string   `json:"product"`
	Component    string   `json:"component"`
	Priority     string   `json:"priority"`
	Severity     string   `json:"severity"`
	Keywords     []string `json:"keywords"`
	CreationTime string   `json:"creation_time"`
	Atoms        string   `json:"cf_stabilisation_atoms"` // Gentoo keywording/stabilization package list
	AssignedTo   bugUser  `json:"assigned_to_detail"`
	Creator      bugUser  `json:"creator_detail"`
}

// bugFields is the Bugzilla include_fields list shared by the newest-bugs poll and the
// re-poll of tracked bugs (for in-place state-change edits), so both decode the same recentBug shape.
const bugFields = "id,summary,status,resolution,product,component,priority,severity," +
	"keywords,creation_time,cf_stabilisation_atoms,assigned_to_detail,creator_detail"

func fetchRecentBugs(ctx context.Context) []recentBug {
	u := "https://bugs.gentoo.org/rest/bug?order=bug_id%20DESC&limit=" +
		strconv.Itoa(recentBugsLimit) + "&include_fields=" + bugFields
	var br struct {
		Bugs []recentBug `json:"bugs"`
	}
	if err := httpGetJSON(ctx, u, nil, &br); err != nil {
		log.Printf("feed: bugs fetch: %v", err)
		return nil
	}
	return br.Bugs // newest first (order=bug_id DESC)
}

// fetchBugsByID fetches the current state of specific bugs (to detect when a posted bug has been
// resolved/reopened, so its message can be edited). Requested in chunks so one oversized/failing
// request can't lose the whole batch. Returns allOK=false if ANY chunk failed, so refreshTracked
// can avoid mistaking a bug that was in a failed chunk for one that vanished from Bugzilla.
func fetchBugsByID(ctx context.Context, ids []int) (bugs []recentBug, allOK bool) {
	const chunkSize = 50
	allOK = true
	for i := 0; i < len(ids); i += chunkSize {
		end := i + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		parts := make([]string, end-i)
		for j, id := range ids[i:end] {
			parts[j] = strconv.Itoa(id)
		}
		u := "https://bugs.gentoo.org/rest/bug?include_fields=" + bugFields + "&id=" + strings.Join(parts, ",")
		var br struct {
			Bugs []recentBug `json:"bugs"`
		}
		if err := httpGetJSON(ctx, u, nil, &br); err != nil {
			log.Printf("feed: tracked-bug refetch chunk [%d:%d]: %v", i, end, err)
			allOK = false
			continue
		}
		bugs = append(bugs, br.Bugs...)
	}
	return bugs, allOK
}

// bugResolved reports whether a bug is closed (a resolution has been set: RESOLVED/VERIFIED/…);
// open bugs (UNCONFIRMED/CONFIRMED/IN_PROGRESS) have an empty resolution.
func bugResolved(b recentBug) bool { return strings.TrimSpace(b.Resolution) != "" }

func loadFeedState(path string) feedState {
	var st feedState
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &st); err != nil {
				// Don't silently clobber: a corrupt file would otherwise re-baseline (losing all
				// tracking, the very failure that froze old markers). Preserve it for inspection.
				log.Printf("feed state load %s: %v — backing up to %s.corrupt and starting fresh (tracking re-baselines)", path, err, path)
				st = feedState{}
				if rerr := os.Rename(path, path+".corrupt"); rerr != nil {
					log.Printf("feed state: could not back up corrupt %s: %v", path, rerr)
				}
			}
		}
	}
	migrateFeedState(&st)
	return st
}

// migrateFeedState upgrades state written by a pre-v3.4.3 binary: tracked bugs then stored only
// the bug `status` (no resolution), so fold it into the current `state` key (status|resolution).
// Without this, the first poll after an upgrade sees every tracked bug as "changed" and fires a
// needless edit. A no-op for already-current files (legacy Status empty).
func migrateFeedState(st *feedState) {
	for _, tb := range st.Tracked {
		if tb == nil {
			continue
		}
		if tb.State == "" && tb.Status != "" {
			tb.State = tb.Status + "|" // tracked bugs are open, so resolution was empty
		}
		tb.Status = "" // drop the legacy field so it isn't re-serialized
	}
}

func saveFeedState(path string, st feedState) {
	if path == "" {
		return
	}
	writeJSONFile(path, st)
}

// postFeed sends one feed item and returns the sent message id (0 on failure) plus ok. ok is
// false on a send failure so the caller won't advance the dedup cursor past an item that was
// never delivered (a transient error, a Telegram rate-limit, or shutdown-cancelled context).
// replyTo (0 = none) ties the message to an earlier feed post — used for the confirm notice, which
// replies to the original bug message so the notice links back to it; AllowSendingWithoutReply
// keeps the send working even if that original was deleted.
func postFeed(ctx context.Context, bot feedBot, chatID int64, text string, silent bool, replyTo int) (int, bool) {
	m := htmlMessage(chatID, text)
	if silent {
		m = m.WithDisableNotification()
	}
	if replyTo != 0 {
		m = m.WithReplyParameters(&telego.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true})
	}
	sent, err := bot.SendMessage(ctx, m)
	if err != nil {
		log.Printf("feed: post to %d: %v", chatID, err)
		return 0, false
	}
	paceFeed(ctx) // gentle pacing for catch-up bursts; interruptible so shutdown isn't delayed
	return msgID(sent), true
}

// dateOnly turns "2026-02-26T04:42:47Z" into "2026-02-26".
func dateOnly(t string) string {
	if len(t) >= 10 {
		return t[:10]
	}
	return t
}

// capRunes truncates s to at most n runes (adding an ellipsis when cut), on a rune boundary so
// the result is always valid UTF-8.
func capRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// flattenAtoms collapses the multi-line cf_stabilisation_atoms field into one capped line.
// Truncation is by RUNE (not byte) so a multibyte char can't be cut mid-sequence into invalid
// UTF-8 that Telegram would reject.
func flattenAtoms(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	out := strings.Join(parts, "; ")
	if r := []rune(out); len(r) > 300 {
		out = string(r[:299]) + "…"
	}
	return out
}

// formatBug renders a Bugzilla bug for the feed behind the default open marker (🐞).
func formatBug(b recentBug, lang string) string {
	return formatBugMarked(b, lang, "🐞")
}

// formatBugMarked renders a Bugzilla bug for the feed behind the given leading marker (🐞 open,
// ✅/❌ resolved — passed in rather than string-replaced, so a 🐞 inside a summary can't be hit and
// the marker can't depend on byte ordering). lang "en" uses English field labels, otherwise (default)
// Chinese. Optional fields appear only when present, so simple bugs stay short and rich ones rich.
func formatBugMarked(b recentBug, lang, marker string) string {
	en := strings.EqualFold(lang, "en")
	sep := "："
	if en {
		sep = ": "
	}
	pick := func(zh, eng string) string {
		if en {
			return eng
		}
		return zh
	}
	esc := html.EscapeString
	var sb strings.Builder
	// Cap the free-text summary by rune (Bugzilla summaries are short, but the field is
	// free-form) so a pathological bug can't push the message past Telegram's 4096-char limit.
	fmt.Fprintf(&sb, "%s <a href=\"https://bugs.gentoo.org/%d\"><b>Bug %d</b></a>\n%s", marker, b.ID, b.ID, esc(capRunes(b.Summary, 600)))
	line := func(label, val string) {
		if val != "" {
			fmt.Fprintf(&sb, "\n<b>%s</b>%s%s", label, sep, esc(val))
		}
	}

	zh := !en // zh feed: translate the enum values too (en feed keeps them English)
	status := zhVal(bugStatusZH, b.Status, zh)
	if b.Resolution != "" {
		status += " / " + zhVal(bugResolutionZH, b.Resolution, zh)
	}
	line(pick("状态", "Status"), status)

	comp := b.Product
	if b.Component != "" {
		comp += " › " + b.Component
	}
	line(pick("组件", "Component"), comp)
	line(pick("优先级", "Priority"), zhVal(bugPriorityZH, b.Priority, zh))
	line(pick("严重性", "Severity"), zhVal(bugSeverityZH, b.Severity, zh))
	if len(b.Keywords) > 0 {
		line(pick("关键词", "Keywords"), capRunes(strings.Join(b.Keywords, ", "), 400))
	}
	if atoms := flattenAtoms(b.Atoms); atoms != "" {
		line(pick("包", "Packages"), atoms)
	}

	if a := b.AssignedTo.link("assigned_to"); a != "" {
		fmt.Fprintf(&sb, "\n<b>%s</b>%s%s", pick("负责", "Assigned"), sep, a)
	}
	if c := b.Creator.link("reporter"); c != "" {
		fmt.Fprintf(&sb, "\n<b>%s</b>%s%s", pick("报告", "Reporter"), sep, c)
	}
	if d := dateOnly(b.CreationTime); d != "" {
		line(pick("日期", "Date"), d)
	}
	return sb.String()
}

// resolvedMark is the marker for a CLOSED bug: ✅ only when it was actually FIXED, otherwise ❌ — a
// bug closed as INVALID (误报) / WONTFIX / DUPLICATE / WORKSFORME / OBSOLETE / … was NOT fixed, so a
// green check would misrepresent it.
func resolvedMark(b recentBug) string {
	if strings.EqualFold(strings.TrimSpace(b.Resolution), "FIXED") {
		return "✅"
	}
	return "❌"
}

// formatBugResolved re-renders a now-closed bug for the edited message: the status line shows the
// resolution, and the leading marker is ✅ (FIXED) or ❌ (closed without a fix) so the outcome is
// obvious at a glance.
func formatBugResolved(b recentBug, lang string) string {
	return formatBugMarked(b, lang, resolvedMark(b))
}

// formatNewBug renders a freshly-seen bug for the feed and whether to post it silently. A bug that
// is ALREADY resolved the first time the feed sees it (filed and closed within one poll cycle — e.g.
// resolved INVALID) gets the resolved marker (✅ fixed / ❌ not), not 🐞, and is posted silently: it
// is not an actionable new open bug, so it shouldn't look open or ping. An open bug keeps 🐞 and the
// status-aware silence.
func formatNewBug(b recentBug, lang string, baseSilent bool) (text string, silent bool) {
	if bugResolved(b) {
		return formatBugResolved(b, lang), true
	}
	return formatBug(b, lang), baseSilent
}

// refreshTracked edits the feed message of any tracked bug whose displayed state changed since it
// was last rendered — a status transition (UNCONFIRMED -> CONFIRMED/IN_PROGRESS), a resolution
// (🐞 -> ✅/❌), or a reopen/re-resolution. Runs per feed, in the feed's own language.
//
// Bounded + best-effort: at most maxEditsPerCycle edits per call (a large backlog drains over
// several cycles instead of bursting past Telegram's per-chat edit limit), each paced by
// feedSendPause; a 429 stops the cycle early and retries next time. A bug that vanishes from the
// refetch for maxTrackMisses cycles, hits maxEditFails consecutive non-rate-limit edit failures, or
// gets a permanent edit error is dropped so it can't wedge a tracking slot. RESOLVED bugs stay
// tracked (so a later reopen/re-resolution re-renders); evictOne ages them out under maxTracked.
func refreshTracked(ctx context.Context, bot feedBot, f *FeedConfig, st *feedState, byID map[int]recentBug, fetchOK bool) {
	edits := 0
refresh:
	for idStr, tb := range st.Tracked {
		id, err := strconv.Atoi(idStr)
		if err != nil || tb == nil { // bad id or a null entry (e.g. hand-edited state) — drop it
			delete(st.Tracked, idStr)
			continue
		}
		b, ok := byID[id]
		if !ok {
			// Absent from the refetch. Only treat it as "vanished from Bugzilla" when the WHOLE
			// fetch succeeded; if a chunk failed this cycle the bug may simply have been in it, so
			// leave it untouched (no miss) and retry next cycle — a partial fetch can't drop a live bug.
			if !fetchOK {
				continue
			}
			tb.Misses++
			if tb.Misses >= maxTrackMisses {
				log.Printf("feed: drop tracked bug %d in %d (gone from Bugzilla %d cycles)", id, f.ChatID, tb.Misses)
				delete(st.Tracked, idStr)
			}
			continue
		}
		tb.Misses = 0 // present again
		cur := bugStateKey(b)
		if cur == tb.State {
			continue // nothing visible changed
		}
		if edits >= maxEditsPerCycle {
			break // backlog cap — the rest keep their old state and are picked up next cycle
		}
		wasUnconfirmed := strings.EqualFold(statusOf(tb.State), "UNCONFIRMED")
		text := formatBug(b, f.Lang)
		if bugResolved(b) {
			text = formatBugResolved(b, f.Lang) // 🐞 -> ✅/❌
		}
		edit := htmlMessage(f.ChatID, text)
		_, eerr := bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:             tu.ID(f.ChatID),
			MessageID:          tb.MsgID,
			Text:               edit.Text,
			ParseMode:          edit.ParseMode,
			LinkPreviewOptions: edit.LinkPreviewOptions,
		})
		edits++
		switch {
		case eerr == nil || isNotModified(eerr): // edited (or already current) — sync our state
			tb.EditFails = 0
			if wasUnconfirmed && !bugResolved(b) && !strings.EqualFold(b.Status, "UNCONFIRMED") && !f.bugSilent(b) {
				// The silent UNCONFIRMED post has moved OUT of UNCONFIRMED (but not straight to
				// resolved) — send the non-silent notice the silent original never gave. Advance
				// state only once the ping lands, so a transient send failure retries next cycle (the
				// re-edit is then a harmless "message is not modified"). A silent_bugs feed skips it.
				if _, ok := postFeed(ctx, bot, f.ChatID, confirmNotice(b, f.Lang), false, tb.MsgID); ok {
					tb.State = cur
				}
			} else {
				// Resolved bugs are KEPT (not deleted) so a later reopen/re-resolution is detected;
				// evictOne ages them out under the cap.
				tb.State = cur
			}
		case isRateLimited(eerr):
			log.Printf("feed: edit tracked bug %d in %d rate-limited (%v) — pausing edits this cycle", id, f.ChatID, eerr)
			break refresh
		case permanentEditErr(eerr):
			log.Printf("feed: drop tracked bug %d in %d (uneditable): %v", id, f.ChatID, eerr)
			delete(st.Tracked, idStr)
		default:
			tb.EditFails++
			log.Printf("feed: edit tracked bug %d in %d (transient %d/%d): %v", id, f.ChatID, tb.EditFails, maxEditFails, eerr)
			if tb.EditFails >= maxEditFails {
				log.Printf("feed: drop tracked bug %d in %d after %d consecutive edit failures", id, f.ChatID, maxEditFails)
				delete(st.Tracked, idStr)
			}
		}
		if !paceFeed(ctx) {
			return // shutdown mid-refresh: stop editing; pollAll still persists the advanced cursor
		}
	}
}

// isNotModified reports whether an edit failed only because the new text equals the current text
// (Telegram "message is not modified") — treated as success since the message is already correct.
func isNotModified(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "message is not modified")
}

// permanentEditErr reports whether a message edit can never succeed (the message was deleted or
// is otherwise uneditable), so the bug should be dropped from tracking rather than retried.
// "chat not found" is intentionally NOT permanent — it's usually a transient channel/network blip,
// not a per-message defect; a genuinely dead chat is surfaced by probeFeedPerms and its bugs are
// eventually dropped via maxEditFails. Dropping every changed bug on a blip would be worse.
func permanentEditErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "message to edit not found") ||
		strings.Contains(s, "message can't be edited") ||
		strings.Contains(s, "message_id_invalid")
}

// isRateLimited reports whether an edit/send failed because Telegram throttled us (429). Such a
// failure is transient and not the bug's fault, so refreshTracked stops editing for the cycle
// (rather than hammering) and does NOT count it toward maxEditFails.
func isRateLimited(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "too many requests") || strings.Contains(s, "retry after")
}

func formatNews(n newsItem) string {
	return fmt.Sprintf("📰 <a href=\"%s\">%s — %s</a>",
		html.EscapeString(n.url), n.date, html.EscapeString(html.UnescapeString(n.title)))
}

// confirmNotice is the brief, non-silent message sent when a previously-UNCONFIRMED bug (which was
// posted silently) leaves UNCONFIRMED — the notification the silent original never produced. It
// names the bug's ACTUAL new status (e.g. 已确认 / 处理中, CONFIRMED / IN_PROGRESS) rather than
// always "confirmed", since the trigger is any move out of UNCONFIRMED. 🔔 (not ✅, which marks
// resolution) signals a live status update; rendered in the feed's own language.
func confirmNotice(b recentBug, lang string) string {
	status := zhVal(bugStatusZH, b.Status, !strings.EqualFold(lang, "en")) // CONFIRMED→已确认, IN_PROGRESS→处理中, …
	return fmt.Sprintf("🔔 <a href=\"https://bugs.gentoo.org/%d\"><b>Bug %d</b></a> → %s\n%s",
		b.ID, b.ID, html.EscapeString(status), html.EscapeString(capRunes(b.Summary, 600)))
}

// bugSilent reports whether a feed bug should be posted WITHOUT a notification:
// UNCONFIRMED bugs are silent (a fresh report may be a false alarm); confirmed ones
// notify. silent_bugs=true forces every bug silent regardless of status.
func (f *FeedConfig) bugSilent(b recentBug) bool {
	if f.SilentBugs != nil && *f.SilentBugs {
		return true
	}
	return strings.EqualFold(b.Status, "UNCONFIRMED")
}

// matchesBug reports whether a bug passes this feed's optional product/component filter.
func (f *FeedConfig) matchesBug(b recentBug) bool {
	if f.BugProduct != "" && !strings.EqualFold(b.Product, f.BugProduct) {
		return false
	}
	if f.BugComponent != "" && !strings.EqualFold(b.Component, f.BugComponent) {
		return false
	}
	return true
}

func feedStatePath(dir string, chatID int64) string {
	if dir == "" {
		return ""
	}
	return dir + "/feed-" + strconv.FormatInt(chatID, 10) + ".json"
}

// postFeedItems posts the bugs/news that are new to this feed (filtered, localized, deduped).
func postFeedItems(ctx context.Context, bot *telego.Bot, f *FeedConfig, st *feedState, bugs []recentBug, news []newsItem) {
	if f.bugsOn() && len(bugs) > 0 {
		if st.LastBugID == 0 {
			st.LastBugID = bugs[0].ID // first run: record a baseline, don't backfill history
			log.Printf("feed: %d baselining bug cursor at #%d (no prior bug state — first run or reset)", f.ChatID, bugs[0].ID)
		} else {
			// A FULL page whose oldest bug is still above the cursor means more new bugs were filed
			// in one interval than the fetch window holds — the gap below the page is unreachable and
			// the cursor would jump past it. Log loudly so the loss is recoverable, not invisible.
			if len(bugs) == recentBugsLimit && bugs[len(bugs)-1].ID > st.LastBugID+1 {
				log.Printf("feed: WARNING %d: >%d new bugs since #%d (oldest fetched #%d) exceed the fetch window — #%d..#%d not posted; backfill manually if needed",
					f.ChatID, recentBugsLimit, st.LastBugID, bugs[len(bugs)-1].ID, st.LastBugID+1, bugs[len(bugs)-1].ID-1)
			}
			var nb []recentBug
			for _, b := range bugs {
				if b.ID > st.LastBugID && f.matchesBug(b) {
					nb = append(nb, b)
				}
			}
			delivered := true
			for i := len(nb) - 1; i >= 0; i-- { // oldest first
				text, silent := formatNewBug(nb[i], f.Lang, f.bugSilent(nb[i]))
				mid, ok := postFeed(ctx, bot, f.ChatID, text, silent, 0)
				if !ok {
					delivered = false // leave the cursor so the next cycle retries this item
					break
				}
				st.LastBugID = nb[i].ID
				st.trackBug(nb[i], mid) // follow this open bug for a later state-change edit (confirm / resolve)
			}
			if delivered && bugs[0].ID > st.LastBugID {
				st.LastBugID = bugs[0].ID // all sent -> advance FORWARD past newest seen (incl. filtered-out)
			}
		}
	}
	if f.newsOn() && len(news) > 0 {
		if st.LastNewsURL == "" {
			st.LastNewsURL = news[0].url // first run: baseline only
			log.Printf("feed: %d baselining news cursor (no prior news state — first run or reset)", f.ChatID)
		} else {
			found := false
			var nn []newsItem
			for _, n := range news {
				if n.url == st.LastNewsURL {
					found = true
					break
				}
				nn = append(nn, n)
			}
			if !found {
				// The cursor item is no longer in the fetched list (the index/URL format changed or
				// it scrolled off the page). Re-baseline to the newest item rather than re-broadcast
				// the whole archive — but log it, so a genuine miss (vs a benign format change) is
				// visible instead of silent.
				log.Printf("feed: WARNING %d: news cursor %s not on the fetched page — re-baselining (any items newer than it are skipped, not re-posted)", f.ChatID, st.LastNewsURL)
				st.LastNewsURL = news[0].url
				nn = nil
			}
			delivered := true
			for i := len(nn) - 1; i >= 0; i-- { // oldest first
				if _, ok := postFeed(ctx, bot, f.ChatID, formatNews(nn[i]), false, 0); !ok {
					delivered = false
					break
				}
				st.LastNewsURL = nn[i].url
			}
			if delivered && len(nn) > 0 {
				st.LastNewsURL = news[0].url
			}
		}
	}
}

// pollAll processes the feeds that are DUE at time now (now >= nextDue[chat]); it fetches
// Gentoo bugs + news ONCE for the due set (so upstream load stays at ~2 requests per cycle no
// matter how many feeds) and, after handling each due feed, advances its nextDue by its own
// interval — so a feed's configured interval_seconds is honoured even when feeds differ.
func pollAll(ctx context.Context, bot *telego.Bot, feeds []*FeedConfig, states map[int64]*feedState, stateDir string, now time.Time, nextDue map[int64]time.Time) {
	var due []*FeedConfig
	for _, f := range feeds {
		if !now.Before(nextDue[f.ChatID]) {
			due = append(due, f)
		}
	}
	if len(due) == 0 {
		return
	}
	needBugs, needNews := false, false
	for _, f := range due {
		needBugs = needBugs || f.bugsOn()
		needNews = needNews || f.newsOn()
	}
	// Union of bug ids tracked across the DUE feeds, re-polled once (shared) so resolved bugs
	// can be edited.
	trackedSet := map[int]bool{}
	for _, f := range due {
		for k := range states[f.ChatID].Tracked {
			if id, err := strconv.Atoi(k); err == nil {
				trackedSet[id] = true
			}
		}
	}

	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	var bugs []recentBug
	var news []newsItem
	if needBugs {
		bugs = fetchRecentBugs(fctx)
	}
	if needNews {
		news, _ = fetchNews(fctx)
	}
	var byID map[int]recentBug
	fetchOK := false
	if len(trackedSet) > 0 {
		ids := make([]int, 0, len(trackedSet))
		for id := range trackedSet {
			ids = append(ids, id)
		}
		byID = map[int]recentBug{}
		fetched, ok := fetchBugsByID(fctx, ids)
		for _, b := range fetched {
			byID[b.ID] = b
		}
		fetchOK = ok
	}
	cancel()

	for _, f := range due {
		st := states[f.ChatID]
		postFeedItems(ctx, bot, f, st, bugs, news)
		if len(st.Tracked) > 0 {
			refreshTracked(ctx, bot, f, st, byID, fetchOK)
		}
		saveFeedState(feedStatePath(stateDir, f.ChatID), *st)
		nextDue[f.ChatID] = now.Add(f.interval()) // honour THIS feed's own interval
	}
}

// probeFeedPerms checks, at startup, that the bot can actually post in each feed's target chat,
// so a misconfigured chat_id or a missing admin/post right is logged loudly here instead of
// surfacing only when the first send/edit fails. Best-effort: any probe error is logged, never
// fatal, and never blocks the feed loop.
func probeFeedPerms(ctx context.Context, bot *telego.Bot, feeds []*FeedConfig) {
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	me, err := bot.GetMe(pctx)
	if err != nil {
		log.Printf("feed: startup permission probe skipped (GetMe: %v)", err)
		return
	}
	for _, f := range feeds {
		chat, err := bot.GetChat(pctx, &telego.GetChatParams{ChatID: tu.ID(f.ChatID)})
		if err != nil {
			log.Printf("feed: WARNING target chat %d unreachable at startup (GetChat: %v) — posts will fail; check chat_id and that the bot was added", f.ChatID, err)
			continue
		}
		member, err := bot.GetChatMember(pctx, &telego.GetChatMemberParams{ChatID: tu.ID(f.ChatID), UserID: me.ID})
		if err != nil {
			log.Printf("feed: WARNING cannot read bot membership in chat %d (%s) at startup (%v) — post rights unverified", f.ChatID, chat.Type, err)
			continue
		}
		if reason := feedPostBlocked(chat.Type, member); reason != "" {
			log.Printf("feed: WARNING bot cannot post in target chat %d (%s): %s — make the bot an admin with post rights", f.ChatID, chat.Type, reason)
		} else {
			log.Printf("feed: target chat %d (%s) post permission OK", f.ChatID, chat.Type)
		}
	}
}

// feedPostBlocked returns a human-readable reason the bot can't post to a chat of chatType given
// its membership there, or "" if posting should work. A channel requires admin rights with
// can_post_messages; a group/supergroup only requires that the bot isn't left, banned, or muted.
func feedPostBlocked(chatType string, m telego.ChatMember) string {
	isChannel := chatType == "channel"
	switch mm := m.(type) {
	case *telego.ChatMemberOwner:
		return ""
	case *telego.ChatMemberAdministrator:
		if isChannel && !mm.CanPostMessages {
			return "admin without can_post_messages right"
		}
		return ""
	case *telego.ChatMemberMember:
		if isChannel {
			return "not an admin (a channel needs admin post rights)"
		}
		return ""
	case *telego.ChatMemberRestricted:
		if isChannel {
			return "not an admin (a channel needs admin post rights)"
		}
		if !mm.CanSendMessages {
			return "restricted: can't send messages"
		}
		return ""
	case *telego.ChatMemberLeft:
		return "bot is not a member of the chat"
	case *telego.ChatMemberBanned:
		return "bot is banned from the chat"
	default:
		return "" // unknown member type — don't cry wolf
	}
}

// runFeeds drives the feeds: it ticks at the smallest configured interval (so the fastest feed
// is timely) but each feed only posts when its OWN interval has elapsed (per-feed nextDue). The
// shared bug/news fetch happens once per due cycle. The first poll records a baseline per feed.
// The poll loop is wrapped in a recover so a feed panic can never take down verification.
func runFeeds(ctx context.Context, bot *telego.Bot, feeds []*FeedConfig, stateDir string) {
	tick := feeds[0].interval()
	for _, f := range feeds {
		if d := f.interval(); d < tick {
			tick = d
		}
	}
	states := map[int64]*feedState{}
	for _, f := range feeds {
		st := loadFeedState(feedStatePath(stateDir, f.ChatID))
		states[f.ChatID] = &st
	}
	nextDue := map[int64]time.Time{} // zero => every feed is due on the first poll
	safePoll := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("feed: poll panicked (recovered, feeds continue): %v", r)
			}
		}()
		pollAll(ctx, bot, feeds, states, stateDir, time.Now(), nextDue)
	}
	log.Printf("feed: %d destination(s), tick %s, per-feed interval honoured (shared fetch)", len(feeds), tick)
	probeFeedPerms(ctx, bot, feeds)
	safePoll()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Final flush so the latest cursor/tracking is persisted before exit (best-effort;
			// writeJSONFile is atomic + fsync'd). Each cycle already saves, so this only captures
			// state changed since the last save.
			for _, f := range feeds {
				saveFeedState(feedStatePath(stateDir, f.ChatID), *states[f.ChatID])
			}
			return
		case <-t.C:
			safePoll()
		}
	}
}
