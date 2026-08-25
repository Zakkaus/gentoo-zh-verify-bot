package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	botapp "github.com/Zakkaus/gentoo-zh-verify-bot/internal/bot"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/panel"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/verify"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

func runRuntimeHandler(t *testing.T, bot *telego.Bot, handler th.Handler, update telego.Update) {
	t.Helper()
	updates := make(chan telego.Update, 1)
	botHandler, err := th.NewBotHandler(bot, updates)
	if err != nil {
		t.Fatal(err)
	}
	handled := make(chan error, 1)
	botHandler.Handle(func(ctx *th.Context, update telego.Update) error {
		err := handler(ctx, update)
		handled <- err
		return err
	})
	started := make(chan error, 1)
	go func() { started <- botHandler.Start() }()
	updates <- update
	close(updates)
	if err := <-handled; err != nil {
		t.Fatalf("handler returned %v", err)
	}
	if err := <-started; err != nil {
		t.Fatalf("bot handler returned %v", err)
	}
}

func TestRuntimeRegistrationActivatesServicesWithoutRebuiltConfig(t *testing.T) {
	const (
		groupID = int64(-1009000000999)
		userID  = int64(7001)
	)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"lang":"zh-Hant","notify_ttl_seconds":-1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, settings, err := loadRuntimeState(configPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000_000, 0)
	bindTestOwner(t, settings, now)
	caller := &registrationCaller{members: map[[2]int64]telego.ChatMember{
		{groupID, testOwner}: adminMember(testOwner),
		{groupID, testBotID}: adminMember(testBotID),
	}}
	bot := newRegistrationBot(t, caller)
	telegram := tg.New(bot)
	verification := verify.New(settings, telegram, cfg, &i18n.Messages, bot,
		verify.Identity{ID: testBotID, Username: "verify_test_bot"}, "")
	defer verification.Shutdown()
	administration := panel.New(
		settings, telegram, cfg, &i18n.Messages,
		verification, nil, nil, "test", time.Now(),
	)
	application := botapp.New(cfg, settings, telegram, verification, administration, nil, nil)
	menusRefreshed := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registration := newRegistrationService(ctx, bot, settings, cfg, "verify_test_bot", testBotID, func(callbackCtx context.Context, _ int64) {
		application.SetupCommands(callbackCtx, bot)
		close(menusRefreshed)
	}, nil, nil)
	registration.now = func() time.Time { return now }

	registrationUpdate := telego.Update{MyChatMember: &telego.ChatMemberUpdated{
		Chat:          telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: "Runtime Group"},
		From:          telego.User{ID: testOwner, LanguageCode: "en"},
		OldChatMember: &telego.ChatMemberLeft{Status: telego.MemberStatusLeft, User: telego.User{ID: testBotID}},
		NewChatMember: adminMember(testBotID),
	}}
	if !registration.registrationMembershipUpdate(context.Background(), registrationUpdate) {
		t.Fatalf("registration fixture was not eligible: config_known=%t settings_known=%t",
			cfg.IsKnownChat(groupID), settings.IsKnownChat(groupID))
	}
	runRuntimeHandler(t, bot, registration.onMyChatMember, registrationUpdate)
	if !settings.IsGroup(groupID) {
		t.Fatalf("registration did not publish the runtime group: state=%+v sent=%+v left=%v",
			settings.Registrations(), caller.sent, caller.left)
	}
	if cfg.IsGroup(groupID) {
		t.Fatal("test rebuilt or mutated the startup config")
	}

	beforeJoin := caller.sentTo(groupID)
	beforePrivate := caller.sentTo(userID)
	runRuntimeHandler(t, bot, verification.OnJoinRequest, telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: "Runtime Group"},
		From: telego.User{ID: userID, FirstName: "Applicant", LanguageCode: "en"},
	}})
	joinHandled := caller.sentTo(userID) > beforePrivate && caller.sentTo(groupID) == beforeJoin

	runRuntimeHandler(t, bot, administration.OnStop, telego.Update{Message: &telego.Message{
		MessageID: 2,
		Chat:      telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: "Runtime Group"},
		From:      &telego.User{ID: testOwner, LanguageCode: "en"},
		Text:      "/stop",
	}})
	commandHandled := !verification.IsEnabled(groupID)

	select {
	case <-menusRefreshed:
	case <-time.After(2 * time.Second):
		t.Error("registration completion did not refresh command menus")
	}
	if !caller.hasCommandScope(groupID) {
		t.Error("runtime group did not receive its locale-specific command scope")
	}
	if !joinHandled {
		t.Error("runtime group join request did not create only the default private verification challenge")
	}
	if !commandHandled {
		t.Error("runtime group /stop command did not change verification state")
	}
}
