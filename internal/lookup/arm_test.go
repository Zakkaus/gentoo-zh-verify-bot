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
	for _, l := range i18n.Languages() {
		for _, tc := range []struct {
			name      string
			atoms     []string
			available bool
			want      func() string
			notWant   func() string
			wantHTML  bool
		}{
			{
				name: "search unavailable",
				want: func() string {
					return i18n.Messages.LookupPackages.Arm.OfficialUnavailable.For(l)
				},
				notWant: func() string {
					return i18n.Messages.LookupPackages.Arm.NotFound.Render(l, "firefox")
				},
			},
			{
				name:      "answered miss",
				available: true,
				want: func() string {
					return i18n.Messages.LookupPackages.Arm.NotFound.Render(l, "firefox")
				},
				notWant: func() string {
					return i18n.Messages.LookupPackages.Arm.OfficialUnavailable.For(l)
				},
			},
			{
				name:      "package found",
				atoms:     []string{"www-client/firefox"},
				available: true,
				want: func() string {
					return i18n.Messages.LookupPackages.Arm.StableOnly.Render(l, "140.12.0")
				},
				wantHTML: true,
			},
		} {
			t.Run(l.String()+"/"+tc.name, func(t *testing.T) {
				got, useHTML := lookupArm(context.Background(), l, "firefox", func(context.Context, string) ([]string, bool) { return tc.atoms, tc.available }, func(context.Context, string) (string, string, bool) { return "140.12.0", "", true })
				if useHTML != tc.wantHTML {
					t.Errorf("lookupArm() useHTML = %v, want %v", useHTML, tc.wantHTML)
				}
				want := tc.want()
				if !strings.Contains(got, want) {
					t.Errorf("lookupArm() = %q, want substring %q", got, want)
				}
				if tc.notWant != nil {
					notWant := tc.notWant()
					if strings.Contains(got, notWant) {
						t.Errorf("lookupArm() = %q, unwanted substring %q", got, notWant)
					}
				}
			})
		}
	}
}
