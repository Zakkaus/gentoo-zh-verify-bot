package moderate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

var (
	_ Telegram     = (*fakeModBot)(nil)
	_ MemberLookup = (*fakeModBot)(nil)
)

// fakeModBot is the package-local Telegram transport for moderation tests.
type fakeModBot struct {
	member            telego.ChatMember
	memberByID        map[int64]telego.ChatMember
	memberByChat      map[int64]telego.ChatMember
	memberErr         error
	memberErrByChat   map[int64]error
	memberRequests    []telego.GetChatMemberParams
	chats             map[int64]*telego.ChatFullInfo
	chatErrByID       map[int64]error
	chatRequests      []telego.GetChatParams
	sendErrByChat     map[int64]error
	banErr            error
	unbanErr          error
	muteErr           error
	unmuteErr         error
	senderBanErr      error
	senderUnbanErr    map[int64]error
	bans              int
	unbans            int
	mutes             int
	unmutes           int
	deletes           int
	sends             int
	senderBans        int
	senderUnbans      []telego.UnbanChatSenderChatParams
	lastMuteSeconds   int
	lastSendChat      int64
	lastSendText      string
	lastBanRevoke     bool
	lastBanSeconds    int
	lastBannedUserID  int64
	lastDeletedChatID int64
	deletedMessageIDs []int
	notifications     []fakeModNotification
	failAlerts        []fakeModFailAlert
}

type fakeModNotification struct {
	chatID int64
	text   string
}

type fakeModFailAlert struct {
	adminLogChatID int64
	groupID        int64
	text           string
}

func newFakeMod() *fakeModBot { return &fakeModBot{} }

func (b *fakeModBot) GetChatMember(_ context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error) {
	b.memberRequests = append(b.memberRequests, *params)
	if err := b.memberErrByChat[params.ChatID.ID]; err != nil {
		return nil, err
	}
	if b.memberErr != nil {
		return nil, b.memberErr
	}
	if member, ok := b.memberByChat[params.ChatID.ID]; ok {
		return member, nil
	}
	if member, ok := b.memberByID[params.UserID]; ok {
		return member, nil
	}
	return b.member, nil
}

func (b *fakeModBot) GetChat(_ context.Context, params *telego.GetChatParams) (*telego.ChatFullInfo, error) {
	b.chatRequests = append(b.chatRequests, *params)
	if err := b.chatErrByID[params.ChatID.ID]; err != nil {
		return nil, err
	}
	if chat := b.chats[params.ChatID.ID]; chat != nil {
		return chat, nil
	}
	return &telego.ChatFullInfo{ID: params.ChatID.ID, Type: telego.ChatTypeSupergroup}, nil
}

func (b *fakeModBot) SendMessage(_ context.Context, params *telego.SendMessageParams) (*telego.Message, error) {
	b.sends++
	b.lastSendChat = params.ChatID.ID
	b.lastSendText = params.Text
	if err := b.sendErrByChat[params.ChatID.ID]; err != nil {
		return nil, err
	}
	return &telego.Message{MessageID: b.sends}, nil
}

func (b *fakeModBot) CachedAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	return b.adminStatus(ctx, chatID, userID)
}

func (b *fakeModBot) FreshAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	return b.adminStatus(ctx, chatID, userID)
}

func (b *fakeModBot) adminStatus(ctx context.Context, chatID, userID int64) (bool, error) {
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

func (b *fakeModBot) Delete(_ context.Context, chatID int64, messageID int) {
	b.deletes++
	b.lastDeletedChatID = chatID
	b.deletedMessageIDs = append(b.deletedMessageIDs, messageID)
}

func (b *fakeModBot) Notify(_ context.Context, chatID int64, text string, _ int) {
	b.sends++
	b.lastSendChat = chatID
	b.lastSendText = text
	b.notifications = append(b.notifications, fakeModNotification{chatID: chatID, text: text})
}

func (b *fakeModBot) Alert(_ context.Context, chatID int64, text string) {
	if chatID != 0 {
		b.sends++
		b.lastSendChat = chatID
		b.lastSendText = text
		b.notifications = append(b.notifications, fakeModNotification{chatID: chatID, text: text})
	}
}

func (b *fakeModBot) FailAlert(_ context.Context, adminLogChatID, groupID int64, text string) {
	b.failAlerts = append(b.failAlerts, fakeModFailAlert{
		adminLogChatID: adminLogChatID,
		groupID:        groupID,
		text:           text,
	})
	if adminLogChatID == 0 {
		adminLogChatID = groupID
	}
	b.sends++
	b.lastSendChat = adminLogChatID
	b.lastSendText = text
	b.notifications = append(b.notifications, fakeModNotification{chatID: adminLogChatID, text: text})
}

func (b *fakeModBot) Ban(_ context.Context, _ int64, userID int64, seconds int, revoke bool) error {
	b.bans++
	b.lastBannedUserID = userID
	b.lastBanSeconds = seconds
	b.lastBanRevoke = revoke
	return b.banErr
}

func (b *fakeModBot) Unban(_ context.Context, _ int64, _ int64, _ bool) error {
	b.unbans++
	return b.unbanErr
}

func (b *fakeModBot) Mute(_ context.Context, _ int64, _ int64, seconds int) error {
	b.mutes++
	b.lastMuteSeconds = seconds
	return b.muteErr
}

func (b *fakeModBot) Unmute(_ context.Context, _ int64, _ int64) error {
	b.unmutes++
	return b.unmuteErr
}

func (b *fakeModBot) BanSenderChat(_ context.Context, _, _ int64) error {
	b.senderBans++
	return b.senderBanErr
}

func (b *fakeModBot) UnbanSenderChat(_ context.Context, chatID, senderChatID int64) error {
	b.senderUnbans = append(b.senderUnbans, telego.UnbanChatSenderChatParams{
		ChatID:       telego.ChatID{ID: chatID},
		SenderChatID: senderChatID,
	})
	return b.senderUnbanErr[chatID]
}

// Call should remain unused because handlers send all moderation operations through Telegram.
func (b *fakeModBot) Call(context.Context, string, *ta.RequestData) (*ta.Response, error) {
	return nil, errors.New("unexpected raw Telegram API call")
}

func testSettings(t *testing.T, cfg *config.Config) *store.Settings {
	t.Helper()
	groupIDs := append([]int64(nil), cfg.GroupIDs...)
	if len(groupIDs) == 0 {
		for _, group := range cfg.Groups {
			groupIDs = append(groupIDs, group.ID)
		}
	}
	if len(groupIDs) == 0 {
		groupIDs = []int64{-100}
	}
	defaultGroup := store.GroupBaseline{
		Enabled:                 store.BaselineValue[bool]{Value: true},
		VerifyMode:              store.BaselineValue[string]{Value: config.ModeKernel},
		NameSpoiler:             store.BaselineValue[bool]{Value: true},
		BanSeconds:              store.BaselineValue[int]{Value: cfg.BanSeconds},
		LookupTTLSeconds:        store.BaselineValue[int]{Value: 180},
		LookupAutoDeleteEnabled: store.BaselineValue[bool]{Value: true},
		TimeoutSeconds:          store.BaselineValue[int]{Value: 240},
		VerifyMaxFails:          store.BaselineValue[int]{Value: 3},
		VerifyRetrySeconds:      store.BaselineValue[int]{Value: 180},
		AntispamEnabled:         store.BaselineValue[bool]{Value: cfg.BlockChannelSenders},
		Lang:                    store.BaselineValue[string]{Value: cfg.Lang},
		ChannelWhitelist:        store.BaselineValue[[]int64]{Value: append([]int64(nil), cfg.ChannelWhitelist...)},
		TrustedMemberGroupIDs:   store.BaselineValue[[]int64]{Value: append([]int64(nil), cfg.TrustedMemberGroupIDs...)},
		KnownChatIDs:            store.BaselineValue[[]int64]{Value: append([]int64(nil), cfg.KnownChatIDs...)},
		RequiredChannelID:       store.BaselineValue[int64]{Value: cfg.RequiredChannelID},
		ChannelDisplay:          store.BaselineValue[string]{Value: cfg.ChannelDisplay},
		ChannelInviteURL:        store.BaselineValue[string]{Value: cfg.ChannelInviteURL},
		FallbackBuiltin:         store.BaselineValue[bool]{Value: true},
	}
	baseline := store.SettingsBaseline{
		DefaultGroup:   defaultGroup,
		ControlGroupID: cfg.ControlGroupID,
		Global: store.GlobalBaseline{
			PrivateQueryPerMin: store.BaselineValue[int]{Value: 1},
		},
	}
	for _, groupID := range groupIDs {
		group := defaultGroup
		group.ID = groupID
		baseline.Groups = append(baseline.Groups, group)
	}
	settings, err := store.NewSettings("", baseline)
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func newTestService(t *testing.T, cfg *config.Config, telegram *fakeModBot, stateDirectory string) *Service {
	t.Helper()
	return New(testSettings(t, cfg), telegram, cfg, stateDirectory)
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

func moderationCommand(groupID int64, text string) *telego.Message {
	return &telego.Message{
		MessageID: 11,
		Chat:      telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 7, FirstName: "Admin"},
		Text:      text,
		ReplyToMessage: &telego.Message{
			MessageID: 10,
			From:      &telego.User{ID: 8, FirstName: "Member"},
		},
	}
}

func assertModerationCommandCleanup(t *testing.T, telegram *fakeModBot) {
	t.Helper()
	if len(telegram.deletedMessageIDs) != 1 || telegram.deletedMessageIDs[0] != 11 {
		t.Fatalf("deleted message IDs = %v, want only command message 11", telegram.deletedMessageIDs)
	}
}

func assertModerationNotifications(t *testing.T, telegram *fakeModBot, want ...fakeModNotification) {
	t.Helper()
	if len(telegram.notifications) != len(want) {
		t.Fatalf("notifications = %#v, want %#v", telegram.notifications, want)
	}
	for index, wantNotification := range want {
		if got := telegram.notifications[index]; got != wantNotification {
			t.Errorf("notification %d = %#v, want %#v", index, got, wantNotification)
		}
	}
}

func assertFailAlert(t *testing.T, telegram *fakeModBot, adminLogChatID, groupID int64, text string) {
	t.Helper()
	if len(telegram.failAlerts) != 1 {
		t.Fatalf("failure alerts = %#v, want one", telegram.failAlerts)
	}
	if got := telegram.failAlerts[0]; got != (fakeModFailAlert{
		adminLogChatID: adminLogChatID,
		groupID:        groupID,
		text:           text,
	}) {
		t.Errorf("failure alert = %#v, want %#v", got, fakeModFailAlert{
			adminLogChatID: adminLogChatID,
			groupID:        groupID,
			text:           text,
		})
	}
}
func TestGroupSetupReportPermissionsAndChannelReadability(t *testing.T) {
	const (
		groupID   = int64(-100)
		channelID = int64(-200)
		selfID    = int64(900)
	)
	completeAdmin := func() telego.ChatMember {
		return &telego.ChatMemberAdministrator{
			Status:             telego.MemberStatusAdministrator,
			User:               telego.User{ID: selfID},
			CanInviteUsers:     true,
			CanRestrictMembers: true,
			CanDeleteMessages:  true,
		}
	}
	tests := []struct {
		name            string
		groupMember     telego.ChatMember
		channelMember   telego.ChatMember
		channelErr      error
		requiredChannel bool
		ready           bool
		wantText        string
		wantLookups     int
	}{
		{name: "group owner", groupMember: &telego.ChatMemberOwner{Status: telego.MemberStatusCreator}, ready: true, wantLookups: 1},
		{name: "complete administrator", groupMember: completeAdmin(), ready: true, wantLookups: 1},
		{
			name: "missing invite right",
			groupMember: &telego.ChatMemberAdministrator{
				Status: telego.MemberStatusAdministrator, CanRestrictMembers: true, CanDeleteMessages: true,
			},
			wantText: i18n.Messages.Moderate.Setup.ApproveJoinRequests.For(i18n.LangEN), wantLookups: 1,
		},
		{
			name: "missing restrict right",
			groupMember: &telego.ChatMemberAdministrator{
				Status: telego.MemberStatusAdministrator, CanInviteUsers: true, CanDeleteMessages: true,
			},
			wantText: i18n.Messages.Moderate.Setup.BanUsers.For(i18n.LangEN), wantLookups: 1,
		},
		{
			name: "missing delete right",
			groupMember: &telego.ChatMemberAdministrator{
				Status: telego.MemberStatusAdministrator, CanInviteUsers: true, CanRestrictMembers: true,
			},
			wantText: i18n.Messages.Moderate.Setup.DeleteMessages.For(i18n.LangEN), wantLookups: 1,
		},
		{
			name: "plain group member", groupMember: &telego.ChatMemberMember{Status: telego.MemberStatusMember},
			wantText: i18n.Messages.Moderate.Setup.GroupAdmin.For(i18n.LangEN), wantLookups: 1,
		},
		{
			name: "plain channel member", groupMember: completeAdmin(), requiredChannel: true,
			channelMember: &telego.ChatMemberMember{Status: telego.MemberStatusMember},
			wantText:      i18n.Messages.Moderate.Setup.ChannelAdmin.Render(i18n.LangEN, "Required Channel", channelID), wantLookups: 2,
		},
		{
			name: "channel administrator", groupMember: completeAdmin(), requiredChannel: true,
			channelMember: &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator},
			ready:         true, wantLookups: 2,
		},
		{
			name: "channel owner", groupMember: completeAdmin(), requiredChannel: true,
			channelMember: &telego.ChatMemberOwner{Status: telego.MemberStatusCreator},
			ready:         true, wantLookups: 2,
		},
		{
			name: "channel lookup failure", groupMember: completeAdmin(), requiredChannel: true,
			channelErr: errors.New("forbidden"), wantText: i18n.Messages.Moderate.Setup.ChannelAdmin.Render(i18n.LangEN, "Required Channel", channelID), wantLookups: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{
				Groups:   []config.GroupConfig{{ID: groupID}},
				GroupIDs: []int64{groupID},
				Lang:     "en",
			}
			if test.requiredChannel {
				cfg.RequiredChannelID = channelID
			}
			telegram := newFakeMod()
			telegram.chats = map[int64]*telego.ChatFullInfo{
				groupID:   {ID: groupID, Type: telego.ChatTypeSupergroup, Title: "Test Group"},
				channelID: {ID: channelID, Type: telego.ChatTypeChannel, Title: "Required Channel"},
			}
			telegram.memberByChat = map[int64]telego.ChatMember{
				groupID:   test.groupMember,
				channelID: test.channelMember,
			}
			telegram.memberErrByChat = map[int64]error{channelID: test.channelErr}
			service := newTestService(t, cfg, telegram, "")
			report := service.CheckGroupSetup(context.Background(), telegram, selfID, groupID)
			if report.Ready != test.ready {
				t.Fatalf("ready = %t, want %t: %s", report.Ready, test.ready, report.Text)
			}
			if test.wantText != "" && !strings.Contains(report.Text, test.wantText) {
				t.Fatalf("report %q does not contain %q", report.Text, test.wantText)
			}
			if !test.ready {
				restart := i18n.Messages.Moderate.Setup.Restart.For(i18n.LangEN)
				if !strings.Contains(report.Text, restart) {
					t.Fatalf("incomplete setup report %q does not contain catalogue recovery %q", report.Text, restart)
				}
			}
			if len(telegram.memberRequests) != test.wantLookups {
				t.Fatalf("member lookups = %d, want %d: %+v", len(telegram.memberRequests), test.wantLookups, telegram.memberRequests)
			}
			service.LogGroupSetup(context.Background(), telegram, selfID, groupID)
			wantSends := 1
			if test.ready {
				wantSends = 0
			}
			if telegram.sends != wantSends {
				t.Fatalf("setup report sends = %d, want %d", telegram.sends, wantSends)
			}
			if !test.ready && telegram.lastSendText != report.Text {
				t.Fatalf("delivered report = %q, want %q", telegram.lastSendText, report.Text)
			}
		})
	}
}

func TestIsGroupAdminFailsClosed(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		member telego.ChatMember
		err    error
		want   bool
	}{
		{name: "administrator", member: &telego.ChatMemberAdministrator{}, want: true},
		{name: "ordinary member", member: &telego.ChatMemberMember{}},
		{name: "lookup error", err: errors.New("network")},
	} {
		t.Run(test.name, func(t *testing.T) {
			telegram := newFakeMod()
			telegram.member = test.member
			telegram.memberErr = test.err
			service := newTestService(t, &config.Config{NotifyTTLSeconds: -1}, telegram, "")
			if got := service.isGroupAdmin(ctx, -100, 1); got != test.want {
				t.Errorf("isGroupAdmin = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWarnPrecheckGate(t *testing.T) {
	ctx := context.Background()
	const groupID = int64(-100)
	callerID, targetID := int64(7), int64(8)
	message := func() *telego.Message {
		return &telego.Message{
			Chat:           telego.Chat{ID: groupID},
			From:           &telego.User{ID: callerID},
			ReplyToMessage: &telego.Message{From: &telego.User{ID: targetID}},
		}
	}

	denied := newFakeMod()
	denied.memberByID = map[int64]telego.ChatMember{callerID: &telego.ChatMemberMember{}}
	deniedService := newTestService(t, &config.Config{}, denied, "")
	if got := deniedService.warnPrecheck(ctx, message(), "/warn", true, i18n.LangZH); got != nil {
		t.Error("a non-admin caller must be denied")
	}
	if denied.bans != 0 || denied.mutes != 0 || denied.unbans != 0 {
		t.Errorf("deny path issued moderation actions: bans=%d mutes=%d unbans=%d", denied.bans, denied.mutes, denied.unbans)
	}

	allowed := newFakeMod()
	allowed.memberByID = map[int64]telego.ChatMember{callerID: &telego.ChatMemberAdministrator{}, targetID: &telego.ChatMemberMember{}}
	allowedService := newTestService(t, &config.Config{}, allowed, "")
	if got := allowedService.warnPrecheck(ctx, message(), "/warn", true, i18n.LangZH); got == nil || got.ID != targetID {
		t.Errorf("admin caller and non-admin target resolved to %v", got)
	}

	skipped := newFakeMod()
	skipped.memberByID = map[int64]telego.ChatMember{callerID: &telego.ChatMemberAdministrator{}, targetID: &telego.ChatMemberAdministrator{}}
	skippedService := newTestService(t, &config.Config{}, skipped, "")
	if got := skippedService.warnPrecheck(ctx, message(), "/warn", true, i18n.LangZH); got != nil {
		t.Error("an admin target must be skipped")
	}
	if skipped.bans != 0 || skipped.mutes != 0 {
		t.Error("skipping an admin target issued an action")
	}
}

func TestWarnKick(t *testing.T) {
	ctx := context.Background()

	clean := newFakeMod()
	service := newTestService(t, &config.Config{}, clean, "")
	if rejoinable, err := service.warnKick(ctx, -100, 5); !rejoinable || err != nil {
		t.Fatalf("clean kick = rejoinable %v, err %v", rejoinable, err)
	}
	if clean.bans != 1 || clean.unbans != 1 {
		t.Errorf("kick calls = bans %d, unbans %d", clean.bans, clean.unbans)
	}

	banFailed := newFakeMod()
	banFailed.banErr = errors.New("no rights")
	service = newTestService(t, &config.Config{}, banFailed, "")
	if rejoinable, err := service.warnKick(ctx, -100, 5); rejoinable || err == nil {
		t.Fatalf("failed ban = rejoinable %v, err %v", rejoinable, err)
	}
	if banFailed.unbans != 0 {
		t.Error("failed ban attempted an unban")
	}

	stuck := newFakeMod()
	stuck.unbanErr = errors.New("unban failed")
	service = newTestService(t, &config.Config{}, stuck, "")
	if rejoinable, err := service.warnKick(ctx, -100, 5); rejoinable || err != nil {
		t.Fatalf("stuck ban = rejoinable %v, err %v", rejoinable, err)
	}
}

func TestWarnHandlerKicksAtLimitAndClearsCounter(t *testing.T) {
	const groupID = int64(-100)
	telegram := newFakeMod()
	telegram.memberByID = map[int64]telego.ChatMember{
		7: &telego.ChatMemberAdministrator{},
		8: &telego.ChatMemberMember{},
	}
	service := newTestService(t, &config.Config{
		GroupIDs:         []int64{groupID},
		Groups:           []config.GroupConfig{{ID: groupID}},
		WarnLimit:        1,
		NotifyTTLSeconds: -1,
	}, telegram, "")
	message := &telego.Message{
		MessageID: 11,
		Chat:      telego.Chat{ID: groupID, Type: "supergroup"},
		From:      &telego.User{ID: 7, FirstName: "Admin"},
		Text:      "/warn",
		ReplyToMessage: &telego.Message{
			MessageID: 10,
			From:      &telego.User{ID: 8, FirstName: "Member"},
		},
	}
	runFakeHandler(t, newAPITestBot(t, telegram), service.OnWarn, telego.Update{Message: message})
	if telegram.bans != 1 || telegram.unbans != 1 {
		t.Fatalf("warn kick calls = bans %d, unbans %d", telegram.bans, telegram.unbans)
	}
	if _, ok := service.warnings.counters[warningKey{groupID: groupID, userID: 8}]; ok {
		t.Fatal("successful warn kick retained the warning counter")
	}
}

func TestBanAndPurgeHandlers(t *testing.T) {
	const groupID = int64(-100)
	for _, test := range []struct {
		name   string
		text   string
		revoke bool
	}{
		{name: "ban", text: "/ban"},
		{name: "purge", text: "/sb", revoke: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			telegram := newFakeMod()
			telegram.memberByID = map[int64]telego.ChatMember{
				7: &telego.ChatMemberAdministrator{},
				8: &telego.ChatMemberMember{},
			}
			service := newTestService(t, &config.Config{
				GroupIDs:         []int64{groupID},
				Groups:           []config.GroupConfig{{ID: groupID}},
				BanSeconds:       7200,
				NotifyTTLSeconds: -1,
			}, telegram, "")
			handler := service.OnBan
			if test.revoke {
				handler = service.OnPurge
			}
			message := &telego.Message{
				MessageID: 11,
				Chat:      telego.Chat{ID: groupID, Type: "supergroup"},
				From:      &telego.User{ID: 7, FirstName: "Admin"},
				Text:      test.text,
				ReplyToMessage: &telego.Message{
					MessageID: 10,
					From:      &telego.User{ID: 8, FirstName: "Member"},
				},
			}
			runFakeHandler(t, newAPITestBot(t, telegram), handler, telego.Update{Message: message})
			if telegram.bans != 1 || telegram.lastBanRevoke != test.revoke || telegram.lastBanSeconds != 7200 {
				t.Fatalf("ban action = calls %d revoke %v seconds %d", telegram.bans, telegram.lastBanRevoke, telegram.lastBanSeconds)
			}
			if telegram.deletes != 2 {
				t.Fatalf("successful command deleted %d messages, want command and reply", telegram.deletes)
			}
		})
	}
}

func TestMuteAndUnmuteHandlers(t *testing.T) {
	const groupID = int64(-100)
	telegram := newFakeMod()
	telegram.memberByID = map[int64]telego.ChatMember{
		7: &telego.ChatMemberAdministrator{},
		8: &telego.ChatMemberMember{},
	}
	service := newTestService(t, &config.Config{
		GroupIDs:         []int64{groupID},
		Groups:           []config.GroupConfig{{ID: groupID}},
		MuteSeconds:      3600,
		NotifyTTLSeconds: -1,
	}, telegram, "")
	message := &telego.Message{
		MessageID: 11,
		Chat:      telego.Chat{ID: groupID, Type: "supergroup"},
		From:      &telego.User{ID: 7, FirstName: "Admin"},
		Text:      "/mute 30m",
		ReplyToMessage: &telego.Message{
			MessageID: 10,
			From:      &telego.User{ID: 8, FirstName: "Member"},
		},
	}
	bot := newAPITestBot(t, telegram)
	runFakeHandler(t, bot, service.OnMute, telego.Update{Message: message})
	if telegram.mutes != 1 || telegram.lastMuteSeconds != 1800 {
		t.Fatalf("mute calls = %d, seconds %d", telegram.mutes, telegram.lastMuteSeconds)
	}

	message.Text = "/unmute"
	runFakeHandler(t, bot, service.OnUnmute, telego.Update{Message: message})
	if telegram.unmutes != 1 {
		t.Fatalf("unmute calls = %d, want 1", telegram.unmutes)
	}
}

func TestBanRejectionRetainsEvidenceAndAlertsConfiguredLog(t *testing.T) {
	const (
		groupID    = int64(-100)
		adminLogID = int64(-200)
	)
	telegram := newFakeMod()
	telegram.banErr = errors.New("telegram rejected ban")
	telegram.memberByID = map[int64]telego.ChatMember{
		7: &telego.ChatMemberAdministrator{},
		8: &telego.ChatMemberMember{},
	}
	service := newTestService(t, &config.Config{
		GroupIDs:         []int64{groupID},
		Groups:           []config.GroupConfig{{ID: groupID}},
		AdminLogChatID:   adminLogID,
		Lang:             "en",
		NotifyTTLSeconds: -1,
	}, telegram, "")
	message := moderationCommand(groupID, "/ban")
	runFakeHandler(t, newAPITestBot(t, telegram), service.OnBan, telego.Update{Message: message})

	if telegram.bans != 1 || telegram.lastBannedUserID != message.ReplyToMessage.From.ID {
		t.Fatalf("ban calls = %d for user %d, want one rejection for target %d", telegram.bans, telegram.lastBannedUserID, message.ReplyToMessage.From.ID)
	}
	assertModerationCommandCleanup(t, telegram)
	l := i18n.LangEN
	wantAlert := i18n.Messages.Moderate.Ban.FailureAlert.Render(l, "/ban", groupID, message.ReplyToMessage.From.ID, displayName(message.ReplyToMessage.From), displayName(message.From))
	assertModerationNotifications(t, telegram,
		fakeModNotification{chatID: groupID, text: i18n.Messages.Moderate.Ban.Failed.For(l)},
		fakeModNotification{chatID: adminLogID, text: wantAlert},
	)
	assertFailAlert(t, telegram, adminLogID, groupID, wantAlert)
}

func TestBanRejectionFallsBackToGroupWithoutAdminLog(t *testing.T) {
	const groupID = int64(-100)
	telegram := newFakeMod()
	telegram.banErr = errors.New("telegram rejected ban")
	telegram.memberByID = map[int64]telego.ChatMember{
		7: &telego.ChatMemberAdministrator{},
		8: &telego.ChatMemberMember{},
	}
	service := newTestService(t, &config.Config{
		GroupIDs:         []int64{groupID},
		Groups:           []config.GroupConfig{{ID: groupID}},
		Lang:             "en",
		NotifyTTLSeconds: -1,
	}, telegram, "")
	message := moderationCommand(groupID, "/ban")
	runFakeHandler(t, newAPITestBot(t, telegram), service.OnBan, telego.Update{Message: message})

	assertModerationCommandCleanup(t, telegram)
	l := i18n.LangEN
	wantAlert := i18n.Messages.Moderate.Ban.FailureAlert.Render(l, "/ban", groupID, message.ReplyToMessage.From.ID, displayName(message.ReplyToMessage.From), displayName(message.From))
	assertModerationNotifications(t, telegram,
		fakeModNotification{chatID: groupID, text: i18n.Messages.Moderate.Ban.Failed.For(l)},
		fakeModNotification{chatID: groupID, text: wantAlert},
	)
	assertFailAlert(t, telegram, 0, groupID, wantAlert)
}

func TestMuteRejectionRetainsEvidenceAndAlertsConfiguredLog(t *testing.T) {
	const (
		groupID    = int64(-100)
		adminLogID = int64(-200)
	)
	telegram := newFakeMod()
	telegram.muteErr = errors.New("telegram rejected mute")
	telegram.memberByID = map[int64]telego.ChatMember{
		7: &telego.ChatMemberAdministrator{},
		8: &telego.ChatMemberMember{},
	}
	service := newTestService(t, &config.Config{
		GroupIDs:         []int64{groupID},
		Groups:           []config.GroupConfig{{ID: groupID}},
		AdminLogChatID:   adminLogID,
		Lang:             "en",
		MuteSeconds:      3600,
		NotifyTTLSeconds: -1,
	}, telegram, "")
	message := moderationCommand(groupID, "/mute")
	runFakeHandler(t, newAPITestBot(t, telegram), service.OnMute, telego.Update{Message: message})

	if telegram.mutes != 1 || telegram.lastMuteSeconds != 3600 {
		t.Fatalf("mute calls = %d for %d seconds, want one rejected 3600-second mute", telegram.mutes, telegram.lastMuteSeconds)
	}
	assertModerationCommandCleanup(t, telegram)
	l := i18n.LangEN
	wantFailure := i18n.Messages.Moderate.Mute.Failed.For(l)
	wantAlert := wantFailure + "\n" + i18n.Messages.Moderate.Mute.Alert.Render(
		l, banDurationStatus(l, 3600), groupID, message.ReplyToMessage.From.ID,
		displayName(message.ReplyToMessage.From), displayName(message.From))
	assertModerationNotifications(t, telegram,
		fakeModNotification{chatID: groupID, text: wantFailure},
		fakeModNotification{chatID: adminLogID, text: wantAlert},
	)
	assertFailAlert(t, telegram, adminLogID, groupID, wantAlert)
}

func TestMuteRejectionFallsBackToGroupWithoutAdminLog(t *testing.T) {
	const groupID = int64(-100)
	telegram := newFakeMod()
	telegram.muteErr = errors.New("telegram rejected mute")
	telegram.memberByID = map[int64]telego.ChatMember{
		7: &telego.ChatMemberAdministrator{},
		8: &telego.ChatMemberMember{},
	}
	service := newTestService(t, &config.Config{
		GroupIDs:         []int64{groupID},
		Groups:           []config.GroupConfig{{ID: groupID}},
		Lang:             "en",
		MuteSeconds:      3600,
		NotifyTTLSeconds: -1,
	}, telegram, "")
	message := moderationCommand(groupID, "/mute")
	runFakeHandler(t, newAPITestBot(t, telegram), service.OnMute, telego.Update{Message: message})

	assertModerationCommandCleanup(t, telegram)
	l := i18n.LangEN
	wantFailure := i18n.Messages.Moderate.Mute.Failed.For(l)
	wantAlert := wantFailure + "\n" + i18n.Messages.Moderate.Mute.Alert.Render(
		l, banDurationStatus(l, 3600), groupID, message.ReplyToMessage.From.ID,
		displayName(message.ReplyToMessage.From), displayName(message.From))
	assertModerationNotifications(t, telegram,
		fakeModNotification{chatID: groupID, text: wantFailure},
		fakeModNotification{chatID: groupID, text: wantAlert},
	)
	assertFailAlert(t, telegram, 0, groupID, wantAlert)
}

func TestWarnLimitRejectionRetainsCountAndAlertsConfiguredLog(t *testing.T) {
	const (
		groupID    = int64(-100)
		adminLogID = int64(-200)
	)
	telegram := newFakeMod()
	telegram.banErr = errors.New("telegram rejected warning-limit kick")
	telegram.memberByID = map[int64]telego.ChatMember{
		7: &telego.ChatMemberAdministrator{},
		8: &telego.ChatMemberMember{},
	}
	service := newTestService(t, &config.Config{
		GroupIDs:         []int64{groupID},
		Groups:           []config.GroupConfig{{ID: groupID}},
		AdminLogChatID:   adminLogID,
		Lang:             "en",
		WarnLimit:        1,
		NotifyTTLSeconds: -1,
	}, telegram, "")
	message := moderationCommand(groupID, "/warn")
	runFakeHandler(t, newAPITestBot(t, telegram), service.OnWarn, telego.Update{Message: message})

	if telegram.bans != 1 || telegram.unbans != 0 {
		t.Fatalf("warning-limit kick calls = bans %d unbans %d, want one rejected ban", telegram.bans, telegram.unbans)
	}
	if got := service.warnings.counters[warningKey{groupID: groupID, userID: message.ReplyToMessage.From.ID}]; got != 1 {
		t.Fatalf("warning count after rejected limit kick = %d, want 1", got)
	}
	assertModerationCommandCleanup(t, telegram)
	l := i18n.LangEN
	wantAlert := i18n.Messages.Moderate.Warning.LimitKickAlert.Render(l, displayName(message.ReplyToMessage.From), 1, displayName(message.From))
	assertModerationNotifications(t, telegram,
		fakeModNotification{chatID: groupID, text: i18n.Messages.Moderate.Warning.LimitKickFailed.For(l)},
		fakeModNotification{chatID: adminLogID, text: wantAlert},
	)
	assertFailAlert(t, telegram, adminLogID, groupID, wantAlert)
}

func TestWarnLimitRejectionFallsBackToGroupWithoutAdminLog(t *testing.T) {
	const groupID = int64(-100)
	telegram := newFakeMod()
	telegram.banErr = errors.New("telegram rejected warning-limit kick")
	telegram.memberByID = map[int64]telego.ChatMember{
		7: &telego.ChatMemberAdministrator{},
		8: &telego.ChatMemberMember{},
	}
	service := newTestService(t, &config.Config{
		GroupIDs:         []int64{groupID},
		Groups:           []config.GroupConfig{{ID: groupID}},
		Lang:             "en",
		WarnLimit:        1,
		NotifyTTLSeconds: -1,
	}, telegram, "")
	message := moderationCommand(groupID, "/warn")
	runFakeHandler(t, newAPITestBot(t, telegram), service.OnWarn, telego.Update{Message: message})

	if got := service.warnings.counters[warningKey{groupID: groupID, userID: message.ReplyToMessage.From.ID}]; got != 1 {
		t.Fatalf("warning count after rejected limit kick = %d, want 1", got)
	}
	assertModerationCommandCleanup(t, telegram)
	l := i18n.LangEN
	wantAlert := i18n.Messages.Moderate.Warning.LimitKickAlert.Render(l, displayName(message.ReplyToMessage.From), 1, displayName(message.From))
	assertModerationNotifications(t, telegram,
		fakeModNotification{chatID: groupID, text: i18n.Messages.Moderate.Warning.LimitKickFailed.For(l)},
		fakeModNotification{chatID: groupID, text: wantAlert},
	)
	assertFailAlert(t, telegram, 0, groupID, wantAlert)
}
