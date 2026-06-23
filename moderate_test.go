package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

// fakeModBot extends fakeVerifyBot with the wider modBot surface (admin lookup, mute/unmute, kick)
// so the admin gate and the moderation actions can be exercised without a real Telegram connection.
type fakeModBot struct {
	*fakeVerifyBot
	member      telego.ChatMember           // default GetChatMember result
	memberByID  map[int64]telego.ChatMember // per-user override (so a test can give caller vs target different statuses)
	memberErr   error
	chat        *telego.ChatFullInfo // GetChat result
	chatErr     error
	restrictErr error
	unbanErr    error
	restricts   int
	unbans      int
	answers     int
}

func newFakeMod() *fakeModBot { return &fakeModBot{fakeVerifyBot: &fakeVerifyBot{}} }

func (b *fakeModBot) GetChatMember(_ context.Context, p *telego.GetChatMemberParams) (telego.ChatMember, error) {
	if b.memberErr != nil {
		return nil, b.memberErr
	}
	if b.memberByID != nil {
		if m, ok := b.memberByID[p.UserID]; ok {
			return m, nil
		}
	}
	return b.member, nil
}
func (b *fakeModBot) GetChat(context.Context, *telego.GetChatParams) (*telego.ChatFullInfo, error) {
	return b.chat, b.chatErr
}
func (b *fakeModBot) RestrictChatMember(context.Context, *telego.RestrictChatMemberParams) error {
	b.restricts++
	return b.restrictErr
}
func (b *fakeModBot) UnbanChatMember(context.Context, *telego.UnbanChatMemberParams) error {
	b.unbans++
	return b.unbanErr
}
func (b *fakeModBot) AnswerCallbackQuery(context.Context, *telego.AnswerCallbackQueryParams) error {
	b.answers++
	return nil
}

func modTestV() *Verifier {
	return &Verifier{cfg: &Config{NotifyTTLSeconds: -1}, adminCache: map[pkey]time.Time{}}
}

// TestMissingModRights covers the startup rights-preflight classifier: a fully-privileged admin
// (and the owner) are missing nothing; an admin lacking a right is reported; a rights-less admin is
// missing all three (approve / ban / delete).
func TestMissingModRights(t *testing.T) {
	if m := missingModRights(&telego.ChatMemberAdministrator{CanInviteUsers: true, CanRestrictMembers: true, CanDeleteMessages: true}); len(m) != 0 {
		t.Errorf("a fully-privileged admin should be missing nothing, got %v", m)
	}
	if m := missingModRights(&telego.ChatMemberAdministrator{}); len(m) != 3 {
		t.Errorf("an admin with no rights should be missing all 3, got %v", m)
	}
	if m := missingModRights(&telego.ChatMemberAdministrator{CanInviteUsers: true, CanDeleteMessages: true}); len(m) != 1 {
		t.Errorf("an admin missing only can_restrict_members should report 1, got %v", m)
	}
	if m := missingModRights(&telego.ChatMemberOwner{}); len(m) != 0 {
		t.Errorf("the owner implicitly has all rights — should be missing nothing, got %v", m)
	}
}

// TestAdminStatus: an admin is confirmed AND cached; a non-admin is NOT cached (so a promotion takes
// effect at once); a GetChatMember error surfaces as (false, err) so callers can fail-closed.
func TestAdminStatus(t *testing.T) {
	ctx := context.Background()

	v := modTestV()
	mb := newFakeMod()
	mb.member = &telego.ChatMemberAdministrator{}
	if ok, err := v.adminStatus(ctx, mb, -100, 1); !ok || err != nil {
		t.Fatalf("an administrator should be admin: ok=%v err=%v", ok, err)
	}
	if _, cached := v.adminCache[pkey{-100, 1}]; !cached {
		t.Error("a confirmed admin should be cached")
	}

	v2 := modTestV()
	mb2 := newFakeMod()
	mb2.member = &telego.ChatMemberMember{}
	if ok, err := v2.adminStatus(ctx, mb2, -100, 2); ok || err != nil {
		t.Fatalf("a plain member should not be admin: ok=%v err=%v", ok, err)
	}
	if _, cached := v2.adminCache[pkey{-100, 2}]; cached {
		t.Error("a non-admin must NOT be cached (so a fresh promotion is honoured immediately)")
	}

	v3 := modTestV()
	mb3 := newFakeMod()
	mb3.memberErr = errors.New("network")
	if ok, err := v3.adminStatus(ctx, mb3, -100, 3); ok || err == nil {
		t.Fatalf("a GetChatMember error must surface as (false, err): ok=%v err=%v", ok, err)
	}
}

// TestIsGroupAdminFailsClosed: the invoker gate allows an admin, denies a non-admin, and — crucially
// — DENIES on a GetChatMember error (fail-closed) rather than letting an action through.
func TestIsGroupAdminFailsClosed(t *testing.T) {
	ctx := context.Background()
	admin := newFakeMod()
	admin.member = &telego.ChatMemberAdministrator{}
	if !modTestV().isGroupAdmin(ctx, admin, -100, 1) {
		t.Error("an administrator must pass the gate")
	}
	member := newFakeMod()
	member.member = &telego.ChatMemberMember{}
	if modTestV().isGroupAdmin(ctx, member, -100, 2) {
		t.Error("a non-admin must NOT pass the gate")
	}
	errBot := newFakeMod()
	errBot.memberErr = errors.New("network")
	if modTestV().isGroupAdmin(ctx, errBot, -100, 3) {
		t.Error("a GetChatMember error must DENY (fail-closed), never allow")
	}
}

// TestWarnPrecheckGate is the access-control core: a non-admin caller is denied (nil) with NO
// ban/restrict issued; an admin caller against a non-admin target resolves the target; an admin
// target is skipped (admins can't be warned/banned via these commands).
func TestWarnPrecheckGate(t *testing.T) {
	ctx := context.Background()
	const gid = int64(-100)
	caller, target := int64(7), int64(8)
	msg := func() *telego.Message {
		return &telego.Message{
			Chat:           telego.Chat{ID: gid},
			From:           &telego.User{ID: caller},
			ReplyToMessage: &telego.Message{From: &telego.User{ID: target}},
		}
	}

	// non-admin caller -> denied, and the deny path issues NO moderation action.
	deny := newFakeMod()
	deny.memberByID = map[int64]telego.ChatMember{caller: &telego.ChatMemberMember{}}
	if got := modTestV().warnPrecheck(ctx, deny, msg(), "/warn", true); got != nil {
		t.Error("a non-admin caller must be denied (nil target)")
	}
	if deny.bans != 0 || deny.restricts != 0 || deny.unbans != 0 {
		t.Errorf("the deny path must issue NO ban/restrict/unban, got bans=%d restricts=%d unbans=%d", deny.bans, deny.restricts, deny.unbans)
	}

	// admin caller, non-admin target -> resolves the target.
	ok := newFakeMod()
	ok.memberByID = map[int64]telego.ChatMember{caller: &telego.ChatMemberAdministrator{}, target: &telego.ChatMemberMember{}}
	if got := modTestV().warnPrecheck(ctx, ok, msg(), "/warn", true); got == nil || got.ID != target {
		t.Errorf("admin caller + non-admin target should resolve the target, got %v", got)
	}

	// admin caller, ADMIN target -> skipped (nil), no action.
	skip := newFakeMod()
	skip.memberByID = map[int64]telego.ChatMember{caller: &telego.ChatMemberAdministrator{}, target: &telego.ChatMemberAdministrator{}}
	if got := modTestV().warnPrecheck(ctx, skip, msg(), "/warn", true); got != nil {
		t.Error("an admin target must be skipped (can't warn/ban an admin)")
	}
	if skip.bans != 0 || skip.restricts != 0 {
		t.Error("skipping an admin target must issue no action")
	}
}

// TestApplyMuteUnmute: mute restricts; unmute restores the GROUP default when GetChat succeeds and
// falls back to a permissive set (restoredDefault=false) but STILL lifts the mute when GetChat fails.
func TestApplyMuteUnmute(t *testing.T) {
	ctx := context.Background()
	v := &Verifier{cfg: &Config{}}

	mute := newFakeMod()
	if err := v.applyMute(ctx, mute, -100, 5, 3600); err != nil || mute.restricts != 1 {
		t.Fatalf("applyMute should restrict once with no error: err=%v restricts=%d", err, mute.restricts)
	}
	muteErr := newFakeMod()
	muteErr.restrictErr = errors.New("no rights")
	if err := v.applyMute(ctx, muteErr, -100, 5, 3600); err == nil {
		t.Error("applyMute must surface a RestrictChatMember error so the handler reports failure")
	}

	withDefaults := newFakeMod()
	withDefaults.chat = &telego.ChatFullInfo{Permissions: &telego.ChatPermissions{CanSendMessages: telego.ToPtr(true)}}
	if restored, err := v.applyUnmute(ctx, withDefaults, -100, 5); !restored || err != nil || withDefaults.restricts != 1 {
		t.Fatalf("with group defaults: restoredDefault=true, no err, one restrict: restored=%v err=%v restricts=%d", restored, err, withDefaults.restricts)
	}
	noChat := newFakeMod()
	noChat.chatErr = errors.New("no access")
	if restored, err := v.applyUnmute(ctx, noChat, -100, 5); restored || err != nil || noChat.restricts != 1 {
		t.Fatalf("on GetChat failure: restoredDefault=false, still lifts the mute: restored=%v err=%v restricts=%d", restored, err, noChat.restricts)
	}
}

// TestWarnKick covers the warn-limit auto-kick: a clean ban+unban is rejoinable; a failed ban returns
// an error and attempts no unban; a stuck ban (unban fails) is not-rejoinable but not an error.
func TestWarnKick(t *testing.T) {
	ctx := context.Background()
	v := &Verifier{cfg: &Config{}}

	ok := newFakeMod()
	if rejoinable, err := v.warnKick(ctx, ok, -100, 5); !rejoinable || err != nil {
		t.Fatalf("a clean kick should be rejoinable, no err: rejoinable=%v err=%v", rejoinable, err)
	}
	if ok.bans != 1 || ok.unbans != 1 {
		t.Errorf("warnKick must ban then unban once each, got bans=%d unbans=%d", ok.bans, ok.unbans)
	}

	banFail := newFakeMod()
	banFail.banErr = errors.New("no rights")
	if rejoinable, err := v.warnKick(ctx, banFail, -100, 5); rejoinable || err == nil {
		t.Fatalf("a failed ban must return (false, err): rejoinable=%v err=%v", rejoinable, err)
	}
	if banFail.unbans != 0 {
		t.Error("no unban should be attempted when the ban itself failed")
	}

	stuck := newFakeMod()
	stuck.unbanErr = errors.New("unban failed")
	if rejoinable, err := v.warnKick(ctx, stuck, -100, 5); rejoinable || err != nil {
		t.Fatalf("a stuck ban (unban failed) must be not-rejoinable but no err: rejoinable=%v err=%v", rejoinable, err)
	}
}
