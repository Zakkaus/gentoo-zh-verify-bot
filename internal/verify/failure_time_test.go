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

// A settlement the bot has to retry does not move the moment the applicant failed. Stamping the
// strike with the retry time instead would start their cooldown from however long the bot spent
// retrying, which is time they did not spend failing.
func TestRetryingASettlementKeepsTheRealFailureTime(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}, VerifyRetrySeconds: 180})
	gid, uid := int64(-100), int64(5)
	failedAt := v.wallNow().Add(-9 * time.Minute)
	p := &pending{gate: gateMute, nonce: "n", lang: i18n.LangEN, failedAt: failedAt,
		deadline: time.Now().Add(time.Hour), done: true}
	v.pend[pkey{gid, uid}] = p
	v.markTerminalLocked(pkey{gid, uid}, p)
	t.Cleanup(v.stopForShutdown)

	// The first settlement fails, so it is retried.
	failing := &fakeVerifyBot{
		member: &telego.ChatMemberMember{Status: telego.MemberStatusMember},
		banErr: errors.New("not enough rights"),
	}
	if outcome, _ := v.finishDecline(context.Background(), failing, gid, uid, p, wrongAnswerReason); outcome != declineUnsettled {
		t.Fatalf("outcome = %v, want declineUnsettled", outcome)
	}
	v.mu.Lock()
	kept := p.failedAt
	v.mu.Unlock()
	if !kept.Equal(failedAt) {
		t.Fatalf("failure time moved from %v to %v", failedAt, kept)
	}

	// The retry succeeds and records the strike.
	v.mu.Lock()
	p.done = true
	v.mu.Unlock()
	working := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	if _, _ = v.finishDecline(context.Background(), working, gid, uid, p, wrongAnswerReason); true {
		v.mu.Lock()
		rec := v.vfail[pkey{gid, uid}]
		v.mu.Unlock()
		if rec == nil {
			t.Fatal("the wrong answer must be recorded")
		}
		if !rec.last.Equal(failedAt) {
			t.Errorf("strike stamped at %v, want the moment they failed, %v", rec.last, failedAt)
		}
	}
}
