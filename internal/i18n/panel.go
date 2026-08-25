package i18n

// PanelCatalog contains control-panel text.
type PanelCatalog struct {
	// State contains verification-state labels.
	State PanelStateCatalog
	// Status contains status and statistics messages.
	Status PanelStatusCatalog
	// Verification contains verification-setting confirmations.
	Verification PanelVerificationCatalog
	// RichText contains rich-text setting confirmations.
	RichText PanelRichTextCatalog
	// NameSpoiler contains applicant-name hiding confirmations.
	NameSpoiler PanelNameSpoilerCatalog
	// VerificationMode contains verification-mode settings and guidance.
	VerificationMode PanelVerificationModeCatalog
	// AutoDelete contains lookup cleanup settings and guidance.
	AutoDelete PanelAutoDeleteCatalog
	// Help contains member and administrator command guidance.
	Help PanelHelpCatalog
	// Error contains control-panel errors and refusals.
	Error PanelErrorCatalog
}

// PanelStateCatalog contains verification-state labels.
type PanelStateCatalog struct {
	// Enabled labels enabled verification.
	Enabled Text
	// Disabled labels disabled verification.
	Disabled Text
}

// PanelStatusCatalog contains status and statistics messages.
type PanelStatusCatalog struct {
	// Ping formats the version, uptime, and verification state.
	Ping Format
	// Stats formats daily verification counts and runtime state.
	Stats Format
}

// PanelVerificationCatalog contains verification-setting confirmations.
type PanelVerificationCatalog struct {
	// Started confirms that join verification is enabled.
	Started Text
	// Stopped confirms that join verification is disabled.
	Stopped Text
}

// PanelRichTextCatalog contains rich-text setting confirmations.
type PanelRichTextCatalog struct {
	// Enabled confirms rich-text output.
	Enabled Text
	// Disabled confirms plain-text output.
	Disabled Text
}

// PanelNameSpoilerCatalog contains applicant-name hiding confirmations.
type PanelNameSpoilerCatalog struct {
	// Enabled confirms that applicant names are hidden.
	Enabled Text
	// Disabled confirms that applicant names are visible.
	Disabled Text
}

// PanelVerificationModeCatalog contains verification-mode settings and guidance.
type PanelVerificationModeCatalog struct {
	// ConfigSource names the configuration-file source.
	ConfigSource Text
	// RuntimeSource names the runtime command source.
	RuntimeSource Text
	// Current formats the effective mode, source, and usage.
	Current Format
	// KernelSet confirms kernel-version verification.
	KernelSet Text
	// Set confirms an explicit verification mode.
	Set Format
	// AutoSet confirms restoration of the configured mode.
	AutoSet Format
	// Usage explains accepted mode arguments.
	Usage Text
}

// PanelAutoDeleteCatalog contains lookup cleanup settings and guidance.
type PanelAutoDeleteCatalog struct {
	// CurrentEnabled formats an enabled cleanup setting and usage.
	CurrentEnabled Format
	// CurrentDisabled reports a disabled cleanup setting and usage.
	CurrentDisabled Text
	// Disabled confirms that automatic cleanup is disabled.
	Disabled Text
	// Enabled formats confirmation that automatic cleanup is enabled.
	Enabled Format
	// Set formats confirmation of a new cleanup delay.
	Set Format
	// Usage explains accepted cleanup arguments and limits.
	Usage Text
}

// PanelHelpCatalog contains member and administrator command guidance.
type PanelHelpCatalog struct {
	// Member lists actionable member commands.
	Member Text
	// GroupState formats the invoking group's verification state.
	GroupState Format
	// Admin formats actionable administrator commands.
	Admin Format
	// DirectMessageNote formats direct-message limits and command scope.
	DirectMessageNote Format
}

// PanelErrorCatalog contains control-panel errors and refusals.
type PanelErrorCatalog struct {
	// SaveSettings reports a failed settings write.
	SaveSettings Text
	// AdminOnly refuses a settings command from a non-administrator.
	AdminOnly Text
}
