package verify

import (
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/mymmrac/telego"
)

// Telegram redelivered eight join requests in one day, each about five seconds after the first.
// Each repeat posted a second challenge and left the first to be deleted, so a delete that failed
// left an orphan challenge in the group that nobody could answer.
func TestRedeliveredJoinRequestKeepsTheChallengeOnScreen(t *testing.T) {
	const (
		groupID int64 = -1009000000920
		userID  int64 = 920
	)
	v := newTestService(&config.Config{
		Groups:         []config.GroupConfig{{ID: groupID}},
		GroupIDs:       []int64{groupID},
		Lang:           "en",
		VerifyMode:     config.ModeKernel,
		TimeoutSeconds: 240,
		DeliveryMode:   config.DeliveryBoth,
	})
	bot := newFakeVerifyBot()
	update := telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: groupID},
		From: telego.User{ID: userID, FirstName: "Applicant"},
	}}

	runFakeHandler(t, newAPITestBot(t, bot), v.OnJoinRequest, update)
	firstSends, firstDeletes := bot.sends, bot.deletes
	if firstSends == 0 {
		t.Fatal("the first join request posted no challenge")
	}

	runFakeHandler(t, newAPITestBot(t, bot), v.OnJoinRequest, update)
	if bot.sends != firstSends {
		t.Errorf("a redelivered request posted %d more message(s); the challenge already on screen stands",
			bot.sends-firstSends)
	}
	if bot.deletes != firstDeletes {
		t.Errorf("a redelivered request deleted %d message(s); nothing needed replacing",
			bot.deletes-firstDeletes)
	}
}

// A genuine re-application is not a redelivery, and must still get a fresh challenge.
func TestReapplicationAfterTheWindowStillReplacesTheChallenge(t *testing.T) {
	const (
		groupID int64 = -1009000000921
		userID  int64 = 921
	)
	v := newTestService(&config.Config{
		Groups:         []config.GroupConfig{{ID: groupID}},
		GroupIDs:       []int64{groupID},
		Lang:           "en",
		VerifyMode:     config.ModeKernel,
		TimeoutSeconds: 240,
		DeliveryMode:   config.DeliveryBoth,
	})
	bot := newFakeVerifyBot()
	update := telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: groupID},
		From: telego.User{ID: userID, FirstName: "Applicant"},
	}}

	runFakeHandler(t, newAPITestBot(t, bot), v.OnJoinRequest, update)
	firstSends := bot.sends

	// Age the pending past the window the redelivery guard covers.
	v.mu.Lock()
	if p, ok := v.pend[pkey{groupID, userID}]; ok {
		p.startedAt = p.startedAt.Add(-duplicateArrivalWindow - time.Second)
	}
	v.mu.Unlock()

	runFakeHandler(t, newAPITestBot(t, bot), v.OnJoinRequest, update)
	if bot.sends <= firstSends {
		t.Error("a re-application after the window posted no new challenge")
	}
	if bot.deletes == 0 {
		t.Error("a re-application after the window left the superseded challenge on screen")
	}
}
