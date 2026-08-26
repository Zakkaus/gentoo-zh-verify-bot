package verify

import (
	"testing"

	"github.com/mymmrac/telego"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

func memberDM(uid int64, text string) telego.Update {
	return telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: uid, Type: telego.ChatTypePrivate},
		From: &telego.User{ID: uid, LanguageCode: "en"},
		Text: text,
	}}
}

// The whole path for somebody who joins a group that does not ask people to apply: held on
// arrival, released when they answer, and left alone by the membership update that release
// produces.
func TestPostJoinJourneyPasses(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	v.botUsername = "bot"
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	bot := newAPITestBot(t, fb)
	t.Cleanup(v.stopForShutdown)
	gid, uid := int64(-100), int64(5)

	runFakeHandler(t, bot, v.OnMemberJoined, joinUpdate(gid, uid, telego.ChatTypeSupergroup, nil))
	v.mu.Lock()
	p, live := v.pend[pkey{gid, uid}]
	held := live && p.held
	v.mu.Unlock()
	if !live || !held {
		t.Fatalf("arrival must start a held verification: live=%v held=%v", live, held)
	}
	if fb.mutes != 1 {
		t.Fatalf("mutes = %d, want 1", fb.mutes)
	}

	runFakeHandler(t, bot, v.OnKernelAnswer, memberDM(uid, "7.2.0-gentoo-cjk-zakk"))

	v.mu.Lock()
	_, stillPending := v.pend[pkey{gid, uid}]
	v.mu.Unlock()
	if stillPending {
		t.Error("a correct answer settles the verification")
	}
	if fb.unmutes != 1 {
		t.Errorf("unmutes = %d, want 1: passing lifts the hold", fb.unmutes)
	}
	if fb.bans != 0 {
		t.Errorf("bans = %d, want 0: they answered correctly", fb.bans)
	}
	if !v.recentlyPassed(gid, uid) {
		t.Fatal("the pass must be recorded before anything else can read the membership update")
	}
	// Lifting the hold produces its own membership update; it must not start a second round.
	before := fb.sends
	runFakeHandler(t, bot, v.OnMemberJoined, joinUpdate(gid, uid, telego.ChatTypeSupergroup,
		&telego.ChatMemberRestricted{Status: telego.MemberStatusRestricted, IsMember: true}))
	if fb.sends != before || fb.mutes != 1 {
		t.Errorf("the release produced another challenge: sends %d → %d, mutes = %d", before, fb.sends, fb.mutes)
	}
}

// The same path for somebody who never answers: the window runs out and they are removed, not
// left muted forever.
func TestPostJoinJourneyTimesOut(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}, TimeoutSeconds: 30})
	v.botUsername = "bot"
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	bot := newAPITestBot(t, fb)
	t.Cleanup(v.stopForShutdown)
	gid, uid := int64(-100), int64(6)

	runFakeHandler(t, bot, v.OnMemberJoined, joinUpdate(gid, uid, telego.ChatTypeSupergroup, nil))
	v.mu.Lock()
	p := v.pend[pkey{gid, uid}]
	nonce, epoch := p.nonce, p.epoch
	v.mu.Unlock()

	v.onExpiry(t.Context(), fb, gid, uid, nonce, epoch, "timeout")

	v.mu.Lock()
	_, stillPending := v.pend[pkey{gid, uid}]
	strikes := len(v.vfail)
	v.mu.Unlock()
	if stillPending {
		t.Error("an expired verification must settle, not linger")
	}
	if fb.bans != 1 || fb.unbans != 1 {
		t.Errorf("bans = %d unbans = %d, want 1 and 1: removed, not kept out", fb.bans, fb.unbans)
	}
	if strikes != 1 {
		t.Errorf("strike records = %d, want 1: ignoring the challenge is the member's own doing", strikes)
	}
	if !hasHeldWording(fb.sendTexts) {
		t.Errorf("the applicant must be told in the wording for a member, got %q", fb.sendTexts)
	}
}

func hasHeldWording(texts []string) bool {
	want := i18n.Messages.Verification.Held.TimeoutNoWait.For(i18n.LangEN)
	wantRetry := i18n.Messages.Verification.Held.TimeoutRetry.Render(i18n.LangEN, 180)
	for _, text := range texts {
		if text == want || text == wantRetry {
			return true
		}
	}
	return false
}
