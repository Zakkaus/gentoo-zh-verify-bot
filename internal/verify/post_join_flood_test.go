package verify

import (
	"context"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// Leaving and rejoining must not buy a fresh notice and a fresh question every time. One
// verification, one challenge, however many times somebody walks through the door.
func TestRejoiningDoesNotRepeatTheChallenge(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	v.botUsername = "bot"
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	t.Cleanup(v.stopForShutdown)

	for range 8 {
		runFakeHandler(t, newAPITestBot(t, fb), v.OnMemberJoined, joinUpdate(-100, 5, telego.ChatTypeSupergroup, nil))
	}

	// The first arrival posts the group notice and the DM question; the seven after it say nothing.
	if fb.sends != 2 {
		t.Errorf("messages sent = %d, want 2: only the first arrival starts a verification", fb.sends)
	}
	v.mu.Lock()
	pending := len(v.pend)
	v.mu.Unlock()
	if pending != 1 {
		t.Errorf("pending verifications = %d, want 1", pending)
	}
}

// Rejoining must not extend the window either: the deadline belongs to the verification, not to
// however recently the member walked back in.
func TestRejoiningDoesNotExtendTheWindow(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	v.botUsername = "bot"
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	t.Cleanup(v.stopForShutdown)
	runFakeHandler(t, newAPITestBot(t, fb), v.OnMemberJoined, joinUpdate(-100, 5, telego.ChatTypeSupergroup, nil))

	v.mu.Lock()
	first := v.pend[pkey{-100, 5}].deadline
	v.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	runFakeHandler(t, newAPITestBot(t, fb), v.OnMemberJoined, joinUpdate(-100, 5, telego.ChatTypeSupergroup, nil))

	v.mu.Lock()
	second := v.pend[pkey{-100, 5}].deadline
	v.mu.Unlock()
	if !second.Equal(first) {
		t.Errorf("deadline moved from %v to %v: walking in and out must not buy more time", first, second)
	}
}

// The bot tells a removed member how long to wait. Coming back early is refused, not re-questioned.
func TestCooldownIsEnforcedOnRejoin(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}, VerifyRetrySeconds: 180})
	v.botUsername = "bot"
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	t.Cleanup(v.stopForShutdown)
	v.recordVerifyFail(-100, 5, v.wallNow())
	if v.verifyCooldownRemaining(-100, 5) <= 0 {
		t.Fatal("the fixture must be inside the cooldown")
	}

	runFakeHandler(t, newAPITestBot(t, fb), v.OnMemberJoined, joinUpdate(-100, 5, telego.ChatTypeSupergroup, nil))

	v.mu.Lock()
	pending := len(v.pend)
	v.mu.Unlock()
	if pending != 0 {
		t.Error("somebody inside their cooldown must not be given another question")
	}
	if fb.bans != 1 || fb.unbans != 1 {
		t.Errorf("bans = %d unbans = %d, want 1 and 1: removed again, not kept out", fb.bans, fb.unbans)
	}
	if fb.mutes != 0 {
		t.Errorf("mutes = %d, want 0: there is no verification to hold them for", fb.mutes)
	}
	// One explanation, not one per rejoin.
	before := fb.sends
	runFakeHandler(t, newAPITestBot(t, fb), v.OnMemberJoined, joinUpdate(-100, 5, telego.ChatTypeSupergroup, nil))
	if fb.sends != before {
		t.Errorf("sends went %d → %d: the cooldown notice must be throttled, or a rejoin loop becomes a DM loop", before, fb.sends)
	}
}

// Re-applying inside the cooldown is refused every time, but the explanation is sent once:
// the decline is the answer, and repeating it turns a determined applicant into a DM loop.
func TestCooldownNoticeIsThrottledOnBothGates(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}, VerifyRetrySeconds: 180})
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	gid, uid := int64(-100), int64(5)
	v.recordVerifyFail(gid, uid, v.wallNow())

	for range 5 {
		if !v.joinGate(context.Background(), fb, gid, uid, i18n.LangEN) {
			t.Fatal("an applicant inside the cooldown must be refused")
		}
	}
	if fb.declines != 5 {
		t.Errorf("declines = %d, want 5: every re-apply is refused", fb.declines)
	}
	if fb.sends != 1 {
		t.Errorf("cooldown notices = %d, want 1", fb.sends)
	}
}

// An administrator adding somebody outranks a cooldown from an earlier failure. Throwing them
// out seconds after the administrator put them in would be the bot overruling that decision;
// they still have to verify.
func TestAdminAddedMemberIsNotRemovedForAnOldCooldown(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}, VerifyRetrySeconds: 180})
	v.botUsername = "bot"
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	t.Cleanup(v.stopForShutdown)
	v.recordVerifyFail(-100, 5, v.wallNow())

	added := joinUpdate(-100, 5, telego.ChatTypeSupergroup, nil)
	added.ChatMember.From = telego.User{ID: 42} // an administrator, not the member
	runFakeHandler(t, newAPITestBot(t, fb), v.OnMemberJoined, added)

	if fb.bans != 0 {
		t.Errorf("bans = %d, want 0: the administrator just added them", fb.bans)
	}
	v.mu.Lock()
	pending := len(v.pend)
	v.mu.Unlock()
	if pending != 1 {
		t.Errorf("pending verifications = %d, want 1: being vouched for is not verification", pending)
	}
}
