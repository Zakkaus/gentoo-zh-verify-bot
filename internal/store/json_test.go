package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

type wjState struct {
	A int    `json:"a"`
	B string `json:"b"`
}

func TestWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Write(path, wjState{A: 7, B: "x"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, want 0600 (state is private)", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read state directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Errorf("successful write left unexpected files: %v", entries)
	}
}

func TestWriteMarshalFailureKeepsPrior(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Write(path, wjState{A: 1, B: "prior"}); err != nil {
		t.Fatalf("write prior state: %v", err)
	}
	prior, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(path, make(chan int)); err == nil {
		t.Fatal("Write returned nil for an unsupported value")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the prior state file must survive a marshal failure, but it is gone: %v", err)
	}
	if string(after) != string(prior) {
		t.Errorf("a marshal failure must leave the prior state intact:\nprior=%q\nafter=%q", prior, after)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("a marshal failure must leave no temp file behind; dir has %d entries", len(entries))
	}
}

func TestSaveReturnsMarshalFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Save(path, func() any { return make(chan int) }); err == nil {
		t.Fatal("Save returned nil for an unsupported snapshot")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("failed Save left %d file(s), want none", len(entries))
	}
}

func TestLoadCorruptBackup(t *testing.T) {
	dir := t.TempDir()

	var dst []int
	if err := Load(filepath.Join(dir, "missing.json"), &dst); err != nil {
		t.Errorf("a missing file must not be an error (first run), got %v", err)
	}

	okPath := filepath.Join(dir, "ok.json")
	if err := os.WriteFile(okPath, []byte("[1,2,3]"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst = nil
	if err := Load(okPath, &dst); err != nil || len(dst) != 3 {
		t.Errorf("a valid file must load: err=%v dst=%v", err, dst)
	}

	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Load(badPath, &dst)
	if err == nil {
		t.Fatal("a corrupt file must return an error")
	}
	if ReadFailed(err) {
		t.Errorf("ReadFailed(%v) = true for malformed JSON", err)
	}
	if _, err := os.Stat(badPath + ".corrupt"); err != nil {
		t.Errorf("the corrupt file must be backed up to .corrupt: %v", err)
	}
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Error("the corrupt file must be renamed away from the live path")
	}
}

func TestLoadCorruptBackupFailureDisablesWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	original := []byte("{not json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".corrupt", 0o700); err != nil {
		t.Fatal(err)
	}

	var dst any
	err := Load(path, &dst)
	if !ReadFailed(err) {
		t.Fatalf("Load error = %v, want a write-disabling classified failure", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read original after failed backup: %v", readErr)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("original changed after failed backup: got %q, want %q", after, original)
	}
}

func TestLoadReadFailedClassification(t *testing.T) {
	unreadable := t.TempDir()
	var dst any
	err := Load(unreadable, &dst)
	if !ReadFailed(err) {
		t.Fatalf("Load(%q) error = %v, want a classified read failure", unreadable, err)
	}
	if ReadFailed(nil) {
		t.Error("ReadFailed(nil) = true")
	}
}

func TestSaveOrdersSnapshotAndCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	current := wjState{A: 1, B: "old"}
	oldEntered := make(chan struct{})
	releaseOld := make(chan struct{})
	oldDone := make(chan error, 1)
	go func() {
		oldDone <- Save(path, func() any {
			snapshot := current
			close(oldEntered)
			<-releaseOld
			return snapshot
		})
	}()
	select {
	case <-oldEntered:
	case <-time.After(time.Second):
		t.Fatal("old snapshot did not start")
	}

	current = wjState{A: 2, B: "new"}
	newStarted := make(chan struct{})
	newDone := make(chan error, 1)
	go func() {
		close(newStarted)
		newDone <- Save(path, func() any { return current })
	}()
	<-newStarted
	newRanEarly := false
	select {
	case err := <-newDone:
		newRanEarly = true
		if err != nil {
			t.Errorf("new Save: %v", err)
		}
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseOld)
	select {
	case err := <-oldDone:
		if err != nil {
			t.Errorf("old Save: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("old save did not finish")
	}
	if !newRanEarly {
		select {
		case err := <-newDone:
			if err != nil {
				t.Errorf("new Save: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("new save did not finish")
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

func TestWriteConcurrent(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := range 24 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := Write(filepath.Join(dir, "s"+strconv.Itoa(n%4)+".json"), wjState{A: n, B: "concurrent"}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Write: %v", err)
	}
	for i := range 4 {
		data, err := os.ReadFile(filepath.Join(dir, "s"+strconv.Itoa(i)+".json"))
		if err != nil {
			t.Fatalf("s%d missing after concurrent writes: %v", i, err)
		}
		var got wjState
		if err := json.Unmarshal(data, &got); err != nil {
			t.Errorf("concurrent write left invalid JSON in s%d: %v", i, err)
		}
	}
}

func TestReclaimTemps(t *testing.T) {
	dir := t.TempDir()
	remove := []string{".pending.json.tmp-123", ".feed.tmp-orphan"}
	keep := []string{"pending.json.tmp-123", ".pending.json.tmp", "state.json"}
	for _, name := range append(remove, keep...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ReclaimTemps(dir)
	for _, name := range remove {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("temporary file %q was not removed", name)
		}
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("non-temporary file %q was removed: %v", name, err)
		}
	}
}

func TestWriteSyncsParentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	wantErr := errors.New("directory sync failed")
	oldSyncParent := syncParent
	t.Cleanup(func() { syncParent = oldSyncParent })
	var synced string
	syncParent = func(path string) error {
		synced = path
		return wantErr
	}

	err := Write(path, wjState{A: 9, B: "committed"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Write error = %v, want %v", err, wantErr)
	}
	if synced != dir {
		t.Errorf("synced directory = %q, want %q", synced, dir)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("post-rename durability failure rolled back committed state: %v", err)
	}
}
