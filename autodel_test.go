package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

const testLookupGroup int64 = -1

func newTestVerifier(ttl *int) *Verifier {
	return NewVerifier(&config.Config{GroupIDs: []int64{testLookupGroup},
		Questions:        []config.Question{{Q: "x", Options: []string{"a", "b"}, Answer: 0}},
		LookupTTLSeconds: ttl})
}

func TestLookupAutoDelete(t *testing.T) {
	// default: unset => enabled, 3 minutes
	if ttl, on := newTestVerifier(nil).lookupAutoDelete(testLookupGroup); !on || ttl != 3*time.Minute {
		t.Errorf("default = (%v, %v), want (3m, true)", ttl, on)
	}
	// config 0 => disabled
	zero := 0
	disabled := newTestVerifier(&zero)
	if _, on := disabled.lookupAutoDelete(testLookupGroup); on {
		t.Errorf("lookup_ttl_seconds=0 should disable")
	}
	if err := disabled.setLookupAutoDelete(testLookupGroup, 0, true); err != nil {
		t.Fatal(err)
	}
	if ttl, on := disabled.lookupAutoDelete(testLookupGroup); !on || ttl != 3*time.Minute {
		t.Errorf("re-enabled zero baseline = (%v, %v), want (3m, true)", ttl, on)
	}
	// config positive => enabled with that duration
	seconds := 600
	if ttl, on := newTestVerifier(&seconds).lookupAutoDelete(testLookupGroup); !on || ttl != 10*time.Minute {
		t.Errorf("lookup_ttl_seconds=600 = (%v, %v), want (10m, true)", ttl, on)
	}
	// runtime: set minutes, then disable — the TTL must persist for a later re-enable
	v := newTestVerifier(nil)
	if err := v.setLookupAutoDelete(testLookupGroup, 5*time.Minute, true); err != nil {
		t.Fatal(err)
	}
	if err := v.setLookupAutoDelete(testLookupGroup, 0, false); err != nil {
		t.Fatal(err)
	}
	if ttl, on := v.lookupAutoDelete(testLookupGroup); on || ttl != 5*time.Minute {
		t.Errorf("after off = (%v, %v), want (5m, false)", ttl, on)
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
	for _, c := range cases {
		if a, ttl := parseAutoDelArg(c.arg); a != c.action || ttl != c.ttl {
			t.Errorf("parseAutoDelArg(%q) = (%q,%v), want (%q,%v)", c.arg, a, ttl, c.action, c.ttl)
		}
	}
}

func TestDistroAliasVisible(t *testing.T) {
	var description string
	for _, command := range memberCommands() {
		if command.Command == "distro" {
			description = command.Description
			break
		}
	}
	if !strings.Contains(description, "/pkgs") {
		t.Errorf("/distro menu description = %q, want an explicit /pkgs alias", description)
	}
	const helpLine = "/distro <包名> — /pkgs 的别名"
	if !strings.Contains(memberHelpText(), helpLine) {
		t.Errorf("member help is missing %q", helpLine)
	}
}
