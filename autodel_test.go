package main

import (
	"testing"
	"time"
)

func newTestVerifier(ttl *int) *Verifier {
	return NewVerifier(&Config{
		GroupIDs:         []int64{-1},
		Questions:        []Question{{Q: "x", Options: []string{"a", "b"}, Answer: 0}},
		LookupTTLSeconds: ttl,
	})
}

// TestLookupAutoDelete covers the lookup auto-delete setting: the documented default
// (unset => on at 3 min), config disable (<=0) and custom seconds, the runtime setter, and
// that disabling keeps the TTL so re-enabling restores it.
func TestLookupAutoDelete(t *testing.T) {
	// default: unset => enabled, 3 minutes
	if ttl, on := newTestVerifier(nil).lookupAutoDelete(); !on || ttl != 3*time.Minute {
		t.Errorf("default = (%v, %v), want (3m, true)", ttl, on)
	}
	// config 0 => disabled
	zero := 0
	if _, on := newTestVerifier(&zero).lookupAutoDelete(); on {
		t.Errorf("lookup_ttl_seconds=0 should disable")
	}
	// config positive => enabled with that duration
	s := 600
	if ttl, on := newTestVerifier(&s).lookupAutoDelete(); !on || ttl != 10*time.Minute {
		t.Errorf("lookup_ttl_seconds=600 = (%v, %v), want (10m, true)", ttl, on)
	}
	// runtime: set minutes, then disable — the TTL must persist for a later re-enable
	v := newTestVerifier(nil)
	v.setLookupAutoDelete(5*time.Minute, true)
	v.setLookupAutoDelete(0, false) // ttl<=0 => don't change the duration, just toggle off
	if ttl, on := v.lookupAutoDelete(); on || ttl != 5*time.Minute {
		t.Errorf("after off = (%v, %v), want (5m, false)", ttl, on)
	}
}
