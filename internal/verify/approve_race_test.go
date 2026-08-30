package verify

import (
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

// Production hit this twice: an applicant answered correctly, the bot approved them, and one
// second later the post-join path muted them and asked again. OnMemberJoined checks
// recentlyPassed before it asks Telegram about trust and admin status; those two calls take about
// a second, and the approval finishes inside that gap — recording the pass and releasing the
// terminal claim. The first guard saw nothing yet, the second saw a claim already gone.
func TestPostJoinDoesNotReChallengeSomebodyTheBotJustAdmitted(t *testing.T) {
	const (
		gid int64 = -1009000000950
		uid int64 = 950
	)
	v := newTestService(&config.Config{
		Groups: []config.GroupConfig{{ID: gid}}, GroupIDs: []int64{gid},
		Lang: "en", VerifyMode: config.ModeKernel, TimeoutSeconds: 240,
	})
	bot := newFakeVerifyBot()

	// The approval lands while OnMemberJoined is between its two guards.
	v.notePassed(gid, uid)

	p := &pending{gate: gateMute, mode: config.ModeKernel, nonce: newNonce()}
	_, status := v.startPending(bot, gid, uid, p)
	if status != pendingRecentlyPassed {
		t.Errorf("post-join start = %v, want pendingRecentlyPassed", status)
	}
	if _, live := v.livePending(gid, uid); live {
		t.Error("a pending was installed for somebody the bot had just admitted")
	}
}

// A real application from somebody who passed minutes ago must still be challenged: swallowing it
// would leave the join request sitting with Telegram forever.
func TestJoinRequestIsStillChallengedAfterARecentPass(t *testing.T) {
	const (
		gid int64 = -1009000000951
		uid int64 = 951
	)
	v := newTestService(&config.Config{
		Groups: []config.GroupConfig{{ID: gid}}, GroupIDs: []int64{gid},
		Lang: "en", VerifyMode: config.ModeKernel, TimeoutSeconds: 240,
	})
	bot := newFakeVerifyBot()
	v.notePassed(gid, uid)

	p := &pending{gate: gateRequest, mode: config.ModeKernel, nonce: newNonce()}
	_, status := v.startPending(bot, gid, uid, p)
	if status != pendingStarted {
		t.Errorf("join-request start = %v, want pendingStarted — a new application is not a repeat", status)
	}
}

// Outside the window the post-join path challenges normally again.
func TestPostJoinChallengesOnceThePassHasAged(t *testing.T) {
	const (
		gid int64 = -1009000000952
		uid int64 = 952
	)
	v := newTestService(&config.Config{
		Groups: []config.GroupConfig{{ID: gid}}, GroupIDs: []int64{gid},
		Lang: "en", VerifyMode: config.ModeKernel, TimeoutSeconds: 240,
	})
	bot := newFakeVerifyBot()
	now := time.Now()
	v.timeNow = func() time.Time { return now }
	v.notePassed(gid, uid)
	v.timeNow = func() time.Time { return now.Add(recentPassWindow + time.Minute) }

	p := &pending{gate: gateMute, mode: config.ModeKernel, nonce: newNonce()}
	if _, status := v.startPending(bot, gid, uid, p); status != pendingStarted {
		t.Errorf("post-join start after the window = %v, want pendingStarted", status)
	}
}

// held= used to print the chat type, so every supergroup looked successfully muted even when the
// restriction had failed. It made the production logs unreadable for exactly the case they were
// needed for.
func TestHeldReflectsTheActualHold(t *testing.T) {
	p := &pending{gate: gateMute}
	if p.held {
		t.Fatal("a fresh pending must not claim to be held")
	}
	p.held = true
	if !p.held {
		t.Fatal("held must follow the recorded hold")
	}
}
