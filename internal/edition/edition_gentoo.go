//go:build gentoo

// Package edition holds the few constants that differ between the two builds this repository
// produces, so that every other package asks one place rather than testing a build tag.
package edition

const (
	// Name is the binary, systemd unit, and configuration directory name for this build.
	Name = "gentoo-zh-verify-bot"
	// CommandPrefix qualifies the Gentoo lookups. This build serves the community those
	// commands were written for, so they keep the unqualified names.
	CommandPrefix = ""
	// IsGentoo reports whether this build serves the Gentoo-zh Community and may say so.
	IsGentoo = true
)
