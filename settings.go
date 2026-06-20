package main

import (
	"encoding/json"
	"log"
	"os"
)

// settingsState persists the runtime toggles operators expect to survive a service restart.
// Currently just the verification enabled/paused flag (/start, /stop): a /stop during maintenance
// should not be silently undone by a restart. The other runtime toggles (/rich, /autodel,
// /bantime) intentionally reset to their config defaults on restart (documented in the README
// persistence matrix); add them here if they ever need to persist too.
// Enabled is a *bool so a settings.json that is missing the field (e.g. a hand-written {}) keeps
// the seeded default rather than silently unmarshalling to false and pausing verification.
type settingsState struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// loadSettings overrides the NewVerifier-seeded runtime toggles with settings.json when present
// (so a persisted /stop survives restart). A missing file leaves the config/default seed in place.
func (v *Verifier) loadSettings() {
	if v.settingsPath == "" {
		return
	}
	data, err := os.ReadFile(v.settingsPath)
	if err != nil {
		return // no file yet => keep the seeded default (enabled = true)
	}
	var st settingsState
	if err := json.Unmarshal(data, &st); err != nil {
		log.Printf("settings load %s: %v", v.settingsPath, err)
		return
	}
	if st.Enabled != nil { // only override the seeded default when the field is actually present
		v.mu.Lock()
		v.enabled = *st.Enabled
		v.mu.Unlock()
	}
}

// saveSettings persists the current runtime toggles. A no-op when STATE_DIRECTORY is unset.
func (v *Verifier) saveSettings() {
	if v.settingsPath == "" {
		return
	}
	v.mu.Lock()
	en := v.enabled
	v.mu.Unlock()
	writeJSONFile(v.settingsPath, settingsState{Enabled: &en})
}
