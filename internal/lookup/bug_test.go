package lookup

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

type bugTestRoundTripper func(*http.Request) (*http.Response, error)

func (f bugTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchBugLookupState(t *testing.T) {
	oldClient := httpClient
	t.Cleanup(func() { httpClient = oldClient })

	tests := []struct {
		name      string
		status    int
		body      string
		err       error
		wantState bugLookupState
		wantTitle string
	}{
		{
			name:      "found",
			status:    http.StatusOK,
			body:      `{"bugs":[{"summary":"Example","status":"CONFIRMED"}]}`,
			wantState: bugLookupFound,
			wantTitle: "Example",
		},
		{name: "genuine 404", status: http.StatusNotFound, wantState: bugLookupNotFound},
		{name: "rate limited", status: http.StatusTooManyRequests, wantState: bugLookupUnavailable},
		{name: "server failure", status: http.StatusInternalServerError, wantState: bugLookupUnavailable},
		{name: "timeout", err: context.DeadlineExceeded, wantState: bugLookupUnavailable},
		{name: "empty 200", status: http.StatusOK, body: `{"bugs":[]}`, wantState: bugLookupUnavailable},
		{name: "error 200", status: http.StatusOK, body: `{"error":true}`, wantState: bugLookupUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpClient = &http.Client{Transport: bugTestRoundTripper(func(*http.Request) (*http.Response, error) {
				if tt.err != nil {
					return nil, tt.err
				}
				return &http.Response{
					StatusCode: tt.status,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(tt.body)),
				}, nil
			})}
			info, state := fetchBug(context.Background(), "123")
			if state != tt.wantState {
				t.Errorf("fetchBug() state = %v, want %v", state, tt.wantState)
			}
			if info.summary != tt.wantTitle {
				t.Errorf("fetchBug() summary = %q, want %q", info.summary, tt.wantTitle)
			}
		})
	}
}

func TestBugLookupFailureMessage(t *testing.T) {
	const (
		id   = "123"
		link = "https://bugs.gentoo.org/123"
	)
	l := i18n.LangZH
	tests := []struct {
		name  string
		state bugLookupState
		want  string
	}{
		{
			name:  "not found",
			state: bugLookupNotFound,
			want:  i18n.Messages.LookupContent.Bug.NotFound.Render(l, id),
		},
		{
			name:  "temporary failure",
			state: bugLookupUnavailable,
			want:  i18n.Messages.LookupContent.Bug.Unavailable.Render(l, id, link),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bugLookupFailureMessage(l, id, link, tt.state); got != tt.want {
				t.Errorf("bugLookupFailureMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBugLabels(t *testing.T) {
	l := i18n.LangZH
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "removed package resolution",
			got:  bugResolutionZH["PKGREMOVED"],
			want: i18n.Messages.LookupContent.Bug.Resolution.PackageRemoved.For(l),
		},
		{
			name: "upstream resolution",
			got:  bugResolutionZH["UPSTREAM"],
			want: i18n.Messages.LookupContent.Bug.Resolution.Upstream.For(l),
		},
		{
			name: "enhancement severity",
			got:  bugSeverityZH["enhancement"],
			want: i18n.Messages.LookupContent.Bug.Severity.Enhancement.For(l),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("label = %q, want %q", tt.got, tt.want)
			}
		})
	}
}
