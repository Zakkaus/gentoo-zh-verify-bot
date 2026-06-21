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

// maxTracked bounds how many recently-posted open bugs a feed follows for in-place state-change
// edits (older ones drop off — these edits are best-effort for recent bugs).
const maxTracked = 200

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

// feedState is the on-disk dedup cursor so a restart doesn't re-post or miss items. Tracked
// records the message id of each recently-posted OPEN bug so the feed can edit it to show
// "resolved" once Bugzilla closes it.
type feedState struct {
	LastBugID   int                    `json:"last_bug_id"`
	LastNewsURL string                 `json:"last_news_url"`
	Tracked     map[string]*trackedBug `json:"tracked,omitempty"` // bug id (as string for JSON) -> posted message
}

type trackedBug struct {
	MsgID  int    `json:"msg_id"`
	State  string `json:"state"`            // last-rendered state key (status|resolution); edit when it changes
	Status string `json:"status,omitempty"` // legacy pre-v3.4.3 field; folded into State by migrateFeedState on load
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

// trackBug remembers the message posted for an OPEN bug so a later status change / resolution can
// edit it in place. Resolved bugs aren't tracked (nothing left to update); the map is bounded by
// maxTracked, dropping the lowest (oldest) bug id when full.
func (st *feedState) trackBug(b recentBug, msgID int) {
	if msgID == 0 || bugResolved(b) {
		return
	}
	if st.Tracked == nil {
		st.Tracked = map[string]*trackedBug{}
	}
	for len(st.Tracked) >= maxTracked {
		oldest := 0
		for k := range st.Tracked {
			if id, err := strconv.Atoi(k); err == nil && (oldest == 0 || id < oldest) {
				oldest = id
			}
		}
		if oldest == 0 {
			break
		}
		delete(st.Tracked, strconv.Itoa(oldest))
	}
	st.Tracked[strconv.Itoa(b.ID)] = &trackedBug{MsgID: msgID, State: bugStateKey(b)}
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
	u := "https://bugs.gentoo.org/rest/bug?order=bug_id%20DESC&limit=30&include_fields=" + bugFields
	var br struct {
		Bugs []recentBug `json:"bugs"`
	}
	if err := httpGetJSON(ctx, u, nil, &br); err != nil {
		log.Printf("feed: bugs fetch: %v", err)
		return nil
	}
	return br.Bugs // newest first (order=bug_id DESC)
}

// fetchBugsByID fetches the current state of specific bugs (used to detect when a previously
// posted bug has been resolved/closed, so its feed message can be edited).
func fetchBugsByID(ctx context.Context, ids []int) []recentBug {
	if len(ids) == 0 {
		return nil
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	u := "https://bugs.gentoo.org/rest/bug?include_fields=" + bugFields + "&id=" + strings.Join(parts, ",")
	var br struct {
		Bugs []recentBug `json:"bugs"`
	}
	if err := httpGetJSON(ctx, u, nil, &br); err != nil {
		log.Printf("feed: tracked-bug refetch: %v", err)
		return nil
	}
	return br.Bugs
}

// bugResolved reports whether a bug is closed (a resolution has been set: RESOLVED/VERIFIED/…);
// open bugs (UNCONFIRMED/CONFIRMED/IN_PROGRESS) have an empty resolution.
func bugResolved(b recentBug) bool { return strings.TrimSpace(b.Resolution) != "" }

func loadFeedState(path string) feedState {
	var st feedState
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &st); err != nil {
				log.Printf("feed state load %s: %v", path, err)
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
func postFeed(ctx context.Context, bot feedBot, chatID int64, text string, silent bool) (int, bool) {
	m := htmlMessage(chatID, text)
	if silent {
		m = m.WithDisableNotification()
	}
	sent, err := bot.SendMessage(ctx, m)
	if err != nil {
		log.Printf("feed: post to %d: %v", chatID, err)
		return 0, false
	}
	time.Sleep(feedSendPause) // gentle pacing for catch-up bursts
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

// formatBug renders a Bugzilla bug for the feed. lang "en" uses English field labels,
// otherwise (default) Chinese. Optional fields appear only when present, so simple bugs
// stay short and keywording bugs stay rich.
func formatBug(b recentBug, lang string) string {
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
	fmt.Fprintf(&sb, "🐞 <a href=\"https://bugs.gentoo.org/%d\"><b>Bug %d</b></a>\n%s", b.ID, b.ID, esc(capRunes(b.Summary, 600)))
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

// formatBugResolved re-renders a now-closed bug for the edited message: the status line shows
// the resolution, and the 🐞 marker becomes ✅ so the closure is obvious at a glance.
func formatBugResolved(b recentBug, lang string) string {
	return strings.Replace(formatBug(b, lang), "🐞", "✅", 1)
}

// refreshTracked edits the feed message of any tracked bug whose displayed state changed since
// it was posted — a status transition (e.g. an UNCONFIRMED bug becoming CONFIRMED / IN_PROGRESS)
// or a resolution (🐞 -> ✅, after which the bug is untracked). Runs per feed, in the feed's own
// language. Best-effort: a transient edit failure keeps the bug tracked for a retry; a permanent
// one (message deleted / uneditable) drops it.
func refreshTracked(ctx context.Context, bot feedBot, f *FeedConfig, st *feedState, byID map[int]recentBug) {
	for idStr, tb := range st.Tracked {
		id, err := strconv.Atoi(idStr)
		if err != nil || tb == nil { // bad id or a null entry (e.g. hand-edited state) — drop it
			delete(st.Tracked, idStr)
			continue
		}
		b, ok := byID[id]
		if !ok {
			continue // not in this refresh batch — keep tracking
		}
		cur := bugStateKey(b)
		if cur == tb.State {
			continue // nothing visible changed
		}
		wasUnconfirmed := strings.EqualFold(statusOf(tb.State), "UNCONFIRMED")
		text := formatBug(b, f.Lang)
		if bugResolved(b) {
			text = formatBugResolved(b, f.Lang) // 🐞 -> ✅
		}
		edit := htmlMessage(f.ChatID, text)
		_, eerr := bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:             tu.ID(f.ChatID),
			MessageID:          tb.MsgID,
			Text:               edit.Text,
			ParseMode:          edit.ParseMode,
			LinkPreviewOptions: edit.LinkPreviewOptions,
		})
		switch {
		case eerr == nil || isNotModified(eerr): // edited (or already current) — sync our state
			switch {
			case bugResolved(b):
				delete(st.Tracked, idStr) // terminal — stop tracking
			case wasUnconfirmed && !strings.EqualFold(b.Status, "UNCONFIRMED") && !f.bugSilent(b):
				// The UNCONFIRMED post was silent; it has now moved OUT of UNCONFIRMED (CONFIRMED /
				// IN_PROGRESS / …, but not resolved — handled above). Send the fresh non-silent
				// notice the silent original never gave, for ANY such transition rather than only
				// exactly CONFIRMED — so a bug that races past CONFIRMED to IN_PROGRESS before the
				// ping lands still notifies. Advance state only once the ping is delivered, so a
				// transient send failure retries next cycle (the re-edit is then a harmless "message
				// is not modified"). A silent_bugs feed skips the ping entirely.
				if _, ok := postFeed(ctx, bot, f.ChatID, confirmNotice(b, f.Lang), false); ok {
					tb.State = cur
				}
			default:
				tb.State = cur
			}
		case permanentEditErr(eerr):
			log.Printf("feed: drop tracked bug %d in %d (uneditable): %v", id, f.ChatID, eerr)
			delete(st.Tracked, idStr)
		default:
			log.Printf("feed: edit tracked bug %d in %d: %v", id, f.ChatID, eerr) // transient — retry next cycle
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
func permanentEditErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "message to edit not found") ||
		strings.Contains(s, "message can't be edited") ||
		strings.Contains(s, "message_id_invalid") ||
		strings.Contains(s, "chat not found")
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
		} else {
			var nb []recentBug
			for _, b := range bugs {
				if b.ID > st.LastBugID && f.matchesBug(b) {
					nb = append(nb, b)
				}
			}
			delivered := true
			for i := len(nb) - 1; i >= 0; i-- { // oldest first
				mid, ok := postFeed(ctx, bot, f.ChatID, formatBug(nb[i], f.Lang), f.bugSilent(nb[i]))
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
				// The cursor item is no longer in the fetched list (the index/URL format
				// changed or it scrolled off the page). Re-baseline to the newest item
				// instead of re-broadcasting the entire news archive.
				st.LastNewsURL = news[0].url
				nn = nil
			}
			delivered := true
			for i := len(nn) - 1; i >= 0; i-- { // oldest first
				if _, ok := postFeed(ctx, bot, f.ChatID, formatNews(nn[i]), false); !ok {
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
	if len(trackedSet) > 0 {
		ids := make([]int, 0, len(trackedSet))
		for id := range trackedSet {
			ids = append(ids, id)
		}
		byID = map[int]recentBug{}
		for _, b := range fetchBugsByID(fctx, ids) {
			byID[b.ID] = b
		}
	}
	cancel()

	for _, f := range due {
		st := states[f.ChatID]
		postFeedItems(ctx, bot, f, st, bugs, news)
		if len(byID) > 0 {
			refreshTracked(ctx, bot, f, st, byID)
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
			return
		case <-t.C:
			safePoll()
		}
	}
}
