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
	mu      sync.Mutex
	members map[[2]int64]telego.ChatMember
	sent    []telego.SendMessageParams
	left    []int64
	events  chan string
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
		var params telego.SendMessageParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		c.sent = append(c.sent, params)
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
	default:
		return nil, fmt.Errorf("unexpected Telegram method %q", method)
	}
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
	if len(caller.left) != 0 || len(caller.sent) != 1 ||
		!strings.Contains(caller.sent[0].Text, "configure_-1901") {
		t.Fatalf("owner registration Telegram calls: sent=%+v left=%v", caller.sent, caller.left)
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
