package i18n

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var (
	textType       = reflect.TypeFor[Text]()
	formatType     = reflect.TypeFor[Format]()
	stringListType = reflect.TypeFor[StringList]()
)

func TestFromTelegram(t *testing.T) {
	tests := map[string]Lang{
		"zh-hans": LangZH,
		"zh-CN":   LangZH,
		"zh":      LangZH,
		"zh-sg":   LangZH,
		"zh-hant": LangZHHant,
		"zh-TW":   LangZHHant,
		"zh-hk":   LangZHHant,
		"zh-MO":   LangZHHant,
		"yue":     LangZHHant,
		"en":      LangEN,
		"en-US":   LangEN,
		"ru":      LangEN,
		"ja":      LangEN,
		"":        LangEN,
	}
	for tag, want := range tests {
		if got := FromTelegram(tag); got != want {
			t.Errorf("FromTelegram(%q) = %s, want %s", tag, got, want)
		}
	}
}

func TestFromRequester(t *testing.T) {
	for code, want := range map[string]Lang{
		"en":      LangEN,
		"en-US":   LangEN,
		"zh-CN":   LangZH,
		"zh-Hant": LangZHHant,
		"yue-HK":  LangZHHant,
		"":        LangZHHant,
		"fr":      LangZHHant,
	} {
		if got := FromRequester(code, LangZHHant); got != want {
			t.Errorf("FromRequester(%q, zh-Hant) = %v, want %v", code, got, want)
		}
	}
}

func TestFromStored(t *testing.T) {
	tests := map[string]Lang{
		"":        LangZH,
		"zh":      LangZH,
		"zh-hans": LangZH,
		"zh-Hant": LangZHHant,
		"zh-hant": LangZHHant,
		"en":      LangEN,
		"unknown": LangZH,
	}
	for tag, want := range tests {
		if got := FromStored(tag); got != want {
			t.Errorf("FromStored(%q) = %s, want %s", tag, got, want)
		}
	}
}

func TestLangString(t *testing.T) {
	tests := map[Lang]string{
		LangZH:     "zh",
		LangZHHant: "zh-Hant",
		LangEN:     "en",
	}
	for language, want := range tests {
		if got := language.String(); got != want {
			t.Errorf("Lang(%d).String() = %q, want %q", language, got, want)
		}
	}
}

func TestCatalogComplete(t *testing.T) {
	visitCatalog(reflect.ValueOf(Messages), "Messages", func(path string, value reflect.Value) {
		switch value.Type() {
		case textType, formatType:
			for locale, text := range localizedValues(value) {
				location := catalogEntry(path, locale)
				if text == "" {
					t.Errorf("%s is empty", location)
				}
				if value.Type() == textType && looksFormatted(text) {
					t.Errorf("%s is Text but contains a format directive", location)
				}
			}
		case stringListType:
			for locale, entries := range localizedStringValues(value) {
				location := catalogEntry(path, locale)
				if len(entries) == 0 {
					t.Errorf("%s is empty", location)
				}
				for i, entry := range entries {
					if entry == "" {
						t.Errorf("%s[%d] is empty", location, i)
					}
				}
			}
		}
	})

	for i, question := range Messages.Verification.Challenge.FallbackQuestions {
		for _, locale := range Languages() {
			prompt, answers := question.For(locale)
			for _, answer := range answers {
				if strings.EqualFold(strings.TrimSpace(prompt), strings.TrimSpace(answer)) {
					path := fmt.Sprintf("Messages.Verification.Challenge.FallbackQuestions[%d].Prompt", i)
					t.Errorf("%s exposes its answer", catalogEntry(path, locale))
				}
			}
		}
	}
}

func TestSetupRecoveryCatalogNamesImplementedRestartAction(t *testing.T) {
	for _, definition := range localeDefinitions {
		data, err := localeFiles.ReadFile(localeFilePath(definition.tag, "moderate"))
		if err != nil {
			t.Fatal(err)
		}
		var raw struct {
			Setup map[string]string `json:"setup"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		recovery, ok := raw.Setup["restart"]
		if !ok || recovery == "" {
			t.Errorf("%s setup catalogue has no restart recovery action", definition.tag)
		}
		if _, stale := raw.Setup["recheck"]; stale {
			t.Errorf("%s setup catalogue still names the nonexistent recheck action", definition.tag)
		}
	}
}

func TestFormatPlaceholdersMatchLocales(t *testing.T) {
	visitCatalog(reflect.ValueOf(Messages), "Messages", func(path string, value reflect.Value) {
		if value.Type() != formatType {
			return
		}
		values := localizedValues(value)
		want, err := indexedPlaceholders(values[LangZH])
		if err != nil {
			t.Errorf("%s has an invalid format: %v", catalogEntry(path, LangZH), err)
			return
		}
		if len(want) == 0 {
			t.Errorf("%s is Format but has no placeholders", catalogEntry(path, LangZH))
		}
		for _, locale := range Languages() {
			if locale == LangZH {
				continue
			}
			got, err := indexedPlaceholders(values[locale])
			if err != nil {
				t.Errorf("%s has an invalid format: %v", catalogEntry(path, locale), err)
				continue
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s placeholders = %v, want %v", catalogEntry(path, locale), got, want)
			}
		}
	})
}

func visitCatalog(value reflect.Value, path string, visit func(string, reflect.Value)) {
	visit(path, value)
	if value.Type() == textType || value.Type() == formatType || value.Type() == stringListType {
		return
	}
	switch value.Kind() {
	case reflect.Struct:
		typ := value.Type()
		for i := range value.NumField() {
			visitCatalog(value.Field(i), path+"."+typ.Field(i).Name, visit)
		}
	case reflect.Array, reflect.Slice:
		for i := range value.Len() {
			visitCatalog(value.Index(i), fmt.Sprintf("%s[%d]", path, i), visit)
		}
	}
}

func catalogEntry(path string, locale Lang) string {
	parts := strings.Split(strings.TrimPrefix(path, "Messages."), ".")
	subsystem := fieldKey(parts[0])
	for i := 1; i < len(parts); i++ {
		parts[i-1] = catalogJSONSegment(parts[i])
	}
	key := strings.Join(parts[:len(parts)-1], ".")
	return fmt.Sprintf(
		"subsystem %s file %s key %s",
		subsystem,
		localeFilePath(locale.String(), subsystem),
		key,
	)
}

func catalogJSONSegment(segment string) string {
	if bracket := strings.IndexByte(segment, '['); bracket >= 0 {
		return fieldKey(segment[:bracket]) + segment[bracket:]
	}
	return fieldKey(segment)
}

func localizedValues(value reflect.Value) map[Lang]string {
	localized := value.Field(0)
	values := make(map[Lang]string, len(Languages()))
	for _, language := range Languages() {
		values[language] = localized.Index(int(language)).String()
	}
	return values
}

func localizedStringValues(value reflect.Value) map[Lang][]string {
	localized := value.Field(0)
	values := make(map[Lang][]string, len(Languages()))
	for _, language := range Languages() {
		values[language] = reflectedStrings(localized.Index(int(language)))
	}
	return values
}

func reflectedStrings(value reflect.Value) []string {
	items := make([]string, value.Len())
	for i := range value.Len() {
		items[i] = value.Index(i).String()
	}
	return items
}

func looksFormatted(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] != '%' || i+1 == len(value) {
			continue
		}
		next := value[i+1]
		if next == '%' {
			i++
			continue
		}
		if next == '[' || strings.ContainsRune("vTtbcdoOxXUeEfFgGspqx", rune(next)) {
			return true
		}
	}
	return false
}

func indexedPlaceholders(value string) ([]string, error) {
	var placeholders []string
	for i := 0; i < len(value); i++ {
		if value[i] != '%' {
			continue
		}
		if i+1 < len(value) && value[i+1] == '%' {
			i++
			continue
		}
		if i+1 >= len(value) || value[i+1] != '[' {
			return nil, fmt.Errorf("implicit directive at byte %d", i)
		}
		start := i + 2
		end := start
		for end < len(value) && value[end] >= '0' && value[end] <= '9' {
			end++
		}
		if end == start || end >= len(value) || value[end] != ']' {
			return nil, fmt.Errorf("invalid argument index at byte %d", i)
		}
		index, err := strconv.Atoi(value[start:end])
		if err != nil || index < 1 {
			return nil, fmt.Errorf("invalid argument index at byte %d", i)
		}
		verbPos := end + 1
		if verbPos >= len(value) || !strings.ContainsRune("vTtbcdoOxXUeEfFgGspqx", rune(value[verbPos])) {
			return nil, fmt.Errorf("unsupported directive at byte %d", i)
		}
		placeholders = append(placeholders, fmt.Sprintf("%d:%c", index, value[verbPos]))
		i = verbPos
	}
	sort.Strings(placeholders)
	return placeholders, nil
}
