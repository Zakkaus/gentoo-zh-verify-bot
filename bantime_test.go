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

// TestMuteDuration verifies the /mute default (1h, from config) and that an inline duration
// parses; mute is always timed, so 0/permanent is rejected by /mute (parseBanDuration -> 0).
func TestMuteDuration(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, map[string]any{"group_ids": []int{-100}, "questions": sampleQ}))
	if err != nil {
		t.Fatal(err)
	}
	if c.MuteSeconds != 3600 {
		t.Errorf("default MuteSeconds = %d, want 3600 (1h)", c.MuteSeconds)
	}
	// /mute uses cfg.MuteSeconds by default; an inline duration overrides it.
	if secs, ok := parseBanDuration("30m"); !ok || secs != 1800 {
		t.Errorf("inline /mute 30m parse = (%d,%v), want (1800,true)", secs, ok)
	}
	if secs, _ := parseBanDuration("0"); secs != 0 { // 0 => permanent, which /mute rejects (always timed)
		t.Errorf("parseBanDuration(0) = %d, want 0 (so /mute rejects it)", secs)
	}
}
