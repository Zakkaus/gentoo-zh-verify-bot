package verify

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

func TestVerifyStrikes(t *testing.T) {
	c := &config.Config{VerifyMaxFails: 3, VerifyRetrySeconds: 180}
	path := filepath.Join(t.TempDir(), "verify-fails.json")
	v := newTestService(c)
	v.vfailPath = path

	for i := 1; i <= 2; i++ {
		count, ban := v.recordVerifyFail(-100, 42)
		if count != i || ban {
			t.Errorf("strike %d before restart = (%d, %v), want (%d, false)", i, count, ban, i)
		}
	}

	restored := newTestService(c)
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
	v := newTestService(&config.Config{VerifyMaxFails: -1})
	for i := range 10 {
		if _, ban := v.recordVerifyFail(-100, 7); ban {
			t.Fatalf("auto-ban should be disabled with verify_max_fails=-1 (fired at strike %d)", i+1)
		}
	}
}

func TestVerifyCooldownDisabled(t *testing.T) {
	v := newTestService(&config.Config{VerifyRetrySeconds: -1})
	v.recordVerifyFail(-100, 5)
	if v.verifyCooldownRemaining(-100, 5) != 0 {
		t.Error("cooldown should be disabled with verify_retry_seconds=-1")
	}
}

func TestVerifyStrikeDecay(t *testing.T) {
	v := newTestService(&config.Config{VerifyMaxFails: 3})
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
	v := newTestService(&config.Config{})
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

func TestClaimPendingNonce(t *testing.T) {
	v := newTestService(&config.Config{})
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
