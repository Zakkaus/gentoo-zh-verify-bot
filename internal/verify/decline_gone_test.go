package verify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

var errRequesterMissing = errors.New(`telego: declineChatJoinRequest: api: 400 "Bad Request: HIDE_REQUESTER_MISSING"`)

// A join request Telegram no longer holds must settle instead of retrying forever. Where the
// applicant ended up decides both the message and whether the failure is theirs to carry.
func TestDeclineGivesUpWhenJoinRequestIsGone(t *testing.T) {
	cases := []struct {
		name        string
		member      telego.ChatMember
		memberErr   error
		wantOutcome declineOutcome
		wantStrikes int
	}{
		{
			name: "administrator let them in", member: &telego.ChatMemberMember{Status: telego.MemberStatusMember},
			wantOutcome: declineGoneUnknown, wantStrikes: 0,
		},
		{
			name: "applicant is out", member: &telego.ChatMemberLeft{Status: telego.MemberStatusLeft},
			wantOutcome: declineGoneAndOut, wantStrikes: 1,
		},
		{
			name: "membership unreadable", memberErr: errors.New("Telegram unavailable"),
			wantOutcome: declineGoneUnknown, wantStrikes: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestService(&config.Config{})
			fb := &fakeVerifyBot{declineErr: errRequesterMissing, member: tc.member, memberErr: tc.memberErr}
			key := pkey{gid: -100, uid: 7}
			p := &pending{nonce: "n1", deadline: time.Now().Add(time.Hour), groupMsgID: 11, privateMsgID: 12}
			v.pend[key] = p

			outcome, banned := v.finishDecline(context.Background(), fb, key.gid, key.uid, p, wrongAnswerReason)

			if outcome != tc.wantOutcome || banned {
				t.Fatalf("outcome=%v banned=%v, want outcome=%v banned=false", outcome, banned, tc.wantOutcome)
			}
			v.mu.Lock()
			_, stillPending := v.pend[key]
			strikes := len(v.vfail)
			v.mu.Unlock()
			if stillPending {
				t.Error("a gone join request must not stay pending, or the retry timer loops forever")
			}
			if strikes != tc.wantStrikes {
				t.Errorf("strike records = %d, want %d", strikes, tc.wantStrikes)
			}
			if fb.declines != 1 {
				t.Errorf("decline calls = %d, want 1", fb.declines)
			}
		})
	}
}

// A timeout is not proof the applicant ignored anything: an administrator may have rejected the
// request hours earlier. Only a wrong answer survives a vanished request as a strike.
func TestGoneTimeoutDoesNotStrike(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{declineErr: errRequesterMissing, member: &telego.ChatMemberLeft{Status: telego.MemberStatusLeft}}
	key := pkey{gid: -100, uid: 11}
	p := &pending{nonce: "n5", deadline: time.Now().Add(time.Hour)}
	v.pend[key] = p

	if outcome, _ := v.finishDecline(context.Background(), fb, key.gid, key.uid, p, "timeout"); outcome != declineGoneAndOut {
		t.Fatalf("outcome = %v, want declineGoneAndOut", outcome)
	}
	v.mu.Lock()
	strikes := len(v.vfail)
	v.mu.Unlock()
	if strikes != 0 {
		t.Errorf("strike records = %d, want 0 for a timeout on a request that was already gone", strikes)
	}
}

// A genuine permission failure still keeps the request for a later retry.
func TestDeclineKeepsRequestOnPermissionFailure(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{declineErr: errors.New(`api: 400 "Bad Request: not enough rights"`)}
	key := pkey{gid: -100, uid: 8}
	p := &pending{nonce: "n2", deadline: time.Now().Add(time.Hour)}
	v.pend[key] = p

	if outcome, _ := v.finishDecline(context.Background(), fb, key.gid, key.uid, p, wrongAnswerReason); outcome.settled() {
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

	if got := v.executeApprove(context.Background(), fb, key.gid, key.uid, p); got != approveGone {
		t.Fatalf("approve outcome = %v, want approveGone: the bot must not claim an approval it did not make", got)
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

	if got := v.executeApprove(context.Background(), fb, key.gid, key.uid, p); got != approveFailed {
		t.Fatalf("approve outcome = %v, want approveFailed", got)
	}
	v.mu.Lock()
	_, stillPending := v.pend[key]
	v.mu.Unlock()
	if !stillPending {
		t.Error("the request must stay pending so the retry can settle it once rights are restored")
	}
}
