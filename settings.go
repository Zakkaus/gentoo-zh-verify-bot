package main

// settingsState persists /start, /stop, /spoiler, and /vmode across restarts.
// Other runtime toggles deliberately reset to config defaults.
// Pointer booleans distinguish a missing field from false; empty VerifyMode follows config.
type settingsState struct {
	Enabled     *bool  `json:"enabled,omitempty"`
	NameSpoiler *bool  `json:"name_spoiler,omitempty"`
	VerifyMode  string `json:"verify_mode,omitempty"`
}

// Missing fields retain NewVerifier's seeded defaults.
func (v *Verifier) loadSettings() {
	if v.settingsPath == "" {
		return
	}
	var st settingsState
	if err := loadJSONFile(v.settingsPath, &st); err != nil {
		if stateReadFailed(err) {
			v.settingsPath = ""
		}
		return // corrupt files were backed up; unreadable files keep defaults and disable writes
	}
	v.mu.Lock()
	if st.Enabled != nil { // only override the seeded default when the field is actually present
		v.enabled = *st.Enabled
	}
	if st.NameSpoiler != nil {
		v.nameSpoiler = *st.NameSpoiler
	}
	if validMode(st.VerifyMode) { // ignore an empty/garbage value: keep following the config
		v.vmode = st.VerifyMode
	}
	v.mu.Unlock()
}

func (v *Verifier) saveSettings() {
	if v.settingsPath == "" {
		return
	}
	saveJSONFile(v.settingsPath, func() any {
		v.mu.Lock()
		defer v.mu.Unlock()
		en := v.enabled
		sp := v.nameSpoiler
		return settingsState{Enabled: &en, NameSpoiler: &sp, VerifyMode: v.vmode}
	})
}
