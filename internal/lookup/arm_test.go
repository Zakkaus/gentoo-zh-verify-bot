package lookup

import (
	"context"
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

func TestArm64Keywords(t *testing.T) {
	// firefox-like: a newer testing version above an older stable one.
	stable, testing := arm64Keywords([]pkgVersionJSON{
		{Version: "9999", Keywords: []string{"arm64"}}, // live ebuild — must be skipped
		{Version: "152.0", Keywords: []string{"~amd64", "~arm64", "~x86"}},
		{Version: "140.12.0", Keywords: []string{"amd64", "arm64", "x86"}},
	})
	if stable != "140.12.0" || testing != "152.0" {
		t.Errorf("got (stable=%q testing=%q), want (140.12.0, 152.0)", stable, testing)
	}

	// not keyworded on arm64 at all (e.g. an amd64/x86-only package).
	if s, tt := arm64Keywords([]pkgVersionJSON{
		{Version: "1.0", Keywords: []string{"amd64", "x86"}},
	}); s != "" || tt != "" {
		t.Errorf("non-arm package: got (stable=%q testing=%q), want both empty", s, tt)
	}

	// testing only (no stable arm64).
	if s, tt := arm64Keywords([]pkgVersionJSON{
		{Version: "2.0", Keywords: []string{"~arm64"}},
	}); s != "" || tt != "2.0" {
		t.Errorf("testing-only: got (stable=%q testing=%q), want (\"\", 2.0)", s, tt)
	}
}

func TestLookupArmAvailability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		atoms     []string
		available bool
		want      string
		notWant   string
		wantHTML  bool
	}{
		{
			name:    "search unavailable",
			want:    "暂时无法查询 Gentoo 官方树",
			notWant: "没找到",
		},
		{
			name:      "answered miss",
			available: true,
			want:      "官方树里没找到",
			notWant:   "暂时无法查询",
		},
		{
			name:      "package found",
			atoms:     []string{"www-client/firefox"},
			available: true,
			want:      "稳定(arm64):140.12.0",
			wantHTML:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, useHTML := lookupArm(context.Background(), i18n.LangZH, "firefox", func(context.Context, string) ([]string, bool) { return tc.atoms, tc.available }, func(context.Context, string) (string, string, bool) { return "140.12.0", "", true })
			if useHTML != tc.wantHTML {
				t.Errorf("lookupArm() useHTML = %v, want %v", useHTML, tc.wantHTML)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("lookupArm() = %q, want substring %q", got, tc.want)
			}
			if tc.notWant != "" && strings.Contains(got, tc.notWant) {
				t.Errorf("lookupArm() = %q, unwanted substring %q", got, tc.notWant)
			}
		})
	}
}
