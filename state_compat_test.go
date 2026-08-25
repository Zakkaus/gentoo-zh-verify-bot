package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

const (
	stateCompatGroupA   int64 = -1001234500001
	stateCompatGroupB   int64 = -1001234500002
	stateCompatFeedChat int64 = -1009876543210

	stateCompatFeedFixture = "feed--1009876543210.json"
)

var (
	stateCompatKernelDeadline = time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	stateCompatQuizDeadline   = time.Date(2099, 1, 2, 4, 5, 6, 0, time.UTC)
	stateCompatLegacyDeadline = time.Date(2099, 1, 3, 5, 6, 7, 0, time.UTC)
	stateCompatHeartbeat      = time.Date(2026, 8, 24, 12, 34, 56, 0, time.UTC)
	stateCompatStrikeA        = time.Date(2026, 8, 24, 10, 11, 12, 0, time.UTC)
	stateCompatStrikeB        = time.Date(2026, 8, 24, 11, 12, 13, 0, time.UTC)
)

type stateCompatPendingWant struct {
	userID          int64
	groupID         int64
	groupMsgID      int
	mode            string
	lang            string
	fbAnswers       []string
	prompted        bool
	hinted          bool
	sampleBounced   bool
	noLinuxReminded bool
	osClarified     bool
	tries           int
	qText           string
	qOpts           []string
	correctIdx      int
	nonce           string
	name            string
	deadline        time.Time
}

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

func stateCompatConfig() *Config {
	return &Config{
		Groups:         []GroupConfig{{ID: stateCompatGroupA}, {ID: stateCompatGroupB}},
		GroupIDs:       []int64{stateCompatGroupA, stateCompatGroupB},
		TimeoutSeconds: 240,
		VerifyMaxFails: 3,
	}
}

// Regenerate only by explicit request. Every current fixture below is emitted through its owner save path.
func TestStateCompatGenerateFixtures(t *testing.T) {
	if os.Getenv("UPDATE_STATE_COMPAT_FIXTURES") != "1" {
		t.Skip("set UPDATE_STATE_COMPAT_FIXTURES=1 to regenerate state compatibility fixtures")
	}

	dir := filepath.Join("testdata", "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	pendingV := NewVerifier(stateCompatConfig())
	pendingV.statePath = filepath.Join(dir, "pending.json")
	pendingV.pend[pkey{stateCompatGroupA, 7001}] = &pending{
		groupMsgID: 501, mode: modeKernel, lang: langEN,
		fbAnswers: []string{"gentoozh.org", "gentoozh"}, prompted: true,
		hinted: true, sampleBounced: true, noLinuxReminded: true, osClarified: true, tries: 2,
		qText: "Name the Gentoo Chinese community website", correctIdx: -1,
		nonce: "kernel-compat-nonce", name: "Kernel Applicant", deadline: stateCompatKernelDeadline,
	}
	pendingV.pend[pkey{stateCompatGroupB, 7002}] = &pending{
		groupMsgID: 502, mode: modeQuiz, lang: langZHT, prompted: true,
		qText: "Select the package manager", qOpts: []string{"apt", "Portage", "dnf"}, correctIdx: 1,
		nonce: "quiz-compat-nonce", name: "Quiz Applicant", deadline: stateCompatQuizDeadline,
	}
	pendingV.save()

	warnV := NewVerifier(stateCompatConfig())
	warnV.warnPath = filepath.Join(dir, "warns.json")
	warnV.warns[pkey{stateCompatGroupA, 7101}] = 1
	warnV.warns[pkey{stateCompatGroupA, 7102}] = 2
	warnV.warns[pkey{stateCompatGroupB, 7101}] = 4
	warnV.saveWarns()

	antispamV := NewVerifier(stateCompatConfig())
	antispamV.acPath = filepath.Join(dir, "antispam.json")
	antispamV.acOn = true
	antispamV.acWhite = map[int64]bool{
		-1007000000001: true,
		-1007000000002: true,
		-1007000000003: true,
	}
	antispamV.saveAntispam()

	strikeV := NewVerifier(stateCompatConfig())
	strikeV.vfailPath = filepath.Join(dir, "verifyfail.json")
	strikeV.vfail[pkey{stateCompatGroupA, 7201}] = &vfailRec{count: 2, last: stateCompatStrikeA}
	strikeV.vfail[pkey{stateCompatGroupB, 7202}] = &vfailRec{count: 3, last: stateCompatStrikeB}
	strikeV.saveVerifyFails()

	settingsV := &Verifier{
		settingsPath: filepath.Join(dir, "settings.json"),
		enabled:      false,
		nameSpoiler:  false,
		vmode:        modeMixed,
	}
	settingsV.saveSettings()

	heartbeatV := NewVerifier(stateCompatConfig())
	heartbeatV.hbPath = filepath.Join(dir, "heartbeat.json")
	heartbeatV.lastOnline = stateCompatHeartbeat
	heartbeatV.saveHeartbeat()

	agentV := NewVerifier(stateCompatConfig())
	agentV.agentPath = filepath.Join(dir, "agents.json")
	for _, claim := range []string{
		"model=gpt-5", "model=gpt-5", "model=gpt-5",
		"model=claude-opus-4.5", "model=claude-opus-4.5",
		"model=gemini-2.5-pro",
	} {
		agentV.recordAgent(claim)
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

	legacyPendingV := NewVerifier(stateCompatConfig())
	legacyPendingV.statePath = filepath.Join(dir, "pending-legacy-no-mode.json")
	legacyPendingV.pend[pkey{stateCompatGroupA, 7301}] = &pending{
		groupMsgID: 503,
		qText:      "Legacy quiz question",
		qOpts:      []string{"Portage", "apt"},
		correctIdx: 0,
		nonce:      "legacy-quiz-nonce",
		name:       "Legacy Applicant",
		deadline:   stateCompatLegacyDeadline,
	}
	legacyPendingV.save()

	writeJSONFile(filepath.Join(dir, "feed-legacy-status.json"), stateCompatLegacyFeedState{
		LastBugID:   880002,
		LastNewsURL: "https://www.gentoo.org/support/news-items/legacy.html",
		Tracked: map[string]stateCompatLegacyTracked{
			"880001": {MsgID: 4001, Status: "UNCONFIRMED"},
			"880002": {MsgID: 4002, Status: "IN_PROGRESS"},
		},
	})
}

func TestStateCompatPending(t *testing.T) {
	current := stateCompatFixture(t, "pending.json")
	currentWant := []stateCompatPendingWant{
		{
			userID: 7001, groupID: stateCompatGroupA, groupMsgID: 501, mode: "kernel", lang: "en",
			fbAnswers: []string{"gentoozh.org", "gentoozh"}, prompted: true,
			hinted: true, sampleBounced: true, noLinuxReminded: true, osClarified: true, tries: 2,
			qText: "Name the Gentoo Chinese community website", correctIdx: -1,
			nonce: "kernel-compat-nonce", name: "Kernel Applicant", deadline: stateCompatKernelDeadline,
		},
		{
			userID: 7002, groupID: stateCompatGroupB, groupMsgID: 502, mode: "quiz", lang: "zh-hant",
			prompted: true, qText: "Select the package manager",
			qOpts: []string{"apt", "Portage", "dnf"}, correctIdx: 1,
			nonce: "quiz-compat-nonce", name: "Quiz Applicant", deadline: stateCompatQuizDeadline,
		},
	}
	legacy := stateCompatFixture(t, "pending-legacy-no-mode.json")
	legacyWant := []stateCompatPendingWant{{
		userID: 7301, groupID: stateCompatGroupA, groupMsgID: 503, mode: "quiz",
		qText: "Legacy quiz question", qOpts: []string{"Portage", "apt"}, correctIdx: 0,
		nonce: "legacy-quiz-nonce", name: "Legacy Applicant", deadline: stateCompatLegacyDeadline,
	}}

	tests := []struct {
		name      string
		data      []byte
		want      []stateCompatPendingWant
		roundTrip bool
		legacy    bool
	}{
		{name: "current", data: current, want: currentWant, roundTrip: true},
		{name: "unknown record key", data: stateCompatWithUnknown(t, current), want: currentWant},
		{name: "legacy missing mode and language", data: legacy, want: legacyWant, legacy: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := stateCompatTempFile(t, "pending.json", tt.data)
			v := NewVerifier(stateCompatConfig())
			v.statePath = path
			v.load(nil)
			t.Cleanup(v.stopForShutdown)
			stateCompatAssertPending(t, v, tt.want)

			if tt.roundTrip {
				out := filepath.Join(t.TempDir(), "pending.json")
				v.statePath = out
				v.save()
				stateCompatAssertStableJSON(t, "pending", current, stateCompatRead(t, out))
			}
			if tt.legacy {
				out := filepath.Join(t.TempDir(), "pending.json")
				v.statePath = out
				v.save()
				var migrated []map[string]any
				stateCompatDecode(t, stateCompatRead(t, out), &migrated)
				if len(migrated) != 1 || migrated[0]["mode"] != "quiz" {
					t.Fatalf("legacy pending migration = %#v, want one record with mode=quiz", migrated)
				}
				if _, ok := migrated[0]["lang"]; ok {
					t.Fatalf("legacy pending unexpectedly gained an explicit language: %#v", migrated[0])
				}
			}
		})
	}
}

func TestStateCompatWarnings(t *testing.T) {
	fixture := stateCompatFixture(t, "warns.json")
	want := map[pkey]int{
		{stateCompatGroupA, 7101}: 1,
		{stateCompatGroupA, 7102}: 2,
		{stateCompatGroupB, 7101}: 4,
	}
	tests := []struct {
		name      string
		data      []byte
		roundTrip bool
	}{
		{name: "current", data: fixture, roundTrip: true},
		{name: "unknown record key", data: stateCompatWithUnknown(t, fixture)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewVerifier(stateCompatConfig())
			v.warnPath = stateCompatTempFile(t, "warns.json", tt.data)
			v.loadWarns()
			v.mu.Lock()
			got := make(map[pkey]int, len(v.warns))
			for key, count := range v.warns {
				got[key] = count
			}
			v.mu.Unlock()
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("loaded warnings = %#v, want %#v", got, want)
			}
			if tt.roundTrip {
				out := filepath.Join(t.TempDir(), "warns.json")
				v.warnPath = out
				v.saveWarns()
				stateCompatAssertStableJSON(t, "warnings", fixture, stateCompatRead(t, out))
			}
		})
	}
}

func TestStateCompatAntispam(t *testing.T) {
	fixture := stateCompatFixture(t, "antispam.json")
	wantWhite := map[int64]bool{
		-1007000000001: true,
		-1007000000002: true,
		-1007000000003: true,
	}
	tests := []struct {
		name      string
		data      []byte
		roundTrip bool
	}{
		{name: "current", data: fixture, roundTrip: true},
		{name: "unknown top-level key", data: stateCompatWithUnknown(t, fixture)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewVerifier(stateCompatConfig())
			v.acPath = stateCompatTempFile(t, "antispam.json", tt.data)
			v.loadAntispam()
			v.acMu.RLock()
			on := v.acOn
			gotWhite := make(map[int64]bool, len(v.acWhite))
			for id, allowed := range v.acWhite {
				gotWhite[id] = allowed
			}
			v.acMu.RUnlock()
			if !on || !reflect.DeepEqual(gotWhite, wantWhite) {
				t.Fatalf("loaded antispam = enabled:%v whitelist:%#v, want enabled:true whitelist:%#v", on, gotWhite, wantWhite)
			}
			if tt.roundTrip {
				out := filepath.Join(t.TempDir(), "antispam.json")
				v.acPath = out
				v.saveAntispam()
				stateCompatAssertStableJSON(t, "antispam", fixture, stateCompatRead(t, out))
			}
		})
	}
}

func TestStateCompatVerificationFailures(t *testing.T) {
	fixture := stateCompatFixture(t, "verifyfail.json")
	want := map[pkey]struct {
		count int
		last  int64
	}{
		{stateCompatGroupA, 7201}: {count: 2, last: stateCompatStrikeA.Unix()},
		{stateCompatGroupB, 7202}: {count: 3, last: stateCompatStrikeB.Unix()},
	}
	tests := []struct {
		name      string
		data      []byte
		roundTrip bool
	}{
		{name: "current", data: fixture, roundTrip: true},
		{name: "unknown record key", data: stateCompatWithUnknown(t, fixture)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewVerifier(stateCompatConfig())
			v.vfailPath = stateCompatTempFile(t, "verifyfail.json", tt.data)
			v.loadVerifyFails()
			v.mu.Lock()
			got := make(map[pkey]struct {
				count int
				last  int64
			}, len(v.vfail))
			for key, rec := range v.vfail {
				got[key] = struct {
					count int
					last  int64
				}{count: rec.count, last: rec.last.Unix()}
			}
			v.mu.Unlock()
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("loaded verification failures = %#v, want %#v", got, want)
			}
			if tt.roundTrip {
				out := filepath.Join(t.TempDir(), "verifyfail.json")
				v.vfailPath = out
				v.saveVerifyFails()
				stateCompatAssertStableJSON(t, "verification failures", fixture, stateCompatRead(t, out))
			}
		})
	}
}

func TestStateCompatSettings(t *testing.T) {
	fixture := stateCompatFixture(t, "settings.json")
	tests := []struct {
		name      string
		data      []byte
		roundTrip bool
	}{
		{name: "current", data: fixture, roundTrip: true},
		{name: "unknown top-level key", data: stateCompatWithUnknown(t, fixture)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &Verifier{
				settingsPath: stateCompatTempFile(t, "settings.json", tt.data),
				enabled:      true,
				nameSpoiler:  true,
			}
			v.loadSettings()
			v.mu.Lock()
			enabled, spoiler, mode := v.enabled, v.nameSpoiler, v.vmode
			v.mu.Unlock()
			if enabled || spoiler || mode != "mixed" {
				t.Fatalf("loaded settings = enabled:%v name_spoiler:%v verify_mode:%q, want false, false, mixed", enabled, spoiler, mode)
			}
			if tt.roundTrip {
				out := filepath.Join(t.TempDir(), "settings.json")
				v.settingsPath = out
				v.saveSettings()
				stateCompatAssertStableJSON(t, "settings", fixture, stateCompatRead(t, out))
			}
		})
	}
}

func TestStateCompatHeartbeat(t *testing.T) {
	fixture := stateCompatFixture(t, "heartbeat.json")
	tests := []struct {
		name      string
		data      []byte
		roundTrip bool
	}{
		{name: "current", data: fixture, roundTrip: true},
		{name: "unknown top-level key", data: stateCompatWithUnknown(t, fixture)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &Verifier{hbPath: stateCompatTempFile(t, "heartbeat.json", tt.data)}
			got := v.loadHeartbeat()
			if !got.Equal(stateCompatHeartbeat) {
				t.Fatalf("loaded heartbeat = %v, want %v", got, stateCompatHeartbeat)
			}
			if tt.roundTrip {
				out := filepath.Join(t.TempDir(), "heartbeat.json")
				v.mu.Lock()
				v.lastOnline = got
				v.mu.Unlock()
				v.hbPath = out
				v.saveHeartbeat()
				stateCompatAssertStableJSON(t, "heartbeat", fixture, stateCompatRead(t, out))
			}
		})
	}
}

func TestStateCompatAgentTally(t *testing.T) {
	fixture := stateCompatFixture(t, "agents.json")
	wantCounts := map[string]int{
		"gpt-5":           3,
		"claude-opus-4.5": 2,
		"gemini-2.5-pro":  1,
	}
	tests := []struct {
		name      string
		data      []byte
		roundTrip bool
	}{
		{name: "current", data: fixture, roundTrip: true},
		{name: "unknown top-level key", data: stateCompatWithUnknown(t, fixture)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewVerifier(stateCompatConfig())
			v.agentPath = stateCompatTempFile(t, "agents.json", tt.data)
			v.loadAgents()
			v.agentMu.Lock()
			total := v.agents.Total
			counts := copyCounts(v.agents.Counts)
			v.agentMu.Unlock()
			if total != 6 || !reflect.DeepEqual(counts, wantCounts) {
				t.Fatalf("loaded agent tally = total:%d counts:%#v, want total:6 counts:%#v", total, counts, wantCounts)
			}
			if tt.roundTrip {
				out := filepath.Join(t.TempDir(), "agents.json")
				// recordAgent is the owner writer and necessarily increments. Reuse its exact ordered snapshot path without mutation.
				saveJSONFile(out, func() any {
					v.agentMu.Lock()
					defer v.agentMu.Unlock()
					return agentTally{Total: v.agents.Total, Counts: copyCounts(v.agents.Counts)}
				})
				stateCompatAssertStableJSON(t, "agent tally", fixture, stateCompatRead(t, out))
			}
		})
	}
}

func TestStateCompatFeed(t *testing.T) {
	fixture := stateCompatFixture(t, stateCompatFeedFixture)
	currentTracked := map[string]stateCompatTrackedWant{
		"980001": {msgID: 6001, state: "UNCONFIRMED|", misses: 2, confirmTries: 1},
		"980002": {msgID: 6002, state: "CONFIRMED|", editFails: 3, confirmTries: 2},
		"980003": {msgID: 6003, state: "RESOLVED|FIXED", misses: 4, editFails: 5, confirmTries: 6},
		"980004": {msgID: 6004, state: "RESOLVED|INVALID"},
	}
	legacy := stateCompatFixture(t, "feed-legacy-status.json")
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
			name: "unknown top-level key", data: stateCompatWithUnknown(t, fixture), lastBugID: 980004,
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
			path := stateCompatTempFile(t, stateCompatFeedFixture, tt.data)
			got := loadFeedState(path)
			stateCompatAssertFeed(t, got, tt.lastBugID, tt.lastNewsURL, tt.tracked)
			if tt.roundTrip {
				out := filepath.Join(t.TempDir(), stateCompatFeedFixture)
				saveFeedState(out, got)
				stateCompatAssertStableJSON(t, "feed", fixture, stateCompatRead(t, out))
			}
			if tt.legacy {
				out := filepath.Join(t.TempDir(), "feed-legacy-migrated.json")
				saveFeedState(out, got)
				var migrated map[string]any
				stateCompatDecode(t, stateCompatRead(t, out), &migrated)
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

func stateCompatAssertPending(t *testing.T, v *Verifier, want []stateCompatPendingWant) {
	t.Helper()
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.pend) != len(want) {
		t.Fatalf("loaded pending count = %d, want %d: %#v", len(v.pend), len(want), v.pend)
	}
	for _, expected := range want {
		got, ok := v.pend[pkey{expected.groupID, expected.userID}]
		if !ok {
			t.Errorf("missing pending group=%d user=%d", expected.groupID, expected.userID)
			continue
		}
		if got.groupMsgID != expected.groupMsgID || got.mode != expected.mode || string(got.lang) != expected.lang ||
			!reflect.DeepEqual(got.fbAnswers, expected.fbAnswers) || got.prompted != expected.prompted ||
			got.hinted != expected.hinted || got.sampleBounced != expected.sampleBounced ||
			got.noLinuxReminded != expected.noLinuxReminded || got.osClarified != expected.osClarified ||
			got.tries != expected.tries || got.qText != expected.qText || !reflect.DeepEqual(got.qOpts, expected.qOpts) ||
			got.correctIdx != expected.correctIdx || got.nonce != expected.nonce || got.name != expected.name ||
			!got.deadline.Equal(expected.deadline) {
			t.Errorf("loaded pending group=%d user=%d = %+v, want %+v", expected.groupID, expected.userID, got, expected)
		}
		if expected.lang == "" && tr(got.lang) != tr(langZH) {
			t.Errorf("legacy pending language fallback = %q, want Simplified Chinese", got.lang)
		}
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

func stateCompatFixture(t *testing.T, name string) []byte {
	t.Helper()
	return stateCompatRead(t, filepath.Join("testdata", "state", name))
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

func stateCompatAssertStableJSON(t *testing.T, artifact string, want, got []byte) {
	t.Helper()
	wantValue := stateCompatNormalizedJSON(t, artifact, want)
	gotValue := stateCompatNormalizedJSON(t, artifact, got)
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("%s decoded round trip changed\nwant: %#v\n got: %#v", artifact, wantValue, gotValue)
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
		t.Errorf("%s normalized raw JSON changed\nwant: %s\n got: %s", artifact, wantJSON, gotJSON)
	}
}

func stateCompatNormalizedJSON(t *testing.T, artifact string, data []byte) any {
	t.Helper()
	var root any
	stateCompatDecode(t, data, &root)
	switch artifact {
	case "pending", "warnings", "verification failures":
		records, ok := root.([]any)
		if !ok {
			t.Fatalf("%s fixture root is %T, want array", artifact, root)
		}
		sort.Slice(records, func(i, j int) bool {
			a := records[i].(map[string]any)
			b := records[j].(map[string]any)
			if a["group_id"].(float64) != b["group_id"].(float64) {
				return a["group_id"].(float64) < b["group_id"].(float64)
			}
			return a["user_id"].(float64) < b["user_id"].(float64)
		})
	case "antispam":
		object, ok := root.(map[string]any)
		if !ok {
			t.Fatalf("antispam fixture root is %T, want object", root)
		}
		whitelist, ok := object["whitelist"].([]any)
		if !ok {
			t.Fatalf("antispam whitelist is %T, want array", object["whitelist"])
		}
		sort.Slice(whitelist, func(i, j int) bool {
			return whitelist[i].(float64) < whitelist[j].(float64)
		})
	}
	return root
}
