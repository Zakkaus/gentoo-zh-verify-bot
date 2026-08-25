package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

const (
	testBotID = int64(900)
	testOwner = int64(42)
)

type synchronizedLog struct {
	mu      sync.Mutex
	text    strings.Builder
	updated chan struct{}
}

func newSynchronizedLog() *synchronizedLog {
	return &synchronizedLog{updated: make(chan struct{}, 16)}
}

func (w *synchronizedLog) Write(value []byte) (int, error) {
	w.mu.Lock()
	n, err := w.text.Write(value)
	w.mu.Unlock()
	select {
	case w.updated <- struct{}{}:
	default:
	}
	return n, err
}

func (w *synchronizedLog) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.text.String()
}

func waitForLog(t *testing.T, output *synchronizedLog, text string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		if strings.Contains(output.String(), text) {
			return
		}
		select {
		case <-output.updated:
		case <-timer.C:
			t.Fatalf("timed out waiting for log %q in %q", text, output.String())
		}
	}
}

type registrationCaller struct {
	mu              sync.Mutex
	members         map[[2]int64]telego.ChatMember
	memberErrors    map[[2]int64]error
	memberRequests  [][2]int64
	sent            []telego.SendMessageParams
	sendAttempts    []telego.SendMessageParams
	sendErrors      map[int64]error
	left            []int64
	leaveAttempts   []int64
	leaveErrors     map[int64]error
	commandScopeIDs []int64
	events          chan string
	memberLookups   chan [2]int64
	lookupBlocks    map[[2]int64]<-chan struct{}
	leaveStarted    chan int64
	releaseLeave    <-chan struct{}
	botID           int64
}

func (c *registrationCaller) Call(_ context.Context, endpoint string, data *ta.RequestData) (*ta.Response, error) {
	method := endpoint[strings.LastIndexByte(endpoint, '/')+1:]
	switch method {
	case "getChatMember":
		var params struct {
			ChatID int64 `json:"chat_id"`
			UserID int64 `json:"user_id"`
		}
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		key := [2]int64{params.ChatID, params.UserID}
		c.mu.Lock()
		member := c.members[key]
		memberErr := c.memberErrors[key]
		c.memberRequests = append(c.memberRequests, key)
		block := c.lookupBlocks[key]
		c.mu.Unlock()
		if c.memberLookups != nil {
			c.memberLookups <- key
		}
		if memberErr != nil {
			return nil, memberErr
		}
		if member == nil {
			return nil, fmt.Errorf("no member response for chat %d user %d", params.ChatID, params.UserID)
		}
		if block != nil {
			<-block
		}
		if c.events != nil {
			c.events <- method
		}
		return registrationAPIResponse(member)
	case "sendMessage":
		var wire struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		if err := json.Unmarshal(data.BodyRaw, &wire); err != nil {
			return nil, err
		}
		message := telego.SendMessageParams{
			ChatID: telego.ChatID{ID: wire.ChatID},
			Text:   wire.Text,
		}
		c.mu.Lock()
		c.sendAttempts = append(c.sendAttempts, message)
		sendErr := c.sendErrors[wire.ChatID]
		messageID := 0
		if sendErr == nil {
			c.sent = append(c.sent, message)
			messageID = len(c.sent)
		}
		c.mu.Unlock()
		if sendErr != nil {
			return nil, sendErr
		}
		if c.events != nil {
			c.events <- method
		}
		return registrationAPIResponse(&telego.Message{MessageID: messageID})
	case "leaveChat":
		var params struct {
			ChatID int64 `json:"chat_id"`
		}
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.leaveAttempts = append(c.leaveAttempts, params.ChatID)
		leaveErr := c.leaveErrors[params.ChatID]
		c.mu.Unlock()
		if c.leaveStarted != nil {
			c.leaveStarted <- params.ChatID
		}
		if leaveErr != nil {
			return nil, leaveErr
		}
		if c.releaseLeave != nil {
			<-c.releaseLeave
		}
		c.mu.Lock()
		c.left = append(c.left, params.ChatID)
		botID := c.botID
		if botID == 0 {
			botID = testBotID
		}
		c.members[[2]int64{params.ChatID, botID}] = &telego.ChatMemberLeft{
			Status: telego.MemberStatusLeft,
			User:   telego.User{ID: botID},
		}
		c.mu.Unlock()
		if c.events != nil {
			c.events <- method
		}
		return registrationAPIResponse(true)
	case "deleteMessage":
		return registrationAPIResponse(true)
	case "setMyCommands":
		var params struct {
			Scope struct {
				ChatID int64 `json:"chat_id"`
			} `json:"scope"`
		}
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		if params.Scope.ChatID != 0 {
			c.mu.Lock()
			c.commandScopeIDs = append(c.commandScopeIDs, params.Scope.ChatID)
			c.mu.Unlock()
		}
		return registrationAPIResponse(true)
	default:
		return nil, fmt.Errorf("unexpected Telegram method %q", method)
	}
}

func (c *registrationCaller) leftChats() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.left...)
}

func (c *registrationCaller) leaveAttemptsForTest() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.leaveAttempts...)
}

func (c *registrationCaller) membershipRequestsForTest() [][2]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][2]int64(nil), c.memberRequests...)
}

func (c *registrationCaller) sentTo(chatID int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, message := range c.sent {
		if message.ChatID.ID == chatID {
			count++
		}
	}
	return count
}

func (c *registrationCaller) messagesTo(chatID int64) []telego.SendMessageParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	var messages []telego.SendMessageParams
	for _, message := range c.sent {
		if message.ChatID.ID == chatID {
			messages = append(messages, message)
		}
	}
	return messages
}

func (c *registrationCaller) sendAttemptsTo(chatID int64) []telego.SendMessageParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	var messages []telego.SendMessageParams
	for _, message := range c.sendAttempts {
		if message.ChatID.ID == chatID {
			messages = append(messages, message)
		}
	}
	return messages
}

func (c *registrationCaller) hasCommandScope(groupID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, chatID := range c.commandScopeIDs {
		if chatID == groupID {
			return true
		}
	}
	return false
}

func registrationAPIResponse(value any) (*ta.Response, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &ta.Response{Ok: true, Result: raw}, nil
}

func newRegistrationBot(t *testing.T, caller ta.Caller) *telego.Bot {
	t.Helper()
	bot, err := telego.NewBot("1:"+strings.Repeat("a", 35), telego.WithAPICaller(caller), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return bot
}

func runRegistrationUpdate(t *testing.T, bot *telego.Bot, service *registrationService, update telego.Update) {
	t.Helper()
	updates := make(chan telego.Update, 1)
	handler, err := th.NewBotHandler(bot, updates)
	if err != nil {
		t.Fatal(err)
	}
	processed := make(chan error, 1)
	handler.Use(func(ctx *th.Context, update telego.Update) error {
		err := ctx.Next(update)
		processed <- err
		return err
	})
	service.Register(handler)
	started := make(chan error, 1)
	go func() { started <- handler.Start() }()
	updates <- update
	if err := <-processed; err != nil {
		t.Fatalf("handler returned %v", err)
	}
	close(updates)
	if err := <-started; err != nil {
		t.Fatalf("handler returned %v", err)
	}
}
func TestRegistrationGlobalDispatch(t *testing.T) {
	const (
		knownGroup   int64 = -1009000000901
		unknownGroup int64 = -1009000000902
		userID       int64 = 901
	)
	cfg, settings := registrationFixture(t)
	registration := settings.Registrations()
	registration.RegisteredGroups = []store.RegisteredGroup{{ID: knownGroup, RegisteredBy: testOwner}}
	if _, err := settings.CommitRegistrations(registration.Revision, registration); err != nil {
		t.Fatal(err)
	}
	service := newRegistrationService(
		context.Background(), newRegistrationBot(t, &registrationCaller{members: make(map[[2]int64]telego.ChatMember)}),
		settings, cfg, "test_bot", testBotID, nil, nil, nil,
	)

	privateStart := func(payload string) telego.Update {
		text := "/start"
		if payload != "" {
			text += " " + payload
		}
		return telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: userID, Type: telego.ChatTypePrivate},
			From: &telego.User{ID: userID},
			Text: text,
		}}
	}
	membership := func(groupID int64) telego.Update {
		return telego.Update{MyChatMember: &telego.ChatMemberUpdated{
			Chat: telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup},
			From: telego.User{ID: testOwner},
			OldChatMember: &telego.ChatMemberLeft{
				Status: telego.MemberStatusLeft,
				User:   telego.User{ID: testBotID},
			},
			NewChatMember: &telego.ChatMemberMember{
				Status: telego.MemberStatusMember,
				User:   telego.User{ID: testBotID},
			},
		}}
	}

	fallbackHandler := th.Handler(func(_ *th.Context, _ telego.Update) error { return nil })
	tests := []struct {
		name   string
		update telego.Update
		want   string
	}{
		{name: "owner payload", update: privateStart("owner_nonce"), want: handlerFunctionName(service.onOwnerClaim)},
		{name: "enrollment payload", update: privateStart("enroll_nonce"), want: handlerFunctionName(service.onEnrollmentStart)},
		{name: "panel payload", update: privateStart("panel_token"), want: handlerFunctionName(fallbackHandler)},
		{name: "scoped verification payload", update: privateStart("verify_-100"), want: handlerFunctionName(fallbackHandler)},
		{name: "bare verification payload", update: privateStart("verify"), want: handlerFunctionName(fallbackHandler)},
		{name: "no payload", update: privateStart(""), want: handlerFunctionName(fallbackHandler)},
		{name: "unknown group membership", update: membership(unknownGroup), want: handlerFunctionName(service.onMyChatMember)},
		{name: "known group membership", update: membership(knownGroup), want: handlerFunctionName(service.onEffectiveMembershipUpdate)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, err := th.NewBotHandler(service.bot, nil)
			if err != nil {
				t.Fatal(err)
			}
			var handled []string
			for _, route := range service.handlerRoutes() {
				actual := handlerFunctionName(route.handler)
				handler.Handle(func(_ *th.Context, _ telego.Update) error {
					handled = append(handled, actual)
					return nil
				}, route.predicates...)
			}
			handler.Handle(func(_ *th.Context, _ telego.Update) error {
				handled = append(handled, handlerFunctionName(fallbackHandler))
				return nil
			}, th.CommandEqual("start"))
			if err := handler.BaseGroup().HandleUpdate(context.Background(), service.bot, test.update); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(handled, []string{test.want}) {
				t.Fatalf("handlers = %v, want only %v", handled, test.want)
			}
		})
	}
}

func handlerFunctionName(handler th.Handler) string {
	return runtime.FuncForPC(reflect.ValueOf(handler).Pointer()).Name()
}

func waitForRegistrationMethod(t *testing.T, caller *registrationCaller, method string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-caller.events:
			if got == method {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for Telegram method %q", method)
		}
	}
}

func registrationFixture(t *testing.T) (*config.Config, *store.Settings) {
	t.Helper()
	missingConfig := t.TempDir() + "/missing-config.json"
	cfg, settings, err := loadRuntimeState(missingConfig, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg, settings
}

func registrationStateFromDisk(t *testing.T, configPath, stateDirectory string) store.RegistrationState {
	t.Helper()
	_, settings, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	return settings.Registrations()
}

func assertRegistrationStateEqual(t *testing.T, got, want store.RegistrationState) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registration state = %+v, want %+v", got, want)
	}
}

func bindTestOwner(t *testing.T, settings *store.Settings, now time.Time) {
	t.Helper()
	nonce, _, err := settings.EnsureOwnerClaim(now, ownerClaimLifetime)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.ClaimOwner(testOwner, nonce, now); err != nil {
		t.Fatal(err)
	}
}

func adminMember(userID int64) telego.ChatMember {
	return &telego.ChatMemberAdministrator{
		Status: telego.MemberStatusAdministrator,
		User:   telego.User{ID: userID},
	}
}

func plainMember(userID int64) telego.ChatMember {
	return &telego.ChatMemberMember{
		Status: telego.MemberStatusMember,
		User:   telego.User{ID: userID},
	}
}

func TestStartupStateAllowsMissingConfigAndNoGroups(t *testing.T) {
	configPath := t.TempDir() + "/config.json"
	cfg, settings, err := loadRuntimeState(configPath, t.TempDir())
	if err != nil {
		t.Fatalf("startup state: %v", err)
	}
	if len(cfg.Groups) != 0 || len(settings.GroupIDs()) != 0 {
		t.Fatalf("startup groups = config %v, settings %v; want none", cfg.GroupIDs, settings.GroupIDs())
	}
	if status := settings.Persistence(); !status.Durable || !status.Writable {
		t.Fatalf("settings persistence = %+v, want durable and writable", status)
	}
	if nonce, created, err := settings.EnsureOwnerClaim(time.Now(), ownerClaimLifetime); err != nil || !created || nonce == "" {
		t.Fatalf("owner claim on zero-group startup = nonce %q, created %t, error %v", nonce, created, err)
	}
}

func TestStartupConfigRemainsRegistrationBaseline(t *testing.T) {
	const groupID int64 = -1009000000601
	configPath := t.TempDir() + "/missing-config.json"
	stateDirectory := t.TempDir()
	_, settings, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	registration := settings.Registrations()
	registration.RegisteredGroups = []store.RegisteredGroup{{ID: groupID, RegisteredBy: 42}}
	if _, err := settings.CommitRegistrations(registration.Revision, registration); err != nil {
		t.Fatal(err)
	}
	cfg, reloaded, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.IsGroup(groupID) {
		t.Fatal("runtime group was not restored into live settings")
	}
	if cfg.IsGroup(groupID) {
		t.Fatal("runtime group leaked into the immutable config baseline")
	}
}

func TestOwnerClaimIsFirstUserSingleUse(t *testing.T) {
	cfg, settings := registrationFixture(t)
	now := time.Unix(2_000_000_000, 0)
	caller := &registrationCaller{members: make(map[[2]int64]telego.ChatMember), events: make(chan string, 16)}
	bot := newRegistrationBot(t, caller)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := newRegistrationService(ctx, bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	service.now = func() time.Time { return now }

	logs := newSynchronizedLog()
	oldLog := log.Writer()
	log.SetOutput(logs)
	defer log.SetOutput(oldLog)
	if err := service.EnsureOwnerClaim(); err != nil {
		t.Fatal(err)
	}
	claim := settings.Registrations().OwnerClaimNonce
	message := func(userID int64) telego.Update {
		return telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: userID, Type: telego.ChatTypePrivate},
			From: &telego.User{ID: userID, LanguageCode: "en"},
			Text: "/start owner_" + claim,
		}}
	}
	runRegistrationUpdate(t, bot, service, message(testOwner))
	waitForRegistrationMethod(t, caller, "sendMessage")
	runRegistrationUpdate(t, bot, service, message(testOwner+1))
	waitForRegistrationMethod(t, caller, "sendMessage")
	waitForLog(t, logs, "owner claim refused: user=43")
	state := settings.Registrations()
	if state.OwnerID != testOwner || state.OwnerClaimNonce != "" || state.OwnerClaimExpiresAt != 0 {
		t.Fatalf("owner state = %+v", state)
	}
	if !strings.Contains(logs.String(), "owner claim refused: user=43") {
		t.Fatalf("refused replay was not logged: %s", logs.String())
	}
}

func TestEnrollmentNoncePromotionReplayAndExpiry(t *testing.T) {
	cfg, settings := registrationFixture(t)
	now := time.Unix(2_000_000_000, 0)
	bindTestOwner(t, settings, now)
	caller := &registrationCaller{members: make(map[[2]int64]telego.ChatMember), events: make(chan string, 32)}
	bot := newRegistrationBot(t, caller)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := newRegistrationService(ctx, bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	service.now = func() time.Time { return now }

	const (
		actor  = int64(77)
		groupA = int64(-1001)
		groupB = int64(-1002)
		groupC = int64(-1003)
	)
	caller.members[[2]int64{groupA, actor}] = adminMember(actor)
	caller.members[[2]int64{groupA, testBotID}] = plainMember(testBotID)
	nonce, err := settings.IssueEnrollmentNonce(testOwner, now, enrollmentLifetime)
	if err != nil {
		t.Fatal(err)
	}
	runRegistrationUpdate(t, bot, service, telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: groupA, Type: telego.ChatTypeSupergroup, Title: "A"},
		From: &telego.User{ID: actor, LanguageCode: "en"},
		Text: "/start enroll_" + nonce.Nonce,
	}})
	waitForRegistrationMethod(t, caller, "sendMessage")
	if settings.IsGroup(groupA) || len(settings.Registrations().PendingRegistrations) != 1 {
		t.Fatalf("group should be pending before promotion: %+v", settings.Registrations())
	}
	caller.members[[2]int64{groupA, testBotID}] = adminMember(testBotID)
	runRegistrationUpdate(t, bot, service, telego.Update{MyChatMember: &telego.ChatMemberUpdated{
		Chat:          telego.Chat{ID: groupA, Type: telego.ChatTypeSupergroup, Title: "A"},
		From:          telego.User{ID: actor, LanguageCode: "en"},
		OldChatMember: &telego.ChatMemberMember{Status: telego.MemberStatusMember, User: telego.User{ID: testBotID}},
		NewChatMember: adminMember(testBotID),
	}})
	waitForRegistrationMethod(t, caller, "sendMessage")
	if !settings.IsGroup(groupA) || len(settings.Registrations().PendingRegistrations) != 0 {
		t.Fatalf("promoted group was not registered: %+v", settings.Registrations())
	}

	caller.members[[2]int64{groupB, actor}] = adminMember(actor)
	caller.members[[2]int64{groupB, testBotID}] = plainMember(testBotID)
	runRegistrationUpdate(t, bot, service, telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: groupB, Type: telego.ChatTypeSupergroup, Title: "B"},
		From: &telego.User{ID: actor, LanguageCode: "en"},
		Text: "/start enroll_" + nonce.Nonce,
	}})
	waitForRegistrationMethod(t, caller, "leaveChat")
	if settings.IsGroup(groupB) || len(caller.left) != 1 || caller.left[0] != groupB {
		t.Fatalf("nonce replay result: groups=%v leaves=%v", settings.GroupIDs(), caller.left)
	}

	expired, err := settings.IssueEnrollmentNonce(testOwner, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(2 * time.Second) }
	caller.members[[2]int64{groupC, actor}] = adminMember(actor)
	caller.members[[2]int64{groupC, testBotID}] = plainMember(testBotID)
	runRegistrationUpdate(t, bot, service, telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: groupC, Type: telego.ChatTypeSupergroup, Title: "C"},
		From: &telego.User{ID: actor, LanguageCode: "en"},
		Text: "/start enroll_" + expired.Nonce,
	}})
	waitForRegistrationMethod(t, caller, "leaveChat")
	if settings.IsGroup(groupC) || len(caller.left) != 2 || caller.left[1] != groupC {
		t.Fatalf("expired nonce result: groups=%v leaves=%v", settings.GroupIDs(), caller.left)
	}
}

func TestOwnerPromotionRegistersFirstControlGroup(t *testing.T) {
	cfg, settings := registrationFixture(t)
	now := time.Unix(2_000_000_000, 0)
	bindTestOwner(t, settings, now)
	const groupID = int64(-1901)
	caller := &registrationCaller{
		members: map[[2]int64]telego.ChatMember{
			{groupID, testOwner}: adminMember(testOwner),
			{groupID, testBotID}: adminMember(testBotID),
		},
		events: make(chan string, 16),
	}
	bot := newRegistrationBot(t, caller)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := newRegistrationService(ctx, bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	service.now = func() time.Time { return now }
	runRegistrationUpdate(t, bot, service, telego.Update{MyChatMember: &telego.ChatMemberUpdated{
		Chat:          telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: "Owner Group"},
		From:          telego.User{ID: testOwner, LanguageCode: "en"},
		OldChatMember: &telego.ChatMemberLeft{Status: telego.MemberStatusLeft, User: telego.User{ID: testBotID}},
		NewChatMember: adminMember(testBotID),
	}})
	waitForRegistrationMethod(t, caller, "sendMessage")
	state := settings.Registrations()
	if !settings.IsGroup(groupID) || state.ControlGroupID != groupID || len(state.RegisteredGroups) != 1 {
		t.Fatalf("owner registration = %+v, groups=%v", state, settings.GroupIDs())
	}
	if len(caller.left) != 0 || len(caller.sent) != 1 {
		t.Fatalf("owner registration Telegram calls: sent=%+v left=%v", caller.sent, caller.left)
	}
	want := i18n.Messages.Bot.Registration.GroupRegistered.Render(i18n.LangEN, "Owner Group")
	if caller.sent[0].Text != want {
		t.Fatalf("owner registration message = %q, want catalogue text %q", caller.sent[0].Text, want)
	}
	if strings.Contains(caller.sent[0].Text, "?start=") {
		t.Fatalf("owner registration returned an unroutable start payload: %q", caller.sent[0].Text)
	}
}

func TestRegistrationCompletedMessageLocales(t *testing.T) {
	const (
		groupID = int64(-1902)
		title   = "Runtime Group"
	)
	for _, test := range []struct {
		name string
		code string
		lang i18n.Lang
	}{
		{name: "zh", code: "zh-CN", lang: i18n.LangZH},
		{name: "zh-Hant", code: "zh-TW", lang: i18n.LangZHHant},
		{name: "en", code: "en", lang: i18n.LangEN},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, settings := registrationFixture(t)
			caller := &registrationCaller{members: make(map[[2]int64]telego.ChatMember)}
			bot := newRegistrationBot(t, caller)
			service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
			service.registrationCompleted(context.Background(),
				telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: title},
				telego.User{ID: testOwner, LanguageCode: test.code})
			if len(caller.sent) != 1 {
				t.Fatalf("registration messages = %d, want 1", len(caller.sent))
			}
			want := i18n.Messages.Bot.Registration.GroupRegistered.Render(test.lang, title)
			if caller.sent[0].Text != want {
				t.Errorf("registration message = %q, want catalogue text %q", caller.sent[0].Text, want)
			}
			if strings.Contains(caller.sent[0].Text, "?start=") {
				t.Errorf("registration message returned an unroutable start payload: %q", caller.sent[0].Text)
			}
		})
	}
}

func TestNonOwnerPromotionAttemptLeaves(t *testing.T) {
	cfg, settings := registrationFixture(t)
	now := time.Unix(2_000_000_000, 0)
	bindTestOwner(t, settings, now)
	const (
		actor   = int64(77)
		groupID = int64(-2001)
	)
	caller := &registrationCaller{members: map[[2]int64]telego.ChatMember{
		{groupID, actor}: adminMember(actor),
	}}
	caller.events = make(chan string, 16)
	bot := newRegistrationBot(t, caller)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := newRegistrationService(ctx, bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	service.now = func() time.Time { return now }
	runRegistrationUpdate(t, bot, service, telego.Update{MyChatMember: &telego.ChatMemberUpdated{
		Chat:          telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: "Unauthorized"},
		From:          telego.User{ID: actor, LanguageCode: "en"},
		OldChatMember: &telego.ChatMemberLeft{Status: telego.MemberStatusLeft, User: telego.User{ID: testBotID}},
		NewChatMember: adminMember(testBotID),
	}})
	waitForRegistrationMethod(t, caller, "leaveChat")
	if settings.IsGroup(groupID) || len(caller.left) != 1 || caller.left[0] != groupID {
		t.Fatalf("non-owner attempt: groups=%v leaves=%v", settings.GroupIDs(), caller.left)
	}
}

func TestUnknownGroupGraceExpiry(t *testing.T) {
	newFixture := func(t *testing.T) (*registrationService, *registrationCaller) {
		t.Helper()
		cfg, settings := registrationFixture(t)
		caller := &registrationCaller{
			members: make(map[[2]int64]telego.ChatMember),
			events:  make(chan string, 16),
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		service := newRegistrationService(ctx, newRegistrationBot(t, caller), settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
		return service, caller
	}

	t.Run("expired unknown group leaves exactly once", func(t *testing.T) {
		const groupID = int64(-3001)
		service, caller := newFixture(t)
		service.scheduleUnknownLeave(groupID, "Expired", time.Now().Add(-time.Second))
		waitForRegistrationMethod(t, caller, "leaveChat")
		time.Sleep(20 * time.Millisecond)
		left := caller.leftChats()
		if len(left) != 1 || left[0] != groupID {
			t.Fatalf("expired unknown group leaves = %v, want [%d]", left, groupID)
		}
	})

	t.Run("registered group is retained", func(t *testing.T) {
		const groupID = int64(-3002)
		service, caller := newFixture(t)
		state := service.settings.Registrations()
		state.RegisteredGroups = []store.RegisteredGroup{{ID: groupID, RegisteredBy: testOwner, Title: "Registered"}}
		if _, err := service.settings.CommitRegistrations(state.Revision, state); err != nil {
			t.Fatal(err)
		}
		service.scheduleUnknownLeave(groupID, "Registered", time.Now().Add(-time.Second))
		select {
		case method := <-caller.events:
			t.Fatalf("registered group triggered Telegram method %q", method)
		case <-time.After(20 * time.Millisecond):
		}
		if left := caller.leftChats(); len(left) != 0 {
			t.Fatalf("registered group leaves = %v, want none", left)
		}
	})

	t.Run("later deadline does not double fire", func(t *testing.T) {
		const groupID = int64(-3003)
		service, caller := newFixture(t)
		now := time.Now()
		service.scheduleUnknownLeave(groupID, "Duplicate", now.Add(50*time.Millisecond))
		service.scheduleUnknownLeave(groupID, "Duplicate", now.Add(100*time.Millisecond))
		waitForRegistrationMethod(t, caller, "leaveChat")
		time.Sleep(20 * time.Millisecond)
		left := caller.leftChats()
		if len(left) != 1 || left[0] != groupID {
			t.Fatalf("duplicate deadline leaves = %v, want [%d]", left, groupID)
		}
	})
}

func TestRegistrationAndUnknownLeaveAreSerialized(t *testing.T) {
	cfg, settings := registrationFixture(t)
	now := time.Unix(2_000_000_000, 0)
	bindTestOwner(t, settings, now)
	const (
		actor   = int64(77)
		groupID = int64(-4001)
	)
	nonce, err := settings.IssueEnrollmentNonce(testOwner, now, enrollmentLifetime)
	if err != nil {
		t.Fatal(err)
	}
	releaseLeave := make(chan struct{})
	releaseActorLookup := make(chan struct{})
	caller := &registrationCaller{
		members: map[[2]int64]telego.ChatMember{
			{groupID, actor}:     adminMember(actor),
			{groupID, testBotID}: adminMember(testBotID),
		},
		memberLookups: make(chan [2]int64, 8),
		lookupBlocks: map[[2]int64]<-chan struct{}{
			{groupID, actor}: releaseActorLookup,
		},
		leaveStarted: make(chan int64, 1),
		releaseLeave: releaseLeave,
		events:       make(chan string, 8),
	}
	bot := newRegistrationBot(t, caller)
	service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	service.now = func() time.Time { return now }

	leaveDone := make(chan struct{})
	go func() {
		service.leaveUnknown(context.Background(), telego.Chat{
			ID: groupID, Type: telego.ChatTypeSupergroup, Title: "Race",
		}, actor, "test race")
		close(leaveDone)
	}()
	if leftGroup := <-caller.leaveStarted; leftGroup != groupID {
		t.Fatalf("leave started for %d, want %d", leftGroup, groupID)
	}

	updates := make(chan telego.Update, 1)
	handler, err := th.NewBotHandler(bot, updates)
	if err != nil {
		t.Fatal(err)
	}
	processed := make(chan error, 1)
	handler.Use(func(ctx *th.Context, update telego.Update) error {
		err := ctx.Next(update)
		processed <- err
		return err
	})
	service.Register(handler)
	handlerDone := make(chan error, 1)
	go func() { handlerDone <- handler.Start() }()
	updates <- telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: "Race"},
		From: &telego.User{ID: actor, LanguageCode: "en"},
		Text: "/start enroll_" + nonce.Nonce,
	}}
	if lookup := <-caller.memberLookups; lookup != [2]int64{groupID, actor} {
		t.Fatalf("first membership lookup = %v, want actor lookup", lookup)
	}
	close(releaseLeave)
	<-leaveDone
	close(releaseActorLookup)
	if err := <-processed; err != nil {
		t.Fatal(err)
	}
	close(updates)
	if err := <-handlerDone; err != nil {
		t.Fatal(err)
	}
	if settings.IsGroup(groupID) {
		t.Fatalf("group %d was registered from membership observed before LeaveChat completed", groupID)
	}
}

func TestUnknownGroupLeaveSurvivesRestart(t *testing.T) {
	const (
		actor   = int64(78)
		groupID = int64(-4002)
	)
	configPath := filepath.Join(t.TempDir(), "missing-config.json")
	stateDirectory := t.TempDir()
	cfg, settings, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	bindTestOwner(t, settings, now)
	caller := &registrationCaller{members: map[[2]int64]telego.ChatMember{
		{groupID, actor}:     adminMember(actor),
		{groupID, testBotID}: plainMember(testBotID),
	}}
	firstRoot, cancelFirst := context.WithCancel(context.Background())
	first := newRegistrationService(firstRoot, newRegistrationBot(t, caller), settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	t.Cleanup(func() {
		cancelFirst()
		first.Wait()
	})
	first.now = func() time.Time { return now }
	runRegistrationUpdate(t, first.bot, first, telego.Update{MyChatMember: &telego.ChatMemberUpdated{
		Chat:          telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: "Restart"},
		From:          telego.User{ID: actor, LanguageCode: "en"},
		OldChatMember: &telego.ChatMemberLeft{Status: telego.MemberStatusLeft, User: telego.User{ID: testBotID}},
		NewChatMember: plainMember(testBotID),
	}})
	cancelFirst()
	first.Wait()

	settingsPath := filepath.Join(stateDirectory, "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	leaves, ok := state["unknown_group_leaves"].([]any)
	if !ok || len(leaves) != 1 {
		t.Fatalf("persisted unknown-group leaves = %#v, want one cleanup record", state["unknown_group_leaves"])
	}
	record, ok := leaves[0].(map[string]any)
	if !ok {
		t.Fatalf("unknown-group leave record = %#v", leaves[0])
	}
	record["expires_at"] = time.Now().Add(-time.Second).Unix()
	raw, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	reloadedCfg, reloadedSettings, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	restartedCaller := &registrationCaller{
		members: make(map[[2]int64]telego.ChatMember),
		events:  make(chan string, 4),
	}
	secondRoot, cancelSecond := context.WithCancel(context.Background())
	second := newRegistrationService(secondRoot, newRegistrationBot(t, restartedCaller), reloadedSettings, reloadedCfg, "verify_test_bot", testBotID, nil, nil, nil)
	t.Cleanup(func() {
		cancelSecond()
		second.Wait()
	})
	waitForRegistrationMethod(t, restartedCaller, "leaveChat")
	if left := restartedCaller.leftChats(); len(left) != 1 || left[0] != groupID {
		t.Fatalf("restart leaves = %v, want [%d]", left, groupID)
	}
	cancelSecond()
	second.Wait()
}

func TestEffectiveGroupDemotionRunsOneSetupReport(t *testing.T) {
	cfg, settings := registrationFixture(t)
	const groupID = int64(-4003)
	state := settings.Registrations()
	state.RegisteredGroups = []store.RegisteredGroup{{ID: groupID, RegisteredBy: testOwner}}
	if _, err := settings.CommitRegistrations(state.Revision, state); err != nil {
		t.Fatal(err)
	}
	reports := make(chan int64, 2)
	caller := &registrationCaller{members: make(map[[2]int64]telego.ChatMember)}
	bot := newRegistrationBot(t, caller)
	service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, func(_ context.Context, reportedGroupID int64) { reports <- reportedGroupID }, nil)
	update := telego.Update{MyChatMember: &telego.ChatMemberUpdated{
		Chat:          telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: "Demoted"},
		From:          telego.User{ID: 79, LanguageCode: "en"},
		OldChatMember: adminMember(testBotID),
		NewChatMember: plainMember(testBotID),
	}}
	runRegistrationUpdate(t, bot, service, update)
	select {
	case reportedGroupID := <-reports:
		if reportedGroupID != groupID {
			t.Fatalf("setup report group = %d, want %d", reportedGroupID, groupID)
		}
	default:
		t.Fatal("effective-group demotion did not run the setup report")
	}
	runRegistrationUpdate(t, bot, service, update)
	select {
	case reportedGroupID := <-reports:
		t.Fatalf("flapping group produced an unthrottled second report for %d", reportedGroupID)
	default:
	}
	if !settings.IsGroup(groupID) {
		t.Fatal("membership loss automatically erased an owner registration")
	}
}

func TestOwnerCanUnregisterRuntimeGroupAndDropOverrides(t *testing.T) {
	const groupID = int64(-4004)
	configPath := filepath.Join(t.TempDir(), "missing-config.json")
	stateDirectory := t.TempDir()
	cfg, settings, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000_000, 0)
	bindTestOwner(t, settings, now)
	state := settings.Registrations()
	state.RegisteredGroups = []store.RegisteredGroup{{ID: groupID, RegisteredBy: testOwner, Title: "Remove"}}
	state.ControlGroupID = groupID
	if _, err := settings.CommitRegistrations(state.Revision, state); err != nil {
		t.Fatal(err)
	}
	group, ok := settings.Group(groupID)
	if !ok {
		t.Fatal("registered group is not effective")
	}
	overrides := group.Overrides()
	disabled := false
	overrides.Enabled = &disabled
	if _, err := settings.CommitGroup(groupID, group.Revision(), overrides); err != nil {
		t.Fatal(err)
	}
	releaseLeave := make(chan struct{}, 1)
	caller := &registrationCaller{
		members:      make(map[[2]int64]telego.ChatMember),
		events:       make(chan string, 8),
		leaveStarted: make(chan int64, 1),
		releaseLeave: releaseLeave,
	}
	bot := newRegistrationBot(t, caller)
	type removalEvent struct {
		groupID int64
		durable bool
	}
	removed := make(chan removalEvent, 1)
	onRemoved := func(removedGroupID int64) {
		removed <- removalEvent{groupID: removedGroupID, durable: !settings.IsGroup(removedGroupID)}
	}
	service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, onRemoved)
	command := func(userID int64) telego.Update {
		return telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: userID, Type: telego.ChatTypePrivate},
			From: &telego.User{ID: userID, LanguageCode: "en"},
			Text: fmt.Sprintf("/unregister %d", groupID),
		}}
	}
	runRegistrationUpdate(t, bot, service, command(testOwner+1))
	if !settings.IsGroup(groupID) {
		t.Fatal("non-owner unregistered a runtime group")
	}
	nonOwnerMessages := caller.messagesTo(testOwner + 1)
	if len(nonOwnerMessages) != 1 {
		t.Fatalf("non-owner unregister messages = %d, want 1", len(nonOwnerMessages))
	}
	wantOwnerOnly := i18n.Messages.Bot.Registration.UnregisterOwnerOnly.For(i18n.LangEN)
	if nonOwnerMessages[0].Text != wantOwnerOnly {
		t.Fatalf("non-owner unregister message = %q, want catalogue text %q", nonOwnerMessages[0].Text, wantOwnerOnly)
	}
	ownerDone := make(chan struct{})
	go func() {
		runRegistrationUpdate(t, bot, service, command(testOwner))
		close(ownerDone)
	}()
	select {
	case leftGroupID := <-caller.leaveStarted:
		if leftGroupID != groupID {
			t.Errorf("LeaveChat started for %d, want %d", leftGroupID, groupID)
		}
	case <-time.After(time.Second):
		t.Fatal("owner unregister did not reach LeaveChat")
	}
	select {
	case event := <-removed:
		if event.groupID != groupID || !event.durable {
			t.Errorf("group-removal transition before LeaveChat = %+v, want group %d after durable removal", event, groupID)
		}
	default:
		t.Error("LeaveChat started before the group-removal transition")
	}
	releaseLeave <- struct{}{}
	select {
	case <-ownerDone:
	case <-time.After(time.Second):
		t.Fatal("owner unregister did not finish after LeaveChat release")
	}
	if settings.IsGroup(groupID) {
		t.Fatal("owner unregister did not remove the runtime group")
	}
	if left := caller.leftChats(); len(left) != 1 || left[0] != groupID {
		t.Fatalf("unregister leaves = %v, want [%d]", left, groupID)
	}
	ownerMessages := caller.messagesTo(testOwner)
	if len(ownerMessages) != 1 {
		t.Fatalf("owner unregister messages = %d, want 1", len(ownerMessages))
	}
	wantRemoved := i18n.Messages.Bot.Registration.GroupUnregistered.Render(i18n.LangEN, "Remove")
	if ownerMessages[0].Text != wantRemoved {
		t.Fatalf("owner unregister message = %q, want catalogue text %q", ownerMessages[0].Text, wantRemoved)
	}
	registration := settings.Registrations()
	if registration.ControlGroupID != 0 || len(registration.RegisteredGroups) != 0 ||
		len(registration.UnknownGroupLeaves) != 0 {
		t.Fatalf("registration state after unregister = %+v", registration)
	}
	raw, err := os.ReadFile(filepath.Join(stateDirectory, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		Groups map[string]json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if _, ok := persisted.Groups[fmt.Sprint(groupID)]; ok {
		t.Fatalf("orphaned override for group %d remained in settings.json", groupID)
	}
}

func TestOwnerClaimPersistenceFailureUsesClaimMessage(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing-config.json")
	stateDirectory := t.TempDir()
	cfg, settings, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000_000, 0)
	caller := &registrationCaller{
		members: make(map[[2]int64]telego.ChatMember),
		events:  make(chan string, 4),
	}
	bot := newRegistrationBot(t, caller)
	service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	service.now = func() time.Time { return now }
	if err := service.EnsureOwnerClaim(); err != nil {
		t.Fatal(err)
	}
	claim := settings.Registrations().OwnerClaimNonce
	if err := os.RemoveAll(stateDirectory); err != nil {
		t.Fatal(err)
	}
	runRegistrationUpdate(t, bot, service, telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: testOwner, Type: telego.ChatTypePrivate},
		From: &telego.User{ID: testOwner, LanguageCode: "en"},
		Text: "/start owner_" + claim,
	}})
	waitForRegistrationMethod(t, caller, "sendMessage")
	messages := caller.messagesTo(testOwner)
	if len(messages) != 1 {
		t.Fatalf("owner-claim failure messages = %d, want 1", len(messages))
	}
	want := i18n.Messages.Bot.Registration.OwnerClaimSaveFailed.For(i18n.LangEN)
	if messages[0].Text != want {
		t.Fatalf("owner-claim persistence failure message = %q, want catalogue text %q", messages[0].Text, want)
	}
}

func TestUnknownCleanupRecordCannotAuthorizePromotion(t *testing.T) {
	const (
		actor   = int64(80)
		groupID = int64(-4005)
	)
	cfg, settings := registrationFixture(t)
	now := time.Unix(2_000_000_000, 0)
	bindTestOwner(t, settings, now)
	state := settings.Registrations()
	state.UnknownGroupLeaves = []store.UnknownGroupLeave{{
		GroupID: groupID, Title: "Cleanup only", ExpiresAt: now.Add(registrationPending).Unix(),
	}}
	if _, err := settings.CommitRegistrations(state.Revision, state); err != nil {
		t.Fatal(err)
	}
	caller := &registrationCaller{members: map[[2]int64]telego.ChatMember{
		{groupID, actor}: adminMember(actor),
	}}
	bot := newRegistrationBot(t, caller)
	service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	service.now = func() time.Time { return now }
	runRegistrationUpdate(t, bot, service, telego.Update{MyChatMember: &telego.ChatMemberUpdated{
		Chat:          telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: "Cleanup only"},
		From:          telego.User{ID: actor, LanguageCode: "en"},
		OldChatMember: plainMember(testBotID),
		NewChatMember: adminMember(testBotID),
	}})
	if settings.IsGroup(groupID) {
		t.Fatal("unknown-group cleanup record authorized registration")
	}
	if left := caller.leftChats(); len(left) != 1 || left[0] != groupID {
		t.Fatalf("cleanup-only promotion leaves = %v, want [%d]", left, groupID)
	}
}

func TestOwnerClaimLifetimePinAndOpenStateLog(t *testing.T) {
	cfg, settings := registrationFixture(t)
	cfg.OwnerClaimLifetimeSeconds = 45
	cfg.OwnerClaimUserID = testOwner
	now := time.Unix(2_000_000_000, 0)
	caller := &registrationCaller{members: make(map[[2]int64]telego.ChatMember)}
	bot := newRegistrationBot(t, caller)
	service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	service.now = func() time.Time { return now }

	logs := newSynchronizedLog()
	oldLog := log.Writer()
	log.SetOutput(logs)
	defer log.SetOutput(oldLog)
	if err := service.EnsureOwnerClaim(); err != nil {
		t.Fatal(err)
	}
	state := settings.Registrations()
	if state.OwnerClaimExpiresAt != now.Add(45*time.Second).Unix() {
		t.Fatalf("owner claim expiry = %d, want %d", state.OwnerClaimExpiresAt, now.Add(45*time.Second).Unix())
	}
	if !strings.Contains(logs.String(), "OWNER UNCLAIMED") {
		t.Fatalf("open owner claim was not visible in the journal: %s", logs.String())
	}
	claimUpdate := func(userID int64) telego.Update {
		return telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: userID, Type: telego.ChatTypePrivate},
			From: &telego.User{ID: userID, LanguageCode: "en"},
			Text: "/start owner_" + state.OwnerClaimNonce,
		}}
	}
	runRegistrationUpdate(t, bot, service, claimUpdate(testOwner+1))
	if got := settings.Registrations(); got.OwnerID != 0 || got.OwnerClaimNonce == "" {
		t.Fatalf("unmatched pinned claimant changed owner state: %+v", got)
	}
	refused := caller.messagesTo(testOwner + 1)
	wantRefused := i18n.Messages.Bot.Registration.OwnerClaimRefused.For(i18n.LangEN)
	if len(refused) != 1 || refused[0].Text != wantRefused {
		t.Fatalf("pinned claimant refusal = %+v, want catalogue text %q", refused, wantRefused)
	}
	runRegistrationUpdate(t, bot, service, claimUpdate(testOwner))
	if got := settings.Registrations(); got.OwnerID != testOwner || got.OwnerClaimNonce != "" {
		t.Fatalf("pinned owner claim state = %+v", got)
	}
}

func TestEnrollmentCommandIssuesDurableOneUseLink(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing-config.json")
	stateDirectory := t.TempDir()
	cfg, settings, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000_000, 0)
	bindTestOwner(t, settings, now)
	caller := &registrationCaller{members: make(map[[2]int64]telego.ChatMember)}
	bot := newRegistrationBot(t, caller)
	service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	service.now = func() time.Time { return now }

	runRegistrationUpdate(t, bot, service, telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: testOwner, Type: telego.ChatTypePrivate},
		From: &telego.User{ID: testOwner, LanguageCode: "en"},
		Text: "/enroll",
	}})

	state := registrationStateFromDisk(t, configPath, stateDirectory)
	if state.OwnerID != testOwner || len(state.EnrollmentNonces) != 1 {
		t.Fatalf("enrollment issuance state = %+v, want owner and one nonce", state)
	}
	nonce := state.EnrollmentNonces[0]
	if nonce.Nonce == "" || nonce.IssuedBy != testOwner || nonce.ExpiresAt != now.Add(enrollmentLifetime).Unix() {
		t.Fatalf("issued enrollment nonce = %+v", nonce)
	}
	messages := caller.messagesTo(testOwner)
	if len(messages) != 1 {
		t.Fatalf("enrollment issuance messages = %d, want 1", len(messages))
	}
	link := fmt.Sprintf("https://t.me/%s?startgroup=enroll_%s", "verify_test_bot", nonce.Nonce)
	want := i18n.Messages.Bot.Registration.EnrollmentLink.Render(
		i18n.LangEN, int(enrollmentLifetime/time.Minute), link)
	if messages[0].Text != want {
		t.Fatalf("enrollment issuance message = %q, want catalogue text %q", messages[0].Text, want)
	}
	if strings.Contains(link, "?start=") || !strings.Contains(link, "?startgroup=enroll_") {
		t.Fatalf("enrollment link = %q, want one-use startgroup payload", link)
	}
	if left := caller.leftChats(); len(left) != 0 {
		t.Fatalf("enrollment issuance leaves = %v, want none", left)
	}
}

func TestEnrollmentPayloadRejectsNonAdminAndLeaves(t *testing.T) {
	const (
		actor    = int64(77)
		groupID  = int64(-5001)
		operator = int64(-5002)
		title    = "Non-admin"
	)
	configPath := filepath.Join(t.TempDir(), "missing-config.json")
	stateDirectory := t.TempDir()
	cfg, settings, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdminLogChatID = operator
	now := time.Unix(2_000_000_000, 0)
	bindTestOwner(t, settings, now)
	nonce, err := settings.IssueEnrollmentNonce(testOwner, now, enrollmentLifetime)
	if err != nil {
		t.Fatal(err)
	}
	before := settings.Registrations()
	caller := &registrationCaller{members: map[[2]int64]telego.ChatMember{
		{groupID, actor}:     plainMember(actor),
		{groupID, testBotID}: adminMember(testBotID),
	}}
	bot := newRegistrationBot(t, caller)
	service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	service.now = func() time.Time { return now }

	runRegistrationUpdate(t, bot, service, telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: title},
		From: &telego.User{ID: actor, LanguageCode: "en"},
		Text: "/start enroll_" + nonce.Nonce,
	}})

	after := settings.Registrations()
	assertRegistrationStateEqual(t, after, before)
	assertRegistrationStateEqual(t, registrationStateFromDisk(t, configPath, stateDirectory), before)
	if settings.IsGroup(groupID) {
		t.Fatal("non-admin enrollment registered a group")
	}
	if left := caller.leftChats(); !reflect.DeepEqual(left, []int64{groupID}) {
		t.Fatalf("non-admin enrollment leaves = %v, want [%d]", left, groupID)
	}
	refusal := caller.messagesTo(groupID)
	wantRefusal := i18n.Messages.Bot.Registration.EnrollmentRefused.For(
		i18n.FromStored(cfg.LangForGroup(groupID)))
	if len(refusal) != 1 || refusal[0].Text != wantRefusal {
		t.Fatalf("non-admin enrollment refusal = %+v, want catalogue text %q", refusal, wantRefusal)
	}
	unauthorized := caller.messagesTo(operator)
	wantUnauthorized := i18n.Messages.Bot.Lifecycle.UnauthorizedChat.Render(
		i18n.FromStored(cfg.LangForGroup(0)), title, groupID, telego.ChatTypeSupergroup)
	if len(unauthorized) != 1 || unauthorized[0].Text != wantUnauthorized {
		t.Fatalf("non-admin enrollment operator result = %+v, want catalogue text %q", unauthorized, wantUnauthorized)
	}
}

func TestEnrollmentCommitFailureRetainsNonceAndReportsPersistence(t *testing.T) {
	const (
		actor    = int64(78)
		groupID  = int64(-5003)
		operator = int64(-5004)
		title    = "Commit failure"
	)
	configPath := filepath.Join(t.TempDir(), "missing-config.json")
	stateDirectory := t.TempDir()
	cfg, settings, err := loadRuntimeState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdminLogChatID = operator
	now := time.Unix(2_000_000_000, 0)
	bindTestOwner(t, settings, now)
	nonce, err := settings.IssueEnrollmentNonce(testOwner, now, enrollmentLifetime)
	if err != nil {
		t.Fatal(err)
	}
	before := settings.Registrations()
	caller := &registrationCaller{members: map[[2]int64]telego.ChatMember{
		{groupID, actor}:     adminMember(actor),
		{groupID, testBotID}: adminMember(testBotID),
	}}
	bot := newRegistrationBot(t, caller)
	service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
	service.now = func() time.Time { return now }
	if err := os.RemoveAll(stateDirectory); err != nil {
		t.Fatal(err)
	}

	runRegistrationUpdate(t, bot, service, telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: title},
		From: &telego.User{ID: actor, LanguageCode: "en"},
		Text: "/start enroll_" + nonce.Nonce,
	}})

	after := settings.Registrations()
	assertRegistrationStateEqual(t, after, before)
	if settings.IsGroup(groupID) {
		t.Fatal("failed enrollment commit registered a group")
	}
	if status := settings.Persistence(); status.LastError == nil {
		t.Fatalf("failed enrollment commit persistence = %+v, want retained write error", status)
	}
	if left := caller.leftChats(); !reflect.DeepEqual(left, []int64{groupID}) {
		t.Fatalf("failed enrollment commit leaves = %v, want [%d]", left, groupID)
	}
	failure := caller.messagesTo(groupID)
	wantFailure := i18n.Messages.Bot.Registration.RegistrationSaveFailed.For(i18n.LangEN)
	if len(failure) != 1 || failure[0].Text != wantFailure {
		t.Fatalf("failed enrollment commit response = %+v, want catalogue text %q", failure, wantFailure)
	}
	unauthorized := caller.messagesTo(operator)
	wantUnauthorized := i18n.Messages.Bot.Lifecycle.UnauthorizedChat.Render(
		i18n.FromStored(cfg.LangForGroup(0)), title, groupID, telego.ChatTypeSupergroup)
	if len(unauthorized) != 1 || unauthorized[0].Text != wantUnauthorized {
		t.Fatalf("failed enrollment commit operator result = %+v, want catalogue text %q", unauthorized, wantUnauthorized)
	}
}

func TestEnrollmentTransportFailuresRetainDurableState(t *testing.T) {
	t.Run("membership lookup", func(t *testing.T) {
		const (
			actor    = int64(79)
			groupID  = int64(-5005)
			operator = int64(-5006)
			title    = "Membership lookup"
		)
		configPath := filepath.Join(t.TempDir(), "missing-config.json")
		stateDirectory := t.TempDir()
		cfg, settings, err := loadRuntimeState(configPath, stateDirectory)
		if err != nil {
			t.Fatal(err)
		}
		cfg.AdminLogChatID = operator
		now := time.Unix(2_000_000_000, 0)
		bindTestOwner(t, settings, now)
		nonce, err := settings.IssueEnrollmentNonce(testOwner, now, enrollmentLifetime)
		if err != nil {
			t.Fatal(err)
		}
		before := settings.Registrations()
		caller := &registrationCaller{
			members: map[[2]int64]telego.ChatMember{
				{groupID, actor}: adminMember(actor),
			},
			memberErrors: map[[2]int64]error{
				{groupID, testBotID}: fmt.Errorf("getChatMember unavailable"),
			},
		}
		bot := newRegistrationBot(t, caller)
		service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
		service.now = func() time.Time { return now }

		runRegistrationUpdate(t, bot, service, telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: title},
			From: &telego.User{ID: actor, LanguageCode: "en"},
			Text: "/start enroll_" + nonce.Nonce,
		}})

		if requests := caller.membershipRequestsForTest(); !reflect.DeepEqual(requests,
			[][2]int64{{groupID, actor}, {groupID, testBotID}}) {
			t.Fatalf("enrollment membership requests = %v, want actor then bot", requests)
		}
		after := settings.Registrations()
		assertRegistrationStateEqual(t, after, before)
		assertRegistrationStateEqual(t, registrationStateFromDisk(t, configPath, stateDirectory), before)
		if settings.IsGroup(groupID) {
			t.Fatal("unreadable bot membership registered a group")
		}
		if left := caller.leftChats(); !reflect.DeepEqual(left, []int64{groupID}) {
			t.Fatalf("unreadable bot membership leaves = %v, want [%d]", left, groupID)
		}
		refusal := caller.messagesTo(groupID)
		wantRefusal := i18n.Messages.Bot.Registration.EnrollmentRefused.For(
			i18n.FromStored(cfg.LangForGroup(groupID)))
		if len(refusal) != 1 || refusal[0].Text != wantRefusal {
			t.Fatalf("unreadable bot membership response = %+v, want catalogue text %q", refusal, wantRefusal)
		}
		unauthorized := caller.messagesTo(operator)
		wantUnauthorized := i18n.Messages.Bot.Lifecycle.UnauthorizedChat.Render(
			i18n.FromStored(cfg.LangForGroup(0)), title, groupID, telego.ChatTypeSupergroup)
		if len(unauthorized) != 1 || unauthorized[0].Text != wantUnauthorized {
			t.Fatalf("unreadable bot membership operator result = %+v, want catalogue text %q", unauthorized, wantUnauthorized)
		}
	})

	t.Run("leave", func(t *testing.T) {
		const (
			actor    = int64(80)
			groupID  = int64(-5007)
			operator = int64(-5008)
			title    = "Leave failure"
		)
		configPath := filepath.Join(t.TempDir(), "missing-config.json")
		stateDirectory := t.TempDir()
		cfg, settings, err := loadRuntimeState(configPath, stateDirectory)
		if err != nil {
			t.Fatal(err)
		}
		cfg.AdminLogChatID = operator
		now := time.Unix(2_000_000_000, 0)
		bindTestOwner(t, settings, now)
		nonce, err := settings.IssueEnrollmentNonce(testOwner, now, enrollmentLifetime)
		if err != nil {
			t.Fatal(err)
		}
		state := settings.Registrations()
		state.UnknownGroupLeaves = []store.UnknownGroupLeave{{
			GroupID: groupID, Title: title, ExpiresAt: now.Add(registrationPending).Unix(),
		}}
		if _, err := settings.CommitRegistrations(state.Revision, state); err != nil {
			t.Fatal(err)
		}
		before := settings.Registrations()
		caller := &registrationCaller{
			members: map[[2]int64]telego.ChatMember{
				{groupID, actor}: plainMember(actor),
			},
			leaveErrors: map[int64]error{
				groupID: fmt.Errorf("leaveChat unavailable"),
			},
		}
		bot := newRegistrationBot(t, caller)
		root, cancel := context.WithCancel(context.Background())
		defer cancel()
		service := newRegistrationService(root, bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
		service.now = func() time.Time { return now }

		runRegistrationUpdate(t, bot, service, telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: title},
			From: &telego.User{ID: actor, LanguageCode: "en"},
			Text: "/start enroll_" + nonce.Nonce,
		}})

		after := settings.Registrations()
		assertRegistrationStateEqual(t, after, before)
		assertRegistrationStateEqual(t, registrationStateFromDisk(t, configPath, stateDirectory), before)
		if len(after.UnknownGroupLeaves) != 1 || after.UnknownGroupLeaves[0].GroupID != groupID {
			t.Fatalf("failed leave cleanup evidence = %+v, want group %d record", after.UnknownGroupLeaves, groupID)
		}
		if attempts := caller.leaveAttemptsForTest(); !reflect.DeepEqual(attempts, []int64{groupID}) {
			t.Fatalf("failed leave attempts = %v, want [%d]", attempts, groupID)
		}
		if left := caller.leftChats(); len(left) != 0 {
			t.Fatalf("failed leave completed leaves = %v, want none", left)
		}
		refusal := caller.messagesTo(groupID)
		wantRefusal := i18n.Messages.Bot.Registration.EnrollmentRefused.For(
			i18n.FromStored(cfg.LangForGroup(groupID)))
		if len(refusal) != 1 || refusal[0].Text != wantRefusal {
			t.Fatalf("failed leave response = %+v, want catalogue text %q", refusal, wantRefusal)
		}
		if unauthorized := caller.messagesTo(operator); len(unauthorized) != 0 {
			t.Fatalf("failed leave operator result = %+v, want none before leaving", unauthorized)
		}
	})

	t.Run("response send", func(t *testing.T) {
		const (
			actor   = int64(81)
			groupID = int64(-5009)
			title   = "Response failure"
		)
		configPath := filepath.Join(t.TempDir(), "missing-config.json")
		stateDirectory := t.TempDir()
		cfg, settings, err := loadRuntimeState(configPath, stateDirectory)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(2_000_000_000, 0)
		bindTestOwner(t, settings, now)
		nonce, err := settings.IssueEnrollmentNonce(testOwner, now, enrollmentLifetime)
		if err != nil {
			t.Fatal(err)
		}
		before := settings.Registrations()
		caller := &registrationCaller{
			members: map[[2]int64]telego.ChatMember{
				{groupID, actor}:     adminMember(actor),
				{groupID, testBotID}: adminMember(testBotID),
			},
			sendErrors: map[int64]error{
				actor: fmt.Errorf("sendMessage unavailable"),
			},
		}
		bot := newRegistrationBot(t, caller)
		service := newRegistrationService(context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil, nil, nil)
		service.now = func() time.Time { return now }

		runRegistrationUpdate(t, bot, service, telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: title},
			From: &telego.User{ID: actor, LanguageCode: "en"},
			Text: "/start enroll_" + nonce.Nonce,
		}})

		after := settings.Registrations()
		if after.Revision != before.Revision+1 || !settings.IsGroup(groupID) ||
			len(after.RegisteredGroups) != 1 || len(after.EnrollmentNonces) != 0 ||
			len(after.PendingRegistrations) != 0 || len(after.UnknownGroupLeaves) != 0 {
			t.Fatalf("response failure registration state = %+v, want durable completed registration", after)
		}
		registered := after.RegisteredGroups[0]
		if registered.ID != groupID || registered.RegisteredBy != actor || registered.Title != title {
			t.Fatalf("response failure registered group = %+v", registered)
		}
		assertRegistrationStateEqual(t, registrationStateFromDisk(t, configPath, stateDirectory), after)
		want := i18n.Messages.Bot.Registration.GroupRegistered.Render(i18n.LangEN, title)
		attempts := caller.sendAttemptsTo(actor)
		if len(attempts) != 1 || attempts[0].Text != want {
			t.Fatalf("response failure direct attempt = %+v, want catalogue text %q", attempts, want)
		}
		if direct := caller.messagesTo(actor); len(direct) != 0 {
			t.Fatalf("response failure delivered direct messages = %+v, want none", direct)
		}
		fallback := caller.messagesTo(groupID)
		if len(fallback) != 1 || fallback[0].Text != want {
			t.Fatalf("response failure fallback = %+v, want catalogue text %q", fallback, want)
		}
		if left := caller.leftChats(); len(left) != 0 {
			t.Fatalf("response failure leaves = %v, want none", left)
		}
	})
}
