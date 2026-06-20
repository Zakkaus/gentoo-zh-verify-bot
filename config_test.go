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
