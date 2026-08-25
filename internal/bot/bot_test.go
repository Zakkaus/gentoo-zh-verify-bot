package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

func TestHandlerOrder(t *testing.T) {
	want := []string{
		"verify.answer",
		"verify.admin_action",
		"verify.channel_recheck",
		"panel.settings_callback",
		"verify.join_request",
		"panel.chat_shared",
		"panel.input",
		"verify.kernel_answer",
		"bot.private_dm",
		"moderate.sb",
		"moderate.ban",
		"moderate.warn",
		"moderate.clearwarn",
		"moderate.bc",
		"panel.ping",
		"panel.start",
		"panel.settings",
		"panel.stop",
		"panel.stats",
		"lookup.pkg",
		"lookup.use",
		"lookup.bug",
		"lookup.news",
		"lookup.wiki",
		"lookup.bbs",
		"lookup.pkgs",
		"lookup.distro",
		"lookup.arm",
		"lookup.armpkgs",
		"panel.rich",
		"panel.spoiler",
		"panel.vmode",
		"panel.autodel",
		"panel.bantime",
		"moderate.mute",
		"moderate.unmute",
		"panel.help",
	}
	routes := (&Service{}).handlerRoutes()
	got := make([]string, len(routes))
	for i := range routes {
		got[i] = routes[i].name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("handler order changed:\n got: %v\nwant: %v", got, want)
	}
}

type recordingCaller struct {
	mu           sync.Mutex
	sendMessages int
}

func (c *recordingCaller) Call(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch url[strings.LastIndexByte(url, '/')+1:] {
	case "sendMessage":
		c.sendMessages++
		return apiResponse(&telego.Message{MessageID: c.sendMessages})
	default:
		return nil, fmt.Errorf("unexpected Telegram method %q", url)
	}
}

type commandRecordingCaller struct {
	requests []struct {
		LanguageCode string `json:"language_code"`
		Scope        struct {
			Type   string          `json:"type"`
			ChatID json.RawMessage `json:"chat_id"`
		} `json:"scope"`
	}
}

func (c *commandRecordingCaller) Call(_ context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
	if !strings.HasSuffix(url, "/setMyCommands") {
		return nil, fmt.Errorf("unexpected Telegram method %q", url)
	}
	var request struct {
		LanguageCode string `json:"language_code"`
		Scope        struct {
			Type   string          `json:"type"`
			ChatID json.RawMessage `json:"chat_id"`
		} `json:"scope"`
	}
	if err := json.Unmarshal(data.BodyRaw, &request); err != nil {
		return nil, err
	}
	c.requests = append(c.requests, request)
	return apiResponse(true)
}

func apiResponse(value any) (*ta.Response, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &ta.Response{Ok: true, Result: raw}, nil
}

func testBot(t *testing.T, caller ta.Caller) *telego.Bot {
	t.Helper()
	telegramBot, err := telego.NewBot("1:"+strings.Repeat("a", 35), telego.WithAPICaller(caller), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return telegramBot
}

func runHandlerUpdates(t *testing.T, telegramBot *telego.Bot, handler th.Handler, values []telego.Update) {
	t.Helper()
	updates := make(chan telego.Update, len(values))
	botHandler, err := th.NewBotHandler(telegramBot, updates)
	if err != nil {
		t.Fatal(err)
	}
	handled := make(chan error, len(values))
	botHandler.Handle(func(ctx *th.Context, update telego.Update) error {
		err := handler(ctx, update)
		handled <- err
		return err
	})
	started := make(chan error, 1)
	go func() { started <- botHandler.Start() }()
	for _, update := range values {
		updates <- update
	}
	for range values {
		if err := <-handled; err != nil {
			t.Fatalf("handler returned %v", err)
		}
	}
	close(updates)
	if err := <-started; err != nil {
		t.Fatalf("bot handler returned %v", err)
	}
}

func TestSetupCommandsLanguageScopes(t *testing.T) {
	const groupID int64 = -100
	cfg := &config.Config{
		Groups:    []config.GroupConfig{{ID: groupID, Lang: "zh-Hant"}},
		GroupIDs:  []int64{groupID},
		WarnLimit: 3,
	}
	settings, err := store.NewSettings("", botTestSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	caller := &commandRecordingCaller{}
	service := &Service{cfg: cfg, settings: settings}
	service.SetupCommands(context.Background(), testBot(t, caller))

	if len(caller.requests) != 8 {
		t.Fatalf("command menu requests = %d, want 8", len(caller.requests))
	}
	languages := map[string]int{}
	scopes := map[string]int{}
	for _, request := range caller.requests {
		languages[request.LanguageCode]++
		scopes[request.Scope.Type]++
		if (request.Scope.Type == "chat" || request.Scope.Type == "chat_administrators") &&
			!strings.Contains(string(request.Scope.ChatID), "-100") {
			t.Fatalf("zh-Hant chat scope has chat_id %s", request.Scope.ChatID)
		}
	}
	if languages[""] != 4 || languages["zh"] != 2 || languages["en"] != 2 {
		t.Fatalf("command language codes = %v", languages)
	}
	if scopes["default"] != 3 || scopes["all_chat_administrators"] != 3 ||
		scopes["chat"] != 1 || scopes["chat_administrators"] != 1 {
		t.Fatalf("command scopes = %v", scopes)
	}
}

func TestSetupCommandsRereadsRuntimeGroups(t *testing.T) {
	const groupID int64 = -1009000000501
	cfg := &config.Config{Lang: "zh-Hant", WarnLimit: 3}
	settings, err := store.NewSettings(t.TempDir()+"/settings.json", botTestSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	caller := &commandRecordingCaller{}
	service := &Service{cfg: cfg, settings: settings}
	bot := testBot(t, caller)
	service.SetupCommands(context.Background(), bot)
	registration := settings.Registrations()
	registration.RegisteredGroups = []store.RegisteredGroup{{ID: groupID, RegisteredBy: 42}}
	if _, err := settings.CommitRegistrations(registration.Revision, registration); err != nil {
		t.Fatal(err)
	}
	service.SetupCommands(context.Background(), bot)

	if len(caller.requests) != 14 {
		t.Fatalf("command menu requests after runtime registration = %d, want 14", len(caller.requests))
	}
	runtimeScopes := 0
	for _, request := range caller.requests[6:] {
		if (request.Scope.Type == "chat" || request.Scope.Type == "chat_administrators") &&
			strings.Contains(string(request.Scope.ChatID), "-1009000000501") {
			runtimeScopes++
		}
	}
	if runtimeScopes != 2 {
		t.Errorf("runtime group command scopes = %d, want 2", runtimeScopes)
	}
}

func TestDMReplyThrottle(t *testing.T) {
	caller := &recordingCaller{}
	telegramBot := testBot(t, caller)
	dm := &dmHandler{
		cfg:      &config.Config{PrivateReply: "Use the group verification prompt."},
		telegram: tg.New(telegramBot),
		last:     make(map[int64]time.Time),
	}
	message := func(userID int64) telego.Update {
		return telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: userID, Type: "private"},
			From: &telego.User{ID: userID},
			Text: "hello",
		}}
	}
	runHandlerUpdates(t, telegramBot, dm.onPrivateDM, []telego.Update{message(7), message(7), message(8)})
	if caller.sendMessages != 2 {
		t.Fatalf("DM replies = %d, want one per user during the cooldown", caller.sendMessages)
	}
}

func TestDMReplyCapacityKeepsActiveCooldown(t *testing.T) {
	caller := &recordingCaller{}
	telegramBot := testBot(t, caller)
	now := time.Now()
	const activeUID int64 = 1
	dm := &dmHandler{
		cfg:            &config.Config{PrivateQueryPerMin: 3},
		telegram:       tg.New(telegramBot),
		last:           make(map[int64]time.Time, dmMapMax),
		catalogueReply: true,
	}
	dm.last[activeUID] = now
	for uid := int64(2); uid <= dmMapMax; uid++ {
		dm.last[uid] = now.Add(-dmReplyCooldown - time.Second)
	}
	message := func(userID int64) telego.Update {
		return telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: userID, Type: "private"},
			From: &telego.User{ID: userID},
			Text: "hello",
		}}
	}

	runHandlerUpdates(t, telegramBot, dm.onPrivateDM, []telego.Update{message(dmMapMax + 1)})
	runHandlerUpdates(t, telegramBot, dm.onPrivateDM, []telego.Update{message(activeUID)})

	if caller.sendMessages != 1 {
		t.Errorf("DM replies across capacity event = %d, want only the new user's reply", caller.sendMessages)
	}
}
