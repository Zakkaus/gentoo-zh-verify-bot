package bot

import "github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"

// gentooPrefix names the Gentoo-specific lookups for routing and menu registration. It comes
// from the i18n layer so that command names in message text and command names the bot actually
// answers can never disagree.
const gentooPrefix = i18n.CommandPrefix
