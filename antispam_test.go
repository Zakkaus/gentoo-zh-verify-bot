package main

import "testing"

// TestParseChannelID covers /bc id parsing: it accepts both the Bot API form (-1001234567890) and
// the bare internal form (1234567890 -> -1001234567890), tolerates surrounding whitespace, and
// rejects junk / int64 overflow.
func TestParseChannelID(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"-1001234567890", -1001234567890, true}, // full Bot API form, used as-is
		{"1234567890", -1001234567890, true},     // bare internal id -> prepend -100
		{" 1234567890 ", -1001234567890, true},   // surrounding whitespace tolerated
		{"-100123456789", -100123456789, true},   // shorter full form, as-is
		{"123456789", -100123456789, true},       // shorter bare id
		{"", 0, false},                           // empty
		{"abc", 0, false},                        // non-numeric
		{"-100abc", 0, false},                    // partly numeric
		{"99999999999999999999", 0, false},       // overflows int64
	} {
		got, ok := parseChannelID(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseChannelID(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
