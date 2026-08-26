//go:build !gentoo

// Package edition holds the few constants that differ between the two builds this repository
// produces, so that every other package asks one place rather than testing a build tag.
package edition

const (
	// Name is the binary, systemd unit, and configuration directory name for this build.
	Name = "gentoo-zhbot"
	// CommandPrefix qualifies the Gentoo lookups. A build serving Linux communities in general
	// still answers them, but /pkg belongs to whichever distribution the group actually runs,
	// so the Gentoo lookups become /gpkg, /gnews and so on.
	CommandPrefix = "g"
	// KernelExampleSuffix is the release suffix shown in the verification prompt's format
	// example. Naming a distribution only makes sense where the group runs it.
	KernelExampleSuffix = ""
	// IsGentoo reports whether this build serves the Gentoo-zh Community and may say so.
	IsGentoo = false
)
