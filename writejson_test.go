package main

import (
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"testing"
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
