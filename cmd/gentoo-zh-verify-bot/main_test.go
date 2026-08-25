package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/feed"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/verify"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

func TestStreamEndedUnexpectedly(t *testing.T) {
	if !streamEndedUnexpectedly(nil) {
		t.Error("nil ctx error should signal an unexpected end => restart")
	}
	if streamEndedUnexpectedly(context.Canceled) {
		t.Error("a cancelled ctx is a graceful shutdown => no restart")
	}
}

func TestPrepareUpdateHandlerRegistersBeforePolling(t *testing.T) {
	source := make(chan telego.Update, 1)
	source <- telego.Update{UpdateID: 1}
	close(source)
	handled := make(chan int, 1)
	var registeredHandler *th.BotHandler
	handler, handlerDone, err := prepareUpdateHandler(
		context.Background(),
		&telego.Bot{},
		func(handler *th.BotHandler) {
			registeredHandler = handler
			handler.Handle(func(_ *th.Context, update telego.Update) error {
				handled <- update.UpdateID
				return nil
			})
		},
		func() (<-chan telego.Update, error) {
			if registeredHandler == nil {
				t.Fatal("long polling started before handlers were registered")
			}
			if !registeredHandler.IsRunning() {
				t.Fatal("long polling started before the handler was consuming updates")
			}
			return source, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if handler == nil {
		t.Fatal("prepareUpdateHandler returned a nil handler")
	}
	if got := <-handled; got != 1 {
		t.Fatalf("handled update ID = %d, want 1", got)
	}
	if err := <-handlerDone; err != nil {
		t.Fatal(err)
	}
	if err := handler.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestForwardUpdatesDrainsConfirmedBufferedUpdateOnStartupCancellation(t *testing.T) {
	ctx := newBlockingCancelContext()
	update := telego.Update{UpdateID: 41}
	source := make(chan telego.Update, 1)
	source <- update
	laterPollConfirmed := make(chan struct{})
	close(laterPollConfirmed)
	handled := make(chan int, 1)
	var registeredHandler *th.BotHandler

	handler, _, err := prepareUpdateHandler(
		ctx,
		&telego.Bot{},
		func(handler *th.BotHandler) {
			registeredHandler = handler
			handler.Handle(func(_ *th.Context, update telego.Update) error {
				handled <- update.UpdateID
				return nil
			})
		},
		func() (<-chan telego.Update, error) {
			<-laterPollConfirmed
			return source, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForBufferedSourceRead(source)
	ctx.cancel()
	close(source)

	var handlerDone <-chan error
	if !registeredHandler.IsRunning() {
		done := make(chan error, 1)
		handlerDone = done
		go func() {
			done <- handler.Start()
		}()
	}
	select {
	case got := <-handled:
		if got != update.UpdateID {
			t.Fatalf("handled update ID = %d, want %d", got, update.UpdateID)
		}
	case err := <-handlerDone:
		t.Fatalf("handler stopped before processing confirmed buffered update: %v", err)
	}
	if err := handler.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestForwardUpdatesDrainsConfirmedBufferedUpdateOnShutdown(t *testing.T) {
	ctx := newBlockingCancelContext()
	update := telego.Update{UpdateID: 73}
	source := make(chan telego.Update, 1)
	source <- update
	laterPollConfirmed := make(chan struct{})
	close(laterPollConfirmed)
	destination := make(chan telego.Update)
	inFlight := make(chan struct{}, 1)
	inFlight <- struct{}{}
	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		forwardUpdates(ctx, source, destination, inFlight)
	}()

	<-laterPollConfirmed
	waitForBufferedSourceRead(source)
	ctx.cancel()
	close(source)
	<-inFlight

	got, ok := <-destination
	if !ok {
		t.Fatal("confirmed buffered update was discarded during shutdown")
	}
	if got.UpdateID != update.UpdateID {
		t.Fatalf("forwarded update ID = %d, want %d", got.UpdateID, update.UpdateID)
	}
	<-inFlight
	<-forwardDone
}

type blockingCancelContext struct {
	done     chan struct{}
	canceled atomic.Bool
}

func newBlockingCancelContext() *blockingCancelContext {
	return &blockingCancelContext{done: make(chan struct{})}
}

func (c *blockingCancelContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *blockingCancelContext) Done() <-chan struct{} {
	return c.done
}

func (c *blockingCancelContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func (c *blockingCancelContext) Value(any) any {
	return nil
}

func (c *blockingCancelContext) cancel() {
	c.canceled.Store(true)
	c.done <- struct{}{}
}

func waitForBufferedSourceRead(source <-chan telego.Update) {
	for len(source) != 0 {
		runtime.Gosched()
	}
}

func TestUpdateHandlerConcurrencyIsBounded(t *testing.T) {
	const (
		handlerCap = 64
		updateN    = handlerCap + 16
	)
	source := make(chan telego.Update, updateN)
	for id := range updateN {
		source <- telego.Update{UpdateID: id + 1}
	}
	close(source)

	started := make(chan struct{}, updateN)
	release := make(chan struct{})
	handler, handlerDone, err := prepareUpdateHandler(
		context.Background(),
		&telego.Bot{},
		func(handler *th.BotHandler) {
			handler.Handle(func(_ *th.Context, _ telego.Update) error {
				started <- struct{}{}
				<-release
				return nil
			})
		},
		func() (<-chan telego.Update, error) {
			return source, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for range handlerCap {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("handler concurrency never reached its bound")
		}
	}
	select {
	case <-started:
		t.Fatalf("more than %d update handlers ran concurrently", handlerCap)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-handlerDone; err != nil {
		t.Fatal(err)
	}
	if err := handler.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionOutageObserverUsesDurableHeartbeatOncePerOutage(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "heartbeat.json")
	writeHeartbeatRecord(t, path, now.Add(-25*time.Hour))
	alerts := make(chan time.Duration, 2)
	observer := retentionOutageObserver{
		heartbeatPath: path,
		alert: func(outage time.Duration) {
			alerts <- outage
		},
	}

	observer.observe(now)
	if got := <-alerts; got != 25*time.Hour {
		t.Fatalf("outage = %v, want 25h", got)
	}
	observer.observe(now)
	select {
	case got := <-alerts:
		t.Fatalf("duplicate alert for the same outage: %v", got)
	default:
	}

	writeHeartbeatRecord(t, path, now.Add(-time.Hour))
	observer.observe(now)
	writeHeartbeatRecord(t, path, now.Add(-26*time.Hour))
	observer.observe(now)
	if got := <-alerts; got != 26*time.Hour {
		t.Fatalf("outage after recovery = %v, want 26h", got)
	}
}

func writeHeartbeatRecord(t *testing.T, path string, lastOnline time.Time) {
	t.Helper()
	data, err := json.Marshal(struct {
		LastOnline int64 `json:"last_online"`
	}{LastOnline: lastOnline.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type lifecycleCaller struct {
	mu        sync.Mutex
	botID     int64
	nextID    int
	sent      []telego.SendMessageParams
	approvals int
}

func (c *lifecycleCaller) Call(_ context.Context, endpoint string, data *ta.RequestData) (*ta.Response, error) {
	method := endpoint[strings.LastIndexByte(endpoint, '/')+1:]
	switch method {
	case "getMe":
		return lifecycleAPIResponse(&telego.User{ID: c.botID, IsBot: true, Username: "lifecycle_bot"})
	case "getChat":
		var params struct {
			ChatID int64 `json:"chat_id"`
		}
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		return lifecycleAPIResponse(&telego.ChatFullInfo{ID: params.ChatID, Type: telego.ChatTypeSupergroup})
	case "getChatMember":
		return lifecycleAPIResponse(&telego.ChatMemberAdministrator{
			Status:          telego.MemberStatusAdministrator,
			User:            telego.User{ID: c.botID, IsBot: true},
			CanPostMessages: true,
		})
	case "sendMessage":
		var params struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.nextID++
		messageID := c.nextID
		c.sent = append(c.sent, telego.SendMessageParams{ChatID: telego.ChatID{ID: params.ChatID}, Text: params.Text})
		c.mu.Unlock()
		chatType := telego.ChatTypePrivate
		if params.ChatID < 0 {
			chatType = telego.ChatTypeSupergroup
		}
		return lifecycleAPIResponse(&telego.Message{
			MessageID: messageID,
			Chat:      telego.Chat{ID: params.ChatID, Type: chatType},
			Text:      params.Text,
		})
	case "approveChatJoinRequest":
		c.mu.Lock()
		c.approvals++
		c.mu.Unlock()
		return lifecycleAPIResponse(true)
	case "deleteMessage":
		return lifecycleAPIResponse(true)
	default:
		return nil, fmt.Errorf("unexpected Telegram method %q", method)
	}
}

func lifecycleAPIResponse(value any) (*ta.Response, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &ta.Response{Ok: true, Result: raw}, nil
}

func (c *lifecycleCaller) sentMessages() []telego.SendMessageParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]telego.SendMessageParams(nil), c.sent...)
}

func (c *lifecycleCaller) approvalCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.approvals
}

func lifecycleHandleUpdate(t *testing.T, bot *telego.Bot, handler th.Handler, update telego.Update) {
	t.Helper()
	botHandler, err := th.NewBotHandler(bot, nil)
	if err != nil {
		t.Fatal(err)
	}
	botHandler.Handle(handler)
	if err := botHandler.BaseGroup().HandleUpdate(context.Background(), bot, update); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeLifecycleStopRestart(t *testing.T) {
	const childEnvironment = "VERIFYBOT_TEST_UNEXPECTED_STREAM_EXIT"
	if os.Getenv(childEnvironment) == "1" {
		handlerDone := make(chan error, 1)
		handlerDone <- nil
		exitOnRuntimeError(runRuntimeLifecycle(context.Background(), runtimeLifecycle{handlerDone: handlerDone}))
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestRuntimeLifecycleStopRestart$")
	child.Env = append(os.Environ(), childEnvironment+"=1")
	output, err := child.CombinedOutput()
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() == 0 {
		t.Fatalf("unexpected update-stream end exit = %v, output %q; want non-zero", err, output)
	}

	const (
		groupID     int64 = -1009000000951
		applicantID int64 = 951
		botID       int64 = 950
	)
	stateDirectory := t.TempDir()
	configPath := filepath.Join(stateDirectory, "config.json")
	configData := []byte(`{"lang":"en","groups":[{"id":-1009000000951,"lang":"en","verify_mode":"kernel"}]}`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, settings, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	caller := &lifecycleCaller{botID: botID}
	bot := newRegistrationBot(t, caller)
	telegram := tg.New(bot)
	verification := verify.New(
		settings, telegram, cfg, &i18n.Messages, bot,
		verify.Identity{ID: botID, Username: "lifecycle_bot"}, stateDirectory,
	)
	join := telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat:       telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup},
		From:       telego.User{ID: applicantID, FirstName: "Applicant", LanguageCode: "en"},
		UserChatID: applicantID,
	}}
	lifecycleHandleUpdate(t, bot, verification.OnJoinRequest, join)
	answer := telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: applicantID, Type: telego.ChatTypePrivate},
		From: &telego.User{ID: applicantID, LanguageCode: "en"},
		Text: "6.12.3",
	}}
	if !verification.KernelAnswerDM(context.Background(), answer) {
		t.Fatal("lifecycle fixture did not establish a gradeable verification")
	}

	heartbeatPath := filepath.Join(stateDirectory, "heartbeat.json")
	writeHeartbeatRecord(t, heartbeatPath, time.Now().Add(-25*time.Hour))
	alerted := make(chan time.Duration, 1)
	observer := &retentionOutageObserver{heartbeatPath: heartbeatPath}
	observer.alert = func(outage time.Duration) {
		alertRetentionOutage(context.Background(), bot, cfg, settings.GroupIDs(), outage)
		alerted <- outage
	}
	beforeAlert := len(caller.sentMessages())
	if _, err := (&outageAwareBot{Bot: bot, observer: observer}).GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if outage := <-alerted; outage <= telegramUpdateRetention {
		t.Fatalf("retention outage = %v, want longer than %v", outage, telegramUpdateRetention)
	}
	alertMessages := caller.sentMessages()[beforeAlert:]
	wantAlert := i18n.Messages.Verification.Admin.OutageBacklog.Render(i18n.LangEN, groupID)
	if len(alertMessages) != 1 || alertMessages[0].Text != wantAlert {
		t.Fatalf("retention alerts = %#v, want one catalogue alert %q", alertMessages, wantAlert)
	}

	pendingPath := filepath.Join(stateDirectory, "pending.json")
	if err := os.Remove(pendingPath); err != nil {
		t.Fatal(err)
	}
	root, cancel := context.WithCancel(context.Background())
	handlerDone := make(chan error)
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	stopReturned := make(chan struct{})
	registrationWaitEntered := make(chan struct{})
	releaseRegistrationWait := make(chan struct{})
	registrationWaitReturned := make(chan struct{})
	heartbeatStopping := make(chan struct{})
	releaseHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		<-root.Done()
		close(heartbeatStopping)
		<-releaseHeartbeat
		close(heartbeatDone)
	}()
	const feedChatID int64 = -1009000000952
	off := false
	feedConfig := &config.FeedConfig{
		ChatID: feedChatID, Lang: "en", IntervalSeconds: 60, Bugs: &off, News: &off,
	}
	feedPath := filepath.Join(stateDirectory, fmt.Sprintf("feed-%d.json", feedChatID))
	actualFeedDone := make(chan struct{})
	go func() {
		feed.New(bot, []*config.FeedConfig{feedConfig}, stateDirectory).Run(root)
		close(actualFeedDone)
	}()
	for {
		if _, err := os.Stat(feedPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	if err := os.Remove(feedPath); err != nil {
		t.Fatal(err)
	}
	actualFeedFlushed := make(chan struct{})
	releaseFeed := make(chan struct{})
	feedDone := make(chan struct{})
	go func() {
		<-actualFeedDone
		close(actualFeedFlushed)
		<-releaseFeed
		close(feedDone)
	}()
	verificationFlushed := make(chan struct{})
	notifierDone := make(chan error, 1)
	lifecycleResult := make(chan error, 1)
	go func() {
		lifecycleResult <- runRuntimeLifecycle(root, runtimeLifecycle{
			handlerDone: handlerDone,
			stopHandlers: func(context.Context) error {
				close(stopEntered)
				<-releaseStop
				close(stopReturned)
				return nil
			},
			waitRegistration: func() {
				close(registrationWaitEntered)
				<-releaseRegistrationWait
				close(registrationWaitReturned)
			},
			heartbeatDone: heartbeatDone,
			flushVerification: func() {
				verification.Shutdown()
				close(verificationFlushed)
			},
			feedDone:         feedDone,
			notifierDone:     notifierDone,
			shutdownDeadline: time.Minute,
		})
	}()

	cancel()
	<-heartbeatStopping
	select {
	case <-stopEntered:
		t.Fatal("update handlers stopped before the fetched update stream drained")
	default:
	}
	close(handlerDone)
	<-stopEntered
	close(releaseStop)
	<-stopReturned
	<-registrationWaitEntered
	close(releaseRegistrationWait)
	<-registrationWaitReturned
	select {
	case <-verificationFlushed:
		t.Fatal("verification flushed before the heartbeat stopped")
	default:
	}
	close(releaseHeartbeat)
	<-verificationFlushed
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("verification shutdown did not flush pending state: %v", err)
	}
	<-actualFeedFlushed
	if _, err := os.Stat(feedPath); err != nil {
		t.Fatalf("final feed flush did not recreate state: %v", err)
	}
	select {
	case err := <-lifecycleResult:
		t.Fatalf("lifecycle returned before the final feed flush completed: %v", err)
	default:
	}
	close(releaseFeed)
	<-feedDone
	notifierDone <- nil
	if err := <-lifecycleResult; err != nil {
		t.Fatal(err)
	}

	restartedCfg, restartedSettings, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	restarted := verify.New(
		restartedSettings, telegram, restartedCfg, &i18n.Messages, bot,
		verify.Identity{ID: botID, Username: "lifecycle_bot"}, stateDirectory,
	)
	defer restarted.Shutdown()
	if !restarted.KernelAnswerDM(context.Background(), answer) {
		t.Fatal("verification state did not survive the stop/start cycle")
	}
	beforeApprove := caller.approvalCount()
	lifecycleHandleUpdate(t, bot, restarted.OnKernelAnswer, answer)
	if got := caller.approvalCount() - beforeApprove; got != 1 {
		t.Fatalf("restarted verification approvals = %d, want 1", got)
	}
}
