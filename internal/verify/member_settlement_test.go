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

// A verification that began while somebody waited outside cannot be settled by declining a join
// request once they are inside: the call settles nothing and the member simply stays, unverified.
func TestVerificationFollowsTheApplicantIntoTheGroup(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	v.botUsername = "bot"
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	gid, uid := int64(-100), int64(5)
	p := &pending{nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{gid, uid}] = p
	t.Cleanup(v.stopForShutdown)

	runFakeHandler(t, newAPITestBot(t, fb), v.OnMemberJoined, joinUpdate(gid, uid, telego.ChatTypeSupergroup, nil))

	v.mu.Lock()
	gate := v.pend[pkey{gid, uid}].gate
	v.mu.Unlock()
	if gate != gateMute {
		t.Fatalf("gate = %q, want the member gate: they are inside the group now", gate)
	}
	v.onExpiry(context.Background(), fb, gid, uid, p.nonce, p.epoch, "timeout")
	if fb.bans == 0 {
		t.Error("the expired verification must remove the member; declining a request that no longer exists leaves them in")
	}
}

// Being blocked by the queue is the bot's problem, but leaving an unverified member inside is
// worse than asking them to come back. They are taken out without a strike.
func TestQueueFullTakesTheMemberBackOut(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	v.botUsername = "bot"
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	for i := range pendingPerGroupCap {
		v.pend[pkey{-100, int64(1000 + i)}] = &pending{nonce: "x", deadline: time.Now().Add(time.Hour)}
	}
	t.Cleanup(v.stopForShutdown)

	runFakeHandler(t, newAPITestBot(t, fb), v.OnMemberJoined, joinUpdate(-100, 5, telego.ChatTypeSupergroup, nil))

	if fb.bans != 1 || fb.unbans != 1 {
		t.Errorf("bans = %d unbans = %d, want 1 and 1: removed so they can return later", fb.bans, fb.unbans)
	}
	v.mu.Lock()
	strikes := len(v.vfail)
	v.mu.Unlock()
	if strikes != 0 {
		t.Errorf("strike records = %d, want 0: a full queue is not the member's failure", strikes)
	}
}

// Somebody an administrator already banned stays banned. Removing them again would ban and then
// unban, quietly lifting the administrator's decision.
func TestRemovalLeavesAnExistingBanAlone(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	fb := &fakeVerifyBot{member: &telego.ChatMemberBanned{Status: telego.MemberStatusBanned}}
	gid, uid := int64(-100), int64(6)
	p := &pending{gate: gateMute, nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{gid, uid}] = p
	t.Cleanup(v.stopForShutdown)

	if _, _ = v.finishDecline(context.Background(), fb, gid, uid, p, "timeout"); fb.unbans != 0 {
		t.Errorf("unbans = %d, want 0: an administrator's ban is not ours to lift", fb.unbans)
	}
	if fb.bans != 0 {
		t.Errorf("bans = %d, want 0: they are already out", fb.bans)
	}
}

// Giving up on a settlement must not leave somebody silenced with nothing left to lift it.
func TestGivingUpLiftsTheHold(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	fb := &fakeVerifyBot{
		member: &telego.ChatMemberMember{Status: telego.MemberStatusMember},
		banErr: errors.New(`api: 403 "Forbidden: bot is not a member of the supergroup chat"`),
	}
	gid, uid := int64(-100), int64(7)
	p := &pending{gate: gateMute, held: true, nonce: "n", lang: i18n.LangEN,
		deadline: time.Now().Add(time.Hour), done: true}
	v.pend[pkey{gid, uid}] = p
	t.Cleanup(v.stopForShutdown)

	_, _ = v.finishDecline(context.Background(), fb, gid, uid, p, "timeout")

	v.mu.Lock()
	_, still := v.pend[pkey{gid, uid}]
	v.mu.Unlock()
	if still {
		t.Fatal("an unsettleable verification is dropped")
	}
	if fb.unmutes != 1 {
		t.Errorf("unmutes = %d, want 1: dropping the verification must release the member", fb.unmutes)
	}
}

// Switching verification off must not leave its timers running: an applicant would still be
// declined and a member still removed, minutes later, for a rule the administrator withdrew.
func TestDisablingVerificationCancelsWhatIsRunning(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	gid := int64(-100)
	waiting := &pending{nonce: "a", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour), groupMsgID: 1}
	held := &pending{gate: gateMute, held: true, nonce: "b", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{gid, 5}] = waiting
	v.pend[pkey{gid, 6}] = held
	other := &pending{nonce: "c", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{-200, 7}] = other
	t.Cleanup(v.stopForShutdown)

	if err := v.SetEnabled(gid, false); err != nil {
		t.Fatal(err)
	}

	v.mu.Lock()
	_, stillWaiting := v.pend[pkey{gid, 5}]
	_, stillHeld := v.pend[pkey{gid, 6}]
	_, untouched := v.pend[pkey{-200, 7}]
	strikes := len(v.vfail)
	v.mu.Unlock()
	if stillWaiting || stillHeld {
		t.Error("verifications in the group must be cancelled, not left to settle")
	}
	if !untouched {
		t.Error("another group's verification is none of this command's business")
	}
	if strikes != 0 {
		t.Errorf("strike records = %d, want 0: a withdrawn rule is nobody's failure", strikes)
	}
}

// Passing restores the group's default permissions, which would also lift a restriction somebody
// else added. The hold is only lifted while the one in force is still the one verification placed.
func TestReleaseLeavesSomebodyElsesRestrictionAlone(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	gid, uid := int64(-100), int64(8)
	ours := v.wallNow().Add(5 * time.Minute).Unix()
	p := &pending{gate: gateMute, held: true, holdUntil: ours, nonce: "n", lang: i18n.LangEN,
		deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{gid, uid}] = p
	t.Cleanup(v.stopForShutdown)

	// An administrator replaced the restriction with one of their own, expiring much later.
	theirs := &fakeVerifyBot{member: &telego.ChatMemberRestricted{
		Status: telego.MemberStatusRestricted, IsMember: true, UntilDate: ours + 86400,
	}}
	if got := v.executeApprove(context.Background(), theirs, gid, uid, p); got != approveConfirmed {
		t.Fatalf("approve outcome = %v, want approveConfirmed", got)
	}
	if theirs.unmutes != 0 {
		t.Errorf("unmutes = %d, want 0: that restriction is not the one verification placed", theirs.unmutes)
	}

	// Our own restriction is still ours to lift.
	v.pend[pkey{gid, uid}] = p
	mine := &fakeVerifyBot{member: &telego.ChatMemberRestricted{
		Status: telego.MemberStatusRestricted, IsMember: true, UntilDate: ours,
	}}
	if got := v.executeApprove(context.Background(), mine, gid, uid, p); got != approveConfirmed {
		t.Fatalf("approve outcome = %v, want approveConfirmed", got)
	}
	if mine.unmutes != 1 {
		t.Errorf("unmutes = %d, want 1: passing lifts the hold verification placed", mine.unmutes)
	}
}

// A button left behind by a failed deletion must not settle a verification the administrator
// never looked at. The applicant's answer buttons have always carried a nonce; these now do too.
func TestStaleAdminButtonDoesNotSettleTheNextVerification(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	gid, uid := int64(-100), int64(9)
	current := &pending{gate: gateMute, nonce: "fresh", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{gid, uid}] = current
	t.Cleanup(v.stopForShutdown)
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}

	stale := telego.Update{CallbackQuery: &telego.CallbackQuery{
		ID:      "1",
		From:    telego.User{ID: 42, LanguageCode: "en"},
		Message: &telego.Message{MessageID: 1, Chat: telego.Chat{ID: gid}},
		Data:    AdminCallbackPrefix + "pass:-100:9:stale",
	}}
	runFakeHandler(t, newAPITestBot(t, fb), v.OnAdminAction, stale)

	v.mu.Lock()
	_, still := v.pend[pkey{gid, uid}]
	v.mu.Unlock()
	if !still {
		t.Error("a button from a finished verification must not settle the running one")
	}
	if fb.unmutes != 0 || fb.approves != 0 {
		t.Errorf("unmutes = %d approves = %d, want 0 and 0", fb.unmutes, fb.approves)
	}
}

// Removal that cannot be undone leaves the member banned. Inviting them back would be false.
func TestStrandedBanIsReportedAsABan(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	gid, uid := int64(-100), int64(10)
	p := &pending{gate: gateMute, nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{gid, uid}] = p
	t.Cleanup(v.stopForShutdown)
	fb := &fakeVerifyBot{
		member:   &telego.ChatMemberMember{Status: telego.MemberStatusMember},
		unbanErr: errors.New("not enough rights"),
	}

	_, banned := v.finishDecline(context.Background(), fb, gid, uid, p, "timeout")
	if !banned {
		t.Error("a removal whose unban failed leaves them banned; the result must say so")
	}
}
