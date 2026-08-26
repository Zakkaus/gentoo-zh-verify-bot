package verify

import (
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
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
