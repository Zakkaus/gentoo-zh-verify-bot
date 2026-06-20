package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

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
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
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
