package i18n

type localizedStrings [langCount][]string

func (s localizedStrings) value(l Lang) []string {
	if l >= langCount {
		l = LangZH
	}
	return s[l]
}

// StringList is a localized list of plain values.
type StringList struct{ localizedStrings }

// For returns the list for l.
func (s StringList) For(l Lang) []string { return s.value(l) }

// Question is one localized answer-hidden verification question.
type Question struct {
	// Prompt contains the localized question text.
	Prompt Text
	// Answers contains the localized accepted answers.
	Answers StringList
}

// For returns the localized prompt and accepted answers.
func (q Question) For(l Lang) (string, []string) {
	return q.Prompt.For(l), q.Answers.For(l)
}

// VerificationCatalog contains the join-verification surface groups.
type VerificationCatalog struct {
	// Group contains the public join challenge.
	Group VerificationGroupCatalog
	// Challenge contains DM questions and answer guidance.
	Challenge VerificationChallengeCatalog
	// Result contains verification outcomes and callback alerts.
	Result VerificationResultCatalog
	// Channel contains required-channel guidance.
	Channel VerificationChannelCatalog
	// Recovery contains outage recovery notices.
	Recovery VerificationRecoveryCatalog
}

// VerificationGroupCatalog contains public group challenge text.
type VerificationGroupCatalog struct {
	// Body formats the public challenge body.
	Body Format
	// LinkText formats the optional deep-link clause.
	LinkText Format
	// ChannelHint formats the required-channel suffix.
	ChannelHint Format
	// VerifyButton labels the verification button.
	VerifyButton Text
}

// VerificationChallengeCatalog contains applicant questions and guidance.
type VerificationChallengeCatalog struct {
	// KernelQuestion is persisted with a kernel challenge.
	KernelQuestion Text
	// QuizPrompt formats a multiple-choice question.
	QuizPrompt Format
	// KernelPrompt formats the initial kernel question.
	KernelPrompt Format
	// KernelWrong formats a kernel retry.
	KernelWrong Format
	// SampleCopied rejects the printed kernel example.
	SampleCopied Text
	// NoLinuxRetry explains the no-Linux minute proof.
	NoLinuxRetry Text
	// OSMixed clarifies a mixed operating-system answer.
	OSMixed Text
	// FallbackIntro formats an answer-hidden fallback question.
	FallbackIntro Format
	// FallbackWrong formats a fallback retry.
	FallbackWrong Format
	// FallbackQuestions contains the built-in answer-hidden questions.
	FallbackQuestions [2]Question
}

// VerificationResultCatalog contains applicant outcomes and callback alerts.
type VerificationResultCatalog struct {
	// AICaught reports automated-agent interception.
	AICaught Text
	// Approved reports successful verification.
	Approved Text
	// WrongRetry formats the retry cooldown.
	WrongRetry Format
	// WrongNoWait reports rejection without a cooldown.
	WrongNoWait Text
	// WrongBanned reports a terminal verification ban.
	WrongBanned Text
	// AlreadyHandled reports a settled or expired request.
	AlreadyHandled Text
	// StaleQuestion reports an expired question.
	StaleQuestion Text
	// NotYours reports a request owned by another applicant.
	NotYours Text
}

// VerificationChannelCatalog contains required-channel guidance.
type VerificationChannelCatalog struct {
	// FallbackName names a channel without a configured display name.
	FallbackName Text
	// First formats the message shown after a correct answer.
	First Format
	// FollowPrompt formats the channel-follow prompt.
	FollowPrompt Format
	// FollowButton formats the channel-follow button.
	FollowButton Format
	// ContinueButton labels the follow-confirmation button.
	ContinueButton Text
	// ContinueOK acknowledges a successful channel check.
	ContinueOK Text
	// NotFollowedYet formats an unsuccessful channel check.
	NotFollowedYet Format
	// NoPending reports that no join request is waiting.
	NoPending Text
}

// VerificationRecoveryCatalog contains outage recovery notices.
type VerificationRecoveryCatalog struct {
	// Renotify formats a restarted verification window notice.
	Renotify Format
}
