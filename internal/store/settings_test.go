package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

const (
	testGroupA int64 = -1009000000001
	testGroupB int64 = -1009000000002
)

func testSettingsBaseline() SettingsBaseline {
	group := GroupBaseline{
		Enabled:                 BaselineValue[bool]{Value: true, Source: SourceDefault},
		VerifyMode:              BaselineValue[string]{Value: config.ModeKernel, Source: SourceDefault},
		NameSpoiler:             BaselineValue[bool]{Value: true, Source: SourceDefault},
		BanSeconds:              BaselineValue[int]{Value: 0, Source: SourceDefault},
		LookupTTLSeconds:        BaselineValue[int]{Value: 180, Source: SourceDefault},
		LookupAutoDeleteEnabled: BaselineValue[bool]{Value: true, Source: SourceDefault},
		TimeoutSeconds:          BaselineValue[int]{Value: 240, Source: SourceConfig},
		VerifyMaxFails:          BaselineValue[int]{Value: 3, Source: SourceDefault},
		VerifyRetrySeconds:      BaselineValue[int]{Value: 180, Source: SourceDefault},
		AntispamEnabled:         BaselineValue[bool]{Value: false, Source: SourceConfig},
		ChannelWhitelist:        BaselineValue[[]int64]{Value: []int64{-1007000000001}, Source: SourceConfig},
		TrustedMemberGroupIDs:   BaselineValue[[]int64]{Value: []int64{-1006000000001}, Source: SourceConfig},
		KnownChatIDs:            BaselineValue[[]int64]{Value: []int64{-1005000000001}, Source: SourceConfig},
		RequiredChannelID:       BaselineValue[int64]{Value: 0, Source: SourceDefault},
		ChannelDisplay:          BaselineValue[string]{Value: "", Source: SourceDefault},
		ChannelInviteURL:        BaselineValue[string]{Value: "https://t.me/+configured", Source: SourceConfig},
		Questions: BaselineValue[[]config.Question]{Value: []config.Question{{
			Q: "Select Portage", Options: []string{"Portage", "apt"}, Answer: 0,
		}}, Source: SourceConfig},
		FallbackQuestions: BaselineValue[[]config.ShortQuestion]{Value: []config.ShortQuestion{}, Source: SourceDefault},
		FallbackBuiltin:   BaselineValue[bool]{Value: true, Source: SourceDefault},
		Lang:              BaselineValue[string]{Value: "zh", Source: SourceDefault},
	}
	groupA, groupB := group, group
	groupA.ID = testGroupA
	groupB.ID = testGroupB
	groupB.VerifyMode = BaselineValue[string]{Value: config.ModeQuiz, Source: SourceConfig}
	return SettingsBaseline{
		Groups:         []GroupBaseline{groupA, groupB},
		DefaultGroup:   group,
		ControlGroupID: testGroupA,
		Global: GlobalBaseline{
			RichMessages:       BaselineValue[bool]{Value: true, Source: SourceConfig},
			PrivateQueryPerMin: BaselineValue[int]{Value: 3, Source: SourceDefault},
		},
	}
}

func TestSettingsSparseRoundTripAndRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	initial, ok := settings.Group(testGroupA)
	if !ok {
		t.Fatal("configured group is absent")
	}
	if got := initial.Enabled(); got.Value != true || got.Source != SourceDefault {
		t.Fatalf("initial enabled = %+v, want built-in true", got)
	}
	if got := initial.TimeoutSeconds(); got.Value != 240 || got.Source != SourceConfig {
		t.Fatalf("initial timeout = %+v, want configured 240", got)
	}
	if got := initial.LookupAutoDeleteEnabled(); !got.Value || got.Source != SourceDefault {
		t.Fatalf("initial lookup auto-delete = %+v, want built-in true", got)
	}
	if got := initial.AntispamEnabled(); got.Value || got.Source != SourceConfig {
		t.Fatalf("initial antispam = %+v, want configured false", got)
	}
	if got := initial.Lang(); got.Value != "zh" || got.Source != SourceDefault {
		t.Fatalf("initial language = %+v, want default zh", got)
	}
	sameAsBaseline := initial.Overrides()
	sameAsBaseline.Enabled = ptr(true)
	noChange, err := settings.CommitGroup(testGroupA, initial.Revision(), sameAsBaseline)
	if err != nil {
		t.Fatal(err)
	}
	if noChange.Revision != 0 {
		t.Fatalf("baseline-equal value created revision %d", noChange.Revision)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("baseline-equal value created settings file: %v", err)
	}

	emptyIDs := []int64{}
	emptyQuestions := []config.Question{}
	fallback := []config.ShortQuestion{{Q: "Package manager?", Answers: []string{"portage", "emerge"}}}
	next := GroupOverrides{
		Enabled:                 ptr(false),
		VerifyMode:              ptr(config.ModeMixed),
		NameSpoiler:             ptr(false),
		BanSeconds:              ptr(3600),
		LookupTTLSeconds:        ptr(300),
		LookupAutoDeleteEnabled: ptr(false),
		TimeoutSeconds:          ptr(600),
		VerifyMaxFails:          ptr(-1),
		VerifyRetrySeconds:      ptr(-1),
		AntispamEnabled:         ptr(true),
		ChannelWhitelist:        &emptyIDs,
		TrustedMemberGroupIDs:   ptr([]int64{-1006000000002}),
		KnownChatIDs:            &emptyIDs,
		RequiredChannelID:       ptr(int64(-1004000000001)),
		ChannelDisplay:          ptr("@required"),
		ChannelInviteURL:        ptr(""),
		Questions:               &emptyQuestions,
		FallbackQuestions:       &fallback,
		FallbackBuiltin:         ptr(false),
		Lang:                    ptr("en"),
	}
	result, err := settings.CommitGroup(testGroupA, initial.Revision(), next)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 1 || !result.Durable {
		t.Fatalf("commit result = %+v, want durable revision 1", result)
	}

	global := settings.Global()
	globalResult, err := settings.CommitGlobal(global.Revision(), GlobalOverrides{
		RichMessages: ptr(false), PrivateQueryPerMin: ptr(7),
	})
	if err != nil {
		t.Fatal(err)
	}
	if globalResult.Revision != 1 || !globalResult.Durable {
		t.Fatalf("global commit result = %+v", globalResult)
	}

	reloaded, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	group, _ := reloaded.Group(testGroupA)
	if group.Revision() != 1 || group.Enabled().Value || group.Enabled().Source != SourceRuntime {
		t.Fatalf("reloaded enabled/revision = %+v/%d", group.Enabled(), group.Revision())
	}
	if got := group.ChannelWhitelist(); len(got.Value) != 0 || got.Source != SourceRuntime {
		t.Fatalf("explicit empty channel whitelist = %+v", got)
	}
	if got := group.LookupTTLSeconds(); got.Value != 300 || got.Source != SourceRuntime {
		t.Fatalf("remembered lookup TTL = %+v", got)
	}
	if got := group.LookupAutoDeleteEnabled(); got.Value || got.Source != SourceRuntime {
		t.Fatalf("disabled lookup auto-delete = %+v", got)
	}
	if got := group.AntispamEnabled(); !got.Value || got.Source != SourceRuntime {
		t.Fatalf("enabled antispam = %+v", got)
	}
	if got := group.KnownChatIDs(); len(got.Value) != 0 || got.Source != SourceRuntime {
		t.Fatalf("explicit empty known chats = %+v", got)
	}
	if got := group.Questions(); len(got.Value) != 0 || got.Source != SourceRuntime {
		t.Fatalf("explicit empty question bank = %+v", got)
	}
	if got := group.ChannelInviteURL(); got.Value != "" || got.Source != SourceRuntime {
		t.Fatalf("explicit empty channel invite = %+v", got)
	}
	if got := group.FallbackQuestions(); !reflect.DeepEqual(got.Value, fallback) || got.Source != SourceRuntime {
		t.Fatalf("fallback questions = %+v", got)
	}
	if group.FallbackBuiltin().Value || group.Lang().Value != "en" {
		t.Fatalf("fallback/lang = %+v/%+v", group.FallbackBuiltin(), group.Lang())
	}
	if got := reloaded.Global().PrivateQueryPerMin(); got.Value != 7 || got.Source != SourceRuntime {
		t.Fatalf("global query rate = %+v", got)
	}

	restore := group.Overrides()
	restore.Enabled = nil
	restored, err := reloaded.CommitGroup(testGroupA, group.Revision(), restore)
	if err != nil {
		t.Fatal(err)
	}
	group, _ = reloaded.Group(testGroupA)
	if restored.Revision != 2 || !group.Enabled().Value || group.Enabled().Source != SourceDefault {
		t.Fatalf("restored enabled/revision = %+v/%d", group.Enabled(), restored.Revision)
	}

	var raw map[string]any
	decodeFile(t, path, &raw)
	groups := raw["groups"].(map[string]any)
	record := groups["-1009000000001"].(map[string]any)
	if _, exists := record["enabled"]; exists {
		t.Fatalf("restored field remains in sparse record: %#v", record)
	}
	if got := record["channel_whitelist"].([]any); len(got) != 0 {
		t.Fatalf("explicit empty whitelist encoded as %#v", got)
	}
	if raw["enabled"] != true || raw["name_spoiler"] != false || raw["verify_mode"] != config.ModeMixed {
		t.Fatalf("legacy mirrors = enabled:%v spoiler:%v mode:%v", raw["enabled"], raw["name_spoiler"], raw["verify_mode"])
	}
}
func TestGroupLanguageSettingSourceAndRevision(t *testing.T) {
	baseline := testSettingsBaseline()
	baseline.Groups[0].Lang = BaselineValue[string]{Value: "zh-Hant", Source: SourceConfig}
	settings, err := NewSettings(filepath.Join(t.TempDir(), "settings.json"), baseline)
	if err != nil {
		t.Fatal(err)
	}

	group, _ := settings.Group(testGroupA)
	if got := group.Lang(); got.Value != "zh-Hant" || got.Source != SourceConfig {
		t.Fatalf("configured language = %+v", got)
	}
	override := group.Overrides()
	override.Lang = ptr("en")
	result, err := settings.CommitGroup(testGroupA, group.Revision(), override)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 1 {
		t.Fatalf("language revision = %d, want 1", result.Revision)
	}
	group, _ = settings.Group(testGroupA)
	if got := group.Lang(); got.Value != "en" || got.Source != SourceRuntime {
		t.Fatalf("runtime language = %+v", got)
	}

	stale := group.Overrides()
	stale.Lang = ptr("zh")
	if _, err := settings.CommitGroup(testGroupA, 0, stale); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale language commit error = %v, want conflict", err)
	}
	invalid := group.Overrides()
	invalid.Lang = ptr("fr")
	if _, err := settings.CommitGroup(testGroupA, group.Revision(), invalid); err == nil {
		t.Fatal("unsupported runtime language was accepted")
	}
	group, _ = settings.Group(testGroupA)
	if group.Revision() != 1 || group.Lang().Value != "en" {
		t.Fatalf("failed language commit published %+v at revision %d", group.Lang(), group.Revision())
	}

	restore := group.Overrides()
	restore.Lang = nil
	if _, err := settings.CommitGroup(testGroupA, group.Revision(), restore); err != nil {
		t.Fatal(err)
	}
	group, _ = settings.Group(testGroupA)
	if got := group.Lang(); got.Value != "zh-Hant" || got.Source != SourceConfig {
		t.Fatalf("restored language = %+v", got)
	}
}

func TestSettingsLookupAutoDeleteRetainsTTLAcrossToggle(t *testing.T) {
	settings, err := NewSettings("", testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}

	group, _ := settings.Group(testGroupA)
	enabled := group.Overrides()
	enabled.LookupTTLSeconds = ptr(300)
	enabled.LookupAutoDeleteEnabled = ptr(true)
	first, err := settings.CommitGroup(group.ID(), group.Revision(), enabled)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 {
		t.Fatalf("enable revision = %d, want 1", first.Revision)
	}

	group, _ = settings.Group(testGroupA)
	disabled := group.Overrides()
	disabled.LookupAutoDeleteEnabled = ptr(false)
	second, err := settings.CommitGroup(group.ID(), group.Revision(), disabled)
	if err != nil {
		t.Fatal(err)
	}
	group, _ = settings.Group(testGroupA)
	if got := group.LookupTTLSeconds(); got.Value != 300 || got.Source != SourceRuntime {
		t.Fatalf("disabled remembered TTL = %+v, want runtime 300", got)
	}
	if got := group.LookupAutoDeleteEnabled(); got.Value || got.Source != SourceRuntime {
		t.Fatalf("disabled state = %+v, want runtime false", got)
	}

	reEnabled := group.Overrides()
	reEnabled.LookupAutoDeleteEnabled = ptr(true)
	third, err := settings.CommitGroup(group.ID(), group.Revision(), reEnabled)
	if err != nil {
		t.Fatal(err)
	}
	group, _ = settings.Group(testGroupA)
	if second.Revision != 2 || third.Revision != 3 {
		t.Fatalf("toggle revisions = %d/%d, want 2/3", second.Revision, third.Revision)
	}
	if got := group.LookupTTLSeconds(); got.Value != 300 || got.Source != SourceRuntime {
		t.Fatalf("re-enabled TTL = %+v, want runtime 300", got)
	}
	if got := group.LookupAutoDeleteEnabled(); !got.Value || got.Source != SourceDefault {
		t.Fatalf("re-enabled state = %+v, want default true", got)
	}
}

func TestSettingsMigratesVersionOneLookupDisableWithoutSentinel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := settingsFile{
		Version: SettingsSchemaVersion - 1,
		Groups: map[int64]groupRecord{
			testGroupA: {Revision: 7, GroupOverrides: GroupOverrides{LookupTTLSeconds: ptr(0)}},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	settings, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	group, _ := settings.Group(testGroupA)
	if group.Revision() != 7 {
		t.Fatalf("migrated revision = %d, want 7", group.Revision())
	}
	if got := group.LookupTTLSeconds(); got.Value != 180 || got.Source != SourceDefault {
		t.Fatalf("migrated remembered TTL = %+v, want default 180", got)
	}
	if got := group.LookupAutoDeleteEnabled(); got.Value || got.Source != SourceRuntime {
		t.Fatalf("migrated enabled state = %+v, want runtime false", got)
	}
}

func TestSettingsMigratesLegacyGoldenToEveryConfiguredGroup(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "state", "settings-legacy-v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	for _, groupID := range []int64{testGroupA, testGroupB} {
		group, ok := settings.Group(groupID)
		if !ok {
			t.Fatalf("migrated group %d is absent", groupID)
		}
		if group.Revision() != 1 || group.Enabled().Value || group.NameSpoiler().Value || group.VerifyMode().Value != config.ModeMixed {
			t.Fatalf("migrated group %d = rev:%d enabled:%v spoiler:%v mode:%q", groupID, group.Revision(), group.Enabled().Value, group.NameSpoiler().Value, group.VerifyMode().Value)
		}
		if group.Enabled().Source != SourceRuntime || group.NameSpoiler().Source != SourceRuntime || group.VerifyMode().Source != SourceRuntime {
			t.Fatalf("migrated group %d sources = %v/%v/%v", groupID, group.Enabled().Source, group.NameSpoiler().Source, group.VerifyMode().Source)
		}
	}

	var migrated settingsFile
	decodeFile(t, path, &migrated)
	if migrated.Version != SettingsSchemaVersion || len(migrated.Groups) != 2 {
		t.Fatalf("migrated file = version:%d groups:%d", migrated.Version, len(migrated.Groups))
	}
	for _, groupID := range []int64{testGroupA, testGroupB} {
		record := migrated.Groups[groupID]
		if record.Revision != 1 || record.Enabled == nil || *record.Enabled || record.NameSpoiler == nil || *record.NameSpoiler || record.VerifyMode == nil || *record.VerifyMode != config.ModeMixed {
			t.Fatalf("migrated record %d = %#v", groupID, record)
		}
	}
	if migrated.Enabled == nil || *migrated.Enabled || migrated.NameSpoiler == nil || *migrated.NameSpoiler || migrated.VerifyMode != config.ModeMixed {
		t.Fatalf("legacy mirrors changed during migration: %#v", migrated)
	}
}

func TestSettingsMigratesCurrentAntispamGoldenToEveryEffectiveGroup(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "state", "antispam-legacy-current.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	antispamPath := filepath.Join(dir, "antispam.json")
	if err := os.WriteFile(antispamPath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	const runtimeGroup int64 = -1009000000003
	current := settingsFile{
		Version:          SettingsSchemaVersion - 1,
		ControlGroupID:   testGroupA,
		RegisteredGroups: []RegisteredGroup{{ID: runtimeGroup, RegisteredBy: 42}},
		Groups:           map[int64]groupRecord{},
	}
	data, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	settings, err := NewSettings(settingsPath, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	wantWhitelist := []int64{-1007000000003, -1007000000001, -1007000000002}
	for _, groupID := range []int64{testGroupA, testGroupB, runtimeGroup} {
		group, ok := settings.Group(groupID)
		if !ok {
			t.Fatalf("migrated group %d is absent", groupID)
		}
		if group.Revision() != 1 {
			t.Fatalf("migrated group %d revision = %d, want 1", groupID, group.Revision())
		}
		if got := group.AntispamEnabled(); !got.Value || got.Source != SourceRuntime {
			t.Fatalf("migrated group %d antispam = %+v", groupID, got)
		}
		if got := group.ChannelWhitelist(); !reflect.DeepEqual(got.Value, wantWhitelist) || got.Source != SourceRuntime {
			t.Fatalf("migrated group %d whitelist = %+v", groupID, got)
		}
	}
	var migrated settingsFile
	decodeFile(t, settingsPath, &migrated)
	if migrated.Version != SettingsSchemaVersion || len(migrated.Groups) != 3 {
		t.Fatalf("migrated settings = version:%d groups:%d", migrated.Version, len(migrated.Groups))
	}
	afterMigration, err := os.ReadFile(antispamPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterMigration, fixture) {
		t.Fatalf("legacy antispam changed during migration\nwant %s\n got %s", fixture, afterMigration)
	}

	group, _ := settings.Group(testGroupA)
	override := group.Overrides()
	override.AntispamEnabled = ptr(false)
	empty := []int64{}
	override.ChannelWhitelist = &empty
	if _, err := settings.CommitGroup(group.ID(), group.Revision(), override); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewSettings(settingsPath, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	group, _ = reloaded.Group(testGroupA)
	if got := group.AntispamEnabled(); got.Value {
		t.Fatalf("legacy antispam reapplied after migration: %+v", got)
	}
	if got := group.ChannelWhitelist(); len(got.Value) != 0 {
		t.Fatalf("legacy whitelist reapplied after migration: %+v", got)
	}
	afterReload, err := os.ReadFile(antispamPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterReload, fixture) {
		t.Fatalf("legacy antispam changed after reload\nwant %s\n got %s", fixture, afterReload)
	}
}

func TestSettingsMalformedLegacyAntispamDisablesWritesWithoutChangingFile(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "antispam.json")
	before := []byte(`{"enabled":`)
	if err := os.WriteFile(legacyPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, "settings.json")
	settings, err := NewSettings(settingsPath, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	if status := settings.Persistence(); status.Writable || status.LastError == nil {
		t.Fatalf("malformed legacy persistence status = %+v", status)
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("malformed legacy antispam changed\nwant %s\n got %s", before, after)
	}
	if _, err := os.Stat(legacyPath + ".corrupt"); !os.IsNotExist(err) {
		t.Fatalf("malformed legacy antispam was moved: %v", err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("settings file written after failed migration: %v", err)
	}
}

func TestSettingsRejectsStaleGroupRevision(t *testing.T) {
	settings, err := NewSettings(filepath.Join(t.TempDir(), "settings.json"), testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	firstView, _ := settings.Group(testGroupA)
	secondView, _ := settings.Group(testGroupA)
	first := firstView.Overrides()
	first.Enabled = ptr(false)
	if _, err := settings.CommitGroup(testGroupA, firstView.Revision(), first); err != nil {
		t.Fatalf("first writer: %v", err)
	}
	second := secondView.Overrides()
	second.NameSpoiler = ptr(false)
	_, err = settings.CommitGroup(testGroupA, secondView.Revision(), second)
	if !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("second writer error = %v, want ErrSettingsConflict", err)
	}
	group, _ := settings.Group(testGroupA)
	if group.Enabled().Value || !group.NameSpoiler().Value || group.Revision() != 1 {
		t.Fatalf("published state after conflict = enabled:%v spoiler:%v revision:%d", group.Enabled().Value, group.NameSpoiler().Value, group.Revision())
	}
	t.Logf("second writer refused: %v", err)
}

func TestSettingsWriteFailurePublishesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "settings.json")
	settings, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	group, _ := settings.Group(testGroupA)
	next := group.Overrides()
	next.Enabled = ptr(false)
	if _, err := settings.CommitGroup(testGroupA, group.Revision(), next); err == nil {
		t.Fatal("commit unexpectedly succeeded")
	}
	group, _ = settings.Group(testGroupA)
	if !group.Enabled().Value || group.Revision() != 0 {
		t.Fatalf("failed write published enabled:%v revision:%d", group.Enabled().Value, group.Revision())
	}
}

func TestSettingsUnknownVersionPreservesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	before := []byte(`{"version":99,"groups":{"-1009000000001":{"revision":7,"enabled":false}},"future":"keep"}`)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	if status := settings.Persistence(); status.Writable || status.Durable || status.LastError == nil {
		t.Fatalf("future-version persistence status = %+v", status)
	}
	group, _ := settings.Group(testGroupA)
	next := group.Overrides()
	next.Enabled = ptr(false)
	if _, err := settings.CommitGroup(testGroupA, group.Revision(), next); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("future-version commit error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("future-version file changed\nwant %s\n got %s", before, after)
	}
}

func TestOwnerClaimExpiryAndEnrollmentIssuance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000_000, 0)
	first, created, err := settings.EnsureOwnerClaim(now, time.Minute)
	if err != nil || !created || first == "" {
		t.Fatalf("first owner claim = %q, created %t, error %v", first, created, err)
	}
	if err := settings.ClaimOwner(42, first, now.Add(time.Minute)); !errors.Is(err, ErrOwnerClaimInvalid) {
		t.Fatalf("expired owner claim error = %v", err)
	}
	replacement, created, err := settings.EnsureOwnerClaim(now.Add(time.Minute), time.Minute)
	if err != nil || !created || replacement == "" || replacement == first {
		t.Fatalf("replacement owner claim = %q, created %t, error %v", replacement, created, err)
	}
	if err := settings.ClaimOwner(42, replacement, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := settings.ClaimOwner(43, replacement, now.Add(time.Minute)); !errors.Is(err, ErrOwnerClaimInvalid) {
		t.Fatalf("used owner claim error = %v", err)
	}
	if _, err := settings.IssueEnrollmentNonce(43, now, time.Minute); !errors.Is(err, ErrRegistrationOwnerOnly) {
		t.Fatalf("non-owner enrollment error = %v", err)
	}
	issued, err := settings.IssueEnrollmentNonce(42, now, time.Minute)
	if err != nil || issued.Nonce == "" || issued.IssuedBy != 42 {
		t.Fatalf("owner enrollment nonce = %+v, error %v", issued, err)
	}
	reloaded, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	state := reloaded.Registrations()
	if state.OwnerID != 42 || state.OwnerClaimNonce != "" || len(state.EnrollmentNonces) != 1 {
		t.Fatalf("registration state after reload = %+v", state)
	}
}

func TestSettingsRegistrationRoundTripAndRuntimeOnlyPolicy(t *testing.T) {
	runtimeOnly, err := NewSettings("", testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	group, _ := runtimeOnly.Group(testGroupA)
	nextGroup := group.Overrides()
	nextGroup.Enabled = ptr(false)
	result, err := runtimeOnly.CommitGroup(testGroupA, group.Revision(), nextGroup)
	if err != nil {
		t.Fatal(err)
	}
	if result.Durable {
		t.Fatalf("runtime-only commit reported durable: %+v", result)
	}
	if _, err := runtimeOnly.CommitRegistrations(0, runtimeOnly.Registrations()); !errors.Is(err, ErrSettingsNotDurable) {
		t.Fatalf("runtime-only registration error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "settings.json")
	settings, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	registration := settings.Registrations()
	registration.OwnerID = 42
	registration.ControlGroupID = -1009000000003
	registration.RegisteredGroups = []RegisteredGroup{{ID: -1009000000003, RegisteredBy: 42, Title: "Runtime"}}
	registration.EnrollmentNonces = []EnrollmentNonce{{Nonce: "enroll", IssuedBy: 42, ExpiresAt: 1000}}
	registration.PendingRegistrations = []PendingRegistration{{GroupID: -1009000000004, RegisteredBy: 42, Title: "Pending", ExpiresAt: 2000}}
	committed, err := settings.CommitRegistrations(registration.Revision, registration)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Revision != 1 || !committed.Durable || !settings.IsGroup(-1009000000003) {
		t.Fatalf("registration commit = %+v, effective=%v", committed, settings.GroupIDs())
	}
	runtimeGroup, _ := settings.Group(-1009000000003)
	runtimeOverrides := runtimeGroup.Overrides()
	runtimeOverrides.TimeoutSeconds = ptr(900)
	if _, err := settings.CommitGroup(runtimeGroup.ID(), runtimeGroup.Revision(), runtimeOverrides); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.GroupIDs(); !reflect.DeepEqual(got, []int64{testGroupA, testGroupB, -1009000000003}) {
		t.Fatalf("effective groups after reload = %v", got)
	}
	metadata := reloaded.Registrations()
	if metadata.Revision != 1 || metadata.OwnerID != 42 || metadata.OwnerClaimNonce != "" || len(metadata.EnrollmentNonces) != 1 || len(metadata.PendingRegistrations) != 1 {
		t.Fatalf("registration metadata after reload = %+v", metadata)
	}
	runtimeGroup, _ = reloaded.Group(-1009000000003)
	if runtimeGroup.TimeoutSeconds().Value != 900 || runtimeGroup.TimeoutSeconds().Source != SourceRuntime {
		t.Fatalf("runtime group timeout after reload = %+v", runtimeGroup.TimeoutSeconds())
	}
}

func TestSettingsEveryCommitPreservesRegistrationAndLegacyMirrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	registration := settings.Registrations()
	registration.OwnerID = 99
	if _, err := settings.CommitRegistrations(registration.Revision, registration); err != nil {
		t.Fatal(err)
	}
	group, _ := settings.Group(testGroupA)
	overrides := group.Overrides()
	overrides.NameSpoiler = ptr(false)
	if _, err := settings.CommitGroup(testGroupA, group.Revision(), overrides); err != nil {
		t.Fatal(err)
	}
	global := settings.Global()
	if _, err := settings.CommitGlobal(global.Revision(), GlobalOverrides{RichMessages: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	var state settingsFile
	decodeFile(t, path, &state)
	if state.OwnerID != 99 || state.OwnerClaimNonce != "" {
		t.Fatalf("metadata lost after setters: %#v", state)
	}
	if state.Enabled == nil || !*state.Enabled || state.NameSpoiler == nil || *state.NameSpoiler || state.VerifyMode != config.ModeKernel {
		t.Fatalf("legacy mirrors after setters = %#v", state)
	}
}

func TestSettingsUnreadableExistingPathDisablesWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	settings, err := NewSettings(path, testSettingsBaseline())
	if err != nil {
		t.Fatal(err)
	}
	if status := settings.Persistence(); status.Writable || status.LastError == nil {
		t.Fatalf("unreadable path status = %+v", status)
	}
	group, _ := settings.Group(testGroupA)
	overrides := group.Overrides()
	overrides.Enabled = ptr(false)
	if _, err := settings.CommitGroup(testGroupA, group.Revision(), overrides); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("commit error = %v, want ErrSettingsUnavailable", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("unreadable state path was overwritten")
	}
}

func decodeFile(t *testing.T, path string, dst any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode %s: %v\n%s", path, err, data)
	}
}

func ptr[T any](value T) *T { return &value }
