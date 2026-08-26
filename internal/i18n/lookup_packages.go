package i18n

// LookupPackagesCatalog contains package-lookup text.
type LookupPackagesCatalog struct {
	// Source contains labels shared by package data sources.
	Source LookupPackageSourceCatalog
	// Pkg contains /pkg command text.
	Pkg LookupPkgCatalog
	// Use contains /use command text.
	Use LookupUseCatalog
	// Arm contains /arm command text.
	Arm LookupArmCatalog
}

// LookupPackageSourceCatalog contains package-source labels.
type LookupPackageSourceCatalog struct {
	// GentooOfficialTree names the official Gentoo package tree.
	GentooOfficialTree Text
	// Overlay formats an overlay source name.
	Overlay Format
	// ListSeparator separates source names and overlay references.
	ListSeparator Text
	// PartialResults identifies unavailable sources after a partial result.
	PartialResults Format
}

// LookupPkgCatalog contains /pkg command text.
type LookupPkgCatalog struct {
	// Usage explains the accepted /pkg arguments.
	Usage Text
	// ResultsHeading formats the package search heading.
	ResultsHeading Format
	// OfficialHeading labels official-tree results.
	OfficialHeading Text
	// OverlayCount formats an overlay result count.
	OverlayCount Format
	// NotFound reports an authoritative empty result.
	NotFound Text
	// Unavailable reports sources that could not be queried.
	Unavailable Format
	// KeywordLegend explains version markers by source.
	KeywordLegend Text
}

// LookupUseCatalog contains /use command text.
type LookupUseCatalog struct {
	// Usage explains the accepted /use arguments.
	Usage Text
	// LocalFlags labels package-local USE flags.
	LocalFlags Text
	// GlobalFlags labels global USE flags.
	GlobalFlags Text
	// Count formats an item count.
	Count Format
	// ValueSeparator separates a counted heading from its values.
	ValueSeparator Text
	// TruncatedCount formats the total after a truncated value list.
	TruncatedCount Format
	// SourceLabel formats a package source label.
	SourceLabel Format
	// Homepage labels the link to a package's own website.
	Homepage Text
	// VersionStableLatest formats distinct stable and latest versions.
	VersionStableLatest Format
	// VersionStable formats a stable version.
	VersionStable Format
	// VersionLatest formats a latest version without an amd64 stable keyword.
	VersionLatest Format
	// NoFlags reports a package without USE flags.
	NoFlags Text
	// AlsoInOverlay formats other overlays containing the package.
	AlsoInOverlay Format
	// OverlayLegend explains overlay USE flag data.
	OverlayLegend Text
	// OfficialLegend explains official-tree version and USE flag markers.
	OfficialLegend Text
	// NotFound reports an authoritative exact-match miss.
	NotFound Format
	// Unavailable reports an exact match that cannot be confirmed.
	Unavailable Format
	// MultipleMatches asks for an unambiguous package atom.
	MultipleMatches Text
	// PartialMatches identifies unavailable sources after ambiguous matches.
	PartialMatches Format
	// InfoUnavailable reports package details that could not be fetched.
	InfoUnavailable Format
}

// LookupArmCatalog contains /arm command text.
type LookupArmCatalog struct {
	// Usage explains the accepted /arm arguments.
	Usage Text
	// OfficialUnavailable reports an official-tree outage.
	OfficialUnavailable Text
	// NotFound reports an authoritative package miss.
	NotFound Format
	// Heading formats a package arm64 keyword heading.
	Heading Format
	// KeywordUnavailable reports keyword details that could not be fetched.
	KeywordUnavailable Text
	// StableTesting formats distinct stable and testing versions.
	StableTesting Format
	// StableOnly formats a stable-only result.
	StableOnly Format
	// TestingOnly formats a testing-only result and its installation action.
	TestingOnly Format
	// NoKeyword reports a package without an arm64 keyword.
	NoKeyword Text
}
