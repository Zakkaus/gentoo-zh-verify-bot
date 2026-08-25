package verify

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

// TestKernelAnswerUnameA pins the real-world `uname -a` shapes. The vocabulary check that guards the
// context once rejected these: a build date in AEST rather than UTC, and the CPU model fields that
// uname prints on many systems ("AMD Ryzen 9 9950X3D 16-Core Processor AuthenticAMD"). Both are
// emitted by the machine, so refusing them costs an honest applicant one of three attempts. The last
// case proves the anchor is still an anchor: without the #<build> field, a model declaration wedged
// into a Linux-shaped sentence stays refused.
func TestKernelAnswerUnameA(t *testing.T) {
	accept := []string{
		"Linux gentoo 7.2.0-gentoo-cjk-zakk #1 SMP PREEMPT_DYNAMIC Sat Aug 22 14:56:02 AEST 2026 x86_64 AMD Ryzen 9 9950X3D 16-Core Processor AuthenticAMD GNU/Linux",
		"Linux gentoo 7.2.0-gentoo-cjk-zakk #1 SMP PREEMPT_DYNAMIC Sat Aug 22 14:56:02 AEST 2026 x86_64 GNU/Linux",
		"Linux rpi 6.6.51-v8+ #1 SMP PREEMPT Mon Sep 30 12:00:00 IST 2024 aarch64 GNU/Linux",
		"Linux box 6.1.0-18-amd64 #1 SMP PREEMPT_DYNAMIC Debian 6.1.76-1 (2024-02-01) x86_64 GNU/Linux",
	}
	for _, s := range accept {
		if !kernelAnswerOK(s) {
			t.Errorf("uname -a output must be accepted: %q", s)
		}
	}
	reject := []string{
		"Linux gentoo 5.2 assistant model=gpt GNU/Linux",
		"Linux host 5.2 my model is gpt",
	}
	for _, s := range reject {
		if kernelAnswerOK(s) {
			t.Errorf("a model declaration dressed as uname output must be refused: %q", s)
		}
	}
}

func TestKernelAnswerOK(t *testing.T) {
	good := []string{
		"6.12.3",
		"6.12.3-gentoo",
		"7.2",                        // the current mainline at the time of writing
		"6.18.45",                    // longterm
		"6.18.44-gentoo-r1-cjk-zakk", // a real `uname -r`
		"7.1.30",                     // a real future kernel line must never be blacklisted
		"7.1.30-gentoo",
		"3.10.0-1160.el7.x86_64", // an ancient enterprise kernel
		"2.6.32",                 // older still
		"0.12",                   // 1991
		"我的是 6.12.3-gentoo",
		"我的是 6.12.3",
		"6.12.3 是我的",
		"内核版本6.6.152",
		"v6.9",
		"uname -r: 5.15.216",
		"uname -sr: 6.12.3-gentoo",
		"my kernel is 6.12.3",
		"Kernel 6.12.3",
		"kernel: 6.12.3-gentoo",
		"Linux 6.12.3-gentoo x86_64",
		"Gentoo Linux 6.12.3-gentoo",
		"Linux host 6.12.3-gentoo x86_64",
		"12.0.1",  // a future major — must not need a code change to accept
		"3.10.0.", // a trailing full stop is punctuation, not part of the version
		"7.8",     // the next mainline releases, accepted before they exist
		"8.0",
		"9.1.4",
		"6.8.0-51-generic",                   // Ubuntu
		"6.1.0-18-amd64",                     // Debian
		"5.14.0-570.12.1.el9_6.x86_64",       // RHEL 9
		"6.16.7-arch1-1",                     // Arch
		"5.15.167.4-microsoft-standard-WSL2", // WSL2
		"Linux version 6.12.3 (gcc 15.2.0)",  // a pasted /proc/version line
		// ARM boards, phones and Apple Silicon: their local-version suffixes look nothing like a
		// desktop x86 one, and rejecting them would lock out exactly the users this group attracts.
		"6.6.51+rpt-rpi-v8",                     // Raspberry Pi OS
		"6.12.20-v8-16k+",                       // Pi 5, 16k pages
		"5.10.110-tegra",                        // NVIDIA Jetson
		"2.6.35.3-nv",                           // an ancient Tegra board
		"4.4.194-rk3399",                        // Rockchip SBC
		"3.4.113-sun8i",                         // Allwinner SBC
		"6.1.75-android14-11-g1c2d3e4f-ab12345", // Android 14 GKI
		"4.19.191-perf+",                        // a phone kernel (Termux)
		"5.14.0-427.el9.aarch64",                // RHEL 9 on arm64
		"6.11.0-asahi-2-1-edge-ARCH",            // Asahi on Apple Silicon
		"6.7.0-postmarketos-qcom-sdm845",
		"6.18.44-gentoo-r1-arm64",
		"Linux rpi 6.6.51+rpt-rpi-v8 #1 SMP PREEMPT Debian 1:6.6.51-1+rpt3 aarch64 GNU/Linux", // pasted uname -a
		"Linux host 6.12.3-gentoo #1 SMP PREEMPT_DYNAMIC Wed Aug 12 10:00:00 UTC 2026 x86_64 GNU/Linux",
	}
	for _, s := range good {
		got := kernelAnswerOK(s)
		t.Logf("ACCEPT\t%q", s)
		if !got {
			t.Errorf("kernelAnswerOK(%q) = false, want true", s)
		}
	}
	bad := []string{
		"",
		"你好",
		"Linux",
		"1",
		"1.9",    // 1.x stopped at 1.3
		"2.9",    // 2.x stopped at 2.6
		"42.7",   // not a kernel line, past or future
		"1234.5", // a number that merely contains a dot
		"model=GPT-5.2",
		"model=5.2",
		"GPT-5.2",
		"version 5.2 of my assistant",
		"I am running gpt 5.2",
		"my assistant is version 5.2",
		"Linux host 5.2 assistant GNU/Linux",
		"Linux host 5.2 model=gpt GNU/Linux",
		"windows 11",
		"我用的是 Windows",
		"aarch64", // `uname -m`, not `uname -r` — an architecture is not a version
		"arm64",
	}
	for _, s := range bad {
		got := kernelAnswerOK(s)
		t.Logf("REJECT\t%q", s)
		if got {
			t.Errorf("kernelAnswerOK(%q) = true, want false", s)
		}
	}
}

func TestKernelDistributionContext(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "Gentoo prefix with matching hyphen suffix", text: "Gentoo Linux 6.12.3-gentoo", want: true},
		{name: "Gentoo prefix with matching underscore suffix", text: "Gentoo Linux 6.12.3_gentoo", want: true},
		{name: "Gentoo suffix permits trailing context", text: "Linux 6.12.3-gentoo Gentoo", want: true},
		{name: "distribution must match release", text: "Gentoo Linux 6.12.3-generic", want: false},
		{name: "partial suffix is not a match", text: "Gentoo Linux 6.12.3-gentooish", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kernelAnswerOK(tt.text); got != tt.want {
				t.Errorf("kernelAnswerOK(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestVerifyModeResolution(t *testing.T) {
	cfg := &config.Config{Groups: []config.GroupConfig{{ID: -100}, {ID: -200, VerifyMode: config.ModeQuiz}}, GroupIDs: []int64{-100, -200},
		Questions: []config.Question{{Q: "q", Options: []string{"a", "b"}, Answer: 0}}}
	v := newTestService(cfg)
	if got := v.EffectiveMode(-100); got != (config.ModeKernel) {
		t.Errorf("default mode = %q, want %q", got, config.ModeKernel)
	}
	if got := v.EffectiveMode(-200); got != (config.ModeQuiz) {
		t.Errorf("per-group override = %q, want %q", got, config.ModeQuiz)
	}
	cfg.VerifyMode = (config.ModeQuiz)
	v = newTestService(cfg)
	if got := v.EffectiveMode(-100); got != (config.ModeQuiz) {
		t.Errorf("global verify_mode = %q, want %q", got, config.ModeQuiz)
	}
	if err := v.SetVerifyMode(-200, config.ModeKernel); err != nil {
		t.Fatal(err)
	}
	if got := v.EffectiveMode(-200); got != (config.ModeKernel) {
		t.Errorf("/vmode group override = %q, want %q", got, config.ModeKernel)
	}
	if err := v.SetVerifyMode(-200, ""); err != nil {
		t.Fatal(err)
	}
	if got := v.EffectiveMode(-200); got != (config.ModeQuiz) {
		t.Errorf("after clearing the override = %q, want %q", got, config.ModeQuiz)
	}
}

func TestPickModeQuizWithoutQuestions(t *testing.T) {
	v := newTestService(&config.Config{Groups: []config.GroupConfig{{ID: -100}}, GroupIDs: []int64{-100}, VerifyMode: config.ModeQuiz})
	if got := v.pickMode(-100); got != (config.ModeKernel) {
		t.Errorf("quiz mode with no questions should fall back to %q, got %q", config.ModeKernel, got)
	}
	mode, text, opts, idx := v.newChallenge(-100, i18n.LangZH)
	if mode != (config.ModeKernel) || text != kernelQuestion(&i18n.Messages, i18n.LangZH) || opts != nil || idx != -1 {
		t.Errorf("kernel challenge = (%q, %q, %v, %d), want the kernel question with no options and idx -1", mode, text, opts, idx)
	}
}

func TestPickModeMixed(t *testing.T) {
	v := newTestService(&config.Config{Groups: []config.GroupConfig{{ID: -100}}, GroupIDs: []int64{-100}, VerifyMode: config.ModeMixed,
		Questions: []config.Question{{Q: "q", Options: []string{"a", "b"}, Answer: 0}}})
	oldReader := cryptorand.Reader
	cryptorand.Reader = bytes.NewReader([]byte{0, 1})
	defer func() { cryptorand.Reader = oldReader }()

	for i, want := range []string{config.ModeKernel, config.ModeQuiz} {
		if got := v.pickMode(-100); got != want {
			t.Errorf("deterministic mixed draw %d = %q, want %q", i, got, want)
		}
	}
}

func TestKernelAnswerDMPredicate(t *testing.T) {
	v := newTestService(&config.Config{})
	dm := func(uid int64, text string) telego.Update {
		return telego.Update{Message: &telego.Message{Chat: telego.Chat{Type: "private", ID: uid},
			From: &telego.User{ID: uid}, Text: text}}
	}
	if v.KernelAnswerDM(context.TODO(), dm(5, "6.12.3")) {
		t.Error("no pending: must not capture the message")
	}
	v.pend[pkey{-100, 5}] = &pending{mode: config.ModeKernel, nonce: "n", prompted: true, deadline: time.Now().Add(time.Hour)}
	if !v.KernelAnswerDM(context.TODO(), dm(5, "6.12.3")) {
		t.Error("a plain DM during a kernel verification must be treated as the answer")
	}
	if v.KernelAnswerDM(context.TODO(), dm(5, "/start")) {
		t.Error("a command must not be swallowed as an answer")
	}
	if v.KernelAnswerDM(context.TODO(), dm(5, "   ")) {
		t.Error("an empty message must not count as an answer")
	}
	if v.KernelAnswerDM(context.TODO(), dm(6, "6.12.3")) {
		t.Error("another user's DM must not match")
	}
	v.pend[pkey{-100, 7}] = &pending{mode: config.ModeQuiz, nonce: "n", prompted: true, deadline: time.Now().Add(time.Hour)}
	if v.KernelAnswerDM(context.TODO(), dm(7, "6.12.3")) {
		t.Error("a quiz applicant's DM must fall through to the auto-reply")
	}
}

// noLinuxNow builds a no-Linux declaration carrying the current minute, the form the prompt asks
// for — a canned string without it is not enough (see minuteProofOK).
func noLinuxNow(prefix string) string {
	return fmt.Sprintf("%s %d", prefix, time.Now().Minute())
}

// kernelTestV builds a Service with one kernel pending for user 5 in group -100.
func kernelTestV() (*Service, *fakeVerifyBot) {
	v := newTestService(&config.Config{Groups: []config.GroupConfig{{ID: -100}}, GroupIDs: []int64{-100}, VerifyMaxFails: 3})
	v.pend[pkey{-100, 5}] = &pending{mode: config.ModeKernel, nonce: "n", prompted: true, groupMsgID: 42, deadline: time.Now().Add(time.Hour)}
	return v, newFakeVerifyBot()
}

func TestGradeKernelAnswerCorrect(t *testing.T) {
	v, fb := kernelTestV()
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "6.18.44-gentoo-r1") // not the printed example
	if fb.approves != 1 {
		t.Errorf("a correct kernel version should approve once, got %d", fb.approves)
	}
	if _, ok := v.pend[pkey{-100, 5}]; ok {
		t.Error("the pending should be consumed after approval")
	}
}

func TestFinishKernelPassUsesValidatedNonce(t *testing.T) {
	tests := []struct {
		name           string
		validatedNonce string
		currentNonce   string
		wantApproves   int
		wantPending    bool
	}{
		{name: "matching pending", validatedNonce: "current", currentNonce: "current", wantApproves: 1},
		{name: "replacement pending", validatedNonce: "old", currentNonce: "new", wantPending: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, fb := kernelTestV()
			key := pkey{-100, 5}
			v.pend[key].nonce = tt.currentNonce
			v.finishKernelPass(context.Background(), fb, key.gid, key.uid, tt.validatedNonce, i18n.LangZH, i18n.LangZH)
			if fb.approves != tt.wantApproves {
				t.Errorf("approves = %d, want %d", fb.approves, tt.wantApproves)
			}
			p, pending := v.pend[key]
			if pending != tt.wantPending {
				t.Errorf("pending exists = %v, want %v", pending, tt.wantPending)
			}
			if pending && p.done {
				t.Error("a replacement pending must remain live")
			}
		})
	}
}

func TestGradeKernelAnswerRetries(t *testing.T) {
	v, fb := kernelTestV()
	for i := 1; i < kernelMaxTries; i++ {
		v.gradeKernelAnswer(context.Background(), fb, -100, 5, "abc")
		if fb.declines != 0 {
			t.Fatalf("try %d should not decline yet", i)
		}
		p, ok := v.pend[pkey{-100, 5}]
		if !ok || p.tries != i {
			t.Fatalf("try %d: pending gone or tries=%d", i, p.tries)
		}
	}
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "12345")
	if fb.declines != 1 {
		t.Errorf("the last try should decline once, got %d", fb.declines)
	}
	if _, ok := v.pend[pkey{-100, 5}]; ok {
		t.Error("the pending should be consumed after the final wrong answer")
	}
	// A further message must not decline again (no pending left to charge).
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "6.12.3")
	if fb.declines != 1 || fb.approves != 0 {
		t.Errorf("a message after the decline must be inert: declines=%d approves=%d", fb.declines, fb.approves)
	}
}

func TestGradeKernelAnswerChannelGate(t *testing.T) {
	const requiredChannel = int64(-400)
	tests := []struct {
		name        string
		member      telego.ChatMember
		memberErr   error
		wantUsers   []int64
		wantApprove int
		wantPending bool
	}{
		{
			name:        "applicant lookup blocks non-member",
			member:      &telego.ChatMemberLeft{Status: telego.MemberStatusLeft},
			wantUsers:   []int64{5},
			wantPending: true,
		},
		{
			name:        "bot self-probe uses required channel",
			memberErr:   errors.New("membership unavailable"),
			wantUsers:   []int64{5, 1},
			wantApprove: 1,
		},
		{
			name:        "joined applicant passes",
			member:      &telego.ChatMemberMember{Status: telego.MemberStatusMember},
			wantUsers:   []int64{5},
			wantApprove: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, bot := kernelTestV()
			if err := v.updateGroupSettings(-100, func(_ store.GroupView, overrides *store.GroupOverrides) {
				channelID := requiredChannel
				display := "@required"
				overrides.RequiredChannelID = &channelID
				overrides.ChannelDisplay = &display
			}); err != nil {
				t.Fatal(err)
			}
			v.botID = 1
			bot.member = tt.member
			bot.memberErr = tt.memberErr

			v.gradeKernelAnswer(context.Background(), bot, -100, 5, "6.12.3")

			if len(bot.memberRequests) != len(tt.wantUsers) {
				t.Fatalf("GetChatMember requests = %d, want %d", len(bot.memberRequests), len(tt.wantUsers))
			}
			for i, request := range bot.memberRequests {
				if request.ChatID.ID != requiredChannel || request.UserID != tt.wantUsers[i] {
					t.Errorf("GetChatMember request %d = chat %d user %d, want chat %d user %d",
						i, request.ChatID.ID, request.UserID, requiredChannel, tt.wantUsers[i])
				}
			}
			if bot.approves != tt.wantApprove {
				t.Errorf("approves = %d, want %d", bot.approves, tt.wantApprove)
			}
			_, pending := v.pend[pkey{-100, 5}]
			if pending != tt.wantPending {
				t.Errorf("pending remains = %v, want %v", pending, tt.wantPending)
			}
		})
	}
}

func TestKernelPendingSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	seed := newTestService(&config.Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	seed.statePath = dir + "/pending.json"
	seed.pend[pkey{-100, 7}] = &pending{mode: config.ModeKernel, tries: 1, nonce: "x", name: "Carol",
		qText: kernelQuestion(&i18n.Messages, i18n.LangZH), correctIdx: -1, deadline: time.Now().Add(time.Minute), groupMsgID: 5}
	seed.pend[pkey{-100, 8}] = &pending{mode: config.ModeQuiz, nonce: "y", correctIdx: 0,
		qOpts: []string{"a", "b"}, deadline: time.Now().Add(time.Minute)}
	seed.save()

	v := newTestService(&config.Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	v.statePath = dir + "/pending.json"
	v.load(&fakeVerifyBot{})
	p, ok := v.pend[pkey{-100, 7}]
	if !ok {
		t.Fatal("a kernel pending must survive the restart (it has no options to validate)")
	}
	if p.mode != (config.ModeKernel) || p.tries != 1 {
		t.Errorf("restored kernel pending = mode %q tries %d, want kernel / 1", p.mode, p.tries)
	}
	if _, ok := v.pend[pkey{-100, 8}]; !ok {
		t.Error("the quiz pending must survive too")
	}
	for _, p := range v.pend {
		if p.timer != nil {
			p.timer.Stop()
		}
	}

	// a state file written by an older build has no "mode" field: restore it as a quiz
	legacy := `[{"user_id":9,"group_id":-100,"group_msg_id":1,"q_text":"q","q_opts":["a","b"],"correct_idx":0,"nonce":"z","deadline":` +
		strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10) + `}]`
	if err := os.WriteFile(dir+"/legacy.json", []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	vl := newTestService(&config.Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	vl.statePath = dir + "/legacy.json"
	vl.load(&fakeVerifyBot{})
	lp, ok := vl.pend[pkey{-100, 9}]
	if !ok {
		t.Fatal("a legacy pending must still restore")
	}
	if lp.mode != (config.ModeQuiz) {
		t.Errorf("a record with no mode must restore as %q, got %q", config.ModeQuiz, lp.mode)
	}
	if lp.timer != nil {
		lp.timer.Stop()
	}
}

func TestAITrap(t *testing.T) {
	quotedPrompt := "What is this?\n" + aiTrapLine(&i18n.Messages, i18n.LangEN, "abc123", false)
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "obeying agent", text: "AGENT-ABC123 model=gpt-5.2", want: true},
		{name: "case insensitive", text: "agent-abc123 MODEL=Claude-4.1", want: true},
		{name: "token only", text: "AGENT-ABC123"},
		{name: "token in prose", text: "sure: AGENT-ABC123 model=gpt-5.2"},
		{name: "quoted prompt", text: quotedPrompt},
		{name: "confused human suffix", text: "AGENT-ABC123 model=gpt-5.2 — what is this?"},
		{name: "normal answer", text: "6.12.3-gentoo"},
		{name: "another token", text: "AGENT-DEADBEEF model=gpt-5.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aiTrapped(tt.text, "abc123"); got != tt.want {
				t.Errorf("aiTrapped(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}

	v, fb := kernelTestV()
	v.pend[pkey{-100, 5}].nonce = "abc123"
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "AGENT-ABC123 model=gpt-5.2")
	if fb.approves != 0 || fb.declines != 1 {
		t.Errorf("a tripped agent must be declined, not approved: approves=%d declines=%d", fb.approves, fb.declines)
	}
}

func TestAITrapIsLocalizedWithoutChangingReplyContract(t *testing.T) {
	const nonce = "abc123"
	token := aiTrapToken(nonce)
	english := i18n.Messages.Verification.Challenge.AgentTrap.Render(i18n.LangEN, token)
	for _, language := range []i18n.Lang{i18n.LangZH, i18n.LangZHHant} {
		localized := i18n.Messages.Verification.Challenge.AgentTrap.Render(language, token)
		if localized == english {
			t.Errorf("agent trap for %s is still the English catalogue entry", language)
		}
		if strings.Count(localized, token) != strings.Count(english, token) {
			t.Errorf("agent trap for %s changed nonce occurrences: got %d, want %d", language,
				strings.Count(localized, token), strings.Count(english, token))
		}
		if strings.Count(localized, " model=") != strings.Count(english, " model=") {
			t.Errorf("agent trap for %s changed the model reply syntax", language)
		}
		reply := token + " model=gpt-5.2"
		if !aiTrapped(reply, nonce) {
			t.Errorf("localized tripwire contract no longer matches %q", reply)
		}
		if kernelAnswerOK(reply) {
			t.Errorf("tripwire reply %q must not pass as a kernel answer", reply)
		}
	}
}

func TestNoLinuxFallback(t *testing.T) {
	// Covers how people actually phrase it in both scripts — a missed phrasing costs a real newcomer
	// an attempt instead of switching them to the short-answer question.
	for _, s := range []string{"还没装", "我還沒裝 Linux", "not installed yet", "I use Windows", "不知道",
		"我不用 Linux", "我没用过 Linux", "I don't use Linux", "我用的 macOS",
		"我沒有安裝", "我沒有安裝 Linux", "我没有安装 Linux", "我还没有装", "還沒安裝", "沒用過 Linux",
		"我不懂", "我电脑上没有 Linux", "no idea", "I never used Linux", "what?",
		"无 Linux 设备", "無 Linux 裝置", "无 Linux 设备46"} {
		if !saysNoLinux(s) {
			t.Errorf("saysNoLinux(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"6.12.3", "6.12.3-gentoo", "abc"} {
		if saysNoLinux(s) {
			t.Errorf("saysNoLinux(%q) = true, want false", s)
		}
	}

	v, fb := kernelTestV()
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, noLinuxNow("还没装 Linux"))
	p, ok := v.pend[pkey{-100, 5}]
	if !ok || p.tries != 0 {
		t.Fatalf("the fallback must not cost an attempt: ok=%v tries=%d", ok, p.tries)
	}
	if !p.hinted || len(p.fbAnswers) == 0 {
		t.Fatalf("the pending should have moved to a short-answer question: hinted=%v answers=%v", p.hinted, p.fbAnswers)
	}
	// the question itself must not leak its answer — that was the whole point of dropping the
	// "go read the version off kernel.org" hint
	if fallbackAnswerOK(p.qText, p.fbAnswers) {
		t.Errorf("the fallback question prints its own answer: %q", p.qText)
	}
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, noLinuxNow("还没装"))
	if v.pend[pkey{-100, 5}].tries != 1 {
		t.Error("a second 'not installed' reply must be graded as a wrong answer")
	}
	// answering the short question passes; so does a real kernel version, in case they went to install
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, p.fbAnswers[0])
	if fb.approves != 1 {
		t.Errorf("the short answer should approve, got %d approves", fb.approves)
	}

	v2, fb2 := kernelTestV()
	v2.gradeKernelAnswer(context.Background(), fb2, -100, 5, noLinuxNow("我没装 Linux"))
	v2.gradeKernelAnswer(context.Background(), fb2, -100, 5, "6.18.44-gentoo-r1") // not the printed example
	if fb2.approves != 1 {
		t.Errorf("a kernel version must still pass after the fallback, got %d approves", fb2.approves)
	}
}

func TestFallbackAnswerIsNotGradedWhilePromptDeliveryIsInFlight(t *testing.T) {
	v, bot := kernelTestV()
	release := make(chan struct{}, 1)
	bot.sendStarted = make(chan struct{}, 1)
	bot.releaseSend = release
	done := make(chan struct{})
	go func() {
		v.gradeKernelAnswer(context.Background(), bot, -100, 5, noLinuxNow("not installed yet"))
		close(done)
	}()
	select {
	case <-bot.sendStarted:
	case <-time.After(time.Second):
		t.Fatal("fallback prompt did not block in delivery")
	}
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}()

	v.gradeKernelAnswer(context.Background(), newFakeVerifyBot(), -100, 5, "wrong")
	if p := v.pend[pkey{-100, 5}]; p == nil || p.tries != 0 {
		t.Fatalf("text sent before fallback confirmation changed attempts: %+v", p)
	}

	release <- struct{}{}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fallback prompt delivery did not finish after release")
	}
	if p := v.pend[pkey{-100, 5}]; p == nil || len(p.fbAnswers) == 0 {
		t.Fatalf("confirmed fallback delivery did not activate the fallback: %+v", p)
	}
}

func TestDefiniteFallbackPromptFailureRestoresRetryableKernelState(t *testing.T) {
	v, failed := kernelTestV()
	failed.sendErr = &ta.Error{ErrorCode: 429, Description: "Too Many Requests"}
	failed.sendFailN = 1

	v.gradeKernelAnswer(context.Background(), failed, -100, 5, noLinuxNow("not installed yet"))

	key := pkey{int64(-100), int64(5)}
	p := v.pend[key]
	if p == nil || p.tries != 0 || p.hinted || len(p.fbAnswers) != 0 {
		t.Fatalf("definite fallback send failure left non-retryable state: %+v", p)
	}

	retry := newFakeVerifyBot()
	v.gradeKernelAnswer(context.Background(), retry, key.gid, key.uid, noLinuxNow("not installed yet"))
	p = v.pend[key]
	if p == nil || p.tries != 0 || !p.hinted || len(p.fbAnswers) == 0 {
		t.Fatalf("retry after definite fallback failure did not deliver fallback: %+v", p)
	}
}

func TestUncertainFallbackPromptDeliveryDoesNotChargeAnswersBeforeRetry(t *testing.T) {
	v, uncertain := kernelTestV()
	uncertain.sendErr = errors.New("connection reset after request write")
	uncertain.sendFailN = 1

	v.gradeKernelAnswer(context.Background(), uncertain, -100, 5, noLinuxNow("not installed yet"))
	v.gradeKernelAnswer(context.Background(), newFakeVerifyBot(), -100, 5, "wrong")

	key := pkey{int64(-100), int64(5)}
	p := v.pend[key]
	if p == nil || p.tries != 0 {
		t.Fatalf("uncertain fallback delivery charged an unconfirmed answer: %+v", p)
	}

	retry := newFakeVerifyBot()
	active, _, _, err := v.sendDMChallengeForGroup(context.Background(), retry, key.gid, key.uid, true)
	if err != nil || !active {
		t.Fatalf("fallback retry = active %v error %v, want confirmed delivery", active, err)
	}
	answer := p.fbAnswers[0]
	v.gradeKernelAnswer(context.Background(), retry, key.gid, key.uid, answer)
	if retry.approves != 1 {
		t.Fatalf("confirmed fallback answer produced %d approvals, want 1", retry.approves)
	}
}

func TestUncertainFallbackDeliveryRemainsUngraduatedAfterRestart(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	path := t.TempDir() + "/pending.json"
	before, uncertain := kernelTestV()
	before.statePath = path
	uncertain.sendErr = errors.New("connection reset after request write")
	uncertain.sendFailN = 1
	before.gradeKernelAnswer(context.Background(), uncertain, gid, uid, noLinuxNow("not installed yet"))
	before.Shutdown()

	after := newTestService(&config.Config{
		Groups:         []config.GroupConfig{{ID: gid}},
		GroupIDs:       []int64{gid},
		VerifyMaxFails: 3,
	})
	after.statePath = path
	after.load(newFakeVerifyBot())
	t.Cleanup(after.Shutdown)
	p := after.pend[pkey{gid, uid}]
	if p == nil || !p.fallbackPending || len(p.fbAnswers) == 0 {
		t.Fatalf("restored uncertain fallback state = %+v", p)
	}
	dm := telego.Update{Message: &telego.Message{
		Chat: telego.Chat{Type: telego.ChatTypePrivate, ID: uid},
		From: &telego.User{ID: uid},
		Text: "wrong",
	}}
	if after.KernelAnswerDM(context.Background(), dm) {
		t.Error("uncertain fallback was gradeable after restart")
	}
	after.gradeKernelAnswer(context.Background(), newFakeVerifyBot(), gid, uid, dm.Message.Text)
	if p.tries != 0 {
		t.Fatalf("uncertain fallback charged %d attempts after restart, want 0", p.tries)
	}
}

func TestFallbackAnswerMatching(t *testing.T) {
	v := newTestService(&config.Config{FallbackQuestions: []config.ShortQuestion{{
		Q:       "Which package manager?",
		Answers: []string{"emerge", "Portage"},
	}}})
	_, answers := v.fallbackQuestion(-100, i18n.LangZH)
	tests := []struct {
		text string
		want bool
	}{
		{text: "emerge", want: true},
		{text: "EMERGE", want: true},
		{text: "“emerge”", want: true},
		{text: "Portage。", want: true},
		{text: "用 emerge 装"},
		{text: "emerge portage"},
		{text: "emergency"},
		{text: "不知道"},
	}
	for _, tt := range tests {
		if got := fallbackAnswerOK(tt.text, answers); got != tt.want {
			t.Errorf("fallbackAnswerOK(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestCopiedSampleBounced(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{text: "X.Y.Z-gentoo", want: true},
		{text: " x.y.z-GENTOO ", want: true},
		{text: "X.Y.Z"},
		{text: "7.1.30"},
		{text: "7.1.30-gentoo"},
	}
	for _, tt := range tests {
		if got := copiedSample(tt.text); got != tt.want {
			t.Errorf("copiedSample(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}

	v, fb := kernelTestV()
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, samplePrompt)
	if p := v.pend[pkey{-100, 5}]; p == nil || p.tries != 0 || !p.sampleBounced {
		t.Fatalf("the nudge should cost no attempt and be marked spent: %+v", p)
	}
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, samplePrompt)
	if p := v.pend[pkey{-100, 5}]; p == nil || p.tries != 1 || fb.approves != 0 {
		t.Errorf("a repeated placeholder should be one wrong answer, pending=%+v approves=%d", p, fb.approves)
	}

	v2, fb2 := kernelTestV()
	v2.gradeKernelAnswer(context.Background(), fb2, -100, 5, "7.1.30")
	if fb2.approves != 1 {
		t.Errorf("a real 7.1.30 kernel should approve at once, got %d approves", fb2.approves)
	}
}

func TestKernelPromptLocalised(t *testing.T) {
	zhQuestion := kernelQuestion(&i18n.Messages, i18n.LangZH)
	zhPrompt := i18n.Messages.Verification.Challenge.KernelPrompt.Render(i18n.LangZH, zhQuestion, 3)
	zhTrap := i18n.Messages.Verification.Challenge.AgentTrap.Render(i18n.LangZH, aiTrapToken("abc123"))
	zh := kernelPromptHTML(&i18n.Messages, i18n.LangZH, zhQuestion, 3, "abc123", true)
	if !strings.Contains(zh, zhPrompt) || !strings.Contains(zh, zhTrap) {
		t.Errorf("zh prompt missing catalogue wording or token: %s", zh)
	}

	zhHantQuestion := kernelQuestion(&i18n.Messages, i18n.LangZHHant)
	zhHantPrompt := i18n.Messages.Verification.Challenge.KernelPrompt.Render(i18n.LangZHHant, zhHantQuestion, 3)
	if !strings.Contains(kernelPromptHTML(&i18n.Messages, i18n.LangZHHant, zhHantQuestion, 3, "n", true), zhHantPrompt) {
		t.Error("zh-hant prompt should use its catalogue wording")
	}

	enQuestion := kernelQuestion(&i18n.Messages, i18n.LangEN)
	enPrompt := i18n.Messages.Verification.Challenge.KernelPrompt.Render(i18n.LangEN, enQuestion, 2)
	en := kernelPromptHTML(&i18n.Messages, i18n.LangEN, enQuestion, 2, "n", true)
	if !strings.Contains(en, enPrompt) {
		t.Errorf("en prompt missing catalogue wording: %s", en)
	}
	for _, locale := range i18n.Languages() {
		prompt := kernelPromptHTML(&i18n.Messages, locale, kernelQuestion(&i18n.Messages, locale), 3, "n", true)
		if !strings.Contains(prompt, samplePrompt) || strings.Contains(prompt, "7.1.30") {
			t.Errorf("catalog %q must print only the impossible placeholder: %s", locale, prompt)
		}
	}
	// The collapsed quote is Bot API 7.4; the fallback rendering drops it but must keep every word,
	// so an old self-hosted API server that rejects the entity still gets a complete question.
	plain := kernelPromptHTML(&i18n.Messages, i18n.LangZH, zhQuestion, 3, "abc123", false)
	if strings.Contains(plain, "<blockquote") {
		t.Error("the fallback rendering must not use the blockquote entity")
	}
	if !strings.Contains(plain, zhPrompt) || !strings.Contains(plain, zhTrap) {
		t.Errorf("the fallback rendering lost catalogue content: %s", plain)
	}
}

func TestFallbackWebsiteAnswers(t *testing.T) {
	_, community := i18n.Messages.Verification.Challenge.FallbackQuestions[0].For(i18n.LangZH)
	_, official := i18n.Messages.Verification.Challenge.FallbackQuestions[1].For(i18n.LangZH)
	tests := []struct {
		name    string
		text    string
		answers []string
		want    bool
	}{
		{name: "community bare", text: "gentoozh.org", answers: community, want: true},
		{name: "community case", text: "GentooZH.org", answers: community, want: true},
		{name: "community scheme", text: "https://gentoozh.org", answers: community, want: true},
		{name: "community URL", text: "http://www.gentoozh.org/", answers: community, want: true},
		{name: "community punctuation", text: "（gentoozh.org。）", answers: community, want: true},
		{name: "official bare", text: "gentoo.org", answers: official, want: true},
		{name: "official URL", text: "https://www.gentoo.org/", answers: official, want: true},
		{name: "both for community", text: "gentoozh.org gentoo.org", answers: community},
		{name: "both for official", text: "gentoozh.org gentoo.org", answers: official},
		{name: "community in prose", text: "是 gentoozh.org", answers: community},
		{name: "official in prose", text: "官网是 gentoo.org", answers: official},
		{name: "different community", text: "gentoo-zh.org", answers: community},
		{name: "wrong official", text: "gentoozh.org", answers: official},
		{name: "unknown", text: "不知道", answers: community},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fallbackAnswerOK(tt.text, tt.answers); got != tt.want {
				t.Errorf("fallbackAnswerOK(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestCopiedSampleGuardCoversFallback(t *testing.T) {
	v, fb := kernelTestV()
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, noLinuxNow("我不用 Linux")) // -> short-answer question
	if len(v.pend[pkey{-100, 5}].fbAnswers) == 0 {
		t.Fatal("the applicant should have been moved to the fallback question")
	}
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, samplePrompt) // the printed placeholder
	if fb.approves != 0 {
		t.Error("pasting the printed example must not approve from the fallback path either")
	}
	if !v.pend[pkey{-100, 5}].sampleBounced {
		t.Error("the nudge should have been spent")
	}
}

func TestUnpromptedDMIsNotAnAnswer(t *testing.T) {
	v := newTestService(&config.Config{})
	dm := telego.Update{Message: &telego.Message{Chat: telego.Chat{Type: "private", ID: 5},
		From: &telego.User{ID: 5}, Text: "已关注"}}
	v.pend[pkey{-100, 5}] = &pending{mode: config.ModeKernel, nonce: "n", deadline: time.Now().Add(time.Hour)}
	if v.KernelAnswerDM(context.TODO(), dm) {
		t.Error("a DM must not be graded before the question has been sent")
	}
	prompt, ok := v.pendingDMChallenge(-100, 5)
	if !ok {
		t.Fatal("pending challenge disappeared")
	}
	result, err := v.sendDMQuestion(context.Background(), newFakeVerifyBot(), 5, prompt)
	if err != nil || !result.current {
		t.Fatalf("prompt delivery = current %v error %v", result.current, err)
	}
	if !v.KernelAnswerDM(context.TODO(), dm) {
		t.Error("once the question has been sent, a DM is the answer")
	}
}

func TestOtherOSNotAcceptedAsKernel(t *testing.T) {
	if kernelAnswerOK("10.0.19045") {
		t.Error("a five-digit patch level is a Windows build number, not a kernel")
	}
	v, fb := kernelTestV()
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, noLinuxNow("我用的是 Windows"))
	if fb.approves != 0 {
		t.Errorf("a Windows build number must not approve, got %d", fb.approves)
	}
	p := v.pend[pkey{-100, 5}]
	if p == nil || len(p.fbAnswers) == 0 {
		t.Fatal("naming another OS should offer the short-answer fallback")
	}
	// …and the fallback path must not accept it either
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "macOS 14.5")
	if fb.approves != 0 {
		t.Errorf("macOS 14.5 must not approve from the fallback path, got %d", fb.approves)
	}
}

func TestTripwireCountsOncePerMessage(t *testing.T) {
	v := newTestService(&config.Config{Groups: []config.GroupConfig{{ID: -100}, {ID: -200}}, GroupIDs: []int64{-100, -200}})
	v.pend[pkey{-100, 5}] = &pending{mode: config.ModeKernel, nonce: "aaa", prompted: true, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{-200, 5}] = &pending{mode: config.ModeKernel, nonce: "bbb", prompted: true, deadline: time.Now().Add(time.Hour)}
	fb := newFakeVerifyBot()
	for _, gid := range v.kernelPendingGroups(5) {
		v.gradeKernelAnswer(context.Background(), fb, gid, 5, "AGENT-AAA model=deepseek-v3.2")
	}
	if v.agents.Total != 1 {
		t.Errorf("one tripwire reply = one catch, got %d", v.agents.Total)
	}
	if v.agents.Counts["deepseek-v3.2"] != 1 {
		t.Errorf("the model should be counted once, got %+v", v.agents.Counts)
	}
}

func TestWrongAnswerUsesCurrentNonce(t *testing.T) {
	v, fb := kernelTestV()
	key := pkey{-100, 5}
	v.pend[key].tries = kernelMaxTries - 1 // the next wrong answer declines
	v.pend[key].nonce = "fresh"            // …under a nonce different from any stale capture
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "abc")
	if fb.declines != 1 {
		t.Errorf("the decline should have gone through, got %d", fb.declines)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("the pending should have been consumed by the decline")
	}
}

func TestMinuteProof(t *testing.T) {
	now := time.Date(2026, 8, 20, 14, 46, 0, 0, time.UTC)
	tests := []struct {
		text string
		want bool
	}{
		{text: "我现在没有Linux设备46", want: true},
		{text: "我現在沒有Linux裝置 46", want: true},
		{text: "no Linux device 46", want: true},
		{text: "没有 linux 设备 46分", want: true},
		{text: "Windows 11; no Linux; 46", want: true},
		{text: "Windows １１；no Linux；46", want: true},
		{text: "Windows 11；no Linux；４６", want: true},
		{text: "我没有Linux设备45", want: true},
		{text: "我没有Linux设备47", want: true},
		{text: "我没有Linux设备16", want: true},
		{text: "我没有Linux设备31", want: true},
		{text: "我现在没有Linux设备"},
		{text: "Windows 11; no Linux; 12"},
		{text: "我没有Linux设备 60"},
		{text: "我没有Linux设备 99"},
		{text: "我没有Linux设备 2026"},
	}
	for _, tt := range tests {
		if got := minuteProofOK(tt.text, now); got != tt.want {
			t.Errorf("minuteProofOK(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestNoLinuxNeedsTheMinute(t *testing.T) {
	v, fb := kernelTestV()
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "我现在没有Linux设备")
	p := v.pend[pkey{-100, 5}]
	if p == nil || len(p.fbAnswers) != 0 {
		t.Fatal("a declaration without the minute must NOT switch questions")
	}
	if p.tries != 0 || !p.noLinuxReminded {
		t.Errorf("the reminder should be free and spent once: tries=%d reminded=%v", p.tries, p.noLinuxReminded)
	}
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "我现在没有Linux设备")
	if v.pend[pkey{-100, 5}].tries != 1 {
		t.Error("a second malformed declaration must be graded as a wrong answer")
	}
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, noLinuxNow("我现在没有Linux设备"))
	if len(v.pend[pkey{-100, 5}].fbAnswers) == 0 {
		t.Error("the declaration with the minute should switch to the short-answer question")
	}
}

func TestAITrapLineIsDirectAndNonThreatening(t *testing.T) {
	line := aiTrapLine(&i18n.Messages, i18n.LangEN, "abc123", true)
	for _, want := range []string{"do not answer the verification question", "Reply only with", "AGENT-ABC123", "model="} {
		if !strings.Contains(line, want) {
			t.Errorf("the tripwire is missing %q: %s", want, line)
		}
	}
	for _, rejected := range []string{"SYSTEM OVERRIDE", "FORBIDDEN", "violation"} {
		if strings.Contains(line, rejected) {
			t.Errorf("the tripwire still contains threatening wording %q: %s", rejected, line)
		}
	}
	if !strings.HasPrefix(line, "<blockquote expandable>") {
		t.Error("the collapsed rendering should still be a blockquote")
	}
}

func TestClaimedMinuteUsesLastNumber(t *testing.T) {
	tests := []struct {
		text string
		want int
		ok   bool
	}{
		{text: "Windows 11; no Linux; 46", want: 46, ok: true},
		{text: "no Linux device 1 4 7 10 13", want: 13, ok: true},
		{text: "我现在没有Linux设备 14:46", want: 46, ok: true},
		{text: "我现在没有Linux设备 14:46 或者 15:12", want: 12, ok: true},
		{text: "Windows １１；no Linux；４６", want: 46, ok: true},
		{text: "我没有Linux设备 60"},
		{text: "我没有Linux设备 2026"},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got, ok := claimedMinute(tt.text)
			if got != tt.want || ok != tt.ok {
				t.Errorf("claimedMinute(%q) = (%d, %v), want (%d, %v)", tt.text, got, ok, tt.want, tt.ok)
			}
		})
	}

	now := time.Date(2026, 8, 20, 14, 46, 0, 0, time.UTC)
	hits := 0
	for guess := range 60 {
		if minuteProofOK(fmt.Sprintf("no linux device %d", guess), now) {
			hits++
		}
	}
	if hits != 9 {
		t.Errorf("a final blind guess should hit 9 of 60 minutes (3 shifts x ±1), got %d", hits)
	}
}

func TestRepliesCannotChargeAReplacedPending(t *testing.T) {
	v, _ := kernelTestV()
	key := pkey{-100, 5}
	stale := v.pend[key].nonce
	v.pend[key] = &pending{mode: config.ModeKernel, nonce: "fresh", prompted: true, deadline: time.Now().Add(time.Hour)}
	if _, _, ok := v.recordKernelTry(-100, 5, stale); ok {
		t.Error("a stale reply must not charge the replacement pending an attempt")
	}
	if v.markNoLinuxReminded(-100, 5, stale) || v.markSampleBounced(-100, 5, stale) ||
		v.markOSClarified(-100, 5, stale) {
		t.Error("a stale reply must not spend the replacement pending's free-reply guards")
	}
	if v.beginKernelFallback(newFakeVerifyBot(), -100, 5, stale, "q", []string{"a"}) {
		t.Error("a stale reply must not switch the replacement pending's question")
	}
	if p := v.pend[key]; p.tries != 0 || p.hinted || p.sampleBounced || p.noLinuxReminded || p.osClarified {
		t.Errorf("the replacement pending should be untouched: %+v", p)
	}
}

func TestReapplyKeepsAttempts(t *testing.T) {
	v := newTestService(&config.Config{Groups: []config.GroupConfig{{ID: -100}}, GroupIDs: []int64{-100},
		TimeoutSeconds: 240, VerifyMode: config.ModeKernel, DeliveryMode: config.DeliveryGroup})
	key := pkey{-100, 5}
	old := &pending{mode: config.ModeKernel, nonce: "old", prompted: true, tries: 2, hinted: true,
		sampleBounced: true, noLinuxReminded: true, osClarified: true, groupMsgID: 42, privateMsgID: 43,
		deadline: time.Now().Add(time.Hour)}
	v.pend[key] = old
	bot := newFakeVerifyBot()
	update := telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: -100},
		From: telego.User{ID: 5, FirstName: "Applicant"},
	}}

	runFakeHandler(t, newAPITestBot(t, bot), v.OnJoinRequest, update)

	p := v.pend[key]
	if p == nil || p == old {
		t.Fatalf("onJoinRequest did not install a fresh pending: old=%p new=%p", old, p)
	}
	if !old.done {
		t.Error("onJoinRequest must retire the replaced pending")
	}
	if p.tries != 2 || !p.hinted || !p.sampleBounced || !p.noLinuxReminded || !p.osClarified {
		t.Errorf("replacement did not inherit attempts and spent guards: %+v", p)
	}
	if bot.sends != 1 || bot.deletes != 2 {
		t.Errorf("real reapply path sent/deleted = %d/%d, want 1/2", bot.sends, bot.deletes)
	}
	if len(bot.deletedChats) != 2 || bot.deletedChats[0] != -100 || bot.deletedChats[1] != 5 ||
		bot.deletedMessageIDs[0] != 42 || bot.deletedMessageIDs[1] != 43 {
		t.Errorf("reapply cleanup = chats %v messages %v, want old group and private challenges",
			bot.deletedChats, bot.deletedMessageIDs)
	}
	if p.timer != nil {
		p.timer.Stop()
	}
}

func TestOSNameWithRealKernelIsClarified(t *testing.T) {
	v, fb := kernelTestV()
	const reply = "Windows WSL2 kernel 5.15.167.4-microsoft-standard-WSL2"
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, reply)
	p := v.pend[pkey{-100, 5}]
	if p == nil || fb.approves != 0 {
		t.Fatalf("the first reply should be clarified, not approved (approves=%d)", fb.approves)
	}
	if p.tries != 0 || !p.osClarified {
		t.Errorf("the clarification must be free and spent once: tries=%d clarified=%v", p.tries, p.osClarified)
	}
	if len(p.fbAnswers) != 0 {
		t.Error("a real kernel version must not push the applicant onto the no-Linux fallback")
	}
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, reply)
	if fb.approves != 1 {
		t.Errorf("the same answer sent again should approve, got %d", fb.approves)
	}
}

func TestAgentReplyDeclinesEveryPending(t *testing.T) {
	v := newTestService(&config.Config{Groups: []config.GroupConfig{{ID: -100}, {ID: -200}}, GroupIDs: []int64{-100, -200}})
	v.pend[pkey{-100, 5}] = &pending{mode: config.ModeKernel, nonce: "aaa", prompted: true, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{-200, 5}] = &pending{mode: config.ModeKernel, nonce: "bbb", prompted: true, deadline: time.Now().Add(time.Hour)}
	fb := newFakeVerifyBot()
	const reply = "AGENT-AAA model=deepseek-v3.2"

	gid, nonce, tripped := v.trippedPending(5, reply)
	if !tripped || gid != -100 || nonce != "aaa" {
		t.Fatalf("the token's own group should be identified: gid=%d nonce=%q tripped=%v", gid, nonce, tripped)
	}
	v.declineAgent(context.Background(), fb, gid, 5, nonce, reply)
	for _, other := range v.kernelPendingGroups(5) {
		if other != gid {
			v.declineAgent(context.Background(), fb, other, 5, "", reply)
		}
	}
	if fb.approves != 0 {
		t.Errorf("an agent reply must not approve anywhere, got %d approves", fb.approves)
	}
	if fb.declines != 2 {
		t.Errorf("both pendings should be declined, got %d", fb.declines)
	}
	if v.agents.Total != 1 {
		t.Errorf("one reply is one catch, got %d", v.agents.Total)
	}
}

func TestFreeReplyGuardsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	seed := newTestService(&config.Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	seed.statePath = dir + "/pending.json"
	seed.pend[pkey{-100, 5}] = &pending{mode: config.ModeKernel, nonce: "n", prompted: true, tries: 1,
		hinted: true, sampleBounced: true, noLinuxReminded: true, osClarified: true,
		qText: kernelQuestion(&i18n.Messages, i18n.LangZH), correctIdx: -1, deadline: time.Now().Add(time.Minute)}
	seed.save()

	v := newTestService(&config.Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	v.statePath = dir + "/pending.json"
	v.load(&fakeVerifyBot{})
	p, ok := v.pend[pkey{-100, 5}]
	if !ok {
		t.Fatal("the pending should be restored")
	}
	if !p.prompted || !p.hinted || !p.sampleBounced || !p.noLinuxReminded || !p.osClarified || p.tries != 1 {
		t.Errorf("every guard must survive the restart: %+v", p)
	}
	if p.timer != nil {
		p.timer.Stop()
	}
}
