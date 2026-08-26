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
