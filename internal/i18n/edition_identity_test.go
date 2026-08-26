package i18n

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// Only two catalogue entries may name the Gentoo-zh Community, and both are selected by build:
// Who() picks the identity sentence, BuiltinFallback() picks the question bank. Any other
// string carrying the community's name would reach a group that is not this community.
var (
	communityClaim = regexp.MustCompile(`Gentoo-zh|Gentoo 中文社[区群]|gentoozh|Gentoo Chinese`)
	gentooOnlyKeys = []string{"challenge.fallback_questions", "direct_message.identity"}
)

func TestOnlyEditionSelectedTextNamesTheCommunity(t *testing.T) {
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
				if !communityClaim.MatchString(value) {
					return
				}
				for _, allowed := range gentooOnlyKeys {
					if strings.HasPrefix(path, allowed) {
						return
					}
				}
				t.Errorf("%s/%s: %s names the Gentoo-zh Community but is not selected by build: %q",
					definition.tag, subsystem, path, value)
			})
		}
	}
}

func walkStrings(node any, path string, visit func(path, value string)) {
	switch typed := node.(type) {
	case map[string]any:
		for key, child := range typed {
			next := key
			if path != "" {
				next = path + "." + key
			}
			walkStrings(child, next, visit)
		}
	case []any:
		for _, child := range typed {
			walkStrings(child, path, visit)
		}
	case string:
		visit(path, typed)
	}
}
