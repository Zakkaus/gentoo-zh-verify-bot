package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

const (
	stateCompatGroupA int64 = -1001234500001
	stateCompatGroupB int64 = -1001234500002
)

func stateCompatConfig() *config.Config {
	return &config.Config{Groups: []config.GroupConfig{{ID: stateCompatGroupA}, {ID: stateCompatGroupB}},
		GroupIDs:       []int64{stateCompatGroupA, stateCompatGroupB},
		TimeoutSeconds: 240,
		VerifyMaxFails: 3}
}

func TestStateCompatAntispamMigration(t *testing.T) {
	fixture := stateCompatFixture(t, "antispam.json")
	wantWhitelist := []int64{
		-1007000000003,
		-1007000000001,
		-1007000000002,
	}
	tests := []struct {
		name string
		data []byte
	}{
		{name: "current", data: fixture},
		{name: "unknown top-level key", data: stateCompatWithUnknown(t, fixture)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			legacyPath := filepath.Join(dir, "antispam.json")
			if err := os.WriteFile(legacyPath, tt.data, 0o600); err != nil {
				t.Fatal(err)
			}
			settings, err := NewSettings(
				filepath.Join(dir, "settings.json"),
				settingsBaselineFromConfig(stateCompatConfig(), configPresence{}),
			)
			if err != nil {
				t.Fatal(err)
			}
			for _, groupID := range []int64{stateCompatGroupA, stateCompatGroupB} {
				group, ok := settings.Group(groupID)
				if !ok {
					t.Fatalf("group %d missing after antispam migration", groupID)
				}
				if !group.AntispamEnabled().Value || !reflect.DeepEqual(group.ChannelWhitelist().Value, wantWhitelist) {
					t.Fatalf("group %d antispam = enabled:%v whitelist:%v", groupID, group.AntispamEnabled().Value, group.ChannelWhitelist().Value)
				}
			}
			if after := stateCompatRead(t, legacyPath); !bytes.Equal(after, tt.data) {
				t.Fatal("legacy antispam fixture changed during migration")
			}
		})
	}
}

func TestStateCompatSettings(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		schemaV2 bool
	}{
		{name: "existing legacy fixture", data: stateCompatFixture(t, "settings.json")},
		{name: "legacy v0 golden", data: stateCompatFixture(t, "settings-legacy-v0.json")},
		{name: "schema v2 golden", data: stateCompatFixture(t, "settings-v2.json"), schemaV2: true},
		{name: "unknown legacy key", data: stateCompatWithUnknown(t, stateCompatFixture(t, "settings.json"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := stateCompatTempFile(t, "settings.json", tt.data)
			settings, err := NewSettings(path, settingsBaselineFromConfig(stateCompatConfig(), configPresence{}))
			if err != nil {
				t.Fatal(err)
			}
			if tt.schemaV2 {
				var want, got map[string]any
				stateCompatDecode(t, tt.data, &want)
				want["version"] = float64(SettingsSchemaVersion)
				stateCompatDecode(t, stateCompatRead(t, path), &got)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("schema-v2 migration changed fields beyond the version and delivery-mode migration:\nwant %#v\n got %#v", want, got)
				}
				for _, groupID := range settings.GroupIDs() {
					group, _ := settings.Group(groupID)
					if group.DeliveryMode().Value != config.DeliveryBoth {
						t.Fatalf("group %d delivery mode = %q, want default %q",
							groupID, group.DeliveryMode().Value, config.DeliveryBoth)
					}
				}
				return
			}
			for _, groupID := range []int64{stateCompatGroupA, stateCompatGroupB} {
				group, ok := settings.Group(groupID)
				if !ok {
					t.Fatalf("group %d missing after migration", groupID)
				}
				if group.Enabled().Value || group.NameSpoiler().Value || group.VerifyMode().Value != config.ModeMixed ||
					group.DeliveryMode().Value != config.DeliveryBoth {
					t.Fatalf("group %d settings = enabled:%v name_spoiler:%v verify_mode:%q delivery_mode:%q",
						groupID, group.Enabled().Value, group.NameSpoiler().Value, group.VerifyMode().Value, group.DeliveryMode().Value)
				}
			}
			var migrated map[string]any
			stateCompatDecode(t, stateCompatRead(t, path), &migrated)
			if migrated["version"] != float64(SettingsSchemaVersion) {
				t.Fatalf("migrated settings version = %v", migrated["version"])
			}
			if groups, ok := migrated["groups"].(map[string]any); !ok || len(groups) != 2 {
				t.Fatalf("migrated settings groups = %#v", migrated["groups"])
			}
		})
	}
}

func stateCompatFixture(t *testing.T, name string) []byte {
	t.Helper()
	return stateCompatRead(t, filepath.Join("..", "..", "testdata", "state", name))
}

func stateCompatTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func stateCompatRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func stateCompatDecode(t *testing.T, data []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode state JSON: %v\n%s", err, data)
	}
}

func stateCompatWithUnknown(t *testing.T, data []byte) []byte {
	t.Helper()
	var root any
	stateCompatDecode(t, data, &root)
	future := map[string]any{"schema": float64(99), "value": "preserve known fields"}
	switch value := root.(type) {
	case map[string]any:
		value["future_compat_key"] = future
	case []any:
		if len(value) == 0 {
			t.Fatal("cannot add an unknown record key to an empty fixture")
		}
		record, ok := value[0].(map[string]any)
		if !ok {
			t.Fatalf("fixture first record is %T, want object", value[0])
		}
		record["future_compat_key"] = future
	default:
		t.Fatalf("fixture root is %T, want object or array", root)
	}
	out, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
