package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
)

func TestRuntimeSettingsGroupCommandsPersist(t *testing.T) {
	cfg := runtimeSettingsTestConfig()
	groupID := cfg.GroupIDs[0]
	path := filepath.Join(t.TempDir(), "settings.json")
	settings, err := store.NewSettings(path, settingsBaselineFromConfig(cfg, configPresence{}))
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(cfg)
	installRuntimeSettings(v, settings)
	if err := v.setEnabled(groupID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := v.toggleNameSpoiler(groupID); err != nil {
		t.Fatal(err)
	}
	if err := v.setVerifyMode(groupID, config.ModeMixed); err != nil {
		t.Fatal(err)
	}

	reloaded, err := store.NewSettings(path, settingsBaselineFromConfig(cfg, configPresence{}))
	if err != nil {
		t.Fatal(err)
	}
	v2 := NewVerifier(cfg)
	installRuntimeSettings(v2, reloaded)
	if v2.isEnabled(groupID) || v2.nameSpoilerOn(groupID) || v2.effectiveMode(groupID) != config.ModeMixed {
		t.Fatalf("reloaded group = enabled:%v spoiler:%v mode:%q", v2.isEnabled(groupID), v2.nameSpoilerOn(groupID), v2.effectiveMode(groupID))
	}

	runtimeOnly, err := store.NewSettings("", settingsBaselineFromConfig(cfg, configPresence{}))
	if err != nil {
		t.Fatal(err)
	}
	v3 := NewVerifier(cfg)
	installRuntimeSettings(v3, runtimeOnly)
	if err := v3.setEnabled(groupID, false); err != nil {
		t.Fatal(err)
	}
	if v3.isEnabled(groupID) {
		t.Fatal("runtime-only group command did not update settings")
	}
	group, _ := runtimeOnly.Group(groupID)
	if group.Enabled().Value || group.Enabled().Source != store.SourceRuntime {
		t.Fatalf("runtime-only transaction = %+v", group.Enabled())
	}
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
	v := NewVerifier(cfg)
	fake := newFakeVerifyBot()
	fake.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
	runFakeHandler(t, newAPITestBot(t, fake), v.onStop, telego.Update{Message: &telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: groupB, Type: "supergroup"},
		From:      &telego.User{ID: 7},
		Text:      "/stop",
	}})
	if !v.isEnabled(groupA) || v.isEnabled(groupB) {
		t.Fatalf("/stop state = group A:%v group B:%v, want true/false", v.isEnabled(groupA), v.isEnabled(groupB))
	}
}

func TestSettingsCommandReportsWriteFailure(t *testing.T) {
	cfg := runtimeSettingsTestConfig()
	cfg.NotifyTTLSeconds = -1
	settings, err := store.NewSettings(t.TempDir(), settingsBaselineFromConfig(cfg, configPresence{}))
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(cfg)
	installRuntimeSettings(v, settings)
	fake := newFakeVerifyBot()
	fake.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
	runFakeHandler(t, newAPITestBot(t, fake), v.onStop, telego.Update{Message: &telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: cfg.GroupIDs[0], Type: "supergroup"},
		From:      &telego.User{ID: 7},
		Text:      "/stop",
	}})
	if !strings.Contains(fake.lastSendText, "无法保存设置") {
		t.Fatalf("write failure notice = %q", fake.lastSendText)
	}
	if !v.isEnabled(cfg.GroupIDs[0]) {
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
	baseline, err := settingsBaseline(path, cfg)
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
	baseline, err := settingsBaseline(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.NewSettings("", baseline)
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(cfg)
	installRuntimeSettings(v, settings)
	if err := v.setEnabled(groupA, false); err != nil {
		t.Fatal(err)
	}

	untouched, _ := settings.Group(groupB)
	if got := untouched.Enabled(); !got.Value || got.Source != store.SourceDefault {
		t.Fatalf("untouched enabled = %+v, want built-in true", got)
	}
	if got := untouched.TimeoutSeconds(); got.Value != 420 || got.Source != store.SourceConfig {
		t.Fatalf("untouched timeout = %+v, want configured 420", got)
	}
	if !v.isEnabled(groupB) || v.timeout(groupB) != 420*time.Second || untouched.BanSeconds().Value != 7200 {
		t.Fatalf("untouched scalar behavior = enabled:%v timeout:%v ban:%d", v.isEnabled(groupB), v.timeout(groupB), untouched.BanSeconds().Value)
	}
	if ttl, enabled := v.lookupAutoDelete(groupB); ttl != 10*time.Minute || !enabled {
		t.Fatalf("untouched lookup cleanup = (%v, %v), want (10m, true)", ttl, enabled)
	}
	if v.verifyMaxFails(groupB) != 5 || v.verifyRetrySeconds(groupB) != 90 || !untouched.AntispamEnabled().Value {
		t.Fatalf("untouched verification/antispam = max:%d retry:%d antispam:%v",
			v.verifyMaxFails(groupB), v.verifyRetrySeconds(groupB), untouched.AntispamEnabled().Value)
	}
	if whitelist := untouched.ChannelWhitelist().Value; len(whitelist) != 1 || whitelist[0] != -1009000000201 ||
		len(v.trustedGroups(groupB)) != 1 ||
		!v.isKnownChat(-1009000000203) {
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
	settings, err := store.NewSettings("", settingsBaselineFromConfig(cfg, configPresence{}))
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
	antispam := true
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
	v := NewVerifier(cfg)
	installRuntimeSettings(v, settings)

	if v.isEnabled(groupA) || !v.isEnabled(groupB) ||
		v.nameSpoilerOn(groupA) || !v.nameSpoilerOn(groupB) ||
		v.effectiveMode(groupA) != config.ModeQuiz || v.effectiveMode(groupB) != config.ModeKernel {
		t.Fatal("enabled, spoiler, or mode leaked between groups")
	}
	if v.timeout(groupA) != 10*time.Minute || v.timeout(groupB) != 4*time.Minute ||
		groupAView.BanSeconds().Value != 3600 || groupBView.BanSeconds().Value != 0 {
		t.Fatal("timeout or ban duration leaked between groups")
	}
	if ttl, on := v.lookupAutoDelete(groupA); ttl != 5*time.Minute || on {
		t.Fatalf("group A lookup cleanup = (%v, %v)", ttl, on)
	}
	if ttl, on := v.lookupAutoDelete(groupB); ttl != 3*time.Minute || !on {
		t.Fatalf("group B lookup cleanup = (%v, %v)", ttl, on)
	}
	if count, ban := v.recordVerifyFail(groupA, 7); count != 1 || !ban {
		t.Fatalf("group A failure threshold = (%d, %v)", count, ban)
	}
	if count, ban := v.recordVerifyFail(groupB, 7); count != 1 || ban {
		t.Fatalf("group B failure threshold = (%d, %v)", count, ban)
	}
	if v.verifyRetrySeconds(groupA) != 30 || v.verifyRetrySeconds(groupB) != 180 ||
		!groupAView.AntispamEnabled().Value || groupBView.AntispamEnabled().Value ||
		len(groupAView.ChannelWhitelist().Value) != 1 || groupAView.ChannelWhitelist().Value[0] != senderID ||
		len(groupBView.ChannelWhitelist().Value) != 0 {
		t.Fatal("failure cooldown or antispam state leaked between groups")
	}
	if len(v.trustedGroups(groupA)) != 1 || len(v.trustedGroups(groupB)) != 0 ||
		!v.isKnownChat(knownID) || v.requiredChannelID(groupA) != requiredID ||
		v.requiredChannelID(groupB) != 0 || v.channelDisplay(groupA) != display ||
		v.channelInviteURL(groupA) != invite {
		t.Fatal("channel or trusted-chat settings leaked between groups")
	}
	if len(v.questions(groupA)) != 1 || len(v.questions(groupB)) != 0 {
		t.Fatal("question pools leaked between groups")
	}
	if question, answers := v.fallbackQuestion(groupA, i18n.LangZH); question != fallback[0].Q || len(answers) != 1 {
		t.Fatalf("group A fallback = %q %v", question, answers)
	}
	if v.groupLanguage(groupA, "zh-hant") != i18n.LangEN ||
		v.groupLanguage(groupB, "zh-hant") != i18n.LangZHHant {
		t.Fatal("language settings leaked between groups")
	}
}

func TestRuntimeOnlyGroupPendingSurvivesRestart(t *testing.T) {
	const runtimeGroup int64 = -1009000000099
	cfg := runtimeSettingsTestConfig()
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	baseline := settingsBaselineFromConfig(cfg, configPresence{})
	settings, err := store.NewSettings(settingsPath, baseline)
	if err != nil {
		t.Fatal(err)
	}
	registration := settings.Registrations()
	registration.RegisteredGroups = []store.RegisteredGroup{{ID: runtimeGroup, RegisteredBy: 42, Title: "Runtime"}}
	if _, err := settings.CommitRegistrations(registration.Revision, registration); err != nil {
		t.Fatal(err)
	}

	effective := configWithEffectiveGroups(cfg, settings)
	before := NewVerifier(effective)
	installRuntimeSettings(before, settings)
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
	after := NewVerifier(configWithEffectiveGroups(cfg, reloaded))
	installRuntimeSettings(after, reloaded)
	after.statePath = before.statePath
	after.load(nil)
	t.Cleanup(after.stopForShutdown)
	if _, ok := after.pend[key]; !ok {
		t.Fatal("pending for durably registered runtime group was dropped on restart")
	}

	withoutRegistration := NewVerifier(cfg)
	withoutRegistration.statePath = before.statePath
	withoutRegistration.load(nil)
	t.Cleanup(withoutRegistration.stopForShutdown)
	if _, ok := withoutRegistration.pend[key]; ok {
		t.Fatal("pending for an unregistered runtime group was restored")
	}
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
