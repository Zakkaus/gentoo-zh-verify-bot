package lookup

import (
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

func TestPkgKeywordLegend(t *testing.T) {
	renderers := []struct {
		name string
		fn   func(i18n.Lang) string
	}{
		{
			name: "plain",
			fn: func(l i18n.Lang) string {
				return renderPkg(l, "vim", []string{"app-editors/vim"}, map[string][2]string{"app-editors/vim": {"", "9.1"}}, nil, pkgLookupAvailability{official: true})
			},
		},
		{
			name: "rich",
			fn: func(l i18n.Lang) string {
				return renderPkgRich(l, "vim", []string{"app-editors/vim"}, map[string][2]string{"app-editors/vim": {"", "9.1"}}, nil, pkgLookupAvailability{official: true})
			},
		},
	}
	for _, l := range i18n.Languages() {
		for _, tt := range renderers {
			t.Run(l.String()+"/"+tt.name, func(t *testing.T) {
				got := tt.fn(l)
				legend := i18n.Messages.LookupPackages.Pkg.KeywordLegend.For(l)
				if !strings.Contains(got, legend) {
					t.Errorf("rendered package result %q does not contain legend %q", got, legend)
				}
				if !strings.Contains(got, "~9.1") {
					t.Errorf("rendered package result does not mark the no-amd64-stable latest version: %q", got)
				}
			})
		}
	}
}
