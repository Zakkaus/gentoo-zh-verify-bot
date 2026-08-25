package verify

import (
	"bytes"
	"context"
	"errors"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"log"
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
	if fb.sends != 4 { // per pending: 1 DM + 1 fresh group challenge
		t.Errorf("recovery should DM + re-post per pending (want 4 sends), got %d", fb.sends)
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

func TestRenotifyFailureKeepsWorkingGroupChallenge(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	v := newTestService(&config.Config{TimeoutSeconds: 240})
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

	v.renotifyPending(context.Background(), bot, gid, uid, p.name, p.groupMsgID, p, time.Minute)

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
	v := newTestService(&config.Config{TimeoutSeconds: 240})
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
		v.renotifyPending(context.Background(), bot, gid, uid, old.name, old.groupMsgID, old, time.Minute)
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
		qOpts: []string{"a", "b"}, deadline: time.Now().Add(30 * time.Second), groupMsgID: 5}
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
	if !p.deadline.After(time.Now().Add(v.timeout(-100) - 10*time.Second)) {
		t.Errorf("a long outage should restore a fresh full window, not the ~30s remaining (deadline=%v)", p.deadline)
	}
	if fb.sends == 0 {
		t.Error("a long outage should re-notify the restored applicant")
	}
	if p.timer != nil {
		p.timer.Stop()
	}
}

func TestLoadQuickRestartKeepsWindow(t *testing.T) {
	dir := t.TempDir()
	seed := newTestService(&config.Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	seed.statePath = dir + "/pending.json"
	seed.hbPath = dir + "/heartbeat.json"
	seed.pend[pkey{-100, 8}] = &pending{nonce: "y", correctIdx: 0, qOpts: []string{"a", "b"},
		deadline: time.Now().Add(120 * time.Second), groupMsgID: 6}
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
