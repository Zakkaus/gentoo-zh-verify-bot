package lookup

import (
	"context"
	"html"
	neturl "net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// repologyNameRe matches a project name and nothing that could steer the request elsewhere.
var repologyNameRe = regexp.MustCompile(`^[A-Za-z0-9_.+-]{1,80}$`)

// repologyRows caps the reply. Repology tracks hundreds of repositories, most of which nobody in
// a Linux chat runs, so the named families are listed and the rest are counted.
const repologyRows = 16

// OnRepology lists one project's newest version in every repository family Repology tracks.
// /pkgs answers the same question for a handful of distributions and adds their release status;
// this is the wide view, for when the question is which repository has it at all.
func (v *Service) OnRepology(ctx *th.Context, update telego.Update) error {
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
	repology := &i18n.Messages.LookupDistros.Repology

	name := strings.ToLower(strings.TrimSpace(commandArg(msg.Text)))
	if !repologyNameRe.MatchString(name) {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, repology.Usage.For(l))
		return nil
	}

	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	proj, pkgs, _, _, available := fetchRepology(hc, name)
	switch {
	case !available:
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, repology.Unavailable.For(l))
	case len(pkgs) == 0:
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, repology.NotFound.Render(l, html.EscapeString(name)))
	default:
		entries, others := repologyByFamily(pkgs)
		link := "https://repology.org/project/" + neturl.PathEscape(proj) + "/versions"
		lines := []string{repology.Heading.Render(l, link, html.EscapeString(proj))}
		for _, entry := range entries {
			lines = append(lines, repology.Row.Render(l, html.EscapeString(entry.label), html.EscapeString(entry.version)))
		}
		if others > 0 {
			lines = append(lines, "", repology.More.Render(l, others))
		}
		v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, strings.Join(lines, "\n"))
	}
	return nil
}

type repologyEntry struct {
	label   string
	version string
}

// repologyByFamily keeps the newest version each known family ships and counts everything else,
// reusing the family map and version ordering /pkgs already relies on.
func repologyByFamily(pkgs []repologyPkg) (entries []repologyEntry, others int) {
	newest := map[string]string{}
	unnamed := map[string]bool{}
	for _, p := range pkgs {
		family := famOf(p.Repo)
		if family == "" {
			unnamed[p.Repo] = true
			continue
		}
		if current, ok := newest[family]; !ok || betterVer(current, p.Version) {
			newest[family] = p.Version
		}
	}
	families := make([]string, 0, len(newest))
	for family := range newest {
		families = append(families, family)
	}
	sort.Strings(families)
	for _, family := range families {
		if len(entries) >= repologyRows {
			others++
			continue
		}
		entries = append(entries, repologyEntry{label: family, version: newest[family]})
	}
	return entries, others + len(unnamed)
}
