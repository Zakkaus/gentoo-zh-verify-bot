package panel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/verify"
	"github.com/mymmrac/telego"
)

func newAdminTestApplication(cfg *config.Config, settings *store.Settings, bot *telego.Bot) (*Panel, *verify.Service) {
	telegram := tg.New(bot)
	verification := verify.New(settings, telegram, cfg, &i18n.Messages, bot, verify.Identity{}, "")
	administration := New(
		settings, telegram, cfg, &i18n.Messages,
		verification, nil, nil, "", time.Time{},
	)
	return administration, verification
}

func TestStopCommandWritesInvokingGroup(t *testing.T) {
	const (
		groupA int64 = -1009000000301
		groupB int64 = -1009000000302
	)
	cfg := &config.Config{
		Groups:           []config.GroupConfig{{ID: groupA}, {ID: groupB}},
		GroupIDs:         []int64{groupA, groupB},
		ControlGroupID:   groupA,
		NotifyTTLSeconds: -1,
	}
	settings, err := store.NewSettings("", testSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeAdminBot()
	fake.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
	bot := newAPITestBot(t, fake)
	administration, verification := newAdminTestApplication(cfg, settings, bot)
	runFakeHandler(t, bot, administration.OnStop, telego.Update{Message: &telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: groupB, Type: "supergroup"},
		From:      &telego.User{ID: 7},
		Text:      "/stop",
	}})
	if !verification.IsEnabled(groupA) || verification.IsEnabled(groupB) {
		t.Fatalf("/stop state = group A:%v group B:%v, want true/false", verification.IsEnabled(groupA), verification.IsEnabled(groupB))
	}
}

func TestSettingsCommandReportsWriteFailure(t *testing.T) {
	cfg := runtimeSettingsTestConfig()
	cfg.NotifyTTLSeconds = -1
	settings, err := store.NewSettings(t.TempDir(), testSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeAdminBot()
	fake.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
	bot := newAPITestBot(t, fake)
	administration, verification := newAdminTestApplication(cfg, settings, bot)
	runFakeHandler(t, bot, administration.OnStop, telego.Update{Message: &telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: cfg.GroupIDs[0], Type: "supergroup"},
		From:      &telego.User{ID: 7},
		Text:      "/stop",
	}})
	if !strings.Contains(fake.lastSendText, "无法保存设置") {
		t.Fatalf("write failure notice = %q", fake.lastSendText)
	}
	if !verification.IsEnabled(cfg.GroupIDs[0]) {
		t.Fatal("failed settings write changed effective state")
	}
}

func TestSettingsBaselineProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"groups":[{"id":-1001,"verify_mode":"quiz","questions":[{"q":"Package manager?","options":["Portage","apt"],"answer":0}]}],
		"channel_whitelist":[],
		"lookup_ttl_seconds":0,
		"private_query_per_min":5
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := store.LoadBaseline(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.NewSettings("", baseline)
	if err != nil {
		t.Fatal(err)
	}
	group, _ := settings.Group(-1001)
	if got := group.VerifyMode(); got.Value != config.ModeQuiz || got.Source != store.SourceConfig {
		t.Fatalf("group verify mode provenance = %+v", got)
	}
	if got := group.ChannelWhitelist(); len(got.Value) != 0 || got.Source != store.SourceConfig {
		t.Fatalf("explicit empty whitelist provenance = %+v", got)
	}
	if got := group.LookupTTLSeconds(); got.Value != 0 || got.Source != store.SourceConfig {
		t.Fatalf("disabled lookup provenance = %+v", got)
	}
	if got := group.TimeoutSeconds(); got.Value != 240 || got.Source != store.SourceDefault {
		t.Fatalf("default timeout provenance = %+v", got)
	}
	if got := settings.Global().PrivateQueryPerMin(); got.Value != 5 || got.Source != store.SourceConfig {
		t.Fatalf("global query-rate provenance = %+v", got)
	}
}

func testSettingsBaseline(t *testing.T, cfg *config.Config) store.SettingsBaseline {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := store.LoadBaseline(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return baseline
}

func runtimeSettingsTestConfig() *config.Config {
	const groupID int64 = -1009000000001
	return &config.Config{
		Groups:             []config.GroupConfig{{ID: groupID}},
		GroupIDs:           []int64{groupID},
		TimeoutSeconds:     240,
		VerifyMaxFails:     3,
		VerifyRetrySeconds: 180,
		PrivateQueryPerMin: 3,
		LookupTTLSeconds:   intPointer(180),
	}
}

func intPointer(value int) *int { return &value }
