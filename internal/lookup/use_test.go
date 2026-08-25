package lookup

import (
	"context"
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

func TestWriteExpandFlags(t *testing.T) {
	many := make([]useFlag, 0, 20)
	for i := range 20 {
		many = append(many, useFlag{name: "lang" + string(rune('a'+i))})
	}
	groups := []useExpandGroup{
		{name: "llvm_slot", flags: []useFlag{{name: "20"}, {name: "21", def: true}, {name: "22"}}},
		{name: "l10n", flags: many},
	}
	for _, l := range i18n.Languages() {
		var b strings.Builder
		writeExpandFlags(&b, l, groups)
		out := b.String()
		messages := i18n.Messages.LookupPackages.Use

		llvmHeader := "<b>LLVM_SLOT</b>" + messages.Count.Render(l, 3) + messages.ValueSeparator.For(l)
		if !strings.Contains(out, llvmHeader) {
			t.Errorf("missing uppercased llvm_slot header with count %q: %q", llvmHeader, out)
		}
		if !strings.Contains(out, "+21") {
			t.Errorf("a default value must be marked +21: %q", out)
		}
		l10nHeader := "<b>L10N</b>" + messages.Count.Render(l, 20) + messages.ValueSeparator.For(l)
		if !strings.Contains(out, l10nHeader) {
			t.Errorf("missing l10n header with full count %q: %q", l10nHeader, out)
		}
		truncatedCount := messages.TruncatedCount.Render(l, 20)
		if !strings.Contains(out, truncatedCount) {
			t.Errorf("a group past expandCap must truncate with tail %q: %q", truncatedCount, out)
		}
		if n := strings.Count(out, "lang"); n != expandCap {
			t.Errorf("l10n should render exactly expandCap=%d values, got %d", expandCap, n)
		}
	}
}

func TestRenderUseIncludesExpand(t *testing.T) {
	info := pkgFullInfo{
		atom:   "www-client/firefox",
		expand: []useExpandGroup{{name: "l10n", flags: []useFlag{{name: "zh-CN"}, {name: "en", def: true}}}},
	}
	out := renderUse(i18n.LangZH, info, "", "", false, nil)
	if !strings.Contains(out, "L10N") {
		t.Errorf("renderUse should include the L10N use_expand group: %q", out)
	}
	if strings.Contains(out, i18n.Messages.LookupPackages.Use.NoFlags.For(i18n.LangZH)) {
		t.Error("a package with use_expand must not be reported as having no USE flags")
	}
}

func TestRenderUseRichIncludesExpand(t *testing.T) {
	info := pkgFullInfo{
		atom:   "www-client/firefox",
		expand: []useExpandGroup{{name: "llvm_slot", flags: []useFlag{{name: "20"}, {name: "21", desc: "Use LLVM 21.", def: true}}}},
	}
	out := renderUseRich(i18n.LangZH, info, "", "https://packages.gentoo.org/packages/www-client/firefox", false, nil)
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
	for _, l := range i18n.Languages() {
		for _, tc := range []struct {
			name         string
			availability pkgLookupAvailability
			want         func(pkgLookupAvailability) string
			notWant      func(pkgLookupAvailability) string
		}{
			{
				name:         "answered miss",
				availability: pkgLookupAvailability{official: true, overlays: map[string]bool{"guru": true}},
				want: func(_ pkgLookupAvailability) string {
					return i18n.Messages.LookupPackages.Use.NotFound.Render(l, "vim")
				},
				notWant: func(availability pkgLookupAvailability) string {
					return i18n.Messages.LookupPackages.Use.Unavailable.Render(l, "vim", availability.unavailableSources(l))
				},
			},
			{
				name:         "source unavailable",
				availability: pkgLookupAvailability{overlays: map[string]bool{"guru": true}},
				want: func(availability pkgLookupAvailability) string {
					return i18n.Messages.LookupPackages.Use.Unavailable.Render(l, "vim", availability.unavailableSources(l))
				},
				notWant: func(_ pkgLookupAvailability) string {
					return i18n.Messages.LookupPackages.Use.NotFound.Render(l, "vim")
				},
			},
		} {
			t.Run(l.String()+"/"+tc.name, func(t *testing.T) {
				got := renderUseLookupMiss(l, "vim", tc.availability)
				want := tc.want(tc.availability)
				if got != want {
					t.Errorf("renderUseLookupMiss() = %q, want %q", got, want)
				}
				notWant := tc.notWant(tc.availability)
				if strings.Contains(got, notWant) {
					t.Errorf("renderUseLookupMiss() = %q, unwanted text %q", got, notWant)
				}
			})
		}
	}
}

func TestAppendUseAvailabilityNote(t *testing.T) {
	for _, l := range i18n.Languages() {
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
			t.Run(l.String()+"/"+tc.name, func(t *testing.T) {
				plain, rich := appendUseAvailabilityNote(l, "plain result", "<p>rich result</p>", tc.availability)
				note := i18n.Messages.LookupPackages.Source.PartialResults.Render(l, tc.availability.unavailableSources(l))
				for label, got := range map[string]string{"plain": plain, "rich": rich} {
					hasNote := strings.Contains(got, note)
					if hasNote != tc.wantNote {
						t.Errorf("%s output %q contains partial note=%v, want %v", label, got, hasNote, tc.wantNote)
					}
				}
			})
		}
	}
}
