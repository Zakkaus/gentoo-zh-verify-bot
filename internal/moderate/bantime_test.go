package moderate

import (
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

func TestParseBanDuration(t *testing.T) {
	valid := map[string]int{
		"0": 0, "perm": 0, "permanent": 0,
		i18n.Messages.Moderate.Duration.PermanentInput.For(i18n.LangZH): 0,
		"30s": 30, "30m": 1800, "2h": 7200, "7d": 604800, "3600": 3600,
	}
	for input, want := range valid {
		if got, ok := parseBanDuration(input); !ok || got != want {
			t.Errorf("parseBanDuration(%q) = (%d,%v), want (%d,true)", input, got, ok, want)
		}
	}
	for input, want := range map[string]int{"10s": 30, "29s": 30, "5": 30, "30s": 30, "366d": 366 * 86400} {
		if got, ok := parseBanDuration(input); !ok || got != want {
			t.Errorf("clamp parseBanDuration(%q) = (%d,%v), want (%d,true)", input, got, ok, want)
		}
	}
	for _, input := range []string{"400d", "367d", "999999999"} {
		if got, ok := parseBanDuration(input); !ok || got != 0 {
			t.Errorf("over-366d parseBanDuration(%q) = (%d,%v), want (0,true)", input, got, ok)
		}
	}
	for _, input := range []string{"", "abc", "-5", "5x", "m", "1.5h"} {
		if _, ok := parseBanDuration(input); ok {
			t.Errorf("parseBanDuration(%q) should be invalid", input)
		}
	}
}

func TestBanDurationText(t *testing.T) {
	for seconds, want := range map[int]string{
		0:      i18n.Messages.Moderate.Duration.Permanent.For(i18n.LangZH),
		-1:     i18n.Messages.Moderate.Duration.Permanent.For(i18n.LangZH),
		604800: i18n.Messages.Moderate.Duration.Days.Render(i18n.LangZH, 7),
		43200:  i18n.Messages.Moderate.Duration.Hours.Render(i18n.LangZH, 12),
		1800:   i18n.Messages.Moderate.Duration.Minutes.Render(i18n.LangZH, 30),
		90:     i18n.Messages.Moderate.Duration.Seconds.Render(i18n.LangZH, 90),
	} {
		if got := banDurationText(i18n.LangZH, seconds); got != want {
			t.Errorf("banDurationText(%d) = %q, want %q", seconds, got, want)
		}
	}
}

func TestMuteDurationPolicy(t *testing.T) {
	if seconds, ok := parseBanDuration("30m"); !ok || seconds != 1800 {
		t.Errorf("inline /mute 30m parse = (%d,%v), want (1800,true)", seconds, ok)
	}
	if seconds, _ := parseBanDuration("0"); seconds != 0 {
		t.Errorf("parseBanDuration(0) = %d, want 0 so /mute rejects it", seconds)
	}
}
