package verify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

// Missing rights may come back, so the settlement is retried — but not forever.
func TestSettlementRetryIsBounded(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{declineErr: errors.New(`api: 400 "Bad Request: not enough rights"`)}
	key := pkey{gid: -100, uid: 3}
	p := &pending{nonce: "n", deadline: time.Now().Add(time.Hour)}
	v.pend[key] = p
	t.Cleanup(v.stopForShutdown)

	for i := 1; i <= maxSettleFailures; i++ {
		v.mu.Lock()
		p.done = true // the claim each settlement path makes before its network call
		v.mu.Unlock()
		if outcome, _ := v.finishDecline(context.Background(), fb, key.gid, key.uid, p, "timeout"); outcome != declineUnsettled {
			t.Fatalf("attempt %d: outcome = %v, want declineUnsettled", i, outcome)
		}
	}
	v.mu.Lock()
	_, stillPending := v.pend[key]
	v.mu.Unlock()
	if stillPending {
		t.Errorf("after %d failed settlements the pending must be dropped, not retried every minute forever", maxSettleFailures)
	}
}

// Being removed from the group cannot be repaired by retrying, so it is not retried at all.
func TestRemovedFromGroupStopsSettlingAtOnce(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{declineErr: errors.New(`api: 403 "Forbidden: bot is not a member of the supergroup chat"`)}
	key := pkey{gid: -100, uid: 4}
	p := &pending{nonce: "n", deadline: time.Now().Add(time.Hour), done: true}
	v.pend[key] = p
	t.Cleanup(v.stopForShutdown)

	if outcome, _ := v.finishDecline(context.Background(), fb, key.gid, key.uid, p, "timeout"); outcome != declineUnsettled {
		t.Fatalf("outcome = %v, want declineUnsettled", outcome)
	}
	v.mu.Lock()
	_, stillPending := v.pend[key]
	v.mu.Unlock()
	if stillPending {
		t.Error("a group the bot was removed from cannot be settled later; the pending must not linger")
	}
	if fb.declines != 1 {
		t.Errorf("decline calls = %d, want 1: no retry is worth making", fb.declines)
	}
}

// The applicant hears "still being settled" once, not once per retry.
func TestSettlementNoticeIsSentOnce(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{gid: -100, uid: 5}
	p := &pending{nonce: "n", deadline: time.Now().Add(time.Hour)}
	v.pend[key] = p
	if !v.claimSettlePendingNotice(key.gid, key.uid, p) {
		t.Fatal("the first notice must be allowed")
	}
	if v.claimSettlePendingNotice(key.gid, key.uid, p) {
		t.Error("a repeat notice would DM the applicant on every retry")
	}
}
