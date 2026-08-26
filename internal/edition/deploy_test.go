package edition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The unit file, the installer, and the binary all have to agree on this build's name. Nothing
// links them at compile time, so this test does.
func TestUnitFileMatchesEditionName(t *testing.T) {
	path := filepath.Join("..", "..", "deploy", Name+".service")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("this build is %q but %s is missing: %v", Name, path, err)
	}
	unit := string(data)
	for _, want := range []string{
		"ExecStart=/usr/bin/env -- /usr/local/bin/" + Name + " ",
		"--config /etc/" + Name + "/config.json",
		"EnvironmentFile=/etc/" + Name + "/bot.env",
		"StateDirectory=" + Name + "\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("%s does not contain %q", path, want)
		}
	}
	// The other edition's name must not leak into this unit.
	other := "gentoo-zhbot"
	if !IsGentoo {
		other = "gentoo-zh-verify-bot"
	}
	if strings.Contains(unit, other) {
		t.Errorf("%s mentions the other edition (%s)", path, other)
	}
}

func TestInstallerOffersThisEdition(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name="+Name) {
		t.Errorf("deploy/install.sh cannot install %q", Name)
	}
}
