package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
	return &Verifier{cfg: &Config{NotifyTTLSeconds: -1}, adminCache: map[pkey]time.Time{}}
}

func TestMissingModRights(t *testing.T) {
	if m := missingModRights(&telego.ChatMemberAdministrator{CanInviteUsers: true, CanRestrictMembers: true, CanDeleteMessages: true}); len(m) != 0 {
		t.Errorf("a fully-privileged admin should be missing nothing, got %v", m)
	}
	if m := missingModRights(&telego.ChatMemberAdministrator{}); len(m) != 3 {
		t.Errorf("an admin with no rights should be missing all 3, got %v", m)
	}
	if m := missingModRights(&telego.ChatMemberAdministrator{CanInviteUsers: true, CanDeleteMessages: true}); len(m) != 1 {
		t.Errorf("an admin missing only can_restrict_members should report 1, got %v", m)
	}
	if m := missingModRights(&telego.ChatMemberOwner{}); len(m) != 0 {
		t.Errorf("the owner implicitly has all rights — should be missing nothing, got %v", m)
	}
}

func TestAdminStatus(t *testing.T) {
	ctx := context.Background()

	v := modTestV()
	mb := newFakeMod()
	mb.member = &telego.ChatMemberAdministrator{}
	if ok, err := v.adminStatus(ctx, mb, -100, 1); !ok || err != nil {
		t.Fatalf("an administrator should be admin: ok=%v err=%v", ok, err)
	}
	if _, cached := v.adminCache[pkey{-100, 1}]; !cached {
		t.Error("a confirmed admin should be cached")
	}

	v2 := modTestV()
	mb2 := newFakeMod()
	mb2.member = &telego.ChatMemberMember{}
	if ok, err := v2.adminStatus(ctx, mb2, -100, 2); ok || err != nil {
		t.Fatalf("a plain member should not be admin: ok=%v err=%v", ok, err)
	}
	if _, cached := v2.adminCache[pkey{-100, 2}]; cached {
		t.Error("a non-admin must NOT be cached (so a fresh promotion is honoured immediately)")
	}

	v3 := modTestV()
	mb3 := newFakeMod()
	mb3.memberErr = errors.New("network")
	if ok, err := v3.adminStatus(ctx, mb3, -100, 3); ok || err == nil {
		t.Fatalf("a GetChatMember error must surface as (false, err): ok=%v err=%v", ok, err)
	}
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

func TestApplyMuteUnmute(t *testing.T) {
	ctx := context.Background()
	v := &Verifier{cfg: &Config{}}

	mute := newFakeMod()
	if err := v.applyMute(ctx, mute, -100, 5, 3600); err != nil || mute.restricts != 1 {
		t.Fatalf("applyMute should restrict once with no error: err=%v restricts=%d", err, mute.restricts)
	}
	muteErr := newFakeMod()
	muteErr.restrictErr = errors.New("no rights")
	if err := v.applyMute(ctx, muteErr, -100, 5, 3600); err == nil {
		t.Error("applyMute must surface a RestrictChatMember error so the handler reports failure")
	}

	defaults := telego.ChatPermissions{
		CanSendMessages: telego.ToPtr(false),
		CanInviteUsers:  telego.ToPtr(true),
	}
	tests := []struct {
		name         string
		chat         *telego.ChatFullInfo
		chatErr      error
		wantDefaults bool
	}{
		{name: "group defaults", chat: &telego.ChatFullInfo{Permissions: &defaults}, wantDefaults: true},
		{name: "GetChat failure falls back", chatErr: errors.New("temporary failure")},
		{name: "nil chat falls back"},
		{name: "missing defaults falls back", chat: &telego.ChatFullInfo{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bot := newFakeMod()
			bot.chat, bot.chatErr = tt.chat, tt.chatErr
			if err := v.applyUnmute(ctx, bot, -100, 5); err != nil {
				t.Fatalf("applyUnmute() error = %v", err)
			}
			if len(bot.restrictions) != 1 {
				t.Fatalf("RestrictChatMember calls = %d, want 1", len(bot.restrictions))
			}
			got := bot.restrictions[0].Permissions
			if tt.wantDefaults {
				if !reflect.DeepEqual(got, defaults) {
					t.Errorf("permissions = %+v, want group defaults %+v", got, defaults)
				}
				return
			}
			all := []*bool{
				got.CanSendMessages, got.CanSendAudios, got.CanSendDocuments, got.CanSendPhotos,
				got.CanSendVideos, got.CanSendVideoNotes, got.CanSendVoiceNotes, got.CanSendPolls,
				got.CanSendOtherMessages, got.CanAddWebPagePreviews, got.CanReactToMessages,
				got.CanEditTag, got.CanChangeInfo, got.CanInviteUsers, got.CanPinMessages,
				got.CanManageTopics,
			}
			for i, allowed := range all {
				if allowed == nil || !*allowed {
					t.Errorf("fallback permission field %d = %v, want true", i, allowed)
				}
			}
		})
	}
}

func TestWarnKick(t *testing.T) {
	ctx := context.Background()
	v := &Verifier{cfg: &Config{}}

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

func TestDestructiveAdminGateBypassesCache(t *testing.T) {
	if adminCacheTTL != 30*time.Second {
		t.Fatalf("adminCacheTTL = %v, want 30s", adminCacheTTL)
	}
	tests := []struct {
		name      string
		member    telego.ChatMember
		memberErr error
	}{
		{name: "revoked", member: &telego.ChatMemberMember{}},
		{name: "lookup error", memberErr: errors.New("network")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bot := &countingModBot{fakeModBot: newFakeMod()}
			bot.member = tt.member
			bot.memberErr = tt.memberErr
			v := modTestV()
			v.adminCache[pkey{-100, 7}] = time.Now().Add(time.Minute)
			if v.isGroupAdmin(context.Background(), bot, -100, 7) {
				t.Error("a destructive action must deny a revoked admin or a failed fresh lookup")
			}
			if bot.memberCalls != 1 {
				t.Errorf("GetChatMember calls = %d, want 1 despite a positive cache entry", bot.memberCalls)
			}
		})
	}

	cached := &countingModBot{fakeModBot: newFakeMod()}
	v := modTestV()
	v.adminCache[pkey{-100, 7}] = time.Now().Add(time.Minute)
	if ok, err := v.adminStatus(context.Background(), cached, -100, 7); !ok || err != nil {
		t.Fatalf("cheap cached path = (%v, %v), want (true, nil)", ok, err)
	}

	if cached.memberCalls != 0 {
		t.Errorf("cheap cached path made %d GetChatMember calls, want 0", cached.memberCalls)
	}
}

func TestAdminCacheBound(t *testing.T) {
	tests := []struct {
		name  string
		extra int
	}{
		{name: "one live entry over cap", extra: 1},
		{name: "multiple live entries over cap", extra: 17},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := modTestV()
			now := time.Now()
			for i := range adminCacheMax + tt.extra {
				v.adminCache[pkey{-100, int64(i)}] = now.Add(time.Duration(i+1) * time.Second)
			}
			v.pruneAdminCacheLocked(now)
			if len(v.adminCache) != adminCacheMax {
				t.Fatalf("admin cache = %d entries, want cap %d", len(v.adminCache), adminCacheMax)
			}
			for i := range tt.extra {
				if _, ok := v.adminCache[pkey{-100, int64(i)}]; ok {
					t.Errorf("oldest live entry %d was not evicted", i)
				}
			}
		})
	}
}

type cleanupTimerBot struct {
	*fakeModBot
	deleted chan struct{}
}

func (b *cleanupTimerBot) DeleteMessage(context.Context, *telego.DeleteMessageParams) error {
	b.deleted <- struct{}{}
	return nil
}

func TestCleanupTimerGuard(t *testing.T) {
	tests := []struct {
		name       string
		prefill    int
		wantDelete bool
	}{
		{name: "below capacity schedules deletion", prefill: cleanupTimerMax - 1, wantDelete: true},
		{name: "at capacity skips deletion", prefill: cleanupTimerMax},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewVerifier(&Config{NotifyTTLSeconds: 0})
			for range tt.prefill {
				if !v.reserveCleanupTimer() {
					t.Fatalf("could not reserve %d cleanup slots below capacity", tt.prefill)
				}
			}
			bot := &cleanupTimerBot{fakeModBot: newFakeMod(), deleted: make(chan struct{}, 1)}
			v.notify(context.Background(), bot, -100, "test")

			if tt.wantDelete {
				select {
				case <-bot.deleted:
				case <-time.After(time.Second):
					t.Fatal("cleanup deletion was not scheduled below capacity")
				}
				deadline := time.Now().Add(time.Second)
				for v.cleanupTimers.Load() != int32(tt.prefill) && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
			} else {
				select {
				case <-bot.deleted:
					t.Fatal("cleanup deletion was scheduled at capacity")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if got := v.cleanupTimers.Load(); got != int32(tt.prefill) {
				t.Fatalf("outstanding cleanup timers = %d, want %d", got, tt.prefill)
			}
			v.cleanupTimers.Add(-int32(tt.prefill))
			if !v.reserveCleanupTimer() {
				t.Error("a released cleanup slot was not reusable")
			}
			v.cleanupTimers.Add(-1)
		})
	}
}

type countingModBot struct {
	*fakeModBot
	memberCalls int
}

func (b *countingModBot) GetChatMember(c context.Context, p *telego.GetChatMemberParams) (telego.ChatMember, error) {
	b.memberCalls++
	return b.fakeModBot.GetChatMember(c, p)
}

func TestWarnsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warns.json")
	cleared := pkey{gid: -200, uid: 9}
	seed := NewVerifier(&Config{})
	seed.warnPath = path
	seed.warns[pkey{gid: -100, uid: 7}] = 1
	seed.warns[pkey{gid: -100, uid: 8}] = 3
	seed.warns[pkey{gid: -200, uid: 7}] = 2
	seed.warns[cleared] = 4
	seed.saveWarns()
	delete(seed.warns, cleared)
	seed.saveWarns()

	restored := NewVerifier(&Config{})
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
