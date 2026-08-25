package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

func newTestService(cfg *config.Config) *Service {
	v := newService(nil, nil, cfg, &i18n.Messages)
	groupIDs := make([]int64, 0, max(len(cfg.Groups), len(cfg.GroupIDs)))
	seen := make(map[int64]bool, cap(groupIDs))
	for _, configured := range cfg.Groups {
		if configured.ID != 0 && !seen[configured.ID] {
			groupIDs = append(groupIDs, configured.ID)
			seen[configured.ID] = true
		}
	}
	for _, groupID := range cfg.GroupIDs {
		if groupID != 0 && !seen[groupID] {
			groupIDs = append(groupIDs, groupID)
			seen[groupID] = true
		}
	}
	if len(groupIDs) == 0 {
		return v
	}
	privateQueryPerMin := cfg.PrivateQueryPerMin
	if privateQueryPerMin <= 0 {
		privateQueryPerMin = 3
	}
	baseline := store.SettingsBaseline{
		DefaultGroup:   v.fallbackGroupSettings(0),
		ControlGroupID: cfg.ControlGroupID,
		Global: store.GlobalBaseline{
			RichMessages:       store.BaselineValue[bool]{Value: cfg.RichMessages, Source: store.SourceDefault},
			PrivateQueryPerMin: store.BaselineValue[int]{Value: privateQueryPerMin, Source: store.SourceDefault},
		},
		Groups: make([]store.GroupBaseline, 0, len(groupIDs)),
	}
	for _, groupID := range groupIDs {
		baseline.Groups = append(baseline.Groups, v.fallbackGroupSettings(groupID))
	}
	settings, err := store.NewSettings("", baseline)
	if err != nil {
		panic(fmt.Sprintf("test settings baseline: %v", err))
	}
	v.settings = settings
	return v
}

func TestJoinerLabel(t *testing.T) {
	const evil = `繁星帮<&>"` // an advert-style name with HTML metacharacters
	on := joinerLabel(42, evil, true)
	if !strings.HasPrefix(on, "<tg-spoiler>") || !strings.HasSuffix(on, "</tg-spoiler>") {
		t.Errorf("spoiler-on should wrap the name in one <tg-spoiler> entity, got %q", on)
	}
	if strings.Contains(on, "<a ") || strings.Contains(on, "tg://user") {
		t.Errorf("spoiler-on must NOT emit a nested mention link (parse-safety), got %q", on)
	}
	if strings.Contains(on, "<&>") || strings.Contains(on, "\"") {
		t.Errorf("spoiler-on must HTML-escape the name, got %q", on)
	}
	off := joinerLabel(42, evil, false)
	if !strings.Contains(off, `href="tg://user?id=42"`) {
		t.Errorf("spoiler-off should render a clickable mention, got %q", off)
	}
	if strings.Contains(off, "<&>") {
		t.Errorf("spoiler-off must HTML-escape the name, got %q", off)
	}
}

func TestNameSpoilerDefaultAndToggle(t *testing.T) {
	const groupID int64 = -100
	v := newTestService(&config.Config{Groups: []config.GroupConfig{{ID: groupID}}, GroupIDs: []int64{groupID}})
	if !v.NameSpoilerOn(groupID) {
		t.Error("name spoiler should default ON (spam names are often adverts)")
	}
	enabled, err := v.ToggleNameSpoiler(groupID)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Error("toggle should turn it OFF and return the new state (false)")
	}
	if v.NameSpoilerOn(groupID) {
		t.Error("name spoiler should now be OFF")
	}
}
func TestJoinResolvesApplicantAndGroupLanguagesSeparately(t *testing.T) {
	const (
		groupID int64 = -100
		userID  int64 = 42
	)
	v := newTestService(&config.Config{
		Groups:         []config.GroupConfig{{ID: groupID}},
		GroupIDs:       []int64{groupID},
		Lang:           "zh",
		VerifyMode:     config.ModeKernel,
		TimeoutSeconds: 240,
	})
	bot := newFakeVerifyBot()
	update := telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: groupID},
		From: telego.User{ID: userID, FirstName: "Applicant", LanguageCode: "zh-Hant"},
	}}

	runFakeHandler(t, newAPITestBot(t, bot), v.OnJoinRequest, update)
	if !strings.Contains(bot.lastSendText, "申请加入") || strings.Contains(bot.lastSendText, "申請加入") {
		t.Fatalf("group challenge did not use the configured zh language: %q", bot.lastSendText)
	}
	p := v.pend[pkey{groupID, userID}]
	if p == nil || p.lang != i18n.LangZHHant {
		t.Fatalf("pending applicant language = %v, want zh-Hant", p)
	}

	v.sendQuizzes(context.Background(), bot, userID)
	if !strings.Contains(bot.lastSendText, "完成入群驗證") {
		t.Fatalf("applicant DM did not retain zh-Hant: %q", bot.lastSendText)
	}
	if p.timer != nil {
		p.timer.Stop()
	}
}

// fakeVerifyBot is a verifyBot stand-in so the approve / decline / ban handler branches can be
// exercised without a real Telegram connection; it records call counts and returns configured
// errors for those network actions.
type fakeVerifyBot struct {
	approveErr      error
	declineErr      error
	banErr          error
	getMeErr        error
	sendErr         error // returned by the first sendFailN SendMessage calls (markup-rejection tests)
	sendFailN       int
	approves        int
	declines        int
	bans            int
	deletes         int
	sends           int
	getMeCalls      int
	lastSendChat    int64
	lastSendText    string
	lastParseMode   string
	member          telego.ChatMember
	memberByID      map[int64]telego.ChatMember
	memberErr       error
	memberRequests  []telego.GetChatMemberParams
	answers         int
	callbackAnswers []telego.AnswerCallbackQueryParams
}

func newFakeVerifyBot() *fakeVerifyBot { return &fakeVerifyBot{} }

// GetMe lets the fake stand in for the heartbeat's liveness probe (liveProbe / heartbeatBot).
func (b *fakeVerifyBot) GetMe(context.Context) (*telego.User, error) {
	b.getMeCalls++
	if b.getMeErr != nil {
		return nil, b.getMeErr
	}
	return &telego.User{ID: 1, IsBot: true}, nil
}

func (b *fakeVerifyBot) ApproveChatJoinRequest(context.Context, *telego.ApproveChatJoinRequestParams) error {
	b.approves++
	return b.approveErr
}
func (b *fakeVerifyBot) DeclineChatJoinRequest(context.Context, *telego.DeclineChatJoinRequestParams) error {
	b.declines++
	return b.declineErr
}
func (b *fakeVerifyBot) BanChatMember(context.Context, *telego.BanChatMemberParams) error {
	b.bans++
	return b.banErr
}
func (b *fakeVerifyBot) DeleteMessage(context.Context, *telego.DeleteMessageParams) error {
	b.deletes++
	return nil
}
func (b *fakeVerifyBot) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	b.sends++
	b.lastSendChat = p.ChatID.ID
	b.lastSendText = p.Text
	b.lastParseMode = p.ParseMode
	if b.sendErr != nil && b.sends <= b.sendFailN {
		return nil, b.sendErr
	}
	return &telego.Message{MessageID: 1}, nil
}

func (b *fakeVerifyBot) SendHTMLFallback(ctx context.Context, chatID int64, rich, simpler string) bool {
	send := func(text, parseMode string) error {
		_, err := b.SendMessage(ctx, &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      text,
			ParseMode: parseMode,
		})
		return err
	}
	if err := send(rich, telego.ModeHTML); err == nil {
		return true
	} else {
		message := strings.ToLower(err.Error())
		if !strings.Contains(message, "parse") && !strings.Contains(message, "entit") && !strings.Contains(message, "bad request") {
			return false
		}
	}
	if simpler != "" && simpler != rich {
		if err := send(simpler, telego.ModeHTML); err == nil {
			return true
		}
	}
	return send(simpler, "") == nil
}

func (b *fakeVerifyBot) Delete(ctx context.Context, chatID int64, messageID int) {
	if messageID != 0 {
		_ = b.DeleteMessage(ctx, &telego.DeleteMessageParams{ChatID: telego.ChatID{ID: chatID}, MessageID: messageID})
	}
}

func (b *fakeVerifyBot) Alert(ctx context.Context, adminLogChatID int64, text string) {
	if adminLogChatID != 0 {
		_, _ = b.SendMessage(ctx, &telego.SendMessageParams{ChatID: telego.ChatID{ID: adminLogChatID}, Text: text})
	}
}

func (b *fakeVerifyBot) FailAlert(ctx context.Context, adminLogChatID, groupID int64, text string) {
	if adminLogChatID == 0 {
		adminLogChatID = groupID
	}
	_, _ = b.SendMessage(ctx, &telego.SendMessageParams{ChatID: telego.ChatID{ID: adminLogChatID}, Text: text})
}

func (b *fakeVerifyBot) Ban(ctx context.Context, chatID, userID int64, _ int, revokeMessages bool) error {
	return b.BanChatMember(ctx, &telego.BanChatMemberParams{
		ChatID:         telego.ChatID{ID: chatID},
		UserID:         userID,
		RevokeMessages: revokeMessages,
	})
}

func (b *fakeVerifyBot) GetChatMember(_ context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error) {
	b.memberRequests = append(b.memberRequests, *params)
	if b.memberErr != nil {
		return nil, b.memberErr
	}
	if member, ok := b.memberByID[params.UserID]; ok {
		return member, nil
	}
	return b.member, nil
}

func (b *fakeVerifyBot) AnswerCallbackQuery(_ context.Context, params *telego.AnswerCallbackQueryParams) error {
	b.answers++
	b.callbackAnswers = append(b.callbackAnswers, *params)
	return nil
}

func (b *fakeVerifyBot) CachedAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	return b.adminStatus(ctx, chatID, userID)
}

func (b *fakeVerifyBot) FreshAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	return b.adminStatus(ctx, chatID, userID)
}

func (b *fakeVerifyBot) Notify(ctx context.Context, chatID int64, text string, _ int) {
	_, _ = b.SendMessage(ctx, &telego.SendMessageParams{ChatID: telego.ChatID{ID: chatID}, Text: text})
}

func (b *fakeVerifyBot) adminStatus(ctx context.Context, chatID, userID int64) (bool, error) {
	member, err := b.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: telego.ChatID{ID: chatID}, UserID: userID})
	if err != nil {
		return false, err
	}
	if member == nil {
		return false, nil
	}
	status := member.MemberStatus()
	return status == telego.MemberStatusCreator || status == telego.MemberStatusAdministrator, nil
}

func fakeTelegramResponse(value any, err error) (*ta.Response, error) {
	if err != nil {
		return nil, err
	}
	if value == nil {
		return &ta.Response{Ok: true}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &ta.Response{Ok: true, Result: raw}, nil
}

func fakeSendMessageParams(raw []byte) (*telego.SendMessageParams, error) {
	var wire struct {
		ChatID              int64                   `json:"chat_id"`
		Text                string                  `json:"text"`
		ParseMode           string                  `json:"parse_mode"`
		DisableNotification bool                    `json:"disable_notification"`
		ReplyParameters     *telego.ReplyParameters `json:"reply_parameters"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	return &telego.SendMessageParams{
		ChatID:              telego.ChatID{ID: wire.ChatID},
		Text:                wire.Text,
		ParseMode:           wire.ParseMode,
		DisableNotification: wire.DisableNotification,
		ReplyParameters:     wire.ReplyParameters,
	}, nil
}

// Call adapts fakeVerifyBot to telego's transport hook for concrete handler tests.
func (b *fakeVerifyBot) Call(ctx context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
	method := url[strings.LastIndexByte(url, '/')+1:]
	switch method {
	case "getChatMember":
		var params telego.GetChatMemberParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		member, err := b.GetChatMember(ctx, &params)
		return fakeTelegramResponse(member, err)
	case "answerCallbackQuery":
		var params telego.AnswerCallbackQueryParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.AnswerCallbackQuery(ctx, &params))
	case "approveChatJoinRequest":
		var params telego.ApproveChatJoinRequestParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.ApproveChatJoinRequest(ctx, &params))
	case "declineChatJoinRequest":
		var params telego.DeclineChatJoinRequestParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.DeclineChatJoinRequest(ctx, &params))
	case "banChatMember":
		var params telego.BanChatMemberParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.BanChatMember(ctx, &params))
	case "deleteMessage":
		var params telego.DeleteMessageParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.DeleteMessage(ctx, &params))
	case "sendMessage":
		params, err := fakeSendMessageParams(data.BodyRaw)
		if err != nil {
			return nil, err
		}
		message, err := b.SendMessage(ctx, params)
		return fakeTelegramResponse(message, err)
	default:
		return nil, fmt.Errorf("unexpected Telegram method %q", method)
	}
}

func newAPITestBot(t *testing.T, caller ta.Caller) *telego.Bot {
	t.Helper()
	bot, err := telego.NewBot("1:"+strings.Repeat("a", 35), telego.WithAPICaller(caller), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return bot
}

func runFakeHandler(t *testing.T, bot *telego.Bot, handler th.Handler, update telego.Update) {
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

type blockingTerminalBot struct {
	*fakeVerifyBot
	approveStarted chan struct{}
	declineStarted chan struct{}
	release        chan struct{}
}

func newBlockingTerminalBot() *blockingTerminalBot {
	return &blockingTerminalBot{
		fakeVerifyBot:  &fakeVerifyBot{},
		approveStarted: make(chan struct{}),
		declineStarted: make(chan struct{}),
		release:        make(chan struct{}),
	}
}

func (b *blockingTerminalBot) ApproveChatJoinRequest(context.Context, *telego.ApproveChatJoinRequestParams) error {
	close(b.approveStarted)
	<-b.release
	return nil
}

func (b *blockingTerminalBot) DeclineChatJoinRequest(context.Context, *telego.DeclineChatJoinRequestParams) error {
	close(b.declineStarted)
	<-b.release
	return nil
}

func livePending(msgID int) *pending {
	return &pending{nonce: "n", deadline: time.Now().Add(time.Hour), groupMsgID: msgID}
}

func TestOnAnswer(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	tests := []struct {
		name        string
		from        int64
		data        string
		required    bool
		wantApprove int
		wantDecline int
		wantSend    int
		wantPending bool
		wantAlert   bool
	}{
		{name: "another applicant", from: 6, data: "v:-100:5:current:1", wantPending: true, wantAlert: true},
		{name: "stale nonce", from: uid, data: "v:-100:5:stale:1", wantPending: true, wantAlert: true},
		{name: "wrong option", from: uid, data: "v:-100:5:current:0", wantDecline: 1, wantAlert: true},
		{name: "correct option", from: uid, data: "v:-100:5:current:1", wantApprove: 1, wantSend: 1},
		{name: "correct option before joining channel", from: uid, data: "v:-100:5:current:1", required: true, wantPending: true, wantAlert: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{VerifyMaxFails: 3}
			if tt.required {
				cfg.RequiredChannelID = -400
			}
			v := newTestService(cfg)
			key := pkey{gid: gid, uid: uid}
			v.pend[key] = &pending{nonce: "current", correctIdx: 1, groupMsgID: 42, deadline: time.Now().Add(time.Hour)}
			bot := newFakeVerifyBot()
			if tt.required {
				bot.member = &telego.ChatMemberLeft{Status: telego.MemberStatusLeft}
			}
			update := telego.Update{CallbackQuery: &telego.CallbackQuery{
				ID: "answer", From: telego.User{ID: tt.from}, Data: tt.data,
			}}
			runFakeHandler(t, newAPITestBot(t, bot), v.OnAnswer, update)

			if bot.approves != tt.wantApprove || bot.declines != tt.wantDecline || bot.sends != tt.wantSend {
				t.Errorf("actions = approve %d, decline %d, send %d; want %d, %d, %d",
					bot.approves, bot.declines, bot.sends, tt.wantApprove, tt.wantDecline, tt.wantSend)
			}
			_, pending := v.pend[key]
			if pending != tt.wantPending {
				t.Errorf("pending remains = %v, want %v", pending, tt.wantPending)
			}
			if len(bot.callbackAnswers) != 1 {
				t.Fatalf("callback answers = %d, want 1", len(bot.callbackAnswers))
			}
			if got := bot.callbackAnswers[0].ShowAlert; got != tt.wantAlert {
				t.Errorf("callback show_alert = %v, want %v", got, tt.wantAlert)
			}
		})
	}
}

func TestApproveSuccess(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	v.vfail[key] = &vfailRec{count: 2, last: time.Now()} // had strikes; approve should clear them
	fb := &fakeVerifyBot{}
	if !v.approve(context.Background(), fb, -100, 5) {
		t.Fatal("approve should return true on success")
	}
	if fb.approves != 1 {
		t.Errorf("ApproveChatJoinRequest should be called once, got %d", fb.approves)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("the pending should be consumed after a successful approve")
	}
	if _, ok := v.vfail[key]; ok {
		t.Error("a successful approve should clear the user's verify-fail strikes")
	}
	if fb.bans != 0 {
		t.Error("approve must never ban")
	}
}

func TestApproveFailureReopens(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{-100, 5}
	p := livePending(42)
	v.pend[key] = p
	fb := &fakeVerifyBot{approveErr: errors.New("Forbidden: not enough rights")}
	if v.approve(context.Background(), fb, -100, 5) {
		t.Fatal("approve should return false when ApproveChatJoinRequest fails")
	}
	if cur, ok := v.pend[key]; !ok || cur != p {
		t.Error("a failed approve must keep the pending (retryable), not strand the applicant")
	}
	if p.done {
		t.Error("a failed approve must re-open the pending (done=false) so it can retry / time out")
	}
	if fb.bans != 0 {
		t.Error("a failed approve must never ban the user")
	}
	if p.timer != nil {
		p.timer.Stop() // reopenPending re-armed a (far-future) timer; tidy
	}
}

func TestDeclineBelowThreshold(t *testing.T) {
	v := newTestService(&config.Config{VerifyMaxFails: 3})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	fb := &fakeVerifyBot{}
	handled, banned := v.decline(context.Background(), fb, -100, 5, "n", "wrong answer")
	if !handled || banned {
		t.Fatalf("first failure should decline, not ban: handled=%v banned=%v", handled, banned)
	}
	if fb.declines != 1 || fb.bans != 0 {
		t.Errorf("below threshold: want 1 decline + 0 bans, got declines=%d bans=%d", fb.declines, fb.bans)
	}
	if r := v.vfail[key]; r == nil || r.count != 1 {
		t.Errorf("a strike should be recorded, got %+v", r)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("decline should consume the pending")
	}
}

func TestDeclineAutoBan(t *testing.T) {
	v := newTestService(&config.Config{VerifyMaxFails: 1}) // the first failure trips the auto-ban
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	fb := &fakeVerifyBot{}
	handled, banned := v.decline(context.Background(), fb, -100, 5, "n", "wrong answer")
	if !handled || !banned {
		t.Fatalf("reaching the threshold should auto-ban: handled=%v banned=%v", handled, banned)
	}
	if fb.bans != 1 {
		t.Errorf("BanChatMember should be called once, got %d", fb.bans)
	}
	if _, ok := v.vfail[key]; ok {
		t.Error("strikes should be cleared after a successful auto-ban")
	}
}

func TestBanApplicant(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	fb := &fakeVerifyBot{}
	handled, banned := v.banApplicant(context.Background(), fb, -100, 5)
	if !handled || !banned {
		t.Fatalf("banApplicant should decline + ban: handled=%v banned=%v", handled, banned)
	}
	if fb.declines != 1 || fb.bans != 1 {
		t.Errorf("want 1 decline + 1 ban, got declines=%d bans=%d", fb.declines, fb.bans)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("banApplicant should consume the pending")
	}

	v.pend[key] = livePending(0)
	fbFail := &fakeVerifyBot{banErr: errors.New("not enough rights")}
	if _, banned := v.banApplicant(context.Background(), fbFail, -100, 5); banned {
		t.Error("a failed BanChatMember must report banned=false (honest feedback)")
	}
}

func TestClaimThenExecuteApprove(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)

	p, ok := v.claimPending(-100, 5)
	if !ok {
		t.Fatal("claimPending should claim a live pending")
	}
	if cur, ok := v.pend[key]; !ok || cur != p || !p.done {
		t.Fatal("claimPending must KEEP the entry in the map, marked done (so a failed approve can reopen it)")
	}
	if _, ok := v.claimPending(-100, 5); ok {
		t.Error("an already-claimed pending must not be re-claimable (a timer/second callback can't double-act)")
	}
	fb := &fakeVerifyBot{}
	if !v.executeApprove(context.Background(), fb, -100, 5, p) {
		t.Fatal("executeApprove should succeed")
	}
	if fb.approves != 1 {
		t.Errorf("want 1 ApproveChatJoinRequest, got %d", fb.approves)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("a successful executeApprove should remove the pending")
	}
}

func TestTerminalActionBlocksReapplication(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		started func(*blockingTerminalBot) <-chan struct{}
	}{
		{name: "approval in flight", action: "approve", started: func(b *blockingTerminalBot) <-chan struct{} { return b.approveStarted }},
		{name: "decline in flight", action: "decline", started: func(b *blockingTerminalBot) <-chan struct{} { return b.declineStarted }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const gid, uid = int64(-100), int64(5)
			key := pkey{gid, uid}
			v := newTestService(&config.Config{TimeoutSeconds: 3600, VerifyMaxFails: 3})
			old := &pending{nonce: "old", deadline: time.Now().Add(time.Hour)}
			v.pend[key] = old
			bot := newBlockingTerminalBot()
			result := make(chan bool, 1)
			go func() {
				if tt.action == "approve" {
					p, ok := v.claimPendingNonce(gid, uid, old.nonce)
					result <- ok && v.executeApprove(context.Background(), bot, gid, uid, p)
					return
				}
				handled, _ := v.decline(context.Background(), bot, gid, uid, old.nonce, "wrong answer")
				result <- handled
			}()
			select {
			case <-tt.started(bot):
			case <-time.After(time.Second):
				t.Fatal("terminal Telegram call did not start")
			}

			replacement := &pending{nonce: "replacement"}
			if _, status := v.startPending(bot, gid, uid, replacement); status != pendingBlockedTerminal {
				t.Fatalf("startPending status = %v, want pendingBlockedTerminal", status)
			}
			if v.pend[key] == replacement {
				t.Fatal("re-application replaced a pending while its terminal action was in flight")
			}

			close(bot.release)
			select {
			case ok := <-result:
				if !ok {
					t.Fatal("terminal action did not complete")
				}
			case <-time.After(time.Second):
				t.Fatal("terminal action did not return")
			}

			next := &pending{nonce: "next"}
			if _, status := v.startPending(bot, gid, uid, next); status != pendingStarted {
				t.Fatalf("startPending after terminal completion = %v, want pendingStarted", status)
			}
			next.timer.Stop()
		})
	}
}

func TestConsumeThenExecuteBan(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)

	p, ok := v.consume(-100, 5)
	if !ok {
		t.Fatal("consume should claim a live pending")
	}
	if _, ok := v.pend[key]; ok {
		t.Error("consume must REMOVE the pending (no reopen for a ban)")
	}
	fb := &fakeVerifyBot{}
	if banned := v.executeBan(context.Background(), fb, -100, 5, p); !banned {
		t.Fatal("executeBan should report banned=true on success")
	}
	if fb.declines != 1 || fb.bans != 1 {
		t.Errorf("want 1 decline + 1 ban, got declines=%d bans=%d", fb.declines, fb.bans)
	}
}

func TestFailAlertFallsBackToGroup(t *testing.T) {
	v := newTestService(&config.Config{}) // AdminLogChatID == 0
	fb := &fakeVerifyBot{}
	v.failAlert(context.Background(), fb, -555, "x")
	if fb.lastSendChat != -555 {
		t.Errorf("with no admin-log chat, failAlert should post to the group, got chat %d", fb.lastSendChat)
	}
	v.cfg.AdminLogChatID = -999
	v.failAlert(context.Background(), fb, -555, "x")
	if fb.lastSendChat != -999 {
		t.Errorf("with an admin-log chat set, failAlert should post there, got chat %d", fb.lastSendChat)
	}
}

func TestApproveClaimBlocksTimeoutDecline(t *testing.T) {
	v := &Service{pend: map[pkey]*pending{}}
	key := pkey{gid: -100, uid: 5}
	v.pend[key] = &pending{nonce: "abc", deadline: time.Now().Add(time.Hour)}

	// approve claims the pending (marks it done) before its network call.
	v.mu.Lock()
	v.pend[key].done = true
	v.mu.Unlock()

	// the timeout timer now fires -> decline -> consumeNonce; it MUST bail on the claimed pending.
	if _, ok := v.consumeNonce(-100, 5, "abc"); ok {
		t.Error("a claimed (done) pending must not be consumable by the timeout path — a verified user would otherwise get a strike/ban")
	}
}

func TestStopForShutdownFreezesPending(t *testing.T) {
	v := &Service{pend: map[pkey]*pending{}}
	key := pkey{gid: -100, uid: 42}
	tmr := time.AfterFunc(time.Hour, func() {}) // a live timer that stopForShutdown should stop
	v.pend[key] = &pending{nonce: "n1", deadline: time.Now().Add(time.Hour), timer: tmr}

	v.stopForShutdown()

	if !v.shuttingDown {
		t.Error("stopForShutdown must set the shutting-down flag")
	}
	if _, ok := v.pend[key]; !ok {
		t.Fatal("stopForShutdown must NOT remove pendings — they must persist across the restart")
	}
	// a timeout timer firing now reaches consumeNonce, which must refuse while shutting down.
	if _, ok := v.consumeNonce(-100, 42, "n1"); ok {
		t.Error("consumeNonce must refuse while shutting down, so a firing timeout can't decline/strike/ban")
	}
	if _, ok := v.pend[key]; !ok {
		t.Error("the pending must remain intact after the refused consumeNonce")
	}
}

func TestTrustedBypass(t *testing.T) {
	ctx := context.Background()
	const gid, src, uid = int64(-1003265952923), int64(-1001163306055), int64(5)
	mkV := func() *Service {
		return &Service{loc: time.UTC, vfail: map[pkey]*vfailRec{},
			cfg: &config.Config{Groups: []config.GroupConfig{{ID: gid, TrustedMemberGroupIDs: []int64{src}}}}}
	}

	v := mkV()
	v.vfail[pkey{gid, uid}] = &vfailRec{count: 1, last: time.Now()} // a prior failed-verify strike
	member := newFakeVerifyBot()
	member.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberMember{}}
	if handled, trusted := v.tryTrustedBypass(ctx, member, gid, uid); !handled || !trusted {
		t.Fatalf("a member must be approved: handled=%v trusted=%v", handled, trusted)
	}
	if member.approves != 1 {
		t.Errorf("must approve exactly once, got %d", member.approves)
	}
	if _, still := v.vfail[pkey{gid, uid}]; still {
		t.Error("a successful bypass must clearVerifyFails (clean slate)")
	}

	left := newFakeVerifyBot()
	left.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberLeft{}}
	if handled, trusted := mkV().tryTrustedBypass(ctx, left, gid, uid); handled || trusted || left.approves != 0 {
		t.Errorf("a non-member must be (false,false): handled=%v trusted=%v", handled, trusted)
	}

	errBot := newFakeVerifyBot()
	errBot.memberErr = errors.New("bot not in the trusted group")
	if handled, trusted := mkV().tryTrustedBypass(ctx, errBot, gid, uid); handled || trusted || errBot.approves != 0 {
		t.Errorf("a lookup error must be (false,false) — fail-closed: handled=%v trusted=%v", handled, trusted)
	}

	fail := newFakeVerifyBot()
	fail.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberMember{}}
	fail.approveErr = errors.New("no rights")
	if handled, trusted := mkV().tryTrustedBypass(ctx, fail, gid, uid); handled || !trusted {
		t.Errorf("a confirmed member with a failed approve must be (false,true): handled=%v trusted=%v", handled, trusted)
	}

	plain := &Service{loc: time.UTC, vfail: map[pkey]*vfailRec{}, cfg: &config.Config{Groups: []config.GroupConfig{{ID: gid}}}}
	if handled, trusted := plain.tryTrustedBypass(ctx, newFakeVerifyBot(), gid, uid); handled || trusted {
		t.Errorf("no trusted config -> (false,false): handled=%v trusted=%v", handled, trusted)
	}
}

func TestJoinGate(t *testing.T) {
	ctx := context.Background()
	const gid, src, uid = int64(-1003265952923), int64(-1001163306055), int64(5)
	mkV := func() *Service {
		return &Service{loc: time.UTC, vfail: map[pkey]*vfailRec{},
			cfg: &config.Config{VerifyRetrySeconds: 600, Groups: []config.GroupConfig{{ID: gid, TrustedMemberGroupIDs: []int64{src}}}}}
	}
	cooldown := func(v *Service) { v.vfail[pkey{gid, uid}] = &vfailRec{count: 1, last: time.Now()} }

	// trusted member IN cooldown -> bypassed (handled), approved, NOT declined
	v := mkV()
	cooldown(v)
	bot := newFakeVerifyBot()
	bot.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberMember{}}
	if !v.joinGate(ctx, bot, gid, uid) {
		t.Error("a trusted member in cooldown must be handled (bypassed)")
	}
	if bot.approves != 1 || bot.declines != 0 {
		t.Errorf("a trusted member in cooldown must be APPROVED, not declined: approves=%d declines=%d", bot.approves, bot.declines)
	}

	// trusted member in cooldown whose approve FAILS -> not handled (proceed to quiz), NOT declined
	vf := mkV()
	cooldown(vf)
	failBot := newFakeVerifyBot()
	failBot.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberMember{}}
	failBot.approveErr = errors.New("no rights")
	if vf.joinGate(ctx, failBot, gid, uid) {
		t.Error("a confirmed trusted member whose approve failed must proceed to verification (not be handled by the cooldown)")
	}
	if failBot.declines != 0 {
		t.Errorf("a confirmed trusted member must NOT be cooldown-declined, got %d declines", failBot.declines)
	}

	// NON-member in cooldown -> declined (the ordinary cooldown applies)
	vn := mkV()
	cooldown(vn)
	nonBot := newFakeVerifyBot()
	nonBot.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberLeft{}}
	if !vn.joinGate(ctx, nonBot, gid, uid) {
		t.Error("a non-member in cooldown must be handled (declined)")
	}
	if nonBot.declines != 1 || nonBot.approves != 0 {
		t.Errorf("a non-member in cooldown must be DECLINED: declines=%d approves=%d", nonBot.declines, nonBot.approves)
	}

	// non-member, NO cooldown -> proceed to the challenge (not handled)
	vp := mkV()
	pBot := newFakeVerifyBot()
	pBot.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberLeft{}}
	if vp.joinGate(ctx, pBot, gid, uid) {
		t.Error("a non-member with no cooldown must proceed to the challenge (not handled)")
	}
}

func TestStrikesUser(t *testing.T) {
	tests := []struct {
		reason string
		want   bool
	}{
		{reason: "timeout", want: true},
		{reason: "wrong answer", want: true},
		{reason: "something-else", want: true},
		{reason: "approve-retry"},
		{reason: "restart-lapsed"},
		{reason: "recovered"},
		{reason: "challenge-post-failed"},
	}
	for _, tt := range tests {
		if got := strikesUser(tt.reason); got != tt.want {
			t.Errorf("strikesUser(%q) = %v, want %v", tt.reason, got, tt.want)
		}
	}
}

func TestDeclineNoStrike(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	mkV := func() *Service {
		return &Service{loc: time.UTC, cfg: &config.Config{}, pend: map[pkey]*pending{}, vfail: map[pkey]*vfailRec{}}
	}
	for _, reason := range []string{"approve-retry", "restart-lapsed", "challenge-post-failed"} {
		v := mkV()
		v.pend[pkey{gid, uid}] = &pending{nonce: "n", deadline: time.Now().Add(time.Hour)}
		fb := &fakeVerifyBot{}
		v.decline(context.Background(), fb, gid, uid, "n", reason)
		if _, struck := v.vfail[pkey{gid, uid}]; struck {
			t.Errorf("decline(%q) must NOT record a strike", reason)
		}
		if fb.declines != 1 {
			t.Errorf("decline(%q) should still decline the join request, got %d", reason, fb.declines)
		}
		if _, still := v.pend[pkey{gid, uid}]; still {
			t.Errorf("decline(%q) should still consume the pending", reason)
		}
	}
	// a genuine timeout still strikes
	v := mkV()
	v.pend[pkey{gid, uid}] = &pending{nonce: "n", deadline: time.Now().Add(time.Hour)}
	v.decline(context.Background(), &fakeVerifyBot{}, gid, uid, "n", "timeout")
	if r := v.vfail[pkey{gid, uid}]; r == nil || r.count != 1 {
		t.Errorf("decline(timeout) must record a strike, got %+v", r)
	}
}

func TestReopenPendingRestoresRetryable(t *testing.T) {
	v := &Service{pend: map[pkey]*pending{}}
	key := pkey{gid: -100, uid: 5}
	p := &pending{nonce: "abc", deadline: time.Now().Add(time.Hour), done: true}
	v.pend[key] = p

	v.reopenPending(nil, -100, 5, p) // bot unused: a 1h deadline means the re-armed timer won't fire in-test
	if p.done {
		t.Error("reopenPending should re-open the pending (done=false) for retry")
	}
	if p.timer == nil {
		t.Fatal("reopenPending should re-arm the timeout timer")
	}
	p.timer.Stop() // tidy: don't let it fire after the test

	// a pending already replaced by a newer request must NOT be re-opened.
	v.pend[key] = &pending{nonce: "new", deadline: time.Now().Add(time.Hour)}
	stale := &pending{nonce: "abc", deadline: time.Now().Add(time.Hour), done: true}
	v.reopenPending(nil, -100, 5, stale)
	if !stale.done {
		t.Error("a replaced pending must not be re-opened")
	}
}

func TestDeclineFailureAlertsAdmins(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	v := &Service{
		cfg:   &config.Config{AdminLogChatID: -200},
		loc:   time.UTC,
		pend:  map[pkey]*pending{{gid, uid}: livePending(42)},
		vfail: map[pkey]*vfailRec{},
	}
	fb := &fakeVerifyBot{declineErr: errors.New("Forbidden: missing can_invite_users")}
	handled, _ := v.decline(context.Background(), fb, gid, uid, "n", "wrong answer")
	if !handled || fb.declines != 1 {
		t.Fatalf("decline result = handled %v, calls %d; want true, 1", handled, fb.declines)
	}
	if fb.sends != 1 || fb.lastSendChat != -200 {
		t.Fatalf("admin alert sends/chat = %d/%d, want 1/-200", fb.sends, fb.lastSendChat)
	}
	if !strings.Contains(fb.lastSendText, "missing can_invite_users") {
		t.Errorf("admin alert must name the decline failure, got %q", fb.lastSendText)
	}
}

func TestSendQuizzesMarksPromptedOnlyAfterDelivery(t *testing.T) {
	tests := []struct {
		name      string
		bot       *fakeVerifyBot
		want      bool
		wantSends int
	}{
		{name: "rich delivered", bot: &fakeVerifyBot{}, want: true, wantSends: 1},
		{name: "simpler delivered", bot: &fakeVerifyBot{sendErr: errors.New("Bad Request: can't parse entities"), sendFailN: 1}, want: true, wantSends: 2},
		{name: "all renderings failed", bot: &fakeVerifyBot{sendErr: errors.New("Bad Request: can't parse entities"), sendFailN: 3}, want: false, wantSends: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const gid, uid = int64(-100), int64(5)
			v := newTestService(&config.Config{})
			p := &pending{mode: config.ModeKernel, lang: i18n.LangZH, qText: "kernel", nonce: "n", deadline: time.Now().Add(time.Hour)}
			v.pend[pkey{gid, uid}] = p
			v.sendQuizzes(context.Background(), tt.bot, uid)
			if p.prompted != tt.want {
				t.Errorf("prompted = %v, want %v", p.prompted, tt.want)
			}
			if tt.bot.sends != tt.wantSends {
				t.Errorf("SendMessage calls = %d, want %d", tt.bot.sends, tt.wantSends)
			}
		})
	}
}

func TestPendingCaps(t *testing.T) {
	tests := []struct {
		name       string
		fill       func(*Service)
		gid        int64
		uid        int64
		wantStatus pendingStartStatus
	}{
		{name: "below caps", fill: func(*Service) {}, gid: -100, uid: 1, wantStatus: pendingStarted},
		{name: "per-group cap", fill: func(v *Service) {
			for i := range pendingPerGroupCap {
				v.pend[pkey{-100, int64(i + 1)}] = &pending{}
			}
		}, gid: -100, uid: pendingPerGroupCap + 1, wantStatus: pendingBlockedCapacity},
		{name: "global cap", fill: func(v *Service) {
			for i := range pendingGlobalCap {
				gid := -int64(i/pendingPerGroupCap + 1)
				v.pend[pkey{gid, int64(i + 1)}] = &pending{}
			}
		}, gid: -999, uid: 1, wantStatus: pendingBlockedCapacity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newTestService(&config.Config{TimeoutSeconds: 3600})
			tt.fill(v)
			p := &pending{nonce: "new"}
			_, status := v.startPending(&fakeVerifyBot{}, tt.gid, tt.uid, p)
			if status != tt.wantStatus {
				t.Fatalf("startPending status = %v, want %v", status, tt.wantStatus)
			}
			if status == pendingStarted {
				if p.timer == nil {
					t.Fatal("accepted pending must have an expiry timer")
				}
				p.timer.Stop()
				return
			}
			if p.timer != nil {
				t.Error("rejected pending must not arm an expiry timer")
			}
			if _, exists := v.pend[pkey{tt.gid, tt.uid}]; exists {
				t.Error("rejected pending must not enter the queue")
			}
		})
	}
}

func TestPendingCapAlertThrottled(t *testing.T) {
	tests := []struct {
		name       string
		adminLogID int64
		groupID    int64
		wantChatID int64
	}{
		{name: "configured admin log", adminLogID: -200, groupID: -100, wantChatID: -200},
		{name: "affected group fallback", groupID: -100, wantChatID: -100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newTestService(&config.Config{AdminLogChatID: tt.adminLogID})
			fb := &fakeVerifyBot{}
			v.alertPendingCap(context.Background(), fb, tt.groupID)
			v.alertPendingCap(context.Background(), fb, -300)
			if fb.sends != 1 {
				t.Fatalf("two over-cap joins inside the cooldown sent %d alerts, want 1", fb.sends)
			}
			if fb.lastSendChat != tt.wantChatID {
				t.Errorf("alert chat = %d, want %d", fb.lastSendChat, tt.wantChatID)
			}
			v.mu.Lock()
			v.pendingCapAlertAt = time.Now().Add(-pendingCapAlertCooldown)
			v.mu.Unlock()
			v.alertPendingCap(context.Background(), fb, tt.groupID)
			if fb.sends != 2 {
				t.Errorf("an alert after the cooldown brought sends to %d, want 2", fb.sends)
			}
		})
	}
}
