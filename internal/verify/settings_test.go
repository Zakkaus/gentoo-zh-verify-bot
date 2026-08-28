package verify

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/lookup"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
)

func TestRuntimeSettingsDirectSettersPersist(t *testing.T) {
	cfg := runtimeSettingsTestConfig()
	groupID := cfg.GroupIDs[0]
	path := filepath.Join(t.TempDir(), "settings.json")
	settings, err := store.NewSettings(path, testSettingsBaselineFromConfig(t, cfg, store.SourceDefault))
	if err != nil {
		t.Fatal(err)
	}
	v := newService(settings, nil, cfg, &i18n.Messages)
	if err := v.SetEnabled(groupID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ToggleNameSpoiler(groupID); err != nil {
		t.Fatal(err)
	}
	if err := v.SetVerifyMode(groupID, config.ModeMixed); err != nil {
		t.Fatal(err)
	}

	reloaded, err := store.NewSettings(path, testSettingsBaselineFromConfig(t, cfg, store.SourceDefault))
	if err != nil {
		t.Fatal(err)
	}
	v2 := newService(reloaded, nil, cfg, &i18n.Messages)
	if v2.IsEnabled(groupID) || v2.NameSpoilerOn(groupID) || v2.EffectiveMode(groupID) != config.ModeMixed {
		t.Fatalf("reloaded group = enabled:%v spoiler:%v mode:%q", v2.IsEnabled(groupID), v2.NameSpoilerOn(groupID), v2.EffectiveMode(groupID))
	}

	runtimeOnly, err := store.NewSettings("", testSettingsBaselineFromConfig(t, cfg, store.SourceDefault))
	if err != nil {
		t.Fatal(err)
	}
	v3 := newService(runtimeOnly, nil, cfg, &i18n.Messages)
	if err := v3.SetEnabled(groupID, false); err != nil {
		t.Fatal(err)
	}
	if v3.IsEnabled(groupID) {
		t.Fatal("runtime-only group command did not update settings")
	}
	group, _ := runtimeOnly.Group(groupID)
	if group.Enabled().Value || group.Enabled().Source != store.SourceRuntime {
		t.Fatalf("runtime-only transaction = %+v", group.Enabled())
	}
}
func TestUntouchedGroupUsesConfigAndDefaults(t *testing.T) {
	const (
		groupA int64 = -1009000000101
		groupB int64 = -1009000000102
	)
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"groups":[{"id":-1009000000101},{"id":-1009000000102}],
		"timeout_seconds":420,
		"ban_seconds":7200,
		"lookup_ttl_seconds":600,
		"verify_max_fails":5,
		"verify_retry_seconds":90,
		"block_channel_senders":true,
		"channel_whitelist":[-1009000000201],
		"trusted_member_group_ids":[-1009000000202],
		"known_chat_ids":[-1009000000203]
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	baseline := testSettingsBaselineFromConfig(t, cfg, store.SourceConfig)
	settings, err := store.NewSettings("", baseline)
	if err != nil {
		t.Fatal(err)
	}
	v := newService(settings, nil, cfg, &i18n.Messages)
	if err := v.SetEnabled(groupA, false); err != nil {
		t.Fatal(err)
	}

	untouched, _ := settings.Group(groupB)
	if got := untouched.Enabled(); !got.Value || got.Source != store.SourceDefault {
		t.Fatalf("untouched enabled = %+v, want built-in true", got)
	}
	if got := untouched.TimeoutSeconds(); got.Value != 420 || got.Source != store.SourceConfig {
		t.Fatalf("untouched timeout = %+v, want configured 420", got)
	}
	if !v.IsEnabled(groupB) || v.timeout(groupB) != 420*time.Second || untouched.BanSeconds().Value != 7200 {
		t.Fatalf("untouched scalar behavior = enabled:%v timeout:%v ban:%d", v.IsEnabled(groupB), v.timeout(groupB), untouched.BanSeconds().Value)
	}
	if ttl, enabled := testLookupService(v).AutoDelete(groupB); ttl != 10*time.Minute || !enabled {
		t.Fatalf("untouched lookup cleanup = (%v, %v), want (10m, true)", ttl, enabled)
	}
	if v.verifyMaxFails(groupB) != 5 || v.verifyRetrySeconds(groupB) != 90 || !untouched.AntispamEnabled().Value {
		t.Fatalf("untouched verification/antispam = max:%d retry:%d antispam:%v",
			v.verifyMaxFails(groupB), v.verifyRetrySeconds(groupB), untouched.AntispamEnabled().Value)
	}
	known := untouched.KnownChatIDs().Value
	if whitelist := untouched.ChannelWhitelist().Value; len(whitelist) != 1 || whitelist[0] != -1009000000201 ||
		len(v.trustedGroups(groupB)) != 1 || len(known) != 1 || known[0] != -1009000000203 {
		t.Fatal("untouched group did not use configured list settings")
	}
}
func TestPerGroupRuntimeSettingsIsolation(t *testing.T) {
	const (
		groupA       int64 = -1009000000401
		groupB       int64 = -1009000000402
		senderID     int64 = -1009000000501
		trustedID    int64 = -1009000000502
		knownID      int64 = -1009000000503
		requiredID   int64 = -1009000000504
		overrideSecs       = 300
	)
	cfg := &config.Config{
		Groups:             []config.GroupConfig{{ID: groupA}, {ID: groupB}},
		GroupIDs:           []int64{groupA, groupB},
		TimeoutSeconds:     240,
		VerifyMaxFails:     3,
		VerifyRetrySeconds: 180,
		LookupTTLSeconds:   intPointer(180),
	}
	settings, err := store.NewSettings("", testSettingsBaselineFromConfig(t, cfg, store.SourceDefault))
	if err != nil {
		t.Fatal(err)
	}
	group, _ := settings.Group(groupA)
	overrides := group.Overrides()
	enabled := false
	spoiler := false
	mode := config.ModeQuiz
	banSeconds := 3600
	lookupEnabled := false
	timeoutSeconds := 600
	maxFails := 1
	retrySeconds := 30
	antispam := false // the group-B default is on, so isolation is proved by overriding to off
	requiredChannel := requiredID
	display := "@required"
	invite := "https://t.me/required"
	fallbackBuiltin := false
	language := "en"
	whitelist := []int64{senderID}
	trusted := []int64{trustedID}
	known := []int64{knownID}
	questions := []config.Question{{Q: "Package manager?", Options: []string{"Portage", "apt"}, Answer: 0}}
	fallback := []config.ShortQuestion{{Q: "Init system?", Answers: []string{"OpenRC"}}}
	overrides.Enabled = &enabled
	overrides.NameSpoiler = &spoiler
	overrides.VerifyMode = &mode
	overrides.BanSeconds = &banSeconds
	overrides.LookupTTLSeconds = intPointer(overrideSecs)
	overrides.LookupAutoDeleteEnabled = &lookupEnabled
	overrides.TimeoutSeconds = &timeoutSeconds
	overrides.VerifyMaxFails = &maxFails
	overrides.VerifyRetrySeconds = &retrySeconds
	overrides.AntispamEnabled = &antispam
	overrides.ChannelWhitelist = &whitelist
	overrides.TrustedMemberGroupIDs = &trusted
	overrides.KnownChatIDs = &known
	overrides.RequiredChannelID = &requiredChannel
	overrides.ChannelDisplay = &display
	overrides.ChannelInviteURL = &invite
	overrides.Questions = &questions
	overrides.FallbackQuestions = &fallback
	overrides.FallbackBuiltin = &fallbackBuiltin
	overrides.Lang = &language
	if _, err := settings.CommitGroup(groupA, group.Revision(), overrides); err != nil {
		t.Fatal(err)
	}
	groupAView, _ := settings.Group(groupA)
	groupBView, _ := settings.Group(groupB)
	v := newService(settings, nil, cfg, &i18n.Messages)

	if v.IsEnabled(groupA) || !v.IsEnabled(groupB) ||
		v.NameSpoilerOn(groupA) || !v.NameSpoilerOn(groupB) ||
		v.EffectiveMode(groupA) != config.ModeQuiz || v.EffectiveMode(groupB) != config.ModeKernel {
		t.Fatal("enabled, spoiler, or mode leaked between groups")
	}
	if v.timeout(groupA) != 10*time.Minute || v.timeout(groupB) != 4*time.Minute ||
		groupAView.BanSeconds().Value != 3600 || groupBView.BanSeconds().Value != 0 {
		t.Fatal("timeout or ban duration leaked between groups")
	}
	if ttl, on := testLookupService(v).AutoDelete(groupA); ttl != 5*time.Minute || on {
		t.Fatalf("group A lookup cleanup = (%v, %v)", ttl, on)
	}
	if ttl, on := testLookupService(v).AutoDelete(groupB); ttl != 3*time.Minute || !on {
		t.Fatalf("group B lookup cleanup = (%v, %v)", ttl, on)
	}
	if count, ban := v.recordVerifyFail(groupA, 7, v.wallNow()); count != 1 || !ban {
		t.Fatalf("group A failure threshold = (%d, %v)", count, ban)
	}
	if count, ban := v.recordVerifyFail(groupB, 7, v.wallNow()); count != 1 || ban {
		t.Fatalf("group B failure threshold = (%d, %v)", count, ban)
	}
	if v.verifyRetrySeconds(groupA) != 30 || v.verifyRetrySeconds(groupB) != 180 ||
		groupAView.AntispamEnabled().Value || !groupBView.AntispamEnabled().Value ||
		len(groupAView.ChannelWhitelist().Value) != 1 || groupAView.ChannelWhitelist().Value[0] != senderID ||
		len(groupBView.ChannelWhitelist().Value) != 0 {
		t.Fatal("failure cooldown or antispam state leaked between groups")
	}
	knownValues := groupAView.KnownChatIDs().Value
	if len(v.trustedGroups(groupA)) != 1 || len(v.trustedGroups(groupB)) != 0 ||
		len(knownValues) != 1 || knownValues[0] != knownID || v.RequiredChannelID(groupA) != requiredID ||
		v.RequiredChannelID(groupB) != 0 || v.channelDisplay(groupA) != display ||
		v.channelInviteURL(groupA) != invite {
		t.Fatal("channel or trusted-chat settings leaked between groups")
	}
	if len(v.questions(groupA)) != 1 || len(v.questions(groupB)) != 0 {
		t.Fatal("question pools leaked between groups")
	}
	if question, answers := v.fallbackQuestion(groupA, i18n.LangZH); question != fallback[0].Q || len(answers) != 1 {
		t.Fatalf("group A fallback = %q %v", question, answers)
	}
	if v.groupLanguage(groupA) != i18n.LangEN ||
		v.groupLanguage(groupB) != i18n.LangZH {
		t.Fatal("language settings leaked between groups")
	}
}

func TestRuntimeOnlyGroupPendingSurvivesRestartWithoutRebuiltConfig(t *testing.T) {
	const runtimeGroup int64 = -1009000000099
	cfg := runtimeSettingsTestConfig()
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	baseline := testSettingsBaselineFromConfig(t, cfg, store.SourceDefault)
	settings, err := store.NewSettings(settingsPath, baseline)
	if err != nil {
		t.Fatal(err)
	}
	registration := settings.Registrations()
	registration.RegisteredGroups = []store.RegisteredGroup{{ID: runtimeGroup, RegisteredBy: 42, Title: "Runtime"}}
	if _, err := settings.CommitRegistrations(registration.Revision, registration); err != nil {
		t.Fatal(err)
	}

	before := newService(settings, nil, cfg, &i18n.Messages)
	before.statePath = filepath.Join(dir, "pending.json")
	key := pkey{gid: runtimeGroup, uid: 7001}
	before.pend[key] = &pending{
		groupMsgID: 501,
		mode:       config.ModeKernel,
		qText:      "Kernel version?",
		correctIdx: -1,
		nonce:      "runtime-restart",
		name:       "Applicant",
		deadline:   time.Now().Add(time.Hour),
	}
	before.save()
	before.stopForShutdown()

	reloaded, err := store.NewSettings(settingsPath, baseline)
	if err != nil {
		t.Fatal(err)
	}
	after := newService(reloaded, nil, cfg, &i18n.Messages)
	after.statePath = before.statePath
	after.load(nil)
	t.Cleanup(after.stopForShutdown)
	if _, ok := after.pend[key]; !ok {
		t.Fatal("pending for durably registered runtime group was dropped on restart")
	}

	withoutRegistration := newTestService(cfg)
	withoutRegistration.statePath = before.statePath
	withoutRegistration.load(nil)
	t.Cleanup(withoutRegistration.stopForShutdown)
	if _, ok := withoutRegistration.pend[key]; ok {
		t.Fatal("pending for an unregistered runtime group was restored")
	}
}

func testSettingsBaselineFromConfig(t *testing.T, cfg *config.Config, configuredSource store.Source) store.SettingsBaseline {
	t.Helper()
	path := filepath.Join(t.TempDir(), "missing-config.json")
	if configuredSource == store.SourceConfig {
		path = filepath.Join(t.TempDir(), "config.json")
		data := []byte(`{
			"ban_seconds": 0,
			"lookup_ttl_seconds": 0,
			"timeout_seconds": 0,
			"verify_max_fails": 0,
			"verify_retry_seconds": 0,
			"block_channel_senders": false,
			"channel_whitelist": [],
			"trusted_member_group_ids": [],
			"known_chat_ids": [],
			"required_channel_id": 0,
			"channel_display": "",
			"channel_invite_url": "",
			"questions": [],
			"fallback_questions": [],
			"rich_messages": false,
			"private_query_per_min": 0
		}`)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	baseline, err := store.LoadBaseline(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return baseline
}

func testLookupService(v *Service) *lookup.Service {
	return lookup.New(v.settings, nil, v.cfg, "")
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
