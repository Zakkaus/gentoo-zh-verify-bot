package i18n

// BotCatalog contains bot lifecycle and command text.
type BotCatalog struct {
	// Menu contains Telegram command-menu descriptions.
	Menu BotMenuCatalog
	// Lifecycle contains process-level bot alerts.
	Lifecycle BotLifecycleCatalog
	// DirectMessage contains ordinary direct-message replies.
	DirectMessage BotDirectMessageCatalog
	// Registration contains owner claim and runtime group-registration text.
	Registration BotRegistrationCatalog
}

// BotMenuCatalog contains member and administrator command descriptions.
type BotMenuCatalog struct {
	// Member contains member command descriptions.
	Member BotMemberMenuCatalog
	// Admin contains administrator command descriptions.
	Admin BotAdminMenuCatalog
}

// BotMemberMenuCatalog contains member command descriptions.
type BotMemberMenuCatalog struct {
	// Help describes the help command.
	Help Text
	// Pkg describes package lookup.
	Pkg Text
	// Use describes package and USE flag lookup.
	Use Text
	// Bug describes Bugzilla lookup.
	Bug Text
	// News describes Gentoo news lookup.
	News Text
	// Wiki describes wiki lookup.
	Wiki Text
	// BBS describes Linux forum lookup.
	BBS Text
	// Pkgs describes cross-distribution package lookup.
	Pkgs Text
	// Distro describes the package-lookup alias.
	Distro Text
	// Arm describes Gentoo arm64 keyword lookup.
	Arm Text
	// ArmPkgs describes cross-distribution arm64 lookup.
	ArmPkgs Text
	// Ping describes bot status.
	Ping Text
	// Stats describes daily verification statistics.
	Stats Text
}

// BotAdminMenuCatalog contains administrator command descriptions.
type BotAdminMenuCatalog struct {
	// Start describes enabling join verification.
	Start Text
	// Stop describes disabling join verification.
	Stop Text
	// Mute describes muting a replied user.
	Mute Text
	// Unmute describes unmuting a replied user.
	Unmute Text
	// Purge describes banning a user and deleting their messages.
	Purge Text
	// Ban describes banning and removing a user.
	Ban Text
	// Warn formats the warning-limit description.
	Warn Format
	// ClearWarn describes clearing a user's warnings.
	ClearWarn Text
	// Channel describes channel-identity posting controls.
	Channel Text
	// RichText describes rich-text output controls.
	RichText Text
	// NameSpoiler describes applicant-name hiding controls.
	NameSpoiler Text
	// VerificationMode describes verification-mode controls.
	VerificationMode Text
	// AutoDelete describes lookup cleanup controls.
	AutoDelete Text
	// BanTime describes ban-duration controls.
	BanTime Text
}

// BotLifecycleCatalog contains process-level bot alerts.
type BotLifecycleCatalog struct {
	// UnauthorizedChat formats an alert after leaving an unknown chat.
	UnauthorizedChat Format
}

// BotDirectMessageCatalog contains ordinary direct-message replies.
type BotDirectMessageCatalog struct {
	// AutoReply formats the built-in direct-message guidance.
	AutoReply Format
}

// BotRegistrationCatalog contains private owner and group-enrollment notices.
type BotRegistrationCatalog struct {
	// OwnerClaimed confirms the first successful owner claim.
	OwnerClaimed Text
	// OwnerClaimRefused reports an invalid, used, or expired owner claim.
	OwnerClaimRefused Text
	// EnrollmentOwnerOnly rejects enrollment-link creation by a non-owner.
	EnrollmentOwnerOnly Text
	// EnrollmentLink formats a one-use enrollment link and its lifetime.
	EnrollmentLink Format
	// EnrollmentRefused reports an invalid, used, or expired enrollment link.
	EnrollmentRefused Text
	// RegistrationPending formats an authorized add awaiting promotion.
	RegistrationPending Format
	// GroupRegistered formats a completed group registration and settings link.
	GroupRegistered Format
	// RegistrationSaveFailed reports a registration that could not be made durable.
	RegistrationSaveFailed Text
}
