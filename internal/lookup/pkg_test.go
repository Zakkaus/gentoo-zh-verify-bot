package lookup

import (
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

func TestPkgKeywordLegend(t *testing.T) {
	renderers := []struct {
		name string
		fn   func() string
	}{
		{
			name: "plain",
			fn: func() string {
				return renderPkg(i18n.LangZH, "vim", []string{"app-editors/vim"}, map[string][2]string{"app-editors/vim": {"", "9.1"}}, nil, pkgLookupAvailability{official: true})
			},
		},
		{
			name: "rich",
			fn: func() string {
				return renderPkgRich(i18n.LangZH, "vim", []string{"app-editors/vim"}, map[string][2]string{"app-editors/vim": {"", "9.1"}}, nil, pkgLookupAvailability{official: true})
			},
		},
	}
	for _, tt := range renderers {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn()
			if !strings.Contains(got, pkgKeywordLegend) {
				t.Errorf("rendered package result %q does not contain legend %q", got, pkgKeywordLegend)
			}
			if strings.Contains(got, "~ 表示测试 keyword(~arch)") {
				t.Errorf("rendered package result retains the inaccurate keyword legend: %q", got)
			}
			if !strings.Contains(got, "~9.1") {
				t.Errorf("rendered package result does not mark the no-amd64-stable latest version: %q", got)
			}
		})
	}
}
