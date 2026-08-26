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

// The required channel can break after the challenge is posted. Somebody who never opened the
// direct chat never had the gate read for them, so nothing would mark the failure as the bot's
// and the timeout would count against them, all the way to an automatic ban.
func TestChannelBreakingAfterTheChallengeIsNotTheApplicantsFault(t *testing.T) {
	cfg := &config.Config{GroupIDs: []int64{-100}, RequiredChannelID: -400, VerifyMaxFails: 3}
	v := newTestService(cfg)
	v.botID = 42
	gid, uid := int64(-100), int64(5)
	// The challenge went out while the channel was readable; it broke afterwards.
	fb := &fakeVerifyBot{memberErr: errors.New("CHAT_ADMIN_REQUIRED")}
	p := &pending{nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(-time.Minute), challengeDelivered: true}
	v.pend[pkey{gid, uid}] = p
	t.Cleanup(v.stopForShutdown)

	v.onExpiry(context.Background(), fb, gid, uid, p.nonce, p.epoch, "timeout")

	v.mu.Lock()
	strikes := len(v.vfail)
	v.mu.Unlock()
	if strikes != 0 {
		t.Errorf("strike records = %d, want 0: the gate the bot could not read is not their failure", strikes)
	}
}

// A working gate still charges the timeout to the applicant.
func TestWorkingChannelStillChargesTheTimeout(t *testing.T) {
	cfg := &config.Config{GroupIDs: []int64{-100}, RequiredChannelID: -400, VerifyMaxFails: 3}
	v := newTestService(cfg)
	v.botID = 42
	gid, uid := int64(-100), int64(6)
	fb := &fakeVerifyBot{member: &telego.ChatMemberLeft{Status: telego.MemberStatusLeft}}
	p := &pending{nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(-time.Minute), challengeDelivered: true}
	v.pend[pkey{gid, uid}] = p
	t.Cleanup(v.stopForShutdown)

	v.onExpiry(context.Background(), fb, gid, uid, p.nonce, p.epoch, "timeout")

	v.mu.Lock()
	strikes := len(v.vfail)
	v.mu.Unlock()
	if strikes != 1 {
		t.Errorf("strike records = %d, want 1: ignoring a working challenge is their own doing", strikes)
	}
}
