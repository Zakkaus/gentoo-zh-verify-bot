package main

import (
	"encoding/json"
	"os"
	"testing"
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

// TestLoadConfigLegacy verifies backward compatibility: a bare group_ids list plus global
// settings is merged into the canonical Groups, and the per-group accessors fall back to
// the globals — so an existing config keeps working unchanged.
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
	if c.requiredChannel(-100) != -300 || c.channelDisplay(-100) != "@x" || len(c.questions(-100)) != 1 {
		t.Errorf("global fallback wrong for -100")
	}
	if !c.IsKnownChat(-300) { // the required channel must not be auto-left
		t.Errorf("required channel -300 should be IsKnownChat")
	}
}

// TestLoadConfigPerGroup verifies per-group overrides win over the globals, and that a
// per-group required channel is protected from auto-leave.
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
	if c.requiredChannel(-100) != -400 || c.channelDisplay(-100) != "@y" {
		t.Errorf("group -100 override not applied")
	}
	if c.requiredChannel(-200) != -300 || c.channelDisplay(-200) != "@x" {
		t.Errorf("group -200 fallback not applied")
	}
	if !c.IsKnownChat(-400) || !c.IsKnownChat(-300) {
		t.Errorf("both required channels should be IsKnownChat")
	}
}

// TestLoadConfigValidation checks that a misconfiguration fails fast at load (instead of
// half-breaking verification at runtime): a required channel with no reachable link, and
// a group with no questions anywhere.
func TestLoadConfigValidation(t *testing.T) {
	if _, err := LoadConfig(writeConfig(t, map[string]any{
		"groups":    []map[string]any{{"id": -100, "required_channel_id": -400}}, // no @handle / invite url
		"questions": sampleQ,
	})); err == nil {
		t.Errorf("expected error for required channel with no reachable link")
	}
	if _, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids": []int{-100}, // no questions at all
	})); err == nil {
		t.Errorf("expected error for a group with no questions")
	}
}

// TestTimeoutSecondsClamp verifies the verification timeout is clamped sane: a too-small value (a
// typo) is raised to the 30s floor so the challenge stays winnable, an oversized one is capped, and
// an omitted one takes the default.
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

// TestTrustedGroupsResolver: a per-group trusted_member_group_ids overrides the global default;
// otherwise (and for unknown groups) the global default applies.
func TestTrustedGroupsResolver(t *testing.T) {
	c := &Config{
		TrustedMemberGroupIDs: []int64{-100},
		Groups: []GroupConfig{
			{ID: -1}, // omitted (nil) -> inherit global
			{ID: -2, TrustedMemberGroupIDs: []int64{-200, -300}}, // non-empty -> override
			{ID: -3, TrustedMemberGroupIDs: []int64{}},           // explicit [] -> DISABLE (opt out of global)
		},
	}
	if got := c.trustedGroups(-1); len(got) != 1 || got[0] != -100 {
		t.Errorf("group -1 (omitted) should inherit the global [-100], got %v", got)
	}
	if got := c.trustedGroups(-2); len(got) != 2 || got[0] != -200 {
		t.Errorf("group -2 should use its per-group override, got %v", got)
	}
	if got := c.trustedGroups(-3); len(got) != 0 {
		t.Errorf("group -3 with explicit [] must DISABLE the bypass (no inheritance), got %v", got)
	}
	if got := c.trustedGroups(-999); len(got) != 1 || got[0] != -100 {
		t.Errorf("an unknown group should get the global default, got %v", got)
	}
}

// TestIsKnownChatTrusted: trusted source groups (global + per-group) must count as known chats, so
// the auto-leave logic never kicks the bot out of a group it needs to read membership from.
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

// TestIsKnownChatExtra: known_chat_ids keeps the bot in a chat it only posts to (e.g. an announcement
// channel) WITHOUT making it a trusted bypass source — the bot neither auto-leaves it nor skips its members.
func TestIsKnownChatExtra(t *testing.T) {
	c := &Config{
		GroupIDs:     []int64{-1},
		Groups:       []GroupConfig{{ID: -1}},
		KnownChatIDs: []int64{-1001166068646},
	}
	if !c.IsKnownChat(-1001166068646) {
		t.Error("a known_chat_ids chat must be a known chat (never auto-left)")
	}
	if len(c.trustedGroups(-1)) != 0 {
		t.Error("known_chat_ids must NOT add a trusted bypass source")
	}
	if c.IsKnownChat(-77777) {
		t.Error("an unrelated chat must NOT be known")
	}
}

// TestLoadConfigKnownChats proves known_chat_ids round-trips through LoadConfig and makes the chat
// known (no auto-leave) without turning it into a trusted source.
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

// TestLoadConfigTrustedGroups proves the new field round-trips through LoadConfig (top-level + per-group)
// and that the source group is then a known chat.
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
	if got := c.trustedGroups(-1003265952923); len(got) != 1 || got[0] != -1001163306055 {
		t.Errorf("per-group trusted_member_group_ids not resolved: %v", got)
	}
	if !c.IsKnownChat(-1001163306055) {
		t.Error("the trusted source group must be a known chat (so auto-leave won't kick the bot)")
	}
}

// TestLoadConfigTrustedDisable proves the nil-vs-[] distinction survives JSON: an omitted field
// inherits the global default, while an explicit empty array disables the bypass for that group.
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
	if got := c.trustedGroups(-100); len(got) != 1 || got[0] != -1001163306055 {
		t.Errorf("group -100 (omitted) should inherit the global, got %v", got)
	}
	if got := c.trustedGroups(-200); len(got) != 0 {
		t.Errorf("group -200 with explicit [] must DISABLE the bypass (no inheritance), got %v", got)
	}
}

// TestWarnLimitDefault verifies the documented default is applied when omitted.
func TestWarnLimitDefault(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{"group_ids": []int{-100}, "questions": sampleQ}))
	if err != nil {
		t.Fatal(err)
	}
	if c.WarnLimit != 3 {
		t.Errorf("WarnLimit default = %d, want 3", c.WarnLimit)
	}
}

// TestPrivateQueryRate verifies the per-minute DM lookup limit honours the config (default 3)
// and is per-user; guarded groups are never limited.
func TestPrivateQueryRate(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{"group_ids": []int{-100}, "questions": sampleQ}))
	if err != nil {
		t.Fatal(err)
	}
	if c.PrivateQueryPerMin != 3 {
		t.Errorf("default PrivateQueryPerMin = %d, want 3", c.PrivateQueryPerMin)
	}
	v := NewVerifier(c)
	pass := 0
	for i := 0; i < 5; i++ {
		if v.queryRateOK(7) {
			pass++
		}
	}
	if pass != 3 {
		t.Errorf("user 7: %d/5 allowed, want 3", pass)
	}
	if !v.queryRateOK(8) { // a different user is independent
		t.Errorf("user 8 should be allowed")
	}
}
