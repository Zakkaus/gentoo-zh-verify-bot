package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	htmlstd "html"
	neturl "net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/lookup"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
)

type deadlineFeedBot struct {
	*fakeFeedBot
	sendDeadline bool
	editDeadline bool
}

func (b *deadlineFeedBot) SendMessage(ctx context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	_, b.sendDeadline = ctx.Deadline()
	<-ctx.Done()
	b.fakeFeedBot.sendErr = ctx.Err()
	return b.fakeFeedBot.SendMessage(ctx, p)
}

func (b *deadlineFeedBot) EditMessageText(ctx context.Context, p *telego.EditMessageTextParams) (*telego.Message, error) {
	_, b.editDeadline = ctx.Deadline()
	<-ctx.Done()
	b.fakeFeedBot.editErr = ctx.Err()
	return b.fakeFeedBot.EditMessageText(ctx, p)
}

type scriptedFeedBot struct {
	*fakeFeedBot
	sendErrs []error
}

func (b *scriptedFeedBot) SendMessage(ctx context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	call := b.fakeFeedBot.sends
	oldErr := b.fakeFeedBot.sendErr
	if call < len(b.sendErrs) {
		b.fakeFeedBot.sendErr = b.sendErrs[call]
	} else {
		b.fakeFeedBot.sendErr = nil
	}
	msg, err := b.fakeFeedBot.SendMessage(ctx, p)
	b.fakeFeedBot.sendErr = oldErr
	return msg, err
}

func (b *scriptedFeedBot) EditMessageText(ctx context.Context, p *telego.EditMessageTextParams) (*telego.Message, error) {
	return b.fakeFeedBot.EditMessageText(ctx, p)
}

func setFeedTestTiming(t *testing.T, telegramTimeout, fetchTimeout time.Duration) {
	t.Helper()
	oldPause, oldTelegram, oldFetch := feedSendPause, feedTelegramTimeout, feedFetchTimeout
	feedSendPause, feedTelegramTimeout, feedFetchTimeout = 0, telegramTimeout, fetchTimeout
	t.Cleanup(func() {
		feedSendPause, feedTelegramTimeout, feedFetchTimeout = oldPause, oldTelegram, oldFetch
	})
}

// TestBugBacklogPaginationAcrossCycles proves a 250-item gap drains through three one-request
// ascending slices, preserving an oldest-first cursor boundary after every cycle.
func TestBugBacklogPaginationAcrossCycles(t *testing.T) {
	tests := []struct {
		name        string
		backlog     int
		wantCursor  []int
		wantFetches []int
	}{
		{
			name:        "250 bugs drain over three bounded cycles",
			backlog:     250,
			wantCursor:  []int{1100, 1200, 1250},
			wantFetches: []int{1, 1, 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setFeedTestTiming(t, time.Second, time.Second)
			const initialCursor = 1000
			fetchBatch := func(_ context.Context, afterID int) ([]recentBug, error) {
				end := afterID + recentBugsLimit
				maxID := initialCursor + tt.backlog
				if end > maxID {
					end = maxID
				}
				batch := make([]recentBug, 0, end-afterID)
				for id := afterID + 1; id <= end; id++ {
					batch = append(batch, recentBug{ID: id, Summary: fmt.Sprintf("bug %d", id), Status: "CONFIRMED"})
				}
				return batch, nil
			}

			st := &feedState{LastBugID: initialCursor}
			fb := &fakeFeedBot{}
			f := &config.FeedConfig{ChatID: -100, Lang: "en"}
			for cycle, wantCursor := range tt.wantCursor {
				fetches := 0
				bugs, ok := collectRecentBugs(context.Background(), st.LastBugID, func(ctx context.Context, afterID int) ([]recentBug, error) {
					fetches++
					return fetchBatch(ctx, afterID)
				})
				if !ok {
					t.Fatalf("cycle %d: bounded fetch failed", cycle+1)
				}
				postFeedItems(context.Background(), fb, f, st, bugs, nil)
				if st.LastBugID != wantCursor {
					t.Fatalf("cycle %d cursor = %d, want %d", cycle+1, st.LastBugID, wantCursor)
				}
				if fetches != tt.wantFetches[cycle] {
					t.Errorf("cycle %d used %d fetches, want %d", cycle+1, fetches, tt.wantFetches[cycle])
				}
			}
			if fb.sends != tt.backlog {
				t.Fatalf("delivered %d bugs, want %d", fb.sends, tt.backlog)
			}
			for i, text := range fb.sentText {
				wantID := initialCursor + i + 1
				if !strings.Contains(text, "<b>Bug "+strconv.Itoa(wantID)+"</b>") {
					t.Fatalf("delivery %d is not bug %d: %q", i, wantID, text)
				}
			}
		})
	}
}

// TestBugCursorStopsAtUndeliveredItem proves a failed post leaves the cursor on the delivered prefix.
func TestBugCursorStopsAtUndeliveredItem(t *testing.T) {
	setFeedTestTiming(t, time.Second, time.Second)
	st := &feedState{LastBugID: 1000}
	base := &fakeFeedBot{}
	bot := &scriptedFeedBot{
		fakeFeedBot: base,
		sendErrs:    []error{nil, errors.New("connection reset by peer"), nil},
	}
	bugs := []recentBug{
		{ID: 1003, Summary: "third", Status: "CONFIRMED"},
		{ID: 1001, Summary: "first", Status: "CONFIRMED"},
		{ID: 1002, Summary: "second", Status: "CONFIRMED"},
	}

	postFeedItems(context.Background(), bot, &config.FeedConfig{ChatID: -100, Lang: "en"}, st, bugs, nil)

	if st.LastBugID != 1001 {
		t.Fatalf("cursor = %d, want delivered prefix boundary 1001", st.LastBugID)
	}
	if base.sends != 2 {
		t.Fatalf("send attempts = %d, want 2; the cycle must stop at the failed item", base.sends)
	}
	if st.Tracked["1001"] == nil || st.Tracked["1002"] != nil || st.Tracked["1003"] != nil {
		t.Fatalf("tracked bugs = %#v, want only delivered bug 1001", st.Tracked)
	}
}

func TestFetchRecentBugsUsesCursorBoundedQuery(t *testing.T) {
	tests := []struct {
		name    string
		afterID int
		body    string
		wantIDs []int
		order   string
		limit   string
	}{
		{name: "first run baselines newest", body: `{"bugs":[{"id":1250}]}`, wantIDs: []int{1250}, order: "bug_id DESC", limit: "1"},
		{name: "catch-up starts above cursor", afterID: 1000, body: `{"bugs":[{"id":1002},{"id":1001}]}`, wantIDs: []int{1001, 1002}, order: "bug_id ASC", limit: "100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var query neturl.Values
			got, ok := fetchRecentBugsWith(context.Background(), tt.afterID, func(_ context.Context, rawURL string, dst any) error {
				u, err := neturl.Parse(rawURL)
				if err != nil {
					return err
				}
				query = u.Query()
				return json.Unmarshal([]byte(tt.body), dst)
			})
			if !ok {
				t.Fatal("fetchRecentBugs() failed")
			}
			gotIDs := make([]int, len(got))
			for i, bug := range got {
				gotIDs[i] = bug.ID
			}
			if fmt.Sprint(gotIDs) != fmt.Sprint(tt.wantIDs) {
				t.Errorf("bug IDs = %v, want %v", gotIDs, tt.wantIDs)
			}
			if query.Get("order") != tt.order || query.Get("limit") != tt.limit {
				t.Errorf("query order/limit = %q/%q, want %q/%q", query.Get("order"), query.Get("limit"), tt.order, tt.limit)
			}
			if tt.afterID > 0 {
				if query.Get("f1") != "bug_id" || query.Get("o1") != "greaterthan" || query.Get("v1") != strconv.Itoa(tt.afterID) {
					t.Errorf("cursor filter = f1=%q o1=%q v1=%q", query.Get("f1"), query.Get("o1"), query.Get("v1"))
				}
			} else if query.Get("f1") != "" {
				t.Errorf("baseline query unexpectedly has cursor filter %q", query.Get("f1"))
			}
		})
	}
}

func TestPollAllUsesPerCursorBugBatches(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "baseline and catch-up use independent cursors"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setFeedTestTiming(t, time.Second, time.Second)
			off := false
			feeds := []*config.FeedConfig{{ChatID: -100, Lang: "en", News: &off},
				{ChatID: -200, Lang: "en", News: &off}}
			states := map[int64]*feedState{
				-100: {},
				-200: {LastBugID: 1000},
			}
			now := time.Now()
			var cursors []int
			sources := feedSources{
				recent: func(_ context.Context, cursor int) ([]recentBug, bool) {
					cursors = append(cursors, cursor)
					if cursor == 0 {
						return []recentBug{{ID: 1250, Status: "CONFIRMED"}}, true
					}
					return []recentBug{
						{ID: 1001, Summary: "bug 1001", Status: "CONFIRMED"},
						{ID: 1002, Summary: "bug 1002", Status: "CONFIRMED"},
					}, true
				},
				news:    func(context.Context) ([]lookup.NewsItem, error) { return nil, nil },
				tracked: func(context.Context, []int) ([]recentBug, bool) { return nil, true },
			}
			bot := &fakeFeedBot{}
			pollAllWithSources(context.Background(), bot, feeds, states, "", now,
				map[int64]time.Time{-100: now, -200: now}, sources)

			if fmt.Sprint(cursors) != "[0 1000]" {
				t.Errorf("recent bug cursors = %v, want [0 1000]", cursors)
			}
			if states[-100].LastBugID != 1250 {
				t.Errorf("baseline cursor = %d, want newest 1250", states[-100].LastBugID)
			}
			if states[-200].LastBugID != 1002 {
				t.Errorf("catch-up cursor = %d, want 1002", states[-200].LastBugID)
			}
			if bot.sends != 2 {
				t.Errorf("catch-up sends = %d, want 2; baseline must stay silent", bot.sends)
			}
		})
	}
}

func TestTelegramFeedOperationDeadlines(t *testing.T) {
	tests := []struct {
		name string
		run  func(*deadlineFeedBot) bool
	}{
		{
			name: "send",
			run: func(bot *deadlineFeedBot) bool {
				_, ok, _, _ := postFeed(context.Background(), bot, -100, "test", false, 0)
				return bot.sendDeadline && !ok && bot.sends == 1
			},
		},
		{
			name: "edit",
			run: func(bot *deadlineFeedBot) bool {
				st := &feedState{Tracked: map[string]*trackedBug{"7": {MsgID: 1, State: "CONFIRMED|"}}}
				refreshTracked(context.Background(), bot, &config.FeedConfig{ChatID: -100}, st,
					map[int]recentBug{7: {ID: 7, Status: "IN_PROGRESS"}}, true)
				tb := st.Tracked["7"]
				return bot.editDeadline && bot.edits == 1 && tb != nil && tb.EditFails == 0
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setFeedTestTiming(t, 10*time.Millisecond, time.Second)
			bot := &deadlineFeedBot{fakeFeedBot: &fakeFeedBot{}}
			started := time.Now()
			if !tt.run(bot) {
				t.Fatal("operation did not receive and honor its child deadline")
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("operation remained blocked for %s", elapsed)
			}
		})
	}
}

func TestPollFetchPhaseDeadlines(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *fakeFeedBot, *config.FeedConfig, *feedState, *bool) feedSources
	}{
		{
			name: "hung recent bugs do not starve news",
			run: func(_ *testing.T, _ *fakeFeedBot, _ *config.FeedConfig, _ *feedState, nextRan *bool) feedSources {
				return feedSources{
					recent: func(ctx context.Context, _ int) ([]recentBug, bool) {
						<-ctx.Done()
						return nil, false
					},
					news: func(ctx context.Context) ([]lookup.NewsItem, error) {
						*nextRan = ctx.Err() == nil
						return []lookup.NewsItem{{Date: "2026-08-23", Title: "new", URL: "new"}, {URL: "old"}}, nil
					},
					tracked: func(context.Context, []int) ([]recentBug, bool) { return nil, true },
				}
			},
		},
		{
			name: "hung news does not starve tracked bugs",
			run: func(_ *testing.T, _ *fakeFeedBot, _ *config.FeedConfig, st *feedState, nextRan *bool) feedSources {
				st.Tracked = map[string]*trackedBug{"42": {MsgID: 1, State: "CONFIRMED|"}}
				return feedSources{
					recent: func(context.Context, int) ([]recentBug, bool) { return nil, true },
					news: func(ctx context.Context) ([]lookup.NewsItem, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					},
					tracked: func(ctx context.Context, _ []int) ([]recentBug, bool) {
						*nextRan = ctx.Err() == nil
						return []recentBug{{ID: 42, Status: "CONFIRMED"}}, true
					},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setFeedTestTiming(t, time.Second, 10*time.Millisecond)
			fb := &fakeFeedBot{}
			f := &config.FeedConfig{ChatID: -100}
			st := &feedState{LastBugID: 1, LastNewsURL: "old"}
			nextRan := false
			sources := tt.run(t, fb, f, st, &nextRan)
			now := time.Now()
			pollAllWithSources(context.Background(), fb, []*config.FeedConfig{f}, map[int64]*feedState{f.ChatID: st}, "", now,
				map[int64]time.Time{f.ChatID: now}, sources)
			if !nextRan {
				t.Fatal("the phase after a timed-out fetch inherited an expired context")
			}
		})
	}
}

func TestTrackedBugChunksGetIndependentDeadlines(t *testing.T) {
	tests := []struct {
		name         string
		ids          int
		blockedCalls int
		wantID       int
	}{
		{name: "second chunk survives first timeout", ids: 51, blockedCalls: 1, wantID: 51},
		{name: "third chunk survives two timeouts", ids: 101, blockedCalls: 2, wantID: 101},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setFeedTestTiming(t, time.Second, 10*time.Millisecond)
			calls := 0
			getJSON := func(ctx context.Context, _ string, dst any) error {
				calls++
				if calls <= tt.blockedCalls {
					<-ctx.Done()
					return ctx.Err()
				}
				body := fmt.Sprintf(`{"bugs":[{"id":%d,"status":"IN_PROGRESS"}]}`, tt.wantID)
				return json.Unmarshal([]byte(body), dst)
			}
			ids := make([]int, tt.ids)
			for i := range ids {
				ids[i] = i + 1
			}
			bugs, ok := fetchBugsByIDWith(context.Background(), ids, getJSON)
			if ok {
				t.Fatal("timed-out chunks must make the aggregate fetch incomplete")
			}
			if calls != tt.blockedCalls+1 {
				t.Fatalf("made %d chunk calls, want %d", calls, tt.blockedCalls+1)
			}
			byID := map[int]recentBug{}
			for _, bug := range bugs {
				byID[bug.ID] = bug
			}
			st := &feedState{Tracked: map[string]*trackedBug{strconv.Itoa(tt.wantID): {MsgID: 1, State: "CONFIRMED|"}}}
			fb := &fakeFeedBot{}
			refreshTracked(context.Background(), fb, &config.FeedConfig{ChatID: -100}, st, byID, ok)
			if fb.edits != 1 {
				t.Fatalf("later successful chunk was not usable: got %d edits", fb.edits)
			}
		})
	}
}

func TestTrackedBugSchemaValidation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantOK     bool
		wantMisses int
	}{
		{name: "missing field", body: `{}`, wantOK: false, wantMisses: 0},
		{name: "null field", body: `{"bugs":null}`, wantOK: false, wantMisses: 0},
		{name: "empty array is valid", body: `{"bugs":[]}`, wantOK: true, wantMisses: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setFeedTestTiming(t, time.Second, time.Second)
			bugs, ok := fetchBugsByIDWith(context.Background(), []int{77}, func(_ context.Context, _ string, dst any) error {
				return json.Unmarshal([]byte(tt.body), dst)
			})
			if ok != tt.wantOK {
				t.Fatalf("fetchOK = %v, want %v", ok, tt.wantOK)
			}
			st := &feedState{Tracked: map[string]*trackedBug{"77": {MsgID: 1, State: "CONFIRMED|"}}}
			fb := &fakeFeedBot{}
			refreshTracked(context.Background(), fb, &config.FeedConfig{ChatID: -100}, st, map[int]recentBug{}, ok)
			if got := st.Tracked["77"].Misses; got != tt.wantMisses {
				t.Fatalf("misses = %d, want %d (decoded bugs: %v)", got, tt.wantMisses, bugs)
			}
		})
	}
}

func TestTransientEditsNeverAgeOutTracking(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		repeats     int
		initialFail int
		wantFails   int
		wantDropped bool
	}{
		{name: "transport", err: errors.New("connection reset by peer"), repeats: maxEditFails + 1, initialFail: 3},
		{name: "context cancellation", err: context.Canceled, repeats: maxEditFails + 1, initialFail: 3},
		{name: "server 500", err: &telegoapi.Error{ErrorCode: 500, Description: "Internal Server Error"}, repeats: maxEditFails + 1, initialFail: 3},
		{name: "deterministic 400 counts", err: &telegoapi.Error{ErrorCode: 400, Description: "Bad Request: can't parse entities"}, repeats: 1, wantFails: 1},
		{name: "repeated deterministic 400 drops", err: errors.New("Bad Request: can't parse entities"), repeats: maxEditFails, wantDropped: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setFeedTestTiming(t, time.Second, time.Second)
			st := &feedState{Tracked: map[string]*trackedBug{"88": {MsgID: 1, State: "CONFIRMED|", EditFails: tt.initialFail}}}
			fb := &fakeFeedBot{editErr: tt.err}
			for i := 0; i < tt.repeats && st.Tracked["88"] != nil; i++ {
				refreshTracked(context.Background(), fb, &config.FeedConfig{ChatID: -100}, st,
					map[int]recentBug{88: {ID: 88, Status: "IN_PROGRESS"}}, true)
			}
			tb := st.Tracked["88"]
			if (tb == nil) != tt.wantDropped {
				t.Fatalf("dropped = %v, want %v", tb == nil, tt.wantDropped)
			}
			if tb != nil && tb.EditFails != tt.wantFails {
				t.Fatalf("EditFails = %d, want %d", tb.EditFails, tt.wantFails)
			}
		})
	}
}

func TestFormatNewsTelegramLimit(t *testing.T) {
	tests := []struct {
		name  string
		title string
	}{
		{name: "ASCII", title: strings.Repeat("a", tg.MessageLimit+500)},
		{name: "UTF-16 surrogate pairs", title: strings.Repeat("😀", tg.MessageLimit)},
		{name: "escaped HTML", title: strings.Repeat("&amp;<>", tg.MessageLimit)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatNews(lookup.NewsItem{Date: "2026-08-23", Title: tt.title, URL: "https://www.gentoo.org/news"})
			start, end := strings.Index(got, ">"), strings.LastIndex(got, "</a>")
			if start < 0 || end <= start {
				t.Fatalf("invalid rendered anchor: %q", got)
			}
			visible := "📰 " + htmlstd.UnescapeString(got[start+1:end])
			if units := tg.TextUnits(visible); units > tg.MessageLimit {
				t.Fatalf("rendered news uses %d Telegram text units, limit %d", units, tg.MessageLimit)
			}
			if !strings.Contains(got, "…</a>") {
				t.Error("oversized title was not visibly truncated")
			}
		})
	}
}

func TestPermanentNewsRejectionAdvancesCursor(t *testing.T) {
	tests := []struct {
		name       string
		firstErr   error
		wantCursor string
		wantSends  int
	}{
		{
			name:       "permanent item rejection is skipped",
			firstErr:   &telegoapi.Error{ErrorCode: 400, Description: "Bad Request: message is too long"},
			wantCursor: "newest",
			wantSends:  2,
		},
		{
			name:       "server failure remains retryable",
			firstErr:   &telegoapi.Error{ErrorCode: 500, Description: "Internal Server Error"},
			wantCursor: "old",
			wantSends:  1,
		},
		{
			name:       "context cancellation remains retryable",
			firstErr:   context.Canceled,
			wantCursor: "old",
			wantSends:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setFeedTestTiming(t, time.Second, time.Second)
			base := &fakeFeedBot{}
			bot := &scriptedFeedBot{fakeFeedBot: base, sendErrs: []error{tt.firstErr, nil}}
			st := &feedState{LastNewsURL: "old"}
			news := []lookup.NewsItem{
				{Date: "2026-08-23", Title: "Newest", URL: "newest"},
				{Date: "2026-08-22", Title: "Older new", URL: "older-new"},
				{Date: "2026-08-21", Title: "Old", URL: "old"},
			}
			postFeedItems(context.Background(), bot, &config.FeedConfig{ChatID: -100}, st, nil, news)
			if st.LastNewsURL != tt.wantCursor {
				t.Fatalf("news cursor = %q, want %q", st.LastNewsURL, tt.wantCursor)
			}
			if base.sends != tt.wantSends {
				t.Fatalf("send attempts = %d, want %d", base.sends, tt.wantSends)
			}
			if !strings.Contains(base.sentText[0], "Older new") {
				t.Fatalf("first attempted item was not oldest: %q", base.sentText[0])
			}
		})
	}
}
