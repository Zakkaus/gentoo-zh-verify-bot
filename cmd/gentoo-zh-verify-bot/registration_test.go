package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	sent            []telego.SendMessageParams
	left            []int64
	commandScopeIDs []int64
	events          chan string
}

func (c *registrationCaller) Call(_ context.Context, endpoint string, data *ta.RequestData) (*ta.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
		member := c.members[[2]int64{params.ChatID, params.UserID}]
		if member == nil {
			return nil, fmt.Errorf("no member response for chat %d user %d", params.ChatID, params.UserID)
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
		c.sent = append(c.sent, telego.SendMessageParams{
			ChatID: telego.ChatID{ID: wire.ChatID},
			Text:   wire.Text,
		})
		if c.events != nil {
			c.events <- method
		}
		return registrationAPIResponse(&telego.Message{MessageID: len(c.sent)})
	case "leaveChat":
		var params struct {
			ChatID int64 `json:"chat_id"`
		}
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		c.left = append(c.left, params.ChatID)
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
			c.commandScopeIDs = append(c.commandScopeIDs, params.Scope.ChatID)
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
	service.Register(handler)
	started := make(chan error, 1)
	go func() { started <- handler.Start() }()
	updates <- update
	close(updates)
	if err := <-started; err != nil {
		t.Fatalf("handler returned %v", err)
	}
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
	service := newRegistrationService(ctx, bot, settings, cfg, "verify_test_bot", testBotID, nil)
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
	service := newRegistrationService(ctx, bot, settings, cfg, "verify_test_bot", testBotID, nil)
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
		members: map[[2]int64]telego.ChatMember{{groupID, testOwner}: adminMember(testOwner)},
		events:  make(chan string, 16),
	}
	bot := newRegistrationBot(t, caller)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := newRegistrationService(ctx, bot, settings, cfg, "verify_test_bot", testBotID, nil)
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
			service := newRegistrationService(
				context.Background(), bot, settings, cfg, "verify_test_bot", testBotID, nil)
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
	service := newRegistrationService(ctx, bot, settings, cfg, "verify_test_bot", testBotID, nil)
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
		service := newRegistrationService(ctx, newRegistrationBot(t, caller), settings, cfg, "verify_test_bot", testBotID, nil)
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
		if err := service.registerGroup(groupID, testOwner, "Registered"); err != nil {
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
