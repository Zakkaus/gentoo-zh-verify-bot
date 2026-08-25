package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

// fakeModBot extends fakeVerifyBot with the wider modBot surface (admin lookup, mute/unmute, kick)
// so the admin gate and the moderation actions can be exercised without a real Telegram connection.
type fakeModBot struct {
	*fakeVerifyBot
	member          telego.ChatMember           // default GetChatMember result
	memberByID      map[int64]telego.ChatMember // per-user override (so a test can give caller vs target different statuses)
	memberErr       error
	memberRequests  []telego.GetChatMemberParams
	chat            *telego.ChatFullInfo // GetChat result
	chatErr         error
	restrictErr     error
	unbanErr        error
	senderUnbanErr  map[int64]error
	restrictions    []telego.RestrictChatMemberParams
	senderUnbans    []telego.UnbanChatSenderChatParams
	restricts       int
	unbans          int
	answers         int
	callbackAnswers []telego.AnswerCallbackQueryParams
}

func newFakeMod() *fakeModBot { return &fakeModBot{fakeVerifyBot: &fakeVerifyBot{}} }

func (b *fakeModBot) GetChatMember(_ context.Context, p *telego.GetChatMemberParams) (telego.ChatMember, error) {
	b.memberRequests = append(b.memberRequests, *p)
	if b.memberErr != nil {
		return nil, b.memberErr
	}
	if b.memberByID != nil {
		if m, ok := b.memberByID[p.UserID]; ok {
			return m, nil
		}
	}
	return b.member, nil
}
func (b *fakeModBot) GetChat(context.Context, *telego.GetChatParams) (*telego.ChatFullInfo, error) {
	return b.chat, b.chatErr
}
func (b *fakeModBot) RestrictChatMember(_ context.Context, p *telego.RestrictChatMemberParams) error {
	b.restricts++
	b.restrictions = append(b.restrictions, *p)
	return b.restrictErr
}
func (b *fakeModBot) UnbanChatMember(context.Context, *telego.UnbanChatMemberParams) error {
	b.unbans++
	return b.unbanErr
}
func (b *fakeModBot) UnbanChatSenderChat(_ context.Context, p *telego.UnbanChatSenderChatParams) error {
	b.senderUnbans = append(b.senderUnbans, *p)
	return b.senderUnbanErr[p.ChatID.ID]
}
func (b *fakeModBot) AnswerCallbackQuery(_ context.Context, p *telego.AnswerCallbackQueryParams) error {
	b.answers++
	b.callbackAnswers = append(b.callbackAnswers, *p)
	return nil
}

func (b *fakeModBot) CachedAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	return b.adminStatus(ctx, chatID, userID)
}

func (b *fakeModBot) FreshAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	return b.adminStatus(ctx, chatID, userID)
}

func (b *fakeModBot) Notify(ctx context.Context, chatID int64, text string, _ int) {
	_, _ = b.SendMessage(ctx, &telego.SendMessageParams{ChatID: telego.ChatID{ID: chatID}, Text: text})
}

func (b *fakeModBot) Mute(ctx context.Context, chatID, userID int64, _ int) error {
	return b.RestrictChatMember(ctx, &telego.RestrictChatMemberParams{
		ChatID:      telego.ChatID{ID: chatID},
		UserID:      userID,
		Permissions: telego.ChatPermissions{},
	})
}

func (b *fakeModBot) Unmute(ctx context.Context, chatID, userID int64) error {
	return b.RestrictChatMember(ctx, &telego.RestrictChatMemberParams{
		ChatID:      telego.ChatID{ID: chatID},
		UserID:      userID,
		Permissions: telego.ChatPermissions{},
	})
}

func (b *fakeModBot) adminStatus(ctx context.Context, chatID, userID int64) (bool, error) {
	member, err := b.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: telego.ChatID{ID: chatID}, UserID: userID})
	if err != nil {
		return false, err
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

// Call adapts fakeModBot to telego's transport hook. This lets tests drive concrete
// telegohandler.Context handlers while preserving the shared fake's typed call records.
func (b *fakeModBot) Call(ctx context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
	method := url[strings.LastIndexByte(url, '/')+1:]
	switch method {
	case "getChatMember":
		var p telego.GetChatMemberParams
		if err := json.Unmarshal(data.BodyRaw, &p); err != nil {
			return nil, err
		}
		member, err := b.GetChatMember(ctx, &p)
		return fakeTelegramResponse(member, err)
	case "answerCallbackQuery":
		var p telego.AnswerCallbackQueryParams
		if err := json.Unmarshal(data.BodyRaw, &p); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.AnswerCallbackQuery(ctx, &p))
	case "approveChatJoinRequest":
		var p telego.ApproveChatJoinRequestParams
		if err := json.Unmarshal(data.BodyRaw, &p); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.ApproveChatJoinRequest(ctx, &p))
	case "declineChatJoinRequest":
		var p telego.DeclineChatJoinRequestParams
		if err := json.Unmarshal(data.BodyRaw, &p); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.DeclineChatJoinRequest(ctx, &p))
	case "banChatMember":
		var p telego.BanChatMemberParams
		if err := json.Unmarshal(data.BodyRaw, &p); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.BanChatMember(ctx, &p))
	case "unbanChatSenderChat":
		var p telego.UnbanChatSenderChatParams
		if err := json.Unmarshal(data.BodyRaw, &p); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.UnbanChatSenderChat(ctx, &p))
	case "deleteMessage":
		var p telego.DeleteMessageParams
		if err := json.Unmarshal(data.BodyRaw, &p); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.DeleteMessage(ctx, &p))
	case "sendMessage":
		p, err := fakeSendMessageParams(data.BodyRaw)
		if err != nil {
			return nil, err
		}
		msg, err := b.SendMessage(ctx, p)
		return fakeTelegramResponse(msg, err)
	default:
		return nil, fmt.Errorf("unexpected Telegram method %q", method)
	}
}

func newAPITestBot(t *testing.T, caller ta.Caller) *telego.Bot {
	t.Helper()
	bot, err := telego.NewBot("1:"+strings.Repeat("a", 35),
		telego.WithAPICaller(caller), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return bot
}

func runFakeHandler(t *testing.T, bot *telego.Bot, handler th.Handler, update telego.Update) {
	t.Helper()
	updates := make(chan telego.Update, 1)
	bh, err := th.NewBotHandler(bot, updates)
	if err != nil {
		t.Fatal(err)
	}
	handled := make(chan error, 1)
	bh.Handle(func(ctx *th.Context, update telego.Update) error {
		err := handler(ctx, update)
		handled <- err
		return err
	})
	started := make(chan error, 1)
	go func() {
		started <- bh.Start()
	}()
	updates <- update
	close(updates)
	if err := <-handled; err != nil {
		t.Fatalf("handler returned %v", err)
	}
	if err := <-started; err != nil {
		t.Fatalf("bot handler returned %v", err)
	}
}

func modTestV() *Verifier {
	return &Verifier{cfg: &config.Config{NotifyTTLSeconds: -1}}
}

func TestIsGroupAdminFailsClosed(t *testing.T) {
	ctx := context.Background()
	admin := newFakeMod()
	admin.member = &telego.ChatMemberAdministrator{}
	if !modTestV().isGroupAdmin(ctx, admin, -100, 1) {
		t.Error("an administrator must pass the gate")
	}
	member := newFakeMod()
	member.member = &telego.ChatMemberMember{}
	if modTestV().isGroupAdmin(ctx, member, -100, 2) {
		t.Error("a non-admin must NOT pass the gate")
	}
	errBot := newFakeMod()
	errBot.memberErr = errors.New("network")
	if modTestV().isGroupAdmin(ctx, errBot, -100, 3) {
		t.Error("a GetChatMember error must DENY (fail-closed), never allow")
	}
}

func TestWarnPrecheckGate(t *testing.T) {
	ctx := context.Background()
	const gid = int64(-100)
	caller, target := int64(7), int64(8)
	msg := func() *telego.Message {
		return &telego.Message{
			Chat:           telego.Chat{ID: gid},
			From:           &telego.User{ID: caller},
			ReplyToMessage: &telego.Message{From: &telego.User{ID: target}},
		}
	}

	// non-admin caller -> denied, and the deny path issues NO moderation action.
	deny := newFakeMod()
	deny.memberByID = map[int64]telego.ChatMember{caller: &telego.ChatMemberMember{}}
	if got := modTestV().warnPrecheck(ctx, deny, msg(), "/warn", true); got != nil {
		t.Error("a non-admin caller must be denied (nil target)")
	}
	if deny.bans != 0 || deny.restricts != 0 || deny.unbans != 0 {
		t.Errorf("the deny path must issue NO ban/restrict/unban, got bans=%d restricts=%d unbans=%d", deny.bans, deny.restricts, deny.unbans)
	}

	// admin caller, non-admin target -> resolves the target.
	ok := newFakeMod()
	ok.memberByID = map[int64]telego.ChatMember{caller: &telego.ChatMemberAdministrator{}, target: &telego.ChatMemberMember{}}
	if got := modTestV().warnPrecheck(ctx, ok, msg(), "/warn", true); got == nil || got.ID != target {
		t.Errorf("admin caller + non-admin target should resolve the target, got %v", got)
	}

	// admin caller, ADMIN target -> skipped (nil), no action.
	skip := newFakeMod()
	skip.memberByID = map[int64]telego.ChatMember{caller: &telego.ChatMemberAdministrator{}, target: &telego.ChatMemberAdministrator{}}
	if got := modTestV().warnPrecheck(ctx, skip, msg(), "/warn", true); got != nil {
		t.Error("an admin target must be skipped (can't warn/ban an admin)")
	}
	if skip.bans != 0 || skip.restricts != 0 {
		t.Error("skipping an admin target must issue no action")
	}
}

func TestWarnKick(t *testing.T) {
	ctx := context.Background()
	v := &Verifier{cfg: &config.Config{}}

	ok := newFakeMod()
	if rejoinable, err := v.warnKick(ctx, ok, -100, 5); !rejoinable || err != nil {
		t.Fatalf("a clean kick should be rejoinable, no err: rejoinable=%v err=%v", rejoinable, err)
	}
	if ok.bans != 1 || ok.unbans != 1 {
		t.Errorf("warnKick must ban then unban once each, got bans=%d unbans=%d", ok.bans, ok.unbans)
	}

	banFail := newFakeMod()
	banFail.banErr = errors.New("no rights")
	if rejoinable, err := v.warnKick(ctx, banFail, -100, 5); rejoinable || err == nil {
		t.Fatalf("a failed ban must return (false, err): rejoinable=%v err=%v", rejoinable, err)
	}
	if banFail.unbans != 0 {
		t.Error("no unban should be attempted when the ban itself failed")
	}

	stuck := newFakeMod()
	stuck.unbanErr = errors.New("unban failed")
	if rejoinable, err := v.warnKick(ctx, stuck, -100, 5); rejoinable || err != nil {
		t.Fatalf("a stuck ban (unban failed) must be not-rejoinable but no err: rejoinable=%v err=%v", rejoinable, err)
	}
}

func TestWarnsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warns.json")
	cleared := pkey{gid: -200, uid: 9}
	seed := NewVerifier(&config.Config{})
	seed.warnPath = path
	seed.warns[pkey{gid: -100, uid: 7}] = 1
	seed.warns[pkey{gid: -100, uid: 8}] = 3
	seed.warns[pkey{gid: -200, uid: 7}] = 2
	seed.warns[cleared] = 4
	seed.saveWarns()
	delete(seed.warns, cleared)
	seed.saveWarns()

	restored := NewVerifier(&config.Config{})
	restored.warnPath = path
	restored.loadWarns()
	for _, tt := range []struct {
		key  pkey
		want int
	}{
		{key: pkey{gid: -100, uid: 7}, want: 1},
		{key: pkey{gid: -100, uid: 8}, want: 3},
		{key: pkey{gid: -200, uid: 7}, want: 2},
	} {
		if got := restored.warns[tt.key]; got != tt.want {
			t.Errorf("restored warning %v = %d, want %d", tt.key, got, tt.want)
		}
	}
	if got, ok := restored.warns[cleared]; ok {
		t.Errorf("cleared warning %v came back with count %d", cleared, got)
	}
}
