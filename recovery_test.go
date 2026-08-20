package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func setOffline(v *Verifier) {
	v.mu.Lock()
	v.lastOnline = time.Now().Add(-2 * offlineThreshold)
	v.mu.Unlock()
}
func setOnline(v *Verifier) { v.mu.Lock(); v.lastOnline = time.Now(); v.mu.Unlock() }

// TestOfflineNow: seeded online at construction; flips offline only after contact goes stale.
func TestOfflineNow(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240})
	if v.offlineNow() {
		t.Fatal("a fresh Verifier is seeded online")
	}
	setOffline(v)
	if !v.offlineNow() {
		t.Error("no contact for > offlineThreshold should read offline")
	}
	setOnline(v)
	if v.offlineNow() {
		t.Error("recent contact should read online again")
	}
}

// TestStrikesUserReasons: only genuine user failures strike; bot-fault / outage reasons never do.
func TestStrikesUserReasons(t *testing.T) {
	for _, r := range []string{"approve-retry", "restart-lapsed", "recovered"} {
		if strikesUser(r) {
			t.Errorf("%q must NOT strike the user (not their fault)", r)
		}
	}
	for _, r := range []string{"timeout", "wrong answer"} {
		if !strikesUser(r) {
			t.Errorf("%q should strike", r)
		}
	}
}

// TestOnExpiryOfflineDefers: a timeout firing while the bot is offline must not decline or strike; it
// keeps the pending and re-arms a fresh window, so an outage can't burn a mid-verification user.
func TestOnExpiryOfflineDefers(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240, VerifyMaxFails: 3})
	setOffline(v)
	key := pkey{-100, 5}
	v.pend[key] = &pending{nonce: "n", deadline: time.Now().Add(-time.Second), groupMsgID: 42}
	fb := &fakeVerifyBot{}
	v.onExpiry(context.Background(), fb, -100, 5, "n", 0, "timeout")
	if fb.declines != 0 {
		t.Errorf("offline expiry must not decline, got declines=%d", fb.declines)
	}
	cur, ok := v.pend[key]
	if !ok || cur.done {
		t.Fatal("offline expiry must keep the pending live (deferred, not consumed)")
	}
	if _, struck := v.vfail[key]; struck {
		t.Error("offline expiry must not record a strike")
	}
	if !cur.deadline.After(time.Now().Add(v.timeout() - 5*time.Second)) {
		t.Errorf("deferred expiry should re-arm a fresh full window, deadline=%v", cur.deadline)
	}
	if cur.timer != nil {
		cur.timer.Stop()
	}
}

// TestOnExpiryOnlineDeclines: a timeout firing while online (and reachable) declines + strikes.
func TestOnExpiryOnlineDeclines(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240, VerifyMaxFails: 3}) // seeded online, no probe => reachable
	key := pkey{-100, 5}
	v.pend[key] = &pending{nonce: "n", deadline: time.Now(), groupMsgID: 42}
	fb := &fakeVerifyBot{}
	v.onExpiry(context.Background(), fb, -100, 5, "n", 0, "timeout")
	if fb.declines != 1 {
		t.Errorf("online timeout should decline once, got %d", fb.declines)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("online timeout should consume the pending")
	}
	if r := v.vfail[key]; r == nil || r.count != 1 {
		t.Error("online timeout should record one strike")
	}
}

// TestOnExpiryOnsetLagProbeDefers: at outage ONSET offlineNow may still read false (heartbeat lag), but
// an on-demand probe that can't reach Telegram must make the expiry defer instead of declining/striking.
func TestOnExpiryOnsetLagProbeDefers(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240, VerifyMaxFails: 3}) // offlineNow == false (seeded online)
	probe := &fakeVerifyBot{getMeErr: errors.New("network down")}
	v.probe = probe
	key := pkey{-100, 5}
	v.pend[key] = &pending{nonce: "n", deadline: time.Now()}
	fb := &fakeVerifyBot{}
	v.onExpiry(context.Background(), fb, -100, 5, "n", 0, "timeout")
	if probe.getMeCalls == 0 {
		t.Error("reachable() should probe when offlineNow is false")
	}
	if fb.declines != 0 {
		t.Error("an unreachable probe at outage onset must defer, not decline")
	}
	if p, ok := v.pend[key]; !ok || p.done {
		t.Error("onset-lag defer must keep the pending")
	}
	if _, struck := v.vfail[key]; struck {
		t.Error("onset-lag defer must not strike")
	}
	if p := v.pend[key]; p != nil && p.timer != nil {
		p.timer.Stop()
	}
}

// TestOnExpiryStaleEpochNoop: a superseded timer (its epoch bumped by a later re-arm) must no-op even
// while online — the guard that stops a pre-recovery timeout from declining/striking a refreshed user.
func TestOnExpiryStaleEpochNoop(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240, VerifyMaxFails: 3}) // online
	key := pkey{-100, 5}
	p := &pending{nonce: "n", deadline: time.Now()}
	v.pend[key] = p
	fb := &fakeVerifyBot{}
	v.mu.Lock()
	v.armExpiry(fb, p, -100, 5, time.Hour, "timeout") // epoch -> 1
	stale := p.epoch
	v.armExpiry(fb, p, -100, 5, time.Hour, "timeout") // epoch -> 2 (a later re-arm superseded epoch 1)
	v.mu.Unlock()
	v.onExpiry(context.Background(), fb, -100, 5, "n", stale, "timeout") // the epoch-1 timer fires late
	if fb.declines != 0 {
		t.Error("a superseded (stale-epoch) expiry must not decline")
	}
	if _, ok := v.pend[key]; !ok {
		t.Error("a superseded expiry must not consume the pending")
	}
	if _, struck := v.vfail[key]; struck {
		t.Error("a superseded expiry must not strike")
	}
	if p.timer != nil {
		p.timer.Stop()
	}
}

// TestExpiryDeferThenOnlineStrikes: a pending deferred during an outage must still be declined AND
// struck once the bot is back online — the defer must not launder away a genuine timeout forever.
func TestExpiryDeferThenOnlineStrikes(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240, VerifyMaxFails: 3})
	setOffline(v)
	key := pkey{-100, 5}
	v.pend[key] = &pending{nonce: "n", deadline: time.Now().Add(-time.Second)}
	fb := &fakeVerifyBot{}
	v.onExpiry(context.Background(), fb, -100, 5, "n", 0, "timeout") // offline -> defer
	cur, ok := v.pend[key]
	if !ok || cur.done || fb.declines != 0 {
		t.Fatal("offline expiry should keep the pending and not decline")
	}
	deferredEpoch := cur.epoch
	if cur.timer != nil {
		cur.timer.Stop() // we drive the "fire" manually below
	}
	setOnline(v)
	v.onExpiry(context.Background(), fb, -100, 5, "n", deferredEpoch, "timeout") // re-armed timer fires online
	if fb.declines != 1 {
		t.Errorf("a re-armed timeout, once online, should decline, got %d", fb.declines)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("the online fire should consume the pending")
	}
	if r := v.vfail[key]; r == nil || r.count != 1 {
		t.Error("the deferred timeout should still strike once online — no strike laundering")
	}
}

// TestDeferExpiryGuards: deferring a stale expiry (nonce OR epoch no longer matches) is a no-op — it
// must not resurrect or re-arm a pending a newer request/re-arm has superseded.
func TestDeferExpiryGuards(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240})
	key := pkey{-100, 5}
	fresh := &pending{nonce: "new", epoch: 7, deadline: time.Now().Add(time.Hour)}
	v.pend[key] = fresh
	v.deferExpiry(&fakeVerifyBot{}, -100, 5, "old", 7, "timeout") // stale nonce
	if fresh.timer != nil {
		t.Error("a stale-nonce defer must not re-arm the current pending")
	}
	v.deferExpiry(&fakeVerifyBot{}, -100, 5, "new", 3, "timeout") // right nonce, stale epoch
	if fresh.timer != nil {
		t.Error("a stale-epoch defer must not re-arm the current pending")
	}
}

// TestHeartbeatTickRecovers: a successful probe that ends a long outage advances lastOnline and triggers
// the recovery refresh + re-notify.
func TestHeartbeatTickRecovers(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240})
	v.botUsername = "bot"
	v.mu.Lock()
	v.lastOnline = time.Now().Add(-10 * time.Minute) // a long outage
	v.mu.Unlock()
	key := pkey{-100, 1}
	v.pend[key] = &pending{nonce: "a", name: "A", deadline: time.Now().Add(-time.Minute), groupMsgID: 7}
	bot := &fakeVerifyBot{}
	if !v.heartbeatTick(context.Background(), bot) {
		t.Fatal("a successful probe should return true")
	}
	if bot.getMeCalls != 1 {
		t.Errorf("expected one GetMe probe, got %d", bot.getMeCalls)
	}
	if bot.sends == 0 {
		t.Error("recovery after a long outage should re-notify")
	}
	p, ok := v.pend[key]
	if !ok || !p.deadline.After(time.Now().Add(v.timeout()-10*time.Second)) {
		t.Error("recovery should refresh the pending's window")
	}
	if p != nil && p.timer != nil {
		p.timer.Stop()
	}
}

// TestHeartbeatTickOfflineKeepsClock: a failed probe returns false and must NOT advance lastOnline (so
// offlineNow can flip true).
func TestHeartbeatTickOfflineKeepsClock(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240})
	before := time.Now().Add(-time.Hour)
	v.mu.Lock()
	v.lastOnline = before
	v.mu.Unlock()
	bot := &fakeVerifyBot{getMeErr: errors.New("down")}
	if v.heartbeatTick(context.Background(), bot) {
		t.Error("a failed probe should return false")
	}
	v.mu.Lock()
	after := v.lastOnline
	v.mu.Unlock()
	if !after.Equal(before) {
		t.Error("a failed probe must not advance lastOnline")
	}
}

// TestOnRecoveryRefreshesAndRenotifies: after an outage, every live pending gets a fresh full window
// and a best-effort re-notify (a DM plus a fresh in-group challenge), and stays live.
func TestOnRecoveryRefreshesAndRenotifies(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240})
	v.botUsername = "bot"
	k1, k2 := pkey{-100, 1}, pkey{-100, 2}
	v.pend[k1] = &pending{nonce: "a", name: "Alice", deadline: time.Now().Add(-time.Minute), groupMsgID: 11}
	v.pend[k2] = &pending{nonce: "b", name: "Bob", deadline: time.Now().Add(10 * time.Second), groupMsgID: 12}
	fb := &fakeVerifyBot{}
	v.onRecovery(context.Background(), fb, 3*time.Minute)
	if fb.sends != 4 { // per pending: 1 DM + 1 fresh group challenge
		t.Errorf("recovery should DM + re-post per pending (want 4 sends), got %d", fb.sends)
	}
	for _, k := range []pkey{k1, k2} {
		p, ok := v.pend[k]
		if !ok || p.done {
			t.Fatalf("pending %v should stay live after recovery", k)
		}
		if !p.deadline.After(time.Now().Add(v.timeout() - 10*time.Second)) {
			t.Errorf("pending %v should get a fresh full window, deadline=%v", k, p.deadline)
		}
		if p.timer != nil {
			p.timer.Stop()
		}
	}
}

// TestOnRecoveryRenotifyCooldown: a second recovery within a window (flapping) refreshes silently and
// must NOT re-message the same applicant again.
func TestOnRecoveryRenotifyCooldown(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240})
	v.botUsername = "bot"
	v.pend[pkey{-100, 1}] = &pending{nonce: "a", name: "A", deadline: time.Now(), groupMsgID: 5}
	fb := &fakeVerifyBot{}
	v.onRecovery(context.Background(), fb, 2*time.Minute)
	first := fb.sends
	if first == 0 {
		t.Fatal("first recovery should re-notify")
	}
	v.onRecovery(context.Background(), fb, 2*time.Minute) // immediate flap
	if fb.sends != first {
		t.Errorf("a second recovery within the window must not re-notify again (cooldown), sends %d -> %d", first, fb.sends)
	}
	for _, p := range v.pend {
		if p.timer != nil {
			p.timer.Stop()
		}
	}
}

// TestOnRecoveryShuttingDown: recovery is a no-op once shutdown has begun (don't fight the exit).
func TestOnRecoveryShuttingDown(t *testing.T) {
	v := NewVerifier(&Config{TimeoutSeconds: 240})
	v.pend[pkey{-100, 1}] = &pending{nonce: "a", deadline: time.Now()}
	v.shuttingDown = true
	fb := &fakeVerifyBot{}
	v.onRecovery(context.Background(), fb, time.Hour)
	if fb.sends != 0 {
		t.Error("recovery must be a no-op during shutdown")
	}
}

// TestLoadRefreshesAfterOutage: restoring after a long downtime gives a fresh full window (not the
// stale ~30s remaining) and re-notifies; the strike-free "recovered" reason means a lapse won't ban.
func TestLoadRefreshesAfterOutage(t *testing.T) {
	dir := t.TempDir()
	seed := NewVerifier(&Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	seed.statePath = dir + "/pending.json"
	seed.hbPath = dir + "/heartbeat.json"
	seed.pend[pkey{-100, 7}] = &pending{nonce: "x", name: "Carol", correctIdx: 0,
		qOpts: []string{"a", "b"}, deadline: time.Now().Add(30 * time.Second), groupMsgID: 5}
	seed.save()
	seed.mu.Lock()
	seed.lastOnline = time.Now().Add(-10 * time.Minute) // a long outage
	seed.mu.Unlock()
	seed.saveHeartbeat()

	v := NewVerifier(&Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	v.botUsername = "bot"
	v.statePath = dir + "/pending.json"
	v.hbPath = dir + "/heartbeat.json"
	fb := &fakeVerifyBot{}
	v.load(fb)

	p, ok := v.pend[pkey{-100, 7}]
	if !ok {
		t.Fatal("the pending should be restored")
	}
	if !p.deadline.After(time.Now().Add(v.timeout() - 10*time.Second)) {
		t.Errorf("a long outage should restore a fresh full window, not the ~30s remaining (deadline=%v)", p.deadline)
	}
	if fb.sends == 0 {
		t.Error("a long outage should re-notify the restored applicant")
	}
	if p.timer != nil {
		p.timer.Stop()
	}
}

// TestLoadQuickRestartKeepsWindow: a quick restart (recent heartbeat) restores the remaining window and
// does NOT re-notify — a routine deploy stays quiet.
func TestLoadQuickRestartKeepsWindow(t *testing.T) {
	dir := t.TempDir()
	seed := NewVerifier(&Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	seed.statePath = dir + "/pending.json"
	seed.hbPath = dir + "/heartbeat.json"
	seed.pend[pkey{-100, 8}] = &pending{nonce: "y", correctIdx: 0, qOpts: []string{"a", "b"},
		deadline: time.Now().Add(120 * time.Second), groupMsgID: 6}
	seed.save()
	seed.saveHeartbeat() // heartbeat = now (quick restart)

	v := NewVerifier(&Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	v.statePath = dir + "/pending.json"
	v.hbPath = dir + "/heartbeat.json"
	fb := &fakeVerifyBot{}
	v.load(fb)

	p, ok := v.pend[pkey{-100, 8}]
	if !ok {
		t.Fatal("the pending should be restored")
	}
	if p.deadline.After(time.Now().Add(150 * time.Second)) {
		t.Errorf("a quick restart should keep the remaining ~120s window, not reset to a full 240s (deadline=%v)", p.deadline)
	}
	if fb.sends != 0 {
		t.Errorf("a quick restart must not re-notify, got %d sends", fb.sends)
	}
	if p.timer != nil {
		p.timer.Stop()
	}
}

// TestStreamEndedUnexpectedly: the lifecycle guard — a nil ctx error (stream ended without a shutdown
// signal) means restart; a cancelled ctx (real shutdown) means graceful exit.
func TestStreamEndedUnexpectedly(t *testing.T) {
	if !streamEndedUnexpectedly(nil) {
		t.Error("nil ctx error should signal an unexpected end => restart")
	}
	if streamEndedUnexpectedly(context.Canceled) {
		t.Error("a cancelled ctx is a graceful shutdown => no restart")
	}
}

// TestOutageText: friendly duration rendering, incl. an hours branch for a long outage, in each
// locale the verification path speaks.
func TestOutageText(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Second: "45 秒",
		3 * time.Minute:  "3 分钟",
		8 * time.Hour:    "8 小时",
	}
	for d, want := range cases {
		if got := outageText(langZH, d); got != want {
			t.Errorf("outageText(zh, %v) = %q, want %q", d, got, want)
		}
	}
	if got := outageText(langZHT, 3*time.Minute); got != "3 分鐘" {
		t.Errorf("outageText(zh-hant, 3m) = %q, want %q", got, "3 分鐘")
	}
	if got := outageText(langEN, 8*time.Hour); got != "8 hours" {
		t.Errorf("outageText(en, 8h) = %q, want %q", got, "8 hours")
	}
}
