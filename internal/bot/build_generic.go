//go:build !gentoo

package bot

// gentooPrefix names the Gentoo-specific lookups. A build serving Linux communities in general
// still answers them, but it does not hand Gentoo the unqualified names: /pkg belongs to
// whichever distribution the group actually runs, so these become /gpkg, /gnews and so on.
const gentooPrefix = "g"
