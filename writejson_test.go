package main

import (
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// writeJSONFile is the single atomic-write primitive behind every persisted state file
// (pending.json / warns / feed cursors / settings / verifyfail). These guard the properties a
// restart-critical state file depends on: a clean round-trip, that a marshal failure leaves the
// PRIOR file intact (never a torn/empty state), and that concurrent writers can't corrupt a file.

type wjState struct {
	A int    `json:"a"`
	B string `json:"b"`
}

func TestWriteJSONFileRoundTrip(t *testing.T) {
	path := t.TempDir() + "/state.json"
	writeJSONFile(path, wjState{A: 7, B: "x"})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got wjState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("round-trip JSON invalid: %v", err)
	}
	if got.A != 7 || got.B != "x" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, want 0600 (state is private)", fi.Mode().Perm())
	}
}

func TestWriteJSONFileMarshalFailureKeepsPrior(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"
	writeJSONFile(path, wjState{A: 1, B: "prior"}) // a valid prior state
	prior, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// a value json.Marshal cannot encode (a channel) — writeJSONFile must bail BEFORE touching the file.
	writeJSONFile(path, make(chan int))
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the prior state file must survive a marshal failure, but it's gone: %v", err)
	}
	if string(after) != string(prior) {
		t.Errorf("a marshal failure must leave the prior state intact:\nprior=%q\nafter=%q", prior, after)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("a marshal failure must leave no temp file behind; dir has %d entries", len(ents))
	}
}

func TestLoadJSONFileCorruptBackup(t *testing.T) {
	dir := t.TempDir()

	var dst []int
	if err := loadJSONFile(dir+"/missing.json", &dst); err != nil {
		t.Errorf("a missing file must not be an error (first run), got %v", err)
	}

	okPath := dir + "/ok.json"
	if err := os.WriteFile(okPath, []byte("[1,2,3]"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst = nil
	if err := loadJSONFile(okPath, &dst); err != nil || len(dst) != 3 {
		t.Errorf("a valid file must load: err=%v dst=%v", err, dst)
	}

	bad := dir + "/bad.json"
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadJSONFile(bad, &dst); err == nil {
		t.Error("a corrupt file must return an error")
	}
	if _, err := os.Stat(bad + ".corrupt"); err != nil {
		t.Errorf("the corrupt file must be backed up to .corrupt: %v", err)
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Error("the corrupt file must be renamed away from the live path (so the next save can't clobber it)")
	}
}

func TestLoadStateReadErrorDisablesWrites(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Verifier, string)
		load func(*Verifier)
		path func(*Verifier) string
	}{
		{name: "pending", set: func(v *Verifier, p string) { v.statePath = p }, load: func(v *Verifier) { v.load(nil) }, path: func(v *Verifier) string { return v.statePath }},
		{name: "warns", set: func(v *Verifier, p string) { v.warnPath = p }, load: func(v *Verifier) { v.loadWarns() }, path: func(v *Verifier) string { return v.warnPath }},
		{name: "antispam", set: func(v *Verifier, p string) { v.acPath = p }, load: func(v *Verifier) { v.loadAntispam() }, path: func(v *Verifier) string { return v.acPath }},
		{name: "settings", set: func(v *Verifier, p string) { v.settingsPath = p }, load: func(v *Verifier) { v.loadSettings() }, path: func(v *Verifier) string { return v.settingsPath }},
		{name: "verify failures", set: func(v *Verifier, p string) { v.vfailPath = p }, load: func(v *Verifier) { v.loadVerifyFails() }, path: func(v *Verifier) string { return v.vfailPath }},
		{name: "heartbeat", set: func(v *Verifier, p string) { v.hbPath = p }, load: func(v *Verifier) { v.loadHeartbeat() }, path: func(v *Verifier) string { return v.hbPath }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unreadable := t.TempDir()
			var dst any
			if err := loadJSONFile(unreadable, &dst); !stateReadFailed(err) {
				t.Fatalf("loadJSONFile(%q) error = %v, want stateReadError", unreadable, err)
			}
			v := NewVerifier(&Config{})
			tt.set(v, unreadable)
			tt.load(v)
			if got := tt.path(v); got != "" {
				t.Errorf("write path remains %q after read failure; want disabled", got)
			}
		})
	}
}

func TestSaveJSONFileOrdersSnapshotAndCommit(t *testing.T) {
	path := t.TempDir() + "/state.json"
	current := wjState{A: 1, B: "old"}
	oldEntered := make(chan struct{})
	releaseOld := make(chan struct{})
	oldDone := make(chan struct{})
	go func() {
		saveJSONFile(path, func() any {
			snapshot := current
			close(oldEntered)
			<-releaseOld
			return snapshot
		})
		close(oldDone)
	}()
	select {
	case <-oldEntered:
	case <-time.After(time.Second):
		t.Fatal("old snapshot did not start")
	}

	current = wjState{A: 2, B: "new"}
	newStarted := make(chan struct{})
	newDone := make(chan struct{})
	go func() {
		close(newStarted)
		saveJSONFile(path, func() any { return current })
		close(newDone)
	}()
	<-newStarted
	newRanEarly := false
	select {
	case <-newDone:
		newRanEarly = true
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseOld)
	for name, done := range map[string]<-chan struct{}{"old": oldDone, "new": newDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s save did not finish", name)
		}
	}
	if newRanEarly {
		t.Error("newer save completed while the older snapshot was still open")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got wjState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != current {
		t.Errorf("persisted state = %+v, want newest %+v", got, current)
	}
}

func TestWriteJSONFileConcurrent(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			writeJSONFile(dir+"/s"+strconv.Itoa(n%4)+".json", wjState{A: n, B: "concurrent"})
		}(i)
	}
	wg.Wait()
	// every target file must be complete, valid JSON — no torn write under -race.
	for j := 0; j < 4; j++ {
		data, err := os.ReadFile(dir + "/s" + strconv.Itoa(j) + ".json")
		if err != nil {
			t.Fatalf("s%d missing after concurrent writes: %v", j, err)
		}
		var got wjState
		if err := json.Unmarshal(data, &got); err != nil {
			t.Errorf("concurrent write left invalid JSON in s%d: %v", j, err)
		}
	}
}
