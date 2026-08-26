package verify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/mymmrac/telego"
)

func joinUpdate(gid, uid int64, chatType string, old telego.ChatMember) telego.Update {
	if old == nil {
		old = &telego.ChatMemberLeft{Status: telego.MemberStatusLeft}
	}
	return telego.Update{ChatMember: &telego.ChatMemberUpdated{
		Chat:          telego.Chat{ID: gid, Type: chatType},
		From:          telego.User{ID: uid},
		OldChatMember: old,
		NewChatMember: &telego.ChatMemberMember{Status: telego.MemberStatusMember, User: telego.User{ID: uid, LanguageCode: "en"}},
	}}
}

// joinedNow must fire for an arrival and stay silent for every other membership change.
func TestJoinedNow(t *testing.T) {
	member := &telego.ChatMemberMember{Status: telego.MemberStatusMember}
	cases := []struct {
		name string
		old  telego.ChatMember
		new  telego.ChatMember
		want bool
	}{
		{name: "joined from outside", old: &telego.ChatMemberLeft{Status: telego.MemberStatusLeft}, new: member, want: true},
		{name: "joined after a ban expired", old: &telego.ChatMemberBanned{Status: telego.MemberStatusBanned}, new: member, want: true},
		{name: "hold lifted", old: &telego.ChatMemberRestricted{Status: telego.MemberStatusRestricted, IsMember: true}, new: member, want: false},
		{name: "demoted from administrator", old: &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}, new: member, want: false},
		{name: "left the group", old: member, new: &telego.ChatMemberLeft{Status: telego.MemberStatusLeft}, want: false},
		{name: "promoted", old: member, new: &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := joinedNow(&telego.ChatMemberUpdated{OldChatMember: tc.old, NewChatMember: tc.new})
			if got != tc.want {
				t.Errorf("joinedNow = %v, want %v", got, tc.want)
			}
		})
	}
}

// Someone the bot itself just let in must not be challenged a second time by the membership
// update that its own approval produced.
func TestApprovedApplicantIsNotChallengedAgainOnJoining(t *testing.T) {
	v := newTestService(&config.Config{})
	gid, uid := int64(-100), int64(5)
	v.notePassed(gid, uid)
	if !v.recentlyPassed(gid, uid) {
		t.Fatal("a verification that just passed must be remembered")
	}
	if v.recentlyPassed(gid, 6) {
		t.Error("the memory is per applicant")
	}
	if v.recentlyPassed(-200, uid) {
		t.Error("the memory is per group")
	}
	v.mu.Lock()
	v.passed[pkey{gid, uid}] = v.wallNow().Add(-recentPassWindow - time.Minute)
	v.mu.Unlock()
	if v.recentlyPassed(gid, uid) {
		t.Error("a stale pass must not suppress a genuinely new arrival")
	}
}

// Passing lifts the hold instead of approving a join request that does not exist.
func TestHeldMemberIsReleasedOnPass(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{}
	gid, uid := int64(-100), int64(7)
	p := &pending{gate: gateMute, nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{gid, uid}] = p

	if got := v.executeApprove(context.Background(), fb, gid, uid, p); got != approveConfirmed {
		t.Fatalf("approve outcome = %v, want approveConfirmed", got)
	}
	if fb.unmutes != 1 {
		t.Errorf("unmutes = %d, want 1", fb.unmutes)
	}
	if fb.approves != 0 {
		t.Errorf("approveChatJoinRequest calls = %d, want 0: there is no join request to approve", fb.approves)
	}
}

// Failing removes the member without keeping them out.
func TestHeldMemberIsRemovedOnFailure(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{}
	gid, uid := int64(-100), int64(8)
	p := &pending{gate: gateMute, nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{gid, uid}] = p

	outcome, _ := v.finishDecline(context.Background(), fb, gid, uid, p, wrongAnswerReason)
	if outcome != declineConfirmed {
		t.Fatalf("outcome = %v, want declineConfirmed", outcome)
	}
	if fb.bans != 1 || fb.unbans != 1 {
		t.Errorf("bans = %d unbans = %d, want 1 and 1: removal must not leave them banned", fb.bans, fb.unbans)
	}
	if fb.declines != 0 {
		t.Errorf("declineChatJoinRequest calls = %d, want 0", fb.declines)
	}
}

// The applicant-facing wording follows the gate: a member standing in the group is never told
// their join request was declined.
func TestHeldWordingDiffersFromRequestWording(t *testing.T) {
	v := newTestService(&config.Config{})
	request := v.wrongAnswerText(-100, i18n.LangEN, gateRequest, false)
	held := v.wrongAnswerText(-100, i18n.LangEN, gateMute, false)
	if request == held {
		t.Fatal("a held member and an applicant must not be given the same sentence")
	}
	if v.voice(gateMute).Passed.For(i18n.LangEN) == v.voice(gateRequest).Passed.For(i18n.LangEN) {
		t.Error("passing a hold and passing a join request are different events")
	}
}

// A basic group cannot restrict anyone, so no hold is attempted there.
func TestBasicGroupIsNotHeld(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{}
	v.holdMember(context.Background(), fb, -100, 5, false)
	if fb.mutes != 0 {
		t.Errorf("mutes = %d, want 0: Telegram only restricts members of supergroups", fb.mutes)
	}
	v.holdMember(context.Background(), fb, -100, 5, true)
	if fb.mutes != 1 {
		t.Errorf("mutes = %d, want 1", fb.mutes)
	}
}

// A chat the bot does not guard, and a bot account joining, are both left alone.
func TestPostJoinIgnoresWhatItShouldNotVerify(t *testing.T) {
	cases := []struct {
		name   string
		update telego.Update
	}{
		{name: "unguarded chat", update: joinUpdate(-999, 5, telego.ChatTypeSupergroup, nil)},
		{name: "another bot joining", update: botJoinUpdate(-100, 6)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestService(&config.Config{GroupIDs: []int64{-100}})
			fb := &fakeVerifyBot{}
			runFakeHandler(t, newAPITestBot(t, fb), v.OnMemberJoined, tc.update)
			if fb.mutes != 0 || fb.sends != 0 {
				t.Errorf("mutes = %d sends = %d, want 0 and 0", fb.mutes, fb.sends)
			}
			v.mu.Lock()
			pending := len(v.pend)
			v.mu.Unlock()
			if pending != 0 {
				t.Errorf("pending verifications = %d, want 0", pending)
			}
		})
	}
}

func botJoinUpdate(gid, uid int64) telego.Update {
	update := joinUpdate(gid, uid, telego.ChatTypeSupergroup, nil)
	update.ChatMember.NewChatMember = &telego.ChatMemberMember{
		Status: telego.MemberStatusMember,
		User:   telego.User{ID: uid, IsBot: true},
	}
	return update
}

// Someone brought in by another member still verifies, but the group notice says so and points
// administrators at the button that vouches for them.
func TestInvitedMemberGetsItsOwnNotice(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{}
	gid, uid := int64(-100), int64(9)
	v.botUsername = "bot"

	invited := challengeVoice{gate: gateMute, invited: true}
	arrived := challengeVoice{gate: gateMute}
	applying := challengeVoice{gate: gateRequest}

	texts := map[string]string{}
	for name, voice := range map[string]challengeVoice{"invited": invited, "arrived": arrived, "applying": applying} {
		fb.lastSendText = ""
		v.postGroupChallenge(context.Background(), fb, gid, uid, "Alice", i18n.LangEN, voice)
		texts[name] = fb.lastSendText
	}
	if texts["invited"] == texts["arrived"] {
		t.Error("an invited member and one who arrived alone must not get the same notice")
	}
	if texts["arrived"] == texts["applying"] {
		t.Error("a member and an applicant must not get the same notice")
	}
	if !strings.Contains(texts["invited"], "invited") {
		t.Errorf("the invited notice must say so, got %q", texts["invited"])
	}
	release := i18n.Messages.Verification.Admin.ReleaseButton.For(i18n.LangEN)
	if !strings.Contains(texts["invited"], release) {
		t.Errorf("the invited notice must point at %q, got %q", release, texts["invited"])
	}
}

// Being added by somebody else is what marks a member as invited; walking in alone is not.
func TestInvitedIsDecidedByWhoActed(t *testing.T) {
	joiner := joinUpdate(-100, 5, telego.ChatTypeSupergroup, nil)
	if joiner.ChatMember.From.ID != 5 {
		t.Fatal("a member who joins alone acts on their own behalf")
	}
	added := joinUpdate(-100, 5, telego.ChatTypeSupergroup, nil)
	added.ChatMember.From = telego.User{ID: 42}
	if added.ChatMember.From.ID == added.ChatMember.NewChatMember.MemberUser().ID {
		t.Fatal("fixture error: the actor must differ from the member")
	}
}
