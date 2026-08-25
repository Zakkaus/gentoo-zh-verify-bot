package verify

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/mymmrac/telego"
)

func failedStateWritePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "missing", name)
}

func captureStateWriteLog(t *testing.T, run func()) string {
	t.Helper()
	var output bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(oldWriter) })
	run()
	return output.String()
}

func requireStateWriteFailureLogged(t *testing.T, path, output string) {
	t.Helper()
	if !strings.Contains(output, "state: temp for "+path) {
		t.Fatalf("state write failure for %q was not logged: %q", path, output)
	}
}

func TestStateWriteFailuresKeepRuntimeStateButLoseRestartRecovery(t *testing.T) {
	const gid, uid = int64(-100), int64(5)

	t.Run("pending_verification", func(t *testing.T) {
		cfg := &config.Config{
			Groups:         []config.GroupConfig{{ID: gid}},
			GroupIDs:       []int64{gid},
			TimeoutSeconds: 240,
			VerifyMode:     config.ModeQuiz,
		}
		v := newTestService(cfg)
		group, ok := v.settings.Group(gid)
		if !ok {
			t.Fatal("test group is missing")
		}
		overrides := group.Overrides()
		dmFirst := false
		overrides.DMFirst = &dmFirst
		if _, err := v.settings.CommitGroup(gid, group.Revision(), overrides); err != nil {
			t.Fatal(err)
		}
		path := failedStateWritePath(t, "pending.json")
		v.statePath = path
		bot := newFakeVerifyBot()
		update := telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
			Chat: telego.Chat{ID: gid},
			From: telego.User{ID: uid, FirstName: "Applicant"},
		}}

		output := captureStateWriteLog(t, func() {
			runFakeHandler(t, newAPITestBot(t, bot), v.OnJoinRequest, update)
		})
		key := pkey{gid, uid}
		p := v.pend[key]
		if p == nil || !p.challengeDelivered || p.groupMsgID == 0 {
			t.Fatalf("runtime pending after failed save = %+v", p)
		}
		t.Cleanup(func() { p.timer.Stop() })
		if bot.sends != 1 || bot.lastSendChat != gid {
			t.Fatalf("pending write failure sends/chat = %d/%d, want only the group challenge", bot.sends, bot.lastSendChat)
		}
		requireStateWriteFailureLogged(t, path, output)

		fresh := newTestService(cfg)
		fresh.statePath = path
		fresh.load(newFakeVerifyBot())
		if _, restored := fresh.pend[key]; restored {
			t.Fatal("fresh service restored a pending whose state write failed")
		}
	})

	t.Run("verification_strikes", func(t *testing.T) {
		cfg := &config.Config{
			Groups:             []config.GroupConfig{{ID: gid}},
			GroupIDs:           []int64{gid},
			VerifyMaxFails:     3,
			VerifyRetrySeconds: 600,
		}
		v := newTestService(cfg)
		path := failedStateWritePath(t, "verifyfail.json")
		v.vfailPath = path
		key := pkey{gid, uid}
		v.pend[key] = &pending{nonce: "n", deadline: time.Now().Add(time.Hour)}
		bot := newFakeVerifyBot()

		output := captureStateWriteLog(t, func() {
			handled, settled, banned := v.decline(context.Background(), bot, gid, uid, "n", "timeout")
			if !handled || !settled || banned {
				t.Fatalf("runtime decline = handled:%v settled:%v banned:%v", handled, settled, banned)
			}
		})
		if remaining := v.verifyCooldownRemaining(gid, uid); remaining <= 0 {
			t.Fatalf("runtime strike no longer serves its cooldown: %v", remaining)
		}
		if bot.sends != 0 {
			t.Fatalf("strike write failure sent a Telegram operator alert: %d sends", bot.sends)
		}
		requireStateWriteFailureLogged(t, path, output)

		fresh := newTestService(cfg)
		fresh.vfailPath = path
		fresh.loadVerifyFails()
		if _, restored := fresh.vfail[key]; restored {
			t.Fatal("fresh service restored a strike whose state write failed")
		}
		if remaining := fresh.verifyCooldownRemaining(gid, uid); remaining != 0 {
			t.Fatalf("fresh service retained a failed-write cooldown: %v", remaining)
		}
	})

	t.Run("heartbeat", func(t *testing.T) {
		v := newTestService(&config.Config{})
		path := failedStateWritePath(t, "heartbeat.json")
		v.hbPath = path
		setOffline(v)
		bot := newFakeVerifyBot()

		output := captureStateWriteLog(t, func() {
			if !v.heartbeatTick(context.Background(), bot) {
				t.Fatal("successful heartbeat probe did not update runtime reachability")
			}
		})
		if v.offlineNow() {
			t.Fatal("successful heartbeat write failure left the running service offline")
		}
		if bot.sends != 0 {
			t.Fatalf("heartbeat write failure sent a Telegram operator alert: %d sends", bot.sends)
		}
		requireStateWriteFailureLogged(t, path, output)

		fresh := newTestService(&config.Config{})
		fresh.hbPath = path
		if restored := fresh.loadHeartbeat(); !restored.IsZero() {
			t.Fatalf("fresh service restored heartbeat from failed write: %v", restored)
		}
	})

	t.Run("agent_tally", func(t *testing.T) {
		v := newTestService(&config.Config{})
		path := failedStateWritePath(t, "agents.json")
		v.agentPath = path
		var model string
		var total int

		output := captureStateWriteLog(t, func() {
			model, total = v.recordAgent("AGENT-N model=gpt-5")
		})
		if model != "gpt-5" || total != 1 {
			t.Fatalf("runtime agent tally = (%q, %d), want (gpt-5, 1)", model, total)
		}
		wantStats := v.messages.Verification.Admin.AgentStats.Render(i18n.LangEN, 1, "gpt-5 1")
		if got := v.AgentStatsText(i18n.LangEN); got != wantStats {
			t.Fatalf("runtime agent stats = %q, want catalogue rendering %q", got, wantStats)
		}
		requireStateWriteFailureLogged(t, path, output)

		fresh := newTestService(&config.Config{})
		fresh.agentPath = path
		fresh.loadAgents()
		if fresh.agents.Total != 0 || fresh.AgentStatsText(i18n.LangEN) != "" {
			t.Fatalf("fresh service retained failed-write agent tally: %+v", fresh.agents)
		}
	})
}
