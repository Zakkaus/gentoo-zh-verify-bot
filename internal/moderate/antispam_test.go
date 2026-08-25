package moderate

import (
	"errors"
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/mymmrac/telego"
)

func TestParseChannelID(t *testing.T) {
	for _, test := range []struct {
		input string
		want  int64
		ok    bool
	}{
		{input: "-1001234567890", want: -1001234567890, ok: true},
		{input: "1234567890", want: -1001234567890, ok: true},
		{input: " 1234567890 ", want: -1001234567890, ok: true},
		{input: "-100123456789", want: -100123456789, ok: true},
		{input: "123456789", want: -100123456789, ok: true},
		{input: ""},
		{input: "abc"},
		{input: "-100abc"},
		{input: "99999999999999999999"},
	} {
		got, ok := parseChannelID(test.input)
		if ok != test.ok || (ok && got != test.want) {
			t.Errorf("parseChannelID(%q) = (%d, %v), want (%d, %v)", test.input, got, ok, test.want, test.ok)
		}
	}
}

func TestControlGroupAllowed(t *testing.T) {
	for _, test := range []struct {
		name        string
		controlID   int64
		chatID      int64
		wantAllowed bool
		wantNotice  string
	}{
		{name: "control group", controlID: -100, chatID: -100, wantAllowed: true},
		{name: "satellite refused", controlID: -100, chatID: -200, wantNotice: "⛔ 该命令只能在控制群（ID -100）中使用。"},
		{name: "unset preserves legacy policy", chatID: -200, wantAllowed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{ControlGroupID: test.controlID}
			allowed, notice := cfg.ControlGroupAllowed(test.chatID)
			if allowed != test.wantAllowed || notice != test.wantNotice {
				t.Errorf("ControlGroupAllowed(%d) = (%v, %q), want (%v, %q)", test.chatID, allowed, notice, test.wantAllowed, test.wantNotice)
			}
		})
	}
}

func TestBCAllowUpdatesOnlyInvokingGroup(t *testing.T) {
	const senderID = int64(-1001234567890)
	groups := []int64{-100, -200, -300}
	for _, test := range []struct {
		name      string
		lang      string
		failUnban bool
	}{
		{name: "unban succeeds"},
		{name: "unban failure is reported", failUnban: true},
		{name: "English notice", lang: "en"},
		{name: "Traditional Chinese failure notice", lang: "zh-Hant", failUnban: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{
				GroupIDs:         groups,
				Groups:           []config.GroupConfig{{ID: -100}, {ID: -200}, {ID: -300}},
				ControlGroupID:   -100,
				NotifyTTLSeconds: -1,
				Lang:             test.lang,
			}
			telegram := newFakeMod()
			telegram.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
			if test.failUnban {
				telegram.senderUnbanErr = map[int64]error{-200: errors.New("no rights")}
			}
			service := newTestService(t, cfg, telegram, "")
			runFakeHandler(t, newAPITestBot(t, telegram), service.OnBC, telego.Update{Message: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: -200, Type: "supergroup"},
				From:      &telego.User{ID: 7},
				Text:      "/bc allow 1234567890",
			}})

			if !service.channelWhitelisted(-200, senderID) {
				t.Fatal("successful /bc allow did not update the invoking group")
			}
			if service.channelWhitelisted(-100, senderID) || service.channelWhitelisted(-300, senderID) {
				t.Fatal("/bc allow changed another group")
			}
			if len(telegram.senderUnbans) != 1 {
				t.Fatalf("sender unbans = %d, want 1", len(telegram.senderUnbans))
			}
			call := telegram.senderUnbans[0]
			if call.ChatID.ID != -200 || call.SenderChatID != senderID {
				t.Errorf("unban call = chat %d sender %d, want chat -200 sender %d", call.ChatID.ID, call.SenderChatID, senderID)
			}
			l := i18n.FromStored(test.lang)
			wantNotice := i18n.Messages.Moderate.Antispam.Allowed.Render(l, senderID)
			if test.failUnban {
				wantNotice = i18n.Messages.Moderate.Antispam.AllowedUnbanFailed.Render(l, senderID)
			}
			if telegram.lastSendText != wantNotice {
				t.Errorf("notice = %q, want %q", telegram.lastSendText, wantNotice)
			}
		})
	}
}

func TestChannelWhitelistBound(t *testing.T) {
	for _, test := range []struct {
		name  string
		extra int
	}{
		{name: "one over cap", extra: 1},
		{name: "multiple over cap", extra: 19},
	} {
		t.Run(test.name, func(t *testing.T) {
			var whitelist []int64
			for index := range channelWhitelistMax + test.extra {
				whitelist = nextChannelWhitelist(whitelist, -1000000-int64(index), true)
			}
			if len(whitelist) != channelWhitelistMax {
				t.Fatalf("whitelist entries = %d, want %d", len(whitelist), channelWhitelistMax)
			}
			for index := range test.extra {
				for _, senderID := range whitelist {
					if senderID == -1000000-int64(index) {
						t.Errorf("oldest whitelist entry %d was not evicted", index)
					}
				}
			}
			if whitelist[len(whitelist)-1] != -1000000-int64(channelWhitelistMax+test.extra-1) {
				t.Error("newest whitelist entry was evicted")
			}
		})
	}
}

func TestFilterChannelSendersUsesTelegramTransport(t *testing.T) {
	const (
		groupID  int64 = -100
		senderID int64 = -1001234567890
	)
	cfg := &config.Config{
		GroupIDs:            []int64{groupID},
		Groups:              []config.GroupConfig{{ID: groupID}},
		BlockChannelSenders: true,
		AdminLogChatID:      -200,
	}
	telegram := newFakeMod()
	service := newTestService(t, cfg, telegram, "")
	runFakeHandler(t, newAPITestBot(t, telegram), service.FilterChannelSenders, telego.Update{Message: &telego.Message{
		MessageID: 3,
		Chat:      telego.Chat{ID: groupID, Type: "supergroup"},
		SenderChat: &telego.Chat{
			ID:    senderID,
			Title: "Spam Channel",
		},
	}})
	if telegram.deletes != 1 || telegram.senderBans != 1 {
		t.Fatalf("filter actions = deletes %d, sender bans %d", telegram.deletes, telegram.senderBans)
	}
	if telegram.lastSendChat != cfg.AdminLogChatID || !strings.Contains(telegram.lastSendText, "Spam Channel") {
		t.Fatalf("operator alert = chat %d text %q", telegram.lastSendChat, telegram.lastSendText)
	}
}
