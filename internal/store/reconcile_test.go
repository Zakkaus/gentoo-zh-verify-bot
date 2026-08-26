package store

import (
	"path/filepath"
	"testing"
)

const testRuntimeGroup int64 = -1009000000009

// Promoting a runtime-registered group into config.json, and retiring the control group, are
// ordinary maintenance. Neither may cost administrators their settings: discarding the stored
// state silently re-enables verification everywhere and unregisters live groups.
func TestConfigDriftKeepsRuntimeDecisions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(base *SettingsBaseline)
	}{
		{
			name: "registered group promoted into config",
			mutate: func(base *SettingsBaseline) {
				promoted := base.DefaultGroup
				promoted.ID = testRuntimeGroup
				base.Groups = append(base.Groups, promoted)
			},
		},
		{
			name: "control group retired from config",
			mutate: func(base *SettingsBaseline) {
				base.Groups = base.Groups[1:] // drop testGroupA, the control group
				base.ControlGroupID = 0
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			first, err := NewSettings(path, testSettingsBaseline())
			if err != nil {
				t.Fatal(err)
			}
			registration := first.Registrations()
			registration.OwnerID = 42
			registration.RegisteredGroups = []RegisteredGroup{{ID: testRuntimeGroup, RegisteredBy: 42, Title: "Runtime"}}
			if _, err := first.CommitRegistrations(registration.Revision, registration); err != nil {
				t.Fatal(err)
			}
			target := testGroupB
			if len(tc.name) > 0 && tc.name[0] == 'c' {
				target = testGroupB // survives both baselines
			}
			group, _ := first.Group(target)
			overrides := group.Overrides()
			disabled := false
			overrides.Enabled = &disabled
			if _, err := first.CommitGroup(target, group.Revision(), overrides); err != nil {
				t.Fatal(err)
			}

			base := testSettingsBaseline()
			tc.mutate(&base)
			second, err := NewSettings(path, base)
			if err != nil {
				t.Fatal(err)
			}
			survived, ok := second.Group(target)
			if !ok {
				t.Fatal("the configured group must survive reconciliation")
			}
			if survived.Enabled().Value || survived.Enabled().Source != SourceRuntime {
				t.Errorf("Enabled = %#v, want the administrator's runtime override to survive", survived.Enabled())
			}
			if got := second.Registrations().OwnerID; got != 42 {
				t.Errorf("OwnerID = %d, want 42: reconciliation must not forget who owns the bot", got)
			}
			if !second.IsGroup(testRuntimeGroup) {
				t.Error("the runtime group is still guarded, whether it is registered or configured")
			}
			if second.Persistence().Writable {
				t.Error("writes stay held until the operator resolves the drift")
			}
		})
	}
}
