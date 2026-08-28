package config

import "testing"

// Posting as a channel is how this spam arrives, and the ban does nothing at all while privacy
// mode keeps those posts from the bot. A group that has to discover the setting is a group that
// stays unprotected: six of seven registered groups had it off simply because nobody turned it on.
func TestBlockChannelSendersDefaultsToOn(t *testing.T) {
	on, off := true, false
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "unset", cfg: Config{}, want: true},
		{name: "explicitly on", cfg: Config{BlockChannelSenders: &on}, want: true},
		{name: "explicitly off is honoured", cfg: Config{BlockChannelSenders: &off}, want: false},
	}
	for _, tt := range tests {
		if got := tt.cfg.BlockChannelSendersEnabled(); got != tt.want {
			t.Errorf("%s: BlockChannelSendersEnabled = %v, want %v", tt.name, got, tt.want)
		}
	}
}
