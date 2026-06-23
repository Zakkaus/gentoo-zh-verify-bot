package main

import (
	"errors"
	"testing"
)

// TestHTTPStatusCode: a non-200 httpGet error carries its status (so a real 404 can be told apart
// from a transient timeout/5xx), while a non-HTTP failure reports 0. This drives the armpkgs
// "not found" vs "query failed" distinction.
func TestHTTPStatusCode(t *testing.T) {
	if got := httpStatusCode(&httpStatusError{url: "u", code: 404}); got != 404 {
		t.Errorf("httpStatusCode(404) = %d, want 404", got)
	}
	if got := httpStatusCode(&httpStatusError{url: "u", code: 503}); got != 503 {
		t.Errorf("httpStatusCode(503) = %d, want 503", got)
	}
	if got := httpStatusCode(errors.New("context deadline exceeded")); got != 0 {
		t.Errorf("a non-HTTP (timeout/network) error must report 0, got %d", got)
	}
	if got := httpStatusCode(nil); got != 0 {
		t.Errorf("a nil error must report 0, got %d", got)
	}
}
