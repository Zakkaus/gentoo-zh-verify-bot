package main

// settingsState persists the runtime toggles operators expect to survive a service restart: the
// verification enabled/paused flag (/start, /stop) — a /stop during maintenance should not be
// silently undone by a restart — plus the name spoiler (/spoiler) and the challenge mode (/vmode).
// The other runtime toggles (/rich, /autodel, /bantime) intentionally reset to their config defaults
// on restart (documented in the README persistence matrix); add them here if they ever need to persist.
// Enabled is a *bool so a settings.json that is missing the field (e.g. a hand-written {}) keeps
// the seeded default rather than silently unmarshalling to false and pausing verification.
// NameSpoiler (/spoiler) persists the same way for the same reason — a missing field keeps the
// seeded default (spoiler ON).
// VerifyMode (/vmode) persists as a plain string: "" means no override (follow the config), so an
// absent field keeps the configured mode rather than forcing one.
type settingsState struct {
	Enabled     *bool  `json:"enabled,omitempty"`
	NameSpoiler *bool  `json:"name_spoiler,omitempty"`
	VerifyMode  string `json:"verify_mode,omitempty"`
}

// loadSettings overrides the NewVerifier-seeded runtime toggles with settings.json when present
// (so a persisted /stop survives restart). A missing file leaves the config/default seed in place.
func (v *Verifier) loadSettings() {
	if v.settingsPath == "" {
		return
	}
	var st settingsState
	if err := loadJSONFile(v.settingsPath, &st); err != nil {
		return // corrupt file backed up to .corrupt; keep the seeded default
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

// saveSettings persists the current runtime toggles. A no-op when STATE_DIRECTORY is unset.
func (v *Verifier) saveSettings() {
	if v.settingsPath == "" {
		return
	}
	v.mu.Lock()
	en := v.enabled
	sp := v.nameSpoiler
	vm := v.vmode
	v.mu.Unlock()
	writeJSONFile(v.settingsPath, settingsState{Enabled: &en, NameSpoiler: &sp, VerifyMode: vm})
}
