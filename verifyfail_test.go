package main

import (
	"testing"
	"time"
)

// TestVerifyStrikes covers the anti-spam strike logic: N failures trigger the auto-ban, the
// cooldown is active right after a failure, a successful clear resets, and a negative
// max disables auto-ban.
func TestVerifyStrikes(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids": []int{-100}, "questions": sampleQ,
		"verify_max_fails": 3, "verify_retry_seconds": 180,
	}))
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(c)

	for i := 1; i <= 3; i++ {
		count, ban := v.recordVerifyFail(-100, 42)
		if count != i {
			t.Errorf("strike %d: count=%d", i, count)
		}
		if want := i >= 3; ban != want {
			t.Errorf("strike %d: ban=%v want %v", i, ban, want)
		}
	}
	if v.verifyCooldownRemaining(-100, 42) <= 0 {
		t.Error("cooldown should be active right after a failure")
	}
	v.clearVerifyFails(-100, 42)
	if v.verifyCooldownRemaining(-100, 42) != 0 {
		t.Error("cooldown should be 0 after clear")
	}
	if count, _ := v.recordVerifyFail(-100, 42); count != 1 {
		t.Errorf("strikes should restart at 1 after a clear, got %d", count)
	}

	// A different user is independent.
	if count, ban := v.recordVerifyFail(-100, 99); count != 1 || ban {
		t.Errorf("user 99 first strike = (%d,%v), want (1,false)", count, ban)
	}
}

// TestVerifyNoAutoBan: a negative max disables the auto-ban entirely.
func TestVerifyNoAutoBan(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids": []int{-100}, "questions": sampleQ, "verify_max_fails": -1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(c)
	for i := 0; i < 10; i++ {
		if _, ban := v.recordVerifyFail(-100, 7); ban {
			t.Fatalf("auto-ban should be disabled with verify_max_fails=-1 (fired at strike %d)", i+1)
		}
	}
}

// TestVerifyCooldownDisabled: retry_seconds<=0 means no cooldown.
func TestVerifyCooldownDisabled(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids": []int{-100}, "questions": sampleQ, "verify_retry_seconds": -1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(c)
	v.recordVerifyFail(-100, 5)
	if v.verifyCooldownRemaining(-100, 5) != 0 {
		t.Error("cooldown should be disabled with verify_retry_seconds=-1")
	}
}

// TestVerifyStrikeDecay: a failure older than verifyFailWindow ages out, so isolated honest
// mistakes don't accumulate into a ban.
func TestVerifyStrikeDecay(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{"group_ids": []int{-100}, "questions": sampleQ, "verify_max_fails": 3}))
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(c)
	if count, _ := v.recordVerifyFail(-100, 42); count != 1 {
		t.Fatalf("first strike count=%d, want 1", count)
	}
	// back-date the last failure beyond the window
	v.mu.Lock()
	v.vfail[pkey{-100, 42}].last = time.Now().Add(-verifyFailWindow - time.Minute)
	v.mu.Unlock()
	if count, ban := v.recordVerifyFail(-100, 42); count != 1 || ban {
		t.Errorf("after window elapsed, strike = (%d,%v), want fresh (1,false)", count, ban)
	}
}
