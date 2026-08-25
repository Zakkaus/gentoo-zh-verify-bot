package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/moderate"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/verify"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

const (
	panelTestGroupA int64 = -1009000000501
	panelTestGroupB int64 = -1009000000502
	panelTestUser   int64 = 77
)

type panelVerifierStub struct {
	kernelPending  bool
	challengeCalls int
}

func (v *panelVerifierStub) AgentStatsText(i18n.Lang) string { return "" }
func (v *panelVerifierStub) ControlGroupID() int64           { return panelTestGroupA }
func (v *panelVerifierStub) DMOrGroup(*telego.Message) bool  { return true }
func (v *panelVerifierStub) EffectiveMode(int64) string      { return config.ModeKernel }
func (v *panelVerifierStub) IsEnabled(int64) bool            { return true }
func (v *panelVerifierStub) KernelAnswerDM(_ context.Context, update telego.Update) bool {
	message := update.Message
	return v.kernelPending && message != nil && message.From != nil && message.Chat.Type == "private" &&
		strings.TrimSpace(message.Text) != "" && !strings.HasPrefix(strings.TrimSpace(message.Text), "/")
}
func (v *panelVerifierStub) SendDMChallenge(context.Context, *telego.Bot, int64, string, int64) {
	v.challengeCalls++
}
func (v *panelVerifierStub) SetAutoDelete(int64, time.Duration, bool) error { return nil }
func (v *panelVerifierStub) SetEnabled(int64, bool) error                   { return nil }
func (v *panelVerifierStub) SetVerifyMode(int64, string) error              { return nil }
func (v *panelVerifierStub) Stats() (string, int, int)                      { return "", 0, 0 }
func (v *panelVerifierStub) ToggleNameSpoiler(int64) (bool, error)          { return false, nil }
func (v *panelVerifierStub) ToggleRich() (bool, error)                      { return false, nil }

type panelAPICaller struct {
	admin                bool
	editErr              error
	chatUsername         string
	memberCalls          atomic.Int32
	lastEditText         string
	lastAnswerText       string
	lastSendText         string
	lastURL              string
	sendChats            []int64
	sendTexts            []string
	messageID            int
	replyKeyboardRemoved bool
	senderUnbans         []telego.UnbanChatSenderChatParams
}

func (c *panelAPICaller) Call(_ context.Context, endpoint string, data *ta.RequestData) (*ta.Response, error) {
	method := endpoint[strings.LastIndexByte(endpoint, '/')+1:]
	switch method {
	case "getMe":
		return panelAPIResponse(&telego.User{ID: 500, Username: "settings_test_bot", IsBot: true})
	case "getChat":
		var request struct {
			ChatID int64 `json:"chat_id"`
		}
		if err := json.Unmarshal(data.BodyRaw, &request); err != nil {
			return nil, err
		}
		return panelAPIResponse(&telego.ChatFullInfo{
			ID: request.ChatID, Type: "supergroup", Title: fmt.Sprintf("Group %d", request.ChatID), Username: c.chatUsername,
		})
	case "getChatMember":
		c.memberCalls.Add(1)
		if c.admin {
			return panelAPIResponse(&telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator})
		}
		return panelAPIResponse(&telego.ChatMemberMember{Status: telego.MemberStatusMember})
	case "editMessageText":
		var request struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data.BodyRaw, &request); err != nil {
			return nil, err
		}
		c.lastEditText = request.Text
		if c.editErr != nil {
			return nil, c.editErr
		}
		return panelAPIResponse(&telego.Message{MessageID: 90})
	case "answerCallbackQuery":
		var request struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data.BodyRaw, &request); err != nil {
			return nil, err
		}
		c.lastAnswerText = request.Text
		return panelAPIResponse(true)
	case "sendMessage":
		var request struct {
			ChatID      int64  `json:"chat_id"`
			Text        string `json:"text"`
			ReplyMarkup struct {
				InlineKeyboard [][]struct {
					URL string `json:"url"`
				} `json:"inline_keyboard"`
				RemoveKeyboard bool `json:"remove_keyboard"`
			} `json:"reply_markup"`
		}
		if err := json.Unmarshal(data.BodyRaw, &request); err != nil {
			return nil, err
		}
		c.lastSendText = request.Text
		c.sendChats = append(c.sendChats, request.ChatID)
		c.sendTexts = append(c.sendTexts, request.Text)
		if len(request.ReplyMarkup.InlineKeyboard) > 0 && len(request.ReplyMarkup.InlineKeyboard[0]) > 0 {
			c.lastURL = request.ReplyMarkup.InlineKeyboard[0][0].URL
		}
		c.replyKeyboardRemoved = c.replyKeyboardRemoved || request.ReplyMarkup.RemoveKeyboard
		c.messageID++
		return panelAPIResponse(&telego.Message{MessageID: c.messageID})
	case "deleteMessage":
		return panelAPIResponse(true)
	case "unbanChatSenderChat":
		var request telego.UnbanChatSenderChatParams
		if err := json.Unmarshal(data.BodyRaw, &request); err != nil {
			return nil, err
		}
		c.senderUnbans = append(c.senderUnbans, request)
		return panelAPIResponse(true)
	default:
		return nil, fmt.Errorf("unexpected Telegram method %q", method)
	}
}

func panelAPIResponse(value any) (*ta.Response, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &ta.Response{Ok: true, Result: raw}, nil
}

type blockingPanelCaller struct {
	delegate ta.Caller

	memberCalls     atomic.Int32
	blockMemberCall atomic.Int32
	memberStarted   chan struct{}
	releaseMember   chan struct{}

	editCalls     atomic.Int32
	blockEditCall atomic.Int32
	editStarted   chan struct{}
	releaseEdit   chan struct{}

	sendCalls     atomic.Int32
	blockSendCall atomic.Int32
	sendStarted   chan struct{}
	releaseSend   chan struct{}
}

func newBlockingPanelCaller(delegate ta.Caller) *blockingPanelCaller {
	return &blockingPanelCaller{
		delegate:      delegate,
		memberStarted: make(chan struct{}), releaseMember: make(chan struct{}),
		editStarted: make(chan struct{}), releaseEdit: make(chan struct{}),
		sendStarted: make(chan struct{}), releaseSend: make(chan struct{}),
	}
}

func (c *blockingPanelCaller) Call(ctx context.Context, endpoint string, data *ta.RequestData) (*ta.Response, error) {
	method := endpoint[strings.LastIndexByte(endpoint, '/')+1:]
	switch method {
	case "getChatMember":
		call := c.memberCalls.Add(1)
		if call == c.blockMemberCall.Load() {
			c.memberStarted <- struct{}{}
			<-c.releaseMember
		}
	case "editMessageText":
		call := c.editCalls.Add(1)
		if call == c.blockEditCall.Load() {
			c.editStarted <- struct{}{}
			<-c.releaseEdit
		}
	case "sendMessage":
		call := c.sendCalls.Add(1)
		if call == c.blockSendCall.Load() {
			c.sendStarted <- struct{}{}
			<-c.releaseSend
		}
	}
	return c.delegate.Call(ctx, endpoint, data)
}

func waitForExpiredTombstonePrune(t *testing.T, panel *Panel, key promptKey) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		panel.panelState.mu.Lock()
		_, present := panel.panelState.tombstones[key]
		panel.panelState.mu.Unlock()
		if !present {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("callback did not resolve its session before the deadline")
		}
		runtime.Gosched()
	}
}

func waitForUserSessionRemoval(t *testing.T, panel *Panel, userID int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		panel.panelState.mu.Lock()
		_, present := panel.panelState.byUser[userID]
		panel.panelState.mu.Unlock()
		if !present {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement session did not remove the previous mapping before the deadline")
		}
		runtime.Gosched()
	}
}

func newSettingsPanelTest(t *testing.T, path string) (*Panel, *store.Settings, *panelAPICaller, *telego.Bot) {
	t.Helper()
	caller := &panelAPICaller{admin: true, messageID: 100}
	panel, settings, bot := newSettingsPanelTestWithCaller(t, path, caller)
	return panel, settings, caller, bot
}

func newSettingsPanelTestWithCaller(t *testing.T, path string, caller ta.Caller) (*Panel, *store.Settings, *telego.Bot) {
	t.Helper()
	cfg := &config.Config{
		Groups:           []config.GroupConfig{{ID: panelTestGroupA}, {ID: panelTestGroupB}},
		GroupIDs:         []int64{panelTestGroupA, panelTestGroupB},
		ControlGroupID:   panelTestGroupA,
		TimeoutSeconds:   240,
		NotifyTTLSeconds: -1,
	}
	settings, err := store.NewSettings(path, testSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	bot := newAPITestBot(t, caller)
	verifier := &panelVerifierStub{}
	telegram := tg.New(bot)
	moderation := moderate.New(settings, telegram, cfg, "")
	panel := New(settings, telegram, cfg, &i18n.Messages, verifier, moderation, nil, "test", time.Now())
	return panel, settings, bot
}

func addPanelSession(t *testing.T, panel *Panel, settings *store.Settings, groupID int64, screen string) *panelSession {
	t.Helper()
	session, err := panel.newSettingsSession(panelTestUser, groupID, i18n.LangEN)
	if err != nil {
		t.Fatal(err)
	}
	group, ok := settings.Group(groupID)
	if !ok {
		t.Fatalf("missing test group %d", groupID)
	}
	session.screen = screen
	session.chatID = panelTestUser
	session.messageID = 90
	session.revision = group.Revision()
	session.globalRevision = settings.Global().Revision()
	return session
}

func invokePanelCallback(t *testing.T, panel *Panel, bot *telego.Bot, session *panelSession, groupID int64, field, value string) {
	t.Helper()
	encoded, err := encodeCallback(callbackData{token: session.token, screen: session.screen, group: groupID, field: field, value: value})
	if err != nil {
		t.Fatal(err)
	}
	runFakeHandler(t, bot, panel.OnSettingsCallback, telego.Update{CallbackQuery: &telego.CallbackQuery{
		ID: "callback", From: telego.User{ID: panelTestUser, LanguageCode: "en"}, Data: encoded,
		Message: &telego.Message{MessageID: session.messageID, Chat: telego.Chat{ID: panelTestUser, Type: "private"}},
	}})
}

func TestSettingsLauncherOpensGroupPickerWithoutVerification(t *testing.T) {
	panel, _, caller, bot := newSettingsPanelTest(t, "")
	verifier := panel.verifier.(*panelVerifierStub)
	runFakeHandler(t, bot, panel.OnSettings, telego.Update{Message: &telego.Message{
		MessageID: 12,
		Chat:      telego.Chat{ID: panelTestGroupA, Type: "supergroup"},
		From:      &telego.User{ID: panelTestUser, LanguageCode: "en"},
		Text:      "/settings",
	}})
	session := panel.sessionByUser(panelTestUser)
	if session == nil || !strings.Contains(caller.lastURL, "?start=panel_"+session.token) {
		t.Fatalf("launcher URL = %q, session = %+v", caller.lastURL, session)
	}
	if caller.lastSendText != i18n.Messages.Panel.Settings.Launch.Sent.For(i18n.LangZH) {
		t.Fatalf("launcher text = %q", caller.lastSendText)
	}
	startToken := session.token
	runFakeHandler(t, bot, panel.OnStart, telego.Update{Message: &telego.Message{
		MessageID: 13,
		Chat:      telego.Chat{ID: panelTestUser, Type: "private"},
		From:      &telego.User{ID: panelTestUser, LanguageCode: "en"},
		Text:      "/start panel_" + startToken,
	}})
	if verifier.challengeCalls != 0 {
		t.Fatalf("panel deep link launched verification %d times", verifier.challengeCalls)
	}
	groupA := i18n.Messages.Panel.Settings.Value.GroupButton.Render(
		i18n.LangEN, fmt.Sprintf("Group %d", panelTestGroupA), panelTestGroupA)
	groupB := i18n.Messages.Panel.Settings.Value.GroupButton.Render(
		i18n.LangEN, fmt.Sprintf("Group %d", panelTestGroupB), panelTestGroupB)
	wantPicker := i18n.Messages.Panel.Settings.Screen.Groups.Render(i18n.LangEN, 1, groupA+"\n"+groupB)
	if session.messageID == 0 || session.screen != "gl" || caller.lastSendText != wantPicker {
		t.Fatalf("group picker = %q session=%+v, want catalogue screen %q", caller.lastSendText, session, wantPicker)
	}
}

func TestVerificationStartPayloadSelectsOnePendingGroupAndBarePayloadStillFansOut(t *testing.T) {
	const userID int64 = 8801
	questionA := config.Question{Q: "Group A question", Options: []string{"A", "B"}, Answer: 0}
	questionB := config.Question{Q: "Group B question", Options: []string{"A", "B"}, Answer: 1}

	newFlow := func(t *testing.T) (*Panel, *verify.Service, *panelAPICaller, *telego.Bot) {
		t.Helper()
		cfg := &config.Config{
			Groups: []config.GroupConfig{
				{ID: panelTestGroupA, VerifyMode: config.ModeQuiz, Questions: []config.Question{questionA}},
				{ID: panelTestGroupB, VerifyMode: config.ModeQuiz, Questions: []config.Question{questionB}},
			},
			GroupIDs:       []int64{panelTestGroupA, panelTestGroupB},
			ControlGroupID: panelTestGroupA,
			Lang:           "en",
			VerifyMode:     config.ModeQuiz,
			TimeoutSeconds: 240,
		}
		settings, err := store.NewSettings("", testSettingsBaseline(t, cfg))
		if err != nil {
			t.Fatal(err)
		}
		caller := &panelAPICaller{messageID: 100}
		bot := newAPITestBot(t, caller)
		telegram := tg.New(bot)
		verifier := verify.New(settings, telegram, cfg, &i18n.Messages, bot,
			verify.Identity{ID: 500, Username: "settings_test_bot"}, "")
		t.Cleanup(verifier.Shutdown)
		panel := New(settings, telegram, cfg, &i18n.Messages, verifier, nil, nil, "test", time.Now())
		for _, groupID := range []int64{panelTestGroupA, panelTestGroupB} {
			runFakeHandler(t, bot, verifier.OnJoinRequest, telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
				Chat: telego.Chat{ID: groupID, Type: "supergroup"},
				From: telego.User{ID: userID, FirstName: "Applicant", LanguageCode: "en"},
			}})
		}
		caller.sendChats = nil
		caller.sendTexts = nil
		return panel, verifier, caller, bot
	}

	t.Run("group-scoped payload", func(t *testing.T) {
		panel, _, caller, bot := newFlow(t)
		runFakeHandler(t, bot, panel.OnStart, telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: userID, Type: "private"},
			From: &telego.User{ID: userID, LanguageCode: "en"},
			Text: fmt.Sprintf("/start verify_%d", panelTestGroupB),
		}})
		want := i18n.Messages.Verification.Challenge.QuizPrompt.Render(i18n.LangEN, html.EscapeString(questionB.Q))
		if len(caller.sendTexts) != 1 || caller.sendChats[0] != userID || caller.sendTexts[0] != want {
			t.Fatalf("scoped start sends/chats = %q/%v, want only group B catalogue prompt %q", caller.sendTexts, caller.sendChats, want)
		}
	})

	t.Run("bare payload", func(t *testing.T) {
		panel, _, caller, bot := newFlow(t)
		runFakeHandler(t, bot, panel.OnStart, telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: userID, Type: "private"},
			From: &telego.User{ID: userID, LanguageCode: "en"},
			Text: "/start verify",
		}})
		if len(caller.sendTexts) != 2 || caller.sendChats[0] != userID || caller.sendChats[1] != userID {
			t.Fatalf("bare start sends/chats = %q/%v, want both live challenges", caller.sendTexts, caller.sendChats)
		}
	})
}

func TestPanelDMFirstTogglePersistsAndRejectsStaleRevision(t *testing.T) {
	path := t.TempDir() + "/settings.json"
	panel, settings, caller, bot := newSettingsPanelTest(t, path)
	session := addPanelSession(t, panel, settings, panelTestGroupA, "rt")

	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "df", "_")
	readOverride := func() *bool {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var state struct {
			Groups map[string]struct {
				DMFirst *bool `json:"dm_first"`
			} `json:"groups"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		return state.Groups[strconv.FormatInt(panelTestGroupA, 10)].DMFirst
	}
	if value := readOverride(); value == nil || *value {
		t.Fatalf("DM-first override after panel toggle = %v, want persisted false", value)
	}
	setting, ok := settings.Group(panelTestGroupA)
	if !ok {
		t.Fatal("panel group disappeared after DM-first toggle")
	}
	if got := setting.DMFirst(); got.Value || got.Source != store.SourceRuntime {
		t.Fatalf("effective DM-first setting after panel toggle = %+v", got)
	}

	group, _ := settings.Group(panelTestGroupA)
	next := group.Overrides()
	spoiler := !group.NameSpoiler().Value
	next.NameSpoiler = &spoiler
	if _, err := settings.CommitGroup(panelTestGroupA, group.Revision(), next); err != nil {
		t.Fatal(err)
	}
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "df", "_")
	if value := readOverride(); value == nil || *value {
		t.Fatalf("stale callback changed persisted DM-first override to %v", value)
	}
	if caller.lastEditText != i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN) {
		t.Fatalf("stale DM-first callback message = %q, want catalogue conflict text %q",
			caller.lastEditText, i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN))
	}
}

func TestPanelSettingsScreensExposeCatalogueValuesAndCallbacks(t *testing.T) {
	testSettingsScreenContracts(t)
}

func TestPanelCallbackCodecWorstCase(t *testing.T) {
	literal := "p1:0123456789abcdef:li:ffffffffffffffff:cw:ffffffffffffffff"
	if got := len(literal); got != 59 {
		t.Fatalf("worst-case callback length = %d, want 59", got)
	}
	decoded, err := parseCallback(literal)
	if err != nil {
		t.Fatalf("parse worst-case callback: %v", err)
	}
	if decoded.group != math.MinInt64 || decoded.value != "ffffffffffffffff" {
		t.Fatalf("worst-case callback decoded as %+v", decoded)
	}
	for _, groupID := range []int64{math.MinInt64, math.MaxInt64, -1, 0, 1} {
		encoded, err := encodeCallback(callbackData{
			token: "0123456789abcdef", screen: "li", group: groupID, field: "cw", value: "ffffffffffffffff",
		})
		if err != nil {
			t.Fatalf("encode group %d: %v", groupID, err)
		}
		roundTrip, err := parseCallback(encoded)
		if err != nil || roundTrip.group != groupID {
			t.Fatalf("round trip group %d = %+v, %v", groupID, roundTrip, err)
		}
	}
	for _, malformed := range []string{
		"p2:0123456789abcdef:li:1:cw:1", "p1:ABCDEF0123456789:li:1:cw:1",
		"p1:0123456789abcdef:li:1:xx:1", literal + ":extra",
	} {
		if _, err := parseCallback(malformed); err == nil {
			t.Errorf("parseCallback(%q) succeeded", malformed)
		}
	}
}

func TestPanelInputPrecedesKernelForSameUser(t *testing.T) {
	verifier := &panelVerifierStub{kernelPending: true}
	panel := &Panel{verifier: verifier, panelState: newSettingsPanelState()}
	session, err := panel.newSettingsSession(panelTestUser, panelTestGroupA, i18n.LangEN)
	if err != nil {
		t.Fatal(err)
	}
	session.pending = &pendingInput{kind: inputQuizQuestion, promptMessageID: 71}
	update := telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: panelTestUser, Type: "private"}, From: &telego.User{ID: panelTestUser}, Text: "6.12.41",
		ReplyToMessage: &telego.Message{MessageID: 71},
	}}
	if !panel.PanelInputDM(context.Background(), update) {
		t.Fatal("exact panel prompt reply did not match panel input")
	}
	if !verifier.KernelAnswerDM(context.Background(), update) {
		t.Fatal("test did not establish a simultaneous kernel pending")
	}
	if session.pending != nil {
		t.Fatal("kernel activation did not cancel panel input")
	}
	if _, ok := panel.consumeTombstone(promptKey{userID: panelTestUser, messageID: 71}); !ok {
		t.Fatal("canceled prompt tombstone was not retained")
	}
}

func TestConcurrentCallbackReplayUsesRotatedTokenOnce(t *testing.T) {
	base := &panelAPICaller{admin: true, messageID: 100}
	caller := newBlockingPanelCaller(base)
	caller.blockEditCall.Store(1)
	panel, settings, bot := newSettingsPanelTestWithCaller(t, "", caller)
	session := addPanelSession(t, panel, settings, panelTestGroupA, "rt")
	group, _ := settings.Group(panelTestGroupA)
	beforeRevision := group.Revision()
	beforeEnabled := group.Enabled().Value
	encoded, err := encodeCallback(callbackData{
		token: session.token, screen: session.screen, group: panelTestGroupA, field: "en", value: "_",
	})
	if err != nil {
		t.Fatal(err)
	}
	update := telego.Update{CallbackQuery: &telego.CallbackQuery{
		ID: "callback", From: telego.User{ID: panelTestUser, LanguageCode: "en"}, Data: encoded,
		Message: &telego.Message{MessageID: session.messageID, Chat: telego.Chat{ID: panelTestUser, Type: "private"}},
	}}

	firstDone := startFakeHandler(t, bot, panel.OnSettingsCallback, update)
	<-caller.editStarted
	marker := promptKey{userID: -1, messageID: -1}
	panel.panelState.mu.Lock()
	panel.panelState.tombstones[marker] = inputTombstone{expiresAt: time.Now().Add(-time.Second)}
	panel.panelState.mu.Unlock()
	secondDone := startFakeHandler(t, bot, panel.OnSettingsCallback, update)
	waitForExpiredTombstonePrune(t, panel, marker)
	close(caller.releaseEdit)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}

	current, _ := settings.Group(panelTestGroupA)
	if got := current.Revision(); got != beforeRevision+1 {
		t.Fatalf("concurrent callback revision = %d, want %d", got, beforeRevision+1)
	}
	if got := current.Enabled().Value; got == beforeEnabled {
		t.Fatalf("concurrent callback restored enabled=%v instead of applying one toggle", got)
	}
}

func TestPanelInputCancelsWhenKernelStartsDuringAuthorization(t *testing.T) {
	base := &panelAPICaller{admin: true, messageID: 100}
	caller := newBlockingPanelCaller(base)
	caller.blockMemberCall.Store(1)
	panel, settings, bot := newSettingsPanelTestWithCaller(t, "", caller)
	session := addPanelSession(t, panel, settings, panelTestGroupA, "vp")
	group, _ := settings.Group(panelTestGroupA)
	beforeRevision := group.Revision()
	beforeTimeout := group.TimeoutSeconds().Value
	session.pending = &pendingInput{
		kind: inputTimeout, parent: "vp", promptMessageID: 71, expectedRevision: beforeRevision,
	}
	update := telego.Update{Message: &telego.Message{
		MessageID: 1071, Chat: telego.Chat{ID: panelTestUser, Type: "private"},
		From: &telego.User{ID: panelTestUser, LanguageCode: "en"}, Text: "600",
		ReplyToMessage: &telego.Message{MessageID: 71},
	}}
	if !panel.PanelInputDM(context.Background(), update) {
		t.Fatal("exact ForceReply did not match panel input predicate")
	}

	done := startFakeHandler(t, bot, panel.OnPanelInput, update)
	<-caller.memberStarted
	panel.verifier.(*panelVerifierStub).kernelPending = true
	close(caller.releaseMember)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	current, _ := settings.Group(panelTestGroupA)
	if current.Revision() != beforeRevision || current.TimeoutSeconds().Value != beforeTimeout {
		t.Fatalf("kernel activation changed timeout/revision to %d/%d", current.TimeoutSeconds().Value, current.Revision())
	}
	if session.pending != nil {
		t.Fatal("kernel activation retained the panel prompt")
	}
	if _, ok := panel.consumeTombstone(promptKey{userID: panelTestUser, messageID: 71}); !ok {
		t.Fatal("kernel activation did not tombstone the panel prompt")
	}
	if !base.replyKeyboardRemoved {
		t.Fatal("kernel activation did not remove the panel reply keyboard")
	}
	want := i18n.Messages.Panel.Settings.Error.InputCanceledVerification.For(i18n.LangEN)
	if base.lastSendText != want {
		t.Fatalf("kernel activation notice = %q, want catalogue text %q", base.lastSendText, want)
	}
}

func TestPanelInputRejectsSessionReplacedDuringAuthorization(t *testing.T) {
	base := &panelAPICaller{admin: true, messageID: 100}
	caller := newBlockingPanelCaller(base)
	caller.blockMemberCall.Store(1)
	panel, settings, bot := newSettingsPanelTestWithCaller(t, "", caller)
	session := addPanelSession(t, panel, settings, panelTestGroupA, "vp")
	group, _ := settings.Group(panelTestGroupA)
	beforeRevision := group.Revision()
	beforeTimeout := group.TimeoutSeconds().Value
	session.pending = &pendingInput{
		kind: inputTimeout, parent: "vp", promptMessageID: 72, expectedRevision: beforeRevision,
	}
	update := telego.Update{Message: &telego.Message{
		MessageID: 1072, Chat: telego.Chat{ID: panelTestUser, Type: "private"},
		From: &telego.User{ID: panelTestUser, LanguageCode: "en"}, Text: "600",
		ReplyToMessage: &telego.Message{MessageID: 72},
	}}
	if !panel.PanelInputDM(context.Background(), update) {
		t.Fatal("exact ForceReply did not match panel input predicate")
	}

	handlerDone := startFakeHandler(t, bot, panel.OnPanelInput, update)
	<-caller.memberStarted
	replacementDone := make(chan error, 1)
	go func() {
		_, err := panel.newSettingsSession(panelTestUser, panelTestGroupA, i18n.LangEN)
		replacementDone <- err
	}()
	waitForUserSessionRemoval(t, panel, panelTestUser)
	close(caller.releaseMember)
	if err := <-handlerDone; err != nil {
		t.Fatal(err)
	}
	if err := <-replacementDone; err != nil {
		t.Fatal(err)
	}

	current, _ := settings.Group(panelTestGroupA)
	if current.Revision() != beforeRevision || current.TimeoutSeconds().Value != beforeTimeout {
		t.Fatalf("replaced session changed timeout/revision to %d/%d", current.TimeoutSeconds().Value, current.Revision())
	}
}

func TestConcurrentPanelStartRejectsRotatedToken(t *testing.T) {
	base := &panelAPICaller{admin: true, messageID: 100}
	caller := newBlockingPanelCaller(base)
	caller.blockSendCall.Store(1)
	panel, settings, bot := newSettingsPanelTestWithCaller(t, "", caller)
	session := addPanelSession(t, panel, settings, panelTestGroupA, "gl")
	session.messageID = 0
	token := session.token
	update := telego.Update{Message: &telego.Message{
		MessageID: 13, Chat: telego.Chat{ID: panelTestUser, Type: "private"},
		From: &telego.User{ID: panelTestUser, LanguageCode: "en"}, Text: "/start panel_" + token,
	}}

	firstDone := startFakeHandler(t, bot, panel.OnStart, update)
	<-caller.sendStarted
	caller.blockMemberCall.Store(caller.memberCalls.Load() + 1)
	secondDone := startFakeHandler(t, bot, panel.OnStart, update)
	<-caller.memberStarted
	close(caller.releaseMember)
	close(caller.releaseSend)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}

	if got := caller.editCalls.Load(); got != 0 {
		t.Fatalf("replayed panel start rendered %d additional panel edits", got)
	}
	want := i18n.Messages.Panel.Settings.Error.Expired.For(i18n.LangEN)
	if base.lastSendText != want {
		t.Fatalf("replayed panel start notice = %q, want catalogue text %q", base.lastSendText, want)
	}
}

func submitPanelText(t *testing.T, panel *Panel, bot *telego.Bot, session *panelSession, text string) {
	t.Helper()
	if session.pending == nil || session.pending.promptMessageID == 0 {
		t.Fatal("panel has no active text prompt")
	}
	update := telego.Update{Message: &telego.Message{
		MessageID:      session.pending.promptMessageID + 1000,
		Chat:           telego.Chat{ID: panelTestUser, Type: "private"},
		From:           &telego.User{ID: panelTestUser, LanguageCode: "en"},
		Text:           text,
		ReplyToMessage: &telego.Message{MessageID: session.pending.promptMessageID},
	}}
	if !panel.PanelInputDM(context.Background(), update) {
		t.Fatal("exact ForceReply did not match panel input predicate")
	}
	runFakeHandler(t, bot, panel.OnPanelInput, update)
}

func submitSharedChat(t *testing.T, panel *Panel, bot *telego.Bot, session *panelSession, chatID int64) {
	t.Helper()
	if session.pending == nil || session.pending.requestID == 0 {
		t.Fatal("panel has no active chat picker")
	}
	update := telego.Update{Message: &telego.Message{
		MessageID: session.pending.promptMessageID + 1000,
		Chat:      telego.Chat{ID: panelTestUser, Type: "private"},
		From:      &telego.User{ID: panelTestUser, LanguageCode: "en"},
		ChatShared: &telego.ChatShared{
			RequestID: int(session.pending.requestID),
			ChatID:    chatID,
		},
	}}
	if !panel.PanelChatSharedDM(context.Background(), update) {
		t.Fatal("exact ChatShared request did not match panel predicate")
	}
	runFakeHandler(t, bot, panel.OnPanelChatShared, update)
}

func TestPanelQuizAndFallbackQuestionLifecycles(t *testing.T) {
	panel, settings, _, bot := newSettingsPanelTest(t, "")
	session := addPanelSession(t, panel, settings, panelTestGroupA, "qb")

	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ca", "_")
	submitPanelText(t, panel, bot, session, "Original quiz")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "qo", "_")
	submitPanelText(t, panel, bot, session, "Correct")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "qo", "_")
	submitPanelText(t, panel, bot, session, "Wrong")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ok", encodeUnsigned(0))
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "sv", "_")

	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "qq", encodeUnsigned(0))
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "qq", "_")
	submitPanelText(t, panel, bot, session, "Edited quiz")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "sv", "_")
	group, _ := settings.Group(panelTestGroupA)
	if questions := group.Questions().Value; len(questions) != 1 || questions[0].Q != "Edited quiz" {
		t.Fatalf("quiz add/edit result = %+v", questions)
	}
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "qq", encodeUnsigned(0))
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "rm", "_")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ok", "_")
	group, _ = settings.Group(panelTestGroupA)
	if len(group.Questions().Value) != 0 {
		t.Fatalf("quiz delete result = %+v", group.Questions().Value)
	}

	session.screen = "fb"
	session.page = 0
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ca", "_")
	submitPanelText(t, panel, bot, session, "Original fallback")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "fa", "_")
	submitPanelText(t, panel, bot, session, "answer")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "sv", "_")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "fq", encodeUnsigned(0))
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "fq", "_")
	submitPanelText(t, panel, bot, session, "Edited fallback")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "sv", "_")
	group, _ = settings.Group(panelTestGroupA)
	if questions := group.FallbackQuestions().Value; len(questions) != 1 || questions[0].Q != "Edited fallback" {
		t.Fatalf("fallback add/edit result = %+v", questions)
	}
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "fq", encodeUnsigned(0))
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "rm", "_")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ok", "_")
	group, _ = settings.Group(panelTestGroupA)
	if !group.FallbackBuiltin().Value {
		t.Fatal("deleting the last fallback question did not restore built-ins")
	}
}

func TestPanelChatListsAndRequiredChannel(t *testing.T) {
	const sharedChatID int64 = -1009000000999
	panel, settings, _, bot := newSettingsPanelTest(t, "")
	session := addPanelSession(t, panel, settings, panelTestGroupA, "ls")

	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "kc", "_")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ca", "kc")
	submitSharedChat(t, panel, bot, session, sharedChatID)
	group, _ := settings.Group(panelTestGroupA)
	if values := group.KnownChatIDs().Value; len(values) != 1 || values[0] != sharedChatID {
		t.Fatalf("known chat add result = %v", values)
	}
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "kc", encodeSigned(sharedChatID))
	group, _ = settings.Group(panelTestGroupA)
	if len(group.KnownChatIDs().Value) != 0 {
		t.Fatalf("known chat remove result = %v", group.KnownChatIDs().Value)
	}

	session.screen = "ch"
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ci", "_")
	submitSharedChat(t, panel, bot, session, sharedChatID)
	if session.pending == nil || session.pending.kind != inputInviteURL {
		t.Fatal("private channel selection did not request an invite link")
	}
	submitPanelText(t, panel, bot, session, "https://t.me/+privateinvite")
	group, _ = settings.Group(panelTestGroupA)
	if panel.requiredChannelID(group) != sharedChatID || group.ChannelInviteURL().Value != "https://t.me/+privateinvite" {
		t.Fatalf("required channel result = id %d invite %q", panel.requiredChannelID(group), group.ChannelInviteURL().Value)
	}
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ds", "_")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ok", "_")
	group, _ = settings.Group(panelTestGroupA)
	if panel.requiredChannelID(group) != 0 {
		t.Fatalf("required channel disable result = %d", panel.requiredChannelID(group))
	}
}

func TestPanelChannelWhitelistUsesModerationPolicy(t *testing.T) {
	const (
		whitelistLimit = 4096
		sharedChatID   = int64(-1009000000999)
	)
	panel, settings, caller, bot := newSettingsPanelTest(t, "")
	group, _ := settings.Group(panelTestGroupA)
	whitelist := make([]int64, whitelistLimit)
	for index := range whitelist {
		whitelist[index] = -1008000000000 - int64(index)
	}
	overrides := group.Overrides()
	overrides.ChannelWhitelist = &whitelist
	if _, err := settings.CommitGroup(panelTestGroupA, group.Revision(), overrides); err != nil {
		t.Fatal(err)
	}

	session := addPanelSession(t, panel, settings, panelTestGroupA, "ls")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "cw", "_")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ca", "cw")
	submitSharedChat(t, panel, bot, session, sharedChatID)

	group, _ = settings.Group(panelTestGroupA)
	values := group.ChannelWhitelist().Value
	if len(values) != whitelistLimit {
		t.Fatalf("panel whitelist entries = %d, want %d", len(values), whitelistLimit)
	}
	if values[0] != whitelist[1] || values[len(values)-1] != sharedChatID {
		t.Fatalf("panel whitelist did not evict the oldest entry: first=%d last=%d", values[0], values[len(values)-1])
	}
	if len(caller.senderUnbans) != 1 {
		t.Fatalf("panel sender unbans = %d, want 1", len(caller.senderUnbans))
	}
	unban := caller.senderUnbans[0]
	if unban.ChatID.ID != panelTestGroupA || unban.SenderChatID != sharedChatID {
		t.Fatalf("panel sender unban = chat %d sender %d", unban.ChatID.ID, unban.SenderChatID)
	}
}

func TestPanelDemotedAdminLosesSession(t *testing.T) {
	panel, settings, caller, bot := newSettingsPanelTest(t, "")
	session := addPanelSession(t, panel, settings, panelTestGroupB, "rt")
	if admin, err := panel.telegram.CachedAdmin(context.Background(), panelTestGroupB, panelTestUser); err != nil || !admin {
		t.Fatalf("prime admin cache = %v, %v", admin, err)
	}
	caller.admin = false
	invokePanelCallback(t, panel, bot, session, panelTestGroupB, "en", "_")
	group, _ := settings.Group(panelTestGroupB)
	if !group.Enabled().Value {
		t.Fatal("demoted callback changed settings")
	}
	if panel.sessionByUser(panelTestUser) != nil {
		t.Fatal("demoted callback retained the panel session")
	}
	if caller.lastEditText != i18n.Messages.Panel.Settings.Error.AuthorizationLost.For(i18n.LangEN) {
		t.Fatalf("demotion message = %q", caller.lastEditText)
	}
	if caller.memberCalls.Load() != 2 {
		t.Fatalf("membership lookups = %d, want one cached prime and one fresh callback check", caller.memberCalls.Load())
	}
}

func TestPanelPerGroupChangeIgnoresControlGroupGate(t *testing.T) {
	panel, settings, _, bot := newSettingsPanelTest(t, "")
	session := addPanelSession(t, panel, settings, panelTestGroupB, "rt")
	invokePanelCallback(t, panel, bot, session, panelTestGroupB, "en", "_")
	group, _ := settings.Group(panelTestGroupB)
	if group.Enabled().Value {
		t.Fatal("fresh admin could not change a non-control group's own setting")
	}
}

func TestPanelStaleSessionAfterRestartExpires(t *testing.T) {
	panel, settings, caller, bot := newSettingsPanelTest(t, "")
	session := addPanelSession(t, panel, settings, panelTestGroupA, "rt")
	restarted := New(settings, tg.New(bot), panel.cfg, &i18n.Messages, &panelVerifierStub{}, nil, nil, "test", time.Now())
	invokePanelCallback(t, restarted, bot, session, panelTestGroupA, "en", "_")
	group, _ := settings.Group(panelTestGroupA)
	if !group.Enabled().Value {
		t.Fatal("callback from before restart changed settings")
	}
	if caller.lastEditText != i18n.Messages.Panel.Settings.Error.Expired.For(i18n.LangEN) {
		t.Fatalf("stale-session message = %q", caller.lastEditText)
	}
}

func TestPanelStaleRevisionRefused(t *testing.T) {
	panel, settings, caller, bot := newSettingsPanelTest(t, "")
	session := addPanelSession(t, panel, settings, panelTestGroupA, "rt")
	group, _ := settings.Group(panelTestGroupA)
	next := group.Overrides()
	spoiler := !group.NameSpoiler().Value
	next.NameSpoiler = &spoiler
	if _, err := settings.CommitGroup(panelTestGroupA, group.Revision(), next); err != nil {
		t.Fatal(err)
	}
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "en", "_")
	current, _ := settings.Group(panelTestGroupA)
	if !current.Enabled().Value {
		t.Fatal("stale callback changed settings")
	}
	if caller.lastEditText != i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN) {
		t.Fatalf("stale revision message = %q", caller.lastEditText)
	}
}

func TestPanelStaleQuestionIndexRefused(t *testing.T) {
	panel, settings, caller, bot := newSettingsPanelTest(t, "")
	group, _ := settings.Group(panelTestGroupA)
	questions := []config.Question{
		{Q: "First", Options: []string{"A", "B"}, Answer: 0},
		{Q: "Second", Options: []string{"A", "B"}, Answer: 1},
	}
	next := group.Overrides()
	next.Questions = &questions
	result, err := settings.CommitGroup(panelTestGroupA, group.Revision(), next)
	if err != nil {
		t.Fatal(err)
	}
	session := addPanelSession(t, panel, settings, panelTestGroupA, "qb")
	session.revision = result.Revision
	group, _ = settings.Group(panelTestGroupA)
	questions = questions[:1]
	next = group.Overrides()
	next.Questions = &questions
	if _, err := settings.CommitGroup(panelTestGroupA, group.Revision(), next); err != nil {
		t.Fatal(err)
	}
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "qq", encodeUnsigned(1))
	if caller.lastEditText != i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN) {
		t.Fatalf("stale question message = %q", caller.lastEditText)
	}
	current, _ := settings.Group(panelTestGroupA)
	if len(current.Questions().Value) != 1 || current.Questions().Value[0].Q != "First" {
		t.Fatalf("stale question callback changed bank: %+v", current.Questions().Value)
	}
}

func TestPanelFailedCommitSurfaced(t *testing.T) {
	panel, settings, caller, bot := newSettingsPanelTest(t, t.TempDir())
	session := addPanelSession(t, panel, settings, panelTestGroupA, "rt")
	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "en", "_")
	group, _ := settings.Group(panelTestGroupA)
	if !group.Enabled().Value {
		t.Fatal("failed commit published a setting")
	}
	if caller.lastEditText != i18n.Messages.Panel.Settings.Error.SaveFailed.For(i18n.LangEN) {
		t.Fatalf("failed commit message = %q", caller.lastEditText)
	}
	if err := settings.Persistence().LastError; err == nil {
		t.Fatal("failed settings path did not retain its error")
	}
}

func TestPanelPostCommitRenderFailureDoesNotClaimSaveFailed(t *testing.T) {
	panel, settings, caller, bot := newSettingsPanelTest(t, "")
	session := addPanelSession(t, panel, settings, panelTestGroupA, "rt")
	caller.editErr = errors.New("edit failed after commit")

	invokePanelCallback(t, panel, bot, session, panelTestGroupA, "en", "_")

	group, _ := settings.Group(panelTestGroupA)
	if group.Enabled().Value {
		t.Fatal("runtime setting was not committed before the render failure")
	}
	want := i18n.Messages.Panel.Settings.Error.SavedRenderFailed.For(i18n.LangEN)
	if caller.lastAnswerText != want {
		t.Fatalf("post-commit callback message = %q, want saved-render warning %q", caller.lastAnswerText, want)
	}
}

func assertSavedRenderWarning(t *testing.T, got string) {
	t.Helper()
	want := i18n.Messages.Panel.Settings.Error.SavedRenderFailed.For(i18n.LangEN)
	if got != want {
		t.Fatalf("post-commit render message = %q, want catalogue warning %q", got, want)
	}
}

func TestPanelCommittedCallbackRenderFailuresReportSaved(t *testing.T) {
	t.Run("quiz save", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		session := addPanelSession(t, panel, settings, panelTestGroupA, "qd")
		session.quiz = &quizDraft{
			index: -1,
			question: config.Question{
				Q: "Question", Options: []string{"Correct", "Wrong"}, Answer: 0,
			},
			revision: session.revision,
		}
		before := session.revision
		caller.editErr = errors.New("edit failed after quiz commit")

		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "sv", "_")

		group, _ := settings.Group(panelTestGroupA)
		if group.Revision() != before+1 || len(group.Questions().Value) != 1 {
			t.Fatalf("quiz save state = revision %d, questions %+v", group.Revision(), group.Questions().Value)
		}
		assertSavedRenderWarning(t, caller.lastAnswerText)
	})

	t.Run("fallback save", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		session := addPanelSession(t, panel, settings, panelTestGroupA, "fd")
		session.fallback = &fallbackDraft{
			index: -1,
			question: config.ShortQuestion{
				Q: "Fallback", Answers: []string{"Answer"},
			},
			revision: session.revision,
		}
		before := session.revision
		caller.editErr = errors.New("edit failed after fallback commit")

		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "sv", "_")

		group, _ := settings.Group(panelTestGroupA)
		if group.Revision() != before+1 || len(group.FallbackQuestions().Value) != 1 {
			t.Fatalf("fallback save state = revision %d, questions %+v", group.Revision(), group.FallbackQuestions().Value)
		}
		assertSavedRenderWarning(t, caller.lastAnswerText)
	})

	t.Run("channel change", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		group, _ := settings.Group(panelTestGroupA)
		display, invite := "@required", "https://t.me/+invite"
		next := group.Overrides()
		next.ChannelDisplay = &display
		next.ChannelInviteURL = &invite
		if _, err := settings.CommitGroup(panelTestGroupA, group.Revision(), next); err != nil {
			t.Fatal(err)
		}
		session := addPanelSession(t, panel, settings, panelTestGroupA, "ch")
		before := session.revision
		caller.editErr = errors.New("edit failed after channel commit")

		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "dl", "_")

		group, _ = settings.Group(panelTestGroupA)
		if group.Revision() != before+1 || group.ChannelInviteURL().Value != "" {
			t.Fatalf("channel save state = revision %d, invite %q", group.Revision(), group.ChannelInviteURL().Value)
		}
		assertSavedRenderWarning(t, caller.lastAnswerText)
	})

	t.Run("list change", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		group, _ := settings.Group(panelTestGroupA)
		const knownID int64 = -1009000000601
		known := []int64{knownID}
		next := group.Overrides()
		next.KnownChatIDs = &known
		if _, err := settings.CommitGroup(panelTestGroupA, group.Revision(), next); err != nil {
			t.Fatal(err)
		}
		session := addPanelSession(t, panel, settings, panelTestGroupA, "li")
		session.listKind = inputKnownChat
		before := session.revision
		caller.editErr = errors.New("edit failed after list commit")

		invokePanelCallback(t, panel, bot, session, panelTestGroupA, string(inputKnownChat), encodeSigned(knownID))

		group, _ = settings.Group(panelTestGroupA)
		if group.Revision() != before+1 || len(group.KnownChatIDs().Value) != 0 {
			t.Fatalf("list save state = revision %d, values %v", group.Revision(), group.KnownChatIDs().Value)
		}
		assertSavedRenderWarning(t, caller.lastAnswerText)
	})

	t.Run("channel whitelist change", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		group, _ := settings.Group(panelTestGroupA)
		const senderID int64 = -1009000000602
		whitelist := []int64{senderID}
		next := group.Overrides()
		next.ChannelWhitelist = &whitelist
		if _, err := settings.CommitGroup(panelTestGroupA, group.Revision(), next); err != nil {
			t.Fatal(err)
		}
		session := addPanelSession(t, panel, settings, panelTestGroupA, "li")
		session.listKind = inputChannelWhitelist
		before := session.revision
		caller.editErr = errors.New("edit failed after whitelist commit")

		invokePanelCallback(t, panel, bot, session, panelTestGroupA, string(inputChannelWhitelist), encodeSigned(senderID))

		group, _ = settings.Group(panelTestGroupA)
		if group.Revision() != before+1 || len(group.ChannelWhitelist().Value) != 0 {
			t.Fatalf("whitelist save state = revision %d, values %v", group.Revision(), group.ChannelWhitelist().Value)
		}
		assertSavedRenderWarning(t, caller.lastAnswerText)
	})

	t.Run("confirmation", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		group, _ := settings.Group(panelTestGroupA)
		channelID := int64(-1009000000603)
		display, invite := "@required", "https://t.me/+invite"
		next := group.Overrides()
		next.RequiredChannelID = &channelID
		next.ChannelDisplay = &display
		next.ChannelInviteURL = &invite
		if _, err := settings.CommitGroup(panelTestGroupA, group.Revision(), next); err != nil {
			t.Fatal(err)
		}
		session := addPanelSession(t, panel, settings, panelTestGroupA, "cf")
		session.confirm = &confirmation{kind: "channel", revision: session.revision}
		before := session.revision
		caller.editErr = errors.New("edit failed after confirmation commit")

		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ok", "_")

		group, _ = settings.Group(panelTestGroupA)
		if group.Revision() != before+1 {
			t.Fatalf("confirmation revision = %d, want %d", group.Revision(), before+1)
		}
		assertSavedRenderWarning(t, caller.lastAnswerText)
	})
}

func TestPanelCommittedTextInputRenderFailuresReportSaved(t *testing.T) {
	tests := []struct {
		name string
		kind inputKind
		text string
	}{
		{name: "group commit", kind: inputTimeout, text: "600"},
		{name: "global commit", kind: inputPrivateRate, text: "9"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			panel, settings, caller, bot := newSettingsPanelTest(t, "")
			session := addPanelSession(t, panel, settings, panelTestGroupA, "in")
			session.pending = &pendingInput{
				kind: test.kind, parent: "vp", promptMessageID: 71, expectedRevision: session.revision,
			}
			beforeGroup := session.revision
			beforeGlobal := session.globalRevision
			caller.editErr = errors.New("edit failed after text-input commit")

			submitPanelText(t, panel, bot, session, test.text)

			group, _ := settings.Group(panelTestGroupA)
			global := settings.Global()
			switch test.kind {
			case inputTimeout:
				if group.Revision() != beforeGroup+1 || group.TimeoutSeconds().Value != 600 {
					t.Fatalf("group input state = revision %d, timeout %d", group.Revision(), group.TimeoutSeconds().Value)
				}
			case inputPrivateRate:
				if global.Revision() != beforeGlobal+1 || global.PrivateQueryPerMin().Value != 9 {
					t.Fatalf("global input state = revision %d, rate %d", global.Revision(), global.PrivateQueryPerMin().Value)
				}
			}
			assertSavedRenderWarning(t, caller.lastEditText)
		})
	}
}

func TestPanelCommittedSharedChatRenderFailuresReportSaved(t *testing.T) {
	tests := []struct {
		name     string
		kind     inputKind
		chatID   int64
		username string
	}{
		{name: "required channel", kind: inputRequiredChannel, chatID: -1009000000701, username: "required"},
		{name: "trusted group list", kind: inputTrustedGroup, chatID: -1009000000702},
		{name: "channel whitelist", kind: inputChannelWhitelist, chatID: -1009000000703},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			panel, settings, caller, bot := newSettingsPanelTest(t, "")
			session := addPanelSession(t, panel, settings, panelTestGroupA, "in")
			session.pending = &pendingInput{
				kind: test.kind, parent: "li", promptMessageID: 72, requestID: 7, expectedRevision: session.revision,
			}
			before := session.revision
			caller.chatUsername = test.username
			caller.editErr = errors.New("edit failed after shared-chat commit")

			submitSharedChat(t, panel, bot, session, test.chatID)

			group, _ := settings.Group(panelTestGroupA)
			if group.Revision() != before+1 {
				t.Fatalf("shared-chat revision = %d, want %d", group.Revision(), before+1)
			}
			switch test.kind {
			case inputRequiredChannel:
				if group.RequiredChannelID().Value != test.chatID {
					t.Fatalf("required channel = %d, want %d", group.RequiredChannelID().Value, test.chatID)
				}
			case inputTrustedGroup:
				if values := group.TrustedMemberGroupIDs().Value; len(values) != 1 || values[0] != test.chatID {
					t.Fatalf("trusted groups = %v, want [%d]", values, test.chatID)
				}
			case inputChannelWhitelist:
				if values := group.ChannelWhitelist().Value; len(values) != 1 || values[0] != test.chatID {
					t.Fatalf("channel whitelist = %v, want [%d]", values, test.chatID)
				}
			}
			assertSavedRenderWarning(t, caller.lastEditText)
		})
	}
}
