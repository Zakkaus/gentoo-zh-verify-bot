package lookup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// kernel.org publishes one document; parsing it must survive the shapes it actually takes,
// including a series marked end of life and a moniker this code has never seen.
func TestKernelReleasesParsing(t *testing.T) {
	const body = `{"latest_stable":{"version":"7.2"},"releases":[
		{"moniker":"mainline","version":"7.2","released":{"isodate":"2026-08-16"}},
		{"moniker":"stable","version":"7.1.10","released":{"isodate":"2026-08-23"}},
		{"moniker":"longterm","version":"6.1.184","released":{"isodate":"2026-08-23"}},
		{"moniker":"linux-next","version":"next-20260825","released":{"isodate":"2026-08-25"}},
		{"moniker":"longterm","version":"5.15.999","iseol":true,"released":{"isodate":"2025-01-01"}}]}`
	withFixtureBody(t, kernelReleasesURL, body, func() {
		kernelCacheReset()
		releases, ok := fetchKernelReleases(context.Background())
		if !ok {
			t.Fatal("a well-formed listing must parse")
		}
		if len(releases) != 5 {
			t.Fatalf("releases = %d, want 5", len(releases))
		}
		if releases[0].Moniker != "mainline" || releases[0].Version != "7.2" {
			t.Errorf("first release = %+v", releases[0])
		}
		if !releases[4].IsEOL {
			t.Error("a series marked iseol must be reported as unmaintained")
		}
	})
}

// A body that is not the expected document is refused rather than reported as an empty list.
func TestKernelReleasesRefusesJunk(t *testing.T) {
	for _, body := range []string{"", "null", "{}", `{"releases":[]}`, "<html>"} {
		withFixtureBody(t, kernelReleasesURL, body, func() {
			kernelCacheReset()
			if _, ok := fetchKernelReleases(context.Background()); ok {
				t.Errorf("%q must not be accepted as a release listing", body)
			}
		})
	}
}

// The listing is cached briefly so one query per group does not become one request per query.
func TestKernelReleasesAreCached(t *testing.T) {
	calls := 0
	withCountingFixture(t, kernelReleasesURL,
		`{"releases":[{"moniker":"stable","version":"7.1.10","released":{"isodate":"2026-08-23"}}]}`,
		&calls, func() {
			kernelCacheReset()
			for range 3 {
				if _, ok := fetchKernelReleases(context.Background()); !ok {
					t.Fatal("fetch failed")
				}
			}
			if calls != 1 {
				t.Errorf("HTTP calls = %d, want 1: the listing is cached", calls)
			}
		})
}

func kernelCacheReset() {
	kernelCacheMu.Lock()
	defer kernelCacheMu.Unlock()
	kernelCacheData = nil
	kernelCacheAt = time.Time{}
}

// withFixtureBody serves one inline body for one URL, without a fixture file on disk.
func withFixtureBody(t *testing.T, url, body string, fn func()) {
	t.Helper()
	calls := 0
	withCountingFixture(t, url, body, &calls, fn)
}

func withCountingFixture(t *testing.T, url, body string, calls *int, fn func()) {
	t.Helper()
	old := httpClient
	httpClient = &http.Client{Transport: fixtureRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != url {
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
		*calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	t.Cleanup(func() { httpClient = old })
	fn()
}

// withStatusFixture answers every request with one status and an empty body.
func withStatusFixture(t *testing.T, code int, fn func()) {
	t.Helper()
	old := httpClient
	httpClient = &http.Client{Transport: fixtureRoundTrip(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: code,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})}
	t.Cleanup(func() { httpClient = old })
	fn()
}
