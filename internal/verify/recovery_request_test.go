package verify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

// An administrator can approve a join request by hand while the bot is offline. Recovery must
// notice the applicant is already in the group instead of re-posting a challenge for them.
func TestRecoveryDropsPendingWhoseApplicantAlreadyJoined(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
	v.botUsername = "bot"
	joined, waiting := pkey{-100, 1}, pkey{-100, 2}
	v.pend[joined] = &pending{nonce: "a", name: "Alice", deadline: time.Now().Add(-time.Minute), groupMsgID: 11, privateMsgID: 21}
	v.pend[waiting] = &pending{nonce: "b", name: "Bob", deadline: time.Now().Add(-time.Minute), groupMsgID: 12}
	fb := &fakeVerifyBot{memberByID: map[int64]telego.ChatMember{
		joined.uid:  &telego.ChatMemberMember{Status: telego.MemberStatusMember},
		waiting.uid: &telego.ChatMemberLeft{Status: telego.MemberStatusLeft},
	}}

	v.onRecovery(context.Background(), fb, 3*time.Minute)
	t.Cleanup(v.stopForShutdown)

	if _, still := v.pend[joined]; still {
		t.Error("an applicant already in the group has nothing left to verify; the pending must be dropped")
	}
	p, ok := v.pend[waiting]
	if !ok || p.done {
		t.Fatal("an applicant who is not a member yet must keep their verification")
	}
	// Three messages for Bob only: the outage notice, the group challenge, the DM challenge.
	if fb.sends != 3 {
		t.Errorf("sends = %d, want 3: only the applicant still waiting should be messaged", fb.sends)
	}
	deleted := map[int]bool{}
	for _, id := range fb.deletedMessageIDs {
		deleted[id] = true
	}
	for _, id := range []int{11, 21} {
		if !deleted[id] {
			t.Errorf("stale challenge message %d was left behind", id)
		}
	}
}

// A membership lookup that fails must not skip verification.
func TestRecoveryKeepsPendingWhenMembershipLookupFails(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
	v.botUsername = "bot"
	key := pkey{-100, 1}
	v.pend[key] = &pending{nonce: "a", name: "Alice", deadline: time.Now().Add(-time.Minute), groupMsgID: 11}
	fb := &fakeVerifyBot{memberErr: errors.New("Telegram unavailable")}

	v.onRecovery(context.Background(), fb, 3*time.Minute)
	t.Cleanup(v.stopForShutdown)

	p, ok := v.pend[key]
	if !ok || p.done {
		t.Fatal("an unreadable membership lookup is not proof the applicant joined; verification must continue")
	}
	if fb.sends == 0 {
		t.Error("the applicant should still be re-notified after the outage")
	}
}

// The 48-hour deferral cap survives a recovery that also probes membership.
func TestRecoveryPastDeferralCapDoesNotRepostChallenges(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
	v.botUsername = "bot"
	key := pkey{-100, 1}
	past := time.Now().Add(-(maxVerificationDeferral + time.Hour))
	v.pend[key] = &pending{nonce: "a", name: "Alice", deadline: past, deferredSince: past, groupMsgID: 11}
	fb := &fakeVerifyBot{}

	v.onRecovery(context.Background(), fb, 50*time.Hour)
	t.Cleanup(v.stopForShutdown)

	p, ok := v.pend[key]
	if !ok {
		t.Fatal("a capped pending stays until its short settlement retry runs")
	}
	if !p.deferralCapReached {
		t.Error("a verification deferred beyond 48 hours must be marked capped, not given another full window")
	}
	if fb.sends != 0 {
		t.Errorf("sends = %d, want 0: a capped verification is settling, not restarting", fb.sends)
	}
	if p.timer != nil {
		p.timer.Stop()
	}
}
