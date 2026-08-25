package verify

import (
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

func TestLoadStateReadErrorDisablesWrites(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Service, string)
		load func(*Service)
		path func(*Service) string
	}{
		{name: "pending", set: func(v *Service, p string) { v.statePath = p }, load: func(v *Service) { v.load(nil) }, path: func(v *Service) string { return v.statePath }},
		{name: "verify failures", set: func(v *Service, p string) { v.vfailPath = p }, load: func(v *Service) { v.loadVerifyFails() }, path: func(v *Service) string { return v.vfailPath }},
		{name: "heartbeat", set: func(v *Service, p string) { v.hbPath = p }, load: func(v *Service) { v.loadHeartbeat() }, path: func(v *Service) string { return v.hbPath }},
		{name: "agents", set: func(v *Service, p string) { v.agentPath = p }, load: func(v *Service) { v.loadAgents() }, path: func(v *Service) string { return v.agentPath }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unreadable := t.TempDir()
			v := newTestService(&config.Config{})
			tt.set(v, unreadable)
			tt.load(v)
			if got := tt.path(v); got != "" {
				t.Errorf("write path remains %q after read failure; want disabled", got)
			}
		})
	}
}
