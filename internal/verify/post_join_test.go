package verify

import (
	"context"
	"errors"
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
	p := &pending{gate: gateMute, held: true, nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
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

// A basic group could never be held, so passing has nothing to lift. Trying anyway fails with
// "supergroups only" and, before this was fixed, that failure removed a member who answered
// correctly.
func TestPassingInAnUnheldGroupIsNotAFailure(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{unmuteErr: errors.New("Bad Request: method is available only for supergroups")}
	gid, uid := int64(-100), int64(12)
	p := &pending{gate: gateMute, nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{gid, uid}] = p
	t.Cleanup(v.stopForShutdown)

	if got := v.executeApprove(context.Background(), fb, gid, uid, p); got != approveConfirmed {
		t.Fatalf("approve outcome = %v, want approveConfirmed", got)
	}
	if fb.unmutes != 0 {
		t.Errorf("unmute calls = %d, want 0: nothing was ever held", fb.unmutes)
	}
	if fb.bans != 0 {
		t.Errorf("bans = %d, want 0: this member answered correctly", fb.bans)
	}
}

// A hold the bot genuinely cannot lift yet is retried as an admission, never settled as one.
func TestFailedReleaseRetriesTheAdmissionInsteadOfRemoving(t *testing.T) {
	v := newTestService(&config.Config{})
	fb := &fakeVerifyBot{unmuteErr: errors.New("network unreachable")}
	gid, uid := int64(-100), int64(13)
	p := &pending{gate: gateMute, held: true, nonce: "n", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{gid, uid}] = p
	v.markTerminalLocked(pkey{gid, uid}, p)
	p.done = true
	t.Cleanup(v.stopForShutdown)

	if got := v.executeApprove(context.Background(), fb, gid, uid, p); got != approveFailed {
		t.Fatalf("approve outcome = %v, want approveFailed", got)
	}
	v.mu.Lock()
	passing := p.passing
	v.mu.Unlock()
	if !passing {
		t.Fatal("a member who answered correctly must be recorded as passing, or the retry declines them")
	}

	// The re-armed timer settles: it must complete the admission, not remove them.
	fb.unmuteErr = nil
	v.onExpiry(context.Background(), fb, gid, uid, p.nonce, p.epoch, "approve-retry")
	if fb.bans != 0 {
		t.Errorf("bans = %d, want 0: retrying an admission must never remove the member", fb.bans)
	}
	if fb.unmutes == 0 {
		t.Error("the retry must try to lift the hold again")
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
	basic := &pending{gate: gateMute, nonce: "b"}
	v.pend[pkey{-100, 5}] = basic
	v.holdMember(context.Background(), fb, -100, 5, false, basic)
	if fb.mutes != 0 {
		t.Errorf("mutes = %d, want 0: Telegram only restricts members of supergroups", fb.mutes)
	}
	if basic.held {
		t.Error("a group that cannot be held must not be recorded as held")
	}
	super := &pending{gate: gateMute, nonce: "s"}
	v.pend[pkey{-200, 5}] = super
	v.holdMember(context.Background(), fb, -200, 5, true, super)
	if fb.mutes != 1 {
		t.Errorf("mutes = %d, want 1", fb.mutes)
	}
	if !super.held {
		t.Error("a placed hold must be recorded, or passing has nothing to lift")
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

// Someone already in the group is not watching for a challenge the way an applicant is, and the
// hold keeps them harmless, so the post-join window is longer by default. A group that chose its
// own timeout means what it chose.
func TestPostJoinWindowDefaultsLonger(t *testing.T) {
	def := newTestService(&config.Config{GroupIDs: []int64{-100}})
	if got := def.gateTimeout(-100, gateMute); got != postJoinTimeout {
		t.Errorf("post-join window = %v, want %v", got, postJoinTimeout)
	}
	if got := def.gateTimeout(-100, gateRequest); got == postJoinTimeout {
		t.Error("an applicant's window is unchanged by the post-join default")
	}

	chosen := newTestService(&config.Config{GroupIDs: []int64{-100}})
	group, _ := chosen.settings.Group(-100)
	overrides := group.Overrides()
	seconds := 300
	overrides.TimeoutSeconds = &seconds
	if _, err := chosen.settings.CommitGroup(-100, group.Revision(), overrides); err != nil {
		t.Fatal(err)
	}
	for _, gate := range []string{gateRequest, gateMute} {
		if got := chosen.gateTimeout(-100, gate); got != 300*time.Second {
			t.Errorf("gate %q window = %v, want the 300s an administrator chose", gate, got)
		}
	}
}

// Verifying invited members is on unless the group turns it off.
func TestVerifyInvitedDefaultsOn(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}})
	if !v.verifyInvited(-100) {
		t.Error("being vouched for is not verification; the check defaults on")
	}
	off := false
	v2 := newTestService(&config.Config{GroupIDs: []int64{-100}, VerifyInvited: &off})
	if v2.verifyInvited(-100) {
		t.Error("a group that switched it off must be honoured")
	}
}

// deleteProbe records whether a just-admitted applicant would be re-challenged at the moment the
// bot is still cleaning up after admitting them. Approving produces a membership update of its
// own, and handlers run concurrently, so that moment is reachable.
type deleteProbe struct {
	*fakeVerifyBot
	v        *Service
	gid, uid int64
	seen     []bool
}

func (b *deleteProbe) Delete(ctx context.Context, chatID int64, messageID int) {
	b.seen = append(b.seen, b.v.recentlyPassed(b.gid, b.uid))
	b.fakeVerifyBot.Delete(ctx, chatID, messageID)
}

// Admitting somebody must never leave a window in which the bot reads its own approval as a new
// arrival: it would mute the person it just let in and hand them another question.
func TestAdmissionLeavesNoWindowToRechallenge(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate string
	}{
		{name: "join request approved", gate: gateRequest},
		{name: "hold lifted", gate: gateMute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestService(&config.Config{})
			gid, uid := int64(-100), int64(21)
			probe := &deleteProbe{fakeVerifyBot: &fakeVerifyBot{}, v: v, gid: gid, uid: uid}
			p := &pending{gate: tc.gate, held: tc.gate == gateMute, nonce: "n", lang: i18n.LangEN,
				deadline: time.Now().Add(time.Hour), groupMsgID: 1, privateMsgID: 2}
			v.pend[pkey{gid, uid}] = p

			if got := v.executeApprove(context.Background(), probe, gid, uid, p); got != approveConfirmed {
				t.Fatalf("approve outcome = %v, want approveConfirmed", got)
			}
			if len(probe.seen) == 0 {
				t.Fatal("the cleanup that opens the window did not run")
			}
			for i, passed := range probe.seen {
				if !passed {
					t.Errorf("cleanup step %d ran while the admission was still invisible; a membership "+
						"update handled here would mute and re-question the person just admitted", i)
				}
			}
		})
	}
}

// Trusting a group means not asking its members anything. Somebody already inside the group has
// no join request to approve, so the bypass must not be expressed as an approval: that call
// fails, and the failure used to put a trusted member through the challenge anyway.
func TestTrustedMemberJoiningIsNotChallenged(t *testing.T) {
	cfg := &config.Config{GroupIDs: []int64{-100}, TrustedMemberGroupIDs: []int64{-200}}
	v := newTestService(cfg)
	v.botUsername = "bot"
	fb := &fakeVerifyBot{member: &telego.ChatMemberMember{Status: telego.MemberStatusMember}}
	t.Cleanup(v.stopForShutdown)

	runFakeHandler(t, newAPITestBot(t, fb), v.OnMemberJoined, joinUpdate(-100, 5, telego.ChatTypeSupergroup, nil))

	v.mu.Lock()
	pending := len(v.pend)
	v.mu.Unlock()
	if pending != 0 {
		t.Error("a member of a trusted group must not be given a challenge")
	}
	if fb.mutes != 0 {
		t.Errorf("mutes = %d, want 0: a trusted member is not held", fb.mutes)
	}
	if fb.approves != 0 {
		t.Errorf("approveChatJoinRequest calls = %d, want 0: there is no join request for a member", fb.approves)
	}
	if !v.recentlyPassed(-100, 5) {
		t.Error("a trusted member counts as verified, so the next membership update leaves them alone")
	}
}
