package verify

import (
	"bytes"
	"context"
	"errors"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"
)

func setOffline(v *Service) {
	v.mu.Lock()
	v.lastOnline = time.Now().Add(-2 * offlineThreshold)
	v.mu.Unlock()
}
func setOnline(v *Service) { v.mu.Lock(); v.lastOnline = time.Now(); v.mu.Unlock() }

func TestOfflineNow(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
	if v.offlineNow() {
		t.Fatal("a fresh Service is seeded online")
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

func TestOnExpiryOfflineDefers(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240, VerifyMaxFails: 3})
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
	if !cur.deadline.After(time.Now().Add(v.timeout(key.gid) - 5*time.Second)) {
		t.Errorf("deferred expiry should re-arm a fresh full window, deadline=%v", cur.deadline)
	}
	if cur.timer != nil {
		cur.timer.Stop()
	}
}

func TestDeferredExpiryCapSettlesStrikeFree(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	v := newTestService(&config.Config{
		TimeoutSeconds:     4 * 60 * 60,
		VerifyMaxFails:     1,
		VerifyRetrySeconds: 600,
	})
	v.timeNow = func() time.Time { return now }
	key := pkey{gid, uid}
	p := &pending{
		nonce:        "n",
		lang:         i18n.LangEN,
		deadline:     now,
		groupMsgID:   42,
		privateMsgID: 43,
	}
	v.pend[key] = p
	bot := newBlockingTerminalBot()
	bot.getMeErr = errors.New("Telegram unavailable")
	v.probe = bot

	v.onExpiry(context.Background(), bot, gid, uid, p.nonce, p.epoch, "timeout")
	if p.timer != nil {
		p.timer.Stop()
	}
	now = now.Add(49 * time.Hour)
	bot.getMeErr = nil
	v.mu.Lock()
	v.lastOnline = now
	v.mu.Unlock()

	done := make(chan struct{})
	go func() {
		v.onExpiry(context.Background(), bot, gid, uid, p.nonce, p.epoch, "timeout")
		close(done)
	}()
	defer func() {
		select {
		case <-bot.release:
		default:
			close(bot.release)
		}
	}()
	select {
	case <-bot.declineStarted:
	case <-time.After(time.Second):
		t.Fatal("capped expiry did not reach the blocking Telegram decline")
	}
	close(bot.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capped expiry did not finish after Telegram decline returned")
	}

	if _, ok := v.pend[key]; ok {
		t.Error("capped expiry kept the settled pending")
	}
	if _, struck := v.vfail[key]; struck {
		t.Error("capped expiry recorded a verification strike")
	}
	if remaining := v.verifyCooldownRemaining(gid, uid); remaining != 0 {
		t.Errorf("capped expiry started a cooldown of %s", remaining)
	}
	if bot.bans != 0 {
		t.Errorf("capped expiry issued %d bans, want none", bot.bans)
	}
	if bot.deletes != 2 || !reflect.DeepEqual(bot.deletedChats, []int64{gid, uid}) ||
		!reflect.DeepEqual(bot.deletedMessageIDs, []int{42, 43}) {
		t.Errorf("capped expiry cleanup = chats %v messages %v, want both challenge messages",
			bot.deletedChats, bot.deletedMessageIDs)
	}
	wantText := v.messages.Verification.Result.DeferralExpired.For(i18n.LangEN)
	if bot.sends != 1 || bot.lastSendChat != uid || bot.lastSendText != wantText {
		t.Errorf("capped expiry notice = sends %d/chat %d/text %q, want one catalogue message %q",
			bot.sends, bot.lastSendChat, bot.lastSendText, wantText)
	}
}

func TestDeferredExpiryCapRetriesWithoutFreshWindowAndLogsOnce(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	v := newTestService(&config.Config{TimeoutSeconds: 4 * 60 * 60})
	v.timeNow = func() time.Time { return now }
	v.statePath = t.TempDir() + "/pending.json"
	key := pkey{gid, uid}
	p := &pending{nonce: "n", deadline: now}
	v.pend[key] = p
	bot := &fakeVerifyBot{getMeErr: errors.New("Telegram unavailable")}
	v.probe = bot

	v.onExpiry(context.Background(), bot, gid, uid, p.nonce, p.epoch, "timeout")
	if p.timer != nil {
		p.timer.Stop()
	}
	now = now.Add(49 * time.Hour)

	var output bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		if p.timer != nil {
			p.timer.Stop()
		}
	})

	v.onExpiry(context.Background(), bot, gid, uid, p.nonce, p.epoch, "timeout")
	if remaining := p.deadline.Sub(now); remaining != noFaultGrace {
		t.Errorf("first capped retry delay = %s, want %s", remaining, noFaultGrace)
	}
	if !pendingStateBool(t, v.statePath, "deferral_cap_reached") {
		t.Error("first capped retry did not persist its one-time warning marker")
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	now = now.Add(noFaultGrace)
	v.onExpiry(context.Background(), bot, gid, uid, p.nonce, p.epoch, "timeout")
	if remaining := p.deadline.Sub(now); remaining != noFaultGrace {
		t.Errorf("second capped retry delay = %s, want %s", remaining, noFaultGrace)
	}

	logs := output.String()
	if count := strings.Count(logs, "verification deferral cap reached"); count != 1 {
		t.Errorf("cap warning count = %d, want 1; logs: %q", count, logs)
	}
	if !strings.Contains(logs, "group=-100") || !strings.Contains(logs, "applicant=5") {
		t.Errorf("cap warning does not name group and applicant: %q", logs)
	}
}

func TestRecoveryPastDeferralCapKeepsShortSettlementRetry(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	v := newTestService(&config.Config{
		TimeoutSeconds: 4 * 60 * 60,
		DeliveryMode:   config.DeliveryBoth,
	})
	v.timeNow = func() time.Time { return now }
	p := &pending{
		nonce:              "n",
		name:               "Applicant",
		lang:               i18n.LangEN,
		qText:              "Question",
		qOpts:              []string{"a", "b"},
		deadline:           now,
		deferredSince:      now.Add(-49 * time.Hour),
		deferralCapReached: true,
		groupMsgID:         42,
		privateMsgID:       43,
	}
	v.pend[pkey{gid, uid}] = p
	bot := newFakeVerifyBot()

	v.onRecovery(context.Background(), bot, 2*time.Minute)
	t.Cleanup(v.stopForShutdown)

	if got := p.deadline.Sub(now); got != noFaultGrace {
		t.Fatalf("post-cap recovery delay = %s, want settlement retry %s", got, noFaultGrace)
	}
	if bot.sends != 0 {
		t.Fatalf("post-cap recovery sent %d re-notification messages, want none", bot.sends)
	}
}

func TestDeferredExpiryAccumulatorSurvivesLongOutageRestart(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	now := time.Now().UTC().Truncate(time.Second)
	dir := t.TempDir()
	cfg := &config.Config{TimeoutSeconds: 4 * 60 * 60, GroupIDs: []int64{gid}}
	first := newTestService(cfg)
	first.timeNow = func() time.Time { return now }
	first.statePath = dir + "/pending.json"
	first.hbPath = dir + "/heartbeat.json"
	key := pkey{gid, uid}
	p := &pending{
		nonce:      "n",
		name:       "Applicant",
		correctIdx: 0,
		qOpts:      []string{"a", "b"},
		deadline:   now,
		groupMsgID: 42,
	}
	first.pend[key] = p
	probe := &fakeVerifyBot{getMeErr: errors.New("Telegram unavailable")}
	first.probe = probe

	first.onExpiry(context.Background(), probe, gid, uid, p.nonce, p.epoch, "timeout")
	if p.timer != nil {
		p.timer.Stop()
	}
	first.save()
	deferredSince := pendingStateUnix(t, first.statePath, "deferred_since")
	first.mu.Lock()
	first.lastOnline = now
	first.mu.Unlock()
	first.saveHeartbeat()

	now = now.Add(2 * time.Hour)
	restored := newTestService(cfg)
	restored.timeNow = func() time.Time { return now }
	restored.statePath = first.statePath
	restored.hbPath = first.hbPath
	bot := &fakeVerifyBot{}
	restored.load(bot)
	t.Cleanup(restored.stopForShutdown)

	got := restored.pend[key]
	if got == nil {
		t.Fatal("long-outage restart did not restore the deferred pending")
	}
	if want := now.Add(restored.timeout(gid)); !got.deadline.Equal(want) {
		t.Errorf("long-outage restart deadline = %v, want fresh window ending %v", got.deadline, want)
	}
	restored.save()
	if after := pendingStateUnix(t, restored.statePath, "deferred_since"); after != deferredSince {
		t.Errorf("long-outage restart changed deferred_since from %d to %d", deferredSince, after)
	}
}

func pendingStateUnix(t *testing.T, path, field string) int64 {
	t.Helper()
	var records []map[string]any
	if err := store.Load(path, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("pending state has %d records, want 1", len(records))
	}
	value, ok := records[0][field].(float64)
	if !ok || value == 0 {
		t.Fatalf("pending state field %q = %#v, want a nonzero Unix timestamp", field, records[0][field])
	}
	return int64(value)
}

func pendingStateBool(t *testing.T, path, field string) bool {
	t.Helper()
	var records []map[string]any
	if err := store.Load(path, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("pending state has %d records, want 1", len(records))
	}
	value, ok := records[0][field].(bool)
	if !ok {
		t.Fatalf("pending state field %q = %#v, want a boolean", field, records[0][field])
	}
	return value
}

func TestOfflineExpiryDoesNotLogPerPending(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
	key := pkey{-100, 5}
	p := &pending{nonce: "n", deadline: time.Now()}
	v.pend[key] = p
	var output bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		if p.timer != nil {
			p.timer.Stop()
		}
	})

	v.deferExpiry(newFakeVerifyBot(), key.gid, key.uid, p.nonce, p.epoch, "timeout")

	if strings.TrimSpace(output.String()) != "" {
		t.Errorf("one offline pending emitted a log line: %q", output.String())
	}
}

func TestOnExpiryOnlineDeclines(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240, VerifyMaxFails: 3}) // seeded online, no probe => reachable
	key := pkey{-100, 5}
	v.pend[key] = &pending{nonce: "n", deadline: time.Now(), groupMsgID: 42, privateMsgID: 43}
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
	if fb.deletes != 2 || !reflect.DeepEqual(fb.deletedChats, []int64{-100, 5}) ||
		!reflect.DeepEqual(fb.deletedMessageIDs, []int{42, 43}) {
		t.Errorf("online timeout cleanup = chats %v messages %v, want both challenge messages", fb.deletedChats, fb.deletedMessageIDs)
	}
}

func TestOnExpiryNotifiesApplicantOfRetryOutcome(t *testing.T) {
	const (
		gid = int64(-100)
		uid = int64(5)
	)
	var retryText, bannedText, noWaitText string
	for _, tt := range []struct {
		name      string
		maxFails  int
		retry     int
		wantBan   bool
		resultOut *string
	}{
		{name: "retry cooldown", maxFails: 3, retry: 600, resultOut: &retryText},
		{name: "automatic ban", maxFails: 1, retry: 600, wantBan: true, resultOut: &bannedText},
		{name: "immediate retry", maxFails: 3, retry: -1, resultOut: &noWaitText},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := newTestService(&config.Config{TimeoutSeconds: 240, VerifyMaxFails: tt.maxFails, VerifyRetrySeconds: tt.retry})
			v.pend[pkey{gid, uid}] = &pending{
				nonce:      "n",
				lang:       i18n.LangEN,
				deadline:   time.Now(),
				groupMsgID: 42,
			}
			fb := &fakeVerifyBot{}

			v.onExpiry(context.Background(), fb, gid, uid, "n", 0, "timeout")

			want := v.messages.Verification.Result.TimeoutNoWait.For(i18n.LangEN)
			if tt.retry > 0 {
				want = v.messages.Verification.Result.TimeoutRetry.Render(i18n.LangEN, tt.retry)
			}
			if tt.wantBan {
				duration := verificationBanDurationText(v.messages, i18n.LangEN, v.verificationBanDuration(gid))
				want = v.messages.Verification.Result.TimeoutBanned.Render(i18n.LangEN, duration)
			}
			if fb.sends != 1 || fb.lastSendChat != uid || fb.lastSendText != want {
				t.Fatalf("timeout result sends/chat/text = %d/%d/%q, want one applicant catalogue message %q", fb.sends, fb.lastSendChat, fb.lastSendText, want)
			}
			if (fb.bans != 0) != tt.wantBan {
				t.Errorf("timeout ban calls = %d, wantBan=%v", fb.bans, tt.wantBan)
			}
			*tt.resultOut = fb.lastSendText
		})
	}
	if retryText == bannedText || retryText == noWaitText || bannedText == noWaitText {
		t.Errorf("timeout retry, automatic-ban, and no-wait outcomes must use distinct messages")
	}
}

func TestOnExpiryOnsetLagProbeDefers(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240, VerifyMaxFails: 3}) // offlineNow == false (seeded online)
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

func TestOnExpiryStaleEpochNoop(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240, VerifyMaxFails: 3}) // online
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

func TestExpiryDeferThenOnlineStrikes(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240, VerifyMaxFails: 3})
	setOffline(v)
	key := pkey{-100, 5}
	v.pend[key] = &pending{nonce: "n", deadline: time.Now().Add(-time.Second), groupMsgID: 44, privateMsgID: 45}
	fb := &fakeVerifyBot{}
	v.onExpiry(context.Background(), fb, -100, 5, "n", 0, "timeout") // offline -> defer
	cur, ok := v.pend[key]
	if !ok || cur.done || fb.declines != 0 {
		t.Fatal("offline expiry should keep the pending and not decline")
	}
	if fb.deletes != 0 {
		t.Fatalf("offline expiry deleted %d challenge messages before settlement", fb.deletes)
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
	if fb.deletes != 2 || !reflect.DeepEqual(fb.deletedChats, []int64{-100, 5}) ||
		!reflect.DeepEqual(fb.deletedMessageIDs, []int{44, 45}) {
		t.Errorf("deferred timeout cleanup = chats %v messages %v, want both challenge messages", fb.deletedChats, fb.deletedMessageIDs)
	}
}

func TestDeferExpiryGuards(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
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

func TestNonPositiveExpiryDelayGetsStrikeFreeGrace(t *testing.T) {
	const gid, uid = int64(-999), int64(5)
	v := newTestService(&config.Config{TimeoutSeconds: 240})
	p := &pending{nonce: "unknown"}
	key := pkey{gid, uid}

	v.mu.Lock()
	v.pend[key] = p
	v.armExpiry(newFakeVerifyBot(), p, gid, uid, v.timeout(gid), "timeout")
	deadline := p.deadline
	if p.timer != nil {
		p.timer.Stop()
	}
	delete(v.pend, key)
	v.mu.Unlock()

	if remaining := time.Until(deadline); remaining < noFaultGrace-time.Second {
		t.Fatalf("non-positive expiry delay produced deadline %v (%s remaining), want strike-free grace", deadline, remaining)
	}
}

func TestHeartbeatTickRecovers(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
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
	if !ok || !p.deadline.After(time.Now().Add(v.timeout(key.gid)-10*time.Second)) {
		t.Error("recovery should refresh the pending's window")
	}
	if p != nil && p.timer != nil {
		p.timer.Stop()
	}
}

func TestHeartbeatTickOfflineKeepsClock(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
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

func TestOnRecoveryRefreshesAndRenotifies(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
	v.botUsername = "bot"
	k1, k2 := pkey{-100, 1}, pkey{-100, 2}
	v.pend[k1] = &pending{nonce: "a", name: "Alice", deadline: time.Now().Add(-time.Minute), groupMsgID: 11}
	v.pend[k2] = &pending{nonce: "b", name: "Bob", deadline: time.Now().Add(10 * time.Second), groupMsgID: 12}
	fb := &fakeVerifyBot{}
	v.onRecovery(context.Background(), fb, 3*time.Minute)
	if fb.sends != 6 { // per pending in default "both": outage notice + group challenge + private challenge
		t.Errorf("recovery should send all three messages per pending (want 6 sends), got %d", fb.sends)
	}
	for _, k := range []pkey{k1, k2} {
		p, ok := v.pend[k]
		if !ok || p.done {
			t.Fatalf("pending %v should stay live after recovery", k)
		}
		if !p.deadline.After(time.Now().Add(v.timeout(k.gid) - 10*time.Second)) {
			t.Errorf("pending %v should get a fresh full window, deadline=%v", k, p.deadline)
		}
		if p.timer != nil {
			p.timer.Stop()
		}
	}
}

func TestOnRecoveryRenotifyCooldown(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
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

func TestRecoveryPrivateDeliveryPreservesStrikeFreeExpiry(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	v := newTestService(&config.Config{
		TimeoutSeconds: 240,
		DeliveryMode:   config.DeliveryDM,
	})
	v.timeNow = func() time.Time { return now }
	p := &pending{
		nonce:      "n",
		name:       "Applicant",
		lang:       i18n.LangEN,
		qText:      "Question",
		qOpts:      []string{"a", "b"},
		correctIdx: 0,
		deadline:   now,
	}
	v.pend[pkey{gid, uid}] = p

	v.onRecovery(context.Background(), newFakeVerifyBot(), 2*time.Minute)
	t.Cleanup(v.stopForShutdown)

	if p.epoch != 1 {
		t.Fatalf("private recovery delivery re-armed expiry %d times, want only the strike-free recovery arm", p.epoch)
	}
}

func TestRenotifyFailureKeepsWorkingGroupChallenge(t *testing.T) {

	const gid, uid = int64(-100), int64(5)
	v := newTestService(&config.Config{TimeoutSeconds: 240, DeliveryMode: config.DeliveryGroup})
	v.botUsername = "bot"
	key := pkey{gid, uid}
	p := &pending{
		nonce:              "current",
		name:               "Applicant",
		lang:               i18n.LangEN,
		groupMsgID:         41,
		challengeDelivered: true,
		deadline:           time.Now().Add(time.Hour),
	}
	v.pend[key] = p
	bot := newFakeVerifyBot()
	bot.sendErrAt = map[int]error{2: errors.New("group repost failed")}

	v.renotifyPending(context.Background(), bot, gid, uid, p.name, p.messages(), p, time.Minute)

	if p.groupMsgID != 41 || !p.challengeDelivered {
		t.Fatalf("failed repost changed working challenge to message %d delivered=%v, want 41/true",
			p.groupMsgID, p.challengeDelivered)
	}
	if bot.deletes != 0 {
		t.Fatalf("failed repost deleted %v, want no challenge deletion", bot.deletedMessageIDs)
	}
}

func TestRenotifySuccessfulSendDeletesNewOrphanWhenPendingWasReplaced(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	v := newTestService(&config.Config{TimeoutSeconds: 240, DeliveryMode: config.DeliveryGroup})
	v.botUsername = "bot"
	key := pkey{gid, uid}
	old := &pending{
		nonce:              "old",
		name:               "Applicant",
		lang:               i18n.LangEN,
		groupMsgID:         41,
		challengeDelivered: true,
		deadline:           time.Now().Add(time.Hour),
	}
	v.pend[key] = old
	release := make(chan struct{}, 1)
	bot := newFakeVerifyBot()
	bot.sendMessageID = 99
	bot.sendStarted = make(chan struct{}, 1)
	bot.releaseSend = release
	bot.blockSendN = 2
	done := make(chan struct{})
	go func() {
		v.renotifyPending(context.Background(), bot, gid, uid, old.name, old.messages(), old, time.Minute)
		close(done)
	}()
	select {
	case <-bot.sendStarted:
	case <-time.After(time.Second):
		t.Fatal("recovery repost did not block after the private notice")
	}
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}()

	replacement := &pending{nonce: "replacement", groupMsgID: 77, challengeDelivered: true}
	v.mu.Lock()
	v.pend[key] = replacement
	v.mu.Unlock()
	release <- struct{}{}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery repost did not finish after release")
	}

	if v.pend[key] != replacement || replacement.groupMsgID != 77 || !replacement.challengeDelivered {
		t.Fatalf("stale recovery changed replacement pending: %+v", v.pend[key])
	}
	if bot.deletes != 1 || bot.deletedChats[0] != gid || bot.deletedMessageIDs[0] != 99 {
		t.Fatalf("stale recovery deleted chats/messages %v/%v, want [%d]/[99]",
			bot.deletedChats, bot.deletedMessageIDs, gid)
	}
}

func TestOnRecoveryShuttingDown(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
	v.pend[pkey{-100, 1}] = &pending{nonce: "a", deadline: time.Now()}
	v.shuttingDown = true
	fb := &fakeVerifyBot{}
	v.onRecovery(context.Background(), fb, time.Hour)
	if fb.sends != 0 {
		t.Error("recovery must be a no-op during shutdown")
	}
}

func TestLoadPendingFailureStillProtectsHeartbeat(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
	v.statePath = t.TempDir()
	v.hbPath = t.TempDir()

	v.load(nil)

	if v.statePath != "" {
		t.Fatalf("pending path remained writable after load failure: %q", v.statePath)
	}
	if v.hbPath != "" {
		t.Fatalf("heartbeat path remained writable after load failure: %q", v.hbPath)
	}
}

func TestLoadRefreshesAfterOutage(t *testing.T) {
	dir := t.TempDir()
	seed := newTestService(&config.Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	seed.statePath = dir + "/pending.json"
	seed.hbPath = dir + "/heartbeat.json"
	seed.pend[pkey{-100, 7}] = &pending{nonce: "x", name: "Carol", correctIdx: 0,
		qOpts: []string{"a", "b"}, deadline: time.Now().Add(30 * time.Second), groupMsgID: 5, privateMsgID: 6}
	seed.save()
	seed.mu.Lock()
	seed.lastOnline = time.Now().Add(-10 * time.Minute) // a long outage
	seed.mu.Unlock()
	seed.saveHeartbeat()

	v := newTestService(&config.Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	v.botUsername = "bot"
	v.statePath = dir + "/pending.json"
	v.hbPath = dir + "/heartbeat.json"
	fb := &fakeVerifyBot{}
	v.load(fb)

	p, ok := v.pend[pkey{-100, 7}]
	if !ok {
		t.Fatal("the pending should be restored")
	}
	if p.privateMsgID != 1 {
		t.Fatalf("restored private challenge message = %d, want replacement 1", p.privateMsgID)
	}
	if !p.deadline.After(time.Now().Add(v.timeout(-100) - 10*time.Second)) {
		t.Errorf("a long outage should restore a fresh full window, not the ~30s remaining (deadline=%v)", p.deadline)
	}
	if fb.sends == 0 {
		t.Error("a long outage should re-notify the restored applicant")
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	if !v.approve(context.Background(), fb, -100, 7) {
		t.Fatal("restored pending did not settle")
	}
	if !reflect.DeepEqual(fb.deletedChats, []int64{-100, 7, -100, 7}) ||
		!reflect.DeepEqual(fb.deletedMessageIDs, []int{5, 6, 1, 1}) {
		t.Fatalf("outage recovery cleanup = chats %v messages %v, want both old then both replacement challenges",
			fb.deletedChats, fb.deletedMessageIDs)
	}
}

func TestLoadRecoveryBothReplacesAndDeletesBothChallenges(t *testing.T) {
	const gid, uid = int64(-100), int64(7)
	now := time.Now()
	dir := t.TempDir()
	cfg := &config.Config{
		GroupIDs:       []int64{gid},
		DeliveryMode:   config.DeliveryBoth,
		TimeoutSeconds: 240,
	}
	seed := newTestService(cfg)
	seed.statePath = dir + "/pending.json"
	seed.hbPath = dir + "/heartbeat.json"
	seed.pend[pkey{gid, uid}] = &pending{
		nonce:        "x",
		name:         "Applicant",
		lang:         i18n.LangEN,
		qText:        "Question",
		qOpts:        []string{"a", "b"},
		correctIdx:   0,
		deadline:     now.Add(30 * time.Second),
		groupMsgID:   41,
		privateMsgID: 42,
	}
	seed.save()
	seed.lastOnline = now.Add(-10 * time.Minute)
	seed.saveHeartbeat()

	v := newTestService(cfg)
	v.botUsername = "bot"
	v.statePath = seed.statePath
	v.hbPath = seed.hbPath
	bot := newFakeVerifyBot()
	bot.sendMessageID = 99
	v.load(bot)
	t.Cleanup(v.stopForShutdown)

	p := v.pend[pkey{gid, uid}]
	if p == nil {
		t.Fatal("recovery did not restore the pending")
	}
	if p.groupMsgID != 99 || p.privateMsgID != 99 {
		t.Fatalf("replacement message IDs = group %d/private %d, want 99/99", p.groupMsgID, p.privateMsgID)
	}
	if !reflect.DeepEqual(bot.deletedChats, []int64{gid, uid}) ||
		!reflect.DeepEqual(bot.deletedMessageIDs, []int{41, 42}) {
		t.Fatalf("recovery deleted chats/messages %v/%v, want both stale challenges",
			bot.deletedChats, bot.deletedMessageIDs)
	}
	if p.groupMsgID == 41 || p.privateMsgID == 42 {
		t.Fatalf("stale challenge ID survived recovery: group %d/private %d", p.groupMsgID, p.privateMsgID)
	}
}

func TestRenotifyUsesDeliveryModeAndRetainsUnconfirmedPrivateID(t *testing.T) {
	const gid, uid = int64(-100), int64(7)
	tests := []struct {
		name      string
		mode      string
		sendErrAt map[int]error
		wantChats []int64
	}{
		{
			name:      "group",
			mode:      config.DeliveryGroup,
			wantChats: []int64{uid, gid},
		},
		{
			name: "dm rejection falls back to group",
			mode: config.DeliveryDM,
			sendErrAt: map[int]error{
				2: errors.New("Forbidden: bot can't initiate conversation with a user"),
			},
			wantChats: []int64{uid, uid, gid},
		},
		{
			name: "both keeps private ID after uncertain send",
			mode: config.DeliveryBoth,
			sendErrAt: map[int]error{
				3: errors.New("connection reset after request write"),
			},
			wantChats: []int64{uid, gid, uid},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newTestService(&config.Config{TimeoutSeconds: 240, DeliveryMode: tt.mode})
			v.botUsername = "bot"
			p := &pending{
				nonce:              "x",
				name:               "Applicant",
				lang:               i18n.LangEN,
				qText:              "Question",
				qOpts:              []string{"a", "b"},
				correctIdx:         0,
				deadline:           time.Now().Add(time.Minute),
				groupMsgID:         41,
				privateMsgID:       42,
				challengeDelivered: true,
			}
			v.pend[pkey{gid, uid}] = p
			bot := newFakeVerifyBot()
			bot.sendMessageID = 99
			bot.sendErrAt = tt.sendErrAt

			v.renotifyPending(context.Background(), bot, gid, uid, p.name, p.messages(), p, time.Minute)

			if p.groupMsgID != 99 || p.privateMsgID != 42 || !p.challengeDelivered {
				t.Fatalf("replacement state = group %d/private %d/delivered %t, want 99/42/true",
					p.groupMsgID, p.privateMsgID, p.challengeDelivered)
			}
			if !reflect.DeepEqual(bot.sendChats, tt.wantChats) {
				t.Fatalf("send chats = %v, want delivery-mode sequence %v", bot.sendChats, tt.wantChats)
			}
			if !reflect.DeepEqual(bot.deletedChats, []int64{gid, uid}) ||
				!reflect.DeepEqual(bot.deletedMessageIDs, []int{41, 42}) {
				t.Fatalf("deleted chats/messages = %v/%v, want both old challenges",
					bot.deletedChats, bot.deletedMessageIDs)
			}
		})
	}
}

func TestLoadQuickRestartKeepsWindow(t *testing.T) {
	dir := t.TempDir()
	seed := newTestService(&config.Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	seed.statePath = dir + "/pending.json"
	seed.hbPath = dir + "/heartbeat.json"
	seed.pend[pkey{-100, 8}] = &pending{nonce: "y", correctIdx: 0, qOpts: []string{"a", "b"},
		deadline: time.Now().Add(120 * time.Second), groupMsgID: 6, privateMsgID: 7}
	seed.save()
	seed.saveHeartbeat() // heartbeat = now (quick restart)

	v := newTestService(&config.Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	v.statePath = dir + "/pending.json"
	v.hbPath = dir + "/heartbeat.json"
	fb := &fakeVerifyBot{}
	v.load(fb)

	p, ok := v.pend[pkey{-100, 8}]
	if !ok {
		t.Fatal("the pending should be restored")
	}
	if p.privateMsgID != 7 {
		t.Fatalf("restored private challenge message = %d, want 7", p.privateMsgID)
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
	if !v.approve(context.Background(), fb, -100, 8) {
		t.Fatal("restored pending did not settle")
	}
	if !reflect.DeepEqual(fb.deletedChats, []int64{-100, 8}) ||
		!reflect.DeepEqual(fb.deletedMessageIDs, []int{6, 7}) {
		t.Fatalf("quick-restart cleanup = chats %v messages %v, want restored group and private challenges",
			fb.deletedChats, fb.deletedMessageIDs)
	}
}

func TestOutageText(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Second: i18n.Messages.Verification.Recovery.OutageSeconds.Render(i18n.LangZH, 45),
		3 * time.Minute:  i18n.Messages.Verification.Recovery.OutageMinutes.Render(i18n.LangZH, 3),
		8 * time.Hour:    i18n.Messages.Verification.Recovery.OutageHours.Render(i18n.LangZH, 8),
	}
	for d, want := range cases {
		if got := outageText(&i18n.Messages, i18n.LangZH, d); got != want {
			t.Errorf("outageText(zh, %v) = %q, want catalogue rendering %q", d, got, want)
		}
	}
	wantTraditional := i18n.Messages.Verification.Recovery.OutageMinutes.Render(i18n.LangZHHant, 3)
	if got := outageText(&i18n.Messages, i18n.LangZHHant, 3*time.Minute); got != wantTraditional {
		t.Errorf("outageText(zh-hant, 3m) = %q, want catalogue rendering %q", got, wantTraditional)
	}
	wantEnglish := i18n.Messages.Verification.Recovery.OutageHours.Render(i18n.LangEN, 8)
	if got := outageText(&i18n.Messages, i18n.LangEN, 8*time.Hour); got != wantEnglish {
		t.Errorf("outageText(en, 8h) = %q, want catalogue rendering %q", got, wantEnglish)
	}
}

func TestChallengeExpiryReason(t *testing.T) {
	tests := []struct {
		name       string
		delivered  bool
		wantStrike bool
	}{
		{name: "challenge delivered", delivered: true, wantStrike: true},
		{name: "challenge missing", delivered: false, wantStrike: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := challengeExpiryReason(tt.delivered)
			if got := strikesUser(reason); got != tt.wantStrike {
				t.Errorf("strikesUser(%q) = %v, want %v", reason, got, tt.wantStrike)
			}
		})
	}
}
