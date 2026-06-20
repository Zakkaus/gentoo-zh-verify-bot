package main

import (
	"testing"
	"time"
)

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
