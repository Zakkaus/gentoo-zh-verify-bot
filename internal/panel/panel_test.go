package panel

import (
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

func TestGroupLanguageResolution(t *testing.T) {
	const groupID int64 = -100
	p := &Panel{cfg: &config.Config{
		Lang:     "en",
		Groups:   []config.GroupConfig{{ID: groupID, Lang: "zh-Hant"}},
		GroupIDs: []int64{groupID},
	}}
	if got := p.groupLanguage(groupID); got != i18n.LangZHHant {
		t.Fatalf("in-group language = %s, want zh-Hant", got)
	}
	if got := p.groupLanguage(-200); got != i18n.LangEN {
		t.Fatalf("global group fallback = %s, want en", got)
	}
}

func TestParseAutoDelArg(t *testing.T) {
	cases := []struct {
		arg, action string
		ttl         time.Duration
	}{
		{"", "show", 0}, {"off", "off", 0}, {"on", "on", 0},
		{"5", "set", 5 * time.Minute}, {"1", "set", time.Minute}, {"1440", "set", 1440 * time.Minute},
		{"0", "", 0}, {"1441", "", 0}, {"-3", "", 0}, {"abc", "", 0}, {"5x", "", 0},
	}
	for _, test := range cases {
		if action, ttl := parseAutoDelArg(test.arg); action != test.action || ttl != test.ttl {
			t.Errorf("parseAutoDelArg(%q) = (%q,%v), want (%q,%v)", test.arg, action, ttl, test.action, test.ttl)
		}
	}
}

func TestMemberHelpUsesCatalogue(t *testing.T) {
	for _, language := range i18n.Languages() {
		got := memberHelpText(language)
		want := i18n.Messages.Panel.Help.Member.For(language)
		if got != want {
			t.Errorf("member help for %s = %q, want catalogue text %q", language, got, want)
		}
		if !strings.Contains(got, "/distro") {
			t.Errorf("member help for %s is missing the /distro alias", language)
		}
	}
}
