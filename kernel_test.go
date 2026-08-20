package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

// TestKernelAnswerOK is the anti-bot gate itself: a plausible kernel version — however the user
// writes it (bare, `uname -r` output, inside a sentence, past or future release) — passes, while a
// random number, another product's version, or a chat message does not.
func TestKernelAnswerOK(t *testing.T) {
	good := []string{
		"6.12.3",
		"7.2",                        // the current mainline at the time of writing
		"6.18.45",                    // longterm
		"6.18.44-gentoo-r1-cjk-zakk", // a real `uname -r`
		"3.10.0-1160.el7.x86_64",     // an ancient enterprise kernel
		"2.6.32",                     // older still
		"0.12",                       // 1991
		"我的是 6.12.3-gentoo",
		"内核版本6.6.152",
		"v6.9",
		"uname -r: 5.15.216",
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
	}
	for _, s := range good {
		if !kernelAnswerOK(s) {
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
		"windows 11",
		"我用的是 Windows",
		"aarch64", // `uname -m`, not `uname -r` — an architecture is not a version
		"arm64",
	}
	for _, s := range bad {
		if kernelAnswerOK(s) {
			t.Errorf("kernelAnswerOK(%q) = true, want false", s)
		}
	}
}

// TestVerifyModeResolution covers the mode chain: the built-in default is kernel, config and
// per-group settings override it, and /vmode overrides everything.
func TestVerifyModeResolution(t *testing.T) {
	cfg := &Config{Groups: []GroupConfig{{ID: -100}, {ID: -200, VerifyMode: modeQuiz}}, GroupIDs: []int64{-100, -200},
		Questions: []Question{{Q: "q", Options: []string{"a", "b"}, Answer: 0}}}
	v := NewVerifier(cfg)
	if got := v.effectiveMode(-100); got != modeKernel {
		t.Errorf("default mode = %q, want %q", got, modeKernel)
	}
	if got := v.effectiveMode(-200); got != modeQuiz {
		t.Errorf("per-group override = %q, want %q", got, modeQuiz)
	}
	cfg.VerifyMode = modeQuiz
	if got := v.effectiveMode(-100); got != modeQuiz {
		t.Errorf("global verify_mode = %q, want %q", got, modeQuiz)
	}
	v.setVerifyMode(modeKernel) // /vmode wins over both
	if got := v.effectiveMode(-200); got != modeKernel {
		t.Errorf("/vmode override = %q, want %q", got, modeKernel)
	}
	v.setVerifyMode("") // …and clearing it goes back to the config
	if got := v.effectiveMode(-200); got != modeQuiz {
		t.Errorf("after clearing the override = %q, want %q", got, modeQuiz)
	}
}

// TestPickModeQuizWithoutQuestions: a quiz-mode group with an empty pool must fall back to kernel
// instead of posting a challenge with no options (possible after /vmode quiz on a kernel-only config).
func TestPickModeQuizWithoutQuestions(t *testing.T) {
	v := NewVerifier(&Config{Groups: []GroupConfig{{ID: -100}}, GroupIDs: []int64{-100}, VerifyMode: modeQuiz})
	if got := v.pickMode(-100); got != modeKernel {
		t.Errorf("quiz mode with no questions should fall back to %q, got %q", modeKernel, got)
	}
	mode, text, opts, idx := v.newChallenge(-100, langZH)
	if mode != modeKernel || text != kernelQuestion(langZH) || opts != nil || idx != -1 {
		t.Errorf("kernel challenge = (%q, %q, %v, %d), want the kernel question with no options and idx -1", mode, text, opts, idx)
	}
}

// TestPickModeMixed: mixed must actually produce BOTH challenges over many applicants — a coin flip
// stuck on one side would silently disable half the feature.
func TestPickModeMixed(t *testing.T) {
	v := NewVerifier(&Config{Groups: []GroupConfig{{ID: -100}}, GroupIDs: []int64{-100}, VerifyMode: modeMixed,
		Questions: []Question{{Q: "q", Options: []string{"a", "b"}, Answer: 0}}})
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		seen[v.pickMode(-100)] = true
	}
	if !seen[modeKernel] || !seen[modeQuiz] {
		t.Errorf("mixed should yield both modes, got %v", seen)
	}
}

// TestKernelAnswerDMPredicate: only a plain DM from someone with a live KERNEL pending is routed to
// the answer handler — commands still reach their own handlers, and a quiz applicant's DM falls
// through to the normal auto-reply.
func TestKernelAnswerDMPredicate(t *testing.T) {
	v := NewVerifier(&Config{})
	dm := func(uid int64, text string) telego.Update {
		return telego.Update{Message: &telego.Message{Chat: telego.Chat{Type: "private", ID: uid},
			From: &telego.User{ID: uid}, Text: text}}
	}
	if v.kernelAnswerDM(context.TODO(), dm(5, "6.12.3")) {
		t.Error("no pending: must not capture the message")
	}
	v.pend[pkey{-100, 5}] = &pending{mode: modeKernel, nonce: "n", prompted: true, deadline: time.Now().Add(time.Hour)}
	if !v.kernelAnswerDM(context.TODO(), dm(5, "6.12.3")) {
		t.Error("a plain DM during a kernel verification must be treated as the answer")
	}
	if v.kernelAnswerDM(context.TODO(), dm(5, "/start")) {
		t.Error("a command must not be swallowed as an answer")
	}
	if v.kernelAnswerDM(context.TODO(), dm(5, "   ")) {
		t.Error("an empty message must not count as an answer")
	}
	if v.kernelAnswerDM(context.TODO(), dm(6, "6.12.3")) {
		t.Error("another user's DM must not match")
	}
	v.pend[pkey{-100, 7}] = &pending{mode: modeQuiz, nonce: "n", prompted: true, deadline: time.Now().Add(time.Hour)}
	if v.kernelAnswerDM(context.TODO(), dm(7, "6.12.3")) {
		t.Error("a quiz applicant's DM must fall through to the auto-reply")
	}
}

// kernelTestV builds a Verifier with one kernel pending for user 5 in group -100.
func kernelTestV() (*Verifier, *fakeModBot) {
	v := NewVerifier(&Config{Groups: []GroupConfig{{ID: -100}}, GroupIDs: []int64{-100}, VerifyMaxFails: 3})
	v.pend[pkey{-100, 5}] = &pending{mode: modeKernel, nonce: "n", prompted: true, groupMsgID: 42, deadline: time.Now().Add(time.Hour)}
	return v, newFakeMod()
}

// TestGradeKernelAnswerCorrect: a valid version approves the join request.
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

// TestGradeKernelAnswerRetries: a wrong answer costs one try and keeps the applicant in place; only
// the last of kernelMaxTries declines — a typo must not be an instant rejection.
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

// TestGradeKernelAnswerChannelGate: a correct answer from someone who hasn't joined the required
// channel is NOT approved — the channel gate applies to kernel mode exactly as it does to the quiz.
func TestGradeKernelAnswerChannelGate(t *testing.T) {
	v, fb := kernelTestV()
	v.cfg.RequiredChannelID = -400
	v.botID = 1
	fb.member = &telego.ChatMemberLeft{Status: telego.MemberStatusLeft}
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "6.12.3")
	if fb.approves != 0 {
		t.Errorf("must not approve before the channel is joined, got %d approves", fb.approves)
	}
	if _, ok := v.pend[pkey{-100, 5}]; !ok {
		t.Error("the pending must stay live so the user can follow the channel and answer again")
	}
	fb.member = &telego.ChatMemberMember{Status: telego.MemberStatusMember}
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "6.12.3")
	if fb.approves != 1 {
		t.Errorf("after joining the channel the same answer should approve, got %d", fb.approves)
	}
}

// TestKernelPendingSurvivesRestart: a kernel challenge round-trips through the state file with its
// mode and used-up tries intact — it must NOT be dropped by the quiz payload check (it carries no
// options), and a record written before kernel mode existed must still restore as a quiz.
func TestKernelPendingSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	seed := NewVerifier(&Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	seed.statePath = dir + "/pending.json"
	seed.pend[pkey{-100, 7}] = &pending{mode: modeKernel, tries: 1, nonce: "x", name: "Carol",
		qText: kernelQuestion(langZH), correctIdx: -1, deadline: time.Now().Add(time.Minute), groupMsgID: 5}
	seed.pend[pkey{-100, 8}] = &pending{mode: modeQuiz, nonce: "y", correctIdx: 0,
		qOpts: []string{"a", "b"}, deadline: time.Now().Add(time.Minute)}
	seed.save()

	v := NewVerifier(&Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	v.statePath = dir + "/pending.json"
	v.load(&fakeVerifyBot{})
	p, ok := v.pend[pkey{-100, 7}]
	if !ok {
		t.Fatal("a kernel pending must survive the restart (it has no options to validate)")
	}
	if p.mode != modeKernel || p.tries != 1 {
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
	vl := NewVerifier(&Config{TimeoutSeconds: 240, GroupIDs: []int64{-100}})
	vl.statePath = dir + "/legacy.json"
	vl.load(&fakeVerifyBot{})
	lp, ok := vl.pend[pkey{-100, 9}]
	if !ok {
		t.Fatal("a legacy pending must still restore")
	}
	if lp.mode != modeQuiz {
		t.Errorf("a record with no mode must restore as %q, got %q", modeQuiz, lp.mode)
	}
	if lp.timer != nil {
		lp.timer.Stop()
	}
}

// TestLangFor maps Telegram's interface-language tag onto the three locales the verification path
// speaks: Traditional for the tw/hk/hant tags, Simplified for the rest of zh, English otherwise.
func TestLangFor(t *testing.T) {
	cases := map[string]lang{
		"zh-hans": langZH, "zh-CN": langZH, "zh": langZH, "zh-sg": langZH,
		"zh-hant": langZHT, "zh-TW": langZHT, "zh-hk": langZHT, "zh-MO": langZHT, "yue": langZHT,
		"en": langEN, "en-US": langEN, "ru": langEN, "ja": langEN, "": langEN,
	}
	for code, want := range cases {
		if got := langFor(code); got != want {
			t.Errorf("langFor(%q) = %q, want %q", code, got, want)
		}
	}
	// every catalog must be complete: a missing string would send an empty message to a joiner
	for _, l := range []lang{langZH, langZHT, langEN} {
		c := tr(l)
		for name, val := range map[string]string{
			"KernelQuestion": c.KernelQuestion, "GroupBody": c.GroupBody, "VerifyButton": c.VerifyButton, "QuizPrompt": c.QuizPrompt,
			"KernelPrompt": c.KernelPrompt, "KernelWrong": c.KernelWrong, "SampleCopied": c.SampleCopied,
			"FallbackIntro": c.FallbackIntro, "FallbackWrong": c.FallbackWrong,
			"AIWarning": c.AIWarning, "AICaught": c.AICaught, "Approved": c.Approved,
			"WrongRetry": c.WrongRetry, "WrongNoWait": c.WrongNoWait, "WrongBanned": c.WrongBanned,
			"AlreadyHandled": c.AlreadyHandled, "StaleQuestion": c.StaleQuestion, "NotYours": c.NotYours,
			"ChannelFirst": c.ChannelFirst, "FollowPrompt": c.FollowPrompt, "FollowButton": c.FollowButton,
			"ContinueButton": c.ContinueButton, "ContinueOK": c.ContinueOK, "NotFollowedYet": c.NotFollowedYet,
			"NoPending": c.NoPending, "Renotify": c.Renotify, "GroupLinkText": c.GroupLinkText,
			"GroupChannelHint": c.GroupChannelHint,
		} {
			if val == "" {
				t.Errorf("catalog %q is missing %s", l, name)
			}
		}
		if len(c.FallbackQuestions) == 0 {
			t.Errorf("catalog %q has no fallback questions", l)
		}
		for i, q := range c.FallbackQuestions {
			if q.Q == "" || len(q.Answers) == 0 {
				t.Errorf("catalog %q fallback question %d is incomplete", l, i)
			}
			// the answer must NOT appear in the question: printing it would hand out a free pass
			if fallbackAnswerOK(q.Q, q.Answers) {
				t.Errorf("catalog %q fallback question %d contains its own answer: %q", l, i, q.Q)
			}
		}
	}
}

// TestAITrap: an answer echoing the per-applicant canary token is an automated agent answering for
// someone, so it is declined at once — even when it also contains a valid kernel version.
func TestAITrap(t *testing.T) {
	if !aiTrapped("AGENT-"+strings.ToUpper("abc123"), "abc123") {
		t.Error("the exact token must trigger the tripwire")
	}
	if !aiTrapped("sure: agent-abc123", "abc123") {
		t.Error("the token must match case-insensitively")
	}
	if aiTrapped("6.12.3-gentoo", "abc123") {
		t.Error("a normal answer must not trigger the tripwire")
	}
	if aiTrapped("AGENT-DEADBEEF", "abc123") {
		t.Error("another applicant's token must not trigger this one")
	}

	v, fb := kernelTestV()
	v.pend[pkey{-100, 5}].nonce = "abc123"
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "AGENT-ABC123 6.12.3")
	if fb.approves != 0 || fb.declines != 1 {
		t.Errorf("a tripped agent must be declined, not approved: approves=%d declines=%d", fb.approves, fb.declines)
	}
}

// TestNoLinuxFallback: "I haven't installed Linux yet" costs no attempt and gets the kernel.org
// fallback question — once. A second such message is graded as a wrong answer, so the escape can't
// be used to stall the verification.
func TestNoLinuxFallback(t *testing.T) {
	// Covers how people actually phrase it in both scripts — a missed phrasing costs a real newcomer
	// an attempt instead of switching them to the short-answer question.
	for _, s := range []string{"还没装", "我還沒裝 Linux", "not installed yet", "I use Windows", "不知道",
		"我不用 Linux", "我没用过 Linux", "I don't use Linux", "我用的 macOS",
		"我沒有安裝", "我沒有安裝 Linux", "我没有安装 Linux", "我还没有装", "還沒安裝", "沒用過 Linux",
		"我不懂", "我电脑上没有 Linux", "no idea", "I never used Linux", "what?"} {
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
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "还没装 Linux")
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
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "还没装")
	if v.pend[pkey{-100, 5}].tries != 1 {
		t.Error("a second 'not installed' reply must be graded as a wrong answer")
	}
	// answering the short question passes; so does a real kernel version, in case they went to install
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, p.fbAnswers[0])
	if fb.approves != 1 {
		t.Errorf("the short answer should approve, got %d approves", fb.approves)
	}

	v2, fb2 := kernelTestV()
	v2.gradeKernelAnswer(context.Background(), fb2, -100, 5, "我没装 Linux")
	v2.gradeKernelAnswer(context.Background(), fb2, -100, 5, "6.18.44-gentoo-r1") // not the printed example
	if fb2.approves != 1 {
		t.Errorf("a kernel version must still pass after the fallback, got %d approves", fb2.approves)
	}
}

// TestFallbackAnswerMatching: answers match as whole words (so "ls" is not found inside "false")
// and tolerate a sentence around them.
func TestFallbackAnswerMatching(t *testing.T) {
	if !fallbackAnswerOK("用 emerge 装", []string{"emerge"}) {
		t.Error("an answer inside a sentence should count")
	}
	if !fallbackAnswerOK("LS", []string{"ls"}) {
		t.Error("matching must be case-insensitive")
	}
	if fallbackAnswerOK("that is false", []string{"ls"}) {
		t.Error("a short answer must not match inside another word")
	}
	if fallbackAnswerOK("emergency", []string{"emerge"}) {
		t.Error("emerge must not match inside emergency")
	}
	if fallbackAnswerOK("不知道", []string{"emerge", "portage"}) {
		t.Error("a non-answer must not pass")
	}
}

// TestCopiedSampleBounced: sending back the format example printed in the prompt is bounced once
// with a nudge (a copy-paste bot's laziest move), but a person who really runs that version gets in
// by sending it again.
func TestCopiedSampleBounced(t *testing.T) {
	v, fb := kernelTestV()
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "6.12.3-gentoo")
	if fb.approves != 0 {
		t.Fatal("the printed example must not be accepted on first sight")
	}
	if p := v.pend[pkey{-100, 5}]; p == nil || p.tries != 0 || !p.sampleBounced {
		t.Fatalf("the nudge should cost no attempt and be marked spent: %+v", p)
	}
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "6.12.3-gentoo")
	if fb.approves != 1 {
		t.Errorf("sending it again means they really run it — should approve, got %d", fb.approves)
	}
	// a version that is NOT the printed example is accepted immediately
	v2, fb2 := kernelTestV()
	v2.gradeKernelAnswer(context.Background(), fb2, -100, 5, "6.18.44-gentoo-r1-cjk-zakk")
	if fb2.approves != 1 {
		t.Errorf("a real version should approve at once, got %d", fb2.approves)
	}
}

// TestKernelPromptLocalised: each locale's prompt carries that locale's wording plus the applicant's
// own tripwire token — a shared token would let a spam operator filter one fixed string.
func TestKernelPromptLocalised(t *testing.T) {
	zh := kernelPromptHTML(langZH, kernelQuestion(langZH), 3, "abc123", true)
	if !strings.Contains(zh, "还有 3 次机会") || !strings.Contains(zh, "AGENT-ABC123") {
		t.Errorf("zh prompt missing its wording or token: %s", zh)
	}
	if !strings.Contains(kernelPromptHTML(langZHT, kernelQuestion(langZHT), 3, "n", true), "還有 3 次機會") {
		t.Error("zh-hant prompt should use Traditional wording")
	}
	en := kernelPromptHTML(langEN, kernelQuestion(langEN), 2, "n", true)
	if !strings.Contains(en, "2 attempts left") || !strings.Contains(en, "uname -r") {
		t.Errorf("en prompt missing its wording: %s", en)
	}
	// The collapsed quote is Bot API 7.4; the fallback rendering drops it but must keep every word,
	// so an old self-hosted API server that rejects the entity still gets a complete question.
	plain := kernelPromptHTML(langZH, kernelQuestion(langZH), 3, "abc123", false)
	if strings.Contains(plain, "<blockquote") {
		t.Error("the fallback rendering must not use the blockquote entity")
	}
	if !strings.Contains(plain, "AGENT-ABC123") || !strings.Contains(plain, "还有 3 次机会") {
		t.Errorf("the fallback rendering lost content: %s", plain)
	}
	if got := stripHTML("a <b>b</b> &lt;c&gt; <blockquote expandable>d</blockquote> &amp;"); got != "a b <c> d &" {
		t.Errorf("stripHTML = %q", got)
	}
}

// TestSendVerifyDMDegrades: if Telegram rejects the markup — an old self-hosted Bot API server that
// doesn't know the collapsed blockquote — the applicant must still receive a readable question
// instead of silence followed by a timeout decline.
func TestSendVerifyDMDegrades(t *testing.T) {
	v := NewVerifier(&Config{})
	rich := kernelPromptHTML(langZH, kernelQuestion(langZH), 3, "abc123", true)
	plain := kernelPromptHTML(langZH, kernelQuestion(langZH), 3, "abc123", false)

	// first send rejected -> the simpler HTML goes out and is accepted
	fb := &fakeVerifyBot{sendErr: errors.New("Bad Request: can't parse entities"), sendFailN: 1}
	v.sendVerifyDM(context.Background(), fb, 5, rich, plain)
	if fb.sends != 2 {
		t.Fatalf("expected a retry after the markup rejection, got %d send(s)", fb.sends)
	}
	if strings.Contains(fb.lastSendText, "<blockquote") || !strings.Contains(fb.lastSendText, "AGENT-ABC123") {
		t.Errorf("the retry should drop the blockquote but keep the question: %q", fb.lastSendText)
	}

	// both HTML attempts rejected -> plain text, tags stripped, still carrying the question
	fb2 := &fakeVerifyBot{sendErr: errors.New("Bad Request: can't parse entities"), sendFailN: 2}
	v.sendVerifyDM(context.Background(), fb2, 5, rich, plain)
	if fb2.sends != 3 {
		t.Fatalf("expected a plain-text last resort, got %d send(s)", fb2.sends)
	}
	if fb2.lastParseMode != "" {
		t.Errorf("the last resort must not use a parse mode, got %q", fb2.lastParseMode)
	}
	if strings.Contains(fb2.lastSendText, "<code>") || !strings.Contains(fb2.lastSendText, "uname -r") {
		t.Errorf("plain-text fallback should be tag-free but complete: %q", fb2.lastSendText)
	}

	// the happy path stays a single send
	fb3 := &fakeVerifyBot{}
	v.sendVerifyDM(context.Background(), fb3, 5, rich, plain)
	if fb3.sends != 1 {
		t.Errorf("a successful send must not retry, got %d", fb3.sends)
	}
}

// TestFallbackWebsiteAnswers: the no-Linux fallback asks for a website, so the accepted forms must
// cover how people actually type a domain — and must NOT accept gentoo-zh.org, which is a DIFFERENT
// site, not this community's.
func TestFallbackWebsiteAnswers(t *testing.T) {
	zh := tr(langZH).FallbackQuestions[0].Answers // gentoozh.org
	for _, s := range []string{"gentoozh.org", "https://gentoozh.org", "www.gentoozh.org", "https://gentoozh.org/", "是 gentoozh.org", "GentooZH.org"} {
		if !fallbackAnswerOK(s, zh) {
			t.Errorf("%q should be accepted for the community site", s)
		}
	}
	for _, s := range []string{"gentoo-zh.org", "gentoo.org", "google.com", "不知道"} {
		if fallbackAnswerOK(s, zh) {
			t.Errorf("%q must NOT be accepted for the community site", s)
		}
	}
	official := tr(langZH).FallbackQuestions[1].Answers // gentoo.org
	for _, s := range []string{"gentoo.org", "https://www.gentoo.org/", "官网是 gentoo.org"} {
		if !fallbackAnswerOK(s, official) {
			t.Errorf("%q should be accepted for the official site", s)
		}
	}
	for _, s := range []string{"gentoo", "gentoozh.org", "gentoo.com"} {
		if fallbackAnswerOK(s, official) {
			t.Errorf("%q must NOT be accepted for the official site", s)
		}
	}
}

// TestCopiedSampleGuardCoversFallback: the kernel prompt stays on screen after the applicant is
// moved to the short-answer question, so pasting the printed example back must be bounced there too
// — otherwise the "a real kernel version is still accepted" branch would be a free way around it.
func TestCopiedSampleGuardCoversFallback(t *testing.T) {
	v, fb := kernelTestV()
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "我不用 Linux") // -> short-answer question
	if len(v.pend[pkey{-100, 5}].fbAnswers) == 0 {
		t.Fatal("the applicant should have been moved to the fallback question")
	}
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "6.12.3-gentoo") // the printed example
	if fb.approves != 0 {
		t.Error("pasting the printed example must not approve from the fallback path either")
	}
	if !v.pend[pkey{-100, 5}].sampleBounced {
		t.Error("the nudge should have been spent")
	}
}

// TestUnpromptedDMIsNotAnAnswer: a DM sent before the question was ever shown must not be graded.
// With a required channel the applicant first gets only the follow-prompt, so typing "已关注"
// instead of tapping the button used to burn an attempt — and three of those declined a legitimate
// user who had not yet seen a question.
func TestUnpromptedDMIsNotAnAnswer(t *testing.T) {
	v := NewVerifier(&Config{})
	dm := telego.Update{Message: &telego.Message{Chat: telego.Chat{Type: "private", ID: 5},
		From: &telego.User{ID: 5}, Text: "已关注"}}
	v.pend[pkey{-100, 5}] = &pending{mode: modeKernel, nonce: "n", deadline: time.Now().Add(time.Hour)}
	if v.kernelAnswerDM(context.TODO(), dm) {
		t.Error("a DM must not be graded before the question has been sent")
	}
	v.markPrompted(-100, 5)
	if !v.kernelAnswerDM(context.TODO(), dm) {
		t.Error("once the question has been sent, a DM is the answer")
	}
}

// TestOtherOSNotAcceptedAsKernel: another system's version number is not a Linux kernel version.
// "Windows 10.0.19045" parses as 10.0.x, which the plausibility range accepts, so the reply must be
// caught by the OS name first and routed to the short-answer fallback instead of approving.
func TestOtherOSNotAcceptedAsKernel(t *testing.T) {
	if kernelAnswerOK("10.0.19045") {
		t.Error("a five-digit patch level is a Windows build number, not a kernel")
	}
	v, fb := kernelTestV()
	v.gradeKernelAnswer(context.Background(), fb, -100, 5, "我用的是 Windows 10.0.19045")
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

// TestTripwireCountsOncePerMessage: the canary token is derived from ONE pending's nonce, so an
// applicant verifying in several groups at once still records a single catch — the tally must not
// multiply by the number of pendings.
func TestTripwireCountsOncePerMessage(t *testing.T) {
	v := NewVerifier(&Config{Groups: []GroupConfig{{ID: -100}, {ID: -200}}, GroupIDs: []int64{-100, -200}})
	v.pend[pkey{-100, 5}] = &pending{mode: modeKernel, nonce: "aaa", prompted: true, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{-200, 5}] = &pending{mode: modeKernel, nonce: "bbb", prompted: true, deadline: time.Now().Add(time.Hour)}
	fb := newFakeMod()
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

// TestWrongAnswerUsesCurrentNonce: the decline must claim the pending that was actually charged. If
// the pending is replaced between the charge and the decline, using the stale nonce would no-op —
// telling the applicant they were rejected while their new request quietly stayed live.
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
