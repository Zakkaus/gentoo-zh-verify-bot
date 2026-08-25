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

// httpStatusError preserves authoritative statuses such as 404 across the shared transport.
type httpStatusError struct {
	url  string
	code int
}

func (e *httpStatusError) Error() string { return fmt.Sprintf("GET %s: HTTP %d", e.url, e.code) }

// httpBusyError marks local saturation as a temporary lookup failure.
type httpBusyError struct {
	url  string
	wait time.Duration
}

func (e *httpBusyError) Error() string {
	return fmt.Sprintf("GET %s: outbound HTTP limit busy for %s", e.url, e.wait)
}

// httpBodyTooLargeError prevents parsers from treating a valid-looking prefix as a complete reply.
type httpBodyTooLargeError struct {
	url   string
	limit int64
}

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

// httpGetJSON GETs url and decodes a 200 JSON response into dst (capped at maxJSONBytes).
func httpGetJSON(ctx context.Context, url string, hdr http.Header, dst any) error {
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
