package i18n

// ModerateCatalog contains moderation text.
type ModerateCatalog struct {
	// Common contains authorization, target, and settings notices.
	Common ModerateCommonCatalog
	// Warning contains warning-count and warning-limit notices.
	Warning ModerateWarningCatalog
	// Ban contains ban and message-purge notices.
	Ban ModerateBanCatalog
	// Mute contains mute and unmute notices.
	Mute ModerateMuteCatalog
	// BanTime contains ban-duration policy notices.
	BanTime ModerateBanTimeCatalog
	// Duration contains localized finite and permanent durations.
	Duration ModerateDurationCatalog
	// Antispam contains sender-channel filtering notices.
	Antispam ModerateAntispamCatalog
	// Setup contains startup permission and membership diagnostics.
	Setup ModerateSetupCatalog
}

// ModerateCommonCatalog contains shared moderation notices.
type ModerateCommonCatalog struct {
	// CommandAdminOnly formats a command-specific authorization failure.
	CommandAdminOnly Format
	// AdminOnly reports a generic authorization failure.
	AdminOnly Text
	// ReplyUsage formats reply-target command guidance.
	ReplyUsage Format
	// TargetAdminCheckFailed reports an unavailable target-admin check.
	TargetAdminCheckFailed Text
	// CallerAdminCheckFailed reports an unavailable admin check for the caller themselves.
	CallerAdminCheckFailed Text
	// TargetIsAdmin reports that an admin target was left unchanged.
	TargetIsAdmin Text
	// SettingsSaveFailed reports a moderation settings persistence failure.
	SettingsSaveFailed Text
}

// ModerateWarningCatalog contains warning-count and warning-limit notices.
type ModerateWarningCatalog struct {
	// LimitKickFailed reports a failed warning-limit kick.
	LimitKickFailed Text
	// LimitKickAlert formats an operator alert for a failed automatic kick.
	LimitKickAlert Format
	// KickRejoinable describes a successful rejoinable kick.
	KickRejoinable Text
	// KickUnbanFailed describes the permanent ban left by a failed unban.
	KickUnbanFailed Text
	// LimitReached formats the warning-limit outcome.
	LimitReached Format
	// KickAlert formats the operator record for a warning-limit kick.
	KickAlert Format
	// Issued formats a warning-count notice.
	Issued Format
	// Cleared formats a warning-counter reset notice.
	Cleared Format
}

// ModerateBanCatalog contains ban and purge notices.
type ModerateBanCatalog struct {
	// Failed reports a failed ban.
	Failed Text
	// FailureAlert formats a failed-ban operator alert.
	FailureAlert Format
	// Verb names a ban action.
	Verb Text
	// PurgeVerb names a ban that also purges all messages.
	PurgeVerb Text
	// Action formats a completed ban action and duration.
	Action Format
	// Applied formats a group-facing ban result.
	Applied Format
	// Alert formats an operator record for a completed ban.
	Alert Format
}

// ModerateMuteCatalog contains mute and unmute notices.
type ModerateMuteCatalog struct {
	// Usage formats finite mute-duration guidance.
	Usage Format
	// Failed reports a failed mute.
	Failed Text
	// Applied formats a completed finite mute.
	Applied Format
	// Alert formats an operator record for a mute.
	Alert Format
	// UnmuteFailed reports a failed unmute and the retained restriction.
	UnmuteFailed Text
	// Unmuted formats a completed unmute.
	Unmuted Format
}

// ModerateBanTimeCatalog contains ban-duration policy notices.
type ModerateBanTimeCatalog struct {
	// Usage explains accepted ban-duration values and effects.
	Usage Text
	// PermanentDescription describes the effect of a permanent ban.
	PermanentDescription Text
	// TemporaryDescription describes the effect of a finite ban.
	TemporaryDescription Text
	// Current formats the current ban-duration policy.
	Current Format
	// Set formats a saved ban-duration policy.
	Set Format
}

// ModerateDurationCatalog contains localized ban and mute durations.
type ModerateDurationCatalog struct {
	// PermanentInput is the accepted Simplified Chinese permanent-duration token.
	PermanentInput Text
	// Permanent labels a permanent duration.
	Permanent Text
	// Status formats a duration with its automatic-lift behavior.
	Status Format
	// PermanentEffect reports that a permanent action does not lift automatically.
	PermanentEffect Text
	// TemporaryEffect reports that a finite action lifts automatically.
	TemporaryEffect Text
	// Days formats a finite number of days.
	Days Format
	// Hours formats a finite number of hours.
	Hours Format
	// Minutes formats a finite number of minutes.
	Minutes Format
	// Seconds formats a finite number of seconds.
	Seconds Format
}

// ModerateAntispamCatalog contains sender-channel filtering notices.
type ModerateAntispamCatalog struct {
	// SenderBannedAlert formats a deleted-message and sender-ban operator alert.
	SenderBannedAlert Format
	// SenderBanFailedAlert formats a deleted-message and failed-ban operator alert.
	SenderBanFailedAlert Format
	// Enabled explains the enabled sender-channel filter requirement.
	Enabled Text
	// Disabled reports that sender-channel filtering is disabled.
	Disabled Text
	// InvalidChannelID explains accepted channel ID forms.
	InvalidChannelID Text
	// Removed formats a channel allowlist removal.
	Removed Format
	// AllowedUnbanFailed formats a partial allowlist success.
	AllowedUnbanFailed Format
	// Allowed formats an allowlisted and unbanned channel.
	Allowed Format
	// Usage explains sender-channel filtering commands.
	Usage Text
}

// ModerateSetupCatalog contains one actionable setup report per guarded group.
type ModerateSetupCatalog struct {
	// Ready formats a complete setup result.
	Ready Format
	// MissingHeader formats the heading for an incomplete setup.
	MissingHeader Format
	// GroupAccess asks the operator to restore group access.
	GroupAccess Text
	// GroupAdmin asks the operator to promote the bot.
	GroupAdmin Text
	// ApproveJoinRequests asks for the invite-users capability.
	ApproveJoinRequests Text
	// BanUsers asks for the restrict-members capability.
	BanUsers Text
	// DeleteMessages asks for the delete-messages capability.
	DeleteMessages Text
	// ChannelAdmin formats the required-channel administrator action.
	ChannelAdmin Format
	// Restart explains how to rerun the setup check.
	Restart Text
}
