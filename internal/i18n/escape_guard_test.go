package i18n

import (
	"encoding/json"
	"strings"
	"testing"
)

// A catalogue string reaches Telegram verbatim, so a literal backslash-n is printed as those two
// characters rather than breaking the line. This got into the Traditional Chinese verification
// prompt when a message was pasted in already-escaped, and no test caught it: the prompt still
// contained every word, just on one line. Compare the raw bytes instead.
func TestCatalogueHasNoLiteralEscapeSequences(t *testing.T) {
	// Rendering markup is fine; these are the escapes that only ever mean "the file was
	// double-encoded on its way in".
	bad := map[string]string{
		`\n`: "newline",
		`\t`: "tab",
		`\r`: "carriage return",
	}
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
				for seq, name := range bad {
					if strings.Contains(value, seq) {
						t.Errorf("%s/%s: %s contains a literal %s escape, which Telegram prints verbatim: %q",
							definition.tag, subsystem, path, name, value)
					}
				}
			})
		}
	}
}
