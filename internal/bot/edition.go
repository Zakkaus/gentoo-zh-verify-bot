package bot

import "github.com/Zakkaus/gentoo-zh-verify-bot/internal/edition"

// gentooPrefix names the Gentoo-specific lookups for routing and menu registration. It comes
// from the edition package so that the commands the bot answers and the command names its
// messages print can never disagree.
const gentooPrefix = edition.CommandPrefix
