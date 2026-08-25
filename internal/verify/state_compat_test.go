package verify

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
)

const (
	stateCompatGroupA int64 = -1001234500001
	stateCompatGroupB int64 = -1001234500002
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
	privateMsgID    int
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

func stateCompatConfig() *config.Config {
	return &config.Config{Groups: []config.GroupConfig{{ID: stateCompatGroupA}, {ID: stateCompatGroupB}},
		GroupIDs:           []int64{stateCompatGroupA, stateCompatGroupB},
		TimeoutSeconds:     240,
		VerifyMaxFails:     3,
		VerifyRetrySeconds: int((60 * 365 * 24 * time.Hour) / time.Second)}
}

// Regenerate only by explicit request. Historical legacy fixtures are never rewritten.
func TestStateCompatGenerateFixtures(t *testing.T) {
	if os.Getenv("UPDATE_STATE_COMPAT_FIXTURES") != "1" {
		t.Skip("set UPDATE_STATE_COMPAT_FIXTURES=1 to regenerate state compatibility fixtures")
	}

	dir := filepath.Join("..", "..", "testdata", "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	pendingV := newTestService(stateCompatConfig())
	pendingV.statePath = filepath.Join(dir, "pending.json")
	pendingV.pend[pkey{stateCompatGroupA, 7001}] = &pending{
		groupMsgID: 501, privateMsgID: 601, mode: config.ModeKernel, lang: i18n.LangEN,
		fbAnswers: []string{"gentoozh.org", "gentoozh"}, prompted: true,
		hinted: true, sampleBounced: true, noLinuxReminded: true, osClarified: true, tries: 2,
		qText: "Name the Gentoo Chinese community website", correctIdx: -1,
		nonce: "kernel-compat-nonce", name: "Kernel Applicant", deadline: stateCompatKernelDeadline,
	}
	pendingV.pend[pkey{stateCompatGroupB, 7002}] = &pending{
		groupMsgID: 502, privateMsgID: 602, mode: config.ModeQuiz, lang: i18n.LangZHHant, prompted: true,
		qText: "Select the package manager", qOpts: []string{"apt", "Portage", "dnf"}, correctIdx: 1,
		nonce: "quiz-compat-nonce", name: "Quiz Applicant", deadline: stateCompatQuizDeadline,
	}
	pendingV.save()

	strikeV := newTestService(stateCompatConfig())
	strikeV.vfailPath = filepath.Join(dir, "verifyfail.json")
	strikeV.vfail[pkey{stateCompatGroupA, 7201}] = &vfailRec{count: 2, last: stateCompatStrikeA}
	strikeV.vfail[pkey{stateCompatGroupB, 7202}] = &vfailRec{count: 3, last: stateCompatStrikeB}
	strikeV.saveVerifyFails()

	heartbeatV := newTestService(stateCompatConfig())
	heartbeatV.hbPath = filepath.Join(dir, "heartbeat.json")
	heartbeatV.lastOnline = stateCompatHeartbeat
	heartbeatV.saveHeartbeat()

	agentV := newTestService(stateCompatConfig())
	agentV.agentPath = filepath.Join(dir, "agents.json")
	for _, claim := range []string{
		"model=gpt-5", "model=gpt-5", "model=gpt-5",
		"model=claude-opus-4.5", "model=claude-opus-4.5",
		"model=gemini-2.5-pro",
	} {
		agentV.recordAgent(claim)
	}
}

func TestStateCompatPending(t *testing.T) {
	current := stateCompatFixture(t, "pending.json")
	currentWant := []stateCompatPendingWant{
		{
			userID: 7001, groupID: stateCompatGroupA, groupMsgID: 501, privateMsgID: 601, mode: "kernel", lang: "en",
			fbAnswers: []string{"gentoozh.org", "gentoozh"}, prompted: true,
			hinted: true, sampleBounced: true, noLinuxReminded: true, osClarified: true, tries: 2,
			qText: "Name the Gentoo Chinese community website", correctIdx: -1,
			nonce: "kernel-compat-nonce", name: "Kernel Applicant", deadline: stateCompatKernelDeadline,
		},
		{
			userID: 7002, groupID: stateCompatGroupB, groupMsgID: 502, privateMsgID: 602, mode: "quiz", lang: "zh-hant",
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
			v := newTestService(stateCompatConfig())
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
			v := newTestService(stateCompatConfig())
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
			v := newTestService(stateCompatConfig())
			v.hbPath = stateCompatTempFile(t, "heartbeat.json", tt.data)
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
			v := newTestService(stateCompatConfig())
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
				if err := store.Save(out, func() any {
					v.agentMu.Lock()
					defer v.agentMu.Unlock()
					return agentTally{Total: v.agents.Total, Counts: copyCounts(v.agents.Counts)}
				}); err != nil {
					t.Fatal(err)
				}
				stateCompatAssertStableJSON(t, "agent tally", fixture, stateCompatRead(t, out))
			}
		})
	}
}

func stateCompatAssertPending(t *testing.T, v *Service, want []stateCompatPendingWant) {
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
		if got.groupMsgID != expected.groupMsgID || got.privateMsgID != expected.privateMsgID ||
			got.mode != expected.mode || got.persistedLang() != expected.lang ||
			!reflect.DeepEqual(got.fbAnswers, expected.fbAnswers) || got.prompted != expected.prompted ||
			got.hinted != expected.hinted || got.sampleBounced != expected.sampleBounced ||
			got.noLinuxReminded != expected.noLinuxReminded || got.osClarified != expected.osClarified ||
			got.tries != expected.tries || got.qText != expected.qText || !reflect.DeepEqual(got.qOpts, expected.qOpts) ||
			got.correctIdx != expected.correctIdx || got.nonce != expected.nonce || got.name != expected.name ||
			!got.deadline.Equal(expected.deadline) {
			t.Errorf("loaded pending group=%d user=%d = %+v, want %+v", expected.groupID, expected.userID, got, expected)
		}
		if expected.lang == "" && got.lang != i18n.LangZH {
			t.Errorf("legacy pending language fallback = %s, want Simplified Chinese", got.lang)
		}
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
