package verify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// An applicant stuck behind a channel gate the bot cannot read is stuck through no fault of
// their own. Refusing entry is right; charging them a strike toward an automatic ban is not.
func TestUnreadableChannelGateDoesNotStrike(t *testing.T) {
	cases := []struct {
		name     string
		failOpen *bool
	}{
		{name: "strict deployment", failOpen: boolPtr(false)},
		{name: "default deployment", failOpen: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{RequiredChannelID: -400, VerifyMaxFails: 3, RequiredChannelFailOpen: tc.failOpen}
			v := newTestService(cfg)
			v.botID = 42
			gid, uid := int64(-100), int64(5)
			fb := &fakeVerifyBot{memberErr: errors.New("CHAT_ADMIN_REQUIRED")}

			for round := 1; round <= 3; round++ {
				p := &pending{nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
				v.pend[pkey{gid, uid}] = p
				v.isChannelMember(context.Background(), fb, gid, uid, i18n.LangEN)
				if _, banned := v.finishDecline(context.Background(), fb, gid, uid, p, "timeout"); banned {
					t.Fatalf("round %d: an unreadable gate must never reach an automatic ban", round)
				}
			}
			v.mu.Lock()
			strikes := len(v.vfail)
			v.mu.Unlock()
			if strikes != 0 {
				t.Errorf("strike records = %d, want 0: the bot could not read the gate", strikes)
			}
			if fb.bans != 0 {
				t.Errorf("bans = %d, want 0", fb.bans)
			}
			if fb.declines != 3 {
				t.Errorf("declines = %d, want 3: the applicant is still refused entry", fb.declines)
			}
		})
	}
}

// A readable gate clears the marker, so someone who simply never joined still carries the strike.
func TestReadableChannelGateStillStrikes(t *testing.T) {
	cfg := &config.Config{RequiredChannelID: -400, VerifyMaxFails: 3}
	v := newTestService(cfg)
	v.botID = 42
	gid, uid := int64(-100), int64(6)
	p := &pending{nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour), channelUnreadable: true}
	v.pend[pkey{gid, uid}] = p
	fb := &fakeVerifyBot{member: &telego.ChatMemberLeft{Status: telego.MemberStatusLeft}}

	if v.isChannelMember(context.Background(), fb, gid, uid, i18n.LangEN) {
		t.Fatal("a confirmed non-member does not pass the gate")
	}
	if _, _ = v.finishDecline(context.Background(), fb, gid, uid, p, "timeout"); true {
		v.mu.Lock()
		strikes := len(v.vfail)
		v.mu.Unlock()
		if strikes != 1 {
			t.Errorf("strike records = %d, want 1: a readable gate clears any earlier failure marker", strikes)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
