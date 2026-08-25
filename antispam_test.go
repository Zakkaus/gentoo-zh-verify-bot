package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/mymmrac/telego"
)

func TestParseChannelID(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"-1001234567890", -1001234567890, true}, // full Bot API form, used as-is
		{"1234567890", -1001234567890, true},     // bare internal id -> prepend -100
		{" 1234567890 ", -1001234567890, true},   // surrounding whitespace tolerated
		{"-100123456789", -100123456789, true},   // shorter full form, as-is
		{"123456789", -100123456789, true},       // shorter bare id
		{"", 0, false},                           // empty
		{"abc", 0, false},                        // non-numeric
		{"-100abc", 0, false},                    // partly numeric
		{"99999999999999999999", 0, false},       // overflows int64
	} {
		got, ok := parseChannelID(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseChannelID(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestControlGroupAllowed(t *testing.T) {
	tests := []struct {
		name        string
		controlID   int64
		chatID      int64
		wantAllowed bool
		wantNotice  string
	}{
		{name: "control group", controlID: -100, chatID: -100, wantAllowed: true},
		{name: "satellite refused", controlID: -100, chatID: -200, wantNotice: "⛔ 该命令只能在控制群（ID -100）中使用。"},
		{name: "unset preserves legacy policy", chatID: -200, wantAllowed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{ControlGroupID: tt.controlID}
			allowed, notice := cfg.ControlGroupAllowed(tt.chatID)
			if allowed != tt.wantAllowed || notice != tt.wantNotice {
				t.Errorf("ControlGroupAllowed(%d) = (%v, %q), want (%v, %q)", tt.chatID, allowed, notice, tt.wantAllowed, tt.wantNotice)
			}
		})
	}
}

func TestBcAllowUnbansEveryGuardedGroup(t *testing.T) {
	const senderID = int64(-1001234567890)
	groups := []int64{-100, -200, -300}
	tests := []struct {
		name        string
		invokedFrom int64
		failGroup   int64
	}{
		{name: "satellite command succeeds", invokedFrom: -200},
		{name: "failed group is reported", invokedFrom: -100, failGroup: -300},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{GroupIDs: groups,
				Groups:           []config.GroupConfig{{ID: -100}, {ID: -200}, {ID: -300}},
				ControlGroupID:   -100,
				NotifyTTLSeconds: -1}
			v := NewVerifier(cfg)
			fake := newFakeMod()
			fake.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
			if tt.failGroup != 0 {
				fake.senderUnbanErr = map[int64]error{tt.failGroup: errors.New("no rights")}
			}
			runFakeHandler(t, newAPITestBot(t, fake), v.onBc, telego.Update{Message: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: tt.invokedFrom, Type: "supergroup"},
				From:      &telego.User{ID: 7},
				Text:      "/bc allow 1234567890",
			}})

			if !v.channelWhitelisted(senderID) {
				t.Fatal("successful /bc allow did not add the sender to the global whitelist")
			}
			if len(fake.senderUnbans) != len(groups) {
				t.Fatalf("UnbanChatSenderChat calls = %d, want %d", len(fake.senderUnbans), len(groups))
			}
			for i, groupID := range groups {
				call := fake.senderUnbans[i]
				if call.ChatID.ID != groupID || call.SenderChatID != senderID {
					t.Errorf("unban call %d = chat %d sender %d, want chat %d sender %d",
						i, call.ChatID.ID, call.SenderChatID, groupID, senderID)
				}
			}
			if tt.failGroup == 0 {
				if strings.Contains(fake.lastSendText, "解封失败") {
					t.Errorf("success notice reports a failure: %q", fake.lastSendText)
				}
			} else if !strings.Contains(fake.lastSendText, "-300") || !strings.Contains(fake.lastSendText, "解封失败") {
				t.Errorf("failure notice = %q, want failed group %d", fake.lastSendText, tt.failGroup)
			}
		})
	}
}

func TestChannelWhitelistBound(t *testing.T) {
	tests := []struct {
		name  string
		extra int
	}{
		{name: "one over cap", extra: 1},
		{name: "multiple over cap", extra: 19},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewVerifier(&config.Config{})
			for i := range channelWhitelistMax + tt.extra {
				v.setChannelWhite(-1000000-int64(i), true)
			}
			if len(v.acWhite) != channelWhitelistMax {
				t.Fatalf("whitelist entries = %d, want cap %d", len(v.acWhite), channelWhitelistMax)
			}
			for i := range tt.extra {
				if v.channelWhitelisted(-1000000 - int64(i)) {
					t.Errorf("oldest whitelist entry %d was not evicted", i)
				}
			}
			if !v.channelWhitelisted(-1000000 - int64(channelWhitelistMax+tt.extra-1)) {
				t.Error("newest whitelist entry was evicted")
			}
		})
	}
}
