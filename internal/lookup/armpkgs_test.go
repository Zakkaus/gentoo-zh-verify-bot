package lookup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

func TestParseMadison(t *testing.T) {
	body := `htop | 3.0.5-7 | bullseye | arm64
htop | 3.2.2-2 | bookworm | arm64
htop | 3.4.1-5 | trixie | arm64
htop | 3.4.1-5+b1 | bookworm-backports | arm64
htop | 3.5.1-3 | sid | arm64
some garbage line without pipes
htop | 2.0.1-1 | xenial/universe | arm64`

	got := parseMadison(body)
	// expect base suites in first-seen order, pockets dropped, /universe stripped
	want := []madEntry{
		{"bullseye", "3.0.5-7"},
		{"bookworm", "3.2.2-2"},
		{"trixie", "3.4.1-5"},
		{"sid", "3.5.1-3"},
		{"xenial", "2.0.1-1"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %v, want %v", i, got[i], want[i])
		}
	}

	// a newer line for an already-seen suite updates its version in place
	dup := parseMadison("p | 1.0 | sid | arm64\np | 2.0 | sid | arm64")
	if len(dup) != 1 || dup[0].ver != "2.0" {
		t.Errorf("dedupe-keep-newest failed: %v", dup)
	}

	// no arm64 lines -> empty
	if e := parseMadison(""); len(e) != 0 {
		t.Errorf("empty body should yield no entries, got %v", e)
	}
}

func TestPickMadison(t *testing.T) {
	dev := func(s string) bool { return s == "stonking" } // the only unreleased dev series here

	// firefox-like Ubuntu: newest suite is the unreleased "stonking" -> skip to released "resolute".
	s, v, d := pickMadison([]madEntry{
		{"jammy", "110"}, {"noble", "120"},
		{"resolute", "1:1snap1-0ubuntu10"}, {"stonking", "1:1snap1-0ubuntu10"},
	}, dev)
	if s != "resolute" || d {
		t.Errorf("pickMadison should pick released 'resolute', got %q dev=%v", s, d)
	}
	if displayVer(v) != "snap" {
		t.Errorf("a Snap transitional must display as snap, got %q", displayVer(v))
	}

	// nil devSuite (Debian): keep the newest suite (sid), unflagged.
	if s2, _, d2 := pickMadison([]madEntry{{"trixie", "1"}, {"sid", "2"}}, nil); s2 != "sid" || d2 {
		t.Errorf("nil devSuite should keep the newest suite, got %q dev=%v", s2, d2)
	}

	// only a dev series ships it -> fall back to it, flagged dev.
	if s3, _, d3 := pickMadison([]madEntry{{"stonking", "9"}}, dev); s3 != "stonking" || !d3 {
		t.Errorf("all-dev should fall back to newest flagged dev, got %q dev=%v", s3, d3)
	}
}

func TestAurArchLabel(t *testing.T) {
	for _, c := range []struct{ pkgbuild, wantSub string }{
		{"pkgname=x\narch=('any')\n", "any"},
		{"arch=('i686' 'x86_64' 'aarch64' 'armv7h')", "aarch64"},
		{"arch=(x86_64 aarch64)", "aarch64"},
		{"arch=('armv7h' 'armv6h')", "32"},  // 32-bit ARM only, no aarch64
		{"arch=('x86_64')", "x86"},          // x86-only
		{"pkgname=x\nno arch here", "无法解析"}, // missing arch=()
	} {
		if got := aurArchLabel(i18n.LangZH, c.pkgbuild); !strings.Contains(got, c.wantSub) {
			t.Errorf("aurArchLabel(%q) = %q, want substring %q", c.pkgbuild, got, c.wantSub)
		}
	}
}

func TestFedoraArmStatusAvailability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		err     error
		want    string
		notWant string
	}{
		{name: "found", version: "3.4.1-2.fc44", want: "rawhide 3.4.1-2.fc44"},
		{name: "404", err: &httpStatusError{url: "u", code: 404}, want: "不在 Fedora"},
		{name: "429", err: &httpStatusError{url: "u", code: 429}, want: "查询失败", notWant: "不在 Fedora"},
		{name: "503", err: &httpStatusError{url: "u", code: 503}, want: "查询失败", notWant: "不在 Fedora"},
		{name: "network", err: errors.New("connection reset"), want: "查询失败", notWant: "不在 Fedora"},
		{name: "busy", err: &httpBusyError{url: "u", wait: time.Millisecond}, want: "查询失败", notWant: "不在 Fedora"},
		{name: "oversized", err: &httpBodyTooLargeError{url: "u", limit: 3}, want: "查询失败", notWant: "不在 Fedora"},
		{name: "missing version", want: "查询失败", notWant: "不在 Fedora"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fedoraArmStatusWith(context.Background(), i18n.LangZH, "htop", func(context.Context, string) (string, error) { return tc.version, tc.err })
			if !strings.Contains(got, tc.want) {
				t.Errorf("fedoraArmStatusWith() = %q, want substring %q", got, tc.want)
			}
			if tc.notWant != "" && strings.Contains(got, tc.notWant) {
				t.Errorf("fedoraArmStatusWith() = %q, unwanted substring %q", got, tc.notWant)
			}
		})
	}
}

func TestGentooArmStatusAvailability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		atoms     []string
		available bool
		want      string
		notWant   string
	}{
		{name: "search unavailable", want: "查询失败", notWant: "不在官方树"},
		{name: "answered miss", available: true, want: "不在官方树", notWant: "查询失败"},
		{name: "found", atoms: []string{"sys-process/htop"}, available: true, want: "稳定 3.4.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := gentooArmStatusWith(context.Background(), i18n.LangZH, "htop", func(context.Context, string) ([]string, bool) { return tc.atoms, tc.available }, func(context.Context, string) (string, string, bool) { return "3.4.1", "", true })
			if !strings.Contains(got, tc.want) {
				t.Errorf("gentooArmStatusWith() = %q, want substring %q", got, tc.want)
			}
			if tc.notWant != "" && strings.Contains(got, tc.notWant) {
				t.Errorf("gentooArmStatusWith() = %q, unwanted substring %q", got, tc.notWant)
			}
		})
	}
}
