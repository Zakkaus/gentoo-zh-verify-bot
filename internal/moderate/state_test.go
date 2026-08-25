package moderate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

const (
	stateCompatGroupA int64 = -1001234500001
	stateCompatGroupB int64 = -1001234500002
)

func TestWarningsPersistence(t *testing.T) {
	stateDirectory := t.TempDir()
	telegram := newFakeMod()
	service := newTestService(t, &config.Config{}, telegram, stateDirectory)
	cleared := warningKey{groupID: -200, userID: 9}
	service.warnings.counters[warningKey{groupID: -100, userID: 7}] = 1
	service.warnings.counters[warningKey{groupID: -100, userID: 8}] = 3
	service.warnings.counters[warningKey{groupID: -200, userID: 7}] = 2
	service.warnings.counters[cleared] = 4
	service.warnings.save()
	delete(service.warnings.counters, cleared)
	service.warnings.save()

	restored := newTestService(t, &config.Config{}, newFakeMod(), stateDirectory)
	for _, test := range []struct {
		key  warningKey
		want int
	}{
		{key: warningKey{groupID: -100, userID: 7}, want: 1},
		{key: warningKey{groupID: -100, userID: 8}, want: 3},
		{key: warningKey{groupID: -200, userID: 7}, want: 2},
	} {
		if got := restored.warnings.counters[test.key]; got != test.want {
			t.Errorf("restored warning %v = %d, want %d", test.key, got, test.want)
		}
	}
	if got, ok := restored.warnings.counters[cleared]; ok {
		t.Errorf("cleared warning %v came back with count %d", cleared, got)
	}
}

func TestWarningGoldenCompatibility(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "state", "warns.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[warningKey]int{
		{groupID: stateCompatGroupA, userID: 7101}: 1,
		{groupID: stateCompatGroupA, userID: 7102}: 2,
		{groupID: stateCompatGroupB, userID: 7101}: 4,
	}
	withUnknown := warningFixtureWithUnknown(t, fixture)
	for _, test := range []struct {
		name      string
		data      []byte
		roundTrip bool
	}{
		{name: "current", data: fixture, roundTrip: true},
		{name: "unknown record key", data: withUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateDirectory := t.TempDir()
			path := filepath.Join(stateDirectory, "warns.json")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			service := newTestService(t, &config.Config{}, newFakeMod(), stateDirectory)
			if !reflect.DeepEqual(service.warnings.counters, want) {
				t.Fatalf("loaded warnings = %#v, want %#v", service.warnings.counters, want)
			}
			if test.roundTrip {
				out := filepath.Join(t.TempDir(), "warns.json")
				service.warnings.path = out
				service.warnings.save()
				got, err := os.ReadFile(out)
				if err != nil {
					t.Fatal(err)
				}
				assertStableWarningJSON(t, fixture, got)
			}
		})
	}
}

func TestWarningReadErrorDisablesWrites(t *testing.T) {
	stateDirectory := t.TempDir()
	if err := os.Mkdir(filepath.Join(stateDirectory, "warns.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, &config.Config{}, newFakeMod(), stateDirectory)
	if service.warnings.path != "" {
		t.Errorf("write path remains %q after read failure", service.warnings.path)
	}
}

func TestWarnCounterBound(t *testing.T) {
	for _, test := range []struct {
		name         string
		evicted      warningKey
		evictedCount int
	}{
		{name: "lowest count is evicted", evicted: warningKey{groupID: -200, userID: 1}, evictedCount: 1},
		{name: "key order breaks equal-count ties", evicted: warningKey{groupID: -200, userID: 1}, evictedCount: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newWarningState("")
			for index := range warnCounterMax {
				state.counters[warningKey{groupID: -100, userID: int64(index + 1)}] = 2
			}
			state.counters[test.evicted] = test.evictedCount - 1
			state.increment(test.evicted.groupID, test.evicted.userID)

			if len(state.counters) != warnCounterMax {
				t.Fatalf("warning counters = %d, want %d", len(state.counters), warnCounterMax)
			}
			if _, ok := state.counters[test.evicted]; ok {
				t.Errorf("eviction candidate %v remains in warning counters", test.evicted)
			}
		})
	}
}

// Regenerate only by explicit request; historical fixtures are never rewritten implicitly.
func TestGenerateWarningFixture(t *testing.T) {
	if os.Getenv("UPDATE_STATE_COMPAT_FIXTURES") != "1" {
		t.Skip("set UPDATE_STATE_COMPAT_FIXTURES=1 to regenerate state compatibility fixtures")
	}
	stateDirectory := filepath.Join("..", "..", "testdata", "state")
	service := newTestService(t, &config.Config{}, newFakeMod(), stateDirectory)
	service.warnings.counters = map[warningKey]int{
		{groupID: stateCompatGroupA, userID: 7101}: 1,
		{groupID: stateCompatGroupA, userID: 7102}: 2,
		{groupID: stateCompatGroupB, userID: 7101}: 4,
	}
	service.warnings.save()
}

func warningFixtureWithUnknown(t *testing.T, fixture []byte) []byte {
	t.Helper()
	var records []map[string]any
	if err := json.Unmarshal(fixture, &records); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		record["future_key"] = true
	}
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertStableWarningJSON(t *testing.T, want, got []byte) {
	t.Helper()
	decode := func(data []byte) []warningRecord {
		var records []warningRecord
		if err := json.Unmarshal(data, &records); err != nil {
			t.Fatal(err)
		}
		sort.Slice(records, func(i, j int) bool {
			if records[i].GroupID != records[j].GroupID {
				return records[i].GroupID < records[j].GroupID
			}
			return records[i].UserID < records[j].UserID
		})
		return records
	}
	if wantRecords, gotRecords := decode(want), decode(got); !reflect.DeepEqual(wantRecords, gotRecords) {
		t.Fatalf("warning JSON changed\nwant %s\n got %s", want, got)
	}
}
