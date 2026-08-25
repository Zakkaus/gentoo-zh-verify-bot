package feed

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	neturl "net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/lookup"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// maxTracked bounds how many recently-posted bugs a feed follows for in-place state-change edits.
// When full, trackBug evicts an already-resolved tracked bug first (terminal-ish), only sacrificing
// a still-OPEN bug — whose eventual resolution we still want to catch — when no resolved one remains.
const maxTracked = 200

// recentBugsLimit bounds each Bugzilla catch-up slice and each destination's delivery per cycle.
// Querying IDs above the durable cursor in ascending order lets a large backlog advance without
// repeatedly walking newer pages that cannot fit inside one fetch deadline.
const recentBugsLimit = 100

const maxBugCatchUpPerCycle = recentBugsLimit

// maxEditsPerCycle caps how many tracked-bug edits one refresh does, so a large backlog (e.g. after
// downtime, or a mass re-mark) drains over several cycles instead of bursting past Telegram's
// per-chat edit rate limit. The remainder stays tracked and is picked up next cycle.
const maxEditsPerCycle = 20

// maxTrackMisses drops a tracked bug after this many CONSECUTIVE cycles absent from a (non-empty)
// refetch — i.e. it vanished from Bugzilla (deleted / moved to a restricted product) — so it can't
// wedge a tracking slot forever. Generous, so a transient partial-fetch can't evict a live bug.
const maxTrackMisses = 10

// maxEditFails drops a tracked bug after this many consecutive deterministic 400 responses that
// are not classified more precisely. Transport, cancellation, rate-limit, and 5xx failures reset
// this counter because they do not prove that the specific message is permanently uneditable.
const maxEditFails = 10

// maxConfirmTries bounds how many cycles the feed retries an UNCONFIRMED->confirmed ping that keeps
// failing to send, so an edits-work-but-sends-fail outage can't pin a bug into an endless re-edit
// loop; after this it gives up the (best-effort, observability-only) ping and advances state.
const maxConfirmTries = 10

// feedBot is the slice of the telego.Bot API the feed uses to post and edit messages. Threading
// this interface (rather than *telego.Bot) through postFeed and refreshTracked lets the send/edit
// success and error branches be unit-tested with a fake; *telego.Bot satisfies it.
type feedBot interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
	EditMessageText(ctx context.Context, params *telego.EditMessageTextParams) (*telego.Message, error)
}

// Service polls configured Bugzilla and news feeds and persists their per-chat cursors.
type Service struct {
	bot      *telego.Bot
	feeds    []*config.FeedConfig
	stateDir string
}

// New constructs a feed service from its Telegram bot, destination configs, and state directory.
func New(bot *telego.Bot, feeds []*config.FeedConfig, stateDir string) *Service {
	return &Service{bot: bot, feeds: feeds, stateDir: stateDir}
}

// feedSendPause throttles bursts of feed sends (catch-up after downtime). Package variables let
// tests remove pacing and exercise deadlines without waiting for production durations.
var (
	feedSendPause       = time.Second
	feedTelegramTimeout = 15 * time.Second
	feedFetchTimeout    = 30 * time.Second
)

// feedState is the on-disk dedup cursor so a restart doesn't re-post or miss items. Tracked
// records the message id of each recently-posted OPEN bug so the feed can edit it to show
// "resolved" once Bugzilla closes it.
type feedState struct {
	LastBugID     int                    `json:"last_bug_id"`
	LastNewsURL   string                 `json:"last_news_url"`
	Tracked       map[string]*trackedBug `json:"tracked,omitempty"` // bug id (as string for JSON) -> posted message
	writeDisabled bool                   `json:"-"`
}

type trackedBug struct {
	MsgID        int    `json:"msg_id"`
	State        string `json:"state"`                   // last-rendered state key (status|resolution); edit when it changes
	Misses       int    `json:"misses,omitempty"`        // consecutive cycles absent from a non-empty refetch (vanished bug)
	EditFails    int    `json:"edit_fails,omitempty"`    // consecutive deterministic, unclassified Telegram 400 responses
	ConfirmTries int    `json:"confirm_tries,omitempty"` // consecutive failed confirm-ping sends (bounded by maxConfirmTries)
	Status       string `json:"status,omitempty"`        // legacy pre-v3.4.3 field; folded into State by migrateFeedState on load
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
// Bugzilla only emits each *_detail object when its base user field is requested too.
const bugFields = "id,summary,status,resolution,product,component,priority,severity," +
	"keywords,creation_time,cf_stabilisation_atoms,assigned_to,creator,assigned_to_detail,creator_detail"

type recentBugBatchFetcher func(context.Context, int) ([]recentBug, error)

type feedJSONFetcher func(context.Context, string, any) error

func fetchRecentBugs(ctx context.Context, afterID int) ([]recentBug, bool) {
	return fetchRecentBugsWith(ctx, afterID, func(ctx context.Context, url string, dst any) error {
		return lookup.GetJSON(ctx, url, nil, dst)
	})
}

func fetchRecentBugsWith(ctx context.Context, afterID int, getJSON feedJSONFetcher) ([]recentBug, bool) {
	return collectRecentBugs(ctx, afterID, func(ctx context.Context, afterID int) ([]recentBug, error) {
		u := "https://bugs.gentoo.org/rest/bug?include_fields=" + bugFields
		if afterID == 0 {
			u += "&order=bug_id%20DESC&limit=1"
		} else {
			u += "&f1=bug_id&o1=greaterthan&v1=" + strconv.Itoa(afterID) +
				"&order=bug_id%20ASC&limit=" + strconv.Itoa(recentBugsLimit)
		}
		var br struct {
			Bugs *[]recentBug `json:"bugs"`
		}
		if err := getJSON(ctx, u, &br); err != nil {
			return nil, err
		}
		if br.Bugs == nil {
			return nil, errors.New(`response missing "bugs" field`)
		}
		return *br.Bugs, nil
	})
}

// One bounded ascending batch advances the cursor; a later cycle starts from that new boundary.
func collectRecentBugs(ctx context.Context, afterID int, fetch recentBugBatchFetcher) ([]recentBug, bool) {
	batch, err := fetch(ctx, afterID)
	if err != nil {
		log.Printf("feed: bugs fetch after ID %d: %v", afterID, err)
		return nil, false
	}
	seen := make(map[int]bool, len(batch))
	bugs := make([]recentBug, 0, len(batch))
	for _, b := range batch {
		if b.ID <= afterID || seen[b.ID] {
			continue
		}
		seen[b.ID] = true
		bugs = append(bugs, b)
	}
	if afterID == 0 {
		sort.Slice(bugs, func(i, j int) bool { return bugs[i].ID > bugs[j].ID })
		if len(bugs) > 1 {
			bugs = bugs[:1]
		}
		return bugs, true
	}
	sort.Slice(bugs, func(i, j int) bool { return bugs[i].ID < bugs[j].ID })
	if len(bugs) > recentBugsLimit {
		bugs = bugs[:recentBugsLimit]
	}
	return bugs, true
}

// fetchBugsByID fetches the current state of specific bugs (to detect when a posted bug has been
// resolved/reopened, so its message can be edited). Each chunk gets a fresh deadline, so one hung
// Bugzilla request cannot consume the budget of every chunk that follows.
func fetchBugsByID(ctx context.Context, ids []int) (bugs []recentBug, allOK bool) {
	return fetchBugsByIDWith(ctx, ids, func(ctx context.Context, url string, dst any) error {
		return lookup.GetJSON(ctx, url, nil, dst)
	})
}

func fetchBugsByIDWith(ctx context.Context, ids []int, getJSON feedJSONFetcher) (bugs []recentBug, allOK bool) {
	const chunkSize = 50
	allOK = true
	for i := 0; i < len(ids); i += chunkSize {
		if ctx.Err() != nil {
			return bugs, false
		}
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
			Bugs *[]recentBug `json:"bugs"`
		}
		chunkCtx, cancel := context.WithTimeout(ctx, feedFetchTimeout)
		err := getJSON(chunkCtx, u, &br)
		cancel()
		if err != nil {
			log.Printf("feed: tracked-bug refetch chunk [%d:%d]: %v", i, end, err)
			allOK = false
			continue
		}
		if br.Bugs == nil {
			log.Printf(`feed: tracked-bug refetch chunk [%d:%d]: response missing "bugs" field`, i, end)
			allOK = false
			continue
		}
		bugs = append(bugs, (*br.Bugs)...)
	}
	return bugs, allOK
}

// bugResolved reports whether a bug is closed (a resolution has been set: RESOLVED/VERIFIED/…);
// open bugs (UNCONFIRMED/CONFIRMED/IN_PROGRESS) have an empty resolution.
func bugResolved(b recentBug) bool { return strings.TrimSpace(b.Resolution) != "" }

func loadFeedState(path string) feedState {
	var st feedState
	if path != "" {
		if err := store.Load(path, &st); err != nil {
			st = feedState{writeDisabled: store.ReadFailed(err)}
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
	if path == "" || st.writeDisabled {
		return
	}
	_ = store.Write(path, st)
}

// postFeed sends one feed item and returns the sent message id (0 on failure) plus classifications
// callers need to decide whether to retry. Every Telegram operation gets a short-lived child
// context; the feed loop's parent context intentionally remains long-lived.
// replyTo (0 = none) ties a confirmation notice to the original bug message.
func postFeed(ctx context.Context, bot feedBot, chatID int64, text string, silent bool, replyTo int) (id int, ok, rateLimited, permanent bool) {
	m := tg.HTMLMessage(chatID, text)
	if silent {
		m = m.WithDisableNotification()
	}
	if replyTo != 0 {
		m = m.WithReplyParameters(&telego.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true})
	}
	opCtx, cancel := context.WithTimeout(ctx, feedTelegramTimeout)
	sent, err := bot.SendMessage(opCtx, m)
	cancel()
	if err != nil {
		log.Printf("feed: post to %d: %v", chatID, err)
		return 0, false, tg.IsRateLimited(err), tg.PermanentPostError(err)
	}
	tg.Pace(ctx, feedSendPause)
	return tg.MessageID(sent), true, false, false
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
func feedLanguage(tag string) i18n.Lang {
	return i18n.FromStored(tag)
}

// formatBug renders a Bugzilla bug for the feed behind the default open marker (🐞).
func formatBug(b recentBug, l i18n.Lang) string {
	return formatBugMarked(b, l, "🐞")
}

// formatBugMarked renders a Bugzilla bug for the feed behind the given leading marker (🐞 open,
// ✅/❌ resolved — passed in rather than string-replaced, so a 🐞 inside a summary can't be hit and
// every configured feed locale uses its catalogue field labels.
func formatBugMarked(b recentBug, l i18n.Lang, marker string) string {
	labels := i18n.Messages.Feed.Bug
	sep := labels.FieldSeparator.For(l)
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

	status := lookup.TranslateBugValue(l, b.Status)
	if b.Resolution != "" {
		status += labels.StatusResolutionSeparator.For(l) + lookup.TranslateBugValue(l, b.Resolution)
	}
	line(labels.Status.For(l), status)

	comp := b.Product
	if b.Component != "" {
		comp += " › " + b.Component
	}
	line(labels.ProductComponent.For(l), comp)
	line(labels.Priority.For(l), lookup.TranslateBugValue(l, b.Priority))
	line(labels.Severity.For(l), lookup.TranslateBugValue(l, b.Severity))
	if len(b.Keywords) > 0 {
		line(labels.Keywords.For(l), capRunes(strings.Join(b.Keywords, ", "), 400))
	}
	if atoms := flattenAtoms(b.Atoms); atoms != "" {
		line(labels.Packages.For(l), atoms)
	}

	if a := b.AssignedTo.link("assigned_to"); a != "" {
		fmt.Fprintf(&sb, "\n<b>%s</b>%s%s", labels.Assignee.For(l), sep, a)
	}
	if c := b.Creator.link("reporter"); c != "" {
		fmt.Fprintf(&sb, "\n<b>%s</b>%s%s", labels.Reporter.For(l), sep, c)
	}
	if d := dateOnly(b.CreationTime); d != "" {
		line(labels.CreationDate.For(l), d)
	}
	return sb.String()
}

// resolvedMark is the marker for a CLOSED bug: ✅ only when it was actually FIXED, otherwise ❌ — a
// bug closed as INVALID / WONTFIX / DUPLICATE / WORKSFORME / OBSOLETE / … was NOT fixed, so a
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
func formatBugResolved(b recentBug, l i18n.Lang) string {
	return formatBugMarked(b, l, resolvedMark(b))
}

// formatNewBug renders a freshly-seen bug for the feed and whether to post it silently. A bug that
// is ALREADY resolved the first time the feed sees it (filed and closed within one poll cycle — e.g.
// resolved INVALID) gets the resolved marker (✅ fixed / ❌ not), not 🐞, and is posted silently: it
// is not an actionable new open bug, so it shouldn't look open or ping. An open bug keeps 🐞 and the
// status-aware silence.
func formatNewBug(b recentBug, l i18n.Lang, baseSilent bool) (text string, silent bool) {
	if bugResolved(b) {
		return formatBugResolved(b, l), true
	}
	return formatBug(b, l), baseSilent
}

// refreshTracked edits the feed message of any tracked bug whose displayed state changed since it
// was last rendered — a status transition (UNCONFIRMED -> CONFIRMED/IN_PROGRESS), a resolution
// (🐞 -> ✅/❌), or a reopen/re-resolution. Runs per feed, in the feed's own language.
//
// Bounded + best-effort: at most maxEditsPerCycle edits per call (a large backlog drains over
// several cycles instead of bursting past Telegram's per-chat edit limit), each paced by
// feedSendPause; a 429 stops the cycle early and retries next time. A bug that vanishes from the
// refetch for maxTrackMisses cycles, repeatedly receives a deterministic Telegram 400, or gets a
// known permanent edit error is dropped so it cannot wedge a tracking slot. Transport, context,
// and 5xx failures never age tracking out.
func refreshTracked(ctx context.Context, bot feedBot, f *config.FeedConfig, l i18n.Lang, st *feedState, byID map[int]recentBug, fetchOK bool) {
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
		text := formatBug(b, l)
		if bugResolved(b) {
			text = formatBugResolved(b, l) // 🐞 -> ✅/❌
		}
		edit := tg.HTMLMessage(f.ChatID, text)
		opCtx, cancel := context.WithTimeout(ctx, feedTelegramTimeout)
		_, eerr := bot.EditMessageText(opCtx, &telego.EditMessageTextParams{
			ChatID:             tu.ID(f.ChatID),
			MessageID:          tb.MsgID,
			Text:               edit.Text,
			ParseMode:          edit.ParseMode,
			LinkPreviewOptions: edit.LinkPreviewOptions,
		})
		cancel()
		edits++
		switch {
		case eerr == nil || tg.IsNotModified(eerr): // edited (or already current) — sync our state
			tb.EditFails = 0
			if wasUnconfirmed && !bugResolved(b) && !strings.EqualFold(b.Status, "UNCONFIRMED") && !bugSilent(f, b) {
				// The silent UNCONFIRMED post moved OUT of UNCONFIRMED (but not straight to resolved) —
				// owe the non-silent notice the silent original never gave. The edit already landed.
				// Abandon a permanently rejected notice immediately; retry other failures over a
				// bounded number of cycles so an outage cannot pin this bug into an endless loop.
				if _, ok, rl, permanent := postFeed(ctx, bot, f.ChatID, confirmNotice(b, l), false, tb.MsgID); ok {
					tb.ConfirmTries = 0
					tb.State = cur
				} else if permanent {
					log.Printf("feed: skip permanently rejected confirm ping for bug %d in %d", id, f.ChatID)
					tb.ConfirmTries = 0
					tb.State = cur
				} else {
					tb.ConfirmTries++
					if tb.ConfirmTries >= maxConfirmTries {
						log.Printf("feed: giving up confirm ping for bug %d in %d after %d tries", id, f.ChatID, tb.ConfirmTries)
						tb.State = cur // abandon the ping; advance so the bug isn't re-edited forever
					} else if rl {
						break refresh // rate-limited send: retry next cycle, stop hammering (state un-advanced, the re-edit is a harmless no-op)
					}
					// else (transient non-429, under budget): leave state un-advanced to retry next cycle
				}
			} else {
				// Resolved bugs are KEPT (not deleted) so a later reopen/re-resolution is detected;
				// evictOne ages them out under the cap.
				tb.State = cur
			}
		case tg.IsRateLimited(eerr):
			log.Printf("feed: edit tracked bug %d in %d rate-limited (%v) — pausing edits this cycle", id, f.ChatID, eerr)
			break refresh
		case tg.PermanentEditError(eerr):
			log.Printf("feed: drop tracked bug %d in %d (uneditable): %v", id, f.ChatID, eerr)
			delete(st.Tracked, idStr)
		case tg.CountablePermanentEditError(eerr):
			tb.EditFails++
			log.Printf("feed: edit tracked bug %d in %d (deterministic 400 %d/%d): %v", id, f.ChatID, tb.EditFails, maxEditFails, eerr)
			if tb.EditFails >= maxEditFails {
				log.Printf("feed: drop tracked bug %d in %d after %d deterministic edit rejections", id, f.ChatID, maxEditFails)
				delete(st.Tracked, idStr)
			}
		default:
			tb.EditFails = 0
			log.Printf("feed: edit tracked bug %d in %d (transient, tracking retained): %v", id, f.ChatID, eerr)
		}
		if !tg.Pace(ctx, feedSendPause) {
			return // shutdown mid-refresh: stop editing; pollAll still persists the advanced cursor
		}
	}
}

func formatNews(_ i18n.Lang, n lookup.NewsItem) string {
	const prefix = "📰 "
	label := n.Date + " — " + html.UnescapeString(n.Title)
	label = tg.CapText(label, tg.MessageLimit-tg.TextUnits(prefix))
	return fmt.Sprintf("%s<a href=\"%s\">%s</a>", prefix,
		html.EscapeString(n.URL), html.EscapeString(label))
}

// confirmNotice is the brief, non-silent message sent when a previously-UNCONFIRMED bug (which was
// posted silently) leaves UNCONFIRMED — the notification the silent original never produced. It
// names the bug's ACTUAL new status (CONFIRMED, IN_PROGRESS, …), localized by lookup.TranslateBugValue, rather than
// always "confirmed", since the trigger is any move out of UNCONFIRMED. 🔔 (not ✅, which marks
// resolution) signals a live status update; rendered in the feed's own language.
func confirmNotice(b recentBug, l i18n.Lang) string {
	status := lookup.TranslateBugValue(l, b.Status)
	return fmt.Sprintf("🔔 <a href=\"https://bugs.gentoo.org/%d\"><b>Bug %d</b></a> → %s\n%s",
		b.ID, b.ID, html.EscapeString(status), html.EscapeString(capRunes(b.Summary, 600)))
}

// bugSilent reports whether a feed bug should be posted WITHOUT a notification:
// UNCONFIRMED bugs are silent (a fresh report may be a false alarm); confirmed ones
// notify. silent_bugs=true forces every bug silent regardless of status.
func bugSilent(f *config.FeedConfig, b recentBug) bool {
	if f.SilentBugs != nil && *f.SilentBugs {
		return true
	}
	return strings.EqualFold(b.Status, "UNCONFIRMED")
}

// matchesBug reports whether a bug passes this feed's optional product/component filter.
func matchesBug(f *config.FeedConfig, b recentBug) bool {
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
func postFeedItems(ctx context.Context, bot feedBot, f *config.FeedConfig, l i18n.Lang, st *feedState, bugs []recentBug, news []lookup.NewsItem) {
	if f.BugsOn() && len(bugs) > 0 {
		if st.LastBugID == 0 {
			latest := bugs[0].ID
			for _, b := range bugs[1:] {
				if b.ID > latest {
					latest = b.ID
				}
			}
			st.LastBugID = latest // first run: record a baseline, don't backfill history
			log.Printf("feed: %d baselining bug cursor at #%d (no prior bug state — first run or reset)", f.ChatID, latest)
		} else {
			sort.Slice(bugs, func(i, j int) bool { return bugs[i].ID < bugs[j].ID })
			processed := 0
			for _, b := range bugs {
				if b.ID <= st.LastBugID {
					continue
				}
				if processed >= maxBugCatchUpPerCycle {
					break // the cursor stays on this contiguous prefix; the remainder carries over
				}
				if !matchesBug(f, b) {
					st.LastBugID = b.ID // intentionally filtered, so this item is fully processed
					processed++
					continue
				}
				text, silent := formatNewBug(b, l, bugSilent(f, b))
				mid, ok, _, permanent := postFeed(ctx, bot, f.ChatID, text, silent, 0)
				if !ok {
					if permanent {
						log.Printf("feed: skip permanently rejected bug %d in %d", b.ID, f.ChatID)
						st.LastBugID = b.ID
						processed++
						continue
					}
					break // do not advance across an undelivered bug
				}
				st.LastBugID = b.ID
				st.trackBug(b, mid)
				processed++
			}
		}
	}
	if f.NewsOn() && len(news) > 0 {
		if st.LastNewsURL == "" {
			st.LastNewsURL = news[0].URL // first run: baseline only
			log.Printf("feed: %d baselining news cursor (no prior news state — first run or reset)", f.ChatID)
		} else {
			found := false
			var nn []lookup.NewsItem
			for _, n := range news {
				if n.URL == st.LastNewsURL {
					found = true
					break
				}
				nn = append(nn, n)
			}
			if !found {
				// The positional cursor cannot safely distinguish a changed index from an expired
				// window. Re-baseline rather than broadcasting the archive; a durable seen set is
				// intentionally left for a persisted-state redesign.
				log.Printf("feed: WARNING %d: news cursor %s not on the fetched page — re-baselining (any items newer than it are skipped, not re-posted)", f.ChatID, st.LastNewsURL)
				st.LastNewsURL = news[0].URL
				nn = nil
			}
			for i := len(nn) - 1; i >= 0; i-- { // oldest first
				_, ok, _, permanent := postFeed(ctx, bot, f.ChatID, formatNews(l, nn[i]), false, 0)
				if !ok {
					if permanent {
						log.Printf("feed: skip permanently rejected news item %s in %d", nn[i].URL, f.ChatID)
						st.LastNewsURL = nn[i].URL
						continue
					}
					break
				}
				st.LastNewsURL = nn[i].URL
			}
		}
	}
}

// feedSources keeps poll orchestration testable without replacing process-wide network globals.
type feedSources struct {
	recent  func(context.Context, int) ([]recentBug, bool)
	news    func(context.Context) ([]lookup.NewsItem, error)
	tracked func(context.Context, []int) ([]recentBug, bool)
}

var defaultFeedSources = feedSources{
	recent:  fetchRecentBugs,
	news:    lookup.FetchNews,
	tracked: fetchBugsByID,
}

// pollAll processes the feeds due at now. Destinations sharing a bug cursor reuse one bounded
// upstream slice; different cursors get independent slices so neither catch-up nor baselining skips.
// News gets its own deadline, and fetchBugsByID gives each tracked chunk its own.
func pollAll(ctx context.Context, bot *telego.Bot, feeds []*config.FeedConfig, states map[int64]*feedState, stateDir string, now time.Time, nextDue map[int64]time.Time) {
	pollAllWithSources(ctx, bot, feeds, states, stateDir, now, nextDue, defaultFeedSources)
}

func pollAllWithSources(ctx context.Context, bot feedBot, feeds []*config.FeedConfig, states map[int64]*feedState, stateDir string, now time.Time, nextDue map[int64]time.Time, sources feedSources) {
	var due []*config.FeedConfig
	for _, f := range feeds {
		if !now.Before(nextDue[f.ChatID]) {
			due = append(due, f)
		}
	}
	if len(due) == 0 {
		return
	}

	needNews := false
	bugCursorSet := map[int]bool{}
	for _, f := range due {
		needNews = needNews || f.NewsOn()
		if f.BugsOn() {
			bugCursorSet[states[f.ChatID].LastBugID] = true
		}
	}

	trackedSet := map[int]bool{}
	for _, f := range due {
		for k := range states[f.ChatID].Tracked {
			if id, err := strconv.Atoi(k); err == nil {
				trackedSet[id] = true
			}
		}
	}

	bugsByCursor := make(map[int][]recentBug, len(bugCursorSet))
	bugCursors := make([]int, 0, len(bugCursorSet))
	for cursor := range bugCursorSet {
		bugCursors = append(bugCursors, cursor)
	}
	sort.Ints(bugCursors)
	for _, cursor := range bugCursors {
		fctx, cancel := context.WithTimeout(ctx, feedFetchTimeout)
		bugs, ok := sources.recent(fctx, cursor)
		cancel()
		if ok {
			bugsByCursor[cursor] = bugs
		}
	}

	var news []lookup.NewsItem
	if needNews {
		fctx, cancel := context.WithTimeout(ctx, feedFetchTimeout)
		var err error
		news, err = sources.news(fctx)
		cancel()
		if err != nil {
			log.Printf("feed: news fetch: %v", err)
			news = nil
		}
	}

	var byID map[int]recentBug
	fetchOK := false
	if len(trackedSet) > 0 {
		ids := make([]int, 0, len(trackedSet))
		for id := range trackedSet {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		byID = map[int]recentBug{}
		fetched, ok := sources.tracked(ctx, ids)
		for _, b := range fetched {
			byID[b.ID] = b
		}
		fetchOK = ok
	}

	for _, f := range due {
		l := feedLanguage(f.Lang)
		st := states[f.ChatID]
		cursor := st.LastBugID
		postFeedItems(ctx, bot, f, l, st, bugsByCursor[cursor], news)
		if len(st.Tracked) > 0 {
			refreshTracked(ctx, bot, f, l, st, byID, fetchOK)
		}
		saveFeedState(feedStatePath(stateDir, f.ChatID), *st)
		nextDue[f.ChatID] = now.Add(f.Interval())
	}
}

// probeFeedPerms checks, at startup, that the bot can actually post in each feed's target chat,
// so a misconfigured chat_id or a missing admin/post right is logged loudly here instead of
// surfacing only when the first send/edit fails. Best-effort: any probe error is logged, never
// fatal, and never blocks the feed loop.
func probeFeedPerms(ctx context.Context, bot *telego.Bot, feeds []*config.FeedConfig) {
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

// Run polls every configured feed until ctx is canceled, then flushes all feed state.
func (s *Service) Run(ctx context.Context) {
	tick := s.feeds[0].Interval()
	for _, f := range s.feeds {
		if d := f.Interval(); d < tick {
			tick = d
		}
	}
	states := map[int64]*feedState{}
	for _, f := range s.feeds {
		st := loadFeedState(feedStatePath(s.stateDir, f.ChatID))
		states[f.ChatID] = &st
	}
	nextDue := map[int64]time.Time{} // zero => every feed is due on the first poll
	safePoll := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("feed: poll panicked (recovered, feeds continue): %v", r)
			}
		}()
		pollAll(ctx, s.bot, s.feeds, states, s.stateDir, time.Now(), nextDue)
	}
	log.Printf("feed: %d destination(s), tick %s, per-feed interval honoured (shared fetch)", len(s.feeds), tick)
	probeFeedPerms(ctx, s.bot, s.feeds)
	safePoll()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Final flush so the latest cursor/tracking is persisted before exit (best-effort;
			// store.Write is atomic and fsynced). Each cycle already saves, so this only captures
			// state changed since the last save.
			for _, f := range s.feeds {
				saveFeedState(feedStatePath(s.stateDir, f.ChatID), *states[f.ChatID])
			}
			return
		case <-t.C:
			safePoll()
		}
	}
}
