package verify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

var errRequesterMissing = errors.New(`telego: declineChatJoinRequest: api: 400 "Bad Request: HIDE_REQUESTER_MISSING"`)

// A join request Telegram no longer holds must settle instead of retrying forever.
func TestDeclineGivesUpWhenJoinRequestIsGone(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{declineErr: errRequesterMissing}
	key := pkey{gid: -100, uid: 7}
	p := &pending{nonce: "n1", deadline: time.Now().Add(time.Hour), groupMsgID: 11, privateMsgID: 12}
	v.pend[key] = p

	settled, banned := v.finishDecline(context.Background(), fb, key.gid, key.uid, p, "wrong answer")

	if !settled || banned {
		t.Fatalf("settled=%v banned=%v, want settled=true banned=false", settled, banned)
	}
	v.mu.Lock()
	_, stillPending := v.pend[key]
	strikes := len(v.vfail)
	v.mu.Unlock()
	if stillPending {
		t.Error("a gone join request must not stay pending, or the retry timer loops forever")
	}
	if strikes != 0 {
		t.Errorf("strikes recorded = %d, want 0: the applicant did not cause the request to vanish", strikes)
	}
	if fb.declines != 1 {
		t.Errorf("decline calls = %d, want 1", fb.declines)
	}
}

// A genuine permission failure still keeps the request for a later retry.
func TestDeclineKeepsRequestOnPermissionFailure(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{declineErr: errors.New(`api: 400 "Bad Request: not enough rights"`)}
	key := pkey{gid: -100, uid: 8}
	p := &pending{nonce: "n2", deadline: time.Now().Add(time.Hour)}
	v.pend[key] = p

	if settled, _ := v.finishDecline(context.Background(), fb, key.gid, key.uid, p, "wrong answer"); settled {
		t.Fatal("a decline that Telegram rejected for missing rights is not settled")
	}
	v.mu.Lock()
	_, stillPending := v.pend[key]
	v.mu.Unlock()
	if !stillPending {
		t.Error("the request must stay pending so the retry can settle it once rights are restored")
	}
}

// An administrator settling the request in Telegram's own interface must not leave the bot
// retrying an approval it can never complete.
func TestApproveGivesUpWhenJoinRequestIsGone(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{approveErr: errRequesterMissing}
	key := pkey{gid: -100, uid: 9}
	p := &pending{nonce: "n3", deadline: time.Now().Add(time.Hour), groupMsgID: 21, privateMsgID: 22}
	v.pend[key] = p

	if !v.executeApprove(context.Background(), fb, key.gid, key.uid, p) {
		t.Fatal("a gone join request settles the approval instead of reporting a failure")
	}
	v.mu.Lock()
	_, stillPending := v.pend[key]
	v.mu.Unlock()
	if stillPending {
		t.Error("a gone join request must not stay pending, or the retry timer loops forever")
	}
	if fb.approves != 1 {
		t.Errorf("approve calls = %d, want 1", fb.approves)
	}
	if fb.lastSendChat == key.gid {
		t.Error("a request an administrator already settled is not a failure worth alerting the group about")
	}
	if fb.deletes != 2 {
		t.Errorf("deleted challenge messages = %d, want 2 (group and DM)", fb.deletes)
	}
}

// A genuine approval failure still keeps the request for a retry.
func TestApproveKeepsRequestOnRealFailure(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{approveErr: errors.New(`api: 400 "Bad Request: not enough rights"`)}
	key := pkey{gid: -100, uid: 10}
	p := &pending{nonce: "n4", deadline: time.Now().Add(time.Hour)}
	v.pend[key] = p

	if v.executeApprove(context.Background(), fb, key.gid, key.uid, p) {
		t.Fatal("an approval Telegram rejected for missing rights is not settled")
	}
	v.mu.Lock()
	_, stillPending := v.pend[key]
	v.mu.Unlock()
	if !stillPending {
		t.Error("the request must stay pending so the retry can settle it once rights are restored")
	}
}
