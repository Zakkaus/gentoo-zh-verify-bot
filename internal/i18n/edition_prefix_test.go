package i18n

import (
	"encoding/json"
	"regexp"
	"testing"
)

// Every Gentoo lookup named anywhere in the catalogue must be written with the {g} token, so
// that the build decides whether it renders as /pkg or /gpkg. A bare name is not a style
// problem: it prints a command the generic build does not answer. The rendered-output test
// only catches a token left unsubstituted; this one catches a token that was never there.
var bareGentooCommand = regexp.MustCompile(`(?:^|[^\w./{])/(pkg|use|bug|news|bbs|arm)(?:[^a-z]|$)`)

func TestCatalogueNamesGentooCommandsWithTheEditionToken(t *testing.T) {
	for _, definition := range localeDefinitions {
		for _, subsystem := range []string{"bot", "verification", "panel", "moderate", "feed",
			"lookup_content", "lookup_distros", "lookup_packages"} {
			data, err := localeFiles.ReadFile(localeFilePath(definition.tag, subsystem))
			if err != nil {
				t.Fatalf("read %s/%s: %v", definition.tag, subsystem, err)
			}
			var tree any
			if err := json.Unmarshal(data, &tree); err != nil {
				t.Fatalf("parse %s/%s: %v", definition.tag, subsystem, err)
			}
			walkStrings(tree, "", func(path, value string) {
				for _, match := range bareGentooCommand.FindAllStringSubmatch(value, -1) {
					t.Errorf("%s/%s: %s writes /%s without the {g} token, so the generic build prints a command it does not answer: %q",
						definition.tag, subsystem, path, match[1], value)
				}
			})
		}
	}
}
