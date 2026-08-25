package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
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
func (v *panelVerifierStub) SendDMChallenge(context.Context, *telego.Bot, int64) {
	v.challengeCalls++
}
func (v *panelVerifierStub) SetAutoDelete(int64, time.Duration, bool) error { return nil }
func (v *panelVerifierStub) SetEnabled(int64, bool) error                   { return nil }
func (v *panelVerifierStub) SetVerifyMode(int64, string) error              { return nil }
func (v *panelVerifierStub) Stats() (string, int, int)                      { return "", 0, 0 }
func (v *panelVerifierStub) ToggleNameSpoiler(int64) (bool, error)          { return false, nil }
func (v *panelVerifierStub) ToggleRich() (bool, error)                      { return false, nil }

type panelAPICaller struct {
	admin          bool
	memberCalls    int
	lastEditText   string
	lastAnswerText string
	lastSendText   string
	lastURL        string
	messageID      int
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
		return panelAPIResponse(&telego.ChatFullInfo{ID: request.ChatID, Type: "supergroup", Title: fmt.Sprintf("Group %d", request.ChatID)})
	case "getChatMember":
		c.memberCalls++
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
			Text        string `json:"text"`
			ReplyMarkup struct {
				InlineKeyboard [][]struct {
					URL string `json:"url"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		if err := json.Unmarshal(data.BodyRaw, &request); err != nil {
			return nil, err
		}
		c.lastSendText = request.Text
		if len(request.ReplyMarkup.InlineKeyboard) > 0 && len(request.ReplyMarkup.InlineKeyboard[0]) > 0 {
			c.lastURL = request.ReplyMarkup.InlineKeyboard[0][0].URL
		}
		c.messageID++
		return panelAPIResponse(&telego.Message{MessageID: c.messageID})
	case "deleteMessage", "unbanChatSenderChat":
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

func newSettingsPanelTest(t *testing.T, path string) (*Panel, *store.Settings, *panelAPICaller, *telego.Bot) {
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
	caller := &panelAPICaller{admin: true, messageID: 100}
	bot := newAPITestBot(t, caller)
	verifier := &panelVerifierStub{}
	panel := New(settings, tg.New(bot), cfg, &i18n.Messages, verifier, nil, nil, "test", time.Now())
	return panel, settings, caller, bot
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
	if session.messageID == 0 || session.screen != "gl" ||
		!strings.Contains(caller.lastSendText, "Settings groups") ||
		!strings.Contains(caller.lastSendText, fmt.Sprintf("%d", panelTestGroupA)) ||
		!strings.Contains(caller.lastSendText, fmt.Sprintf("%d", panelTestGroupB)) {
		t.Fatalf("group picker did not render: message=%q session=%+v", caller.lastSendText, session)
	}
}

func TestPanelBuildsEverySettingsScreen(t *testing.T) {
	panel, settings, _, bot := newSettingsPanelTest(t, "")
	session := addPanelSession(t, panel, settings, panelTestGroupA, "gh")
	token := "0123456789abcdef"
	for _, screen := range []string{"gl", "gh", "rt", "ls", "li", "vp", "ct", "qb", "qd", "fb", "fd", "ch", "cf", "in"} {
		session.screen = screen
		session.page = 0
		session.listKind = inputKnownChat
		session.quiz = &quizDraft{question: config.Question{Q: "Question", Options: []string{"A", "B"}, Answer: 0}, revision: session.revision}
		session.fallback = &fallbackDraft{question: config.ShortQuestion{Q: "Question", Answers: []string{"Answer"}}, revision: session.revision}
		session.confirm = &confirmation{kind: "channel", revision: session.revision}
		session.pending = &pendingInput{kind: inputTimeout, parent: "vp", expectedRevision: session.revision}
		text, keyboard, err := panel.buildScreen(context.Background(), bot, session, panelTestGroupA, token)
		if err != nil {
			t.Fatalf("build screen %s: %v", screen, err)
		}
		if text == "" || keyboard == nil || len(keyboard.InlineKeyboard) == 0 {
			t.Fatalf("screen %s is incomplete: text=%q keyboard=%+v", screen, text, keyboard)
		}
	}
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
			RequestID: session.pending.requestID,
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
	if caller.memberCalls != 2 {
		t.Fatalf("membership lookups = %d, want one cached prime and one fresh callback check", caller.memberCalls)
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
