package verify

import (
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// One DM reply is graded for every group the applicant is verifying for. When those groups drew
// different fallback questions, answering one of them correctly must not spend an attempt in the
// others — three such replies would otherwise exhaust the tries and strike an honest applicant.
func TestReplyToAnotherGroupsFallbackDoesNotCount(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100, -200}})
	uid := int64(5)
	v.pend[pkey{-100, uid}] = &pending{nonce: "a", mode: config.ModeKernel, lang: i18n.LangEN,
		qText: "Question A", fbAnswers: []string{"gentoo.org"}, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{-200, uid}] = &pending{nonce: "b", mode: config.ModeKernel, lang: i18n.LangEN,
		qText: "Question B", fbAnswers: []string{"gentoozh.org"}, deadline: time.Now().Add(time.Hour)}

	if !v.answersAnotherFallback(-100, uid, "gentoozh.org") {
		t.Error("the reply answers the other group's question, so this group must not charge a try")
	}
	if v.answersAnotherFallback(-100, uid, "gentoo.org") {
		t.Error("a reply that only answers this group's own question is not another group's")
	}
	if v.answersAnotherFallback(-100, uid, "something else") {
		t.Error("a genuinely wrong answer must still cost an attempt")
	}
}

// Groups drawing from the same bank reuse one question, so the common case never diverges.
func TestFallbackQuestionReusedAcrossGroupsWithTheSameBank(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100, -200}})
	uid := int64(6)
	v.pend[pkey{-200, uid}] = &pending{nonce: "b", mode: config.ModeKernel, lang: i18n.LangEN,
		qText: "Question B", fbAnswers: []string{"gentoozh.org"}, deadline: time.Now().Add(time.Hour)}

	text, answers := v.sharedFallbackQuestion(-100, uid, i18n.LangEN)
	if text != "Question B" || len(answers) != 1 || answers[0] != "gentoozh.org" {
		t.Errorf("question = %q answers = %v, want the one already drawn for this applicant", text, answers)
	}
}

// A group with its own configured bank never inherits another group's question.
func TestConfiguredBankIsNotSharedAcrossGroups(t *testing.T) {
	cfg := &config.Config{
		Groups: []config.GroupConfig{{ID: -100}, {ID: -200}},
		FallbackQuestions: []config.ShortQuestion{
			{Q: "Configured question", Answers: []string{"configured"}},
		},
	}
	v := newTestService(cfg)
	uid := int64(7)
	v.pend[pkey{-200, uid}] = &pending{nonce: "b", mode: config.ModeKernel, lang: i18n.LangEN,
		qText: "Some other question", fbAnswers: []string{"other"}, deadline: time.Now().Add(time.Hour)}

	// Both groups share one configured bank here, so reuse is correct; the guard is that reuse
	// only happens when the banks match, which sameFallbackSource decides.
	if !sameFallbackSource(v.fallbackSource(-100), v.fallbackSource(-200)) {
		t.Fatal("two groups reading the same configured bank must compare equal")
	}
	differing := []config.ShortQuestion{{Q: "Different", Answers: []string{"x"}}}
	if sameFallbackSource(v.fallbackSource(-100), differing) {
		t.Error("a group with a different bank must not have its question reused")
	}
}
