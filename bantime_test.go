package main

import "testing"

func TestParseBanDuration(t *testing.T) {
	ok := map[string]int{
		"0": 0, "永久": 0, "perm": 0, "permanent": 0,
		"3600": 3600, "3600s": 3600, "30m": 1800, "12h": 43200, "7d": 604800, "1d": 86400,
	}
	for arg, want := range ok {
		if got, valid := parseBanDuration(arg); !valid || got != want {
			t.Errorf("parseBanDuration(%q) = (%d,%v), want (%d,true)", arg, got, valid, want)
		}
	}

	// Telegram window clamp: <30s -> 30, >366d -> permanent(0).
	for arg, want := range map[string]int{"10s": 30, "29s": 30, "5": 30, "30s": 30, "366d": 366 * 86400} {
		if got, valid := parseBanDuration(arg); !valid || got != want {
			t.Errorf("clamp parseBanDuration(%q) = (%d,%v), want (%d,true)", arg, got, valid, want)
		}
	}
	for _, arg := range []string{"400d", "367d", "999999999"} {
		if got, valid := parseBanDuration(arg); !valid || got != 0 {
			t.Errorf("over-366d parseBanDuration(%q) = (%d,%v), want (0,true=permanent)", arg, got, valid)
		}
	}
	for _, arg := range []string{"", "abc", "-5", "5x", "m", "1.5h"} {
		if _, valid := parseBanDuration(arg); valid {
			t.Errorf("parseBanDuration(%q) should be invalid", arg)
		}
	}
}

func TestBanDurationText(t *testing.T) {
	for secs, want := range map[int]string{0: "永久", -1: "永久", 604800: "7 天", 43200: "12 小时", 1800: "30 分钟", 90: "90 秒"} {
		if got := banDurationText(secs); got != want {
			t.Errorf("banDurationText(%d) = %q, want %q", secs, got, want)
		}
	}
}
