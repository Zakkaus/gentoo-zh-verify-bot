package moderate

import (
	"log"
	"sort"
	"sync"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
)

const warnCounterMax = 4096

type warningKey struct {
	groupID int64
	userID  int64
}

// warningRecord is the stable on-disk form of one group-user warning counter.
type warningRecord struct {
	GroupID int64 `json:"group_id"`
	UserID  int64 `json:"user_id"`
	Count   int   `json:"count"`
}

type warningState struct {
	mu       sync.Mutex
	path     string
	counters map[warningKey]int
}

func newWarningState(stateDirectory string) warningState {
	return warningState{
		path:     warningsPath(stateDirectory),
		counters: make(map[warningKey]int),
	}
}

func (w *warningState) load() {
	if w.path == "" {
		return
	}
	var records []warningRecord
	if err := store.Load(w.path, &records); err != nil {
		if store.ReadFailed(err) {
			w.path = ""
		}
		return // corrupt files were backed up; unreadable files remain untouched and write-disabled
	}
	w.mu.Lock()
	for _, record := range records {
		if record.Count > 0 {
			w.counters[warningKey{groupID: record.GroupID, userID: record.UserID}] = record.Count
		}
	}
	w.pruneLocked()
	count := len(w.counters)
	w.mu.Unlock()
	if count > 0 {
		log.Printf("restored %d warning counter(s)", count)
	}
}

func (w *warningState) save() error {
	if w.path == "" {
		return nil
	}
	return store.Save(w.path, func() any {
		w.mu.Lock()
		defer w.mu.Unlock()
		records := make([]warningRecord, 0, len(w.counters))
		for key, count := range w.counters {
			if count > 0 {
				records = append(records, warningRecord{GroupID: key.groupID, UserID: key.userID, Count: count})
			}
		}
		return records
	})
}

func (w *warningState) increment(groupID, userID int64) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := warningKey{groupID: groupID, userID: userID}
	w.counters[key]++
	count := w.counters[key]
	w.pruneLocked()
	return count
}

func (w *warningState) clear(groupID, userID int64) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := warningKey{groupID: groupID, userID: userID}
	count := w.counters[key]
	delete(w.counters, key)
	return count
}

// pruneLocked evicts the least severe counters first with deterministic key tie-breaking.
func (w *warningState) pruneLocked() {
	over := len(w.counters) - warnCounterMax
	if over <= 0 {
		return
	}
	type warning struct {
		key   warningKey
		count int
	}
	all := make([]warning, 0, len(w.counters))
	for key, count := range w.counters {
		all = append(all, warning{key: key, count: count})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].count != all[j].count {
			return all[i].count < all[j].count
		}
		if all[i].key.groupID != all[j].key.groupID {
			return all[i].key.groupID < all[j].key.groupID
		}
		return all[i].key.userID < all[j].key.userID
	})
	for _, item := range all[:over] {
		delete(w.counters, item.key)
	}
}
