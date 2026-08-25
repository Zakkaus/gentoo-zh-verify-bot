package lookup

import (
	"context"
	"strings"
	"testing"
)

func TestWriteExpandFlags(t *testing.T) {
	many := make([]useFlag, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, useFlag{name: "lang" + string(rune('a'+i))})
	}
	groups := []useExpandGroup{
		{name: "llvm_slot", flags: []useFlag{{name: "20"}, {name: "21", def: true}, {name: "22"}}},
		{name: "l10n", flags: many},
	}
	var b strings.Builder
	writeExpandFlags(&b, groups)
	out := b.String()

	if !strings.Contains(out, "<b>LLVM_SLOT</b>(3):") {
		t.Errorf("missing uppercased llvm_slot header with count: %q", out)
	}
	if !strings.Contains(out, "+21") {
		t.Errorf("a default value must be marked +21: %q", out)
	}
	if !strings.Contains(out, "<b>L10N</b>(20):") {
		t.Errorf("missing l10n header with full count 20: %q", out)
	}
	if !strings.Contains(out, "…(共 20)") {
		t.Errorf("a group past expandCap must truncate with a tail: %q", out)
	}
	if n := strings.Count(out, "lang"); n != expandCap {
		t.Errorf("l10n should render exactly expandCap=%d values, got %d", expandCap, n)
	}
}

func TestRenderUseIncludesExpand(t *testing.T) {
	info := pkgFullInfo{
		atom:   "www-client/firefox",
		expand: []useExpandGroup{{name: "l10n", flags: []useFlag{{name: "zh-CN"}, {name: "en", def: true}}}},
	}
	out := renderUse(info, "", "", false, nil)
	if !strings.Contains(out, "L10N") {
		t.Errorf("renderUse should include the L10N use_expand group: %q", out)
	}
	if strings.Contains(out, "该包无 USE 标志") {
		t.Error("a package with use_expand must not be reported as having no USE flags")
	}
}

func TestRenderUseRichIncludesExpand(t *testing.T) {
	info := pkgFullInfo{
		atom:   "www-client/firefox",
		expand: []useExpandGroup{{name: "llvm_slot", flags: []useFlag{{name: "20"}, {name: "21", desc: "Use LLVM 21.", def: true}}}},
	}
	out := renderUseRich(info, "", "https://packages.gentoo.org/packages/www-client/firefox", false, nil)
	if !strings.Contains(out, "<details>") || !strings.Contains(out, "LLVM_SLOT") {
		t.Errorf("renderUseRich should put USE_EXPAND in a <details> block, got %q", out)
	}
	if !strings.Contains(out, "+21") || !strings.Contains(out, "Use LLVM 21.") {
		t.Errorf("rich USE_EXPAND should show the default marker + description, got %q", out)
	}
}

func TestResolveUseSourcesAvailability(t *testing.T) {
	for _, tc := range []struct {
		name        string
		query       string
		atoms       []string
		found       bool
		officialOK  bool
		wantSources int
		unavailable bool
	}{
		{name: "bare outage", query: "vim", unavailable: true},
		{name: "bare answered miss", query: "vim", officialOK: true},
		{name: "bare found", query: "vim", atoms: []string{"app-editors/vim"}, officialOK: true, wantSources: 1},
		{name: "exact outage", query: "app-editors/vim", unavailable: true},
		{name: "exact 404", query: "app-editors/vim", officialOK: true},
		{name: "exact found", query: "app-editors/vim", found: true, officialOK: true, wantSources: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srcs, availability := resolveUseSourcesWith(
				context.Background(),
				tc.query,
				map[string]bool{"guru": true},
				func(context.Context, string) (pkgFullInfo, bool, bool) {
					return pkgFullInfo{}, tc.found, tc.officialOK
				},
				func(context.Context, string) ([]string, bool) {
					return tc.atoms, tc.officialOK
				},
			)
			if len(srcs) != tc.wantSources {
				t.Errorf("resolveUseSourcesWith() returned %d sources, want %d", len(srcs), tc.wantSources)
			}
			if got := availability.anyUnavailable(); got != tc.unavailable {
				t.Errorf("availability.anyUnavailable() = %v, want %v", got, tc.unavailable)
			}
		})
	}
}

func TestRenderUseLookupMiss(t *testing.T) {
	for _, tc := range []struct {
		name         string
		availability pkgLookupAvailability
		want         string
		notWant      string
	}{
		{
			name:         "answered miss",
			availability: pkgLookupAvailability{official: true, overlays: map[string]bool{"guru": true}},
			want:         "没找到精确匹配",
			notWant:      "暂时无法查询",
		},
		{
			name:         "source unavailable",
			availability: pkgLookupAvailability{overlays: map[string]bool{"guru": true}},
			want:         "目前无法确认是否有精确匹配",
			notWant:      "没找到精确匹配",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderUseLookupMiss("vim", tc.availability)
			if !strings.Contains(got, tc.want) {
				t.Errorf("renderUseLookupMiss() = %q, want substring %q", got, tc.want)
			}
			if strings.Contains(got, tc.notWant) {
				t.Errorf("renderUseLookupMiss() = %q, unwanted substring %q", got, tc.notWant)
			}
		})
	}
}

func TestAppendUseAvailabilityNote(t *testing.T) {
	for _, tc := range []struct {
		name         string
		availability pkgLookupAvailability
		wantNote     bool
	}{
		{
			name:         "all answered",
			availability: pkgLookupAvailability{official: true, overlays: map[string]bool{"guru": true}},
		},
		{
			name:         "overlay failed",
			availability: pkgLookupAvailability{official: true, overlays: map[string]bool{"guru": false}},
			wantNote:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain, rich := appendUseAvailabilityNote("plain result", "<p>rich result</p>", tc.availability)
			for label, got := range map[string]string{"plain": plain, "rich": rich} {
				hasNote := strings.Contains(got, "结果可能不完整")
				if hasNote != tc.wantNote {
					t.Errorf("%s output %q contains partial note=%v, want %v", label, got, hasNote, tc.wantNote)
				}
			}
		})
	}
}
