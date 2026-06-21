package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

// fakeVerifyBot is a verifyBot stand-in so the approve / decline / ban handler branches can be
// exercised without a real Telegram connection; it records call counts and returns configured
// errors for the approve and ban calls.
type fakeVerifyBot struct {
	approveErr error
	banErr     error
	approves   int
	declines   int
	bans       int
	deletes    int
	sends      int
}

func (b *fakeVerifyBot) ApproveChatJoinRequest(context.Context, *telego.ApproveChatJoinRequestParams) error {
	b.approves++
	return b.approveErr
}
func (b *fakeVerifyBot) DeclineChatJoinRequest(context.Context, *telego.DeclineChatJoinRequestParams) error {
	b.declines++
	return nil
}
func (b *fakeVerifyBot) BanChatMember(context.Context, *telego.BanChatMemberParams) error {
	b.bans++
	return b.banErr
}
func (b *fakeVerifyBot) DeleteMessage(context.Context, *telego.DeleteMessageParams) error {
	b.deletes++
	return nil
}
func (b *fakeVerifyBot) SendMessage(context.Context, *telego.SendMessageParams) (*telego.Message, error) {
	b.sends++
	return &telego.Message{MessageID: 1}, nil
}

func livePending(msgID int) *pending {
	return &pending{nonce: "n", deadline: time.Now().Add(time.Hour), groupMsgID: msgID}
}

// TestApproveSuccess: a successful approve consumes the pending, clears the user's strikes, deletes
// the challenge, and never bans.
func TestApproveSuccess(t *testing.T) {
	v := NewVerifier(&Config{})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	v.vfail[key] = &vfailRec{count: 2, last: time.Now()} // had strikes; approve should clear them
	fb := &fakeVerifyBot{}
	if !v.approve(context.Background(), fb, -100, 5) {
		t.Fatal("approve should return true on success")
	}
	if fb.approves != 1 {
		t.Errorf("ApproveChatJoinRequest should be called once, got %d", fb.approves)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("the pending should be consumed after a successful approve")
	}
	if _, ok := v.vfail[key]; ok {
		t.Error("a successful approve should clear the user's verify-fail strikes")
	}
	if fb.bans != 0 {
		t.Error("approve must never ban")
	}
}

// TestApproveFailureReopens: a failed approve keeps the pending retryable, re-opens it (done=false),
// and — the v3.6.1 race guarantee — never bans the user.
func TestApproveFailureReopens(t *testing.T) {
	v := NewVerifier(&Config{})
	key := pkey{-100, 5}
	p := livePending(42)
	v.pend[key] = p
	fb := &fakeVerifyBot{approveErr: errors.New("Forbidden: not enough rights")}
	if v.approve(context.Background(), fb, -100, 5) {
		t.Fatal("approve should return false when ApproveChatJoinRequest fails")
	}
	if cur, ok := v.pend[key]; !ok || cur != p {
		t.Error("a failed approve must keep the pending (retryable), not strand the applicant")
	}
	if p.done {
		t.Error("a failed approve must re-open the pending (done=false) so it can retry / time out")
	}
	if fb.bans != 0 {
		t.Error("a failed approve must never ban the user")
	}
	if p.timer != nil {
		p.timer.Stop() // reopenPending re-armed a (far-future) timer; tidy
	}
}

// TestDeclineBelowThreshold: a wrong answer below the auto-ban threshold declines and records one
// strike, without banning.
func TestDeclineBelowThreshold(t *testing.T) {
	v := NewVerifier(&Config{VerifyMaxFails: 3})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	fb := &fakeVerifyBot{}
	handled, banned := v.decline(context.Background(), fb, -100, 5, "n", "wrong answer")
	if !handled || banned {
		t.Fatalf("first failure should decline, not ban: handled=%v banned=%v", handled, banned)
	}
	if fb.declines != 1 || fb.bans != 0 {
		t.Errorf("below threshold: want 1 decline + 0 bans, got declines=%d bans=%d", fb.declines, fb.bans)
	}
	if r := v.vfail[key]; r == nil || r.count != 1 {
		t.Errorf("a strike should be recorded, got %+v", r)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("decline should consume the pending")
	}
}

// TestDeclineAutoBan: reaching the threshold auto-bans (BanChatMember) and clears strikes on a
// successful ban.
func TestDeclineAutoBan(t *testing.T) {
	v := NewVerifier(&Config{VerifyMaxFails: 1}) // the first failure trips the auto-ban
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	fb := &fakeVerifyBot{}
	handled, banned := v.decline(context.Background(), fb, -100, 5, "n", "wrong answer")
	if !handled || !banned {
		t.Fatalf("reaching the threshold should auto-ban: handled=%v banned=%v", handled, banned)
	}
	if fb.bans != 1 {
		t.Errorf("BanChatMember should be called once, got %d", fb.bans)
	}
	if _, ok := v.vfail[key]; ok {
		t.Error("strikes should be cleared after a successful auto-ban")
	}
}

// TestBanApplicant: the admin report-and-ban path declines + bans, consumes the pending, and
// reports banned=false honestly when the ban call fails.
func TestBanApplicant(t *testing.T) {
	v := NewVerifier(&Config{})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	fb := &fakeVerifyBot{}
	handled, banned := v.banApplicant(context.Background(), fb, -100, 5)
	if !handled || !banned {
		t.Fatalf("banApplicant should decline + ban: handled=%v banned=%v", handled, banned)
	}
	if fb.declines != 1 || fb.bans != 1 {
		t.Errorf("want 1 decline + 1 ban, got declines=%d bans=%d", fb.declines, fb.bans)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("banApplicant should consume the pending")
	}

	v.pend[key] = livePending(0)
	fbFail := &fakeVerifyBot{banErr: errors.New("not enough rights")}
	if _, banned := v.banApplicant(context.Background(), fbFail, -100, 5); banned {
		t.Error("a failed BanChatMember must report banned=false (honest feedback)")
	}
}

// TestApproveClaimBlocksTimeoutDecline guards the approve/timeout race fix: once the approve path
// has CLAIMED a pending (marked it done before issuing the network ApproveChatJoinRequest), the
// timeout timer's decline path (consumeNonce) must refuse to act on it — otherwise a user who
// answered correctly right at the deadline could be struck or auto-banned.
func TestApproveClaimBlocksTimeoutDecline(t *testing.T) {
	v := &Verifier{pend: map[pkey]*pending{}}
	key := pkey{gid: -100, uid: 5}
	v.pend[key] = &pending{nonce: "abc", deadline: time.Now().Add(time.Hour)}

	// approve claims the pending (marks it done) before its network call.
	v.mu.Lock()
	v.pend[key].done = true
	v.mu.Unlock()

	// the timeout timer now fires -> decline -> consumeNonce; it MUST bail on the claimed pending.
	if _, ok := v.consumeNonce(-100, 5, "abc"); ok {
		t.Error("a claimed (done) pending must not be consumable by the timeout path — a verified user would otherwise get a strike/ban")
	}
}

// TestReopenPendingRestoresRetryable covers the failed-approve restore: reopenPending re-opens a
// claimed pending (done=false, timer re-armed) so the applicant stays retryable, but refuses to
// touch one that a newer request has since replaced.
func TestReopenPendingRestoresRetryable(t *testing.T) {
	v := &Verifier{pend: map[pkey]*pending{}}
	key := pkey{gid: -100, uid: 5}
	p := &pending{nonce: "abc", deadline: time.Now().Add(time.Hour), done: true}
	v.pend[key] = p

	v.reopenPending(nil, -100, 5, p) // bot unused: a 1h deadline means the re-armed timer won't fire in-test
	if p.done {
		t.Error("reopenPending should re-open the pending (done=false) for retry")
	}
	if p.timer == nil {
		t.Fatal("reopenPending should re-arm the timeout timer")
	}
	p.timer.Stop() // tidy: don't let it fire after the test

	// a pending already replaced by a newer request must NOT be re-opened.
	v.pend[key] = &pending{nonce: "new", deadline: time.Now().Add(time.Hour)}
	stale := &pending{nonce: "abc", deadline: time.Now().Add(time.Hour), done: true}
	v.reopenPending(nil, -100, 5, stale)
	if !stale.done {
		t.Error("a replaced pending must not be re-opened")
	}
}
