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

// TestConsumeNonceIdentity covers the stale-timer fix: a decline/timeout carrying an OLD nonce
// must not consume a freshly re-issued pending (same gid,uid, new nonce).
func TestConsumeNonceIdentity(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{"group_ids": []int{-100}, "questions": sampleQ}))
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(c)
	key := pkey{-100, 42}
	v.pend[key] = &pending{nonce: "NEW"}
	if _, ok := v.consumeNonce(-100, 42, "OLD"); ok {
		t.Error("stale nonce must NOT consume the fresh pending")
	}
	if _, ok := v.pend[key]; !ok {
		t.Error("fresh pending must survive a stale-nonce consume attempt")
	}
	if _, ok := v.consumeNonce(-100, 42, "NEW"); !ok {
		t.Error("matching nonce should consume")
	}
	if _, ok := v.pend[key]; ok {
		t.Error("pending should be gone after a matching consume")
	}
}

// TestConfigClampDurations: out-of-window config ban/mute durations are normalized at load so the
// reported duration matches what Telegram enforces.
func TestConfigClampDurations(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids": []int{-100}, "questions": sampleQ, "ban_seconds": 10, "mute_seconds": 10,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.BanSeconds != 30 || c.MuteSeconds != 30 {
		t.Errorf("sub-30s clamp: ban=%d mute=%d, want 30/30", c.BanSeconds, c.MuteSeconds)
	}
	c2, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids": []int{-100}, "questions": sampleQ, "ban_seconds": 40000000, "mute_seconds": 40000000,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c2.BanSeconds != 0 { // >366d ban => permanent
		t.Errorf("over-366d ban_seconds should clamp to 0 (permanent), got %d", c2.BanSeconds)
	}
	if c2.MuteSeconds != telegramBanMax { // mute can't be permanent => capped
		t.Errorf("over-366d mute_seconds should cap to %d, got %d", telegramBanMax, c2.MuteSeconds)
	}
}
