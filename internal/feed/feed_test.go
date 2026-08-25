package feed

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/lookup"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

func TestFeedLanguageResolution(t *testing.T) {
	if got := feedLanguage("zh-Hant"); got != i18n.LangZHHant {
		t.Fatalf("feed language = %s, want zh-Hant", got)
	}
	if got := feedLanguage(""); got != i18n.LangZH {
		t.Fatalf("default feed language = %s, want zh", got)
	}
}

func TestFormatBugCatalogueLabels(t *testing.T) {
	bug := recentBug{
		ID:           42,
		Summary:      "summary",
		Status:       "RESOLVED",
		Resolution:   "FIXED",
		Product:      "Gentoo Linux",
		Component:    "Current packages",
		Priority:     "P1",
		Severity:     "normal",
		Keywords:     []string{"STABLEREQ"},
		CreationTime: "2026-08-24T12:00:00Z",
		Atoms:        "app-misc/example",
		AssignedTo:   bugUser{RealName: "Assignee"},
		Creator:      bugUser{RealName: "Reporter"},
	}

	for _, lang := range i18n.Languages() {
		t.Run(lang.String(), func(t *testing.T) {
			got := formatBug(bug, lang)
			labels := i18n.Messages.Feed.Bug
			for _, label := range []i18n.Text{
				labels.Status,
				labels.ProductComponent,
				labels.Priority,
				labels.Severity,
				labels.Keywords,
				labels.Packages,
				labels.Assignee,
				labels.Reporter,
				labels.CreationDate,
			} {
				want := "<b>" + label.For(lang) + "</b>" + labels.FieldSeparator.For(lang)
				if !strings.Contains(got, want) {
					t.Errorf("formatted bug does not contain catalogue field %q: %q", want, got)
				}
			}
			wantStatus := lookup.TranslateBugValue(lang, bug.Status) +
				labels.StatusResolutionSeparator.For(lang) +
				lookup.TranslateBugValue(lang, bug.Resolution)
			if !strings.Contains(got, wantStatus) {
				t.Errorf("formatted bug status = %q, want substring %q", got, wantStatus)
			}
		})
	}
}

// TestBugSilent verifies status-aware notifications: UNCONFIRMED bugs post silently (a
// fresh report may be a false alarm), confirmed bugs notify, and silent_bugs=true forces
// every bug silent.
func TestBugSilent(t *testing.T) {
	f := &config.FeedConfig{}
	for _, c := range []struct {
		status string
		want   bool // want silent
	}{
		{"UNCONFIRMED", true},
		{"CONFIRMED", false},
		{"IN_PROGRESS", false},
		{"RESOLVED", false},
		{"VERIFIED", false},
	} {
		if got := bugSilent(f, recentBug{Status: c.status}); got != c.want {
			t.Errorf("bugSilent(%s) = %v, want %v", c.status, got, c.want)
		}
	}
	forced := true
	if !bugSilent(&config.FeedConfig{SilentBugs: &forced}, recentBug{Status: "CONFIRMED"}) {
		t.Errorf("silent_bugs=true should force a CONFIRMED bug silent")
	}
}

// TestFormatNewBug guards the born-resolved case: a bug already resolved the first time the feed
// sees it (filed + closed within one poll, e.g. RESOLVED/INVALID) must render ✅ (not 🐞) and be
// posted silently; an open bug keeps 🐞 and the caller's status-aware silence.
func TestFormatNewBug(t *testing.T) {
	text, silent := formatNewBug(recentBug{ID: 1, Summary: "x", Status: "CONFIRMED"}, feedLanguage("en"), false)
	if !strings.Contains(text, "🐞") || silent {
		t.Errorf("an open new bug should be 🐞 and not forced silent (silent=%v)", silent)
	}
	text, silent = formatNewBug(recentBug{ID: 2, Summary: "x", Status: "RESOLVED", Resolution: "INVALID"}, feedLanguage("en"), false)
	if strings.Contains(text, "🐞") || !strings.Contains(text, "❌") || !silent {
		t.Errorf("a born-resolved INVALID (误报) bug should be ❌ and silent (silent=%v)", silent)
	}
	text, silent = formatNewBug(recentBug{ID: 3, Summary: "x", Status: "RESOLVED", Resolution: "FIXED"}, feedLanguage("en"), false)
	if !strings.Contains(text, "✅") || strings.Contains(text, "🐞") || !silent {
		t.Errorf("a born-resolved FIXED bug should be ✅ and silent (silent=%v)", silent)
	}
}

// TestResolvedMark: only an actually-FIXED bug gets the fixed marker; every other closure
// (INVALID, WONTFIX, DUPLICATE, WORKSFORME, …) gets the not-fixed one, case-insensitively.
func TestResolvedMark(t *testing.T) {
	if resolvedMark(recentBug{Resolution: "FIXED"}) != "✅" || resolvedMark(recentBug{Resolution: "fixed"}) != "✅" {
		t.Error("FIXED (any case) should be ✅")
	}
	for _, r := range []string{"INVALID", "WONTFIX", "DUPLICATE", "WORKSFORME", "OBSOLETE", ""} {
		if got := resolvedMark(recentBug{Resolution: r}); got != "❌" {
			t.Errorf("resolution %q should be ❌, got %s", r, got)
		}
	}
}

// TestNewsCursorMonotonic protects postFeedItems' news dedup cursor: a missing cursor re-baselines
// without replay, a present cursor sends only newer items oldest-first, and the newest cursor sends
// nothing. Removing the production cursor guard changes the fake's sent items or final cursor.
func TestNewsCursorMonotonic(t *testing.T) {
	oldPause := feedSendPause
	feedSendPause = 0
	t.Cleanup(func() { feedSendPause = oldPause })
	news := []lookup.NewsItem{
		{URL: "https://example.test/u5", Title: "u5", Date: "2026-05-05"},
		{URL: "https://example.test/u4", Title: "u4", Date: "2026-05-04"},
		{URL: "https://example.test/u3", Title: "u3", Date: "2026-05-03"},
		{URL: "https://example.test/u2", Title: "u2", Date: "2026-05-02"},
		{URL: "https://example.test/u1", Title: "u1", Date: "2026-05-01"},
	}
	newsOn, bugsOff := true, false
	feed := &config.FeedConfig{ChatID: -100, Lang: "en", News: &newsOn, Bugs: &bugsOff}
	tests := []struct {
		name       string
		cursor     string
		wantCursor string
		wantURLs   []string
	}{
		{name: "cursor missing", cursor: "https://example.test/gone", wantCursor: news[0].URL},
		{name: "cursor in window", cursor: news[2].URL, wantCursor: news[0].URL, wantURLs: []string{news[1].URL, news[0].URL}},
		{name: "cursor already newest", cursor: news[0].URL, wantCursor: news[0].URL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &feedState{LastNewsURL: tt.cursor}
			fake := &fakeFeedBot{}
			postFeedItems(context.Background(), newAPITestBot(t, fake), feed, feedLanguage((feed).Lang), state, nil, news)

			if state.LastNewsURL != tt.wantCursor {
				t.Errorf("LastNewsURL = %q, want %q", state.LastNewsURL, tt.wantCursor)
			}
			if len(fake.sentText) != len(tt.wantURLs) {
				t.Fatalf("sent items = %d, want %d", len(fake.sentText), len(tt.wantURLs))
			}
			for i, wantURL := range tt.wantURLs {
				if !strings.Contains(fake.sentText[i], wantURL) {
					t.Errorf("sent item %d = %q, want URL %q", i, fake.sentText[i], wantURL)
				}
			}
		})
	}
}

// TestBugCursorForwardOnly protects postFeedItems' bug cursor against regression and verifies the
// forward path sends every newer matching bug oldest-first. Removing the guarded cursor advance
// changes LastBugID or causes the shared fake to resend an old item.
func TestBugCursorForwardOnly(t *testing.T) {
	oldPause := feedSendPause
	feedSendPause = 0
	t.Cleanup(func() { feedSendPause = oldPause })
	bugsOn, newsOff := true, false
	feed := &config.FeedConfig{ChatID: -100, Lang: "en", Bugs: &bugsOn, News: &newsOff}
	tests := []struct {
		name       string
		bugs       []recentBug
		wantCursor int
		wantIDs    []int
	}{
		{
			name: "lower fetched IDs do not regress",
			bugs: []recentBug{
				{ID: 98, Summary: "older-98", Status: "CONFIRMED"},
				{ID: 97, Summary: "older-97", Status: "CONFIRMED"},
			},
			wantCursor: 100,
		},
		{
			name: "new bugs advance after delivery",
			bugs: []recentBug{
				{ID: 105, Summary: "new-105", Status: "CONFIRMED"},
				{ID: 104, Summary: "new-104", Status: "CONFIRMED"},
				{ID: 99, Summary: "older-99", Status: "CONFIRMED"},
			},
			wantCursor: 105,
			wantIDs:    []int{104, 105},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &feedState{LastBugID: 100}
			fake := &fakeFeedBot{}
			postFeedItems(context.Background(), newAPITestBot(t, fake), feed, feedLanguage((feed).Lang), state, tt.bugs, nil)

			if state.LastBugID != tt.wantCursor {
				t.Errorf("LastBugID = %d, want %d", state.LastBugID, tt.wantCursor)
			}
			if len(fake.sentText) != len(tt.wantIDs) {
				t.Fatalf("sent items = %d, want %d", len(fake.sentText), len(tt.wantIDs))
			}
			for i, wantID := range tt.wantIDs {
				wantURL := "https://bugs.gentoo.org/" + strconv.Itoa(wantID)
				if !strings.Contains(fake.sentText[i], wantURL) {
					t.Errorf("sent item %d = %q, want bug %d", i, fake.sentText[i], wantID)
				}
			}
		})
	}
}

// TestBugTracking covers tracking: open AND born-resolved bugs are tracked (so a later reopen can
// re-render), a bug with no msg id is not, and the cap evicts a RESOLVED bug before any open one.
func TestBugTracking(t *testing.T) {
	if bugResolved(recentBug{Status: "CONFIRMED"}) {
		t.Error("open bug (no resolution) should not be 'resolved'")
	}
	if !bugResolved(recentBug{Status: "RESOLVED", Resolution: "FIXED"}) {
		t.Error("bug with a resolution should be 'resolved'")
	}

	var st feedState
	st.trackBug(recentBug{ID: 100, Status: "CONFIRMED"}, 5001)                     // open -> tracked
	st.trackBug(recentBug{ID: 101, Status: "RESOLVED", Resolution: "FIXED"}, 5002) // born-resolved -> ALSO tracked (for a later reopen)
	st.trackBug(recentBug{ID: 102, Status: "CONFIRMED"}, 0)                        // no msg id -> not tracked
	if len(st.Tracked) != 2 || st.Tracked["100"].MsgID != 5001 || st.Tracked["101"].MsgID != 5002 {
		t.Fatalf("tracked = %+v, want bugs 100 and 101 tracked", st.Tracked)
	}
	if st.Tracked["102"] != nil {
		t.Error("a bug with msg id 0 must not be tracked")
	}

	// resolved-first eviction: fill to exactly the cap, then one more add must evict the RESOLVED
	// bug (101) and keep the older OPEN bug (100) — a long-lived open bug shouldn't be lost first.
	for i := 0; i < maxTracked-2; i++ {
		st.trackBug(recentBug{ID: 3000 + i, Status: "CONFIRMED"}, 7000+i)
	}
	st.trackBug(recentBug{ID: 9999, Status: "CONFIRMED"}, 8000) // forces a single eviction
	if len(st.Tracked) > maxTracked {
		t.Errorf("tracked grew to %d, want <= %d", len(st.Tracked), maxTracked)
	}
	if st.Tracked["101"] != nil {
		t.Error("resolved-first eviction: the resolved bug (101) must be evicted before any open bug")
	}
	if st.Tracked["100"] == nil {
		t.Error("resolved-first eviction: the open bug (100) must survive while a resolved one remains to evict")
	}

	got := formatBugResolved(recentBug{ID: 7, Summary: "x", Status: "RESOLVED", Resolution: "FIXED"}, feedLanguage("en"))
	if !strings.HasPrefix(got, "✅") || strings.Contains(got, "🐞") {
		t.Errorf("formatBugResolved should render ✅, got prefix %q", got[:12])
	}
}

// TestResolvedState covers the persisted-state-key resolved check that drives resolved-first eviction.
func TestResolvedState(t *testing.T) {
	for _, s := range []string{"RESOLVED|FIXED", "VERIFIED|INVALID", "RESOLVED|WONTFIX"} {
		if !resolvedState(s) {
			t.Errorf("%q has a resolution -> should be resolved", s)
		}
	}
	for _, s := range []string{"CONFIRMED|", "UNCONFIRMED|", "IN_PROGRESS|", "CONFIRMED"} {
		if resolvedState(s) {
			t.Errorf("%q has no resolution -> should be open", s)
		}
	}
}

// TestBugStateKey covers the status+resolution state key that drives in-place edits, and that an
// unchanged state doesn't attempt an edit (a matching bug must be skipped, not re-rendered).
func TestBugStateKey(t *testing.T) {
	if bugStateKey(recentBug{Status: "UNCONFIRMED"}) == bugStateKey(recentBug{Status: "CONFIRMED"}) {
		t.Error("UNCONFIRMED vs CONFIRMED must have distinct state keys (so a confirm triggers an edit)")
	}
	if bugStateKey(recentBug{Status: "RESOLVED", Resolution: "FIXED"}) == bugStateKey(recentBug{Status: "RESOLVED", Resolution: "WONTFIX"}) {
		t.Error("different resolutions must have distinct state keys")
	}
	var st feedState
	b := recentBug{ID: 200, Status: "UNCONFIRMED"}
	st.trackBug(b, 7001)
	if st.Tracked["200"] == nil || st.Tracked["200"].State != bugStateKey(b) {
		t.Fatalf("trackBug should store the state key, got %+v", st.Tracked["200"])
	}
	// Unchanged state in the refresh batch => skipped before any edit (nil bot is safe only
	// because no edit is attempted); the bug stays tracked.
	refreshTracked(context.Background(), nil, &config.FeedConfig{ChatID: -1, Lang: "en"}, feedLanguage((&config.FeedConfig{ChatID: -1, Lang: "en"}).Lang), &st, map[int]recentBug{200: b}, true)
	if st.Tracked["200"] == nil {
		t.Error("an unchanged bug must stay tracked (no edit, no drop)")
	}
}

// TestCapRunesAndNilTracked covers the rune-safe truncation and the nil-tracked-entry guard
// (a hand-edited state file with a null entry must not crash refreshTracked).
func TestCapRunesAndNilTracked(t *testing.T) {
	if got := capRunes("abcdef", 4); got != "abc…" {
		t.Errorf("capRunes(abcdef,4) = %q, want abc…", got)
	}
	if got := capRunes("ab", 4); got != "ab" {
		t.Errorf("capRunes short = %q, want ab", got)
	}
	if got := capRunes(strings.Repeat("包", 10), 4); !utf8.ValidString(got) {
		t.Errorf("capRunes produced invalid UTF-8: %q", got)
	}
	st := &feedState{Tracked: map[string]*trackedBug{"100": nil}}
	refreshTracked(context.Background(), nil, &config.FeedConfig{ChatID: -1, Lang: "en"}, feedLanguage((&config.FeedConfig{ChatID: -1, Lang: "en"}).Lang), st, map[int]recentBug{100: {Status: "CONFIRMED"}}, true)
	if _, ok := st.Tracked["100"]; ok {
		t.Error("a nil tracked entry should be dropped (not panic)")
	}
}

// fakeFeedBot is a feedBot stand-in so refreshTracked / postFeed edit & send branches can be
// exercised without a real Telegram connection. editErr (when set) is returned by
// EditMessageText; every SendMessage is recorded so a confirm-ping can be asserted.
type fakeFeedBot struct {
	editErr        error
	sendErr        error
	edits          int
	sends          int
	sentText       []string
	sentSilent     []bool
	sentReplyTo    []int
	sentReplyAllow []bool
}

func (b *fakeFeedBot) EditMessageText(_ context.Context, _ *telego.EditMessageTextParams) (*telego.Message, error) {
	b.edits++
	if b.editErr != nil {
		return nil, b.editErr
	}
	return &telego.Message{MessageID: 1}, nil
}

func (b *fakeFeedBot) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	b.sends++
	b.sentText = append(b.sentText, p.Text)
	b.sentSilent = append(b.sentSilent, p.DisableNotification)
	rt, allowWithout := 0, false
	if p.ReplyParameters != nil {
		rt = p.ReplyParameters.MessageID
		allowWithout = p.ReplyParameters.AllowSendingWithoutReply
	}
	b.sentReplyTo = append(b.sentReplyTo, rt)
	b.sentReplyAllow = append(b.sentReplyAllow, allowWithout)
	if b.sendErr != nil {
		return nil, b.sendErr
	}
	return &telego.Message{MessageID: 100 + b.sends}, nil
}

func fakeTelegramResponse(value any, err error) (*ta.Response, error) {
	if err != nil {
		return nil, err
	}
	if value == nil {
		return &ta.Response{Ok: true}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &ta.Response{Ok: true, Result: raw}, nil
}

func fakeSendMessageParams(raw []byte) (*telego.SendMessageParams, error) {
	var wire struct {
		ChatID              int64                   `json:"chat_id"`
		Text                string                  `json:"text"`
		ParseMode           string                  `json:"parse_mode"`
		DisableNotification bool                    `json:"disable_notification"`
		ReplyParameters     *telego.ReplyParameters `json:"reply_parameters"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	return &telego.SendMessageParams{
		ChatID:              telego.ChatID{ID: wire.ChatID},
		Text:                wire.Text,
		ParseMode:           wire.ParseMode,
		DisableNotification: wire.DisableNotification,
		ReplyParameters:     wire.ReplyParameters,
	}, nil
}

func newAPITestBot(t *testing.T, caller ta.Caller) *telego.Bot {
	t.Helper()
	bot, err := telego.NewBot("1:"+strings.Repeat("a", 35), telego.WithAPICaller(caller), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return bot
}

// Call adapts fakeFeedBot to telego's transport hook so postFeedItems exercises real bot request encoding.
func (b *fakeFeedBot) Call(ctx context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
	method := url[strings.LastIndexByte(url, '/')+1:]
	switch method {
	case "sendMessage":
		p, err := fakeSendMessageParams(data.BodyRaw)
		if err != nil {
			return nil, err
		}
		msg, err := b.SendMessage(ctx, p)
		return fakeTelegramResponse(msg, err)
	case "editMessageText":
		var p telego.EditMessageTextParams
		if err := json.Unmarshal(data.BodyRaw, &p); err != nil {
			return nil, err
		}
		msg, err := b.EditMessageText(ctx, &p)
		return fakeTelegramResponse(msg, err)
	default:
		return nil, errors.New("unexpected Telegram method " + method)
	}
}

// TestRefreshTrackedEditBranches drives the real EditMessageText result branches via a fake:
// a successful edit syncs state and keeps tracking; "message is not modified" counts as success;
// a known permanent error drops the bug; transient errors stay uncounted; and an otherwise
// unclassified permanent 400 increments the bounded failure counter.
func TestRefreshTrackedEditBranches(t *testing.T) {
	feedSendPause = 0 // never sleep in tests
	f := &config.FeedConfig{ChatID: -100, Lang: "en"}
	track := func(state string) *feedState {
		return &feedState{Tracked: map[string]*trackedBug{"500": {MsgID: 42, State: state}}}
	}

	t.Run("success syncs state and keeps tracking", func(t *testing.T) {
		st := track("CONFIRMED|") // non-UNCONFIRMED origin: isolates edit-success without a confirm ping
		fb := &fakeFeedBot{}
		b := recentBug{ID: 500, Status: "IN_PROGRESS"}
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, map[int]recentBug{500: b}, true)
		if fb.edits != 1 {
			t.Fatalf("want 1 edit, got %d", fb.edits)
		}
		if tb := st.Tracked["500"]; tb == nil || tb.State != bugStateKey(b) {
			t.Errorf("state not synced after a successful edit: %+v", tb)
		}
		if fb.sends != 0 {
			t.Errorf("a non-UNCONFIRMED-origin transition must not ping, got %d sends", fb.sends)
		}
	})

	t.Run("not-modified is treated as success", func(t *testing.T) {
		st := track("CONFIRMED|")
		fb := &fakeFeedBot{editErr: errors.New("Bad Request: message is not modified")}
		b := recentBug{ID: 500, Status: "IN_PROGRESS"}
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, map[int]recentBug{500: b}, true)
		if tb := st.Tracked["500"]; tb == nil || tb.State != bugStateKey(b) {
			t.Errorf("not-modified should sync state and keep tracking: %+v", tb)
		}
	})

	t.Run("permanent error drops the bug", func(t *testing.T) {
		st := track("UNCONFIRMED|")
		fb := &fakeFeedBot{editErr: errors.New("Bad Request: message to edit not found")}
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, map[int]recentBug{500: {ID: 500, Status: "IN_PROGRESS"}}, true)
		if _, ok := st.Tracked["500"]; ok {
			t.Error("a permanent edit error should drop the bug from tracking")
		}
	})

	t.Run("non-rate-limit transient keeps tracking, old state, and no failure count", func(t *testing.T) {
		st := track("UNCONFIRMED|")
		fb := &fakeFeedBot{editErr: errors.New("Bad Gateway")}
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, map[int]recentBug{500: {ID: 500, Status: "IN_PROGRESS"}}, true)
		tb := st.Tracked["500"]
		if tb == nil {
			t.Fatal("a transient edit error must keep the bug tracked for retry")
		}
		if tb.State != "UNCONFIRMED|" {
			t.Errorf("a transient error must NOT advance the stored state, got %q", tb.State)
		}
		if tb.EditFails != 0 {
			t.Errorf("a transient failure must not count toward permanent edit failures, got %d", tb.EditFails)
		}
	})

	t.Run("unclassified permanent 400 counts one failure", func(t *testing.T) {
		st := track("UNCONFIRMED|")
		fb := &fakeFeedBot{editErr: errors.New("Bad Request: can't parse entities")}
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, map[int]recentBug{500: {ID: 500, Status: "IN_PROGRESS"}}, true)
		tb := st.Tracked["500"]
		if tb == nil {
			t.Fatal("an unclassified permanent 400 should be retried up to the failure limit")
		}
		if tb.State != "UNCONFIRMED|" {
			t.Errorf("a rejected edit must NOT advance the stored state, got %q", tb.State)
		}
		if tb.EditFails != 1 {
			t.Errorf("a permanent 400 should count one edit failure, got %d", tb.EditFails)
		}
	})

	t.Run("resolved bug is edited and KEPT tracked for a later reopen", func(t *testing.T) {
		st := track("CONFIRMED|")
		fb := &fakeFeedBot{}
		b := recentBug{ID: 500, Status: "RESOLVED", Resolution: "FIXED"}
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, map[int]recentBug{500: b}, true)
		if fb.edits != 1 {
			t.Fatalf("want 1 edit for the resolution, got %d", fb.edits)
		}
		tb := st.Tracked["500"]
		if tb == nil || tb.State != bugStateKey(b) {
			t.Errorf("a resolved bug must be KEPT tracked with its resolved state (for a later reopen): %+v", tb)
		}
	})
}

// TestRefreshTrackedRateLimitStops: a 429 stops the cycle after the first attempt (rather than
// hammering); the unattempted bugs keep their old state and 0 EditFails (retried next cycle).
func TestRefreshTrackedRateLimitStops(t *testing.T) {
	feedSendPause = 0
	f := &config.FeedConfig{ChatID: -100, Lang: "en"}
	st := &feedState{Tracked: map[string]*trackedBug{
		"800": {MsgID: 1, State: "CONFIRMED|"},
		"801": {MsgID: 2, State: "CONFIRMED|"},
	}}
	fb := &fakeFeedBot{editErr: errors.New("Too Many Requests: retry after 30")}
	byID := map[int]recentBug{800: {ID: 800, Status: "IN_PROGRESS"}, 801: {ID: 801, Status: "IN_PROGRESS"}}
	refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, byID, true)
	if fb.edits != 1 {
		t.Errorf("a 429 must stop the cycle after one attempt, got %d edits", fb.edits)
	}
	for _, id := range []string{"800", "801"} {
		if tb := st.Tracked[id]; tb == nil || tb.State != "CONFIRMED|" || tb.EditFails != 0 {
			t.Errorf("bug %s should stay tracked, old state, 0 EditFails after a 429: %+v", id, tb)
		}
	}
}

// TestRefreshTrackedEditCap: when more tracked bugs changed than maxEditsPerCycle, only that many
// edits fire this call (the backlog drains over later cycles, never bursting past the rate limit).
func TestRefreshTrackedEditCap(t *testing.T) {
	feedSendPause = 0
	f := &config.FeedConfig{ChatID: -100, Lang: "en"}
	st := &feedState{Tracked: map[string]*trackedBug{}}
	byID := map[int]recentBug{}
	for i := 0; i < maxEditsPerCycle+5; i++ {
		id := 1000 + i
		st.Tracked[strconv.Itoa(id)] = &trackedBug{MsgID: id, State: "CONFIRMED|"}
		byID[id] = recentBug{ID: id, Status: "IN_PROGRESS"} // all changed (and not a ping transition)
	}
	fb := &fakeFeedBot{}
	refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, byID, true)
	if fb.edits != maxEditsPerCycle {
		t.Errorf("edit cap: want exactly %d edits, got %d", maxEditsPerCycle, fb.edits)
	}
}

// TestRefreshTrackedMissDrop: a bug absent from a non-empty refetch (vanished from Bugzilla) is
// dropped only after maxTrackMisses consecutive misses — never edited in the meantime.
func TestRefreshTrackedMissDrop(t *testing.T) {
	feedSendPause = 0
	f := &config.FeedConfig{ChatID: -100, Lang: "en"}
	st := &feedState{Tracked: map[string]*trackedBug{"900": {MsgID: 1, State: "CONFIRMED|"}}}
	fb := &fakeFeedBot{}
	other := map[int]recentBug{12345: {ID: 12345, Status: "CONFIRMED"}} // non-empty, but 900 absent
	for i := 1; i < maxTrackMisses; i++ {
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, other, true)
		if st.Tracked["900"] == nil {
			t.Fatalf("dropped too early after %d misses", i)
		}
		if st.Tracked["900"].Misses != i {
			t.Errorf("after %d cycles Misses=%d, want %d", i, st.Tracked["900"].Misses, i)
		}
	}
	refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, other, true) // maxTrackMisses-th miss
	if st.Tracked["900"] != nil {
		t.Errorf("bug 900 should be dropped after %d consecutive misses", maxTrackMisses)
	}
	if fb.edits != 0 {
		t.Errorf("a missing bug must never be edited, got %d", fb.edits)
	}
}

// TestRefreshTrackedEditFailDrop proves repeated permanent message rejections eventually release
// tracking, while the same number of transient failures never ages a live tracked bug out.
func TestRefreshTrackedEditFailDrop(t *testing.T) {
	feedSendPause = 0
	f := &config.FeedConfig{ChatID: -100, Lang: "en"}
	b := map[int]recentBug{950: {ID: 950, Status: "IN_PROGRESS"}}
	tests := []struct {
		name        string
		err         error
		wantDropped bool
		wantFails   int
	}{
		{
			name:        "permanent 400 drops at limit",
			err:         errors.New("Bad Request: can't parse entities"),
			wantDropped: true,
		},
		{
			name:      "transient failures never drop",
			err:       errors.New("Bad Gateway"),
			wantFails: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &feedState{Tracked: map[string]*trackedBug{"950": {MsgID: 1, State: "CONFIRMED|"}}}
			fb := &fakeFeedBot{editErr: tt.err}
			for i := 1; i <= maxEditFails; i++ {
				refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, b, true)
				if i < maxEditFails && st.Tracked["950"] == nil {
					t.Fatalf("dropped too early after %d failures", i)
				}
			}
			tb := st.Tracked["950"]
			if (tb == nil) != tt.wantDropped {
				t.Fatalf("dropped = %v, want %v", tb == nil, tt.wantDropped)
			}
			if tb != nil && tb.EditFails != tt.wantFails {
				t.Errorf("EditFails = %d, want %d", tb.EditFails, tt.wantFails)
			}
		})
	}
}

// TestRefreshTrackedReopenReRenders: a bug already tracked as resolved (INVALID) that is reopened
// and re-resolved (FIXED) must re-edit and stay tracked — impossible before resolved bugs were kept.
func TestRefreshTrackedReopenReRenders(t *testing.T) {
	feedSendPause = 0
	f := &config.FeedConfig{ChatID: -100, Lang: "en"}
	st := &feedState{Tracked: map[string]*trackedBug{"600": {MsgID: 1, State: "RESOLVED|INVALID"}}}
	fb := &fakeFeedBot{}
	b := recentBug{ID: 600, Status: "RESOLVED", Resolution: "FIXED"}
	refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, map[int]recentBug{600: b}, true)
	if fb.edits != 1 {
		t.Fatalf("a resolution flip (INVALID->FIXED) must re-edit, got %d edits", fb.edits)
	}
	if tb := st.Tracked["600"]; tb == nil || tb.State != bugStateKey(b) {
		t.Errorf("after a flip the bug must stay tracked with the new state: %+v", tb)
	}
}

// TestLoadFeedStateCorruptBackup: a corrupt state file is renamed to .corrupt (preserved for
// inspection) rather than silently clobbered, and loads as empty.
func TestLoadFeedStateCorruptBackup(t *testing.T) {
	dir := t.TempDir()
	path := feedStatePath(dir, -42)
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := loadFeedState(path)
	if st.LastBugID != 0 || len(st.Tracked) != 0 {
		t.Errorf("a corrupt state must load as empty, got %+v", st)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("the corrupt file should be backed up to %s.corrupt: %v", path, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the corrupt file should have been renamed away from the live path")
	}
}

// TestRefreshTrackedPartialFetchNoMiss locks in finding B's fix: when the refetch was incomplete
// (fetchOK=false, a chunk failed), an absent tracked bug must NOT accrue a miss — it could have been
// in the failed chunk — so a flaky chunk can never age out a live bug.
func TestRefreshTrackedPartialFetchNoMiss(t *testing.T) {
	feedSendPause = 0
	f := &config.FeedConfig{ChatID: -100, Lang: "en"}
	st := &feedState{Tracked: map[string]*trackedBug{"900": {MsgID: 1, State: "CONFIRMED|"}}}
	fb := &fakeFeedBot{}
	other := map[int]recentBug{12345: {ID: 12345, Status: "CONFIRMED"}} // 900 absent
	for i := 0; i < maxTrackMisses+3; i++ {
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, other, false) // fetchOK=false every cycle
	}
	tb := st.Tracked["900"]
	if tb == nil {
		t.Fatal("a partial-fetch failure must NOT drop a tracked bug")
	}
	if tb.Misses != 0 {
		t.Errorf("a partial-fetch absence must not count a miss, got Misses=%d", tb.Misses)
	}
}

// TestRefreshTrackedConfirmPing covers the UNCONFIRMED->CONFIRMED confirm ping: a fresh
// non-silent notice is sent (on top of the in-place edit), silent_bugs suppresses it, and a
// transition that does not originate from UNCONFIRMED does not ping.
func TestRefreshTrackedConfirmPing(t *testing.T) {
	feedSendPause = 0
	f := &config.FeedConfig{ChatID: -100, Lang: "en"}

	t.Run("UNCONFIRMED->CONFIRMED sends one non-silent ping", func(t *testing.T) {
		st := &feedState{Tracked: map[string]*trackedBug{"700": {MsgID: 9, State: "UNCONFIRMED|"}}}
		fb := &fakeFeedBot{}
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, map[int]recentBug{700: {ID: 700, Status: "CONFIRMED", Summary: "boom"}}, true)
		if fb.edits != 1 {
			t.Fatalf("the in-place edit must still happen, got %d edits", fb.edits)
		}
		if fb.sends != 1 {
			t.Fatalf("UNCONFIRMED->CONFIRMED should ping exactly once, got %d sends", fb.sends)
		}
		if fb.sentSilent[0] {
			t.Error("the confirm ping must be NON-silent")
		}
		if !strings.Contains(fb.sentText[0], "Bug 700") {
			t.Errorf("the confirm ping should reference the bug, got %q", fb.sentText[0])
		}
		if fb.sentReplyTo[0] != 9 {
			t.Errorf("the confirm ping should reply to the original bug message (id 9), got %d", fb.sentReplyTo[0])
		}
		if !fb.sentReplyAllow[0] {
			t.Error("the confirm-ping reply should set AllowSendingWithoutReply so a deleted original doesn't block it")
		}
	})

	t.Run("silent_bugs=true suppresses the ping", func(t *testing.T) {
		forced := true
		fs := &config.FeedConfig{ChatID: -100, Lang: "en", SilentBugs: &forced}
		st := &feedState{Tracked: map[string]*trackedBug{"701": {MsgID: 9, State: "UNCONFIRMED|"}}}
		fb := &fakeFeedBot{}
		refreshTracked(context.Background(), fb, fs, feedLanguage((fs).Lang), st, map[int]recentBug{701: {ID: 701, Status: "CONFIRMED", Summary: "x"}}, true)
		if fb.edits != 1 {
			t.Fatalf("the edit must still happen under silent_bugs, got %d edits", fb.edits)
		}
		if fb.sends != 0 {
			t.Errorf("silent_bugs=true must not ping, got %d sends", fb.sends)
		}
	})

	t.Run("CONFIRMED->IN_PROGRESS does not ping", func(t *testing.T) {
		st := &feedState{Tracked: map[string]*trackedBug{"702": {MsgID: 9, State: "CONFIRMED|"}}}
		fb := &fakeFeedBot{}
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, map[int]recentBug{702: {ID: 702, Status: "IN_PROGRESS", Summary: "x"}}, true)
		if fb.edits != 1 {
			t.Fatalf("want the edit, got %d", fb.edits)
		}
		if fb.sends != 0 {
			t.Errorf("a transition not from UNCONFIRMED must not ping, got %d sends", fb.sends)
		}
	})

	t.Run("UNCONFIRMED->IN_PROGRESS pings (raced past CONFIRMED)", func(t *testing.T) {
		st := &feedState{Tracked: map[string]*trackedBug{"704": {MsgID: 9, State: "UNCONFIRMED|"}}}
		fb := &fakeFeedBot{}
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, map[int]recentBug{704: {ID: 704, Status: "IN_PROGRESS", Summary: "x"}}, true)
		if fb.edits != 1 || fb.sends != 1 {
			t.Fatalf("a bug leaving UNCONFIRMED (even straight to IN_PROGRESS) must ping once: edits=%d sends=%d", fb.edits, fb.sends)
		}
		if fb.sentSilent[0] {
			t.Error("the confirm ping must be non-silent")
		}
	})

	t.Run("a failed confirm ping does not advance state (retries next cycle)", func(t *testing.T) {
		st := &feedState{Tracked: map[string]*trackedBug{"703": {MsgID: 9, State: "UNCONFIRMED|"}}}
		fb := &fakeFeedBot{sendErr: errors.New("Too Many Requests: retry after 5")}
		refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, map[int]recentBug{703: {ID: 703, Status: "CONFIRMED", Summary: "x"}}, true)
		if fb.edits != 1 || fb.sends != 1 {
			t.Fatalf("want 1 edit + 1 attempted ping, got edits=%d sends=%d", fb.edits, fb.sends)
		}
		if tb := st.Tracked["703"]; tb == nil || tb.State != "UNCONFIRMED|" {
			t.Errorf("a failed ping must NOT advance state (so the transition retries), got %+v", tb)
		}
	})
}

// TestRefreshTrackedConfirmRetry guards the confirm-ping send-failure handling: a failed ping does
// NOT advance state (so it retries) but is bounded by ConfirmTries; a rate-limited ping stops the
// cycle without an endless re-edit; after maxConfirmTries the ping is abandoned and state advances.
func TestRefreshTrackedConfirmRetry(t *testing.T) {
	feedSendPause = 0
	f := &config.FeedConfig{ChatID: -100, Lang: "en"}
	b := map[int]recentBug{700: {ID: 700, Status: "CONFIRMED", Summary: "x"}}

	// rate-limited confirm send: edit lands, ping 429s -> state stays UNCONFIRMED|, ConfirmTries=1
	st := &feedState{Tracked: map[string]*trackedBug{"700": {MsgID: 9, State: "UNCONFIRMED|"}}}
	fb := &fakeFeedBot{sendErr: errors.New("Too Many Requests: retry after 5")}
	refreshTracked(context.Background(), fb, f, feedLanguage((f).Lang), st, b, true)
	tb := st.Tracked["700"]
	if tb == nil || tb.State != "UNCONFIRMED|" {
		t.Fatalf("a failed confirm ping must NOT advance state (so it retries), got %+v", tb)
	}
	if tb.ConfirmTries != 1 || fb.edits != 1 {
		t.Errorf("expected one edit + ConfirmTries=1, got edits=%d tries=%d", fb.edits, tb.ConfirmTries)
	}

	// at the retry budget: a further failure abandons the (best-effort) ping and advances state, so a
	// send-only outage can't pin the bug into an endless re-edit loop.
	st2 := &feedState{Tracked: map[string]*trackedBug{"700": {MsgID: 9, State: "UNCONFIRMED|", ConfirmTries: maxConfirmTries - 1}}}
	fb2 := &fakeFeedBot{sendErr: errors.New("Bad Gateway")}
	refreshTracked(context.Background(), fb2, f, feedLanguage((f).Lang), st2, b, true)
	if tb := st2.Tracked["700"]; tb == nil || tb.State != "CONFIRMED|" {
		t.Errorf("after maxConfirmTries the ping is abandoned and state advances, got %+v", tb)
	}
}

// TestConfirmNotice guards the confirm-notice wording: it names the bug's ACTUAL status
// (localized in zh, raw in en), never always "confirmed", and falls back to the raw status for an
// unmapped value.
func TestConfirmNotice(t *testing.T) {
	if got := confirmNotice(recentBug{ID: 5, Status: "IN_PROGRESS"}, feedLanguage("en")); !strings.Contains(got, "IN_PROGRESS") {
		t.Errorf("en IN_PROGRESS notice should name the status, got %q", got)
	}
	want := lookup.TranslateBugValue(i18n.LangZH, "IN_PROGRESS")
	if got := confirmNotice(recentBug{ID: 5, Status: "IN_PROGRESS"}, feedLanguage("zh")); !strings.Contains(got, want) {
		t.Errorf("zh IN_PROGRESS notice should contain catalogue status %q, got %q", want, got)
	}
	if got := confirmNotice(recentBug{ID: 5, Status: "CONFIRMED"}, feedLanguage("en")); !strings.Contains(got, "CONFIRMED") {
		t.Errorf("en CONFIRMED notice should name the status, got %q", got)
	}
	if got := confirmNotice(recentBug{ID: 5, Status: "WEIRD_STATE"}, feedLanguage("en")); !strings.Contains(got, "WEIRD_STATE") {
		t.Errorf("an unmapped status should fall back to the raw value, got %q", got)
	}
}

// TestFeedStateMigration covers the load-time upgrade of pre-v3.4.3 state: a tracked bug that
// carried only the legacy `status` is folded into the current `state` key, while an
// already-current entry is left untouched.
func TestFeedStateMigration(t *testing.T) {
	st := &feedState{Tracked: map[string]*trackedBug{
		"300": {MsgID: 5, Status: "UNCONFIRMED"}, // legacy: status only, no state
		"301": {MsgID: 6, State: "CONFIRMED|"},   // already current
		"302": nil,                               // a hand-edited null entry must not panic
	}}
	migrateFeedState(st)
	if tb := st.Tracked["300"]; tb.State != "UNCONFIRMED|" || tb.Status != "" {
		t.Errorf("legacy status not migrated to state: %+v", tb)
	}
	if tb := st.Tracked["301"]; tb.State != "CONFIRMED|" {
		t.Errorf("an already-current entry must be left intact: %+v", tb)
	}
}

// TestFeedStateRoundTrip proves feed cursors and tracked message state survive a save/load cycle
// (the persistence the review flagged as untested).
func TestFeedStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := feedStatePath(dir, -100200300)
	saveFeedState(path, feedState{
		LastBugID: 999,
		Tracked:   map[string]*trackedBug{"500": {MsgID: 42, State: "CONFIRMED|"}},
	})
	got := loadFeedState(path)
	if got.LastBugID != 999 {
		t.Errorf("LastBugID lost across save/load: %d", got.LastBugID)
	}
	if tb := got.Tracked["500"]; tb == nil || tb.MsgID != 42 || tb.State != "CONFIRMED|" {
		t.Errorf("tracked state did not survive save/load: %+v", tb)
	}
}

// TestFeedPostBlocked covers the startup permission classifier: channels need an admin with
// post rights; groups only need the bot to be present and not muted.
func TestFeedPostBlocked(t *testing.T) {
	for _, c := range []struct {
		name     string
		chatType string
		member   telego.ChatMember
		blocked  bool
	}{
		{"channel admin can post", "channel", &telego.ChatMemberAdministrator{CanPostMessages: true}, false},
		{"channel admin without post right", "channel", &telego.ChatMemberAdministrator{CanPostMessages: false}, true},
		{"channel plain member", "channel", &telego.ChatMemberMember{}, true},
		{"channel owner", "channel", &telego.ChatMemberOwner{}, false},
		{"supergroup member ok", "supergroup", &telego.ChatMemberMember{}, false},
		{"group restricted+muted", "supergroup", &telego.ChatMemberRestricted{CanSendMessages: false}, true},
		{"group restricted can send", "supergroup", &telego.ChatMemberRestricted{CanSendMessages: true}, false},
		{"left a channel", "channel", &telego.ChatMemberLeft{}, true},
		{"banned from a group", "supergroup", &telego.ChatMemberBanned{}, true},
	} {
		if got := feedPostBlocked(c.chatType, c.member) != ""; got != c.blocked {
			t.Errorf("%s: blocked=%v, want %v (reason=%q)", c.name, got, c.blocked, feedPostBlocked(c.chatType, c.member))
		}
	}
}
