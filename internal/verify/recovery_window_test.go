package verify

import (
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// After an outage the applicant applied hours ago. Re-posting the ordinary challenge told them
// they had 240 seconds, which is both the wrong window and the wrong thing to say.
func TestRecoveredChallengeSaysSoAndStatesTheRealWindow(t *testing.T) {
	const gid int64 = -1009000000940
	v := newTestService(&config.Config{
		Groups: []config.GroupConfig{{ID: gid}}, GroupIDs: []int64{gid},
		Lang: "zh", VerifyMode: config.ModeKernel, TimeoutSeconds: 240,
	})
	now := time.Now()
	v.timeNow = func() time.Time { return now }

	ordinary := &pending{gate: gateRequest, deadline: now.Add(v.timeout(gid))}
	if voice := v.voiceFor(gid, ordinary); voice.recovered {
		t.Errorf("an ordinary pending was treated as recovered: %+v", voice)
	}

	recovered := &pending{gate: gateRequest, deadline: now.Add(recoveryWindow)}
	voice := v.voiceFor(gid, recovered)
	if !voice.recovered {
		t.Fatal("a pending carrying the recovery window was not treated as recovered")
	}
	if voice.window < recoveryWindow-time.Minute {
		t.Errorf("recovered window = %v, want about %v", voice.window, recoveryWindow)
	}

	body := i18n.Messages.Verification.Group.BodyRecovered.Render(
		i18n.LangZH, "someone", "", windowText(&i18n.Messages, i18n.LangZH, voice.window), "")
	if !strings.Contains(body, "离线") {
		t.Errorf("the recovered challenge does not say the bot was offline: %q", body)
	}
	if strings.Contains(body, "240") || strings.Contains(body, "秒") {
		t.Errorf("the recovered challenge still counts in seconds: %q", body)
	}
	if !strings.Contains(body, "24 小时") {
		t.Errorf("the recovered challenge does not state the real window: %q", body)
	}
}

func TestWindowTextPicksAReadableUnit(t *testing.T) {
	for _, tt := range []struct {
		d    time.Duration
		want string
	}{
		{d: 24 * time.Hour, want: "24 小时"},
		{d: 30 * time.Minute, want: "30 分钟"},
		{d: 45 * time.Second, want: "45 秒"},
	} {
		if got := windowText(&i18n.Messages, i18n.LangZH, tt.d); got != tt.want {
			t.Errorf("windowText(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
