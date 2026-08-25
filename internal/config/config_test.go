package config

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// writeConfig writes a temp config.json and returns its path (cleaned up by t).
func writeConfig(t *testing.T, c map[string]any) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "cfg-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(f).Encode(c); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

var sampleQ = []map[string]any{{"q": "x", "options": []string{"a", "b"}, "answer": 0}}

func TestLoadConfigMissingAndEmptyAllowNoGroups(t *testing.T) {
	paths := []string{
		filepath.Join(t.TempDir(), "missing.json"),
		writeConfig(t, map[string]any{}),
	}
	for _, path := range paths {
		loaded, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig(%q): %v", path, err)
		}
		if len(loaded.Groups) != 0 || len(loaded.GroupIDs) != 0 {
			t.Fatalf("LoadConfig(%q) groups = %v / %v, want none", path, loaded.Groups, loaded.GroupIDs)
		}
		if loaded.TimeoutSeconds != 240 || loaded.WarnLimit != 3 || loaded.PrivateQueryPerMin != 3 {
			t.Fatalf("LoadConfig(%q) defaults = timeout %d, warn %d, private rate %d",
				path, loaded.TimeoutSeconds, loaded.WarnLimit, loaded.PrivateQueryPerMin)
		}
	}

	if _, err := LoadConfig(t.TempDir()); err == nil {
		t.Fatal("an existing unreadable config path was treated as missing")
	}
}

func TestLoadConfigLegacy(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids":           []int{-100, -200},
		"required_channel_id": -300,
		"channel_display":     "@x",
		"questions":           sampleQ,
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(c.Groups) != 2 || !c.IsGroup(-100) || !c.IsGroup(-200) {
		t.Fatalf("groups not merged: %+v", c.GroupIDs)
	}
	if c.RequiredChannel(-100) != -300 || c.ChannelDisplayFor(-100) != "@x" || len(c.QuestionsFor(-100)) != 1 {
		t.Errorf("global fallback wrong for -100")
	}
	if !c.IsKnownChat(-300) { // the required channel must not be auto-left
		t.Errorf("required channel -300 should be IsKnownChat")
	}
}

func TestLoadConfigPerGroup(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{
		"groups": []map[string]any{
			{"id": -100, "required_channel_id": -400, "channel_display": "@y", "questions": sampleQ},
			{"id": -200}, // inherits globals
		},
		"required_channel_id": -300,
		"channel_display":     "@x",
		"questions":           sampleQ,
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.RequiredChannel(-100) != -400 || c.ChannelDisplayFor(-100) != "@y" {
		t.Errorf("group -100 override not applied")
	}
	if c.RequiredChannel(-200) != -300 || c.ChannelDisplayFor(-200) != "@x" {
		t.Errorf("group -200 fallback not applied")
	}
	if !c.IsKnownChat(-400) || !c.IsKnownChat(-300) {
		t.Errorf("both required channels should be IsKnownChat")
	}
}

func TestLoadConfigValidation(t *testing.T) {
	if _, err := LoadConfig(writeConfig(t, map[string]any{
		"groups":    []map[string]any{{"id": -100, "required_channel_id": -400}}, // no @handle / invite url
		"questions": sampleQ,
	})); err == nil {
		t.Errorf("expected error for required channel with no reachable link")
	}
	if _, err := LoadConfig(writeConfig(t, map[string]any{
		"verify_mode": ModeQuiz,
	})); err == nil {
		t.Errorf("expected error for a quiz-mode runtime-group baseline with no questions")
	}
	if _, err := LoadConfig(writeConfig(t, map[string]any{
		"required_channel_id": -400,
	})); err == nil {
		t.Errorf("expected error for a runtime-group channel baseline with no reachable link")
	}
	if _, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids":   []int{-100},
		"verify_mode": ModeQuiz, // quiz mode but no questions at all
	})); err == nil {
		t.Errorf("expected error for a quiz-mode group with no questions")
	}
	if _, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids":   []int{-100},
		"verify_mode": ModeKernel, // kernel mode asks for uname -r — no pool needed
	})); err != nil {
		t.Errorf("kernel mode should load without questions: %v", err)
	}
	if _, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids":   []int{-100},
		"questions":   sampleQ,
		"verify_mode": "buttons", // not a mode
	})); err == nil {
		t.Errorf("expected error for an unknown verify_mode")
	}
	if _, err := LoadConfig(writeConfig(t, map[string]any{
		"groups":    []map[string]any{{"id": -100, "verify_mode": "kenrel"}}, // typo in a per-group mode
		"questions": sampleQ,
	})); err == nil {
		t.Errorf("expected error for an unknown per-group verify_mode")
	}
}

func TestLoadConfigRejectsUnknownDeliveryModes(t *testing.T) {
	for _, value := range []map[string]any{
		{"delivery_mode": "sidecar"},
		{"groups": []map[string]any{{"id": -100, "delivery_mode": "sidecar"}}},
	} {
		if _, err := LoadConfig(writeConfig(t, value)); err == nil {
			t.Errorf("expected error for unknown delivery_mode in %#v", value)
		}
	}
}

func TestDeliveryModeResolution(t *testing.T) {
	loaded, err := LoadConfig(writeConfig(t, map[string]any{
		"delivery_mode": DeliveryDM,
		"groups": []map[string]any{
			{"id": -100, "delivery_mode": DeliveryGroup},
			{"id": -200},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.DeliveryModeFor(-100); got != DeliveryGroup {
		t.Fatalf("group delivery mode = %q, want %q", got, DeliveryGroup)
	}
	if got := loaded.DeliveryModeFor(-200); got != DeliveryDM {
		t.Fatalf("inherited delivery mode = %q, want %q", got, DeliveryDM)
	}
	defaults, err := LoadConfig(writeConfig(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := defaults.DeliveryModeFor(-300); got != DeliveryBoth {
		t.Fatalf("default delivery mode = %q, want %q", got, DeliveryBoth)
	}
}

func TestLoadConfigLanguages(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"group_ids":   []int{-100},
			"verify_mode": ModeKernel,
		}
	}

	defaultConfig, err := LoadConfig(writeConfig(t, base()))
	if err != nil {
		t.Fatal(err)
	}
	if defaultConfig.LangForGroup(-100) != "zh" {
		t.Fatalf("default language = %q, want zh", defaultConfig.LangForGroup(-100))
	}

	configured := base()
	configured["lang"] = "en"
	configured["groups"] = []map[string]any{{"id": -100, "lang": "zh-Hant"}, {"id": -200}}
	configured["group_ids"] = nil
	configured["feeds"] = []map[string]any{{"chat_id": -300, "lang": "zh-Hant"}}
	loaded, err := LoadConfig(writeConfig(t, configured))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LangForGroup(-100) != "zh-Hant" || loaded.LangForGroup(-200) != "en" {
		t.Fatalf("group languages = %q, %q", loaded.LangForGroup(-100), loaded.LangForGroup(-200))
	}
	if loaded.Feeds[0].Lang != "zh-Hant" {
		t.Fatalf("feed language = %q, want zh-Hant", loaded.Feeds[0].Lang)
	}

	for name, mutate := range map[string]func(map[string]any){
		"global": func(value map[string]any) { value["lang"] = "fr" },
		"group": func(value map[string]any) {
			value["groups"] = []map[string]any{{"id": -100, "lang": "zh-hant"}}
			value["group_ids"] = nil
		},
		"feed": func(value map[string]any) {
			value["feeds"] = []map[string]any{{"chat_id": -300, "lang": "de"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := base()
			mutate(value)
			if _, err := LoadConfig(writeConfig(t, value)); err == nil {
				t.Fatal("unsupported language was accepted")
			} else {
				for _, r := range err.Error() {
					if unicode.Is(unicode.Han, r) {
						t.Fatalf("config error contains Han text: %q", err)
					}
				}
			}
		})
	}
}

func TestControlGroupAllowedLocalizesRefusal(t *testing.T) {
	for _, lang := range i18n.Languages() {
		t.Run(lang.String(), func(t *testing.T) {
			cfg := &Config{ControlGroupID: -100, Lang: lang.String()}
			allowed, got := cfg.ControlGroupAllowed(-200)
			want := i18n.Messages.Feed.Config.ControlGroupOnly.Render(lang, int64(-100))
			if allowed || got != want {
				t.Errorf("ControlGroupAllowed(-200) = (%v, %q), want (false, %q)", allowed, got, want)
			}
		})
	}
}

func TestLoadConfigLeavesPrivateReplyForRenderer(t *testing.T) {
	value := map[string]any{"group_ids": []int{-100}, "verify_mode": ModeKernel}
	loaded, err := LoadConfig(writeConfig(t, value))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PrivateReply != "" {
		t.Fatalf("default private_reply = %q, want empty catalogue fallback", loaded.PrivateReply)
	}

	const override = "Use this configured reply verbatim."
	value["private_reply"] = override
	loaded, err = LoadConfig(writeConfig(t, value))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PrivateReply != override {
		t.Errorf("configured private_reply = %q, want %q", loaded.PrivateReply, override)
	}
}

func TestTimeoutSecondsClamp(t *testing.T) {
	load := func(ts any) *Config {
		m := map[string]any{"group_ids": []int{-100}, "questions": sampleQ}
		if ts != nil {
			m["timeout_seconds"] = ts
		}
		c, err := LoadConfig(writeConfig(t, m))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := load(1); c.TimeoutSeconds != 30 {
		t.Errorf("timeout_seconds:1 should clamp to the 30s floor, got %d", c.TimeoutSeconds)
	}
	if c := load(nil); c.TimeoutSeconds != 240 {
		t.Errorf("omitted timeout_seconds should default to 240, got %d", c.TimeoutSeconds)
	}
	if c := load(99999); c.TimeoutSeconds != 1800 {
		t.Errorf("oversized timeout_seconds should cap at 1800, got %d", c.TimeoutSeconds)
	}
}

func TestLoadConfigRejectsOverflowingDurationSeconds(t *testing.T) {
	const overflowing = int64(1<<63 - 1)
	tests := []struct {
		name string
		key  string
		data map[string]any
	}{
		{name: "feed interval", key: "interval_seconds", data: map[string]any{
			"feeds": []map[string]any{{"chat_id": -1001, "interval_seconds": overflowing}},
		}},
		{name: "verification timeout", key: "timeout_seconds", data: map[string]any{"timeout_seconds": overflowing}},
		{name: "notification TTL", key: "notify_ttl_seconds", data: map[string]any{"notify_ttl_seconds": overflowing}},
		{name: "lookup TTL", key: "lookup_ttl_seconds", data: map[string]any{"lookup_ttl_seconds": overflowing}},
		{name: "ban duration", key: "ban_seconds", data: map[string]any{"ban_seconds": overflowing}},
		{name: "mute duration", key: "mute_seconds", data: map[string]any{"mute_seconds": overflowing}},
		{name: "verification cooldown", key: "verify_retry_seconds", data: map[string]any{"verify_retry_seconds": overflowing}},
		{name: "owner claim lifetime", key: "owner_claim_lifetime_seconds", data: map[string]any{"owner_claim_lifetime_seconds": overflowing}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, test.data))
			if err == nil {
				t.Fatalf("%s accepted an overflowing duration", test.key)
			}
			if message := err.Error(); !strings.Contains(message, test.key) || !strings.Contains(message, "accepted range") {
				t.Fatalf("%s error = %q, want key and accepted range", test.key, message)
			}
		})
	}
}

func TestLoadConfigRejectsOperationallyUselessFeedInterval(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, map[string]any{
		"feeds": []map[string]any{{"chat_id": -1001, "interval_seconds": 86401}},
	}))
	if err == nil {
		t.Fatal("interval_seconds above one day was accepted")
	}
	if message := err.Error(); !strings.Contains(message, "interval_seconds") || !strings.Contains(message, "1..86400 seconds") {
		t.Fatalf("interval_seconds error = %q, want key and 1..86400-second positive range", message)
	}
}

func TestTrustedGroupsResolver(t *testing.T) {
	c := &Config{
		TrustedMemberGroupIDs: []int64{-100},
		Groups: []GroupConfig{
			{ID: -1}, // omitted (nil) -> inherit global
			{ID: -2, TrustedMemberGroupIDs: []int64{-200, -300}}, // non-empty -> override
			{ID: -3, TrustedMemberGroupIDs: []int64{}},           // explicit [] -> DISABLE (opt out of global)
		},
	}
	if got := c.TrustedGroups(-1); len(got) != 1 || got[0] != -100 {
		t.Errorf("group -1 (omitted) should inherit the global [-100], got %v", got)
	}
	if got := c.TrustedGroups(-2); len(got) != 2 || got[0] != -200 {
		t.Errorf("group -2 should use its per-group override, got %v", got)
	}
	if got := c.TrustedGroups(-3); len(got) != 0 {
		t.Errorf("group -3 with explicit [] must DISABLE the bypass (no inheritance), got %v", got)
	}
	if got := c.TrustedGroups(-999); len(got) != 1 || got[0] != -100 {
		t.Errorf("an unknown group should get the global default, got %v", got)
	}
}

func TestIsKnownChatTrusted(t *testing.T) {
	c := &Config{
		GroupIDs:              []int64{-1, -2},
		Groups:                []GroupConfig{{ID: -1}, {ID: -2, TrustedMemberGroupIDs: []int64{-500}}},
		TrustedMemberGroupIDs: []int64{-400},
	}
	for _, id := range []int64{-400, -500} {
		if !c.IsKnownChat(id) {
			t.Errorf("trusted source group %d must be a known chat (never auto-left)", id)
		}
	}
	if c.IsKnownChat(-99999) {
		t.Error("an unrelated chat must NOT be known")
	}
}

func TestIsKnownChatExtra(t *testing.T) {
	c := &Config{
		GroupIDs:     []int64{-1},
		Groups:       []GroupConfig{{ID: -1}},
		KnownChatIDs: []int64{-1001166068646},
	}
	if !c.IsKnownChat(-1001166068646) {
		t.Error("a known_chat_ids chat must be a known chat (never auto-left)")
	}
	if len(c.TrustedGroups(-1)) != 0 {
		t.Error("known_chat_ids must NOT add a trusted bypass source")
	}
	if c.IsKnownChat(-77777) {
		t.Error("an unrelated chat must NOT be known")
	}
}

func TestLoadConfigKnownChats(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{
		"known_chat_ids": []int64{-1001166068646},
		"groups":         []map[string]any{{"id": -1003265952923}},
		"questions":      sampleQ,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsKnownChat(-1001166068646) {
		t.Error("a known_chat_ids chat must be a known chat")
	}
	if len(c.TrustedMemberGroupIDs) != 0 {
		t.Error("known_chat_ids must not populate trusted sources")
	}
}

func TestLoadConfigTrustedGroups(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{
		"trusted_member_group_ids": []int64{-1001163306055},
		"groups": []map[string]any{
			{"id": -1003265952923, "trusted_member_group_ids": []int64{-1001163306055}},
		},
		"questions": sampleQ,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedMemberGroupIDs) != 1 || c.TrustedMemberGroupIDs[0] != -1001163306055 {
		t.Errorf("top-level trusted_member_group_ids not parsed: %v", c.TrustedMemberGroupIDs)
	}
	if got := c.TrustedGroups(-1003265952923); len(got) != 1 || got[0] != -1001163306055 {
		t.Errorf("per-group trusted_member_group_ids not resolved: %v", got)
	}
	if !c.IsKnownChat(-1001163306055) {
		t.Error("the trusted source group must be a known chat (so auto-leave won't kick the bot)")
	}
}

func TestLoadConfigTrustedDisable(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{
		"trusted_member_group_ids": []int64{-1001163306055},
		"groups": []map[string]any{
			{"id": -100}, // omitted -> inherit global
			{"id": -200, "trusted_member_group_ids": []int64{}}, // explicit [] -> disable
		},
		"questions": sampleQ,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.TrustedGroups(-100); len(got) != 1 || got[0] != -1001163306055 {
		t.Errorf("group -100 (omitted) should inherit the global, got %v", got)
	}
	if got := c.TrustedGroups(-200); len(got) != 0 {
		t.Errorf("group -200 with explicit [] must DISABLE the bypass (no inheritance), got %v", got)
	}
}

func TestWarnLimitDefault(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{"group_ids": []int{-100}, "questions": sampleQ}))
	if err != nil {
		t.Fatal(err)
	}
	if c.WarnLimit != 3 {
		t.Errorf("WarnLimit default = %d, want 3", c.WarnLimit)
	}
}

func TestPrivateQueryRateDefault(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{"group_ids": []int{-100}, "questions": sampleQ}))
	if err != nil {
		t.Fatal(err)
	}
	if c.PrivateQueryPerMin != 3 {
		t.Errorf("default PrivateQueryPerMin = %d, want 3", c.PrivateQueryPerMin)
	}
}

func captureLoadConfigLog(t *testing.T, config map[string]any) (*Config, string, error) {
	t.Helper()
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	c, err := LoadConfig(writeConfig(t, config))
	return c, output.String(), err
}

func TestLoadConfigWarnsUnknownKeys(t *testing.T) {
	c, output, err := captureLoadConfigLog(t, map[string]any{
		"control_group_id": -100,
		"timeout_second":   240,
		"questions":        sampleQ,
		"groups": []map[string]any{{
			"id":           -100,
			"verify_modes": "quiz",
		}},
		"feeds": []map[string]any{{
			"chat_id":         -300,
			"interval_second": 300,
		}},
	})
	if err != nil {
		t.Fatalf("unknown keys must not reject the config: %v", err)
	}
	if c.ControlGroupID != -100 {
		t.Errorf("ControlGroupID = %d, want -100", c.ControlGroupID)
	}
	for _, want := range []string{
		`WARNING: config: unknown key "timeout_second"`,
		`WARNING: config groups[0]: unknown key "verify_modes"`,
		`WARNING: config feeds[0]: unknown key "interval_second"`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("startup log missing %q:\n%s", want, output)
		}
	}
}

func TestLoadConfigWarnsAboutSharedGlobalControl(t *testing.T) {
	tests := []struct {
		name      string
		controlID int64
		wantCount int
	}{
		{name: "unset", wantCount: 1},
		{name: "configured", controlID: -100, wantCount: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := map[string]any{
				"group_ids": []int64{-100, -200},
				"questions": sampleQ,
			}
			if tt.controlID != 0 {
				config["control_group_id"] = tt.controlID
			}
			_, output, err := captureLoadConfigLog(t, config)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(output, "WARNING: control_group_id is unset"); got != tt.wantCount {
				t.Errorf("warning count = %d, want %d:\n%s", got, tt.wantCount, output)
			}
		})
	}
}

func TestControlGroupIDMustBeGuarded(t *testing.T) {
	if _, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids":        []int{-100},
		"control_group_id": -999, // not a guarded group
		"questions":        sampleQ,
	})); err == nil {
		t.Error("expected an error for a control_group_id outside the guarded groups")
	}
	if _, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids":        []int{-100, -200},
		"control_group_id": -200,
		"questions":        sampleQ,
	})); err != nil {
		t.Errorf("a control_group_id naming a guarded group should load: %v", err)
	}
}

func TestConfigClampDurations(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids": []int{-100}, "questions": sampleQ, "ban_seconds": 10, "mute_seconds": 10,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.BanSeconds != 30 || c.MuteSeconds != 30 {
		t.Errorf("sub-30s clamp: ban=%d mute=%d, want 30/30", c.BanSeconds, c.MuteSeconds)
	}
	c, err = LoadConfig(writeConfig(t, map[string]any{
		"group_ids": []int{-100}, "questions": sampleQ, "ban_seconds": 40000000, "mute_seconds": 40000000,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.BanSeconds != 0 {
		t.Errorf("over-366d ban_seconds should clamp to 0 (permanent), got %d", c.BanSeconds)
	}
	if c.MuteSeconds != telegramBanMax {
		t.Errorf("over-366d mute_seconds should cap to %d, got %d", telegramBanMax, c.MuteSeconds)
	}
}

func TestMuteSecondsDefault(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{"group_ids": []int{-100}, "questions": sampleQ}))
	if err != nil {
		t.Fatal(err)
	}
	if c.MuteSeconds != 3600 {
		t.Errorf("default MuteSeconds = %d, want 3600 (1h)", c.MuteSeconds)
	}
}

func TestLoadConfigExample(t *testing.T) {
	if _, err := LoadConfig(filepath.Join("..", "..", "config.example.json")); err != nil {
		t.Fatalf("load config.example.json: %v", err)
	}
}

func TestOwnerClaimSecurityConfig(t *testing.T) {
	defaults, err := LoadConfig(writeConfig(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if defaults.OwnerClaimLifetime() != 10*time.Minute {
		t.Fatalf("default owner claim lifetime = %s, want 10m", defaults.OwnerClaimLifetime())
	}

	configured, err := LoadConfig(writeConfig(t, map[string]any{
		"owner_claim_lifetime_seconds": 45,
		"owner_claim_user_id":          4242,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if configured.OwnerClaimLifetime() != 45*time.Second || configured.OwnerClaimUserID != 4242 {
		t.Fatalf("owner claim config = lifetime %s, user %d", configured.OwnerClaimLifetime(), configured.OwnerClaimUserID)
	}

	for key, value := range map[string]any{
		"owner_claim_lifetime_seconds": -1,
		"owner_claim_user_id":          -1,
	} {
		t.Run(key, func(t *testing.T) {
			if _, err := LoadConfig(writeConfig(t, map[string]any{key: value})); err == nil {
				t.Fatalf("negative %s was accepted", key)
			}
		})
	}
}
