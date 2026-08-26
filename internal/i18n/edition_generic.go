//go:build !gentoo

package i18n

// CommandPrefix qualifies the Gentoo-specific lookups. A build serving Linux communities in
// general still answers them, but /pkg belongs to whichever distribution the group actually
// runs, so the Gentoo lookups become /gpkg, /gnews and so on.
const CommandPrefix = "g"
