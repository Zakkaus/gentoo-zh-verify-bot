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
	th "github.com/mymmrac/telego/telegohandler"
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

func TestRuntimeRegisteredGroupUsesLiveCommandGuards(t *testing.T) {
	const groupID int64 = -1009000000303
	cfg := &config.Config{Lang: "en", NotifyTTLSeconds: -1}
	settings, err := store.NewSettings(filepath.Join(t.TempDir(), "settings.json"), testSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeAdminBot()
	fake.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
	bot := newAPITestBot(t, fake)
	administration, verification := newAdminTestApplication(cfg, settings, bot)
	defer verification.Shutdown()
	registration := settings.Registrations()
	registration.RegisteredGroups = []store.RegisteredGroup{{ID: groupID, RegisteredBy: 42}}
	if _, err := settings.CommitRegistrations(registration.Revision, registration); err != nil {
		t.Fatal(err)
	}
	message := &telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 7, LanguageCode: "fr"},
	}
	if !verification.DMOrGroup(message) {
		t.Error("runtime group was rejected by the shared message guard")
	}

	fake.lastSendText = ""
	runFakeHandler(t, bot, administration.OnHelp, telego.Update{Message: message})
	groupState := i18n.Messages.Panel.Help.GroupState.Render(
		i18n.LangEN, i18n.Messages.Panel.State.Enabled.For(i18n.LangEN))
	if !strings.Contains(fake.lastSendText, groupState) {
		t.Errorf("runtime group help = %q, want catalogue group state %q", fake.lastSendText, groupState)
	}

	const memberResult = "runtime member command handled"
	fake.lastSendText = ""
	runFakeHandler(t, bot, func(ctx *th.Context, update telego.Update) error {
		return administration.memberCmd(ctx, update, func(int64, i18n.Lang) string {
			return memberResult
		})
	}, telego.Update{Message: message})
	if fake.lastSendText != memberResult {
		t.Errorf("runtime member command result = %q, want %q", fake.lastSendText, memberResult)
	}

	runFakeHandler(t, bot, administration.OnStop, telego.Update{Message: message})
	if verification.IsEnabled(groupID) {
		t.Error("runtime group settings command did not change verification state")
	}
}

func TestRuntimeControlGroupRestrictsGlobalCommands(t *testing.T) {
	const (
		controlGroup = int64(-1009000000304)
		otherGroup   = int64(-1009000000305)
	)
	cfg := &config.Config{Lang: "en", NotifyTTLSeconds: -1}
	settings, err := store.NewSettings(filepath.Join(t.TempDir(), "settings.json"), testSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeAdminBot()
	fake.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
	bot := newAPITestBot(t, fake)
	administration, verification := newAdminTestApplication(cfg, settings, bot)
	defer verification.Shutdown()
	registration := settings.Registrations()
	registration.ControlGroupID = controlGroup
	registration.RegisteredGroups = []store.RegisteredGroup{
		{ID: controlGroup, RegisteredBy: 42},
		{ID: otherGroup, RegisteredBy: 42},
	}
	if _, err := settings.CommitRegistrations(registration.Revision, registration); err != nil {
		t.Fatal(err)
	}
	runFakeHandler(t, bot, administration.OnRich, telego.Update{Message: &telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: otherGroup, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 7, LanguageCode: "en"},
	}})
	if settings.Global().RichMessages().Value {
		t.Error("non-control runtime group changed a global setting")
	}
	want := i18n.Messages.Feed.Config.ControlGroupOnly.Render(i18n.LangEN, controlGroup)
	if fake.lastSendText != want {
		t.Errorf("control-group refusal = %q, want catalogue text %q", fake.lastSendText, want)
	}
}

func TestHelpUsesLivePrivateQueryRate(t *testing.T) {
	cfg := &config.Config{PrivateQueryPerMin: 3}
	settings, err := store.NewSettings("", testSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeAdminBot()
	bot := newAPITestBot(t, fake)
	administration, verification := newAdminTestApplication(cfg, settings, bot)
	defer verification.Shutdown()
	global := settings.Global()
	overrides := global.Overrides()
	rate := 5
	overrides.PrivateQueryPerMin = &rate
	if _, err := settings.CommitGlobal(global.Revision(), overrides); err != nil {
		t.Fatal(err)
	}
	runFakeHandler(t, bot, administration.OnHelp, telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: 7, Type: telego.ChatTypePrivate},
		From: &telego.User{ID: 7, LanguageCode: "en"},
	}})
	want := memberHelpText(i18n.LangEN) + "\n\n" +
		i18n.Messages.Panel.Help.DirectMessageNote.Render(i18n.LangEN, rate)
	if fake.lastSendText != want {
		t.Errorf("private help = %q, want catalogue text %q", fake.lastSendText, want)
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
	if got, want := fake.lastSendText, i18n.Messages.Panel.Error.SaveSettings.For(i18n.LangZH); got != want {
		t.Fatalf("write failure notice = %q, want %q", got, want)
	}
	if !verification.IsEnabled(cfg.GroupIDs[0]) {
		t.Fatal("failed settings write changed effective state")
	}
}

func TestRuntimeSettingsCommandHandlersPersistAndRespond(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		handler     func(*Panel) th.Handler
		wantText    func(i18n.Lang) string
		assertState func(*testing.T, *verify.Service, int64)
	}{
		{
			name:    "stop",
			text:    "/stop",
			handler: func(panel *Panel) th.Handler { return panel.OnStop },
			wantText: func(l i18n.Lang) string {
				return i18n.Messages.Panel.Verification.Stopped.For(l)
			},
			assertState: func(t *testing.T, service *verify.Service, groupID int64) {
				t.Helper()
				if service.IsEnabled(groupID) {
					t.Fatal("/stop handler left verification enabled")
				}
			},
		},
		{
			name:    "spoiler",
			text:    "/spoiler",
			handler: func(panel *Panel) th.Handler { return panel.OnSpoiler },
			wantText: func(l i18n.Lang) string {
				return i18n.Messages.Panel.NameSpoiler.Disabled.For(l)
			},
			assertState: func(t *testing.T, service *verify.Service, groupID int64) {
				t.Helper()
				if service.NameSpoilerOn(groupID) {
					t.Fatal("/spoiler handler did not disable default name hiding")
				}
			},
		},
		{
			name:    "vmode parses mixed",
			text:    "/vmode MIXED",
			handler: func(panel *Panel) th.Handler { return panel.OnVMode },
			wantText: func(l i18n.Lang) string {
				return i18n.Messages.Panel.VerificationMode.Set.Render(l, verify.ModeName(l, config.ModeMixed))
			},
			assertState: func(t *testing.T, service *verify.Service, groupID int64) {
				t.Helper()
				if got := service.EffectiveMode(groupID); got != config.ModeMixed {
					t.Fatalf("/vmode handler mode = %q, want %q", got, config.ModeMixed)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := runtimeSettingsTestConfig()
			cfg.NotifyTTLSeconds = -1
			groupID := cfg.GroupIDs[0]
			baseline := testSettingsBaseline(t, cfg)
			path := filepath.Join(t.TempDir(), "settings.json")
			settings, err := store.NewSettings(path, baseline)
			if err != nil {
				t.Fatal(err)
			}
			fake := newFakeAdminBot()
			fake.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
			bot := newAPITestBot(t, fake)
			administration, verification := newAdminTestApplication(cfg, settings, bot)
			t.Cleanup(verification.Shutdown)

			runFakeHandler(t, bot, test.handler(administration), telego.Update{Message: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup},
				From:      &telego.User{ID: 7},
				Text:      test.text,
			}})
			l := i18n.LangZH
			if got, want := fake.lastSendText, test.wantText(l); got != want {
				t.Fatalf("%s handler response = %q, want catalogue text %q", test.name, got, want)
			}
			test.assertState(t, verification, groupID)

			restoredSettings, err := store.NewSettings(path, baseline)
			if err != nil {
				t.Fatal(err)
			}
			_, restored := newAdminTestApplication(cfg, restoredSettings, bot)
			t.Cleanup(restored.Shutdown)
			test.assertState(t, restored, groupID)
		})
	}
}

func TestSettingsCommandUsesFreshAdminMembership(t *testing.T) {
	cfg := runtimeSettingsTestConfig()
	cfg.NotifyTTLSeconds = -1
	groupID := cfg.GroupIDs[0]
	settings, err := store.NewSettings("", testSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeAdminBot()
	fake.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
	bot := newAPITestBot(t, fake)
	administration, verification := newAdminTestApplication(cfg, settings, bot)
	t.Cleanup(verification.Shutdown)
	message := &telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup},
		From:      &telego.User{ID: 7},
		Text:      "/help",
	}

	runFakeHandler(t, bot, administration.OnHelp, telego.Update{Message: message})
	fake.member = &telego.ChatMemberMember{Status: telego.MemberStatusMember}
	message.Text = "/stop"
	runFakeHandler(t, bot, administration.OnStop, telego.Update{Message: message})

	if !verification.IsEnabled(groupID) {
		t.Fatal("cached administrator status bypassed the fresh settings-command gate")
	}
	want := i18n.Messages.Panel.Error.AdminOnly.For(i18n.LangZH)
	if fake.lastSendText != want {
		t.Fatalf("fresh-admin refusal = %q, want catalogue text %q", fake.lastSendText, want)
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
