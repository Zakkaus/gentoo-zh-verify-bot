package main

import "testing"

// TestSettingsRoundTrip verifies a persisted /stop (verification disabled) survives a reload — the
// point of settings.json (a maintenance pause shouldn't be undone by a restart) — that a later
// /start round-trips back, and that with no path save/load are no-ops while the in-memory flag
// still sets.
func TestSettingsRoundTrip(t *testing.T) {
	path := t.TempDir() + "/settings.json"

	v := &Verifier{settingsPath: path, enabled: true}
	v.setEnabled(false) // /stop -> persists enabled=false
	v2 := &Verifier{settingsPath: path, enabled: true}
	v2.loadSettings()
	if v2.isEnabled() {
		t.Error("a persisted /stop (enabled=false) must survive reload")
	}

	v2.setEnabled(true) // /start -> persists enabled=true
	v3 := &Verifier{settingsPath: path, enabled: false}
	v3.loadSettings()
	if !v3.isEnabled() {
		t.Error("a persisted /start (enabled=true) must survive reload")
	}

	vn := &Verifier{enabled: true} // no settingsPath: save/load are no-ops
	vn.setEnabled(false)
	if vn.isEnabled() {
		t.Error("setEnabled must still set the in-memory flag even without a persistence path")
	}
}
