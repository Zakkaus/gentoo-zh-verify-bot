package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// cveIDRe matches the published identifier form and nothing that could steer the request.
var cveIDRe = regexp.MustCompile(`^(?i)(CVE-[0-9]{4}-[0-9]{4,10})$`)

// cveBodyLimit bounds one record; the largest carry long descriptions and many references.
const cveBodyLimit = 1024 * 1024

// cveDescriptionLimit keeps a long advisory readable in a chat message.
const cveDescriptionLimit = 600

// OnCVE reports one vulnerability from the National Vulnerability Database: how severe it is,
// when it was published, and what it is.
func (v *Service) OnCVE(ctx *th.Context, update telego.Update) error {
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
	cve := &i18n.Messages.LookupDistros.CVE

	arg := strings.TrimSpace(commandArg(msg.Text))
	match := cveIDRe.FindStringSubmatch(arg)
	if match == nil {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, cve.Usage.For(l))
		return nil
	}
	id := strings.ToUpper(match[1])

	hc, cancel := context.WithTimeout(c, 25*time.Second)
	defer cancel()
	record, found, failed := fetchCVE(hc, id)
	switch {
	case failed:
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, cve.Unavailable.For(l))
	case !found:
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, cve.NotFound.Render(l, html.EscapeString(id)))
	default:
		lines := []string{cve.Heading.Render(l, "https://nvd.nist.gov/vuln/detail/"+id, html.EscapeString(id))}
		if record.severity != "" {
			lines = append(lines, cve.Severity.Render(l, html.EscapeString(record.severity), html.EscapeString(record.score)))
		}
		if record.published != "" {
			lines = append(lines, cve.Published.Render(l, html.EscapeString(record.published), html.EscapeString(record.status)))
		}
		if record.description != "" {
			lines = append(lines, "", html.EscapeString(record.description))
		}
		v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, strings.Join(lines, "\n"))
	}
	return nil
}

type cveRecord struct {
	severity    string
	score       string
	published   string
	status      string
	description string
}

func fetchCVE(ctx context.Context, id string) (record cveRecord, found, failed bool) {
	body, err := httpGetBody(ctx, "https://services.nvd.nist.gov/rest/json/cves/2.0?cveId="+id, cveBodyLimit)
	if err != nil {
		var status *httpStatusError
		if errors.As(err, &status) && status.code == 404 {
			return cveRecord{}, false, false
		}
		return cveRecord{}, false, true
	}
	var parsed struct {
		Vulnerabilities []struct {
			CVE struct {
				ID           string `json:"id"`
				Published    string `json:"published"`
				VulnStatus   string `json:"vulnStatus"`
				Descriptions []struct {
					Lang  string `json:"lang"`
					Value string `json:"value"`
				} `json:"descriptions"`
				Metrics struct {
					V31 []cveMetric `json:"cvssMetricV31"`
					V30 []cveMetric `json:"cvssMetricV30"`
					V2  []cveMetric `json:"cvssMetricV2"`
				} `json:"metrics"`
			} `json:"cve"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return cveRecord{}, false, true
	}
	if len(parsed.Vulnerabilities) == 0 {
		return cveRecord{}, false, false
	}
	entry := parsed.Vulnerabilities[0].CVE
	record.published = strings.SplitN(entry.Published, "T", 2)[0]
	record.status = entry.VulnStatus
	for _, d := range entry.Descriptions {
		if d.Lang == "en" {
			record.description = capRunes(d.Value, cveDescriptionLimit)
			break
		}
	}
	// Prefer the newest scoring version the record carries; an older one is better than none.
	for _, metrics := range [][]cveMetric{entry.Metrics.V31, entry.Metrics.V30, entry.Metrics.V2} {
		if len(metrics) == 0 {
			continue
		}
		record.severity = metrics[0].CVSSData.BaseSeverity
		record.score = strconv.FormatFloat(metrics[0].CVSSData.BaseScore, 'f', -1, 64)
		break
	}
	return record, true, false
}

type cveMetric struct {
	CVSSData struct {
		BaseScore    float64 `json:"baseScore"`
		BaseSeverity string  `json:"baseSeverity"`
	} `json:"cvssData"`
}

// capRunes truncates on a rune boundary so a cut never splits a character.
func capRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
