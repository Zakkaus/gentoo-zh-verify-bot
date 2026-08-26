package lookup

import (
	"context"
	"encoding/json"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// kernelReleasesURL is the machine-readable listing kernel.org publishes for its front page.
const kernelReleasesURL = "https://www.kernel.org/releases.json"

// kernelReleasesLimit bounds the response; the real document is a few kilobytes.
const kernelReleasesLimit = 256 * 1024

// kernelReleaseTTL keeps the listing briefly. Releases appear a few times a week, so a short
// cache spares kernel.org a request per query without ever showing a stale week.
const kernelReleaseTTL = 30 * time.Minute

type kernelRelease struct {
	Moniker  string `json:"moniker"`
	Version  string `json:"version"`
	IsEOL    bool   `json:"iseol"`
	Released struct {
		ISODate string `json:"isodate"`
	} `json:"released"`
}

type kernelReleases struct {
	Releases []kernelRelease `json:"releases"`
}

// OnKernel lists the kernel versions kernel.org currently publishes. Every Linux community asks
// this, and here it also answers the question the verification challenge raises.
func (v *Service) OnKernel(ctx *th.Context, update telego.Update) error {
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
	kernel := &i18n.Messages.LookupDistros.Kernel

	releases, ok := fetchKernelReleases(c)
	if !ok {
		v.replyLookupPlain(c, bot, msg.Chat.ID, msg.MessageID, kernel.Unavailable.For(l))
		return nil
	}
	lines := []string{kernel.Heading.For(l)}
	for _, r := range releases {
		note := r.Released.ISODate
		if r.IsEOL {
			note = kernel.EOL.For(l)
		}
		lines = append(lines, kernel.Row.Render(l, html.EscapeString(r.Moniker), html.EscapeString(r.Version), html.EscapeString(note)))
	}
	lines = append(lines, "", kernel.Footer.For(l))
	v.replyLookupHTML(c, bot, msg.Chat.ID, msg.MessageID, strings.Join(lines, "\n"))
	return nil
}

func fetchKernelReleases(ctx context.Context) ([]kernelRelease, bool) {
	if cached, ok := kernelCacheGet(); ok {
		return cached, true
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body, err := httpGetBody(c, kernelReleasesURL, kernelReleasesLimit)
	if err != nil {
		return nil, false
	}
	var parsed kernelReleases
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Releases) == 0 {
		return nil, false
	}
	kernelCachePut(parsed.Releases)
	return parsed.Releases, true
}

// The cache is one small slice guarded by its own mutex; it never grows.
var (
	kernelCacheMu   sync.Mutex
	kernelCacheAt   time.Time
	kernelCacheData []kernelRelease
)

func kernelCacheGet() ([]kernelRelease, bool) {
	kernelCacheMu.Lock()
	defer kernelCacheMu.Unlock()
	if kernelCacheData == nil || time.Since(kernelCacheAt) > kernelReleaseTTL {
		return nil, false
	}
	return kernelCacheData, true
}

func kernelCachePut(releases []kernelRelease) {
	kernelCacheMu.Lock()
	defer kernelCacheMu.Unlock()
	kernelCacheData = releases
	kernelCacheAt = time.Now()
}
