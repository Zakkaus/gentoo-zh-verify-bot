package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/lookup"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

func captureFeedLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOutput, oldPrefix, oldFlags := log.Writer(), log.Prefix(), log.Flags()
	log.SetOutput(&buf)
	log.SetPrefix("")
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldOutput)
		log.SetPrefix(oldPrefix)
		log.SetFlags(oldFlags)
	})
	return &buf
}

func setFeedStateWriter(t *testing.T, write func(string, any) error) {
	t.Helper()
	old := feedStateWrite
	feedStateWrite = write
	t.Cleanup(func() { feedStateWrite = old })
}

func TestPollAllFirstRunBaselinesEachDestination(t *testing.T) {
	setFeedTestTiming(t, time.Second, time.Second)
	bugsOn, newsOn := true, true
	feeds := []*config.FeedConfig{
		{ChatID: -100101, Lang: "en", Bugs: &bugsOn, News: &newsOn, BugProduct: "Gentoo Linux", BugComponent: "Kernel"},
		{ChatID: -100202, Lang: "en", Bugs: &bugsOn, News: &newsOn},
	}
	dir := t.TempDir()
	states := map[int64]*feedState{}
	for _, f := range feeds {
		st := loadFeedState(feedStatePath(dir, f.ChatID))
		states[f.ChatID] = &st
	}
	bugs := []recentBug{
		{ID: 701, Summary: "older", Status: "CONFIRMED", Product: "Gentoo Linux", Component: "Kernel"},
		{ID: 703, Summary: "current", Status: "CONFIRMED", Product: "Other", Component: "Other"},
	}
	news := []lookup.NewsItem{
		{URL: "https://example.test/news/current", Title: "current", Date: "2026-08-25"},
		{URL: "https://example.test/news/older", Title: "older", Date: "2026-08-24"},
	}
	bot := &fakeFeedBot{}
	now := time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC)
	pollAllWithSources(context.Background(), newAPITestBot(t, bot), feeds, states, dir, now, map[int64]time.Time{}, feedSources{
		recent: func(_ context.Context, after int) ([]recentBug, bool) {
			if after != 0 {
				t.Errorf("fresh destination fetched after %d, want 0", after)
			}
			return bugs, true
		},
		news: func(_ context.Context) ([]lookup.NewsItem, error) { return news, nil },
	})

	if bot.sends != 0 {
		t.Fatalf("first run sent %d historical item(s), want none", bot.sends)
	}
	for _, f := range feeds {
		st := states[f.ChatID]
		if st.LastBugID != 703 {
			t.Errorf("destination %d LastBugID = %d, want 703", f.ChatID, st.LastBugID)
		}
		if st.LastNewsURL != news[0].URL {
			t.Errorf("destination %d LastNewsURL = %q, want %q", f.ChatID, st.LastNewsURL, news[0].URL)
		}
		durable := loadFeedState(feedStatePath(dir, f.ChatID))
		if durable.LastBugID != 703 || durable.LastNewsURL != news[0].URL {
			t.Errorf("destination %d durable state = %+v, want current cursors", f.ChatID, durable)
		}
	}
}

func TestPollAllFiltersConfiguredProductAndComponent(t *testing.T) {
	setFeedTestTiming(t, time.Second, time.Second)
	bugsOn, newsOff := true, false
	feed := &config.FeedConfig{
		ChatID:       -100303,
		Lang:         "en",
		Bugs:         &bugsOn,
		News:         &newsOff,
		BugProduct:   "Gentoo Linux",
		BugComponent: "Kernel",
	}
	state := &feedState{LastBugID: 100}
	bot := &fakeFeedBot{}
	pollAllWithSources(context.Background(), newAPITestBot(t, bot), []*config.FeedConfig{feed}, map[int64]*feedState{feed.ChatID: state}, "", time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC), map[int64]time.Time{}, feedSources{
		recent: func(_ context.Context, after int) ([]recentBug, bool) {
			if after != 100 {
				t.Errorf("filtered destination fetched after %d, want 100", after)
			}
			return []recentBug{
				{ID: 101, Summary: "wrong product", Status: "CONFIRMED", Product: "Other", Component: "Kernel"},
				{ID: 102, Summary: "wrong component", Status: "CONFIRMED", Product: "Gentoo Linux", Component: "Other"},
				{ID: 103, Summary: "match", Status: "CONFIRMED", Product: "Gentoo Linux", Component: "Kernel"},
			}, true
		},
	})

	if state.LastBugID != 103 {
		t.Fatalf("filtered destination cursor = %d, want 103", state.LastBugID)
	}
	if len(bot.sentText) != 1 {
		t.Fatalf("filtered destination sent %d item(s), want exactly the matching item", len(bot.sentText))
	}
	if !strings.Contains(bot.sentText[0], "https://bugs.gentoo.org/103") {
		t.Errorf("filtered destination rendered unexpected item %q", bot.sentText[0])
	}
}

type feedProbeCaller struct {
	t         *testing.T
	chat      map[string]any
	member    map[string]any
	chatErr   error
	memberErr error
	methods   []string
}

func (c *feedProbeCaller) Call(_ context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
	c.t.Helper()
	method := url[strings.LastIndexByte(url, '/')+1:]
	c.methods = append(c.methods, method)
	switch method {
	case "getMe":
		return fakeTelegramResponse(map[string]any{"id": 9001, "is_bot": true, "first_name": "Feed"}, nil)
	case "getChat":
		var request struct {
			ChatID int64 `json:"chat_id"`
		}
		if err := json.Unmarshal(data.BodyRaw, &request); err != nil {
			return nil, err
		}
		if request.ChatID != -100404 {
			c.t.Errorf("GetChat chat_id = %d, want -100404", request.ChatID)
		}
		return fakeTelegramResponse(c.chat, c.chatErr)
	case "getChatMember":
		var request struct {
			ChatID int64 `json:"chat_id"`
			UserID int64 `json:"user_id"`
		}
		if err := json.Unmarshal(data.BodyRaw, &request); err != nil {
			return nil, err
		}
		if request.ChatID != -100404 || request.UserID != 9001 {
			c.t.Errorf("GetChatMember request = chat %d user %d, want chat -100404 user 9001", request.ChatID, request.UserID)
		}
		return fakeTelegramResponse(c.member, c.memberErr)
	default:
		return nil, errors.New("unexpected Telegram method " + method)
	}
}

func feedProbeMember(status string, canPost bool) map[string]any {
	return map[string]any{
		"status":            status,
		"can_post_messages": canPost,
		"user": map[string]any{
			"id":         9001,
			"is_bot":     true,
			"first_name": "Feed",
		},
	}
}

func TestProbeFeedPermsTelegramFailuresAndSuccess(t *testing.T) {
	for _, tt := range []struct {
		name        string
		chat        map[string]any
		member      map[string]any
		chatErr     error
		memberErr   error
		wantMethods string
		wantLog     string
		wantError   string
	}{
		{
			name:        "get chat failure",
			chatErr:     errors.New("chat missing"),
			wantMethods: "getMe,getChat",
			wantLog:     "target chat -100404 unreachable at startup (GetChat:",
			wantError:   "chat missing",
		},
		{
			name:        "membership failure",
			chat:        map[string]any{"id": -100404, "type": "channel"},
			memberErr:   errors.New("membership denied"),
			wantMethods: "getMe,getChat,getChatMember",
			wantLog:     "cannot read bot membership in chat -100404 (channel) at startup (",
			wantError:   "membership denied",
		},
		{
			name:        "channel missing post right",
			chat:        map[string]any{"id": -100404, "type": "channel"},
			member:      feedProbeMember("administrator", false),
			wantMethods: "getMe,getChat,getChatMember",
			wantLog:     "bot cannot post in target chat -100404 (channel): admin without can_post_messages right",
		},
		{
			name:        "channel post permission success",
			chat:        map[string]any{"id": -100404, "type": "channel"},
			member:      feedProbeMember("administrator", true),
			wantMethods: "getMe,getChat,getChatMember",
			wantLog:     "target chat -100404 (channel) post permission OK",
		},
		{
			name:        "supergroup member permission success",
			chat:        map[string]any{"id": -100404, "type": "supergroup"},
			member:      feedProbeMember("member", false),
			wantMethods: "getMe,getChat,getChatMember",
			wantLog:     "target chat -100404 (supergroup) post permission OK",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureFeedLogs(t)
			caller := &feedProbeCaller{t: t, chat: tt.chat, member: tt.member, chatErr: tt.chatErr, memberErr: tt.memberErr}
			probeFeedPerms(context.Background(), newAPITestBot(t, caller), []*config.FeedConfig{{ChatID: -100404}})

			if got := strings.Join(caller.methods, ","); got != tt.wantMethods {
				t.Errorf("Telegram methods = %s, want %s", got, tt.wantMethods)
			}
			if got := logs.String(); !strings.Contains(got, tt.wantLog) {
				t.Errorf("probe log = %q, want substring %q", got, tt.wantLog)
			}
			if tt.wantError != "" && !strings.Contains(logs.String(), tt.wantError) {
				t.Errorf("probe log = %q, want API error %q", logs.String(), tt.wantError)
			}
		})
	}
}

func TestServiceRunPollsThenFlushesOnCancellation(t *testing.T) {
	dir := t.TempDir()
	feed := &config.FeedConfig{ChatID: -100505, IntervalSeconds: 60}
	probeEntered := make(chan struct{})
	releaseProbe := make(chan struct{})
	pollCalls := 0
	service := New(nil, []*config.FeedConfig{feed}, dir)
	service.probe = func(context.Context, *telego.Bot, []*config.FeedConfig) {
		close(probeEntered)
		<-releaseProbe
	}
	service.poll = func(ctx context.Context, _ *telego.Bot, _ []*config.FeedConfig, states map[int64]*feedState, _ string, _ time.Time, _ map[int64]time.Time) {
		pollCalls++
		<-ctx.Done()
		states[feed.ChatID].LastBugID = 505
		states[feed.ChatID].LastNewsURL = "https://example.test/news/flushed"
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(done)
	}()
	<-probeEntered
	close(releaseProbe)
	cancel()
	<-done

	if pollCalls != 1 {
		t.Fatalf("Run poll calls = %d, want immediate initial poll", pollCalls)
	}
	got := loadFeedState(feedStatePath(dir, feed.ChatID))
	if got.LastBugID != 505 || got.LastNewsURL != "https://example.test/news/flushed" {
		t.Errorf("Run cancellation flush = %+v, want updated cursors", got)
	}
}

func TestRuntimeStateSaveFailureKeepsOnlyVolatileProgress(t *testing.T) {
	setFeedTestTiming(t, time.Second, time.Second)
	logs := captureFeedLogs(t)
	dir := t.TempDir()
	bugsOn, newsOff := true, false
	feed := &config.FeedConfig{ChatID: -100606, Lang: "en", Bugs: &bugsOn, News: &newsOff}
	path := feedStatePath(dir, feed.ChatID)
	if err := store.Write(path, feedState{LastBugID: 10}); err != nil {
		t.Fatal(err)
	}
	failurePath := filepath.Join(dir, "missing", "feed.json")
	setFeedStateWriter(t, func(_ string, value any) error { return store.Write(failurePath, value) })
	state := loadFeedState(path)
	bot := &fakeFeedBot{}
	now := time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC)
	sources := feedSources{
		recent: func(_ context.Context, _ int) ([]recentBug, bool) {
			return []recentBug{{ID: 11, Summary: "delivered before failed save", Status: "CONFIRMED"}}, true
		},
		tracked: func(_ context.Context, _ []int) ([]recentBug, bool) {
			return []recentBug{{ID: 11, Summary: "delivered before failed save", Status: "CONFIRMED"}}, true
		},
	}
	nextDue := map[int64]time.Time{}
	states := map[int64]*feedState{feed.ChatID: &state}
	apiBot := newAPITestBot(t, bot)
	pollAllWithSources(context.Background(), apiBot, []*config.FeedConfig{feed}, states, dir, now, nextDue, sources)
	pollAllWithSources(context.Background(), apiBot, []*config.FeedConfig{feed}, states, dir, now.Add(feed.Interval()), nextDue, sources)

	if state.LastBugID != 11 {
		t.Errorf("live state cursor after failed save = %d, want 11", state.LastBugID)
	}
	if len(bot.sentText) != 1 {
		t.Errorf("live state sent %d times after a failed save, want the delivered item not to repeat", len(bot.sentText))
	}
	if output := logs.String(); !strings.Contains(output, "state: temp for "+failurePath) {
		t.Errorf("operator state-save warning missing from %q", output)
	}

	var reloaded feedState
	restart := New(nil, []*config.FeedConfig{feed}, dir)
	restart.probe = func(context.Context, *telego.Bot, []*config.FeedConfig) {}
	restart.poll = func(_ context.Context, _ *telego.Bot, _ []*config.FeedConfig, states map[int64]*feedState, _ string, _ time.Time, _ map[int64]time.Time) {
		reloaded = *states[feed.ChatID]
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	restart.Run(cancelled)
	if reloaded.LastBugID != 10 {
		t.Errorf("new service loaded LastBugID = %d, want last durable cursor 10", reloaded.LastBugID)
	}
}

func TestSendBeforeSaveFailureResendsAfterRestart(t *testing.T) {
	setFeedTestTiming(t, time.Second, time.Second)
	dir := t.TempDir()
	bugsOn, newsOff := true, false
	feed := &config.FeedConfig{ChatID: -100707, Lang: "en", Bugs: &bugsOn, News: &newsOff}
	path := feedStatePath(dir, feed.ChatID)
	if err := store.Write(path, feedState{LastBugID: 20}); err != nil {
		t.Fatal(err)
	}
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	writes := 0
	setFeedStateWriter(t, func(path string, value any) error {
		writes++
		if writes == 1 {
			close(writeStarted)
			<-releaseWrite
			return errors.New("interrupted state write")
		}
		return store.Write(path, value)
	})
	sources := feedSources{
		recent: func(_ context.Context, _ int) ([]recentBug, bool) {
			return []recentBug{{ID: 21, Summary: "send before save", Status: "CONFIRMED"}}, true
		},
	}
	firstState := loadFeedState(path)
	firstBot := &fakeFeedBot{}
	firstDone := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-firstDone:
		default:
			close(releaseWrite)
			<-firstDone
		}
	})
	go func() {
		pollAllWithSources(context.Background(), newAPITestBot(t, firstBot), []*config.FeedConfig{feed}, map[int64]*feedState{feed.ChatID: &firstState}, dir, time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC), map[int64]time.Time{}, sources)
		close(firstDone)
	}()
	<-writeStarted

	if firstBot.sends != 1 || firstState.LastBugID != 21 {
		t.Fatalf("blocked first poll = sends %d cursor %d, want one delivery and volatile cursor 21", firstBot.sends, firstState.LastBugID)
	}

	secondBot := &fakeFeedBot{}
	var reloadedCursor int
	restart := New(newAPITestBot(t, secondBot), []*config.FeedConfig{feed}, dir)
	restart.probe = func(context.Context, *telego.Bot, []*config.FeedConfig) {}
	restart.poll = func(ctx context.Context, bot *telego.Bot, feeds []*config.FeedConfig, states map[int64]*feedState, stateDir string, now time.Time, nextDue map[int64]time.Time) {
		reloadedCursor = states[feed.ChatID].LastBugID
		pollAllWithSources(ctx, bot, feeds, states, stateDir, now, nextDue, sources)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	restart.Run(cancelled)

	if reloadedCursor != 20 {
		t.Fatalf("replacement service loaded cursor %d, want last durable cursor 20", reloadedCursor)
	}
	if secondBot.sends != 1 {
		t.Fatalf("replacement service sent %d item(s), want the undurable delivery retried", secondBot.sends)
	}
	close(releaseWrite)
	<-firstDone

	durable := loadFeedState(path)
	if durable.LastBugID != 21 {
		t.Errorf("retry did not make cursor durable: %d", durable.LastBugID)
	}
	if firstBot.sends+secondBot.sends != 2 {
		t.Errorf("send-before-save window deliveries = %d, want one original plus one resend", firstBot.sends+secondBot.sends)
	}
}
