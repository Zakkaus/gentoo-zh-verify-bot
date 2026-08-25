package i18n

// LookupContentCatalog contains content-lookup text.
type LookupContentCatalog struct {
	// Bug contains Gentoo Bugzilla lookup text and enum labels.
	Bug LookupBugCatalog
	// News contains Gentoo news lookup text.
	News LookupNewsCatalog
	// Wiki contains Gentoo Wiki and ArchWiki lookup text.
	Wiki LookupWikiCatalog
	// BBS contains Linux forum lookup text.
	BBS LookupBBSCatalog
	// Transport contains messages shared by content lookup transports.
	Transport LookupTransportCatalog
}

// LookupBugCatalog contains Gentoo Bugzilla lookup text.
type LookupBugCatalog struct {
	// Usage explains the accepted Bugzilla query.
	Usage Text
	// NotFound formats an authoritative missing-bug response.
	NotFound Format
	// Unavailable formats a retryable Bugzilla failure.
	Unavailable Format
	// Heading formats a Bugzilla result heading.
	Heading Format
	// Details contains labels used in Bugzilla result fields.
	Details LookupBugDetailsCatalog
	// Status contains Bugzilla status labels.
	Status LookupBugStatusCatalog
	// Resolution contains Bugzilla resolution labels.
	Resolution LookupBugResolutionCatalog
	// Severity contains Bugzilla severity labels.
	Severity LookupBugSeverityCatalog
	// Priority contains Bugzilla priority labels.
	Priority LookupBugPriorityCatalog
}

// LookupBugDetailsCatalog contains Bugzilla result field labels.
type LookupBugDetailsCatalog struct {
	// ResolutionSeparator separates a status from its resolution.
	ResolutionSeparator Text
	// Status formats the status field.
	Status Format
	// Severity formats the severity field.
	Severity Format
	// ProductComponent formats the product and component field.
	ProductComponent Format
}

// LookupBugStatusCatalog contains Bugzilla status labels.
type LookupBugStatusCatalog struct {
	// Unconfirmed labels UNCONFIRMED.
	Unconfirmed Text
	// Confirmed labels CONFIRMED.
	Confirmed Text
	// InProgress labels IN_PROGRESS.
	InProgress Text
	// Resolved labels RESOLVED.
	Resolved Text
	// Verified labels VERIFIED.
	Verified Text
}

// LookupBugResolutionCatalog contains Bugzilla resolution labels.
type LookupBugResolutionCatalog struct {
	// Fixed labels FIXED.
	Fixed Text
	// WontFix labels WONTFIX.
	WontFix Text
	// CantFix labels CANTFIX.
	CantFix Text
	// Duplicate labels DUPLICATE.
	Duplicate Text
	// Invalid labels INVALID.
	Invalid Text
	// WorksForMe labels WORKSFORME.
	WorksForMe Text
	// Obsolete labels OBSOLETE.
	Obsolete Text
	// Upstream labels UPSTREAM.
	Upstream Text
	// PackageRemoved labels PKGREMOVED.
	PackageRemoved Text
	// NeedInfo labels NEEDINFO.
	NeedInfo Text
	// TestRequest labels TEST-REQUEST.
	TestRequest Text
	// PendingUpstream labels PENDING-UPSTREAM.
	PendingUpstream Text
}

// LookupBugSeverityCatalog contains Bugzilla severity labels.
type LookupBugSeverityCatalog struct {
	// Blocker labels blocker.
	Blocker Text
	// Critical labels critical.
	Critical Text
	// Major labels major.
	Major Text
	// Normal labels normal.
	Normal Text
	// Minor labels minor.
	Minor Text
	// Trivial labels trivial.
	Trivial Text
	// Enhancement labels enhancement.
	Enhancement Text
}

// LookupBugPriorityCatalog contains Bugzilla priority labels.
type LookupBugPriorityCatalog struct {
	// Highest labels Highest.
	Highest Text
	// High labels High.
	High Text
	// Normal labels Normal.
	Normal Text
	// Low labels Low.
	Low Text
	// Lowest labels Lowest.
	Lowest Text
}

// LookupNewsCatalog contains Gentoo news lookup text.
type LookupNewsCatalog struct {
	// LatestHeading labels an unfiltered news list.
	LatestHeading Text
	// SearchHeading formats a filtered news list.
	SearchHeading Format
	// NoMatches reports an authoritative empty search.
	NoMatches Text
	// Unavailable reports an unavailable news index.
	Unavailable Text
	// Stale reports results from a stale index.
	Stale Text
}

// LookupWikiCatalog contains Gentoo Wiki and ArchWiki lookup text.
type LookupWikiCatalog struct {
	// Usage explains the accepted Wiki query.
	Usage Text
	// Heading formats the search result heading.
	Heading Format
	// SourcesUnavailable formats a partial-source failure.
	SourcesUnavailable Format
	// SourceJoin joins two unavailable source names.
	SourceJoin Text
	// NoMatches reports an authoritative empty search.
	NoMatches Text
}

// LookupBBSCatalog contains Linux forum lookup text.
type LookupBBSCatalog struct {
	// Usage explains the accepted forum query.
	Usage Text
	// Heading formats the forum result heading.
	Heading Format
	// ArchCNHeading labels inline Arch Linux CN results.
	ArchCNHeading Text
	// ArchCNUnavailable reports a retryable Arch Linux CN failure.
	ArchCNUnavailable Text
	// ArchCNNoMatches reports an authoritative empty Arch Linux CN search.
	ArchCNNoMatches Text
	// OtherForums introduces external forum search buttons.
	OtherForums Text
	// GentooForum labels the Gentoo Forums search button.
	GentooForum Text
	// ArchBBS labels the Arch BBS search button.
	ArchBBS Text
	// UbuntuForum labels the Ubuntu Forums search button.
	UbuntuForum Text
	// DebianForum labels the Debian Forums search button.
	DebianForum Text
}

// LookupTransportCatalog contains messages shared by content lookup transports.
type LookupTransportCatalog struct {
	// PrivateRateLimited formats the private-query rate limit.
	PrivateRateLimited Format
}
