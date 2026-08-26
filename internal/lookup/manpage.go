package lookup

import (
	"context"
	"errors"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// manSections are probed in the order somebody typing a bare name most likely means: a command,
// then an administrator's command, then a file format, then a library or system call.
var manSections = [...]string{"1", "8", "5", "3", "2", "7", "4", "6"}

// manNameRe accepts a page name, optionally with its section, and nothing that could steer the
// request somewhere else.
var manNameRe = regexp.MustCompile(`^([A-Za-z0-9_.:+-]{1,64}?)(?:\.([1-9]))?$`)

// manPageLimit bounds the plain-text page; the largest real ones are a few hundred kilobytes.
const manPageLimit = 512 * 1024

// manSynopsisLines caps how much of the synopsis is quoted, since some pages list every flag.
const manSynopsisLines = 6

// OnMan answers with a manual page's name line and synopsis. Manual pages are the one reference
// every Linux community shares, whatever it runs.
func (v *Service) OnMan(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil {
		return nil
	}
	l := v.requesterLanguage(msg)
	if !v.queryAllowed(ctx, msg, l) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	man := &i18n.Messages.LookupDistros.Man

	arg := strings.TrimSpace(commandArg(msg.Text))
	parts := manNameRe.FindStringSubmatch(arg)
	if parts == nil {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, man.Usage.For(l))
		return nil
	}
	name, section := parts[1], parts[2]

	hc, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()
	page, found, failed := fetchManPage(hc, name, section)
	switch {
	case failed:
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, man.Unavailable.For(l))
	case !found:
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, man.NotFound.Render(l, arg))
	default:
		lines := []string{man.Heading.Render(l, page.url, html.EscapeString(page.title))}
		if page.synopsis != "" {
			lines = append(lines, "", man.Synopsis.Render(l, html.EscapeString(page.synopsis)))
		}
		v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, strings.Join(lines, "\n"))
	}
	return nil
}

type manPage struct {
	title    string
	synopsis string
	url      string
}

// fetchManPage probes the plausible sections and returns the first page that exists. failed
// separates "no such page" from "could not ask", so the reply never blames the name for an
// outage.
func fetchManPage(ctx context.Context, name, section string) (page manPage, found, failed bool) {
	sections := manSections[:]
	if section != "" {
		sections = []string{section}
	}
	anyReachable := false
	for _, s := range sections {
		id := name + "." + s
		body, err := httpGetBody(ctx, "https://man.archlinux.org/man/"+id+".txt", manPageLimit)
		if err != nil {
			var status *httpStatusError
			if errors.As(err, &status) && status.code == 404 {
				anyReachable = true // the site answered; this section simply has no page
			}
			continue
		}
		anyReachable = true
		title, synopsis := parseManPage(string(body))
		if title == "" {
			title = id
		}
		return manPage{
			title:    title,
			synopsis: synopsis,
			url:      "https://man.archlinux.org/man/" + id,
		}, true, false
	}
	return manPage{}, false, !anyReachable
}

// parseManPage reads the two sections worth quoting: NAME, which says what the page is, and
// SYNOPSIS, which says how to invoke it.
func parseManPage(body string) (title, synopsis string) {
	return manSectionText(body, "NAME", 2), manSectionText(body, "SYNOPSIS", manSynopsisLines)
}

func manSectionText(body, heading string, maxLines int) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.TrimRight(line, " \t") != heading {
			continue
		}
		var collected []string
		for _, next := range lines[i+1:] {
			trimmed := strings.TrimSpace(next)
			if trimmed == "" {
				if len(collected) > 0 {
					break
				}
				continue
			}
			if next == strings.TrimLeft(next, " \t") {
				break // an unindented line starts the next section
			}
			collected = append(collected, trimmed)
			if len(collected) >= maxLines {
				break
			}
		}
		return strings.Join(collected, "\n")
	}
	return ""
}
