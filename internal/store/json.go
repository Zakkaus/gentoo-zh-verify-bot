package store

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Serialize snapshot creation and commit so an older snapshot cannot overwrite a newer save.
var writeMu sync.Mutex

var syncParent = syncParentDirectory

type readError struct {
	cause error
}

func (e *readError) Error() string { return e.cause.Error() }
func (e *readError) Unwrap() error { return e.cause }

// Load decodes one JSON state file while preserving unreadable or corrupt data for recovery.
func Load(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		log.Printf("ERROR state load %s: %v; writes disabled until restart", path, err)
		return &readError{cause: err}
	}
	if err := json.Unmarshal(data, dst); err != nil {
		log.Printf("state load %s: %v — backing up to %s.corrupt and starting fresh", path, err, path)
		if rerr := os.Rename(path, path+".corrupt"); rerr != nil {
			log.Printf("state load: could not back up corrupt %s: %v", path, rerr)
		}
		return err
	}
	return nil
}

// Save serializes snapshot creation and its atomic commit with every other state write.
func Save(path string, snapshot func() any) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	return writeLocked(path, snapshot())
}

// Write atomically commits a stable JSON value in process-wide transaction order.
func Write(path string, value any) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	return writeLocked(path, value)
}

// ReadFailed reports whether Load failed to read an existing path rather than decode it.
func ReadFailed(err error) bool {
	_, ok := err.(*readError)
	return ok
}

// ReclaimTemps removes state temp files left behind before an atomic rename.
func ReclaimTemps(dir string) {
	if leftover, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*")); len(leftover) > 0 {
		for _, file := range leftover {
			_ = os.Remove(file)
		}
		log.Printf("swept %d leftover state temp file(s) in %s", len(leftover), dir)
	}
}

// The caller holds writeMu; fsync precedes same-directory atomic rename.
func writeLocked(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		log.Printf("state: marshal %s: %v", path, err)
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*") // mode 0600
	if err != nil {
		log.Printf("state: temp for %s: %v", path, err)
		return err
	}
	tmp := file.Name()
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync() // flush data before rename so a crash cannot expose a torn or empty state
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(tmp)
		log.Printf("state: write %s: %v", path, writeErr)
		return writeErr
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("state: rename %s: %v", path, err)
		return err
	}
	return syncParent(filepath.Dir(path))
}

func syncParentDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}
