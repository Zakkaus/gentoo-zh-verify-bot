package feed

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
)

const (
	stateCompatFeedChat    int64 = -1009876543210
	stateCompatFeedFixture       = "feed--1009876543210.json"
)

type stateCompatTrackedWant struct {
	msgID        int
	state        string
	misses       int
	editFails    int
	confirmTries int
	status       string
}

type stateCompatLegacyFeedState struct {
	LastBugID   int                                 `json:"last_bug_id"`
	LastNewsURL string                              `json:"last_news_url"`
	Tracked     map[string]stateCompatLegacyTracked `json:"tracked"`
}

type stateCompatLegacyTracked struct {
	MsgID  int    `json:"msg_id"`
	Status string `json:"status"`
}

// TestStateCompatGenerateFeedFixtures regenerates only feed fixtures when explicitly requested.
func TestStateCompatGenerateFeedFixtures(t *testing.T) {
	if os.Getenv("UPDATE_STATE_COMPAT_FIXTURES") != "1" {
		t.Skip("set UPDATE_STATE_COMPAT_FIXTURES=1 to regenerate state compatibility fixtures")
	}

	dir := filepath.Join("..", "..", "testdata", "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	saveFeedState(feedStatePath(dir, stateCompatFeedChat), feedState{
		LastBugID:   980004,
		LastNewsURL: "https://www.gentoo.org/support/news-items/2026-08-24-state-compat.html",
		Tracked: map[string]*trackedBug{
			"980001": {MsgID: 6001, State: "UNCONFIRMED|", Misses: 2, ConfirmTries: 1},
			"980002": {MsgID: 6002, State: "CONFIRMED|", EditFails: 3, ConfirmTries: 2},
			"980003": {MsgID: 6003, State: "RESOLVED|FIXED", Misses: 4, EditFails: 5, ConfirmTries: 6},
			"980004": {MsgID: 6004, State: "RESOLVED|INVALID"},
		},
	})
	if err := store.Write(filepath.Join(dir, "feed-legacy-status.json"), stateCompatLegacyFeedState{
		LastBugID:   880002,
		LastNewsURL: "https://www.gentoo.org/support/news-items/legacy.html",
		Tracked: map[string]stateCompatLegacyTracked{
			"880001": {MsgID: 4001, Status: "UNCONFIRMED"},
			"880002": {MsgID: 4002, Status: "IN_PROGRESS"},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestStateCompatFeed pins current round trips, unknown-field tolerance, and legacy status migration.
func TestStateCompatFeed(t *testing.T) {
	fixture := stateCompatFeedFixtureBytes(t, stateCompatFeedFixture)
	currentTracked := map[string]stateCompatTrackedWant{
		"980001": {msgID: 6001, state: "UNCONFIRMED|", misses: 2, confirmTries: 1},
		"980002": {msgID: 6002, state: "CONFIRMED|", editFails: 3, confirmTries: 2},
		"980003": {msgID: 6003, state: "RESOLVED|FIXED", misses: 4, editFails: 5, confirmTries: 6},
		"980004": {msgID: 6004, state: "RESOLVED|INVALID"},
	}
	legacy := stateCompatFeedFixtureBytes(t, "feed-legacy-status.json")
	legacyTracked := map[string]stateCompatTrackedWant{
		"880001": {msgID: 4001, state: "UNCONFIRMED|"},
		"880002": {msgID: 4002, state: "IN_PROGRESS|"},
	}
	tests := []struct {
		name        string
		data        []byte
		lastBugID   int
		lastNewsURL string
		tracked     map[string]stateCompatTrackedWant
		roundTrip   bool
		legacy      bool
	}{
		{
			name: "current", data: fixture, lastBugID: 980004,
			lastNewsURL: "https://www.gentoo.org/support/news-items/2026-08-24-state-compat.html",
			tracked:     currentTracked, roundTrip: true,
		},
		{
			name: "unknown top-level key", data: stateCompatFeedWithUnknown(t, fixture), lastBugID: 980004,
			lastNewsURL: "https://www.gentoo.org/support/news-items/2026-08-24-state-compat.html",
			tracked:     currentTracked,
		},
		{
			name: "legacy tracked status", data: legacy, lastBugID: 880002,
			lastNewsURL: "https://www.gentoo.org/support/news-items/legacy.html",
			tracked:     legacyTracked, legacy: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := stateCompatFeedTempFile(t, stateCompatFeedFixture, tt.data)
			got := loadFeedState(path)
			stateCompatAssertFeed(t, got, tt.lastBugID, tt.lastNewsURL, tt.tracked)
			if tt.roundTrip {
				out := filepath.Join(t.TempDir(), stateCompatFeedFixture)
				saveFeedState(out, got)
				stateCompatFeedAssertStableJSON(t, fixture, stateCompatFeedRead(t, out))
			}
			if tt.legacy {
				out := filepath.Join(t.TempDir(), "feed-legacy-migrated.json")
				saveFeedState(out, got)
				var migrated map[string]any
				stateCompatFeedDecode(t, stateCompatFeedRead(t, out), &migrated)
				tracked := migrated["tracked"].(map[string]any)
				for id, want := range legacyTracked {
					rec := tracked[id].(map[string]any)
					if rec["state"] != want.state {
						t.Errorf("migrated feed bug %s state = %v, want %q", id, rec["state"], want.state)
					}
					if _, exists := rec["status"]; exists {
						t.Errorf("migrated feed bug %s retained legacy status: %#v", id, rec)
					}
				}
			}
		})
	}
}

func stateCompatAssertFeed(t *testing.T, got feedState, lastBugID int, lastNewsURL string, want map[string]stateCompatTrackedWant) {
	t.Helper()
	if got.LastBugID != lastBugID || got.LastNewsURL != lastNewsURL || len(got.Tracked) != len(want) {
		t.Fatalf("loaded feed header/tracked count = %+v, want last_bug_id=%d last_news_url=%q tracked=%d", got, lastBugID, lastNewsURL, len(want))
	}
	for id, expected := range want {
		rec := got.Tracked[id]
		if rec == nil {
			t.Errorf("missing tracked feed bug %s", id)
			continue
		}
		if rec.MsgID != expected.msgID || rec.State != expected.state || rec.Misses != expected.misses ||
			rec.EditFails != expected.editFails || rec.ConfirmTries != expected.confirmTries || rec.Status != expected.status {
			t.Errorf("loaded tracked feed bug %s = %+v, want %+v", id, rec, expected)
		}
	}
}

func stateCompatFeedFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	return stateCompatFeedRead(t, filepath.Join("..", "..", "testdata", "state", name))
}

func stateCompatFeedTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func stateCompatFeedRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func stateCompatFeedDecode(t *testing.T, data []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode feed state JSON: %v\n%s", err, data)
	}
}

func stateCompatFeedWithUnknown(t *testing.T, data []byte) []byte {
	t.Helper()
	var root map[string]any
	stateCompatFeedDecode(t, data, &root)
	root["future_compat_key"] = map[string]any{"schema": float64(99), "value": "preserve known fields"}
	out, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func stateCompatFeedAssertStableJSON(t *testing.T, want, got []byte) {
	t.Helper()
	var wantValue, gotValue any
	stateCompatFeedDecode(t, want, &wantValue)
	stateCompatFeedDecode(t, got, &gotValue)
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("feed decoded round trip changed\nwant: %#v\n got: %#v", wantValue, gotValue)
	}
	wantJSON, err := json.Marshal(wantValue)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(gotValue)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("feed normalized raw JSON changed\nwant: %s\n got: %s", wantJSON, gotJSON)
	}
}
