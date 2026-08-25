package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestVerifyStrikes(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{
		"group_ids": []int{-100}, "questions": sampleQ,
		"verify_max_fails": 3, "verify_retry_seconds": 180,
	}))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "verify-fails.json")
	v := NewVerifier(c)
	v.vfailPath = path

	for i := 1; i <= 2; i++ {
		count, ban := v.recordVerifyFail(-100, 42)
		if count != i || ban {
			t.Errorf("strike %d before restart = (%d, %v), want (%d, false)", i, count, ban, i)
		}
	}

	restored := NewVerifier(c)
	restored.vfailPath = path
	restored.loadVerifyFails()
	if remaining := restored.verifyCooldownRemaining(-100, 42); remaining <= 0 {
		t.Errorf("restored cooldown = %v, want an active cooldown", remaining)
	}
	if count, ban := restored.recordVerifyFail(-100, 42); count != 3 || !ban {
		t.Errorf("first strike after restart = (%d, %v), want (3, true)", count, ban)
	}

	restored.clearVerifyFails(-100, 42)
	if remaining := restored.verifyCooldownRemaining(-100, 42); remaining != 0 {
		t.Errorf("cooldown after clear = %v, want 0", remaining)
	}
	if count, _ := restored.recordVerifyFail(-100, 42); count != 1 {
		t.Errorf("strikes after clear restart at %d, want 1", count)
	}
	if count, ban := restored.recordVerifyFail(-100, 99); count != 1 || ban {
		t.Errorf("independent user's first strike = (%d, %v), want (1, false)", count, ban)
	}
}

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

func TestClaimPendingNonce(t *testing.T) {
	v := NewVerifier(&Config{})
	key := pkey{-100, 42}
	p := &pending{nonce: "NEW", timer: time.AfterFunc(time.Hour, func() {})}
	v.pend[key] = p
	if _, ok := v.claimPendingNonce(key.gid, key.uid, "OLD"); ok {
		t.Fatal("stale nonce claimed the replacement pending")
	}
	if p.done {
		t.Fatal("stale nonce marked the replacement done")
	}
	got, ok := v.claimPendingNonce(key.gid, key.uid, "NEW")
	if !ok || got != p || !p.done {
		t.Fatalf("matching nonce claim = (%p, %v), pending done=%v", got, ok, p.done)
	}
	p.timer.Stop()
}
