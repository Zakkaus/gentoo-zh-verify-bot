package lookup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

func TestVerLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool // want verLess(a,b)
	}{
		{"1.0-r2", "1.0-r10", true},   // r2 < r10  (regression guard: was wrongly false)
		{"1.0-r10", "1.0-r2", false},  // r10 is NOT older than r2
		{"1.0_p2", "1.0_p10", true},   // patch level, double digit
		{"1.0_rc9", "1.0_rc11", true}, // release candidate, double digit
		{"1.2", "1.10", true},         // plain numeric dotted parts
		{"1.10", "1.2", false},
		{"1.0", "1.0-r1", true}, // a revision is newer than the bare version
		{"2.0", "2.0", false},   // equal is not "less"
		{"9.1.1652", "9.2.0670", true},
		{"1.0.0", "1.0.0.0", true}, // more tokens (all-equal prefix) is newer
		// Gentoo suffix ordering: _alpha < _beta < _pre < _rc < (release) < _p, and -rN newer.
		{"1.0_rc1", "1.0", true},      // a release candidate is OLDER than the release
		{"1.0", "1.0_rc1", false},     // ...and the release is newer
		{"1.0_alpha1", "1.0", true},   // alpha is older
		{"1.0_beta", "1.0_rc1", true}, // beta < rc
		{"1.0_p1", "1.0", false},      // a patch level is NEWER than the release
		{"1.0", "1.0_p1", true},
		{"1.0_rc1", "1.0_rc2", true}, // rc1 < rc2
	}
	for _, c := range cases {
		if got := verLess(c.a, c.b); got != c.want {
			t.Errorf("verLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCommandArg(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/pkg vim", "vim"},
		{"/pkg\nvim", "vim"},
		{"/pkg\tvim", "vim"},
		{"/pkg", ""},
		{"/pkg  a  b", "a b"},
		{"  /pkg  vim  ", "vim"},
	} {
		if got := commandArg(c.in); got != c.want {
			t.Errorf("commandArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCmpNum(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2", "10", -1},
		{"10", "2", 1},
		{"007", "7", 0},                  // leading zeros ignored
		{"00", "0", 0},                   // all zeros
		{"99999999999999999999", "2", 1}, // no overflow: 20-digit number > 2
	}
	for _, c := range cases {
		if got := cmpNum(c.a, c.b); got != c.want {
			t.Errorf("cmpNum(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestSearchMainTreeAvailability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    []byte
		err     error
		wantOK  bool
		wantLen int
	}{
		{name: "answered empty", body: []byte("<html></html>"), wantOK: true},
		{name: "answered with match", body: []byte(`<a href="/packages/app-editors/vim">vim</a>`), wantOK: true, wantLen: 1},
		{name: "network failure", err: errors.New("connection reset")},
		{name: "server failure", err: &httpStatusError{url: "u", code: 503}},
		{name: "outbound busy", err: &httpBusyError{url: "u", wait: time.Millisecond}},
		{name: "body too large", err: &httpBodyTooLargeError{url: "u", limit: 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := searchMainTreeWith(
				context.Background(),
				"vim",
				func(context.Context, string) (string, string, bool) {
					t.Fatal("bare search must not call the exact-version lookup")
					return "", "", false
				},
				func(context.Context, string, int64) ([]byte, error) { return tc.body, tc.err },
			)
			if ok != tc.wantOK || len(got) != tc.wantLen {
				t.Errorf("searchMainTreeWith() = (%v, %v), want len=%d ok=%v", got, ok, tc.wantLen, tc.wantOK)
			}
		})
	}
}

func TestSearchMainTreeExactAvailability(t *testing.T) {
	for _, tc := range []struct {
		name           string
		stable, latest string
		available      bool
		wantOK         bool
		wantLen        int
	}{
		{name: "found", latest: "9.1", available: true, wantOK: true, wantLen: 1},
		{name: "answered missing", available: true, wantOK: true},
		{name: "lookup failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := searchMainTreeWith(
				context.Background(),
				"app-editors/vim",
				func(context.Context, string) (string, string, bool) {
					return tc.stable, tc.latest, tc.available
				},
				func(context.Context, string, int64) ([]byte, error) {
					t.Fatal("exact search must not fetch the HTML search page")
					return nil, nil
				},
			)
			if ok != tc.wantOK || len(got) != tc.wantLen {
				t.Errorf("searchMainTreeWith() = (%v, %v), want len=%d ok=%v", got, ok, tc.wantLen, tc.wantOK)
			}
		})
	}
}

func TestPkgCacheRefreshAvailability(t *testing.T) {
	sources := []overlay{{name: "answered"}, {name: "failed"}}
	pc := &pkgCache{pkgs: map[string]map[string]string{}, available: map[string]bool{}}
	status := pc.refreshWith(context.Background(), sources, func(_ context.Context, source overlay) (map[string]string, error) {
		if source.name == "failed" {
			return nil, errors.New("upstream unavailable")
		}
		return map[string]string{"app-editors/vim": "9.1"}, nil
	})
	if !status["answered"] || status["failed"] {
		t.Errorf("refresh status = %v, want answered=true failed=false", status)
	}
	if got := pc.pkgs["answered"]["app-editors/vim"]; got != "9.1" {
		t.Errorf("successful overlay result = %q, want 9.1", got)
	}
}

func TestRenderPkgAvailability(t *testing.T) {
	renderers := []struct {
		name string
		fn   func([]string, pkgLookupAvailability) string
	}{
		{
			name: "plain",
			fn: func(main []string, availability pkgLookupAvailability) string {
				return renderPkg(i18n.LangZH, "vim", main, map[string][2]string{}, nil, availability)
			},
		},
		{
			name: "rich",
			fn: func(main []string, availability pkgLookupAvailability) string {
				return renderPkgRich(i18n.LangZH, "vim", main, map[string][2]string{}, nil, availability)
			},
		},
	}
	cases := []struct {
		name         string
		main         []string
		availability pkgLookupAvailability
		want         string
		notWant      string
	}{
		{
			name:         "complete miss",
			availability: pkgLookupAvailability{official: true, overlays: map[string]bool{"guru": true}},
			want:         "没找到匹配的包",
			notWant:      "暂时无法查询",
		},
		{
			name:         "lookup unavailable",
			availability: pkgLookupAvailability{overlays: map[string]bool{"guru": true}},
			want:         "目前无法确认是否有匹配的包",
			notWant:      "没找到匹配的包",
		},
		{
			name:         "partial hit",
			main:         []string{"app-editors/vim"},
			availability: pkgLookupAvailability{official: true, overlays: map[string]bool{"guru": false}},
			want:         "以上结果可能不完整",
		},
	}
	for _, renderer := range renderers {
		for _, tc := range cases {
			t.Run(renderer.name+"/"+tc.name, func(t *testing.T) {
				got := renderer.fn(tc.main, tc.availability)
				if !strings.Contains(got, tc.want) {
					t.Errorf("rendered result %q does not contain %q", got, tc.want)
				}
				if tc.notWant != "" && strings.Contains(got, tc.notWant) {
					t.Errorf("rendered result %q unexpectedly contains %q", got, tc.notWant)
				}
			})
		}
	}
}
