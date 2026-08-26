package verify

import (
	"context"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// A held member is a member of the group by definition. Recovery must not read that as "they
// already got in", or every post-join verification is dropped after an outage and the member
// stays muted with no challenge and nobody to lift it.
func TestRecoveryKeepsHeldMembers(t *testing.T) {
	v := newTestService(&config.Config{})
	v.botUsername = "bot"
	gid, uid := int64(-100), int64(42)
	fb := &fakeVerifyBot{member: &telego.ChatMemberRestricted{Status: telego.MemberStatusRestricted, IsMember: true}}
	p := &pending{gate: gateMute, nonce: "n", lang: i18n.LangEN, name: "Alice",
		deadline: time.Now().Add(time.Minute), challengeDelivered: true}
	v.pend[pkey{gid, uid}] = p
	t.Cleanup(v.stopForShutdown)

	v.onRecovery(context.Background(), fb, 10*time.Minute)

	v.mu.Lock()
	_, still := v.pend[pkey{gid, uid}]
	v.mu.Unlock()
	if !still {
		t.Fatal("recovery dropped a held member's verification; they would stay muted forever")
	}
	if fb.sends == 0 {
		t.Error("a held member must be re-notified after an outage like anyone else")
	}
}

// An applicant who really did get in while the bot was offline is still dropped.
func TestRecoveryStillDropsAdmittedApplicants(t *testing.T) {
	v := newTestService(&config.Config{})
	v.botUsername = "bot"
	gid, uid := int64(-100), int64(43)
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	p := &pending{nonce: "n", lang: i18n.LangEN, name: "Bob", deadline: time.Now().Add(time.Minute)}
	v.pend[pkey{gid, uid}] = p
	t.Cleanup(v.stopForShutdown)

	v.onRecovery(context.Background(), fb, 10*time.Minute)

	v.mu.Lock()
	_, still := v.pend[pkey{gid, uid}]
	v.mu.Unlock()
	if still {
		t.Error("an applicant an administrator admitted during the outage has nothing left to verify")
	}
}
