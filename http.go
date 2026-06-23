package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// httpStatusError is returned by httpGet for a non-200 response, carrying the status code so a
// caller can tell a definitive 404 (the resource really isn't there) from a transient 5xx/timeout/
// network failure (where a definitive negative answer would be wrong).
type httpStatusError struct {
	url  string
	code int
}

func (e *httpStatusError) Error() string { return fmt.Sprintf("GET %s: HTTP %d", e.url, e.code) }

// httpStatusCode returns the HTTP status carried by a non-200 httpGet error, or 0 when the failure
// wasn't an HTTP response at all (timeout, DNS, connection reset, body read).
func httpStatusCode(err error) int {
	var se *httpStatusError
	if errors.As(err, &se) {
		return se.code
	}
	return 0
}

// The shared outbound HTTP layer used by every network command (/pkg, /use, /bug, /news,
// /wiki, /bbs, /distro, /arm, /armpkgs, the feed). All requests carry the bot's User-Agent
// (var userAgent, settable via the user_agent config) and are gated on HTTP 200.

var httpClient = &http.Client{Timeout: 25 * time.Second}

// githubToken (optional, from the GITHUB_TOKEN env var) lifts the GitHub API rate
// limit from 60/h to 5000/h. Reading public repos needs a token with NO scopes.
var githubToken string

// maxJSONBytes caps JSON response bodies: large enough for the biggest overlay's
// recursive GitHub tree (a few MB), small enough to bound memory on a hostile body.
const maxJSONBytes = 32 << 20

// httpSem bounds the number of CONCURRENT outbound requests (every lookup + the feed share it).
// This preserves "群里不限次" — group lookups aren't frequency-limited — while capping worst-case
// concurrent network/goroutine pressure under a spam burst (e.g. /armpkgs fans out ~6 each). Each
// httpGet holds one slot until its response body is closed.
const httpMaxConcurrent = 24

var httpSem = make(chan struct{}, httpMaxConcurrent)

// semReleaseCloser releases one httpSem slot exactly once, when the response body is closed.
type semReleaseCloser struct {
	io.ReadCloser
	once sync.Once
}

func (s *semReleaseCloser) Close() error {
	err := s.ReadCloser.Close()
	s.once.Do(func() { <-httpSem })
	return err
}

// httpGet issues a GET with the shared client + User-Agent (plus any extra headers)
// and returns the response only on HTTP 200; the caller must close resp.Body.
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
	select { // acquire a concurrency slot (or give up if the request context is already done)
	case httpSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
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

// httpGetJSON GETs url and decodes a 200 JSON response into dst (capped at maxJSONBytes).
func httpGetJSON(ctx context.Context, url string, hdr http.Header, dst any) error {
	resp, err := httpGet(ctx, url, hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(dst)
}

// httpGetBody GETs url and returns up to limit bytes of a 200 response (for HTML/text scraping).
func httpGetBody(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := httpGet(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
