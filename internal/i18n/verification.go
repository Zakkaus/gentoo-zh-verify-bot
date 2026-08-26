package i18n

import "github.com/Zakkaus/gentoo-zh-verify-bot/internal/edition"

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
// VerificationHeldCatalog words verification outcomes for someone verified after joining, where
// passing lifts a restriction and failing removes them rather than settling a join request.
type VerificationHeldCatalog struct {
	Passed          Text
	CooldownActive  Format
	AlreadyHandled  Text
	SettlePending   Text
	DeferralExpired Text
	Undelivered     Text
	WrongNoWait     Text
	WrongRetry      Format
	WrongBanned     Format
	TimeoutNoWait   Text
	TimeoutRetry    Format
	TimeoutBanned   Format
	AICaught        Format
	AICaughtNoWait  Text
}

type VerificationCatalog struct {
	// Group contains the public join challenge.
	Group VerificationGroupCatalog
	// Challenge contains DM questions and answer guidance.
	Challenge VerificationChallengeCatalog
	// Result contains verification outcomes and callback alerts.
	Result VerificationResultCatalog
	// Held contains the same outcomes worded for a member who is already inside the group.
	Held VerificationHeldCatalog
	// Channel contains required-channel guidance.
	Channel VerificationChannelCatalog
	// Recovery contains outage recovery notices.
	Recovery VerificationRecoveryCatalog
	// Mode contains operator-facing challenge mode labels.
	Mode VerificationModeCatalog
	// Input contains localized phrases recognized in applicant replies.
	Input VerificationInputCatalog
	// Duration contains verification ban duration text.
	Duration VerificationDurationCatalog
	// Admin contains administrator controls and operational notices.
	Admin VerificationAdminCatalog
}

// VerificationGroupCatalog contains public group challenge text.
type VerificationGroupCatalog struct {
	// Body formats the public challenge body.
	Body Format
	// BodyJoined formats the public challenge for someone who is already a member.
	BodyJoined Format
	// BodyInvited formats the public challenge for a member somebody else added.
	BodyInvited Format
	// LinkText formats the optional deep-link clause.
	LinkText Format
	// ChannelHint formats the required-channel suffix.
	ChannelHint Format
	// VerifyButton labels the verification button.
	VerifyButton Text
}

// VerificationChallengeCatalog contains applicant questions and guidance.
type VerificationChallengeCatalog struct {
	// KernelPromptHeld, KernelWrongHeld and FallbackWrongHeld say what failing costs somebody who
	// is already inside the group: removal, not a declined join request.
	KernelPromptHeld  Format
	KernelWrongHeld   Format
	FallbackWrongHeld Format

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
	// FallbackQuestions contains the built-in answer-hidden questions for the Gentoo build.
	FallbackQuestions [2]Question
	// FallbackQuestionsGeneric contains the built-in questions for a build serving Linux
	// communities in general, which cannot ask about a Gentoo-zh Community site.
	FallbackQuestionsGeneric [2]Question
	// AgentTrap formats the hidden automated-agent instruction.
	AgentTrap Format
}

// VerificationResultCatalog contains applicant outcomes and callback alerts.
type VerificationResultCatalog struct {
	// AICaught formats automated-agent interception and its retry delay.
	AICaught Format
	// AICaughtNoWait reports interception when the applicant may retry immediately.
	AICaughtNoWait Text
	// Approved reports successful verification.
	Approved Text
	// CooldownActive formats an early reapplication refusal and its remaining cooldown.
	CooldownActive Format
	// DeclinePending reports that Telegram may still hold the join request.
	DeclinePending Text
	// DeferralExpired reports a strike-free decline after a prolonged Telegram outage.
	DeferralExpired Text
	// Undelivered reports a decline for a challenge that never reached the applicant.
	Undelivered Text
	// TimeoutRetry formats a timeout rejection and its retry cooldown.
	TimeoutRetry Format
	// TimeoutNoWait reports a timeout rejection without a cooldown.
	TimeoutNoWait Text
	// TimeoutBanned formats a timeout rejection and its automatic ban duration.
	TimeoutBanned Format
	// WrongRetry formats the retry cooldown.
	WrongRetry Format
	// WrongNoWait reports rejection without a cooldown.
	WrongNoWait Text
	// WrongBanned formats a terminal verification ban and its duration.
	WrongBanned Format
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
	// Unreadable reports that the channel gate itself could not be read.
	Unreadable Text
	// NotFollowedYet formats an unsuccessful channel check.
	NotFollowedYet Format
	// NoPending reports that no join request is waiting.
	NoPending Text
}

// VerificationRecoveryCatalog contains outage recovery notices.
type VerificationRecoveryCatalog struct {
	// Renotify formats a restarted verification window notice.
	Renotify Format
	// OutageSeconds formats an outage shorter than one minute.
	OutageSeconds Format
	// OutageMinutes formats an outage shorter than one hour.
	OutageMinutes Format
	// OutageHours formats an outage of at least one hour.
	OutageHours Format
}

// VerificationModeCatalog contains operator-facing challenge mode labels.
type VerificationModeCatalog struct {
	// Kernel labels manual Linux kernel version verification.
	Kernel Text
	// Quiz labels multiple-choice verification.
	Quiz Text
	// Mixed labels random kernel and multiple-choice verification.
	Mixed Text
}

// VerificationInputCatalog contains phrases recognized in applicant replies.
type VerificationInputCatalog struct {
	// OtherOSPhrases identifies replies that mention another operating system.
	OtherOSPhrases StringList
	// NoLinuxPhrases identifies replies from applicants without Linux.
	NoLinuxPhrases StringList
}

// VerificationDurationCatalog contains verification ban duration text.
type VerificationDurationCatalog struct {
	// Permanent labels a ban without an expiry.
	Permanent Text
	// Days formats a whole-day duration.
	Days Format
	// Hours formats a whole-hour duration.
	Hours Format
	// Minutes formats a whole-minute duration.
	Minutes Format
	// Seconds formats a duration in seconds.
	Seconds Format
}

// VerificationAdminCatalog contains administrator controls and operational notices.
type VerificationAdminCatalog struct {
	// AgentCaught formats an automated-answer interception alert.
	AgentCaught Format
	// AgentStats formats the lifetime automated-answer tally.
	AgentStats Format
	// TrustedBypassFailed formats a failed trusted-member approval.
	TrustedBypassFailed Format
	// SettingsDegraded reports runtime settings that could not be loaded cleanly.
	SettingsDegraded Format
	// OutageBacklog asks administrators to inspect requests older than Telegram's retention window.
	OutageBacklog Format
	// PendingCap formats a pending-queue capacity alert.
	PendingCap Format
	// OnlyGroupAdmin rejects an unauthorized callback.
	OnlyGroupAdmin Text
	// CannotApprove reports a settled or unapprovable request.
	CannotApprove Text
	// AlreadyHandled reports a settled request.
	AlreadyHandled Text
	// Approving acknowledges that approval is in progress.
	Approving Text
	// Banning formats a decline and ban in progress.
	Banning Format
	// ActionFailed tells the acting group that an acknowledged settlement failed.
	ActionFailed Text
	// ChannelFailOpen describes the configured fail-open action.
	ChannelFailOpen Text
	// ChannelFailClosed describes the configured fail-closed action.
	ChannelFailClosed Text
	// ChannelAccessFailed formats an unreadable required-channel alert.
	ChannelAccessFailed Format
	// ApproveFailed formats a failed approval alert.
	ApproveFailed Format
	// DeclineFailed formats a failed decline alert.
	DeclineFailed Format
	// AutoBanFailed formats a failed automatic ban alert.
	AutoBanFailed Format
	// AutoBanned formats a successful automatic ban alert.
	AutoBanned Format
	// BanFailed formats a failed administrator-requested ban alert.
	BanFailed Format
	// ApproveButton labels the direct-approval button.
	ApproveButton Text
	// The *Held variants say the same thing about a member who is already inside the group,
	// where admitting means lifting a restriction and refusing means removing them.
	ApprovingHeld           Text
	BanningHeld             Format
	ApproveFailedHeld       Format
	BanFailedHeld           Format
	DeclineFailedHeld       Format
	ActionFailedHeld        Text
	CannotApproveHeld       Text
	AlreadyHandledHeld      Text
	ChallengePostFailedHeld Format
	PendingCapHeld          Format
	// ReleaseButton labels the direct-approval button for a held member.
	ReleaseButton Text
	// RemoveButton labels the removal button for a held member.
	RemoveButton Text
	// BanButton labels the decline-and-ban button.
	BanButton Text
	// ChallengePostFailed formats a failed public challenge alert.
	ChallengePostFailed Format
}

// BuiltinFallback returns the built-in questions this build should ask. A group that is not
// the Gentoo-zh Community cannot answer a question about its website, so the generic build
// asks about Linux itself instead.
func (c VerificationChallengeCatalog) BuiltinFallback() []Question {
	if edition.IsGentoo {
		return c.FallbackQuestions[:]
	}
	return c.FallbackQuestionsGeneric[:]
}
