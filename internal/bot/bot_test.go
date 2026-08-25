package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/lookup"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/moderate"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/panel"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/verify"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

// TestHandlerOrder protects the documented label sequence; it does not exercise predicates,
// handler pairings, or Telego's first-match dispatch.
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
		Commands     []telego.BotCommand `json:"commands"`
		LanguageCode string              `json:"language_code"`
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
		Commands     []telego.BotCommand `json:"commands"`
		LanguageCode string              `json:"language_code"`
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

func expectedMemberCommands(language i18n.Lang) []telego.BotCommand {
	menu := i18n.Messages.Bot.Menu.Member
	return []telego.BotCommand{
		{Command: "help", Description: menu.Help.For(language)},
		{Command: "pkg", Description: menu.Pkg.For(language)},
		{Command: "use", Description: menu.Use.For(language)},
		{Command: "bug", Description: menu.Bug.For(language)},
		{Command: "news", Description: menu.News.For(language)},
		{Command: "wiki", Description: menu.Wiki.For(language)},
		{Command: "bbs", Description: menu.BBS.For(language)},
		{Command: "pkgs", Description: menu.Pkgs.For(language)},
		{Command: "distro", Description: menu.Distro.For(language)},
		{Command: "arm", Description: menu.Arm.For(language)},
		{Command: "armpkgs", Description: menu.ArmPkgs.For(language)},
		{Command: "ping", Description: menu.Ping.For(language)},
		{Command: "stats", Description: menu.Stats.For(language)},
	}
}

func expectedAdminCommands(language i18n.Lang, warnLimit int) []telego.BotCommand {
	menu := i18n.Messages.Bot.Menu.Admin
	return append([]telego.BotCommand{
		{Command: "start", Description: menu.Start.For(language)},
		{Command: "settings", Description: i18n.Messages.Panel.Menu.Settings.For(language)},
		{Command: "stop", Description: menu.Stop.For(language)},
		{Command: "mute", Description: menu.Mute.For(language)},
		{Command: "unmute", Description: menu.Unmute.For(language)},
		{Command: "sb", Description: menu.Purge.For(language)},
		{Command: "ban", Description: menu.Ban.For(language)},
		{Command: "warn", Description: menu.Warn.Render(language, warnLimit)},
		{Command: "clearwarn", Description: menu.ClearWarn.For(language)},
		{Command: "bc", Description: menu.Channel.For(language)},
		{Command: "rich", Description: menu.RichText.For(language)},
		{Command: "spoiler", Description: menu.NameSpoiler.For(language)},
		{Command: "vmode", Description: menu.VerificationMode.For(language)},
		{Command: "autodel", Description: menu.AutoDelete.For(language)},
		{Command: "bantime", Description: menu.BanTime.For(language)},
	}, expectedMemberCommands(language)...)
}

func expectedOwnerCommands(language i18n.Lang) []telego.BotCommand {
	menu := i18n.Messages.Bot.Menu.Owner
	return append([]telego.BotCommand{
		{Command: "enroll", Description: menu.Enroll.For(language)},
		{Command: "unregister", Description: menu.Unregister.For(language)},
	}, expectedMemberCommands(language)...)
}
func commandDifference(got, want []telego.BotCommand) string {
	if len(got) != len(want) {
		return fmt.Sprintf("length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if !reflect.DeepEqual(got[index], want[index]) {
			return fmt.Sprintf("command %d = %#v, want catalogue command %#v", index, got[index], want[index])
		}
	}
	return ""
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
		language := i18n.LangZH
		if request.LanguageCode == "en" {
			language = i18n.LangEN
		}
		if request.Scope.Type == "chat" || request.Scope.Type == "chat_administrators" {
			language = i18n.LangZHHant
		}
		wantCommands := expectedMemberCommands(language)
		if request.Scope.Type == "all_chat_administrators" || request.Scope.Type == "chat_administrators" {
			wantCommands = expectedAdminCommands(language, cfg.WarnLimit)
		}
		if difference := commandDifference(request.Commands, wantCommands); difference != "" {
			t.Errorf("%s/%q commands: %s", request.Scope.Type, request.LanguageCode, difference)
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

func TestSetupCommandsAddsOwnerPrivateMenuFromRuntimeState(t *testing.T) {
	const ownerID int64 = 42
	cfg := &config.Config{WarnLimit: 3}
	settings, err := store.NewSettings(t.TempDir()+"/settings.json", botTestSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000_000, 0)
	nonce, _, err := settings.EnsureOwnerClaim(now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.ClaimOwner(ownerID, nonce, now); err != nil {
		t.Fatal(err)
	}

	caller := &commandRecordingCaller{}
	service := &Service{cfg: cfg, settings: settings}
	service.SetupCommands(context.Background(), testBot(t, caller))

	ownerMenus := 0
	languages := map[string]int{}
	for _, request := range caller.requests {
		if request.Scope.Type != "chat" || !strings.Contains(string(request.Scope.ChatID), "42") {
			continue
		}
		ownerMenus++
		languages[request.LanguageCode]++
		language := i18n.LangZH
		if request.LanguageCode == "en" {
			language = i18n.LangEN
		}
		if difference := commandDifference(request.Commands, expectedOwnerCommands(language)); difference != "" {
			t.Errorf("owner/%q commands: %s", request.LanguageCode, difference)
		}
	}
	if ownerMenus != 3 || languages[""] != 1 || languages["zh"] != 1 || languages["en"] != 1 {
		t.Fatalf("owner private menus/languages = %d/%v, want three localized scopes", ownerMenus, languages)
	}
}

func TestDMReplyThrottle(t *testing.T) {
	caller := &recordingCaller{}
	telegramBot := testBot(t, caller)
	dm := &dmHandler{
		cfg:            &config.Config{PrivateQueryPerMin: 4},
		telegram:       tg.New(telegramBot),
		last:           make(map[int64]time.Time),
		catalogueReply: true,
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

type dispatchAPICall struct {
	method    string
	body      []byte
	messageID int
}

type dispatchCaller struct {
	mu            sync.Mutex
	botID         int64
	nextMessageID int
	members       map[[2]int64]telego.ChatMember
	calls         []dispatchAPICall
}

func (c *dispatchCaller) Call(_ context.Context, endpoint string, data *ta.RequestData) (*ta.Response, error) {
	method := endpoint[strings.LastIndexByte(endpoint, '/')+1:]
	body := append([]byte(nil), data.BodyRaw...)
	switch method {
	case "getMe":
		c.record(dispatchAPICall{method: method, body: body})
		return apiResponse(&telego.User{ID: c.botID, IsBot: true, Username: "dispatch_bot"})
	case "getChatMember":
		var params struct {
			ChatID int64 `json:"chat_id"`
			UserID int64 `json:"user_id"`
		}
		if err := json.Unmarshal(body, &params); err != nil {
			return nil, err
		}
		c.mu.Lock()
		member := c.members[[2]int64{params.ChatID, params.UserID}]
		c.mu.Unlock()
		if member == nil {
			return nil, fmt.Errorf("no member response for chat %d user %d", params.ChatID, params.UserID)
		}
		c.record(dispatchAPICall{method: method, body: body})
		return apiResponse(member)
	case "sendMessage":
		var params struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		if err := json.Unmarshal(body, &params); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.nextMessageID++
		messageID := c.nextMessageID
		c.calls = append(c.calls, dispatchAPICall{method: method, body: body, messageID: messageID})
		c.mu.Unlock()
		chatType := telego.ChatTypePrivate
		if params.ChatID < 0 {
			chatType = telego.ChatTypeSupergroup
		}
		return apiResponse(&telego.Message{
			MessageID: messageID,
			Chat:      telego.Chat{ID: params.ChatID, Type: chatType},
			Text:      params.Text,
		})
	case "editMessageText":
		var params struct {
			ChatID    int64  `json:"chat_id"`
			MessageID int    `json:"message_id"`
			Text      string `json:"text"`
		}
		if err := json.Unmarshal(body, &params); err != nil {
			return nil, err
		}
		c.record(dispatchAPICall{method: method, body: body, messageID: params.MessageID})
		return apiResponse(&telego.Message{
			MessageID: params.MessageID,
			Chat:      telego.Chat{ID: params.ChatID, Type: telego.ChatTypePrivate},
			Text:      params.Text,
		})
	case "answerCallbackQuery", "deleteMessage", "banChatSenderChat",
		"approveChatJoinRequest", "declineChatJoinRequest":
		c.record(dispatchAPICall{method: method, body: body})
		return apiResponse(true)
	default:
		return nil, fmt.Errorf("unexpected Telegram method %q", method)
	}
}

func (c *dispatchCaller) record(call dispatchAPICall) {
	c.mu.Lock()
	c.calls = append(c.calls, call)
	c.mu.Unlock()
}

func (c *dispatchCaller) setMember(chatID, userID int64, member telego.ChatMember) {
	c.mu.Lock()
	c.members[[2]int64{chatID, userID}] = member
	c.mu.Unlock()
}

func (c *dispatchCaller) snapshotCalls() []dispatchAPICall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]dispatchAPICall(nil), c.calls...)
}

func (c *dispatchCaller) methodCount(method string) int {
	count := 0
	for _, call := range c.snapshotCalls() {
		if call.method == method {
			count++
		}
	}
	return count
}

func (c *dispatchCaller) latestLaunchToken(t *testing.T) string {
	t.Helper()
	calls := c.snapshotCalls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].method != "sendMessage" {
			continue
		}
		var params struct {
			ReplyMarkup struct {
				InlineKeyboard [][]struct {
					URL string `json:"url"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		if json.Unmarshal(calls[i].body, &params) != nil {
			continue
		}
		for _, row := range params.ReplyMarkup.InlineKeyboard {
			for _, button := range row {
				if index := strings.Index(button.URL, "?start=panel_"); index >= 0 {
					return button.URL[index+len("?start=panel_"):]
				}
			}
		}
	}
	t.Fatal("settings launch token was not sent")
	return ""
}

func (c *dispatchCaller) latestPanelCallback(t *testing.T, screen, field, value string) (string, int) {
	t.Helper()
	calls := c.snapshotCalls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].method != "sendMessage" && calls[i].method != "editMessageText" {
			continue
		}
		var params struct {
			ReplyMarkup struct {
				InlineKeyboard [][]struct {
					CallbackData string `json:"callback_data"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		if json.Unmarshal(calls[i].body, &params) != nil {
			continue
		}
		for _, row := range params.ReplyMarkup.InlineKeyboard {
			for _, button := range row {
				parts := strings.Split(button.CallbackData, ":")
				if len(parts) == 6 && parts[2] == screen && parts[4] == field && parts[5] == value {
					return button.CallbackData, calls[i].messageID
				}
			}
		}
	}
	t.Fatalf("panel callback screen=%q field=%q value=%q was not sent", screen, field, value)
	return "", 0
}

func (c *dispatchCaller) latestForceReplyMessageID(t *testing.T) int {
	t.Helper()
	calls := c.snapshotCalls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].method != "sendMessage" {
			continue
		}
		var params struct {
			ReplyMarkup struct {
				ForceReply bool `json:"force_reply"`
			} `json:"reply_markup"`
		}
		if json.Unmarshal(calls[i].body, &params) == nil && params.ReplyMarkup.ForceReply {
			return calls[i].messageID
		}
	}
	t.Fatal("panel ForceReply prompt was not sent")
	return 0
}

func (c *dispatchCaller) sentTexts() []string {
	var texts []string
	for _, call := range c.snapshotCalls() {
		if call.method != "sendMessage" {
			continue
		}
		var params struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(call.body, &params) == nil {
			texts = append(texts, params.Text)
		}
	}
	return texts
}

func (c *dispatchCaller) callbackAnswers() []telego.AnswerCallbackQueryParams {
	var answers []telego.AnswerCallbackQueryParams
	for _, call := range c.snapshotCalls() {
		if call.method != "answerCallbackQuery" {
			continue
		}
		var params telego.AnswerCallbackQueryParams
		if json.Unmarshal(call.body, &params) == nil {
			answers = append(answers, params)
		}
	}
	return answers
}

type dispatchFixture struct {
	groupID         int64
	requiredChannel int64
	botID           int64
	bot             *telego.Bot
	caller          *dispatchCaller
	settings        *store.Settings
	verification    *verify.Service
	administration  *panel.Panel
	moderation      *moderate.Service
	lookups         *lookup.Service
	application     *Service
}

func newDispatchFixture(t *testing.T, requiredChannel int64) *dispatchFixture {
	t.Helper()
	const (
		groupID int64 = -1009000000801
		botID   int64 = 800
	)
	var channelID *int64
	if requiredChannel != 0 {
		channelID = &requiredChannel
	}
	cfg := &config.Config{
		Groups: []config.GroupConfig{{
			ID:                groupID,
			Lang:              "en",
			VerifyMode:        config.ModeKernel,
			RequiredChannelID: channelID,
			ChannelDisplay:    "@required",
		}},
		GroupIDs:            []int64{groupID},
		Lang:                "en",
		VerifyMode:          config.ModeKernel,
		BlockChannelSenders: true,
		PrivateQueryPerMin:  4,
	}
	cfg.PrivateReply = i18n.Messages.Bot.DirectMessage.AutoReply.Render(i18n.LangEN, cfg.PrivateQueryPerMin)
	settings, err := store.NewSettings("", botTestSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	caller := &dispatchCaller{botID: botID, members: make(map[[2]int64]telego.ChatMember)}
	telegramBot := testBot(t, caller)
	telegram := tg.New(telegramBot)
	stateDirectory := t.TempDir()
	verification := verify.New(
		settings, telegram, cfg, &i18n.Messages, telegramBot,
		verify.Identity{ID: botID, Username: "dispatch_bot"}, stateDirectory,
	)
	moderation := moderate.New(settings, telegram, cfg, stateDirectory)
	lookups := lookup.New(settings, telegram, cfg, "")
	administration := panel.New(
		settings, telegram, cfg, &i18n.Messages, verification, moderation, lookups, "test", time.Now(),
	)
	application := New(cfg, settings, telegram, verification, administration, moderation, lookups)
	return &dispatchFixture{
		groupID:         groupID,
		requiredChannel: requiredChannel,
		botID:           botID,
		bot:             telegramBot,
		caller:          caller,
		settings:        settings,
		verification:    verification,
		administration:  administration,
		moderation:      moderation,
		lookups:         lookups,
		application:     application,
	}
}

func runDirectHandler(t *testing.T, bot *telego.Bot, handler th.Handler, update telego.Update) {
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

func (f *dispatchFixture) joinRequest(userID int64) telego.Update {
	return telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat:       telego.Chat{ID: f.groupID, Type: telego.ChatTypeSupergroup},
		From:       telego.User{ID: userID, FirstName: "Applicant", LanguageCode: "en"},
		UserChatID: userID,
	}}
}

func (f *dispatchFixture) preparePanelInput(t *testing.T, userID int64) telego.Update {
	t.Helper()
	f.caller.setMember(f.groupID, userID, &telego.ChatMemberAdministrator{
		Status: telego.MemberStatusAdministrator,
		User:   telego.User{ID: userID},
	})
	f.caller.setMember(f.groupID, f.botID, &telego.ChatMemberAdministrator{
		Status: telego.MemberStatusAdministrator,
		User:   telego.User{ID: f.botID},
	})
	runDirectHandler(t, f.bot, f.administration.OnSettings, telego.Update{Message: &telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: f.groupID, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: userID, LanguageCode: "en"},
		Text:      "/settings",
	}})
	token := f.caller.latestLaunchToken(t)
	runDirectHandler(t, f.bot, f.administration.OnStart, telego.Update{Message: &telego.Message{
		MessageID: 2,
		Chat:      telego.Chat{ID: userID, Type: telego.ChatTypePrivate},
		From:      &telego.User{ID: userID, LanguageCode: "en"},
		Text:      "/start panel_" + token,
	}})
	click := func(screen, field, value string) {
		t.Helper()
		data, messageID := f.caller.latestPanelCallback(t, screen, field, value)
		runDirectHandler(t, f.bot, f.administration.OnSettingsCallback, telego.Update{CallbackQuery: &telego.CallbackQuery{
			ID:   screen + field,
			From: telego.User{ID: userID, LanguageCode: "en"},
			Message: &telego.Message{
				MessageID: messageID,
				Chat:      telego.Chat{ID: userID, Type: telego.ChatTypePrivate},
			},
			Data: data,
		}})
	}
	click("gl", "go", "gh")
	click("gh", "go", "vp")
	click("vp", "to", "_")
	promptID := f.caller.latestForceReplyMessageID(t)
	return telego.Update{Message: &telego.Message{
		MessageID: 3,
		Chat:      telego.Chat{ID: userID, Type: telego.ChatTypePrivate},
		From:      &telego.User{ID: userID, LanguageCode: "en"},
		Text:      "300",
		ReplyToMessage: &telego.Message{
			MessageID: promptID,
		},
	}}
}

func botHandlerFunctionName(handler th.Handler) string {
	return runtime.FuncForPC(reflect.ValueOf(handler).Pointer()).Name()
}

func dispatchRouteNames(t *testing.T, fixture *dispatchFixture, update telego.Update) []string {
	t.Helper()
	handler, err := th.NewBotHandler(fixture.bot, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler.Use(fixture.moderation.FilterChannelSenders)
	var handled []string
	routes := fixture.application.handlerRoutes()
	for index := range routes {
		name := botHandlerFunctionName(routes[index].handler)
		routes[index].handler = func(_ *th.Context, _ telego.Update) error {
			handled = append(handled, name)
			return nil
		}
	}
	registerHandlerRoutes(handler, routes)
	if err := handler.BaseGroup().HandleUpdate(context.Background(), fixture.bot, update); err != nil {
		t.Fatal(err)
	}
	return handled
}

func TestGlobalDispatchRunsOnlyTheIntendedHandler(t *testing.T) {
	const (
		panelUser  int64 = 801
		kernelUser int64 = 802
	)
	fixture := newDispatchFixture(t, 0)
	panelInput := fixture.preparePanelInput(t, panelUser)
	runDirectHandler(t, fixture.bot, fixture.verification.OnJoinRequest, fixture.joinRequest(kernelUser))
	kernelAnswer := telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: kernelUser, Type: telego.ChatTypePrivate},
		From: &telego.User{ID: kernelUser, LanguageCode: "en"},
		Text: "6.12.3",
	}}
	if !fixture.verification.KernelAnswerDM(context.Background(), kernelAnswer) {
		t.Fatal("kernel fixture did not establish a gradeable private answer")
	}

	tests := []struct {
		name   string
		update telego.Update
		want   th.Handler
	}{
		{
			name: "join request",
			update: telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
				Chat: telego.Chat{ID: fixture.groupID, Type: telego.ChatTypeSupergroup},
				From: telego.User{ID: 803},
			}},
			want: fixture.verification.OnJoinRequest,
		},
		{
			name:   "answer callback",
			update: telego.Update{CallbackQuery: &telego.CallbackQuery{Data: verify.AnswerCallbackPrefix + "payload"}},
			want:   fixture.verification.OnAnswer,
		},
		{
			name:   "admin callback",
			update: telego.Update{CallbackQuery: &telego.CallbackQuery{Data: verify.AdminCallbackPrefix + "payload"}},
			want:   fixture.verification.OnAdminAction,
		},
		{
			name:   "channel recheck callback",
			update: telego.Update{CallbackQuery: &telego.CallbackQuery{Data: verify.ChannelRecheckCallbackPrefix + "payload"}},
			want:   fixture.verification.OnChannelRecheck,
		},
		{
			name:   "settings callback",
			update: telego.Update{CallbackQuery: &telego.CallbackQuery{Data: panel.SettingsCallbackPrefix + "payload"}},
			want:   fixture.administration.OnSettingsCallback,
		},
		{name: "panel input", update: panelInput, want: fixture.administration.OnPanelInput},
		{name: "kernel answer", update: kernelAnswer, want: fixture.verification.OnKernelAnswer},
		{
			name: "panel start payload",
			update: telego.Update{Message: &telego.Message{
				Chat: telego.Chat{ID: panelUser, Type: telego.ChatTypePrivate},
				From: &telego.User{ID: panelUser},
				Text: "/start panel_token",
			}},
			want: fixture.administration.OnStart,
		},
		{
			name: "scoped verification start payload",
			update: telego.Update{Message: &telego.Message{
				Chat: telego.Chat{ID: panelUser, Type: telego.ChatTypePrivate},
				From: &telego.User{ID: panelUser},
				Text: "/start verify_-100",
			}},
			want: fixture.administration.OnStart,
		},
		{
			name: "bare verification start payload",
			update: telego.Update{Message: &telego.Message{
				Chat: telego.Chat{ID: panelUser, Type: telego.ChatTypePrivate},
				From: &telego.User{ID: panelUser},
				Text: "/start verify",
			}},
			want: fixture.administration.OnStart,
		},
		{
			name: "start without payload",
			update: telego.Update{Message: &telego.Message{
				Chat: telego.Chat{ID: panelUser, Type: telego.ChatTypePrivate},
				From: &telego.User{ID: panelUser},
				Text: "/start",
			}},
			want: fixture.administration.OnStart,
		},
		{
			name: "group command",
			update: telego.Update{Message: &telego.Message{
				Chat: telego.Chat{ID: fixture.groupID, Type: telego.ChatTypeSupergroup},
				From: &telego.User{ID: panelUser},
				Text: "/pkg sys-apps/portage",
			}},
			want: fixture.lookups.OnPkg,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := dispatchRouteNames(t, fixture, test.update)
			want := []string{botHandlerFunctionName(test.want)}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("handlers = %v, want only %v", got, want)
			}
		})
	}

	beforeDelete := fixture.caller.methodCount("deleteMessage")
	beforeBan := fixture.caller.methodCount("banChatSenderChat")
	moderated := telego.Update{Message: &telego.Message{
		MessageID: 99,
		Chat:      telego.Chat{ID: fixture.groupID, Type: telego.ChatTypeSupergroup},
		SenderChat: &telego.Chat{
			ID:    -1009000000999,
			Type:  telego.ChatTypeChannel,
			Title: "Untrusted sender",
		},
		Text: "channel post",
	}}
	if got := dispatchRouteNames(t, fixture, moderated); len(got) != 0 {
		t.Fatalf("moderated sender-channel post reached route handlers %v", got)
	}
	if got := fixture.caller.methodCount("deleteMessage") - beforeDelete; got != 1 {
		t.Fatalf("moderation delete calls = %d, want 1", got)
	}
	if got := fixture.caller.methodCount("banChatSenderChat") - beforeBan; got != 1 {
		t.Fatalf("moderation ban calls = %d, want 1", got)
	}
}

func TestVerificationPublicHandlerBoundaries(t *testing.T) {
	const applicantID int64 = 811
	requiredChannel := int64(-1009000000812)
	channelFixture := newDispatchFixture(t, requiredChannel)
	channelFixture.caller.setMember(requiredChannel, applicantID, &telego.ChatMemberLeft{
		Status: telego.MemberStatusLeft,
		User:   telego.User{ID: applicantID},
	})
	runDirectHandler(t, channelFixture.bot, channelFixture.verification.OnJoinRequest, channelFixture.joinRequest(applicantID))
	beforeMessages := len(channelFixture.caller.sentTexts())
	channelFixture.caller.setMember(requiredChannel, applicantID, &telego.ChatMemberMember{
		Status: telego.MemberStatusMember,
		User:   telego.User{ID: applicantID},
	})
	channelCallback := telego.Update{CallbackQuery: &telego.CallbackQuery{
		ID:   "channel-recheck",
		From: telego.User{ID: applicantID, LanguageCode: "en"},
		Data: fmt.Sprintf("%s%d:%d", verify.ChannelRecheckCallbackPrefix, channelFixture.groupID, applicantID),
	}}
	channelHandler, err := th.NewBotHandler(channelFixture.bot, nil)
	if err != nil {
		t.Fatal(err)
	}
	channelFixture.application.Register(channelHandler)
	if err := channelHandler.BaseGroup().HandleUpdate(context.Background(), channelFixture.bot, channelCallback); err != nil {
		t.Fatal(err)
	}
	answers := channelFixture.caller.callbackAnswers()
	if len(answers) != 1 ||
		answers[0].Text != i18n.Messages.Verification.Channel.ContinueOK.For(i18n.LangEN) {
		t.Fatalf("channel recheck answers = %#v, want one catalogue continuation acknowledgement", answers)
	}
	messages := channelFixture.caller.sentTexts()
	if len(messages) != beforeMessages+1 ||
		!strings.Contains(messages[len(messages)-1], i18n.Messages.Verification.Challenge.KernelQuestion.For(i18n.LangEN)) {
		t.Fatalf("channel recheck messages = %v, want one catalogue kernel challenge", messages[beforeMessages:])
	}

	kernelFixture := newDispatchFixture(t, 0)
	kernelHandler, err := th.NewBotHandler(kernelFixture.bot, nil)
	if err != nil {
		t.Fatal(err)
	}
	kernelFixture.application.Register(kernelHandler)
	if err := kernelHandler.BaseGroup().HandleUpdate(
		context.Background(), kernelFixture.bot, kernelFixture.joinRequest(applicantID),
	); err != nil {
		t.Fatal(err)
	}
	answer := telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: applicantID, Type: telego.ChatTypePrivate},
		From: &telego.User{ID: applicantID, LanguageCode: "en"},
		Text: "6.12.3",
	}}
	if !kernelFixture.verification.KernelAnswerDM(context.Background(), answer) {
		t.Fatal("kernel public-handler fixture did not establish a gradeable answer")
	}
	beforeApprove := kernelFixture.caller.methodCount("approveChatJoinRequest")
	beforeTexts := len(kernelFixture.caller.sentTexts())
	if err := kernelHandler.BaseGroup().HandleUpdate(context.Background(), kernelFixture.bot, answer); err != nil {
		t.Fatal(err)
	}
	if got := kernelFixture.caller.methodCount("approveChatJoinRequest") - beforeApprove; got != 1 {
		t.Fatalf("kernel answer approvals = %d, want 1", got)
	}
	autoReply := i18n.Messages.Bot.DirectMessage.AutoReply.Render(i18n.LangEN, 4)
	for _, text := range kernelFixture.caller.sentTexts()[beforeTexts:] {
		if text == autoReply {
			t.Fatalf("kernel answer fell through to the generic private responder: %q", text)
		}
	}
}
