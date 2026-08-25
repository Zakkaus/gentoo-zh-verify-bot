package main

import (
	"os"
	"testing"
)

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

	// a settings.json missing the field ({}) must NOT pause verification — keep the seeded default.
	emptyPath := t.TempDir() + "/settings.json"
	if err := os.WriteFile(emptyPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	vd := &Verifier{settingsPath: emptyPath, enabled: true}
	vd.loadSettings()
	if !vd.isEnabled() {
		t.Error("a settings.json without the enabled field must keep the seeded default (enabled), not pause verification")
	}
}

func TestSettingsPersistSpoiler(t *testing.T) {
	path := t.TempDir() + "/settings.json"

	v := &Verifier{settingsPath: path, enabled: true, nameSpoiler: true}
	v.toggleNameSpoiler() // /spoiler off -> persists name_spoiler=false (+ enabled=true)
	v2 := &Verifier{settingsPath: path, enabled: true, nameSpoiler: true}
	v2.loadSettings()
	if v2.nameSpoilerOn() {
		t.Error("a persisted /spoiler off must survive reload")
	}
	if !v2.isEnabled() {
		t.Error("enabled should round-trip as true alongside name_spoiler")
	}

	// a settings.json with only {"enabled":false} must keep the seeded spoiler default (ON).
	p2 := t.TempDir() + "/settings.json"
	if err := os.WriteFile(p2, []byte(`{"enabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	vd := &Verifier{settingsPath: p2, enabled: true, nameSpoiler: true}
	vd.loadSettings()
	if !vd.nameSpoilerOn() {
		t.Error("a settings.json missing name_spoiler must keep the seeded default (spoiler ON)")
	}
	if vd.isEnabled() {
		t.Error("enabled=false from the file should load")
	}
}

func TestSettingsPersistVerifyMode(t *testing.T) {
	path := t.TempDir() + "/settings.json"

	v := &Verifier{settingsPath: path, enabled: true}
	v.setVerifyMode(modeQuiz)
	v2 := &Verifier{settingsPath: path, enabled: true}
	v2.loadSettings()
	if got := v2.verifyModeOverride(); got != modeQuiz {
		t.Errorf("persisted /vmode = %q, want %q", got, modeQuiz)
	}

	v2.setVerifyMode("") // /vmode auto -> no override
	v3 := &Verifier{settingsPath: path, enabled: true}
	v3.loadSettings()
	if got := v3.verifyModeOverride(); got != "" {
		t.Errorf("cleared /vmode should reload as no override, got %q", got)
	}

	p2 := t.TempDir() + "/settings.json"
	if err := os.WriteFile(p2, []byte(`{"verify_mode":"buttons"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	vb := &Verifier{settingsPath: p2, enabled: true}
	vb.loadSettings()
	if got := vb.verifyModeOverride(); got != "" {
		t.Errorf("an unknown persisted mode must be ignored, got %q", got)
	}
}
