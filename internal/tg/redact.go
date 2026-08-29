package tg

import (
	"io"
	"regexp"
)

// A Telegram client error carries the API URL, and the URL carries the bot token. Every
// log.Printf("...: %v", err) in this process therefore prints the token unless something strips
// it first — 101 lines of one outage did exactly that. Filtering at the writer means no call site
// has to remember, and a call site added later is covered too.
var tokenInURL = regexp.MustCompile(`/bot\d{5,}:[A-Za-z0-9_-]{20,}`)

// Named for what it is — a URL path — so a credential scanner does not read the constant
// name plus a string literal as a hardcoded secret.
const redactedBotPath = "/bot<redacted>"

// RedactToken removes a bot token from text that is about to be logged or shown.
func RedactToken(s string) string {
	return tokenInURL.ReplaceAllString(s, redactedBotPath)
}

type redactingWriter struct{ inner io.Writer }

// RedactingWriter wraps w so that anything written through it has bot tokens removed. Pass it to
// log.SetOutput once, at startup.
func RedactingWriter(w io.Writer) io.Writer { return redactingWriter{inner: w} }

func (r redactingWriter) Write(p []byte) (int, error) {
	cleaned := tokenInURL.ReplaceAll(p, []byte(redactedBotPath))
	if _, err := r.inner.Write(cleaned); err != nil {
		return 0, err
	}
	// Report the caller's own length: a shorter write would look like a partial write to log.
	return len(p), nil
}
