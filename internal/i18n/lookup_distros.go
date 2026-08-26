package i18n

// LookupDistrosCatalog contains distribution-lookup text.
type LookupDistrosCatalog struct {
	// Pkgs contains cross-distribution package-version lookup text.
	Pkgs LookupDistrosPkgsCatalog
	// Armpkgs contains cross-distribution arm64 lookup text.
	Armpkgs LookupDistrosArmpkgsCatalog
	// Man contains manual-page lookup text.
	Man LookupDistrosManCatalog
	// CVE contains vulnerability lookup text.
	CVE LookupDistrosCVECatalog
	// Repology contains cross-repository version listing text.
	Repology LookupDistrosRepologyCatalog
	// Kernel contains the kernel.org release listing.
	Kernel LookupDistrosKernelCatalog
	// Release contains distribution release-role labels.
	Release LookupDistrosReleaseCatalog
}

// LookupDistrosPkgsCatalog contains /pkgs text.
type LookupDistrosPkgsCatalog struct {
	// RepologyUnavailable formats a temporary Repology failure.
	RepologyUnavailable Format
	// RepologyNotFound formats an empty Repology result.
	RepologyNotFound Format
	// Usage explains the command syntax and data sources.
	Usage Text
	// NoSupportedDistro formats an empty supported-distribution result.
	NoSupportedDistro Format
	// Heading formats the package-version heading.
	Heading Format
	// ClosestMatch formats an inexact-match annotation.
	ClosestMatch Format
	// PlainHeading formats the plain table heading.
	PlainHeading Format
	// ReleaseRole formats a release-role annotation.
	ReleaseRole Format
	// PlainRow formats one plain table row.
	PlainRow Format
	// RichRow formats one rich table row.
	RichRow Format
	// Alternatives formats alternative package matches.
	Alternatives Format
	// RichAlternatives formats collapsible alternative package matches.
	RichAlternatives Format
}

// LookupDistrosArmpkgsCatalog contains /armpkgs text.
// LookupDistrosManCatalog covers manual-page lookup, which every Linux community needs.
type LookupDistrosManCatalog struct {
	Usage       Text
	Heading     Format
	Synopsis    Format
	NotFound    Format
	Unavailable Text
}

// LookupDistrosCVECatalog covers vulnerability lookup by identifier.
type LookupDistrosCVECatalog struct {
	Usage       Text
	Heading     Format
	Severity    Format
	Published   Format
	NotFound    Format
	Unavailable Text
}

// LookupDistrosRepologyCatalog covers one package's version in every repository that ships it.
type LookupDistrosRepologyCatalog struct {
	Usage       Text
	Heading     Format
	Row         Format
	More        Format
	NotFound    Format
	Unavailable Text
}

// LookupDistrosKernelCatalog covers the kernel.org release listing, which every Linux community
// asks about and which this bot's own verification depends on.
type LookupDistrosKernelCatalog struct {
	// Heading introduces the listing.
	Heading Text
	// Row formats one release: moniker, version, and release date.
	Row Format
	// EOL marks a series that is no longer maintained.
	EOL Text
	// Footer reminds applicants to send their own version, not one from the list.
	Footer Text
	// Unavailable reports that kernel.org could not be read.
	Unavailable Text
}

type LookupDistrosArmpkgsCatalog struct {
	// Usage explains the command syntax and compared sources.
	Usage Text
	// Heading formats the arm64 support heading.
	Heading Format
	// Row formats one source status row.
	Row Format
	// Footer explains failure and Gentoo keyword semantics.
	Footer Text
	// QueryFailed reports a temporary source failure.
	QueryFailed Text
	// NotInOfficialTree reports an absent Gentoo package.
	NotInOfficialTree Text
	// StableTesting formats Gentoo stable and testing versions.
	StableTesting Format
	// StableOnly formats a Gentoo stable version.
	StableOnly Format
	// TestingOnly formats a Gentoo testing-only version.
	TestingOnly Format
	// NoArm64Keyword reports an absent Gentoo arm64 keyword.
	NoArm64Keyword Text
	// NoArm64Package reports an absent arm64 package.
	NoArm64Package Text
	// DevelopmentSuite formats an unreleased suite label.
	DevelopmentSuite Format
	// Available formats an available suite and version.
	Available Format
	// NotInFedora reports an absent Fedora package.
	NotInFedora Text
	// FedoraQueryFailed reports a temporary Fedora failure.
	FedoraQueryFailed Text
	// FedoraRawhide formats a Fedora Rawhide version.
	FedoraRawhide Format
	// PKGBUILDParseFailed reports an unreadable AUR architecture declaration.
	PKGBUILDParseFailed Text
	// AnyArchitecture reports an architecture-independent AUR package.
	AnyArchitecture Text
	// DeclaresAarch64 reports an AUR aarch64 declaration.
	DeclaresAarch64 Text
	// Arm32Only reports an AUR 32-bit ARM-only declaration.
	Arm32Only Text
	// X86Only reports an AUR x86-only declaration.
	X86Only Text
	// NotInAUR reports an absent AUR package.
	NotInAUR Text
	// AURQueryFailed reports a temporary AUR failure.
	AURQueryFailed Text
	// NotPackaged reports an absent Arch Linux ARM package.
	NotPackaged Text
	// Packaged reports an available Arch Linux ARM package.
	Packaged Text
}

// LookupDistrosReleaseCatalog contains release-role text.
type LookupDistrosReleaseCatalog struct {
	// StandardSupportEnded labels the end of standard support.
	StandardSupportEnded Text
}
