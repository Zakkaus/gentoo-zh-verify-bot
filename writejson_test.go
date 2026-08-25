package main

import (
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

func TestLoadStateReadErrorDisablesWrites(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Verifier, string)
		load func(*Verifier)
		path func(*Verifier) string
	}{
		{name: "pending", set: func(v *Verifier, p string) { v.statePath = p }, load: func(v *Verifier) { v.load(nil) }, path: func(v *Verifier) string { return v.statePath }},
		{name: "verify failures", set: func(v *Verifier, p string) { v.vfailPath = p }, load: func(v *Verifier) { v.loadVerifyFails() }, path: func(v *Verifier) string { return v.vfailPath }},
		{name: "heartbeat", set: func(v *Verifier, p string) { v.hbPath = p }, load: func(v *Verifier) { v.loadHeartbeat() }, path: func(v *Verifier) string { return v.hbPath }},
		{name: "agents", set: func(v *Verifier, p string) { v.agentPath = p }, load: func(v *Verifier) { v.loadAgents() }, path: func(v *Verifier) string { return v.agentPath }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unreadable := t.TempDir()
			v := NewVerifier(&config.Config{})
			tt.set(v, unreadable)
			tt.load(v)
			if got := tt.path(v); got != "" {
				t.Errorf("write path remains %q after read failure; want disabled", got)
			}
		})
	}
}
