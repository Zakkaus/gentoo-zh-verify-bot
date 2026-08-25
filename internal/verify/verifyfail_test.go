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

func TestSaveVerifyFailsPrunesOnlyFullyExpiredRecords(t *testing.T) {
	const (
		shortGroup int64 = -100
		longGroup  int64 = -200
	)
	cfg := &config.Config{
		Groups:             []config.GroupConfig{{ID: shortGroup}, {ID: longGroup}},
		GroupIDs:           []int64{shortGroup, longGroup},
		VerifyMaxFails:     3,
		VerifyRetrySeconds: 30,
	}
	v := newTestService(cfg)
	group, ok := v.settings.Group(longGroup)
	if !ok {
		t.Fatal("long-cooldown group is missing")
	}
	overrides := group.Overrides()
	longRetrySeconds := int((8 * time.Hour) / time.Second)
	overrides.VerifyRetrySeconds = &longRetrySeconds
	if _, err := v.settings.CommitGroup(longGroup, group.Revision(), overrides); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	dead := pkey{shortGroup, 1}
	cooldownLive := pkey{longGroup, 2}
	historyLive := pkey{shortGroup, 3}
	v.vfail[dead] = &vfailRec{count: 1, last: now.Add(-7 * time.Hour)}
	v.vfail[cooldownLive] = &vfailRec{count: 1, last: now.Add(-7 * time.Hour)}
	v.vfail[historyLive] = &vfailRec{count: 2, last: now.Add(-verifyFailWindow + time.Minute)}
	v.vfailPath = filepath.Join(t.TempDir(), "verify-fails.json")

	v.saveVerifyFails()

	if _, ok := v.vfail[dead]; ok {
		t.Error("a record with expired strike and cooldown windows was retained")
	}
	if remaining := v.verifyCooldownRemaining(cooldownLive.gid, cooldownLive.uid); remaining <= 0 {
		t.Errorf("group-specific live cooldown was pruned, remaining = %v", remaining)
	}
	if count, ban := v.recordVerifyFail(historyLive.gid, historyLive.uid); count != 3 || !ban {
		t.Errorf("live ban history after pruning = (%d, %v), want (3, true)", count, ban)
	}

	restored := newTestService(cfg)
	restored.vfailPath = v.vfailPath
	restored.loadVerifyFails()
	if _, ok := restored.vfail[dead]; ok {
		t.Error("fully expired record was serialized")
	}
}

func TestRecordVerifyFailPrunesExpiredRecordsBeforeInsertion(t *testing.T) {
	v := newTestService(&config.Config{VerifyRetrySeconds: 30})
	dead := pkey{-100, 1}
	v.vfail[dead] = &vfailRec{count: 1, last: time.Now().Add(-verifyFailWindow - time.Minute)}

	v.recordVerifyFail(-100, 2)

	if _, ok := v.vfail[dead]; ok {
		t.Error("insertion retained a record whose strike and cooldown windows had expired")
	}
}

func TestVerifyFailCapacityEvictsOldestWithoutClearingLiveState(t *testing.T) {
	v := newTestService(&config.Config{VerifyMaxFails: 3, VerifyRetrySeconds: 180})
	now := time.Now()
	protected := pkey{-100, 1}
	oldest := pkey{-100, 2}
	for uid := int64(1); uid <= vfailMax; uid++ {
		v.vfail[pkey{-100, uid}] = &vfailRec{count: 1, last: now.Add(-time.Hour)}
	}
	v.vfail[protected] = &vfailRec{count: 2, last: now}
	v.vfail[oldest].last = now.Add(-verifyFailWindow + time.Minute)

	v.recordVerifyFail(-100, vfailMax+1)

	if len(v.vfail) != vfailMax {
		t.Fatalf("ledger size after capacity insertion = %d, want %d", len(v.vfail), vfailMax)
	}
	if _, ok := v.vfail[oldest]; ok {
		t.Error("oldest remaining strike record was not evicted")
	}
	if count, ban := v.recordVerifyFail(protected.gid, protected.uid); count != 3 || !ban {
		t.Errorf("live cooldown and ban history were lost at capacity: (%d, %v)", count, ban)
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
