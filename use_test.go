package main

import (
	"strings"
	"testing"
)

// TestWriteExpandFlags covers the compact USE_EXPAND rendering: a group header with its value
// count, the + default marker, and truncation past expandCap with a "…(共 N)" tail.
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

// TestRenderUseIncludesExpand checks the plain /use render surfaces use_expand groups, and that a
// package carrying only use_expand flags is not mislabelled as having no USE flags.
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

// TestRenderUseRichIncludesExpand covers the rich /use path (writeExpandFlagsRich): USE_EXPAND
// groups go in a collapsible <details> block with the default marker and full descriptions.
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
