package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

// httpStatusError preserves authoritative statuses such as 404 across the shared transport.
type httpStatusError struct {
	url  string
	code int
}

// Error describes the authoritative HTTP status.
func (e *httpStatusError) Error() string { return fmt.Sprintf("GET %s: HTTP %d", e.url, e.code) }

// httpBusyError marks local saturation as a temporary lookup failure.
type httpBusyError struct {
	url  string
	wait time.Duration
}

// Error describes local outbound saturation.
func (e *httpBusyError) Error() string {
	return fmt.Sprintf("GET %s: outbound HTTP limit busy for %s", e.url, e.wait)
}

// httpBodyTooLargeError prevents parsers from treating a valid-looking prefix as a complete reply.
type httpBodyTooLargeError struct {
	url   string
	limit int64
}

// Error describes a response that exceeded its parser limit.
func (e *httpBodyTooLargeError) Error() string {
	return fmt.Sprintf("GET %s: response body exceeds %d bytes", e.url, e.limit)
}

// httpStatusCode returns zero for failures without an HTTP response.
func httpStatusCode(err error) int {
	var se *httpStatusError
	if errors.As(err, &se) {
		return se.code
	}
	return 0
}

var httpClient = &http.Client{Timeout: 25 * time.Second}

// An unscoped GITHUB_TOKEN raises the public API limit.
var githubToken string

// Service owns lookup handlers and their private-query rate state.
type Service struct {
	settings  *store.Settings
	telegram  *tg.Client
	cfg       *config.Config
	mu        sync.Mutex
	queryHits map[int64][]time.Time
}

// New constructs a lookup service from runtime settings, Telegram transport, configuration, and an optional GitHub token.
func New(settings *store.Settings, telegram *tg.Client, cfg *config.Config, githubAPIToken string) *Service {
	if cfg == nil {
		cfg = &config.Config{}
	}
	configurePkg(cfg)
	configureNews(cfg)
	githubToken = githubAPIToken
	return &Service{
		settings:  settings,
		telegram:  telegram,
		cfg:       cfg,
		queryHits: map[int64][]time.Time{},
	}
}

// Warm refreshes the package-search cache unless it is already fresh or refreshing.
func (s *Service) Warm(ctx context.Context) {
	pkgC.refresh(ctx)
}

// AutoDelete returns the effective lookup cleanup duration and enabled state for one group.
func (s *Service) AutoDelete(groupID int64) (time.Duration, bool) {
	if s.settings != nil {
		if group, ok := s.settings.Group(groupID); ok {
			duration, valid := config.SecondsToDuration(group.LookupTTLSeconds().Value)
			return duration, group.LookupAutoDeleteEnabled().Value && valid
		}
	}
	seconds := 180
	if s.cfg.LookupTTLSeconds != nil {
		seconds = max(*s.cfg.LookupTTLSeconds, 0)
	}
	duration, valid := config.SecondsToDuration(seconds)
	return duration, seconds > 0 && valid
}

func (s *Service) isGroup(groupID int64) bool {
	if s.settings != nil {
		return s.settings.IsGroup(groupID)
	}
	return s.cfg.IsGroup(groupID)
}

func (s *Service) controlGroupID() int64 {
	if s.settings != nil {
		return s.settings.ControlGroupID()
	}
	if s.cfg.ControlGroupID != 0 {
		return s.cfg.ControlGroupID
	}
	if len(s.cfg.GroupIDs) != 0 {
		return s.cfg.GroupIDs[0]
	}
	if len(s.cfg.Groups) != 0 {
		return s.cfg.Groups[0].ID
	}
	return 0
}

func (s *Service) lookupSettingsGroupID(chatID int64) int64 {
	if s.settings != nil && s.settings.IsGroup(chatID) {
		return chatID
	}
	return s.controlGroupID()
}

func (s *Service) cleanupAfter(chatID int64) time.Duration {
	ttl, on := s.AutoDelete(s.lookupSettingsGroupID(chatID))
	if !on {
		return 0
	}
	return ttl
}

// Delete group lookup commands and answers together using a fresh timer context.
func (s *Service) scheduleLookupCleanup(_ *telego.Bot, chatID int64, cmdMsgID, respMsgID int) {
	s.telegram.ScheduleCleanup(chatID, cmdMsgID, respMsgID, s.cleanupAfter(chatID))
}

// Plain text preserves angle-bracket placeholders and still follows reply/cleanup semantics.
func (s *Service) replyLookupPlain(c context.Context, _ *telego.Bot, chatID int64, replyTo int, text string) {
	s.telegram.ReplyPlain(c, chatID, replyTo, text, s.cleanupAfter(chatID))
}

// HTML lookup replies require callers to escape dynamic content.
func (s *Service) replyLookupHTML(c context.Context, _ *telego.Bot, chatID int64, replyTo int, htmlText string) *telego.Message {
	return s.telegram.ReplyHTML(c, chatID, replyTo, htmlText, s.cleanupAfter(chatID))
}

// Bot API rich messages fall back to HTML on server rejection.
func (s *Service) sendRichOrHTML(c context.Context, _ *telego.Bot, chatID int64, replyTo int, richHTML, plainHTML string) {
	s.telegram.SendRichOrHTML(c, chatID, replyTo, richHTML, plainHTML, s.richEnabled(), s.cleanupAfter(chatID))
}

const privateQueryWindow = time.Minute
const privateQueryMapMax = 10000

func (s *Service) privateQueryPerMin() int {
	if s.settings != nil {
		return s.settings.Global().PrivateQueryPerMin().Value
	}
	if s.cfg.PrivateQueryPerMin > 0 {
		return s.cfg.PrivateQueryPerMin
	}
	return 3
}

func (s *Service) richEnabled() bool {
	if s.settings != nil {
		return s.settings.Global().RichMessages().Value
	}
	return s.cfg.RichMessages
}
func (s *Service) groupLanguage(groupID int64) i18n.Lang {
	if s.settings != nil {
		if group, ok := s.settings.Group(groupID); ok {
			return i18n.FromStored(group.Lang().Value)
		}
	}
	return i18n.FromStored(s.cfg.LangForGroup(groupID))
}

func (s *Service) requesterLanguage(msg *telego.Message) i18n.Lang {
	fallback := i18n.LangEN
	if s.isGroup(msg.Chat.ID) {
		fallback = s.groupLanguage(msg.Chat.ID)
	}
	return i18n.FromRequester(msg.From.LanguageCode, fallback)
}

// Sliding-window limits apply only to private-chat lookups.
func (s *Service) queryRateOK(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-privateQueryWindow)
	kept := s.queryHits[userID][:0]
	for _, hit := range s.queryHits[userID] {
		if hit.After(cutoff) {
			kept = append(kept, hit)
		}
	}
	if len(kept) >= s.privateQueryPerMin() {
		s.queryHits[userID] = kept
		return false
	}
	s.queryHits[userID] = append(kept, now)
	if len(s.queryHits) > privateQueryMapMax {
		for userID, hits := range s.queryHits {
			if len(hits) == 0 || !hits[len(hits)-1].After(cutoff) {
				delete(s.queryHits, userID)
			}
		}
		if len(s.queryHits) > privateQueryMapMax {
			s.queryHits = map[int64][]time.Time{}
		}
	}
	return true
}

// External lookups are unlimited in guarded groups and rate-limited per user in private chats.
func (s *Service) queryAllowed(ctx *th.Context, msg *telego.Message, l i18n.Lang) bool {
	if s.isGroup(msg.Chat.ID) {
		return true
	}
	if msg.Chat.Type == "private" && msg.From != nil {
		if s.queryRateOK(msg.From.ID) {
			return true
		}
		_, _ = ctx.Bot().SendMessage(ctx.Context(), tu.Message(tu.ID(msg.Chat.ID),
			i18n.Messages.LookupContent.Transport.PrivateRateLimited.Render(l, s.privateQueryPerMin())))
		return false
	}
	return false
}

// Bound JSON memory while accommodating recursive GitHub trees.
const maxJSONBytes = 32 << 20

// Every lookup and feed request shares this concurrency bound until its body closes.
const httpMaxConcurrent = 24

// Brief queueing absorbs normal fan-out without parking handlers behind 25-second requests.
const httpSlotWait = 2 * time.Second

var httpSem = make(chan struct{}, httpMaxConcurrent)

// semReleaseCloser releases its outbound slot exactly once.
type semReleaseCloser struct {
	io.ReadCloser
	once sync.Once
}

// Close releases the response body and its outbound slot exactly once.
func (s *semReleaseCloser) Close() error {
	err := s.ReadCloser.Close()
	s.once.Do(func() { <-httpSem })
	return err
}

// Saturation returns a typed temporary error instead of queueing without bound.
func acquireHTTPSlot(ctx context.Context, url string, sem chan struct{}, wait time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case sem <- struct{}{}:
		return nil
	default:
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return &httpBusyError{url: url, wait: wait}
	}
}

// httpGet returns only HTTP 200 responses; callers must close the body.
func httpGet(ctx context.Context, url string, hdr http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, vs := range hdr {
		for _, val := range vs {
			req.Header.Add(k, val)
		}
	}
	if err := acquireHTTPSlot(ctx, url, httpSem, httpSlotWait); err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		<-httpSem
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close() // discarding a non-200 body; close error is irrelevant (slot freed below)
		<-httpSem
		return nil, &httpStatusError{url: url, code: resp.StatusCode}
	}
	resp.Body = &semReleaseCloser{ReadCloser: resp.Body} // slot released when the caller closes the body
	return resp, nil
}

// GetJSON fetches a 200 JSON response into dst with the shared body and concurrency limits.
func GetJSON(ctx context.Context, url string, hdr http.Header, dst any) error {
	resp, err := httpGet(ctx, url, hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(dst)
}

// Reading one extra byte prevents a truncated prefix from reaching a parser.
func httpGetBody(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := httpGet(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, &httpBodyTooLargeError{url: url, limit: limit}
	}
	return body, nil
}
