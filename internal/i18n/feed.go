package i18n

// FeedCatalog contains feed publication text.
type FeedCatalog struct {
	// Bug contains Bugzilla feed field labels and separators.
	Bug FeedBugCatalog
	// Config contains user-facing configuration policy refusals.
	Config FeedConfigCatalog
}

// FeedBugCatalog contains Bugzilla feed field labels and separators.
type FeedBugCatalog struct {
	// FieldSeparator separates a field label from its value.
	FieldSeparator Text
	// StatusResolutionSeparator separates a bug status from its resolution.
	StatusResolutionSeparator Text
	// Status labels the bug status.
	Status Text
	// ProductComponent labels the Bugzilla product and component.
	ProductComponent Text
	// Priority labels the bug priority.
	Priority Text
	// Severity labels the bug severity.
	Severity Text
	// Keywords labels Bugzilla keywords.
	Keywords Text
	// Packages labels affected packages.
	Packages Text
	// Assignee labels the bug assignee.
	Assignee Text
	// Reporter labels the bug reporter.
	Reporter Text
	// CreationDate labels the bug creation date.
	CreationDate Text
}

// FeedConfigCatalog contains user-facing configuration policy refusals.
type FeedConfigCatalog struct {
	// ControlGroupOnly formats a refusal outside the configured control group.
	ControlGroupOnly Format
}
